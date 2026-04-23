package usecase_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/usecase"
)

// openTestDB opens an in-memory SQLite for tests that need a real transaction.
// The schema is empty — the use-case talks to the store through mocks only, but a real
// DB provides honest tx.BeginTx/Commit/Rollback semantics.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// mockTaskStoreForTasks is a mock of model.TaskStore for TaskService tests.
type mockTaskStoreForTasks struct {
	defaultPID string
	projects   map[string]*model.Project
	tasks      map[string]*model.Task
	counter    int

	createTaskErr error
	updateTaskErr error
	moveTaskErr   error
	getTaskErr    error
	getProjectErr error
	listTasksErr  error

	createCalled int
	updateCalled int
	moveCalled   int
	lastMoveID   string
	lastMoveTo   string
}

func newMockTaskStore(defaultPID string) *mockTaskStoreForTasks {
	m := &mockTaskStoreForTasks{
		defaultPID: defaultPID,
		projects:   make(map[string]*model.Project),
		tasks:      make(map[string]*model.Task),
	}
	m.projects[defaultPID] = &model.Project{ID: defaultPID, Name: "Inbox", Slug: "inbox"}
	return m
}

func (m *mockTaskStoreForTasks) CreateProjectTx(_ context.Context, _ *sql.Tx, p *model.Project) error {
	if p.ID == "" {
		p.ID = "new-proj-uuid"
	}
	cp := *p
	m.projects[p.ID] = &cp
	return nil
}

func (m *mockTaskStoreForTasks) UpdateProjectTx(_ context.Context, _ *sql.Tx, _ string, _ model.ProjectUpdate) error {
	return nil
}

func (m *mockTaskStoreForTasks) GetProject(_ context.Context, id string) (*model.Project, error) {
	if m.getProjectErr != nil {
		return nil, m.getProjectErr
	}
	p, ok := m.projects[id]
	if !ok {
		return nil, nil
	}
	cp := *p
	return &cp, nil
}

func (m *mockTaskStoreForTasks) GetProjectTx(ctx context.Context, _ *sql.Tx, id string) (*model.Project, error) {
	return m.GetProject(ctx, id)
}

func (m *mockTaskStoreForTasks) ListProjects(_ context.Context) ([]model.Project, error) {
	out := make([]model.Project, 0, len(m.projects))
	for _, p := range m.projects {
		out = append(out, *p)
	}
	return out, nil
}

func (m *mockTaskStoreForTasks) FindProjectByName(_ context.Context, _ string) (*model.Project, error) {
	return nil, nil
}

func (m *mockTaskStoreForTasks) CreateTaskTx(_ context.Context, _ *sql.Tx, task *model.Task) error {
	if m.createTaskErr != nil {
		return m.createTaskErr
	}
	m.createCalled++
	m.counter++
	task.ID = fmt.Sprintf("task-uuid-%d", m.counter)
	task.Number = m.counter
	task.Status = "open"
	task.CreatedAt = time.Now().UTC()
	task.UpdatedAt = task.CreatedAt
	if p, ok := m.projects[task.ProjectID]; ok {
		task.ProjectSlug = p.Slug
	}
	cp := *task
	m.tasks[task.ID] = &cp
	return nil
}

func (m *mockTaskStoreForTasks) GetTask(_ context.Context, id string) (*model.Task, error) {
	if m.getTaskErr != nil {
		return nil, m.getTaskErr
	}
	t := m.tasks[id]
	if t == nil {
		return nil, nil
	}
	cp := *t
	return &cp, nil
}

func (m *mockTaskStoreForTasks) GetTaskTx(ctx context.Context, _ *sql.Tx, id string) (*model.Task, error) {
	return m.GetTask(ctx, id)
}

