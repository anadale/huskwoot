package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
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

	Tasks    model.TaskService
	Projects model.ProjectService
	Chat     model.ChatService

	Classifiers map[model.MessageKind]model.Classifier
	Extractors  map[model.MessageKind]model.Extractor
	Notifiers   []model.Notifier
	Logger      *slog.Logger

	// ForwardBatch processing dependencies. All are optional; when nil the
	// ForwardBatch pipeline branch is a no-op.
	ForwardClassifier model.Classifier
	ForwardExtractor  model.Extractor
	ForwardBuffer     model.ForwardBuffer
	DB                *sql.DB
	TaskStore         model.TaskStore
	EventStore        model.EventStore
	PushQueue         model.PushQueue
	Broker            model.Broker
	DeviceStore       model.DeviceStore
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
	projects    model.ProjectService
	chat        model.ChatService
	ownerIDs    []string
	aliases     []string
	logger      *slog.Logger

	// ForwardBatch dependencies.
	forwardClassifier model.Classifier
	forwardExtractor  model.Extractor
	forwardBuffer     model.ForwardBuffer
	db                *sql.DB
	taskStore         model.TaskStore
	eventStore        model.EventStore
	pushQueue         model.PushQueue
	broker            model.Broker
	deviceStore       model.DeviceStore
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
		projects:          cfg.Projects,
		chat:              cfg.Chat,
		ownerIDs:          cfg.OwnerIDs,
		aliases:           cfg.Aliases,
		logger:            logger,
		forwardClassifier: cfg.ForwardClassifier,
		forwardExtractor:  cfg.ForwardExtractor,
		forwardBuffer:     cfg.ForwardBuffer,
		db:                cfg.DB,
		taskStore:         cfg.TaskStore,
		eventStore:        cfg.EventStore,
		pushQueue:         cfg.PushQueue,
		broker:            cfg.Broker,
		deviceStore:       cfg.DeviceStore,
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
//   - Group/Batch → classifier → extractor → TaskService + notifiers.
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

// processPromise handles a promise message:
// extracts tasks, saves via TaskService, dispatches to notifiers.
// Returns true when at least one task was extracted (regardless of persistence outcome).
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
	if len(extracted) == 0 {
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
			ProjectID: projectID,
			Summary:   task.Summary,
			Details:   task.Details,
			Topic:     topic,
			Deadline:  task.Deadline,
			Source:    task.Source,
		})
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

