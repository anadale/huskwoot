package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/anadale/huskwoot/internal/model"
)

// --- In-memory DB helper ---

// openForwardTestDB opens an in-memory SQLite database for transaction tests.
// The schema is not needed since all store calls go through mocks; the DB is
// used solely to provide a real BeginTx/Commit/Rollback implementation.
func openForwardTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// --- Mock: ForwardBuffer ---

type mockForwardBufferForPipeline struct {
	mu            sync.Mutex
	deletedChats  []string
	deleteErr     error
}

func (m *mockForwardBufferForPipeline) Append(_ context.Context, _ model.ForwardEntry) error {
	return nil
}

func (m *mockForwardBufferForPipeline) LastReceivedAt(_ context.Context, _ string) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

func (m *mockForwardBufferForPipeline) LoadBatch(_ context.Context, _ string) ([]model.ForwardEntry, error) {
	return nil, nil
}

func (m *mockForwardBufferForPipeline) DeleteBatchTx(_ context.Context, _ *sql.Tx, chatID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.deletedChats = append(m.deletedChats, chatID)
	return nil
}

func (m *mockForwardBufferForPipeline) ListPendingChats(_ context.Context, _ time.Time) ([]string, error) {
	return nil, nil
}

func (m *mockForwardBufferForPipeline) deletedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.deletedChats)
}

// --- Mock: TaskStore (minimal subset needed by processForwardBatch) ---

type mockForwardTaskStore struct {
	mu            sync.Mutex
	createdTasks  []*model.Task
	createTaskErr error
	defaultProjID string
	project       *model.Project
}

func newMockForwardTaskStore(defaultProjID string, proj *model.Project) *mockForwardTaskStore {
	return &mockForwardTaskStore{defaultProjID: defaultProjID, project: proj}
}

func (m *mockForwardTaskStore) DefaultProjectID() string { return m.defaultProjID }

func (m *mockForwardTaskStore) GetProject(_ context.Context, _ string) (*model.Project, error) {
	return m.project, nil
}

func (m *mockForwardTaskStore) GetProjectTx(_ context.Context, _ *sql.Tx, _ string) (*model.Project, error) {
	return m.project, nil
}

func (m *mockForwardTaskStore) CreateTaskTx(_ context.Context, _ *sql.Tx, task *model.Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createTaskErr != nil {
		return m.createTaskErr
	}
	n := len(m.createdTasks) + 1
	task.ID = "task-" + strconv.Itoa(n)
	task.Number = n
	task.Status = "open"
	task.CreatedAt = time.Now().UTC()
	task.UpdatedAt = task.CreatedAt
	m.createdTasks = append(m.createdTasks, task)
	return nil
}

func (m *mockForwardTaskStore) createdCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.createdTasks)
}

// Remaining TaskStore methods — not used by processForwardBatch.
func (m *mockForwardTaskStore) CreateProjectTx(_ context.Context, _ *sql.Tx, _ *model.Project) error {
	return nil
}
func (m *mockForwardTaskStore) UpdateProjectTx(_ context.Context, _ *sql.Tx, _ string, _ model.ProjectUpdate) error {
	return nil
}
func (m *mockForwardTaskStore) ListProjects(_ context.Context) ([]model.Project, error) {
	return nil, nil
}
func (m *mockForwardTaskStore) FindProjectByName(_ context.Context, _ string) (*model.Project, error) {
	return nil, nil
}
func (m *mockForwardTaskStore) GetTask(_ context.Context, _ string) (*model.Task, error) {
	return nil, nil
}
func (m *mockForwardTaskStore) GetTaskTx(_ context.Context, _ *sql.Tx, _ string) (*model.Task, error) {
	return nil, nil
}
func (m *mockForwardTaskStore) GetTaskByRef(_ context.Context, _ string, _ int) (*model.Task, error) {
	return nil, nil
}
func (m *mockForwardTaskStore) ListTasks(_ context.Context, _ string, _ model.TaskFilter) ([]model.Task, error) {
	return nil, nil
}
func (m *mockForwardTaskStore) UpdateTaskTx(_ context.Context, _ *sql.Tx, _ string, _ model.TaskUpdate) error {
	return nil
}
func (m *mockForwardTaskStore) MoveTaskTx(_ context.Context, _ *sql.Tx, _, _ string) error {
	return nil
}
func (m *mockForwardTaskStore) AddProjectAliasTx(_ context.Context, _ *sql.Tx, _, _ string) error {
	return nil
}
func (m *mockForwardTaskStore) RemoveProjectAliasTx(_ context.Context, _ *sql.Tx, _, _ string) error {
	return nil
}
func (m *mockForwardTaskStore) ListAliasesForProject(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (m *mockForwardTaskStore) ListClosedOlderThan(_ context.Context, _ time.Time) ([]model.Task, error) {
	return nil, nil
}
func (m *mockForwardTaskStore) DeleteTaskTx(_ context.Context, _ *sql.Tx, _ string) error {
	return nil
}

// --- Mock: EventStore ---

type mockForwardEventStore struct {
	mu        sync.Mutex
	nextSeq   int64
	insertErr error
}

func (m *mockForwardEventStore) Insert(_ context.Context, _ *sql.Tx, _ model.Event) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.insertErr != nil {
		return 0, m.insertErr
	}
	m.nextSeq++
	return m.nextSeq, nil
}

