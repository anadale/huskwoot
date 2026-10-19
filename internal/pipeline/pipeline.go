package pipeline

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	localei18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

// Config holds all Pipeline parameters.
type Config struct {
	// OwnerIDs are the owner user identifiers whose promises are tracked.
	OwnerIDs []string
	// Aliases are additional owner identifiers (names, aliases).
	Aliases []string

	Tasks     model.TaskService
	AutoTasks model.AutoTaskService
	Projects  model.ProjectService
	Chat      model.ChatService

	Classifiers map[model.MessageKind]model.Classifier
	Extractors  map[model.MessageKind]model.Extractor
	Notifiers   []model.Notifier
	Logger      *slog.Logger

	// ForwardBatch processing dependencies; persistence is provided by AutoTasks.
	ForwardClassifier model.Classifier
	ForwardExtractor  model.Extractor
	ForwardBuffer     model.ForwardBuffer
	TaskStore         model.TaskStore
	Localizer         *goI18n.Localizer
	// Timezone is used to format forwarded message timestamps for the LLM.
	// Falls back to time.UTC when nil.
	Timezone *time.Location
}

// Pipeline connects classifiers, extractors, and outputs.
type Pipeline struct {
	classifiers map[model.MessageKind]model.Classifier
	extractors  map[model.MessageKind]model.Extractor
	notifiers   []model.Notifier
	tasks       model.TaskService
	autoTasks   model.AutoTaskService
	projects    model.ProjectService
	chat        model.ChatService
	ownerIDs    []string
	aliases     []string
	logger      *slog.Logger

	// ForwardBatch dependencies.
	forwardClassifier model.Classifier
	forwardExtractor  model.Extractor
	forwardBuffer     model.ForwardBuffer
	taskStore         model.TaskStore
	localizer         *goI18n.Localizer
	timezone          *time.Location
}

// New creates a new Pipeline with the given components and dependencies.
func New(cfg Config) *Pipeline {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Pipeline{
		classifiers:       cfg.Classifiers,
		extractors:        cfg.Extractors,
		notifiers:         cfg.Notifiers,
		tasks:             cfg.Tasks,
		autoTasks:         cfg.AutoTasks,
		projects:          cfg.Projects,
		chat:              cfg.Chat,
		ownerIDs:          cfg.OwnerIDs,
		aliases:           cfg.Aliases,
		logger:            logger,
		forwardClassifier: cfg.ForwardClassifier,
		forwardExtractor:  cfg.ForwardExtractor,
		forwardBuffer:     cfg.ForwardBuffer,
		taskStore:         cfg.TaskStore,
		localizer:         cfg.Localizer,
		timezone:          cfg.Timezone,
	}
}

func (p *Pipeline) isOwner(author string) bool {
	for _, id := range p.ownerIDs {
		if author == id {
			return true
		}
	}
	for _, alias := range p.aliases {
		if author == alias {
			return true
		}
	}
	return false
}