// processForwardBatch handles MessageKindForwardBatch:
//  1. Parses project hint from trigger text.
//  2. Formats the forward batch for LLM.
//  3. Classifies with ForwardClassifier.
//  4. If skip → clears buffer (tx), replies "no promises found".
//  5. If promise → extracts tasks with ForwardExtractor.
//  6. Creates tasks + event + push in one atomic tx with buffer deletion.
//  7. Optionally deletes Telegram messages via DeleteFn.
//  8. Replies with extracted task list.
func (p *Pipeline) processForwardBatch(ctx context.Context, msg model.Message) error {
	payload := msg.ForwardBatch
	if payload == nil {
		p.logger.WarnContext(ctx, "processForwardBatch: nil ForwardBatchPayload")
		return nil
	}
	if p.forwardClassifier == nil || p.db == nil || p.taskStore == nil || p.forwardBuffer == nil {
		p.logger.WarnContext(ctx, "processForwardBatch: missing critical dependencies, skipping",
			"chat_id", payload.ChatID)
		return nil
	}

	// 1. Resolve target project.
	hint, _ := parseTriggerProjectHint(payload.TriggerText)
	projectID := p.taskStore.DefaultProjectID()
	projectName := "Inbox"
	projectSlug := ""
	var projectNotFoundWarning string

	if hint != "" && p.projects != nil {
		proj, err := p.projects.ResolveProjectRef(ctx, hint)
		if err != nil {
			p.logger.WarnContext(ctx, "processForwardBatch: project hint not found",
				"hint", hint, "error", err, "chat_id", payload.ChatID)
			projectNotFoundWarning = p.localizeMsg("forward.project_not_found",
				map[string]any{"Hint": hint})
		} else {
			projectID = proj.ID
			projectName = proj.Name
			projectSlug = proj.Slug
		}
	}

	// Get Inbox project details when not overridden by a hint.
	if projectSlug == "" {
		proj, err := p.taskStore.GetProject(ctx, projectID)
		if err == nil && proj != nil {
			projectName = proj.Name
			projectSlug = proj.Slug
		}
	}

	// 2. Format batch for LLM.
	tz := p.timezone
	if tz == nil {
		tz = time.UTC
	}
	formatted := FormatForwardBatch(payload.Forwards, payload.Counterpart, tz)

	syntheticMsg := model.Message{
		Text:   formatted,
		Kind:   model.MessageKindForwardBatch,
		Source: msg.Source,
	}

	// 3. Classify.
	class, err := p.forwardClassifier.Classify(ctx, syntheticMsg)
	if err != nil {
		return fmt.Errorf("classifying forward batch: %w", err)
	}

	if class == model.ClassSkip {
		p.logger.InfoContext(ctx, "processForwardBatch: classified as skip, clearing buffer",
			"chat_id", payload.ChatID)
		return p.skipForwardBatch(ctx, payload)
	}

	// 4. Extract.
	if p.forwardExtractor == nil {
		p.logger.WarnContext(ctx, "processForwardBatch: no ForwardExtractor configured")
		return p.skipForwardBatch(ctx, payload)
	}
	extracted, err := p.forwardExtractor.Extract(ctx, syntheticMsg, nil)
	if err != nil {
		return fmt.Errorf("extracting forward tasks: %w", err)
	}
	if len(extracted) == 0 {
		p.logger.InfoContext(ctx, "processForwardBatch: no tasks extracted, treating as skip",
			"chat_id", payload.ChatID)
		return p.skipForwardBatch(ctx, payload)
	}

	// 5. Get active device IDs before opening the transaction.
	activeIDs, err := p.getActiveDeviceIDs(ctx)
	if err != nil {
		p.logger.WarnContext(ctx, "processForwardBatch: ListActiveIDs failed", "error", err)
	}

	// 6. Transactional write: CreateTaskTx × N + events + push + DeleteBatchTx.
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning forward batch tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	created := make([]model.Task, 0, len(extracted))
	pendingEvents := make([]model.Event, 0, len(extracted))

	for i := range extracted {
		task := &model.Task{
			ProjectID:   projectID,
			ProjectSlug: projectSlug,
			Summary:     extracted[i].Summary,
			Details:     extracted[i].Details,
			Topic:       extracted[i].Topic,
			Deadline:    extracted[i].Deadline,
		}
		if err := p.taskStore.CreateTaskTx(ctx, tx, task); err != nil {
			return fmt.Errorf("creating forward task: %w", err)
		}
		ev, err := p.insertForwardEventTx(ctx, tx, task, activeIDs)
		if err != nil {
			return err
		}
		pendingEvents = append(pendingEvents, ev)
		created = append(created, *task)
	}

	if err := p.forwardBuffer.DeleteBatchTx(ctx, tx, payload.ChatID); err != nil {
		return fmt.Errorf("deleting forward buffer in tx: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing forward batch tx: %w", err)
	}

	// 6b. Broker fan-out after commit.
	if p.broker != nil {
		for _, ev := range pendingEvents {
			p.broker.Notify(ev)
		}
	}

	// 7. Delete Telegram messages (best-effort).
	if len(created) > 0 && payload.DeleteFn != nil {
		ids := make([]string, 0, len(payload.Forwards)+1)
		for _, f := range payload.Forwards {
			ids = append(ids, f.MessageID)
		}
		if payload.TriggerID != "" {
			ids = append(ids, payload.TriggerID)
		}
		if err := payload.DeleteFn(ctx, ids); err != nil {
			p.logger.WarnContext(ctx, "processForwardBatch: DeleteFn failed, tasks already saved",
				"error", err, "chat_id", payload.ChatID)
		}
	}

	// 8. Reply.
	if payload.ReplyFn != nil {
		if projectNotFoundWarning != "" {
			_ = payload.ReplyFn(ctx, projectNotFoundWarning)
		}
		lines := make([]string, len(created))
		for i, t := range created {
			lines[i] = "— " + t.Summary
		}
		replyText := p.localizeMsg("forward.extracted_tasks", map[string]any{
			"Count":   len(created),
			"Project": projectName,
			"Tasks":   strings.Join(lines, "\n"),
		}, len(created))
		_ = payload.ReplyFn(ctx, replyText)
	}

	return nil
}