func (m *mockForwardEventStore) SinceSeq(_ context.Context, _ int64, _ int) ([]model.Event, error) {
	return nil, nil
}
func (m *mockForwardEventStore) MaxSeq(_ context.Context) (int64, error)  { return 0, nil }
func (m *mockForwardEventStore) MinSeq(_ context.Context) (int64, error)  { return 0, nil }
func (m *mockForwardEventStore) GetBySeq(_ context.Context, _ int64) (*model.Event, error) {
	return nil, nil
}
func (m *mockForwardEventStore) DeleteOlderThan(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

// --- Mock: PushQueue ---

type mockForwardPushQueue struct {
	mu       sync.Mutex
	enqueued int
}

func (m *mockForwardPushQueue) Enqueue(_ context.Context, _ *sql.Tx, _ string, _ int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enqueued++
	return nil
}
func (m *mockForwardPushQueue) NextBatch(_ context.Context, _ int) ([]model.PushJob, error) {
	return nil, nil
}
func (m *mockForwardPushQueue) MarkDelivered(_ context.Context, _ int64) error   { return nil }
func (m *mockForwardPushQueue) MarkFailed(_ context.Context, _ int64, _ string, _ time.Time) error {
	return nil
}
func (m *mockForwardPushQueue) Drop(_ context.Context, _ int64, _ string) error { return nil }
func (m *mockForwardPushQueue) DeleteDelivered(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

// --- Mock: Broker ---

type mockForwardBroker struct {
	mu       sync.Mutex
	notified []model.Event
}

func (m *mockForwardBroker) Notify(ev model.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.notified = append(m.notified, ev)
}
func (m *mockForwardBroker) IsActive(_ string) bool { return false }
func (m *mockForwardBroker) Subscribe(_ string) (<-chan model.Event, func()) {
	ch := make(chan model.Event)
	return ch, func() { close(ch) }
}

func (m *mockForwardBroker) notifiedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.notified)
}

// --- Mock: DeviceStore ---

type mockForwardDeviceStore struct {
	activeIDs []string
}