// Process handles a single message:
//   - ForwardBatch → processForwardBatch (owner-less, handled by payload).
//   - DM/GroupDirect → ChatService.HandleMessage (tool calling loop, reply via ReplyFn).
//   - Group/Batch → classifier → extractor → persistence + created-task notifiers.
func (p *Pipeline) Process(ctx context.Context, msg model.Message) error {
	// ForwardBatch carries its own auth context (DM chat_id from TelegramChannel).
	if msg.Kind == model.MessageKindForwardBatch {
		return p.processForwardBatch(ctx, msg)
	}

	if msg.Kind != model.MessageKindBatch && msg.Kind != model.MessageKindGroupDirect && !p.isOwner(msg.Author) {
		p.logger.DebugContext(ctx, "message skipped: author is not an owner",
			"author", msg.Author, "kind", msg.Kind)
		return nil
	}

	if msg.Kind == model.MessageKindDM || msg.Kind == model.MessageKindGroupDirect {
		return p.processChat(ctx, msg)
	}

	if msg.Kind == model.MessageKindGroup {
		if p.autoTasks == nil {
			return fmt.Errorf("processing group message: AutoTaskService is not configured")
		}
		processed, err := p.autoTasks.Processed(ctx, autoInputKey(msg.Source, msg.ID, msg.Text))
		if err != nil {
			return fmt.Errorf("checking group input receipt: %w", err)
		}
		if processed {
			return nil
		}
	}

	classifier := p.classifiers[msg.Kind]
	if classifier == nil {
		p.logger.DebugContext(ctx, "no classifier found for message kind",
			"kind", msg.Kind, "msg_id", msg.ID)
		return nil
	}

	class, err := classifier.Classify(ctx, msg)
	if err != nil {
		return fmt.Errorf("classifying message: %w", err)
	}

	switch class {
	case model.ClassSkip:
		p.logger.DebugContext(ctx, "message classified as skip", "msg_id", msg.ID)
		if msg.Kind == model.MessageKindGroup {
			_, err := p.autoTasks.Submit(ctx, model.AutoTaskSubmission{
				InputKeys:  []string{autoInputKey(msg.Source, msg.ID, msg.Text)},
				SourceText: msg.Text,
			})
			if err != nil {
				return fmt.Errorf("saving skipped group input: %w", err)
			}
		}
		return nil
	case model.ClassPromise:
		p.logger.InfoContext(ctx, "message classified as promise",
			"msg_id", msg.ID, "preview", textPreview(msg.Text, 80))
		p.reactPending(ctx, msg)
		created, err := p.processPromise(ctx, msg)
		if err != nil {
			return err
		}
		if created {
			p.reactDone(ctx, msg)
		} else {
			p.reactClear(ctx, msg)
		}
		return nil
	default:
		p.logger.WarnContext(ctx, "unknown classification", "class", class, "msg_id", msg.ID)
		return nil
	}
}

// processChat handles DM/GroupDirect via ChatService.
// On error — logs and returns nil (does not interrupt processing).
func (p *Pipeline) processChat(ctx context.Context, msg model.Message) error {
	if p.chat == nil {
		p.logger.WarnContext(ctx, "ChatService not configured, message ignored", "msg_id", msg.ID)
		return nil
	}

	// AI providers reject empty user content with HTTP 400, so short-circuit on
	// non-textual messages (sticker, media without caption, etc.) and reply with
	// a localized hint instead of calling the agent.
	if strings.TrimSpace(msg.Text) == "" {
		p.logger.DebugContext(ctx, "chat message has empty text, replying with hint",
			"msg_id", msg.ID, "kind", msg.Kind)
		if msg.ReplyFn != nil {
			if err := msg.ReplyFn(ctx, p.localizeMsg("chat.empty_message", nil)); err != nil {
				p.logger.WarnContext(ctx, "error sending empty-message reply", "msg_id", msg.ID, "error", err)
			}
		}
		return nil
	}

	p.reactPending(ctx, msg)

	reply, err := p.chat.HandleMessage(ctx, msg)
	if err != nil {
		p.logger.ErrorContext(ctx, "ChatService error", "msg_id", msg.ID, "error", err)
		if msg.ReplyFn != nil {
			if replyErr := msg.ReplyFn(ctx, "Failed to process request, please try again"); replyErr != nil {
				p.logger.WarnContext(ctx, "error sending error reply", "msg_id", msg.ID, "error", replyErr)
			}
		}
		p.reactDone(ctx, msg)
		return nil
	}

	if reply.Text != "" {
		if msg.ReplyFn != nil {
			if err := msg.ReplyFn(ctx, reply.Text); err != nil {
				p.logger.WarnContext(ctx, "error sending reply", "msg_id", msg.ID, "error", err)
			}
		}
	}
	p.reactDone(ctx, msg)
	return nil
}

