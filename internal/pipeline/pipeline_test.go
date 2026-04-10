package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// --- Mocks ---

type mockClassifier struct {
	class model.Classification
	err   error
	calls int
	mu    sync.Mutex
}

func (m *mockClassifier) Classify(_ context.Context, _ model.Message) (model.Classification, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.class, m.err
}

type mockExtractor struct {
	tasks       []model.Task
	err         error
	calls       int
	lastHistory []model.HistoryEntry
	mu          sync.Mutex
}

func (m *mockExtractor) Extract(_ context.Context, msg model.Message, history []model.HistoryEntry) ([]model.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.lastHistory = history
	result := make([]model.Task, len(m.tasks))
	for i, t := range m.tasks {
		if t.SourceMessage.ID == "" {
			t.SourceMessage = msg
		}
		result[i] = t
	}
	return result, m.err
}

type mockCommandExtractor struct {
	cmd   model.Command
	err   error
	calls int
	mu    sync.Mutex
}

func (m *mockCommandExtractor) Extract(_ context.Context, _ model.Message) (model.Command, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.cmd, m.err
}

type mockCommandHandler struct {
	err   error
	calls int
	cmds  []model.Command
	name  string
	mu    sync.Mutex
}

func (m *mockCommandHandler) Handle(_ context.Context, cmd model.Command) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.cmds = append(m.cmds, cmd)
	return m.err
}

func (m *mockCommandHandler) Name() string { return m.name }

type mockNotifier struct {
	err   error
	calls int
	tasks []model.Task
	mu    sync.Mutex
}

func (m *mockNotifier) Notify(_ context.Context, tasks []model.Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.tasks = append(m.tasks, tasks...)
	return m.err
}

func (m *mockNotifier) Name() string { return "mock-notifier" }

// mockTaskService implements model.TaskService for tests.
type mockTaskService struct {
	mu           sync.Mutex
	createdTasks []model.Task
	createErr    error
}

func (m *mockTaskService) CreateTask(_ context.Context, req model.CreateTaskRequest) (*model.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createErr != nil {
		return nil, m.createErr
	}
	task := model.Task{
		ID:        "task-id",
		ProjectID: req.ProjectID,
		Summary:   req.Summary,
		Details:   req.Details,
		Topic:     req.Topic,
		Deadline:  req.Deadline,
		Source:    req.Source,
	}
	m.createdTasks = append(m.createdTasks, task)
	return &task, nil
}

func (m *mockTaskService) CreateTasks(_ context.Context, req model.CreateTasksRequest) ([]model.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createErr != nil {
		return nil, m.createErr
	}
	var result []model.Task
	for _, r := range req.Tasks {
		task := model.Task{
			ID:        "task-id",
			ProjectID: req.ProjectID,
			Summary:   r.Summary,
			Details:   r.Details,
			Topic:     r.Topic,
			Deadline:  r.Deadline,
			Source:    r.Source,
		}
		m.createdTasks = append(m.createdTasks, task)
		result = append(result, task)
	}
	return result, nil
}

func (m *mockTaskService) UpdateTask(_ context.Context, _ string, _ model.TaskUpdate) (*model.Task, error) {
	return nil, nil
}
func (m *mockTaskService) CompleteTask(_ context.Context, _ string) (*model.Task, error) {
	return nil, nil
}
func (m *mockTaskService) ReopenTask(_ context.Context, _ string) (*model.Task, error) {
	return nil, nil
}
func (m *mockTaskService) MoveTask(_ context.Context, _, _ string) (*model.Task, error) {
	return nil, nil
}
func (m *mockTaskService) ListTasks(_ context.Context, _ string, _ model.TaskFilter) ([]model.Task, error) {
	return nil, nil
}
func (m *mockTaskService) GetTask(_ context.Context, _ string) (*model.Task, error) { return nil, nil }
func (m *mockTaskService) GetTaskByRef(_ context.Context, _ string, _ int) (*model.Task, error) {
	return nil, nil
}

// mockProjectService implements model.ProjectService for tests.
type mockProjectService struct {
	mu         sync.Mutex
	resolveID  string
	resolveErr error
}

func (m *mockProjectService) ResolveProjectForChannel(_ context.Context, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.resolveID, m.resolveErr
}

func (m *mockProjectService) CreateProject(_ context.Context, _ model.CreateProjectRequest) (*model.Project, error) {
	return nil, nil
}
func (m *mockProjectService) UpdateProject(_ context.Context, _ string, _ model.ProjectUpdate) (*model.Project, error) {
	return nil, nil
}
func (m *mockProjectService) ListProjects(_ context.Context) ([]model.Project, error) {
	return nil, nil
}
func (m *mockProjectService) FindProjectByName(_ context.Context, _ string) (*model.Project, error) {
	return nil, nil
}
func (m *mockProjectService) EnsureChannelProject(_ context.Context, _, _ string) (*model.Project, error) {
	return nil, nil
}

// mockChatService implements model.ChatService for tests.
type mockChatService struct {
	mu    sync.Mutex
	calls int
	msgs  []model.Message
	reply model.ChatReply
	err   error
}

