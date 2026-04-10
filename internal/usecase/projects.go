package usecase

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// ProjectServiceDeps collects the dependencies for ProjectService.
type ProjectServiceDeps struct {
	// DB is the database used to open write transactions.
	DB *sql.DB
	// Tasks is the task and project store (write methods are tx-aware).
	Tasks model.TaskStore
	// Meta is the channel metadata store (write methods are tx-aware).
	Meta model.MetaStore
	// Events is the domain event store; shares the same transaction.
	Events model.EventStore
	// Devices is the client device store (source of active device IDs).
	Devices model.DeviceStore
	// Queue is the push job queue (enqueued within the tx).
	Queue model.PushQueue
	// Broker is the in-memory SSE broker; Notify is called after commit.
	Broker model.Broker
}

type projectService struct {
	db      *sql.DB
	store   model.TaskStore
	meta    model.MetaStore
	events  model.EventStore
	devices model.DeviceStore
	queue   model.PushQueue
	broker  model.Broker
}

// NewProjectService creates a ProjectService that wraps write operations in a
// transaction and publishes events via EventStore/Broker/PushQueue.
func NewProjectService(deps ProjectServiceDeps) model.ProjectService {
	return &projectService{
		db:      deps.DB,
		store:   deps.Tasks,
		meta:    deps.Meta,
		events:  deps.Events,
		devices: deps.Devices,
		queue:   deps.Queue,
		broker:  deps.Broker,
	}
}

// projectSnapshot is the JSON schema for a project event payload.
type projectSnapshot struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description string    `json:"description,omitempty"`
	TaskCounter int       `json:"task_counter"`
	CreatedAt   time.Time `json:"created_at"`
}

func makeProjectSnapshot(p *model.Project) projectSnapshot {
	return projectSnapshot{
		ID:          p.ID,
		Name:        p.Name,
		Slug:        p.Slug,
		Description: p.Description,
		TaskCounter: p.TaskCounter,
		CreatedAt:   p.CreatedAt,
	}
}

func (s *projectService) CreateProject(ctx context.Context, req model.CreateProjectRequest) (*model.Project, error) {
	p := &model.Project{
		Name:        req.Name,
		Description: req.Description,
		Slug:        req.Slug,
	}
	if p.Slug == "" {
		p.Slug = Slugify(p.Name)
	}

	activeIDs, err := s.listActiveDeviceIDs(ctx)
	if err != nil {
		return nil, err
	}

	var pendingEvent model.Event
	if err := s.runInTx(ctx, func(tx *sql.Tx) error {
		if err := s.store.CreateProjectTx(ctx, tx, p); err != nil {
			return fmt.Errorf("создание проекта: %w", err)
		}
		ev, err := s.recordEvent(ctx, tx, model.EventProjectCreated, p.ID, makeProjectSnapshot(p), activeIDs)
		if err != nil {
			return err
		}
		pendingEvent = ev
		return nil
	}); err != nil {
		return nil, err
	}

	s.invalidateProjectCache()
	s.notify(pendingEvent)
	appendTouchedProjects(ctx, []model.Project{*p})
	return p, nil
}

func (s *projectService) UpdateProject(ctx context.Context, id string, upd model.ProjectUpdate) (*model.Project, error) {
	activeIDs, err := s.listActiveDeviceIDs(ctx)
	if err != nil {
		return nil, err
	}

	var (
		updated      *model.Project
		pendingEvent model.Event
	)
	if err := s.runInTx(ctx, func(tx *sql.Tx) error {
		if err := s.store.UpdateProjectTx(ctx, tx, id, upd); err != nil {
			return fmt.Errorf("обновление проекта: %w", err)
		}
		p, err := s.store.GetProjectTx(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("получение проекта после обновления: %w", err)
		}
		if p == nil {
			return fmt.Errorf("проект %s не найден после обновления", id)
		}
		updated = p
		ev, err := s.recordEvent(ctx, tx, model.EventProjectUpdated, p.ID, makeProjectSnapshot(p), activeIDs)
		if err != nil {
			return err
		}
		pendingEvent = ev
		return nil
	}); err != nil {
		return nil, err
	}

	s.invalidateProjectCache()
	s.notify(pendingEvent)
	appendTouchedProjects(ctx, []model.Project{*updated})
	return updated, nil
}

func (s *projectService) ListProjects(ctx context.Context) ([]model.Project, error) {
	return s.store.ListProjects(ctx)
}