// processPromise extracts tasks and submits Telegram candidates for durable decisions.
// Returns true only when tasks were actually created.
func (p *Pipeline) processPromise(ctx context.Context, msg model.Message) (bool, error) {
	extractor := p.extractors[msg.Kind]
	if extractor == nil {
		p.logger.WarnContext(ctx, "no extractor found for message kind",
			"kind", msg.Kind, "msg_id", msg.ID)
		return false, nil
	}

	var history []model.HistoryEntry
	if msg.HistoryFn != nil {
		var err error
		history, err = msg.HistoryFn(ctx)
		if err != nil {
			p.logger.WarnContext(ctx, "error fetching activity history", "error", err)
		}
	}

	extracted, err := extractor.Extract(ctx, msg, history)
	if err != nil {
		return false, fmt.Errorf("extracting task: %w", err)
	}
	if len(extracted) == 0 && msg.Kind != model.MessageKindGroup {
		p.logger.InfoContext(ctx, "no tasks extracted: low model confidence", "msg_id", msg.ID)
		return false, nil
	}

	projectID := ""
	if p.projects != nil {
		pid, resolveErr := p.projects.ResolveProjectForChannel(ctx, msg.Source.ID)
		if resolveErr != nil {
			p.logger.WarnContext(ctx, "error resolving project for channel, using Inbox",
				"error", resolveErr)
		} else {
			projectID = pid
		}
	}

	var taskReqs []model.CreateTaskRequest
	for _, task := range extracted {
		topic := task.Topic
		if msg.Kind == model.MessageKindGroup {
			topic = ""
		}
		p.logger.InfoContext(ctx, "task extracted",
			"summary", task.Summary,
			"project_id", projectID,
			"confidence", task.Confidence)
		taskReqs = append(taskReqs, model.CreateTaskRequest{
			ProjectID:     projectID,
			Summary:       task.Summary,
			Details:       task.Details,
			Topic:         topic,
			Deadline:      task.Deadline,
			Priority:      task.Priority,
			Source:        task.Source,
			SourceDetails: model.SourceDetailsFromMessage(msg, model.SourceViaMessage),
		})
	}

	if msg.Kind == model.MessageKindGroup {
		result, err := p.autoTasks.Submit(ctx, model.AutoTaskSubmission{
			InputKeys:  []string{autoInputKey(msg.Source, msg.ID, msg.Text)},
			ProjectID:  projectID,
			Tasks:      taskReqs,
			SourceText: msg.Text,
		})
		if err != nil {
			return false, fmt.Errorf("submitting group task candidates: %w", err)
		}
		if len(result.Created) > 0 {
			for i := range result.Created {
				result.Created[i].SourceMessage = msg
			}
			p.dispatchNotifiers(ctx, result.Created)
		}
		return len(result.Created) > 0, nil
	}

	tasksToNotify := extracted
	if p.tasks != nil {
		created, createErr := p.tasks.CreateTasks(ctx, model.CreateTasksRequest{
			ProjectID: projectID,
			Tasks:     taskReqs,
		})
		if createErr != nil {
			p.logger.ErrorContext(ctx, "error creating tasks in TaskService", "error", createErr)
		} else {
			tasksToNotify = created
		}
	}

	p.dispatchNotifiers(ctx, tasksToNotify)
	return true, nil
}