func (m *mockChatService) HandleMessage(_ context.Context, msg model.Message) (model.ChatReply, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.msgs = append(m.msgs, msg)
	return m.reply, m.err
}

// --- Helpers ---

var discardLogger = slog.New(slog.NewTextHandler(nopWriter{}, nil))

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

func testTasks() []model.Task {
	return []model.Task{
		{
			Summary:    "выполнить задачу",
			Confidence: 0.9,
			Source:     model.Source{Kind: "telegram", ID: "chat-1"},
		},
	}
}

func testGroupMsg(author string) model.Message {
	return model.Message{
		ID:         "msg-1",
		Author:     author,
		AuthorName: author,
		Text:       "сделаю завтра",
		Timestamp:  time.Now(),
		Kind:       model.MessageKindGroup,
		Source:     model.Source{Kind: "telegram", ID: "chat-1", Name: "Группа"},
	}
}

func testGroupDirectMsg(author string) model.Message {
	return model.Message{
		ID:         "msg-gd",
		Author:     author,
		AuthorName: author,
		Text:       "@бот покажи задачи",
		Timestamp:  time.Now(),
		Kind:       model.MessageKindGroupDirect,
		Source:     model.Source{Kind: "telegram", ID: "chat-1", Name: "Группа"},
	}
}

func testDMMsg(author string) model.Message {
	return model.Message{
		ID:         "msg-dm",
		Author:     author,
		AuthorName: author,
		Text:       "опубликую новую версию",
		Timestamp:  time.Now(),
		Kind:       model.MessageKindDM,
		Source:     model.Source{Kind: "telegram", ID: "dm", Name: "DM"},
	}
}

func testBatchMsg() model.Message {
	return model.Message{
		ID:     "imap-1",
		Author: "boss@example.com",
		Text:   "Gregory обещал подготовить отчёт",
		Kind:   model.MessageKindBatch,
		Source: model.Source{Kind: "imap", ID: "user@example.com:INBOX", Name: "Рабочая почта"},
	}
}

// testConfig returns a base Config for tests.
func testConfig() Config {
	return Config{
		OwnerIDs: []string{"owner-123"},
		Aliases:  []string{"gregory", "greg"},
		Logger:   discardLogger,
	}
}

// groupClassifiers builds a classifier map for Group and Batch kinds only.
func groupClassifiers(c model.Classifier) map[model.MessageKind]model.Classifier {
	return map[model.MessageKind]model.Classifier{
		model.MessageKindBatch: c,
		model.MessageKindGroup: c,
	}
}

// groupExtractors builds an extractor map for Group and Batch kinds only.
func groupExtractors(e model.Extractor) map[model.MessageKind]model.Extractor {
	return map[model.MessageKind]model.Extractor{
		model.MessageKindBatch: e,
		model.MessageKindGroup: e,
	}
}

// --- Tests: DM routing → ChatService ---

func TestProcess_DM_GoesToChat_ReplyFnCalled(t *testing.T) {
	chat := &mockChatService{reply: model.ChatReply{Text: "Выполнено!"}}
	cfg := testConfig()
	cfg.Chat = chat

	p := New(cfg)

	var replySent string
	msg := testDMMsg("owner-123")
	msg.ReplyFn = func(_ context.Context, text string) error {
		replySent = text
		return nil
	}

	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if chat.calls != 1 {
		t.Errorf("chat.HandleMessage called %d times, want 1", chat.calls)
	}
	if replySent != "Выполнено!" {
		t.Errorf("ReplyFn got %q, want %q", replySent, "Выполнено!")
	}
}

func TestProcess_GroupDirect_GoesToChat_ReplyFnCalled(t *testing.T) {
	chat := &mockChatService{reply: model.ChatReply{Text: "Задачи: нет"}}
	cfg := testConfig()
	cfg.Chat = chat

	p := New(cfg)

	var replySent string
	msg := testGroupDirectMsg("owner-123")
	msg.ReplyFn = func(_ context.Context, text string) error {
		replySent = text
		return nil
	}

	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if chat.calls != 1 {
		t.Errorf("chat.HandleMessage called %d times, want 1", chat.calls)
	}
	if replySent != "Задачи: нет" {
		t.Errorf("ReplyFn got %q, want %q", replySent, "Задачи: нет")
	}
}

func TestProcess_DM_ChatError_LoggedNoReturnError(t *testing.T) {
	chatErr := errors.New("ошибка ChatService")
	chat := &mockChatService{err: chatErr}

	cfg := testConfig()
	cfg.Chat = chat
	p := New(cfg)

	msg := testDMMsg("owner-123")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process must not return error on ChatService failure: %v", err)
	}

	if chat.calls != 1 {
		t.Errorf("chat.HandleMessage must be called, called %d times", chat.calls)
	}
}

func TestProcess_DM_NoChatService_NoError(t *testing.T) {
	p := New(testConfig())

	msg := testDMMsg("owner-123")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process must not return error when ChatService is absent: %v", err)
	}
}

