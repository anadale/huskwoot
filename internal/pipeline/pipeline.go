package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

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
}

// New creates a new Pipeline with the given components and dependencies.
func New(cfg Config) *Pipeline {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Pipeline{
		classifiers: cfg.Classifiers,
		extractors:  cfg.Extractors,
		notifiers:   cfg.Notifiers,
		tasks:       cfg.Tasks,
		projects:    cfg.Projects,
		chat:        cfg.Chat,
		ownerIDs:    cfg.OwnerIDs,
		aliases:     cfg.Aliases,
		logger:      logger,
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
//   - DM/GroupDirect → ChatService.HandleMessage (tool calling loop, reply via ReplyFn).
//   - Group/Batch → classifier → extractor → TaskService + notifiers.
func (p *Pipeline) Process(ctx context.Context, msg model.Message) error {
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
		if err := p.processPromise(ctx, msg); err != nil {
			return err
		}
		p.reactDone(ctx, msg)
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
func (p *Pipeline) processPromise(ctx context.Context, msg model.Message) error {
	extractor := p.extractors[msg.Kind]
	if extractor == nil {
		p.logger.WarnContext(ctx, "no extractor found for message kind",
			"kind", msg.Kind, "msg_id", msg.ID)
		return nil
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
		return fmt.Errorf("extracting task: %w", err)
	}
	if len(extracted) == 0 {
		p.logger.InfoContext(ctx, "no tasks extracted: low model confidence", "msg_id", msg.ID)
		return nil
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
	return nil
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