// processForwardBatch submits one buffer snapshot; inference never holds a transaction.
func (p *Pipeline) processForwardBatch(ctx context.Context, msg model.Message) error {
	payload := msg.ForwardBatch
	if payload == nil {
		p.logger.WarnContext(ctx, "processForwardBatch: nil ForwardBatchPayload")
		return nil
	}
	if p.forwardClassifier == nil || p.autoTasks == nil || p.taskStore == nil || p.forwardBuffer == nil {
		return fmt.Errorf("processing forward batch: required dependencies are not configured")
	}

	// Keep every snapshot identity for atomic receipts and deletion, but never
	// expose already processed forwards to the classifier or extractor again.
	source := msg.Source
	source.ID = payload.ChatID
	keys := make([]string, 0, len(payload.Forwards))
	ids := make([]string, 0, len(payload.Forwards))
	unprocessed := make([]model.ForwardEntry, 0, len(payload.Forwards))
	for _, entry := range payload.Forwards {
		key := autoInputKey(source, entry.MessageID, entry.Text)
		processed, err := p.autoTasks.Processed(ctx, key)
		if err != nil {
			return fmt.Errorf("checking forward input receipt: %w", err)
		}
		keys = append(keys, key)
		ids = append(ids, entry.MessageID)
		if !processed {
			unprocessed = append(unprocessed, entry)
		}
	}
	if len(keys) == 0 {
		return nil
	}

	hint, _ := parseTriggerProjectHint(payload.TriggerText)
	projectID := p.taskStore.DefaultProjectID()
	projectName := "Inbox"
	var projectNotFoundWarning string
	if hint != "" && p.projects != nil {
		proj, err := p.projects.ResolveProjectRef(ctx, hint)
		if err != nil || proj == nil {
			p.logger.WarnContext(ctx, "forward project hint not found", "hint", hint, "error", err)
			projectNotFoundWarning = p.localizeMsg("forward.project_not_found", map[string]any{"Hint": hint})
		} else {
			projectID = proj.ID
			projectName = proj.Name
		}
	} else if proj, err := p.taskStore.GetProject(ctx, projectID); err == nil && proj != nil {
		projectName = proj.Name
	}

	tz := p.timezone
	if tz == nil {
		tz = time.UTC
	}
	formatted, numbered := FormatForwardBatch(unprocessed, payload.Counterpart, tz)
	var requests []model.CreateTaskRequest
	if len(unprocessed) > 0 {
		syntheticMsg := model.Message{Text: formatted, Kind: model.MessageKindForwardBatch, Source: source}
		class, err := p.forwardClassifier.Classify(ctx, syntheticMsg)
		if err != nil {
			return fmt.Errorf("classifying forward batch: %w", err)
		}
		switch class {
		case model.ClassSkip:
		case model.ClassPromise:
			if p.forwardExtractor == nil {
				return fmt.Errorf("extracting forward batch: ForwardExtractor is not configured")
			}
			extracted, err := p.forwardExtractor.Extract(ctx, syntheticMsg, nil)
			if err != nil {
				return fmt.Errorf("extracting forward tasks: %w", err)
			}
			for _, task := range extracted {
				requests = append(requests, model.CreateTaskRequest{
					ProjectID:     projectID,
					Summary:       task.Summary,
					Details:       task.Details,
					Topic:         task.Topic,
					Deadline:      task.Deadline,
					Priority:      task.Priority,
					Source:        source,
					SourceDetails: forwardSourceDetails(numbered, task.ForwardIndex, payload.Counterpart),
				})
			}
		default:
			return fmt.Errorf("classifying forward batch: unexpected classification %q", class)
		}
	}

	result, err := p.autoTasks.Submit(ctx, model.AutoTaskSubmission{
		InputKeys:  keys,
		ProjectID:  projectID,
		Tasks:      requests,
		SourceText: formatted,
		Finalize: func(ctx context.Context, tx *sql.Tx) error {
			if err := p.forwardBuffer.DeleteEntriesTx(ctx, tx, payload.ChatID, ids); err != nil {
				return fmt.Errorf("deleting forward snapshot: %w", err)
			}
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("submitting forward task candidates: %w", err)
	}

	// Deleting Telegram messages is best effort and only follows actual creation.
	if len(result.Created) > 0 && payload.DeleteFn != nil {
		deleteIDs := append([]string(nil), ids...)
		if payload.TriggerID != "" {
			deleteIDs = append(deleteIDs, payload.TriggerID)
		}
		if err := payload.DeleteFn(ctx, deleteIDs); err != nil {
			p.logger.WarnContext(ctx, "deleting Telegram forwards after commit", "error", err, "chat_id", payload.ChatID)
		}
	}

	if payload.ReplyFn != nil {
		reply := func(text string) {
			if err := payload.ReplyFn(ctx, text); err != nil {
				p.logger.WarnContext(ctx, "sending forward outcome reply", "error", err, "chat_id", payload.ChatID)
			}
		}
		if projectNotFoundWarning != "" {
			reply(projectNotFoundWarning)
		}
		if len(result.Created) > 0 {
			lines := make([]string, len(result.Created))
			for i, task := range result.Created {
				lines[i] = "— " + task.Summary
			}
			reply(p.localizeMsg("forward.extracted_tasks", map[string]any{
				"Count": len(result.Created), "Project": projectName, "Tasks": strings.Join(lines, "\n"),
			}, len(result.Created)))
		}
		if len(requests) == 0 && len(unprocessed) > 0 && !result.Replay && len(result.Pending) == 0 && result.Duplicates == 0 && len(result.Created) == 0 {
			reply(p.localizeMsg("forward.no_promises_found", nil))
		} else {
			reply(p.localizeMsg("auto_tasks.outcome", map[string]any{
				"Created": len(result.Created), "Pending": len(result.Pending), "Duplicates": result.Duplicates,
			}))
		}
	}
	return nil
}

// forwardSourceDetails builds the source snapshot of a task extracted from a
// forward batch. numbered holds the entries in FormatForwardBatch order and index
// is the 1-based message number returned by the extractor. A valid index names the
// author, text and time of that message; a missing or out-of-range index falls
// back to batch-level details (the latest original date, no author or text).
// Forwarded DMs have no public link.
func forwardSourceDetails(numbered []model.ForwardEntry, index int, counterpart string) *model.TaskSourceDetails {
	details := &model.TaskSourceDetails{Name: counterpart, Via: model.SourceViaForward}
	if index >= 1 && index <= len(numbered) {
		entry := numbered[index-1]
		details.Author = entry.ForwardFromName
		details.Text = entry.Text
		details.At = model.SourceTime(entry.OriginalDate)
		return details.Capped()
	}
	var latest time.Time
	for _, entry := range numbered {
		if entry.OriginalDate.After(latest) {
			latest = entry.OriginalDate
		}
	}
	details.At = model.SourceTime(latest)
	return details
}

// autoInputKey separates message edits and source accounts without persisting raw text.
func autoInputKey(source model.Source, messageID, text string) string {
	return fmt.Sprintf("%q/%q/%q/%q/%x", source.Kind, source.AccountID, source.ID, messageID, sha256.Sum256([]byte(text)))
}

// localizeMsg translates msgID using the pipeline's localizer.
// Falls back to returning msgID itself when localizer is nil.
func (p *Pipeline) localizeMsg(msgID string, data any, count ...int) string {
	if p.localizer == nil {
		return msgID
	}
	return localei18n.Translate(p.localizer, msgID, data, count...)
}

func (p *Pipeline) reactPending(ctx context.Context, msg model.Message) {
	if msg.ReactFn == nil {
		return
	}
	if err := msg.ReactFn(ctx, "✍️"); err != nil {
		p.logger.WarnContext(ctx, "error setting pending reaction", "msg_id", msg.ID, "error", err)
	}
}

func (p *Pipeline) reactDone(ctx context.Context, msg model.Message) {
	if msg.ReactFn == nil {
		return
	}
	if err := msg.ReactFn(ctx, "👍"); err != nil {
		p.logger.WarnContext(ctx, "error setting done reaction", "msg_id", msg.ID, "error", err)
	}
}

// reactClear removes the pending reaction without leaving a success marker.
// Used when a message was classified as a promise but no actionable task was extracted.
func (p *Pipeline) reactClear(ctx context.Context, msg model.Message) {
	if msg.ReactFn == nil {
		return
	}
	if err := msg.ReactFn(ctx, ""); err != nil {
		p.logger.WarnContext(ctx, "error clearing reaction", "msg_id", msg.ID, "error", err)
	}
}

func textPreview(text string, n int) string {
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[:n]) + "…"
}

func (p *Pipeline) dispatchNotifiers(ctx context.Context, tasks []model.Task) {
	var wg sync.WaitGroup

	for _, n := range p.notifiers {
		wg.Add(1)
		go func(n model.Notifier) {
			defer wg.Done()
			if err := n.Notify(ctx, tasks); err != nil {
				p.logger.ErrorContext(ctx, "error sending notification", "error", err)
			}
		}(n)
	}

	wg.Wait()
}