func (m *mockForwardDeviceStore) ListActiveIDs(_ context.Context) ([]string, error) {
	return m.activeIDs, nil
}
func (m *mockForwardDeviceStore) Create(_ context.Context, _ *sql.Tx, _ *model.Device) error {
	return nil
}
func (m *mockForwardDeviceStore) FindByTokenHash(_ context.Context, _ string) (*model.Device, error) {
	return nil, nil
}
func (m *mockForwardDeviceStore) UpdateLastSeen(_ context.Context, _ string, _ time.Time) error {
	return nil
}
func (m *mockForwardDeviceStore) Revoke(_ context.Context, _ string) error { return nil }
func (m *mockForwardDeviceStore) List(_ context.Context) ([]model.Device, error) {
	return nil, nil
}
func (m *mockForwardDeviceStore) UpdatePushTokens(_ context.Context, _ string, _, _ *string) error {
	return nil
}
func (m *mockForwardDeviceStore) Get(_ context.Context, _ string) (*model.Device, error) {
	return nil, nil
}
func (m *mockForwardDeviceStore) ListInactive(_ context.Context, _ time.Time) ([]model.Device, error) {
	return nil, nil
}
func (m *mockForwardDeviceStore) DeleteRevokedOlderThan(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

// --- Mock: ProjectService (configurable ResolveProjectRef) ---

type mockForwardProjectService struct {
	resolveRefResult *model.Project
	resolveRefErr    error
}

func (m *mockForwardProjectService) ResolveProjectRef(_ context.Context, _ string) (*model.Project, error) {
	return m.resolveRefResult, m.resolveRefErr
}
func (m *mockForwardProjectService) CreateProject(_ context.Context, _ model.CreateProjectRequest) (*model.Project, error) {
	return nil, nil
}
func (m *mockForwardProjectService) UpdateProject(_ context.Context, _ string, _ model.ProjectUpdate) (*model.Project, error) {
	return nil, nil
}
func (m *mockForwardProjectService) ListProjects(_ context.Context) ([]model.Project, error) {
	return nil, nil
}
func (m *mockForwardProjectService) FindProjectByName(_ context.Context, _ string) (*model.Project, error) {
	return nil, nil
}
func (m *mockForwardProjectService) ResolveProjectForChannel(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (m *mockForwardProjectService) EnsureChannelProject(_ context.Context, _, _ string) (*model.Project, error) {
	return nil, nil
}
func (m *mockForwardProjectService) AddProjectAlias(_ context.Context, _, _ string) (*model.Project, error) {
	return nil, nil
}
func (m *mockForwardProjectService) RemoveProjectAlias(_ context.Context, _, _ string) (*model.Project, error) {
	return nil, nil
}

// --- Mock: Classifier for forward ---

type mockForwardClassifier struct {
	class model.Classification
	err   error
}

func (m *mockForwardClassifier) Classify(_ context.Context, _ model.Message) (model.Classification, error) {
	return m.class, m.err
}

// --- Mock: Extractor for forward ---

type mockForwardExtractor struct {
	tasks []model.Task
	err   error
}

func (m *mockForwardExtractor) Extract(_ context.Context, _ model.Message, _ []model.HistoryEntry) ([]model.Task, error) {
	return m.tasks, m.err
}

// --- Helpers ---

func makeTestForwards(chatID string, n int) []model.ForwardEntry {
	now := time.Now().UTC()
	entries := make([]model.ForwardEntry, n)
	for i := 0; i < n; i++ {
		entries[i] = model.ForwardEntry{
			ChatID:          chatID,
			MessageID:       fmt.Sprintf("msg-%d", i+1),
			ForwardFromName: "Вася",
			IsSelfAuthored:  i%2 == 0, // alternate self/counterpart
			OriginalDate:    now.Add(time.Duration(i) * time.Minute),
			ReceivedAt:      now.Add(time.Duration(i) * time.Minute),
			Text:            fmt.Sprintf("Сообщение %d", i+1),
		}
	}
	return entries
}

func buildForwardPipeline(
	db *sql.DB,
	fwdClassifier model.Classifier,
	fwdExtractor model.Extractor,
	fwdBuffer model.ForwardBuffer,
	taskStore model.TaskStore,
	eventStore model.EventStore,
	pushQueue model.PushQueue,
	broker model.Broker,
	deviceStore model.DeviceStore,
	projects model.ProjectService,
) *Pipeline {
	return New(Config{
		Logger:            discardLogger,
		ForwardClassifier: fwdClassifier,
		ForwardExtractor:  fwdExtractor,
		ForwardBuffer:     fwdBuffer,
		DB:                db,
		TaskStore:         taskStore,
		EventStore:        eventStore,
		PushQueue:         pushQueue,
		Broker:            broker,
		DeviceStore:       deviceStore,
		Projects:          projects,
	})
}

func inboxProject() *model.Project {
	return &model.Project{ID: "inbox-id", Name: "Inbox", Slug: "inbox"}
}

// --- Tests ---

func TestProcessForwardBatch_HappyPath(t *testing.T) {
	// 3 forwards, classifier=promise, extractor=2 tasks
	// Expected: 2 tasks created, buffer cleared, deleteFn called with 3+1=4 IDs
	const chatID = "chat-happy"
	const triggerID = "trigger-msg"
	forwards := makeTestForwards(chatID, 3)

	db := openForwardTestDB(t)
	fwdBuf := &mockForwardBufferForPipeline{}
	taskStore := newMockForwardTaskStore("inbox-id", inboxProject())
	eventStore := &mockForwardEventStore{}
	pushQueue := &mockForwardPushQueue{}
	broker := &mockForwardBroker{}
	deviceStore := &mockForwardDeviceStore{activeIDs: []string{"device-1"}}

	extractor := &mockForwardExtractor{tasks: []model.Task{
		{Summary: "Прислать слайды", Details: "Обещано: Вася"},
		{Summary: "Подготовить отчёт", Details: "Обещано: Вася"},
	}}

	p := buildForwardPipeline(db,
		&mockForwardClassifier{class: model.ClassPromise},
		extractor,
		fwdBuf, taskStore, eventStore, pushQueue, broker, deviceStore, nil)

	var deletedIDs []string
	var replied []string
	payload := &model.ForwardBatchPayload{
		ChatID:      chatID,
		Forwards:    forwards,
		TriggerText: "",
		TriggerID:   triggerID,
		Counterpart: "Вася",
		DeleteFn: func(_ context.Context, ids []string) error {
			deletedIDs = ids
			return nil
		},
		ReplyFn: func(_ context.Context, text string) error {
			replied = append(replied, text)
			return nil
		},
	}

	msg := model.Message{
		Kind:         model.MessageKindForwardBatch,
		ForwardBatch: payload,
		Source:       model.Source{Kind: "telegram", ID: chatID},
	}

	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	// 2 tasks created
	if n := taskStore.createdCount(); n != 2 {
		t.Errorf("created %d tasks, want 2", n)
	}
	// buffer cleared
	if n := fwdBuf.deletedCount(); n != 1 {
		t.Errorf("buffer deleted %d times, want 1", n)
	}
	// 2 events, 2 push jobs (one device, broker.IsActive=false)
	if eventStore.nextSeq != 2 {
		t.Errorf("event store got %d inserts, want 2", eventStore.nextSeq)
	}
	if pushQueue.enqueued != 2 {
		t.Errorf("push queue got %d enqueues, want 2", pushQueue.enqueued)
	}
	// broker notified twice
	if n := broker.notifiedCount(); n != 2 {
		t.Errorf("broker notified %d times, want 2", n)
	}
	// deleteFn called with 3 forward IDs + 1 trigger ID = 4
	if len(deletedIDs) != 4 {
		t.Errorf("deleteFn called with %d IDs, want 4", len(deletedIDs))
	}
	// trigger ID is included
	found := false
	for _, id := range deletedIDs {
		if id == triggerID {
			found = true
		}
	}
	if !found {
		t.Errorf("deletedIDs %v does not include triggerID %q", deletedIDs, triggerID)
	}
	// reply sent
	if len(replied) == 0 {
		t.Error("ReplyFn not called")
	}
}

func TestProcessForwardBatch_SkipPath(t *testing.T) {
	// classifier=skip → buffer cleared, 0 tasks, deleteFn NOT called
	const chatID = "chat-skip"
	forwards := makeTestForwards(chatID, 2)

	db := openForwardTestDB(t)
	fwdBuf := &mockForwardBufferForPipeline{}
	taskStore := newMockForwardTaskStore("inbox-id", inboxProject())

	p := buildForwardPipeline(db,
		&mockForwardClassifier{class: model.ClassSkip},
		nil, // extractor never called on skip
		fwdBuf, taskStore, nil, nil, nil, nil, nil)

	deleteFnCalled := false
	var replied []string
	payload := &model.ForwardBatchPayload{
		ChatID:      chatID,
		Forwards:    forwards,
		Counterpart: "Вася",
		DeleteFn: func(_ context.Context, _ []string) error {
			deleteFnCalled = true
			return nil
		},
		ReplyFn: func(_ context.Context, text string) error {
			replied = append(replied, text)
			return nil
		},
	}
	msg := model.Message{Kind: model.MessageKindForwardBatch, ForwardBatch: payload}

	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	// buffer must be cleared
	if n := fwdBuf.deletedCount(); n != 1 {
		t.Errorf("buffer deleted %d times on skip, want 1", n)
	}
	// no tasks
	if n := taskStore.createdCount(); n != 0 {
		t.Errorf("created %d tasks on skip, want 0", n)
	}
	// deleteFn must NOT be called
	if deleteFnCalled {
		t.Error("deleteFn must not be called on skip")
	}
	// reply sent
	if len(replied) == 0 {
		t.Error("ReplyFn not called on skip")
	}
}

func TestProcessForwardBatch_ProjectHint(t *testing.T) {
	// triggerText="#вася" → tasks placed in project "вася"
	const chatID = "chat-hint"
	forwards := makeTestForwards(chatID, 2)

	db := openForwardTestDB(t)
	fwdBuf := &mockForwardBufferForPipeline{}
	targetProject := &model.Project{ID: "vася-id", Name: "Вася", Slug: "вася"}
	taskStore := newMockForwardTaskStore("inbox-id", inboxProject())

	projects := &mockForwardProjectService{resolveRefResult: targetProject}

	extractor := &mockForwardExtractor{tasks: []model.Task{
		{Summary: "Обещание для Васи"},
	}}

	p := buildForwardPipeline(db,
		&mockForwardClassifier{class: model.ClassPromise},
		extractor,
		fwdBuf, taskStore, &mockForwardEventStore{}, &mockForwardPushQueue{},
		&mockForwardBroker{}, nil, projects)

	payload := &model.ForwardBatchPayload{
		ChatID:      chatID,
		Forwards:    forwards,
		TriggerText: "обработай #вася",
		Counterpart: "Вася",
		ReplyFn:     func(_ context.Context, _ string) error { return nil },
	}
	msg := model.Message{Kind: model.MessageKindForwardBatch, ForwardBatch: payload}

	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	taskStore.mu.Lock()
	defer taskStore.mu.Unlock()
	if len(taskStore.createdTasks) == 0 {
		t.Fatal("no tasks created")
	}
	if taskStore.createdTasks[0].ProjectID != "vася-id" {
		t.Errorf("task ProjectID = %q, want %q", taskStore.createdTasks[0].ProjectID, "vася-id")
	}
}

func TestProcessForwardBatch_ProjectNotFound(t *testing.T) {
	// triggerText="#нет" → hint not found → fallback Inbox, reply contains warning
	const chatID = "chat-nohint"
	forwards := makeTestForwards(chatID, 2)

	db := openForwardTestDB(t)
	fwdBuf := &mockForwardBufferForPipeline{}
	taskStore := newMockForwardTaskStore("inbox-id", inboxProject())
	projects := &mockForwardProjectService{
		resolveRefErr: errors.New("project not found"),
	}
	extractor := &mockForwardExtractor{tasks: []model.Task{
		{Summary: "Задача в inbox"},
	}}

	p := buildForwardPipeline(db,
		&mockForwardClassifier{class: model.ClassPromise},
		extractor,
		fwdBuf, taskStore, &mockForwardEventStore{}, &mockForwardPushQueue{},
		&mockForwardBroker{}, nil, projects)

	var replied []string
	payload := &model.ForwardBatchPayload{
		ChatID:      chatID,
		Forwards:    forwards,
		TriggerText: "#нет",
		Counterpart: "Вася",
		ReplyFn: func(_ context.Context, text string) error {
			replied = append(replied, text)
			return nil
		},
	}
	msg := model.Message{Kind: model.MessageKindForwardBatch, ForwardBatch: payload}

	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	// task created in Inbox
	taskStore.mu.Lock()
	defer taskStore.mu.Unlock()
	if len(taskStore.createdTasks) == 0 {
		t.Fatal("no tasks created")
	}
	if taskStore.createdTasks[0].ProjectID != "inbox-id" {
		t.Errorf("task ProjectID = %q, want %q", taskStore.createdTasks[0].ProjectID, "inbox-id")
	}

	// warning reply must be present (forward.project_not_found key or the hint text)
	found := false
	for _, r := range replied {
		if strings.Contains(r, "нет") || strings.Contains(r, "forward.project_not_found") {
			found = true
		}
	}
	if !found {
		t.Errorf("no warning reply found in: %v", replied)
	}
}

func TestProcessForwardBatch_DeleteFailure(t *testing.T) {
	// deleteFn returns error → process returns nil, tasks already in DB
	const chatID = "chat-delfail"
	forwards := makeTestForwards(chatID, 2)

	db := openForwardTestDB(t)
	fwdBuf := &mockForwardBufferForPipeline{}
	taskStore := newMockForwardTaskStore("inbox-id", inboxProject())
	extractor := &mockForwardExtractor{tasks: []model.Task{{Summary: "Задача"}}}

	p := buildForwardPipeline(db,
		&mockForwardClassifier{class: model.ClassPromise},
		extractor,
		fwdBuf, taskStore, &mockForwardEventStore{}, &mockForwardPushQueue{},
		&mockForwardBroker{}, nil, nil)

	deleteErr := errors.New("telegram delete error")
	payload := &model.ForwardBatchPayload{
		ChatID:      chatID,
		Forwards:    forwards,
		Counterpart: "Вася",
		DeleteFn: func(_ context.Context, _ []string) error {
			return deleteErr
		},
		ReplyFn: func(_ context.Context, _ string) error { return nil },
	}
	msg := model.Message{Kind: model.MessageKindForwardBatch, ForwardBatch: payload}

	// Must NOT return an error even though DeleteFn failed.
	if err := p.Process(context.Background(), msg); err != nil {
		t.Errorf("Process must not return error on DeleteFn failure, got: %v", err)
	}

	// Task must still have been created.
	if n := taskStore.createdCount(); n != 1 {
		t.Errorf("created %d tasks, want 1", n)
	}
}

func TestProcessForwardBatch_TxRollback_BufferNotCleared(t *testing.T) {
	// CreateTaskTx returns error → tx rolled back → buffer NOT cleared
	const chatID = "chat-rollback"
	forwards := makeTestForwards(chatID, 2)

	db := openForwardTestDB(t)
	fwdBuf := &mockForwardBufferForPipeline{}
	taskStore := newMockForwardTaskStore("inbox-id", inboxProject())
	taskStore.createTaskErr = errors.New("db write error")

	extractor := &mockForwardExtractor{tasks: []model.Task{{Summary: "Задача"}}}

	p := buildForwardPipeline(db,
		&mockForwardClassifier{class: model.ClassPromise},
		extractor,
		fwdBuf, taskStore, &mockForwardEventStore{}, &mockForwardPushQueue{},
		&mockForwardBroker{}, nil, nil)

	var replied []string
	payload := &model.ForwardBatchPayload{
		ChatID:      chatID,
		Forwards:    forwards,
		Counterpart: "Вася",
		ReplyFn: func(_ context.Context, text string) error {
			replied = append(replied, text)
			return nil
		},
	}
	msg := model.Message{Kind: model.MessageKindForwardBatch, ForwardBatch: payload}

	// Process should return an error since CreateTaskTx failed.
	err := p.Process(context.Background(), msg)
	if err == nil {
		t.Error("Process must return error when CreateTaskTx fails")
	}

	// Buffer must NOT have been cleared (tx rolled back).
	if n := fwdBuf.deletedCount(); n != 0 {
		t.Errorf("buffer deleted %d times after tx rollback, want 0", n)
	}

	// ReplyFn must NOT have been called (no successful task creation).
	if len(replied) != 0 {
		t.Errorf("ReplyFn called %d times after tx rollback, want 0", len(replied))
	}
}

func TestProcessForwardBatch_NilPayload_ReturnsNil(t *testing.T) {
	p := New(Config{Logger: discardLogger})
	msg := model.Message{Kind: model.MessageKindForwardBatch, ForwardBatch: nil}
	if err := p.Process(context.Background(), msg); err != nil {
		t.Errorf("Process must return nil for nil payload, got: %v", err)
	}
}

func TestProcessForwardBatch_MissingDependencies_ReturnsNil(t *testing.T) {
	// Pipeline with no ForwardBatch deps must handle ForwardBatch silently.
	p := New(Config{Logger: discardLogger})
	payload := &model.ForwardBatchPayload{
		ChatID:   "chat-nodeps",
		Forwards: makeTestForwards("chat-nodeps", 1),
		ReplyFn:  func(_ context.Context, _ string) error { return nil },
	}
	msg := model.Message{Kind: model.MessageKindForwardBatch, ForwardBatch: payload}
	if err := p.Process(context.Background(), msg); err != nil {
		t.Errorf("Process must return nil without ForwardBatch deps, got: %v", err)
	}
}
