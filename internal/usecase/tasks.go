package usecase

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// TaskServiceDeps collects the dependencies for TaskService.
type TaskServiceDeps struct {
	// DB is the database used to open write transactions.
	DB *sql.DB
	// Tasks is the task and project store (write methods are tx-aware).
	Tasks model.TaskStore
	// Events is the domain event store; shares the same transaction.
	Events model.EventStore
	// Devices is the client device store (source of active device IDs).
	Devices model.DeviceStore
	// Queue is the push job queue (enqueued within the tx).
	Queue model.PushQueue
	// Broker is the in-memory SSE broker; Notify is called after commit.
	Broker model.Broker
}

type taskService struct {
	db      *sql.DB
	tasks   model.TaskStore
	events  model.EventStore
	devices model.DeviceStore
	queue   model.PushQueue
	broker  model.Broker
}

// NewTaskService creates a TaskService that wraps write operations in a
// transaction and publishes events via EventStore/Broker/PushQueue.
func NewTaskService(deps TaskServiceDeps) model.TaskService {
	return &taskService{
		db:      deps.DB,
		tasks:   deps.Tasks,
		events:  deps.Events,
		devices: deps.Devices,
		queue:   deps.Queue,
		broker:  deps.Broker,
	}
}

// taskSnapshot is the JSON schema for a task event payload.
type taskSnapshot struct {
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
	ClosedAt    *time.Time `json:"closed_at,omitempty"`
}

func makeTaskSnapshot(t *model.Task) taskSnapshot {
	return taskSnapshot{
		ID:          t.ID,
		Number:      t.Number,
		ProjectID:   t.ProjectID,
		ProjectSlug: t.ProjectSlug,
		Summary:     t.Summary,
		Details:     t.Details,
		Topic:       t.Topic,
		Status:      t.Status,
		Deadline:    t.Deadline,
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
		ClosedAt:    t.ClosedAt,
	}
}

// taskEventPayload wraps the payload for all task_* events.
// changedFields is only present in task_updated.
type taskEventPayload struct {
	Task          taskSnapshot `json:"task"`
	ChangedFields []string     `json:"changedFields,omitempty"`
}

// wrapTaskPayload wraps a task snapshot into the unified payload format.
func wrapTaskPayload(t *model.Task, changed []string) taskEventPayload {
	return taskEventPayload{Task: makeTaskSnapshot(t), ChangedFields: changed}
}

// collectChangedFields compares the original task state with the update set
// and returns a list of changed field names in a fixed order.
func collectChangedFields(original *model.Task, upd model.TaskUpdate) []string {
	var changed []string
	if upd.Summary != nil && *upd.Summary != original.Summary {
		changed = append(changed, "summary")
	}
	if upd.Details != nil && *upd.Details != original.Details {
		changed = append(changed, "details")
	}
	if upd.Topic != nil && *upd.Topic != original.Topic {
		changed = append(changed, "topic")
	}
	if upd.Deadline != nil {
		old, new := original.Deadline, *upd.Deadline
		if (old == nil) != (new == nil) || (old != nil && new != nil && !old.Equal(*new)) {
			changed = append(changed, "deadline")
		}
	}
	if upd.Status != nil && *upd.Status != original.Status {
		changed = append(changed, "status")
	}
	return changed
}

func (s *taskService) CreateTask(ctx context.Context, req model.CreateTaskRequest) (*model.Task, error) {
	tasks, err := s.CreateTasks(ctx, model.CreateTasksRequest{
		ProjectID: req.ProjectID,
		Tasks:     []model.CreateTaskRequest{req},
	})
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("создание задачи: пустой результат")
	}
	created := tasks[0]
	return &created, nil
}

func (s *taskService) CreateTasks(ctx context.Context, req model.CreateTasksRequest) ([]model.Task, error) {
	if len(req.Tasks) == 0 {
		return nil, nil
	}

	projectID := req.ProjectID
	if projectID == "" {
		projectID = s.tasks.DefaultProjectID()
	}

	project, err := s.tasks.GetProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("получение проекта %s: %w", projectID, err)
	}
	if project == nil {
		return nil, fmt.Errorf("проект %s не найден", projectID)
	}

	activeIDs, err := s.devices.ListActiveIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("список активных устройств: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("открытие транзакции: %w", err)
	}
	defer tx.Rollback()

	created := make([]model.Task, 0, len(req.Tasks))
	pendingEvents := make([]model.Event, 0, len(req.Tasks))

	for _, r := range req.Tasks {
		task := &model.Task{
			ProjectID:   projectID,
			ProjectSlug: project.Slug,
			Summary:     r.Summary,
			Details:     r.Details,
			Topic:       r.Topic,
			Deadline:    r.Deadline,
			Source:      r.Source,
		}
		if err := s.tasks.CreateTaskTx(ctx, tx, task); err != nil {
			return nil, fmt.Errorf("создание задачи: %w", err)
		}
		task.ProjectSlug = project.Slug

		ev, err := s.recordEvent(ctx, tx, model.EventTaskCreated, task.ID, wrapTaskPayload(task, nil), activeIDs)
		if err != nil {
			return nil, err
		}
		pendingEvents = append(pendingEvents, ev)
		created = append(created, *task)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit транзакции: %w", err)
	}

	// task_counter was updated inside the transaction; if TaskStore is
	// wrapped in CachedTaskStore its project snapshot is now stale.
	s.invalidateProjectCache()

	for _, ev := range pendingEvents {
		s.broker.Notify(ev)
	}

	appendTouchedTasks(ctx, created)
	return created, nil
}