func (m *mockTaskStoreForTasks) GetTaskByRef(_ context.Context, slug string, number int) (*model.Task, error) {
	if m.getTaskErr != nil {
		return nil, m.getTaskErr
	}
	for _, t := range m.tasks {
		if t.ProjectSlug == slug && t.Number == number {
			cp := *t
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *mockTaskStoreForTasks) ListTasks(_ context.Context, _ string, _ model.TaskFilter) ([]model.Task, error) {
	if m.listTasksErr != nil {
		return nil, m.listTasksErr
	}
	out := make([]model.Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		out = append(out, *t)
	}
	return out, nil
}

func (m *mockTaskStoreForTasks) UpdateTaskTx(_ context.Context, _ *sql.Tx, id string, upd model.TaskUpdate) error {
	if m.updateTaskErr != nil {
		return m.updateTaskErr
	}
	m.updateCalled++
	t := m.tasks[id]
	if t == nil {
		return errors.New("задача не найдена")
	}
	if upd.Summary != nil {
		t.Summary = *upd.Summary
	}
	if upd.Details != nil {
		t.Details = *upd.Details
	}
	if upd.Topic != nil {
		t.Topic = *upd.Topic
	}
	if upd.Status != nil {
		t.Status = *upd.Status
	}
	if upd.Deadline != nil {
		t.Deadline = *upd.Deadline
	}
	if upd.ClosedAt != nil {
		t.ClosedAt = *upd.ClosedAt
	}
	t.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *mockTaskStoreForTasks) MoveTaskTx(_ context.Context, _ *sql.Tx, taskID, newProjectID string) error {
	if m.moveTaskErr != nil {
		return m.moveTaskErr
	}
	m.moveCalled++
	m.lastMoveID = taskID
	m.lastMoveTo = newProjectID
	t := m.tasks[taskID]
	if t != nil {
		t.ProjectID = newProjectID
		if p, ok := m.projects[newProjectID]; ok {
			t.ProjectSlug = p.Slug
		}
	}
	return nil
}

func (m *mockTaskStoreForTasks) DefaultProjectID() string { return m.defaultPID }

// mockEventStore is a mock of model.EventStore.
type mockEventStore struct {
	mu         sync.Mutex
	events     []model.Event
	nextSeq    int64
	insertErr  error
	insertErrN int // after how many calls to start returning insertErr (0 = immediately)
	inserts    int
}

func newMockEventStore() *mockEventStore {
	return &mockEventStore{}
}

func (m *mockEventStore) Insert(_ context.Context, tx *sql.Tx, ev model.Event) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inserts++
	if m.insertErr != nil && m.inserts > m.insertErrN {
		return 0, m.insertErr
	}
	// Verify that tx is not nil — the use-case must always pass a live transaction.
	if tx == nil {
		return 0, errors.New("eventStore.Insert: tx не может быть nil")
	}
	m.nextSeq++
	ev.Seq = m.nextSeq
	ev.CreatedAt = time.Now().UTC()
	m.events = append(m.events, ev)
	return ev.Seq, nil
}

func (m *mockEventStore) SinceSeq(_ context.Context, afterSeq int64, limit int) ([]model.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []model.Event
	for _, ev := range m.events {
		if ev.Seq > afterSeq {
			out = append(out, ev)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (m *mockEventStore) MaxSeq(_ context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nextSeq, nil
}

func (m *mockEventStore) MinSeq(_ context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.events) == 0 {
		return 0, nil
	}
	return m.events[0].Seq, nil
}

func (m *mockEventStore) DeleteOlderThan(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}
func (m *mockEventStore) GetBySeq(_ context.Context, _ int64) (*model.Event, error) {
	return nil, nil
}

func (m *mockEventStore) recorded() []model.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Event, len(m.events))
	copy(out, m.events)
	return out
}

// mockDeviceStore is a mock of model.DeviceStore.
type mockDeviceStore struct {
	activeIDs []string
	listErr   error
}

func (m *mockDeviceStore) Create(_ context.Context, _ *sql.Tx, _ *model.Device) error {
	return nil
}
func (m *mockDeviceStore) FindByTokenHash(_ context.Context, _ string) (*model.Device, error) {
	return nil, nil
}
func (m *mockDeviceStore) UpdateLastSeen(_ context.Context, _ string, _ time.Time) error { return nil }
func (m *mockDeviceStore) Revoke(_ context.Context, _ string) error                      { return nil }
func (m *mockDeviceStore) List(_ context.Context) ([]model.Device, error)                { return nil, nil }
func (m *mockDeviceStore) ListActiveIDs(_ context.Context) ([]string, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	out := make([]string, len(m.activeIDs))
	copy(out, m.activeIDs)
	return out, nil
}
func (m *mockDeviceStore) UpdatePushTokens(_ context.Context, _ string, _, _ *string) error {
	return nil
}
func (m *mockDeviceStore) Get(_ context.Context, _ string) (*model.Device, error) {
	return nil, nil
}
func (m *mockDeviceStore) ListInactive(_ context.Context, _ time.Time) ([]model.Device, error) {
	return nil, nil
}
func (m *mockDeviceStore) DeleteRevokedOlderThan(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

// mockPushQueue is a mock of model.PushQueue.
type mockPushQueue struct {
	mu         sync.Mutex
	enqueued   []mockEnqueued
	enqueueErr error
}

type mockEnqueued struct {
	DeviceID string
	EventSeq int64
}

func (q *mockPushQueue) Enqueue(_ context.Context, tx *sql.Tx, deviceID string, eventSeq int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.enqueueErr != nil {
		return q.enqueueErr
	}
	if tx == nil {
		return errors.New("pushQueue.Enqueue: tx не может быть nil")
	}
	q.enqueued = append(q.enqueued, mockEnqueued{DeviceID: deviceID, EventSeq: eventSeq})
	return nil
}

func (q *mockPushQueue) NextBatch(_ context.Context, _ int) ([]model.PushJob, error) {
	return nil, nil
}
func (q *mockPushQueue) MarkDelivered(_ context.Context, _ int64) error { return nil }
func (q *mockPushQueue) MarkFailed(_ context.Context, _ int64, _ string, _ time.Time) error {
	return nil
}
func (q *mockPushQueue) Drop(_ context.Context, _ int64, _ string) error { return nil }
func (q *mockPushQueue) DeleteDelivered(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

func (q *mockPushQueue) snapshot() []mockEnqueued {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]mockEnqueued, len(q.enqueued))
	copy(out, q.enqueued)
	return out
}

// mockBroker is a mock of model.Broker with IsActive control and Notify call recording.
type mockBroker struct {
	mu         sync.Mutex
	active     map[string]bool
	notified   []model.Event
	notifyCall func(ev model.Event)
}

func newMockBroker() *mockBroker {
	return &mockBroker{active: make(map[string]bool)}
}

func (b *mockBroker) Subscribe(_ string) (<-chan model.Event, func()) { return nil, func() {} }
func (b *mockBroker) IsActive(deviceID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active[deviceID]
}
func (b *mockBroker) Notify(ev model.Event) {
	b.mu.Lock()
	b.notified = append(b.notified, ev)
	cb := b.notifyCall
	b.mu.Unlock()
	if cb != nil {
		cb(ev)
	}
}
func (b *mockBroker) notifiedEvents() []model.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]model.Event, len(b.notified))
	copy(out, b.notified)
	return out
}