func TestProcess_DM_ReplyFnNil_NoError(t *testing.T) {
	chat := &mockChatService{reply: model.ChatReply{Text: "ответ"}}
	cfg := testConfig()
	cfg.Chat = chat
	p := New(cfg)

	msg := testDMMsg("owner-123")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process must not return error with nil ReplyFn: %v", err)
	}
}

func TestProcess_DM_EmptyReply_ReplyFnNotCalled(t *testing.T) {
	chat := &mockChatService{reply: model.ChatReply{Text: ""}}
	var replyFnCalled bool

	cfg := testConfig()
	cfg.Chat = chat
	p := New(cfg)

	msg := testDMMsg("owner-123")
	msg.ReplyFn = func(_ context.Context, _ string) error {
		replyFnCalled = true
		return nil
	}

	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if replyFnCalled {
		t.Error("ReplyFn must not be called on empty ChatService reply")
	}
}

func TestProcess_DM_EmptyReply_ReactDoneCalledToCleanPending(t *testing.T) {
	chat := &mockChatService{reply: model.ChatReply{Text: ""}}
	tracker := &reactTracker{}

	cfg := testConfig()
	cfg.Chat = chat
	p := New(cfg)

	msg := testDMMsg("owner-123")
	msg.ReactFn = tracker.fn

	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	hasDone := false
	for _, e := range tracker.emojis {
		if e == "👍" {
			hasDone = true
		}
	}
	if !hasDone {
		t.Error("👍 reaction must be set even on empty reply to clear ✍️")
	}
}

// --- Tests: DM/GroupDirect routing — reactions ---

func TestProcess_DM_ReactPendingThenDone(t *testing.T) {
	chat := &mockChatService{reply: model.ChatReply{Text: "ок"}}
	tracker := &reactTracker{}

	cfg := testConfig()
	cfg.Chat = chat
	p := New(cfg)

	msg := testDMMsg("owner-123")
	msg.ReactFn = tracker.fn

	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if len(tracker.emojis) != 2 {
		t.Fatalf("want 2 ReactFn calls, got %d: %v", len(tracker.emojis), tracker.emojis)
	}
	if tracker.emojis[0] != "✍️" {
		t.Errorf("first reaction = %q, want %q", tracker.emojis[0], "✍️")
	}
	if tracker.emojis[1] != "👍" {
		t.Errorf("second reaction = %q, want %q", tracker.emojis[1], "👍")
	}
}

func TestProcess_DM_ChatError_ReactDoneCalledToCleanPending(t *testing.T) {
	chat := &mockChatService{err: errors.New("сбой")}
	tracker := &reactTracker{}

	cfg := testConfig()
	cfg.Chat = chat
	p := New(cfg)

	msg := testDMMsg("owner-123")
	msg.ReactFn = tracker.fn

	_ = p.Process(context.Background(), msg)

	if len(tracker.emojis) != 2 {
		t.Fatalf("want 2 ReactFn calls, got %d: %v", len(tracker.emojis), tracker.emojis)
	}
	if tracker.emojis[0] != "✍️" {
		t.Errorf("first reaction = %q, want %q", tracker.emojis[0], "✍️")
	}
	if tracker.emojis[1] != "👍" {
		t.Errorf("second reaction = %q, want %q", tracker.emojis[1], "👍")
	}
}

// --- Tests: Group/Batch → TaskService ---