func (s *taskService) UpdateTask(ctx context.Context, id string, upd model.TaskUpdate) (*model.Task, error) {
	return s.updateAndEmit(ctx, id, upd, model.EventTaskUpdated)
}

func (s *taskService) CompleteTask(ctx context.Context, id string) (*model.Task, error) {
	status := "done"
	now := time.Now().UTC()
	closed := &now
	return s.updateAndEmit(ctx, id, model.TaskUpdate{Status: &status, ClosedAt: &closed}, model.EventTaskCompleted)
}

func (s *taskService) ReopenTask(ctx context.Context, id string) (*model.Task, error) {
	status := "open"
	var nilTime *time.Time
	return s.updateAndEmit(ctx, id, model.TaskUpdate{Status: &status, ClosedAt: &nilTime}, model.EventTaskReopened)
}

func (s *taskService) MoveTask(ctx context.Context, id, newProjectID string) (*model.Task, error) {
	activeIDs, err := s.devices.ListActiveIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("список активных устройств: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("открытие транзакции: %w", err)
	}
	defer tx.Rollback()

	if err := s.tasks.MoveTaskTx(ctx, tx, id, newProjectID); err != nil {
		return nil, fmt.Errorf("перенос задачи: %w", err)
	}

	task, err := s.tasks.GetTaskTx(ctx, tx, id)
	if err != nil {
		return nil, fmt.Errorf("чтение задачи после переноса: %w", err)
	}
	if task == nil {
		return nil, fmt.Errorf("задача %s не найдена после переноса", id)
	}

	ev, err := s.recordEvent(ctx, tx, model.EventTaskMoved, task.ID, wrapTaskPayload(task, nil), activeIDs)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit транзакции: %w", err)
	}

	// MoveTaskTx increments task_counter of the target project — invalidate cache.
	s.invalidateProjectCache()

	s.broker.Notify(ev)
	appendTouchedTasks(ctx, []model.Task{*task})
	return task, nil
}

func (s *taskService) ListTasks(ctx context.Context, projectID string, filter model.TaskFilter) ([]model.Task, error) {
	tasks, err := s.tasks.ListTasks(ctx, projectID, filter)
	if err != nil {
		return nil, fmt.Errorf("список задач: %w", err)
	}
	return tasks, nil
}

func (s *taskService) GetTask(ctx context.Context, id string) (*model.Task, error) {
	task, err := s.tasks.GetTask(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("получение задачи: %w", err)
	}
	return task, nil
}

func (s *taskService) GetTaskByRef(ctx context.Context, projectSlug string, number int) (*model.Task, error) {
	task, err := s.tasks.GetTaskByRef(ctx, projectSlug, number)
	if err != nil {
		return nil, fmt.Errorf("получение задачи по ссылке: %w", err)
	}
	return task, nil
}

// updateAndEmit applies changes in a transaction and publishes an event with kind.
func (s *taskService) updateAndEmit(ctx context.Context, id string, upd model.TaskUpdate, kind model.EventKind) (*model.Task, error) {
	activeIDs, err := s.devices.ListActiveIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("список активных устройств: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("открытие транзакции: %w", err)
	}
	defer tx.Rollback()

	// For task_updated, read the original before applying changes to compute changedFields.
	var original *model.Task
	if kind == model.EventTaskUpdated {
		original, err = s.tasks.GetTaskTx(ctx, tx, id)
		if err != nil {
			return nil, fmt.Errorf("чтение задачи до обновления: %w", err)
		}
		if original == nil {
			return nil, fmt.Errorf("задача %s не найдена", id)
		}
	}

	if err := s.tasks.UpdateTaskTx(ctx, tx, id, upd); err != nil {
		return nil, fmt.Errorf("обновление задачи: %w", err)
	}

	task, err := s.tasks.GetTaskTx(ctx, tx, id)
	if err != nil {
		return nil, fmt.Errorf("чтение задачи после обновления: %w", err)
	}
	if task == nil {
		return nil, fmt.Errorf("задача %s не найдена после обновления", id)
	}

	var payload taskEventPayload
	if kind == model.EventTaskUpdated {
		payload = wrapTaskPayload(task, collectChangedFields(original, upd))
	} else {
		payload = wrapTaskPayload(task, nil)
	}

	ev, err := s.recordEvent(ctx, tx, kind, task.ID, payload, activeIDs)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit транзакции: %w", err)
	}

	s.broker.Notify(ev)
	appendTouchedTasks(ctx, []model.Task{*task})
	return task, nil
}

// recordEvent serialises the payload, inserts the event, enqueues it for
// inactive devices, and returns the event with its assigned seq.
func (s *taskService) recordEvent(
	ctx context.Context,
	tx *sql.Tx,
	kind model.EventKind,
	entityID string,
	payload any,
	activeIDs []string,
) (model.Event, error) {
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

// invalidateProjectCache drops the project cache in TaskStore if it supports
// caching (CachedTaskStore). Called after tx.Commit() for operations that
// change task_counter (CreateTasks, MoveTask) so consumers don't see a stale
// counter in the project list or snapshot.
func (s *taskService) invalidateProjectCache() {
	if inv, ok := s.tasks.(interface{ Invalidate() }); ok {
		inv.Invalidate()
	}
}