// testFixture assembles dependencies for TaskService.
type testFixture struct {
	db      *sql.DB
	tasks   *mockTaskStoreForTasks
	events  *mockEventStore
	devices *mockDeviceStore
	queue   *mockPushQueue
	broker  *mockBroker
	svc     model.TaskService
}

func newTestFixture(t *testing.T) *testFixture {
	f := &testFixture{
		db:      openTestDB(t),
		tasks:   newMockTaskStore("inbox-uuid"),
		events:  newMockEventStore(),
		devices: &mockDeviceStore{},
		queue:   &mockPushQueue{},
		broker:  newMockBroker(),
	}
	f.svc = usecase.NewTaskService(usecase.TaskServiceDeps{
		DB:      f.db,
		Tasks:   f.tasks,
		Events:  f.events,
		Devices: f.devices,
		Queue:   f.queue,
		Broker:  f.broker,
	})
	return f
}

// --- tests ---

func TestTaskServiceCreateUsesInboxIfProjectIDEmpty(t *testing.T) {
	f := newTestFixture(t)

	task, err := f.svc.CreateTask(context.Background(), model.CreateTaskRequest{Summary: "тест"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.ProjectID != "inbox-uuid" {
		t.Fatalf("ProjectID=%q, want %q", task.ProjectID, "inbox-uuid")
	}
	if f.tasks.createCalled != 1 {
		t.Fatalf("store.CreateTaskTx called %d times, expected 1", f.tasks.createCalled)
	}
}

func TestTaskServiceCreatePassesProjectID(t *testing.T) {
	f := newTestFixture(t)
	f.tasks.projects["custom-proj"] = &model.Project{ID: "custom-proj", Slug: "custom"}

	task, err := f.svc.CreateTask(context.Background(), model.CreateTaskRequest{
		ProjectID: "custom-proj",
		Summary:   "задача в проекте",
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.ProjectID != "custom-proj" {
		t.Fatalf("ProjectID=%q, want %q", task.ProjectID, "custom-proj")
	}
}

func TestTaskServiceCreateTaskInsertsEventAndEnqueuesForInactive(t *testing.T) {
	f := newTestFixture(t)
	f.devices.activeIDs = []string{"device-A", "device-B"}
	// device-A is connected via SSE, device-B is not.
	f.broker.active["device-A"] = true

	task, err := f.svc.CreateTask(context.Background(), model.CreateTaskRequest{Summary: "обещание"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Event was recorded via EventStore.
	events := f.events.recorded()
	if len(events) != 1 {
		t.Fatalf("events=%d, want 1", len(events))
	}
	if events[0].Kind != model.EventTaskCreated {
		t.Fatalf("kind=%q, want %q", events[0].Kind, model.EventTaskCreated)
	}
	if events[0].EntityID != task.ID {
		t.Fatalf("EntityID=%q, want %q", events[0].EntityID, task.ID)
	}
	var payload struct {
		Task          map[string]any `json:"task"`
		ChangedFields []string       `json:"changedFields"`
	}
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("payload failed to parse: %v", err)
	}
	if payload.Task["id"] != task.ID {
		t.Fatalf("payload.task.id=%v, want %q", payload.Task["id"], task.ID)
	}
	if payload.ChangedFields != nil {
		t.Fatalf("task_created must not have changedFields, got %v", payload.ChangedFields)
	}

	// Push enqueued only for device-B.
	enq := f.queue.snapshot()
	if len(enq) != 1 {
		t.Fatalf("enqueued=%d, want 1", len(enq))
	}
	if enq[0].DeviceID != "device-B" {
		t.Fatalf("enqueued device=%q, want %q", enq[0].DeviceID, "device-B")
	}

	// Broker.Notify was called exactly once.
	notified := f.broker.notifiedEvents()
	if len(notified) != 1 {
		t.Fatalf("notified=%d, want 1", len(notified))
	}
	if notified[0].Seq != 1 {
		t.Fatalf("notified seq=%d, want 1", notified[0].Seq)
	}
}

func TestTaskServiceCreateTaskSkipsEnqueueForAllActive(t *testing.T) {
	f := newTestFixture(t)
	f.devices.activeIDs = []string{"device-A"}
	f.broker.active["device-A"] = true

	_, err := f.svc.CreateTask(context.Background(), model.CreateTaskRequest{Summary: "активный"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	if n := len(f.queue.snapshot()); n != 0 {
		t.Fatalf("queue enqueue called %d times, want 0", n)
	}
	if n := len(f.broker.notifiedEvents()); n != 1 {
		t.Fatalf("broker.Notify called %d times, want 1", n)
	}
}

func TestTaskServiceCreateTaskRollbackOnEventInsertError(t *testing.T) {
	f := newTestFixture(t)
	f.events.insertErr = errors.New("events insert сломан")

	_, err := f.svc.CreateTask(context.Background(), model.CreateTaskRequest{Summary: "упадёт"})
	if err == nil {
		t.Fatal("want error")
	}

	// Broker.Notify must not be called: the tx was rolled back.
	if n := len(f.broker.notifiedEvents()); n != 0 {
		t.Fatalf("broker.Notify called %d times, want 0 (rollback)", n)
	}
	// Queue must also contain no entries (tx rollback + event insert failed
	// before enqueue).
	if n := len(f.queue.snapshot()); n != 0 {
		t.Fatalf("queue enqueue called %d times, want 0", n)
	}
}

func TestTaskServiceCreateTasksBatchSingleTransaction(t *testing.T) {
	f := newTestFixture(t)
	f.devices.activeIDs = []string{"device-B"}

	tasks, err := f.svc.CreateTasks(context.Background(), model.CreateTasksRequest{
		Tasks: []model.CreateTaskRequest{
			{Summary: "t1"},
			{Summary: "t2"},
			{Summary: "t3"},
		},
	})
	if err != nil {
		t.Fatalf("CreateTasks: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("tasks=%d, want 3", len(tasks))
	}

	events := f.events.recorded()
	if len(events) != 3 {
		t.Fatalf("events=%d, want 3", len(events))
	}
	for i, ev := range events {
		if int(ev.Seq) != i+1 {
			t.Fatalf("event[%d].Seq=%d, want %d", i, ev.Seq, i+1)
		}
	}

	// Each task gets a separate push for device-B.
	enq := f.queue.snapshot()
	if len(enq) != 3 {
		t.Fatalf("enqueued=%d, want 3", len(enq))
	}
	for _, e := range enq {
		if e.DeviceID != "device-B" {
			t.Fatalf("enqueued device=%q, want %q", e.DeviceID, "device-B")
		}
	}

	// Broker received 3 notifications.
	if n := len(f.broker.notifiedEvents()); n != 3 {
		t.Fatalf("notified=%d, want 3", n)
	}
}

func TestTaskServiceCompleteEmitsTaskCompletedEvent(t *testing.T) {
	f := newTestFixture(t)
	f.tasks.tasks["t1"] = &model.Task{
		ID:          "t1",
		ProjectID:   "inbox-uuid",
		ProjectSlug: "inbox",
		Number:      1,
		Status:      "open",
		Summary:     "s1",
	}

	task, err := f.svc.CompleteTask(context.Background(), "t1")
	if err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if task.Status != "done" {
		t.Fatalf("Status=%q, want %q", task.Status, "done")
	}

	events := f.events.recorded()
	if len(events) != 1 {
		t.Fatalf("events=%d, want 1", len(events))
	}
	if events[0].Kind != model.EventTaskCompleted {
		t.Fatalf("kind=%q, want %q", events[0].Kind, model.EventTaskCompleted)
	}
}

func TestTaskServiceReopenEmitsTaskReopenedEvent(t *testing.T) {
	f := newTestFixture(t)
	closed := time.Now().UTC()
	f.tasks.tasks["t2"] = &model.Task{
		ID:          "t2",
		ProjectID:   "inbox-uuid",
		ProjectSlug: "inbox",
		Number:      1,
		Status:      "done",
		Summary:     "s2",
		ClosedAt:    &closed,
	}

	task, err := f.svc.ReopenTask(context.Background(), "t2")
	if err != nil {
		t.Fatalf("ReopenTask: %v", err)
	}
	if task.Status != "open" {
		t.Fatalf("Status=%q, want %q", task.Status, "open")
	}

	events := f.events.recorded()
	if len(events) != 1 || events[0].Kind != model.EventTaskReopened {
		t.Fatalf("want 1 event task_reopened, got %+v", events)
	}
}

func TestTaskServiceUpdateEmitsTaskUpdatedEvent(t *testing.T) {
	f := newTestFixture(t)
	f.tasks.tasks["t3"] = &model.Task{
		ID:          "t3",
		ProjectID:   "inbox-uuid",
		ProjectSlug: "inbox",
		Number:      1,
		Status:      "open",
		Summary:     "s3",
	}

	newDetails := "детали"
	_, err := f.svc.UpdateTask(context.Background(), "t3", model.TaskUpdate{Details: &newDetails})
	if err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	events := f.events.recorded()
	if len(events) != 1 || events[0].Kind != model.EventTaskUpdated {
		t.Fatalf("want 1 event task_updated, got %+v", events)
	}
}

func TestTaskServiceMoveEmitsTaskMovedEventAndDelegates(t *testing.T) {
	f := newTestFixture(t)
	f.tasks.projects["dst-uuid"] = &model.Project{ID: "dst-uuid", Slug: "dst"}
	f.tasks.tasks["t4"] = &model.Task{
		ID:          "t4",
		ProjectID:   "src",
		ProjectSlug: "src",
		Number:      3,
		Status:      "open",
		Summary:     "s4",
	}

	task, err := f.svc.MoveTask(context.Background(), "t4", "dst-uuid")
	if err != nil {
		t.Fatalf("MoveTask: %v", err)
	}
	if task.ProjectID != "dst-uuid" {
		t.Fatalf("ProjectID=%q, want %q", task.ProjectID, "dst-uuid")
	}
	if f.tasks.moveCalled != 1 {
		t.Fatalf("store.MoveTaskTx called %d times, want 1", f.tasks.moveCalled)
	}

	events := f.events.recorded()
	if len(events) != 1 || events[0].Kind != model.EventTaskMoved {
		t.Fatalf("want 1 event task_moved, got %+v", events)
	}
}

func TestTaskServiceGetByRefLookup(t *testing.T) {
	f := newTestFixture(t)
	f.tasks.tasks["t5"] = &model.Task{
		ID:          "t5",
		ProjectID:   "p1",
		ProjectSlug: "inbox",
		Number:      5,
		Summary:     "ref задача",
	}

	task, err := f.svc.GetTaskByRef(context.Background(), "inbox", 5)
	if err != nil {
		t.Fatalf("GetTaskByRef: %v", err)
	}
	if task == nil || task.ID != "t5" {
		t.Fatalf("got=%v", task)
	}
}

func TestTaskServiceGetByRefNotFound(t *testing.T) {
	f := newTestFixture(t)
	task, err := f.svc.GetTaskByRef(context.Background(), "inbox", 999)
	if err != nil {
		t.Fatalf("GetTaskByRef: %v", err)
	}
	if task != nil {
		t.Fatalf("want nil, got %q", task.ID)
	}
}

func TestTaskServiceListTasks(t *testing.T) {
	f := newTestFixture(t)
	f.tasks.tasks["x1"] = &model.Task{ID: "x1", Status: "open"}
	f.tasks.tasks["x2"] = &model.Task{ID: "x2", Status: "done"}

	list, err := f.svc.ListTasks(context.Background(), "", model.TaskFilter{})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len=%d, want 2", len(list))
	}
}

func TestTaskServiceGetTask(t *testing.T) {
	f := newTestFixture(t)
	f.tasks.tasks["t6"] = &model.Task{ID: "t6", Summary: "найди меня"}

	task, err := f.svc.GetTask(context.Background(), "t6")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task == nil || task.ID != "t6" {
		t.Fatalf("GetTask returned %v", task)
	}
}

func TestTaskServiceCreateTaskRollbackOnDeviceListError(t *testing.T) {
	f := newTestFixture(t)
	f.devices.listErr = errors.New("devices store сломан")

	_, err := f.svc.CreateTask(context.Background(), model.CreateTaskRequest{Summary: "s"})
	if err == nil {
		t.Fatal("want error")
	}
	if n := len(f.broker.notifiedEvents()); n != 0 {
		t.Fatalf("broker.Notify called %d times on rollback", n)
	}
	if n := len(f.events.recorded()); n != 0 {
		t.Fatalf("events recorded (%d) before rollback", n)
	}
}

// parseTaskPayload parses a task-event payload into a struct with task + changedFields.
func parseTaskPayload(t *testing.T, raw []byte) (task map[string]any, changed []string) {
	t.Helper()
	var p struct {
		Task          map[string]any `json:"task"`
		ChangedFields []string       `json:"changedFields"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("parseTaskPayload: %v", err)
	}
	return p.Task, p.ChangedFields
}

func TestTaskService_UpdateTask_PayloadIncludesChangedFields(t *testing.T) {
	f := newTestFixture(t)
	f.tasks.tasks["t1"] = &model.Task{
		ID:          "t1",
		ProjectID:   "inbox-uuid",
		ProjectSlug: "inbox",
		Number:      1,
		Status:      "open",
		Summary:     "старая summary",
	}

	newSummary := "новая summary"
	_, err := f.svc.UpdateTask(context.Background(), "t1", model.TaskUpdate{Summary: &newSummary})
	if err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	evs := f.events.recorded()
	if len(evs) != 1 {
		t.Fatalf("events=%d, want 1", len(evs))
	}
	if evs[0].Kind != model.EventTaskUpdated {
		t.Fatalf("kind=%q, want task_updated", evs[0].Kind)
	}

	taskData, changed := parseTaskPayload(t, evs[0].Payload)
	if taskData["summary"] != newSummary {
		t.Fatalf("task.summary=%v, want %q", taskData["summary"], newSummary)
	}
	if len(changed) != 1 || changed[0] != "summary" {
		t.Fatalf("changedFields=%v, want [summary]", changed)
	}
}

func TestTaskService_UpdateTask_MultipleChanges(t *testing.T) {
	f := newTestFixture(t)
	dl := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	f.tasks.tasks["t2"] = &model.Task{
		ID:          "t2",
		ProjectID:   "inbox-uuid",
		ProjectSlug: "inbox",
		Number:      2,
		Status:      "open",
		Summary:     "старая",
	}

	newSummary := "новая"
	dlPtr := &dl
	_, err := f.svc.UpdateTask(context.Background(), "t2", model.TaskUpdate{
		Summary:  &newSummary,
		Deadline: &dlPtr,
	})
	if err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	_, changed := parseTaskPayload(t, f.events.recorded()[0].Payload)
	if len(changed) != 2 || changed[0] != "summary" || changed[1] != "deadline" {
		t.Fatalf("changedFields=%v, want [summary deadline]", changed)
	}
}

func TestTaskService_UpdateTask_NoChanges_DoesNotEmitEvent(t *testing.T) {
	f := newTestFixture(t)
	f.tasks.tasks["t3"] = &model.Task{
		ID:          "t3",
		ProjectID:   "inbox-uuid",
		ProjectSlug: "inbox",
		Number:      3,
		Status:      "open",
		Summary:     "без изменений",
	}

	// Update summary with the same value — changedFields must be empty.
	same := "без изменений"
	_, err := f.svc.UpdateTask(context.Background(), "t3", model.TaskUpdate{Summary: &same})
	if err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	evs := f.events.recorded()
	if len(evs) != 1 {
		t.Fatalf("events=%d, want 1 (event is written even with no real changes)", len(evs))
	}
	_, changed := parseTaskPayload(t, evs[0].Payload)
	if len(changed) != 0 {
		t.Fatalf("changedFields=%v, want [] (no real changes)", changed)
	}
}

func TestTaskService_OtherTaskEvents_PayloadWrapsInTaskKey(t *testing.T) {
	f := newTestFixture(t)
	f.tasks.tasks["t4"] = &model.Task{
		ID:          "t4",
		ProjectID:   "inbox-uuid",
		ProjectSlug: "inbox",
		Number:      4,
		Status:      "open",
		Summary:     "задача",
	}
	f.tasks.projects["dst"] = &model.Project{ID: "dst", Slug: "dst"}
	f.tasks.tasks["t5"] = &model.Task{
		ID:          "t5",
		ProjectID:   "inbox-uuid",
		ProjectSlug: "inbox",
		Number:      5,
		Status:      "open",
		Summary:     "для переноса",
	}

	// task_created — in CreateTask.
	_, err := f.svc.CreateTask(context.Background(), model.CreateTaskRequest{Summary: "создана"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	// task_completed.
	_, err = f.svc.CompleteTask(context.Background(), "t4")
	if err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	// task_moved.
	_, err = f.svc.MoveTask(context.Background(), "t5", "dst")
	if err != nil {
		t.Fatalf("MoveTask: %v", err)
	}

	for i, ev := range f.events.recorded() {
		taskData, changed := parseTaskPayload(t, ev.Payload)
		if len(taskData) == 0 {
			t.Fatalf("event[%d] %s: task is empty", i, ev.Kind)
		}
		if len(changed) != 0 {
			t.Fatalf("event[%d] %s: changedFields=%v, want nil (not task_updated)", i, ev.Kind, changed)
		}
	}
}