func TestProcess_Group_TaskServiceCreateTasks(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{tasks: testTasks()}
	taskSvc := &mockTaskService{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	cfg.Tasks = taskSvc
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if classifier.calls != 1 {
		t.Errorf("classifier.Classify called %d times, want 1", classifier.calls)
	}
	if extractor.calls != 1 {
		t.Errorf("extractor.Extract called %d times, want 1", extractor.calls)
	}
	taskSvc.mu.Lock()
	n := len(taskSvc.createdTasks)
	taskSvc.mu.Unlock()
	if n != 1 {
		t.Errorf("taskSvc.CreateTasks created %d tasks, want 1", n)
	}
}

func TestProcess_Batch_GoesToTaskService(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{tasks: testTasks()}
	taskSvc := &mockTaskService{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	cfg.Tasks = taskSvc
	p := New(cfg)

	msg := testBatchMsg()
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	taskSvc.mu.Lock()
	n := len(taskSvc.createdTasks)
	taskSvc.mu.Unlock()
	if n != 1 {
		t.Errorf("taskSvc should receive task for Batch, got %d", n)
	}
}

func TestProcess_TaskServiceError_NotifierStillCalled(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{tasks: testTasks()}
	taskSvc := &mockTaskService{createErr: errors.New("ошибка записи")}
	notifier := &mockNotifier{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	cfg.Tasks = taskSvc
	cfg.Notifiers = []model.Notifier{notifier}
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process must not return error on TaskService failure: %v", err)
	}

	if notifier.calls != 1 {
		t.Errorf("notifier.Notify must be called even on TaskService error, called %d times", notifier.calls)
	}
}

// --- Tests: ProjectService.ResolveProjectForChannel ---

func TestProcess_ClassPromise_UsesProjectServiceToResolveChannel(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{tasks: []model.Task{{Summary: "задача", Confidence: 0.9}}}
	projects := &mockProjectService{resolveID: "project-uuid-42"}
	taskSvc := &mockTaskService{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	cfg.Projects = projects
	cfg.Tasks = taskSvc
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	taskSvc.mu.Lock()
	defer taskSvc.mu.Unlock()
	if len(taskSvc.createdTasks) == 0 {
		t.Fatal("taskSvc received no tasks")
	}
	if taskSvc.createdTasks[0].ProjectID != "project-uuid-42" {
		t.Errorf("ProjectID = %q, want %q", taskSvc.createdTasks[0].ProjectID, "project-uuid-42")
	}
}

func TestProcess_ClassPromise_ProjectServiceFallbackToDefault(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{tasks: []model.Task{{Summary: "задача", Confidence: 0.9}}}
	projects := &mockProjectService{resolveID: "inbox-default-uuid"}
	taskSvc := &mockTaskService{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	cfg.Projects = projects
	cfg.Tasks = taskSvc
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	taskSvc.mu.Lock()
	defer taskSvc.mu.Unlock()
	if len(taskSvc.createdTasks) == 0 {
		t.Fatal("taskSvc received no tasks")
	}
	if taskSvc.createdTasks[0].ProjectID != "inbox-default-uuid" {
		t.Errorf("ProjectID = %q, want %q", taskSvc.createdTasks[0].ProjectID, "inbox-default-uuid")
	}
}

func TestProcess_ClassPromise_ProjectServiceError_ContinuesWithEmptyProjectID(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{tasks: []model.Task{{Summary: "задача", Confidence: 0.9}}}
	projects := &mockProjectService{resolveErr: errors.New("ошибка ProjectService")}
	taskSvc := &mockTaskService{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	cfg.Projects = projects
	cfg.Tasks = taskSvc
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process must not return error on ProjectService failure: %v", err)
	}

	taskSvc.mu.Lock()
	defer taskSvc.mu.Unlock()
	if len(taskSvc.createdTasks) == 0 {
		t.Fatal("taskSvc must receive task even on ProjectService error")
	}
}

// --- Tests: Skip classification ---

func TestProcess_ClassSkip_NoTaskService(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassSkip}
	extractor := &mockExtractor{tasks: testTasks()}
	notifier := &mockNotifier{}
	taskSvc := &mockTaskService{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	cfg.Tasks = taskSvc
	cfg.Notifiers = []model.Notifier{notifier}
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if extractor.calls != 0 {
		t.Errorf("extractor must not be called on ClassSkip, called %d times", extractor.calls)
	}
	taskSvc.mu.Lock()
	n := len(taskSvc.createdTasks)
	taskSvc.mu.Unlock()
	if n != 0 || notifier.calls != 0 {
		t.Error("taskSvc and notifier must not be called on ClassSkip")
	}
}

// --- Tests: Promise classification ---

func TestProcess_ClassPromise_Group_HappyPath(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{tasks: testTasks()}
	notifier := &mockNotifier{}
	taskSvc := &mockTaskService{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	cfg.Tasks = taskSvc
	cfg.Notifiers = []model.Notifier{notifier}
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if classifier.calls != 1 {
		t.Errorf("classifier.Classify called %d times, want 1", classifier.calls)
	}
	if extractor.calls != 1 {
		t.Errorf("extractor.Extract called %d times, want 1", extractor.calls)
	}
	taskSvc.mu.Lock()
	n := len(taskSvc.createdTasks)
	taskSvc.mu.Unlock()
	if n != 1 {
		t.Errorf("taskSvc created %d tasks, want 1", n)
	}
	if notifier.calls != 1 {
		t.Errorf("notifier.Notify called %d times, want 1", notifier.calls)
	}
}

func TestProcess_ClassPromise_Group_HistoryFnCalled(t *testing.T) {
	historyEntries := []model.HistoryEntry{
		{AuthorName: "owner-123", Text: "сделаю завтра"},
		{AuthorName: "other", Text: "ещё одно сообщение"},
	}
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{tasks: testTasks()}
	taskSvc := &mockTaskService{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	cfg.Tasks = taskSvc
	p := New(cfg)

	historyFnCalled := false
	msg := testGroupMsg("owner-123")
	msg.HistoryFn = func(_ context.Context) ([]model.HistoryEntry, error) {
		historyFnCalled = true
		return historyEntries, nil
	}

	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if !historyFnCalled {
		t.Error("HistoryFn must be called when processing Promise")
	}
	if len(extractor.lastHistory) != 2 {
		t.Errorf("extractor got %d history entries, want 2", len(extractor.lastHistory))
	}
}

func TestProcess_ClassPromise_Group_HistoryFnNil_EmptyHistory(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{tasks: testTasks()}
	taskSvc := &mockTaskService{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	cfg.Tasks = taskSvc
	p := New(cfg)

	msg := testGroupMsg("owner-123")

	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if extractor.calls != 1 {
		t.Errorf("extractor.Extract called %d times, want 1", extractor.calls)
	}
	if len(extractor.lastHistory) != 0 {
		t.Errorf("extractor must receive empty history when HistoryFn==nil, got %d entries", len(extractor.lastHistory))
	}
}

func TestProcess_ClassPromise_Group_HistoryFnError_ContinuesProcessing(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{tasks: testTasks()}
	taskSvc := &mockTaskService{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	cfg.Tasks = taskSvc
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	msg.HistoryFn = func(_ context.Context) ([]model.HistoryEntry, error) {
		return nil, errors.New("ошибка получения истории")
	}

	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process must not return error on HistoryFn failure: %v", err)
	}

	if extractor.calls != 1 {
		t.Errorf("extractor.Extract must be called even on HistoryFn error, called %d times", extractor.calls)
	}
	taskSvc.mu.Lock()
	n := len(taskSvc.createdTasks)
	taskSvc.mu.Unlock()
	if n != 1 {
		t.Errorf("taskSvc must be called even on HistoryFn error, created %d tasks", n)
	}
}

func TestProcess_ClassPromise_Batch_BypassOwnerCheck(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{tasks: testTasks()}
	taskSvc := &mockTaskService{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	cfg.Tasks = taskSvc
	p := New(cfg)

	msg := testBatchMsg()
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	taskSvc.mu.Lock()
	n := len(taskSvc.createdTasks)
	taskSvc.mu.Unlock()
	if n != 1 {
		t.Errorf("taskSvc must be called for Batch, created %d tasks", n)
	}
}

// --- Tests: owner check ---

func TestProcess_NotOwner_Group_Skipped(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	p := New(cfg)

	msg := testGroupMsg("stranger-999")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if classifier.calls != 0 {
		t.Errorf("classifier must not be called for foreign message, called %d times", classifier.calls)
	}
}

func TestProcess_NotOwner_DM_Skipped(t *testing.T) {
	chat := &mockChatService{reply: model.ChatReply{Text: "ответ"}}

	cfg := testConfig()
	cfg.Chat = chat
	p := New(cfg)

	msg := testDMMsg("stranger-999")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if chat.calls != 0 {
		t.Errorf("ChatService must not be called for foreign DM, called %d times", chat.calls)
	}
}

func TestProcess_NotOwner_GroupDirect_ReachesChat(t *testing.T) {
	chat := &mockChatService{reply: model.ChatReply{Text: "ответ"}}

	cfg := testConfig()
	cfg.Chat = chat
	p := New(cfg)

	msg := testGroupDirectMsg("stranger-999")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if chat.calls != 1 {
		t.Errorf("ChatService must be called for GroupDirect regardless of owner, called %d times", chat.calls)
	}
}

func TestProcess_OwnerAlias_Processed(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{tasks: testTasks()}
	taskSvc := &mockTaskService{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	cfg.Tasks = taskSvc
	p := New(cfg)

	msg := testGroupMsg("gregory")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	taskSvc.mu.Lock()
	n := len(taskSvc.createdTasks)
	taskSvc.mu.Unlock()
	if n != 1 {
		t.Errorf("taskSvc must be called for alias, created %d tasks", n)
	}
}

func TestProcess_MultipleOwnerIDs(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{tasks: testTasks()}
	taskSvc := &mockTaskService{}

	cfg := Config{
		OwnerIDs:    []string{"owner-111", "owner-222"},
		Classifiers: groupClassifiers(classifier),
		Extractors:  groupExtractors(extractor),
		Tasks:       taskSvc,
		Logger:      discardLogger,
	}
	p := New(cfg)

	for _, ownerID := range []string{"owner-111", "owner-222"} {
		msg := testGroupMsg(ownerID)
		if err := p.Process(context.Background(), msg); err != nil {
			t.Fatalf("Process(%q) returned error: %v", ownerID, err)
		}
	}

	taskSvc.mu.Lock()
	n := len(taskSvc.createdTasks)
	taskSvc.mu.Unlock()
	if n != 2 {
		t.Errorf("taskSvc must create 2 tasks, created %d", n)
	}
}

// --- Tests: Command classification ---

func TestProcess_ClassCommand_HappyPath(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassCommand}
	cmdExtractor := &mockCommandExtractor{cmd: model.Command{Type: "set_project_name"}}
	handler := &mockCommandHandler{name: "set_project_name"}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.CommandExtractor = cmdExtractor
	cfg.CommandHandlers = []model.CommandHandler{handler}
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if cmdExtractor.calls != 1 {
		t.Errorf("commandExtractor.Extract called %d times, want 1", cmdExtractor.calls)
	}
	if handler.calls != 1 {
		t.Errorf("commandHandler.Handle called %d times, want 1", handler.calls)
	}
	if len(handler.cmds) == 0 || handler.cmds[0].Type != "set_project_name" {
		t.Error("handler received wrong command")
	}
}

func TestProcess_ClassCommand_NoExtractor_NoError(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassCommand}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process must not return error when CommandExtractor is absent: %v", err)
	}
}

func TestProcess_ClassCommand_MultipleHandlers_OnlyMatchingCalled(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassCommand}
	cmdExtractor := &mockCommandExtractor{cmd: model.Command{Type: "set_project_name"}}
	handler1 := &mockCommandHandler{name: "set_project_name"}
	handler2 := &mockCommandHandler{name: "other_command"}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.CommandExtractor = cmdExtractor
	cfg.CommandHandlers = []model.CommandHandler{handler1, handler2}
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if handler1.calls != 1 {
		t.Errorf("matching handler called %d times, want 1", handler1.calls)
	}
	if handler2.calls != 0 {
		t.Errorf("non-matching handler called %d times, want 0", handler2.calls)
	}
}

// --- Tests: classifier selection by Kind ---

func TestProcess_ClassifierByKind_GroupUsesGroupClassifier(t *testing.T) {
	groupClassifier := &mockClassifier{class: model.ClassPromise}
	batchClassifier := &mockClassifier{class: model.ClassSkip}
	extractor := &mockExtractor{tasks: testTasks()}
	taskSvc := &mockTaskService{}

	clsMap := map[model.MessageKind]model.Classifier{
		model.MessageKindGroup: groupClassifier,
		model.MessageKindBatch: batchClassifier,
	}
	extrMap := map[model.MessageKind]model.Extractor{
		model.MessageKindGroup: extractor,
	}

	cfg := testConfig()
	cfg.Classifiers = clsMap
	cfg.Extractors = extrMap
	cfg.Tasks = taskSvc
	p := New(cfg)

	groupMsg := testGroupMsg("owner-123")
	if err := p.Process(context.Background(), groupMsg); err != nil {
		t.Fatalf("Process returned error for Group: %v", err)
	}

	if groupClassifier.calls != 1 {
		t.Errorf("groupClassifier called %d times, want 1", groupClassifier.calls)
	}
	if batchClassifier.calls != 0 {
		t.Errorf("batchClassifier must not be called for Group, called %d times", batchClassifier.calls)
	}

	taskSvc.mu.Lock()
	n := len(taskSvc.createdTasks)
	taskSvc.mu.Unlock()
	if n != 1 {
		t.Errorf("taskSvc must receive task for Group Promise, got %d", n)
	}
}

func TestProcess_DM_DoesNotUseClassifier(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	chat := &mockChatService{reply: model.ChatReply{Text: "ок"}}

	clsMap := map[model.MessageKind]model.Classifier{
		model.MessageKindDM: classifier,
	}

	cfg := testConfig()
	cfg.Classifiers = clsMap
	cfg.Chat = chat
	p := New(cfg)

	msg := testDMMsg("owner-123")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if classifier.calls != 0 {
		t.Errorf("classifier must not be called for DM, called %d times", classifier.calls)
	}
	if chat.calls != 1 {
		t.Errorf("chat.HandleMessage called %d times, want 1", chat.calls)
	}
}

func TestProcess_NoClassifier_Skipped(t *testing.T) {
	p := New(testConfig())

	msg := testGroupMsg("owner-123")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
}

// --- Tests: errors ---

func TestProcess_ClassifierError(t *testing.T) {
	errClassify := errors.New("ошибка классификатора")
	classifier := &mockClassifier{err: errClassify}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	err := p.Process(context.Background(), msg)
	if err == nil {
		t.Fatal("Process must return error on classifier failure")
	}
	if !errors.Is(err, errClassify) {
		t.Errorf("want classifier error, got: %v", err)
	}
}

func TestProcess_ExtractorError(t *testing.T) {
	errExtract := errors.New("ошибка экстрактора")
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{err: errExtract}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	err := p.Process(context.Background(), msg)
	if err == nil {
		t.Fatal("Process must return error on extractor failure")
	}
	if !errors.Is(err, errExtract) {
		t.Errorf("want extractor error, got: %v", err)
	}
}

func TestProcess_CommandExtractorError(t *testing.T) {
	errCmd := errors.New("ошибка commandExtractor")
	classifier := &mockClassifier{class: model.ClassCommand}
	cmdExtractor := &mockCommandExtractor{err: errCmd}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.CommandExtractor = cmdExtractor
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	err := p.Process(context.Background(), msg)
	if err == nil {
		t.Fatal("Process must return error on commandExtractor failure")
	}
	if !errors.Is(err, errCmd) {
		t.Errorf("want commandExtractor error, got: %v", err)
	}
}

// --- Tests: Topic ---

func TestProcess_OriginTopic_ClearedForGroup(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{tasks: []model.Task{{
		Summary: "задача", Confidence: 0.9,
		Topic: "Тема из экстрактора",
	}}}
	taskSvc := &mockTaskService{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	cfg.Tasks = taskSvc
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	taskSvc.mu.Lock()
	defer taskSvc.mu.Unlock()
	if len(taskSvc.createdTasks) == 0 {
		t.Fatal("taskSvc received no tasks")
	}
	if taskSvc.createdTasks[0].Topic != "" {
		t.Errorf("Topic must be empty for Group, got %q", taskSvc.createdTasks[0].Topic)
	}
}

func TestProcess_OriginTopic_PreservedForBatch(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{tasks: []model.Task{{
		Summary: "задача", Confidence: 0.9,
		Topic: "Тема письма",
	}}}
	taskSvc := &mockTaskService{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	cfg.Tasks = taskSvc
	p := New(cfg)

	msg := testBatchMsg()
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	taskSvc.mu.Lock()
	defer taskSvc.mu.Unlock()
	if len(taskSvc.createdTasks) == 0 {
		t.Fatal("taskSvc received no tasks")
	}
	if taskSvc.createdTasks[0].Topic != "Тема письма" {
		t.Errorf("Topic must be preserved for Batch, got %q", taskSvc.createdTasks[0].Topic)
	}
}

func TestProcess_BatchMessage_TaskSourcePreserved(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{tasks: []model.Task{{Summary: "задача", Confidence: 0.9}}}
	taskSvc := &mockTaskService{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	cfg.Tasks = taskSvc
	p := New(cfg)

	msg := model.Message{
		ID:      "imap-1",
		Author:  "boss@example.com",
		Text:    "нужно подготовить отчёт",
		Subject: "Встреча по Q1",
		Kind:    model.MessageKindBatch,
		Source:  model.Source{Kind: "imap", ID: "user@example.com:INBOX", Name: "Рабочая почта"},
	}
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	taskSvc.mu.Lock()
	defer taskSvc.mu.Unlock()
	if len(taskSvc.createdTasks) == 0 {
		t.Fatal("taskSvc received no tasks")
	}
}

// --- Tests: notifiers ---

func TestProcess_MultipleNotifiers_AllCalled(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{tasks: testTasks()}
	taskSvc := &mockTaskService{}
	notifier1 := &mockNotifier{}
	notifier2 := &mockNotifier{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	cfg.Tasks = taskSvc
	cfg.Notifiers = []model.Notifier{notifier1, notifier2}
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	for i, n := range []*mockNotifier{notifier1, notifier2} {
		if n.calls != 1 {
			t.Errorf("notifier%d.Notify called %d times, want 1", i+1, n.calls)
		}
	}
}

func TestProcess_EmptyTasks_NoTaskServiceOrNotifier(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{tasks: nil}
	taskSvc := &mockTaskService{}
	notifier := &mockNotifier{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	cfg.Tasks = taskSvc
	cfg.Notifiers = []model.Notifier{notifier}
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	taskSvc.mu.Lock()
	n := len(taskSvc.createdTasks)
	taskSvc.mu.Unlock()
	if n != 0 {
		t.Errorf("taskSvc must not be called on empty task list, created %d", n)
	}
	if notifier.calls != 0 {
		t.Errorf("notifier.Notify must not be called on empty task list, called %d times", notifier.calls)
	}
}

func TestProcess_MultipleTasks_AllCreated(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	tasks := []model.Task{
		{Summary: "подготовить отчёт", Confidence: 0.9},
		{Summary: "отправить письмо", Confidence: 0.85},
	}
	extractor := &mockExtractor{tasks: tasks}
	taskSvc := &mockTaskService{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	cfg.Tasks = taskSvc
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	taskSvc.mu.Lock()
	n := len(taskSvc.createdTasks)
	taskSvc.mu.Unlock()
	if n != 2 {
		t.Fatalf("taskSvc got %d tasks, want 2", n)
	}
}

func TestNew_DefaultLogger(t *testing.T) {
	p := New(Config{})
	if p.logger == nil {
		t.Error("logger must not be nil")
	}
}

// --- Tests: two-stage reactions ---

// reactTracker records the sequence of emoji calls to ReactFn.
type reactTracker struct {
	mu     sync.Mutex
	emojis []string
	err    error
}

func (r *reactTracker) fn(ctx context.Context, emoji string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emojis = append(r.emojis, emoji)
	return r.err
}

func TestProcess_ClassPromise_ReactPendingThenDone(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{tasks: testTasks()}
	taskSvc := &mockTaskService{}
	tracker := &reactTracker{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	cfg.Tasks = taskSvc
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	msg.ReactFn = tracker.fn

	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if len(tracker.emojis) != 2 {
		t.Fatalf("want 2 ReactFn calls, got %d: %v", len(tracker.emojis), tracker.emojis)
	}
	if tracker.emojis[0] != "✍️" {
		t.Errorf("first reaction = %q, want %q", tracker.emojis[0], "✍️")
	}
	if tracker.emojis[1] != "👍" {
		t.Errorf("second reaction = %q, want %q", tracker.emojis[1], "👍")
	}
}

func TestProcess_ClassCommand_ReactPendingThenDone(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassCommand}
	cmdExtractor := &mockCommandExtractor{cmd: model.Command{Type: "set_project_name"}}
	handler := &mockCommandHandler{name: "set_project_name"}
	tracker := &reactTracker{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.CommandExtractor = cmdExtractor
	cfg.CommandHandlers = []model.CommandHandler{handler}
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	msg.ReactFn = tracker.fn

	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if len(tracker.emojis) != 2 {
		t.Fatalf("want 2 ReactFn calls, got %d: %v", len(tracker.emojis), tracker.emojis)
	}
	if tracker.emojis[0] != "✍️" {
		t.Errorf("first reaction = %q, want %q", tracker.emojis[0], "✍️")
	}
	if tracker.emojis[1] != "👍" {
		t.Errorf("second reaction = %q, want %q", tracker.emojis[1], "👍")
	}
}

func TestProcess_ClassCommand_Unhandled_ReactClear(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassCommand}
	cmdExtractor := &mockCommandExtractor{cmd: model.Command{Type: "unknown_command"}}
	handler := &mockCommandHandler{name: "set_project_name"}
	tracker := &reactTracker{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.CommandExtractor = cmdExtractor
	cfg.CommandHandlers = []model.CommandHandler{handler}
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	msg.ReactFn = tracker.fn

	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if len(tracker.emojis) != 2 {
		t.Fatalf("want 2 ReactFn calls (pending + clear), got %d: %v", len(tracker.emojis), tracker.emojis)
	}
	if tracker.emojis[0] != "✍️" {
		t.Errorf("first reaction = %q, want %q", tracker.emojis[0], "✍️")
	}
	if tracker.emojis[1] != "" {
		t.Errorf("second reaction = %q, want empty string (reaction removal)", tracker.emojis[1])
	}
}

func TestProcess_ClassPromise_ExtractorError_ReactDoneNotCalled(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{err: errors.New("ошибка экстрактора")}
	tracker := &reactTracker{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	msg.ReactFn = tracker.fn

	_ = p.Process(context.Background(), msg)

	if len(tracker.emojis) != 1 {
		t.Fatalf("want 1 ReactFn call, got %d: %v", len(tracker.emojis), tracker.emojis)
	}
	if tracker.emojis[0] != "✍️" {
		t.Errorf("reaction = %q, want %q", tracker.emojis[0], "✍️")
	}
}

func TestProcess_ClassSkip_NoReact(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassSkip}
	tracker := &reactTracker{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	msg.ReactFn = tracker.fn

	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if len(tracker.emojis) != 0 {
		t.Errorf("ReactFn must not be called on ClassSkip, called with: %v", tracker.emojis)
	}
}

func TestProcess_NilReactFn_NoError(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{tasks: testTasks()}
	taskSvc := &mockTaskService{}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	cfg.Tasks = taskSvc
	p := New(cfg)

	msg := testGroupMsg("owner-123")

	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process must not return error with nil ReactFn: %v", err)
	}
}

func TestProcess_GroupDirect_ReactPendingThenDone(t *testing.T) {
	chat := &mockChatService{reply: model.ChatReply{Text: "список задач"}}
	tracker := &reactTracker{}

	cfg := testConfig()
	cfg.Chat = chat
	p := New(cfg)

	msg := testGroupDirectMsg("owner-123")
	msg.ReactFn = tracker.fn

	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if len(tracker.emojis) != 2 {
		t.Fatalf("want 2 ReactFn calls, got %d: %v", len(tracker.emojis), tracker.emojis)
	}
	if tracker.emojis[0] != "✍️" {
		t.Errorf("first reaction = %q, want %q", tracker.emojis[0], "✍️")
	}
	if tracker.emojis[1] != "👍" {
		t.Errorf("second reaction = %q, want %q", tracker.emojis[1], "👍")
	}
}

func TestProcess_ClassCommand_HandlerError_ReturnsError(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassCommand}
	cmdExtractor := &mockCommandExtractor{cmd: model.Command{Type: "set_project_name"}}
	handler := &mockCommandHandler{name: "set_project_name", err: errors.New("хранилище недоступно")}

	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.CommandExtractor = cmdExtractor
	cfg.CommandHandlers = []model.CommandHandler{handler}
	p := New(cfg)

	msg := testGroupMsg("owner-123")
	err := p.Process(context.Background(), msg)
	if err == nil {
		t.Fatal("Process must return error on handler failure")
	}
	if !errors.Is(err, handler.err) {
		t.Errorf("error %v does not wrap original handler error", err)
	}
}

func TestProcess_GroupDirect_NonOwner_Allowed(t *testing.T) {
	chat := &mockChatService{reply: model.ChatReply{Text: "ответ бота"}}

	cfg := testConfig()
	cfg.Chat = chat
	p := New(cfg)

	msg := testGroupDirectMsg("stranger-456")
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if chat.calls != 1 {
		t.Errorf("ChatService called %d times, want 1", chat.calls)
	}
}

func TestProcess_Agent_Error_ReactDoneCalledToCleanPending(t *testing.T) {
	chat := &mockChatService{err: errors.New("AI недоступен")}
	tracker := &reactTracker{}

	cfg := testConfig()
	cfg.Chat = chat
	p := New(cfg)

	msg := testDMMsg("owner-123")
	msg.ReactFn = tracker.fn

	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process must not return error on ChatService failure: %v", err)
	}
	hasDone := false
	for _, e := range tracker.emojis {
		if e == "👍" {
			hasDone = true
		}
	}
	if !hasDone {
		t.Error("👍 reaction must be set on ChatService error to clear ✍️")
	}
}