func (s *projectService) FindProjectByName(ctx context.Context, name string) (*model.Project, error) {
	return s.store.FindProjectByName(ctx, name)
}

func (s *projectService) ResolveProjectForChannel(ctx context.Context, channelID string) (string, error) {
	pid, err := s.meta.Get(ctx, "project:"+channelID)
	if err != nil {
		return "", fmt.Errorf("чтение маппинга канала: %w", err)
	}
	if pid != "" {
		return pid, nil
	}
	return s.store.DefaultProjectID(), nil
}

func (s *projectService) EnsureChannelProject(ctx context.Context, channelID, name string) (*model.Project, error) {
	existing, err := s.store.FindProjectByName(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("поиск проекта: %w", err)
	}

	activeIDs, err := s.listActiveDeviceIDs(ctx)
	if err != nil {
		return nil, err
	}

	var (
		created      bool
		pendingEvent model.Event
	)
	if err := s.runInTx(ctx, func(tx *sql.Tx) error {
		if existing == nil {
			p := &model.Project{Name: name, Slug: Slugify(name)}
			if err := s.store.CreateProjectTx(ctx, tx, p); err != nil {
				return fmt.Errorf("создание проекта: %w", err)
			}
			existing = p
			created = true
		}
		if err := s.meta.SetTx(ctx, tx, "project:"+channelID, existing.ID); err != nil {
			return fmt.Errorf("сохранение маппинга канала: %w", err)
		}
		if created {
			ev, err := s.recordEvent(ctx, tx, model.EventProjectCreated, existing.ID, makeProjectSnapshot(existing), activeIDs)
			if err != nil {
				return err
			}
			pendingEvent = ev
		}
		return nil
	}); err != nil {
		return nil, err
	}

	if created {
		s.invalidateProjectCache()
		s.notify(pendingEvent)
	}
	appendTouchedProjects(ctx, []model.Project{*existing})
	return existing, nil
}

// listActiveDeviceIDs returns a list of non-revoked device IDs, or an empty
// slice when DeviceStore is not configured (e.g. in tests without a real HTTP layer).
func (s *projectService) listActiveDeviceIDs(ctx context.Context) ([]string, error) {
	if s.devices == nil {
		return nil, nil
	}
	ids, err := s.devices.ListActiveIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("список активных устройств: %w", err)
	}
	return ids, nil
}

// recordEvent serialises the payload, inserts the event, enqueues it for
// inactive devices, and returns the event with its assigned seq. If EventStore
// is nil, returns an empty event without error — the use-case may be called
// without realtime infrastructure.
func (s *projectService) recordEvent(
	ctx context.Context,
	tx *sql.Tx,
	kind model.EventKind,
	entityID string,
	payload any,
	activeIDs []string,
) (model.Event, error) {
	if s.events == nil {
		return model.Event{}, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return model.Event{}, fmt.Errorf("сериализация события %s: %w", kind, err)
	}
	ev := model.Event{Kind: kind, EntityID: entityID, Payload: raw}
	seq, err := s.events.Insert(ctx, tx, ev)
	if err != nil {
		return model.Event{}, fmt.Errorf("запись события %s: %w", kind, err)
	}
	ev.Seq = seq

	if s.queue == nil || s.broker == nil {
		return ev, nil
	}
	for _, id := range activeIDs {
		if s.broker.IsActive(id) {
			continue
		}
		if err := s.queue.Enqueue(ctx, tx, id, seq); err != nil {
			return model.Event{}, fmt.Errorf("enqueue push %s для %s: %w", kind, id, err)
		}
	}
	return ev, nil
}

// notify delivers the event to the broker if both the broker and the event are present.
func (s *projectService) notify(ev model.Event) {
	if s.broker == nil || ev.Seq == 0 {
		return
	}
	s.broker.Notify(ev)
}

// invalidateProjectCache drops the project cache in TaskStore if it supports
// caching (CachedTaskStore). Called after tx.Commit() — before that point
// a concurrent ListProjects would read the pre-commit state.
func (s *projectService) invalidateProjectCache() {
	if inv, ok := s.store.(interface{ Invalidate() }); ok {
		inv.Invalidate()
	}
}

// runInTx opens a transaction, calls fn, and commits; rolls back on error.
// db may be nil in tests with direct mock stores — in that case nil tx is
// passed and the mock stores work without a sql layer.
func (s *projectService) runInTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if s.db == nil {
		return fn(nil)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("открытие транзакции: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit транзакции: %w", err)
	}
	return nil
}