// skipForwardBatch clears the forward buffer for the chat in a transaction and
// sends the "no promises found" reply. DeleteFn is intentionally NOT called on
// skip: the user's Telegram messages are left intact.
func (p *Pipeline) skipForwardBatch(ctx context.Context, payload *model.ForwardBatchPayload) error {
	if p.db != nil && p.forwardBuffer != nil {
		tx, txErr := p.db.BeginTx(ctx, nil)
		if txErr != nil {
			p.logger.WarnContext(ctx, "skipForwardBatch: begin tx failed",
				"error", txErr, "chat_id", payload.ChatID)
		} else {
			defer tx.Rollback() //nolint:errcheck
			if delErr := p.forwardBuffer.DeleteBatchTx(ctx, tx, payload.ChatID); delErr != nil {
				p.logger.WarnContext(ctx, "skipForwardBatch: DeleteBatchTx failed",
					"error", delErr, "chat_id", payload.ChatID)
			} else if commitErr := tx.Commit(); commitErr != nil {
				p.logger.WarnContext(ctx, "skipForwardBatch: commit failed",
					"error", commitErr, "chat_id", payload.ChatID)
			}
		}
	}

	if payload.ReplyFn != nil {
		_ = payload.ReplyFn(ctx, p.localizeMsg("forward.no_promises_found", nil))
	}
	return nil
}

// localizeMsg translates msgID using the pipeline's localizer.
// Falls back to returning msgID itself when localizer is nil.
func (p *Pipeline) localizeMsg(msgID string, data any, count ...int) string {
	if p.localizer == nil {
		return msgID
	}
	return localei18n.Translate(p.localizer, msgID, data, count...)
}

// forwardTaskSnap is the per-task JSON object inside a task_created event payload.
// Field names deliberately match usecase.taskSnapshot for protocol compatibility.
type forwardTaskSnap struct {
	ID          string     `json:"id"`
	Number      int        `json:"number"`
	ProjectID   string     `json:"project_id"`
	ProjectSlug string     `json:"project_slug,omitempty"`
	Summary     string     `json:"summary"`
	Details     string     `json:"details,omitempty"`
	Topic       string     `json:"topic,omitempty"`
	Status      string     `json:"status"`
	Deadline    *time.Time `json:"deadline,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type forwardEventPayload struct {
	Task forwardTaskSnap `json:"task"`
}

// insertForwardEventTx records a task_created event within tx and enqueues push
// jobs for offline devices. Returns a zero Event when EventStore is nil.
func (p *Pipeline) insertForwardEventTx(ctx context.Context, tx *sql.Tx, task *model.Task, activeIDs []string) (model.Event, error) {
	if p.eventStore == nil {
		return model.Event{}, nil
	}

	snap := forwardTaskSnap{
		ID:          task.ID,
		Number:      task.Number,
		ProjectID:   task.ProjectID,
		ProjectSlug: task.ProjectSlug,
		Summary:     task.Summary,
		Details:     task.Details,
		Topic:       task.Topic,
		Status:      task.Status,
		Deadline:    task.Deadline,
		CreatedAt:   task.CreatedAt,
		UpdatedAt:   task.UpdatedAt,
	}
	raw, err := json.Marshal(forwardEventPayload{Task: snap})
	if err != nil {
		return model.Event{}, fmt.Errorf("marshaling forward task event: %w", err)
	}

	ev := model.Event{Kind: model.EventTaskCreated, EntityID: task.ID, Payload: raw}
	seq, err := p.eventStore.Insert(ctx, tx, ev)
	if err != nil {
		return model.Event{}, fmt.Errorf("inserting forward task event: %w", err)
	}
	ev.Seq = seq

	if p.pushQueue != nil && p.broker != nil {
		for _, deviceID := range activeIDs {
			if p.broker.IsActive(deviceID) {
				continue
			}
			if err := p.pushQueue.Enqueue(ctx, tx, deviceID, seq); err != nil {
				return model.Event{}, fmt.Errorf("enqueuing push for device %s: %w", deviceID, err)
			}
		}
	}

	return ev, nil
}

// getActiveDeviceIDs returns device IDs for push queue routing.
// Returns nil when DeviceStore is not configured.
func (p *Pipeline) getActiveDeviceIDs(ctx context.Context) ([]string, error) {
	if p.deviceStore == nil {
		return nil, nil
	}
	return p.deviceStore.ListActiveIDs(ctx)
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
