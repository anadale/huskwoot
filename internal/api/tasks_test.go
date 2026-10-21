package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/api"
	"github.com/anadale/huskwoot/internal/devices"
	"github.com/anadale/huskwoot/internal/events"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/push"
	"github.com/anadale/huskwoot/internal/storage"
	"github.com/anadale/huskwoot/internal/usecase"
)

// tasksTestHarness assembles live TaskService+ProjectService dependencies on top
// of SQLite and prepares a valid bearer token for authenticated requests.
type tasksTestHarness struct {
	t          *testing.T
	db         *sql.DB
	server     *api.Server
	token      string
	device     *model.Device
	projectSvc model.ProjectService
	taskSvc    model.TaskService
	inboxID    string
}

func newTasksHarness(t *testing.T) *tasksTestHarness {
	t.Helper()
	db := openTestDB(t)

	sqliteTasks, err := storage.NewSQLiteTaskStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteTaskStore: %v", err)
	}
	tasks := storage.NewCachedTaskStore(sqliteTasks)
	meta := storage.NewSQLiteMetaStore(db)
	eventStore := events.NewSQLiteEventStore(db)
	pushQueue := push.NewSQLitePushQueue(db)
	broker := events.NewBroker(events.BrokerConfig{})
	deviceStore := devices.NewSQLiteDeviceStore(db)

	projectSvc := usecase.NewProjectService(usecase.ProjectServiceDeps{
		DB:        db,
		Tasks:     tasks,
		Meta:      meta,
		AutoTasks: storage.NewSQLiteAutoTaskStore(db),
		Events:    eventStore,
		Devices:   deviceStore,
		Queue:     pushQueue,
		Broker:    broker,
	})
	taskSvc := usecase.NewTaskService(usecase.TaskServiceDeps{
		DB:      db,
		Tasks:   tasks,
		Events:  eventStore,
		Devices: deviceStore,
		Queue:   pushQueue,
		Broker:  broker,
	})

	token := "tasks-test-token"
	device := createTestDevice(t, db, "test-device", token)

	srv := api.New(api.Config{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:       db,
		Devices:  deviceStore,
		Projects: projectSvc,
		Tasks:    taskSvc,
		Owner:    api.OwnerInfo{UserName: "Oliver", TelegramUserID: 42},
	})

	return &tasksTestHarness{
		t:          t,
		db:         db,
		server:     srv,
		token:      token,
		device:     device,
		projectSvc: projectSvc,
		taskSvc:    taskSvc,
		inboxID:    sqliteTasks.DefaultProjectID(),
	}
}

func (h *tasksTestHarness) do(method, target string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	var r *http.Request
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("marshal body: %v", err)
		}
		r = httptest.NewRequest(method, target, bytes.NewReader(buf))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r.Header.Set("Authorization", "Bearer "+h.token)
	rec := httptest.NewRecorder()
	h.server.Handler().ServeHTTP(rec, r)
	return rec
}

// taskResp describes the task snapshot in /v1/tasks/* responses.
type taskResp struct {
	ID          string          `json:"id"`
	Number      int             `json:"number"`
	Ref         string          `json:"ref"`
	ProjectID   string          `json:"projectId"`
	ProjectSlug string          `json:"projectSlug"`
	Summary     string          `json:"summary"`
	Details     string          `json:"details"`
	Topic       string          `json:"topic"`
	Status      string          `json:"status"`
	Priority    int             `json:"priority"`
	Deadline    *time.Time      `json:"deadline,omitempty"`
	Source      *taskSourceResp `json:"source,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
	ClosedAt    *time.Time      `json:"closedAt,omitempty"`
}

// taskSourceResp describes the task source in /v1/tasks/* responses.
type taskSourceResp struct {
	Kind    string     `json:"kind"`
	Name    string     `json:"name"`
	Subject string     `json:"subject"`
	Author  string     `json:"author"`
	Text    string     `json:"text"`
	At      *time.Time `json:"at"`
	ReplyTo *struct {
		Author string `json:"author"`
		Text   string `json:"text"`
	} `json:"replyTo"`
	Link string `json:"link"`
	Via  string `json:"via"`
}

type listResp struct {
	Tasks      []taskResp `json:"tasks"`
	NextCursor string     `json:"nextCursor"`
}

// ---- tests ----

func TestCreateTaskDeletedDestinationReturns404BeforePrecheck(t *testing.T) {
	h := newTasksHarness(t)
	p, err := h.projectSvc.CreateProject(context.Background(), model.CreateProjectRequest{Name: "Already deleted"})
	if err != nil {
		t.Fatal(err)
	}
	rec := h.do(http.MethodGet, "/v1/projects/"+p.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET before deletion status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = h.do(http.MethodDelete, "/v1/projects/"+p.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE status=%d body=%s", rec.Code, rec.Body.String())
	}
	before, err := events.NewSQLiteEventStore(h.db).MaxSeq(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rec = h.do(http.MethodPost, "/v1/tasks", map[string]string{"projectId": p.ID, "summary": "Do not create"})
	assertTaskDestinationNotFound(t, rec)
	rec = h.do(http.MethodGet, "/v1/projects/"+p.ID, nil)
	assertTaskDestinationNotFound(t, rec)
	after, err := events.NewSQLiteEventStore(h.db).MaxSeq(context.Background())
	if err != nil || after != before {
		t.Fatalf("failed create emitted events: before=%d after=%d err=%v", before, after, err)
	}
}

type taskCreatePrecheckBarrier struct {
	model.DeviceStore
	started chan struct{}
	resume  chan struct{}
}

func (b *taskCreatePrecheckBarrier) ListActiveIDs(ctx context.Context) ([]string, error) {
	close(b.started)
	select {
	case <-b.resume:
		return b.DeviceStore.ListActiveIDs(ctx)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestCreateTaskDestinationDeletedAfterPrecheckReturns404(t *testing.T) {
	h := newTasksHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := h.projectSvc.CreateProject(ctx, model.CreateProjectRequest{Name: "Deleted between check and write"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := storage.NewSQLiteTaskStore(h.db)
	if err != nil {
		t.Fatal(err)
	}
	barrier := &taskCreatePrecheckBarrier{
		DeviceStore: devices.NewSQLiteDeviceStore(h.db),
		started:     make(chan struct{}), resume: make(chan struct{}),
	}
	taskService := usecase.NewTaskService(usecase.TaskServiceDeps{
		DB: h.db, Tasks: raw, Devices: barrier,
		Events: events.NewSQLiteEventStore(h.db),
		Queue:  push.NewSQLitePushQueue(h.db), Broker: events.NewBroker(events.BrokerConfig{}),
	})
	server := api.New(api.Config{
		DB: h.db, Devices: barrier.DeviceStore, Projects: h.projectSvc, Tasks: taskService,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	body, err := json.Marshal(map[string]string{"projectId": p.ID, "summary": "Must not survive deletion"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/tasks", bytes.NewReader(body)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", "application/json")
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		result <- rec
	}()
	// The real TaskService has finished GetProject but has not begun its write transaction.
	select {
	case <-barrier.started:
	case rec := <-result:
		t.Fatalf("POST completed before precheck barrier: status=%d body=%s", rec.Code, rec.Body.String())
	case <-ctx.Done():
		t.Fatal("task create did not reach precheck barrier")
	}
	rec := h.do(http.MethodGet, "/v1/projects/"+p.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET at precheck status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = h.do(http.MethodDelete, "/v1/projects/"+p.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE before write status=%d body=%s", rec.Code, rec.Body.String())
	}
	before, err := events.NewSQLiteEventStore(h.db).MaxSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	close(barrier.resume)
	select {
	case rec := <-result:
		assertTaskDestinationNotFound(t, rec)
	case <-ctx.Done():
		t.Fatal("task create did not finish after deletion committed")
	}
	rec = h.do(http.MethodGet, "/v1/projects/"+p.ID, nil)
	assertTaskDestinationNotFound(t, rec)
	after, err := events.NewSQLiteEventStore(h.db).MaxSeq(ctx)
	if err != nil || after != before {
		t.Fatalf("failed tx create emitted events: before=%d after=%d err=%v", before, after, err)
	}
	tasks, err := h.taskSvc.ListTasks(ctx, "", model.TaskFilter{})
	if err != nil || len(tasks) != 0 {
		t.Fatalf("failed create persisted tasks=%+v err=%v", tasks, err)
	}
}

func TestCreateTaskInvalidInputIs422NotMissingProject(t *testing.T) {
	h := newTasksHarness(t)
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{name: "blank summary", body: map[string]any{"projectId": h.inboxID, "summary": " "}},
		{name: "invalid priority", body: map[string]any{"projectId": h.inboxID, "summary": "Valid", "priority": 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(http.MethodPost, "/v1/tasks", tc.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("POST status=%d want 422 body=%s", rec.Code, rec.Body.String())
			}
			if body := readErrorBody(t, rec.Body); body.Error.Code != api.ErrorCodeUnprocessable {
				t.Fatalf("POST invalid input code=%q", body.Error.Code)
			}
			rec = h.do(http.MethodGet, "/v1/projects/"+h.inboxID, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET valid project status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func assertTaskDestinationNotFound(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
	if body := readErrorBody(t, rec.Body); body.Error.Code != api.ErrorCodeNotFound {
		t.Fatalf("error code=%q want not_found", body.Error.Code)
	}
}

func TestListTasksFilterByProjectAndStatus(t *testing.T) {
	h := newTasksHarness(t)
	ctx := context.Background()

	work, err := h.projectSvc.CreateProject(ctx, model.CreateProjectRequest{Name: "Работа"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	home, err := h.projectSvc.CreateProject(ctx, model.CreateProjectRequest{Name: "Дом"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// In project "Work": one open task, one done task.
	openTask, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{ProjectID: work.ID, Summary: "работа-open"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	doneTask, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{ProjectID: work.ID, Summary: "работа-done"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := h.taskSvc.CompleteTask(ctx, doneTask.ID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	// In project "Home" — a task that must not pass the filter.
	if _, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{ProjectID: home.ID, Summary: "дом-open"}); err != nil {
		t.Fatalf("CreateTask home: %v", err)
	}

	rec := h.do(http.MethodGet, "/v1/tasks?project_id="+work.ID+"&status=open", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp listResp
	decodeJSONResp(t, rec.Body, &resp)
	if len(resp.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(resp.Tasks))
	}
	if resp.Tasks[0].ID != openTask.ID {
		t.Fatalf("wrong task returned: %+v", resp.Tasks[0])
	}
	if resp.Tasks[0].Status != "open" {
		t.Fatalf("status=%q, want open", resp.Tasks[0].Status)
	}
}

func TestListTasksSinceCursor(t *testing.T) {
	h := newTasksHarness(t)
	ctx := context.Background()

	p, err := h.projectSvc.CreateProject(ctx, model.CreateProjectRequest{Name: "Since"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	older, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{ProjectID: p.ID, Summary: "старая"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	// UpdatedAt is stored with second precision (RFC3339), so wait a full
	// second to ensure "newer" falls on the next second.
	time.Sleep(1100 * time.Millisecond)
	newer, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{ProjectID: p.ID, Summary: "новая"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	cut := newer.UpdatedAt

	rec := h.do(http.MethodGet, "/v1/tasks?project_id="+p.ID+"&since="+cut.UTC().Format(time.RFC3339), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp listResp
	decodeJSONResp(t, rec.Body, &resp)
	if len(resp.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d: %+v", len(resp.Tasks), resp.Tasks)
	}
	if resp.Tasks[0].ID != newer.ID {
		t.Fatalf("wrong task returned: got %s, want %s", resp.Tasks[0].ID, newer.ID)
	}
	if resp.Tasks[0].ID == older.ID {
		t.Fatalf("older task must not have been included")
	}
}

func TestListTasksPagination(t *testing.T) {
	h := newTasksHarness(t)
	ctx := context.Background()

	p, err := h.projectSvc.CreateProject(ctx, model.CreateProjectRequest{Name: "Paging"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	const total = 7
	for i := 0; i < total; i++ {
		if _, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{ProjectID: p.ID, Summary: "t"}); err != nil {
			t.Fatalf("CreateTask %d: %v", i, err)
		}
		// Ensure distinct updated_at values between tasks.
		time.Sleep(2 * time.Millisecond)
	}

	rec := h.do(http.MethodGet, "/v1/tasks?project_id="+p.ID+"&limit=3", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var first listResp
	decodeJSONResp(t, rec.Body, &first)
	if len(first.Tasks) != 3 {
		t.Fatalf("first page: expected 3 tasks, got %d", len(first.Tasks))
	}
	if first.NextCursor == "" {
		t.Fatal("expected next_cursor on first page")
	}

	rec = h.do(http.MethodGet, "/v1/tasks?project_id="+p.ID+"&limit=3&cursor="+first.NextCursor, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 (page 2); body=%s", rec.Code, rec.Body.String())
	}
	var second listResp
	decodeJSONResp(t, rec.Body, &second)
	if len(second.Tasks) != 3 {
		t.Fatalf("second page: expected 3 tasks, got %d", len(second.Tasks))
	}
	if second.NextCursor == "" {
		t.Fatal("expected next_cursor on second page")
	}

	rec = h.do(http.MethodGet, "/v1/tasks?project_id="+p.ID+"&limit=3&cursor="+second.NextCursor, nil)
	var third listResp
	decodeJSONResp(t, rec.Body, &third)
	if len(third.Tasks) != 1 {
		t.Fatalf("third page: expected 1 task, got %d", len(third.Tasks))
	}
	if third.NextCursor != "" {
		t.Fatalf("next_cursor must be absent on last page: %q", third.NextCursor)
	}

	// The union of all pages must not contain duplicates.
	seen := map[string]bool{}
	for _, lst := range []listResp{first, second, third} {
		for _, task := range lst.Tasks {
			if seen[task.ID] {
				t.Fatalf("duplicate across pages: %s", task.ID)
			}
			seen[task.ID] = true
		}
	}
	if len(seen) != total {
		t.Fatalf("expected %d unique tasks, got %d", total, len(seen))
	}
}

func TestGetTaskByID(t *testing.T) {
	h := newTasksHarness(t)
	ctx := context.Background()

	task, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{Summary: "вот это"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	rec := h.do(http.MethodGet, "/v1/tasks/"+task.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp taskResp
	decodeJSONResp(t, rec.Body, &resp)
	if resp.ID != task.ID {
		t.Fatalf("id=%q, want %q", resp.ID, task.ID)
	}
	if resp.Summary != "вот это" {
		t.Fatalf("summary=%q", resp.Summary)
	}
}

func TestGetTaskByRefSlug42(t *testing.T) {
	h := newTasksHarness(t)
	ctx := context.Background()

	task, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{Summary: "по ref"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	rec := h.do(http.MethodGet, "/v1/tasks/by-ref/inbox-1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp taskResp
	decodeJSONResp(t, rec.Body, &resp)
	if resp.ID != task.ID {
		t.Fatalf("id=%q, want %q", resp.ID, task.ID)
	}
	if resp.Number != 1 {
		t.Fatalf("number=%d, want 1", resp.Number)
	}
	if resp.ProjectSlug != "inbox" {
		t.Fatalf("project_slug=%q, want inbox", resp.ProjectSlug)
	}
}

func TestCreateTaskIntoInboxIfNoProjectID(t *testing.T) {
	h := newTasksHarness(t)

	rec := h.do(http.MethodPost, "/v1/tasks", map[string]any{
		"summary": "в Inbox",
		"details": "детали",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var resp taskResp
	decodeJSONResp(t, rec.Body, &resp)
	if resp.ProjectID != h.inboxID {
		t.Fatalf("project_id=%q, want inbox %q", resp.ProjectID, h.inboxID)
	}
	if resp.ProjectSlug != "inbox" {
		t.Fatalf("project_slug=%q, want inbox", resp.ProjectSlug)
	}
	if resp.Summary != "в Inbox" {
		t.Fatalf("summary=%q", resp.Summary)
	}
	if resp.Status != "open" {
		t.Fatalf("status=%q, want open", resp.Status)
	}
}

func TestCreateTaskStoresClientSourceAndVia(t *testing.T) {
	h := newTasksHarness(t)

	rec := h.do(http.MethodPost, "/v1/tasks", map[string]any{"summary": "из REST"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var resp taskResp
	decodeJSONResp(t, rec.Body, &resp)

	task, err := h.taskSvc.GetTask(context.Background(), resp.ID)
	if err != nil || task == nil {
		t.Fatalf("GetTask: %v %v", task, err)
	}
	if task.Source.Kind != "client" || task.Source.ID != h.device.ID {
		t.Errorf("source=%+v, want client source for device %q", task.Source, h.device.ID)
	}
	if task.SourceDetails == nil || task.SourceDetails.Via != model.SourceViaAPI {
		t.Fatalf("source details=%+v, want via %q", task.SourceDetails, model.SourceViaAPI)
	}
	if task.SourceDetails.Text != "" || task.SourceDetails.Author != "" || task.SourceDetails.At != nil {
		t.Errorf("source details=%+v, want only via", task.SourceDetails)
	}
}

func TestGetTaskReturnsPriorityAndSource(t *testing.T) {
	h := newTasksHarness(t)
	ctx := context.Background()

	at := time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC)
	task, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{
		Summary:  "починить деплой",
		Priority: model.TaskPriorityMedium,
		Source:   model.Source{Kind: "telegram", ID: "-1001234567890", Name: "Команда"},
		SourceDetails: &model.TaskSourceDetails{
			Name:    "Команда",
			Author:  "Алиса",
			Text:    "Завтра починю деплой",
			At:      &at,
			ReplyTo: &model.TaskSourceQuote{Author: "Боб", Text: "Кто починит деплой?"},
			Link:    "https://t.me/c/1234567890/42",
			Via:     model.SourceViaMessage,
		},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	for _, target := range []string{"/v1/tasks/" + task.ID, "/v1/tasks/by-ref/inbox-1"} {
		t.Run(target, func(t *testing.T) {
			rec := h.do(http.MethodGet, target, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			var resp taskResp
			decodeJSONResp(t, rec.Body, &resp)
			if resp.Priority != 2 {
				t.Errorf("priority=%d, want 2", resp.Priority)
			}
			src := resp.Source
			if src == nil {
				t.Fatal("source is missing")
			}
			if src.Kind != "telegram" || src.Name != "Команда" || src.Author != "Алиса" ||
				src.Text != "Завтра починю деплой" || src.Link != "https://t.me/c/1234567890/42" ||
				src.Via != model.SourceViaMessage || src.Subject != "" {
				t.Errorf("source=%+v", src)
			}
			if src.At == nil || !src.At.Equal(at) {
				t.Errorf("source.at=%v, want %v", src.At, at)
			}
			if src.ReplyTo == nil || src.ReplyTo.Author != "Боб" || src.ReplyTo.Text != "Кто починит деплой?" {
				t.Errorf("source.replyTo=%+v", src.ReplyTo)
			}
		})
	}

	rec := h.do(http.MethodGet, "/v1/tasks", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var list listResp
	decodeJSONResp(t, rec.Body, &list)
	if len(list.Tasks) != 1 || list.Tasks[0].Priority != 2 || list.Tasks[0].Source == nil ||
		list.Tasks[0].Source.Author != "Алиса" {
		t.Fatalf("list tasks=%+v, want the task with priority and source", list.Tasks)
	}
}

func TestGetTaskWithoutSourceOmitsSourceAndKeepsPriority(t *testing.T) {
	h := newTasksHarness(t)

	task, err := h.taskSvc.CreateTask(context.Background(), model.CreateTaskRequest{Summary: "без источника"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	rec := h.do(http.MethodGet, "/v1/tasks/"+task.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var raw map[string]json.RawMessage
	decodeJSONResp(t, rec.Body, &raw)
	if got := string(raw["priority"]); got != "0" {
		t.Errorf("priority=%q, want 0 (always present)", got)
	}
	if v, ok := raw["source"]; ok {
		t.Errorf("source=%s, want the key omitted", v)
	}
}

func TestCreateTaskPriority(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		want model.TaskPriority
	}{
		{name: "without priority", body: map[string]any{"summary": "обычная"}, want: model.TaskPriorityNone},
		{name: "explicit zero", body: map[string]any{"summary": "ноль", "priority": 0}, want: model.TaskPriorityNone},
		{name: "low", body: map[string]any{"summary": "низкая", "priority": 1}, want: model.TaskPriorityLow},
		{name: "high", body: map[string]any{"summary": "срочная", "priority": 3}, want: model.TaskPriorityHigh},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTasksHarness(t)

			rec := h.do(http.MethodPost, "/v1/tasks", tc.body)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status=%d, want 201; body=%s", rec.Code, rec.Body.String())
			}
			var resp taskResp
			decodeJSONResp(t, rec.Body, &resp)
			if resp.Priority != int(tc.want) {
				t.Errorf("response priority=%d, want %d", resp.Priority, tc.want)
			}
			if resp.Source == nil || resp.Source.Kind != "client" || resp.Source.Via != model.SourceViaAPI {
				t.Errorf("response source=%+v, want kind client via api", resp.Source)
			}

			task, err := h.taskSvc.GetTask(context.Background(), resp.ID)
			if err != nil || task == nil {
				t.Fatalf("GetTask: %v %v", task, err)
			}
			if task.Priority != tc.want {
				t.Errorf("stored priority=%d, want %d", task.Priority, tc.want)
			}
		})
	}
}

func TestUpdateTaskPatchesPriority(t *testing.T) {
	h := newTasksHarness(t)
	ctx := context.Background()

	task, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{Summary: "приоритет", Priority: model.TaskPriorityHigh})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	steps := []struct {
		name       string
		body       map[string]any
		wantStatus int
		want       model.TaskPriority
	}{
		{name: "lower to low", body: map[string]any{"priority": 1}, wantStatus: http.StatusOK, want: model.TaskPriorityLow},
		{name: "out of range keeps value", body: map[string]any{"priority": 7}, wantStatus: http.StatusUnprocessableEntity, want: model.TaskPriorityLow},
		{name: "wrong type keeps value", body: map[string]any{"priority": "high"}, wantStatus: http.StatusUnprocessableEntity, want: model.TaskPriorityLow},
		{name: "with summary", body: map[string]any{"priority": 2, "summary": "приоритет 2"}, wantStatus: http.StatusOK, want: model.TaskPriorityMedium},
		{name: "reset to zero", body: map[string]any{"priority": 0}, wantStatus: http.StatusOK, want: model.TaskPriorityNone},
	}
	for _, step := range steps {
		rec := h.do(http.MethodPatch, "/v1/tasks/"+task.ID, step.body)
		if rec.Code != step.wantStatus {
			t.Fatalf("%s: status=%d, want %d; body=%s", step.name, rec.Code, step.wantStatus, rec.Body.String())
		}
		if rec.Code == http.StatusOK {
			var resp taskResp
			decodeJSONResp(t, rec.Body, &resp)
			if resp.Priority != int(step.want) {
				t.Errorf("%s: response priority=%d, want %d", step.name, resp.Priority, step.want)
			}
		} else {
			var errResp struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			decodeJSONResp(t, rec.Body, &errResp)
			if errResp.Error.Code != api.ErrorCodeUnprocessable {
				t.Errorf("%s: error code=%q, want %q", step.name, errResp.Error.Code, api.ErrorCodeUnprocessable)
			}
		}
		stored, err := h.taskSvc.GetTask(ctx, task.ID)
		if err != nil || stored == nil {
			t.Fatalf("%s: GetTask: %v %v", step.name, stored, err)
		}
		if stored.Priority != step.want {
			t.Errorf("%s: stored priority=%d, want %d", step.name, stored.Priority, step.want)
		}
	}
}

func TestUpdateTaskPatchesFields(t *testing.T) {
	h := newTasksHarness(t)
	ctx := context.Background()

	task, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{Summary: "старт"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	rec := h.do(http.MethodPatch, "/v1/tasks/"+task.ID, map[string]any{
		"details": "новые подробности",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp taskResp
	decodeJSONResp(t, rec.Body, &resp)
	if resp.Details != "новые подробности" {
		t.Fatalf("details=%q", resp.Details)
	}
	if resp.ID != task.ID {
		t.Fatalf("id changed")
	}
}

// PATCH несуществующей задачи должен возвращать 404, а не 500: иначе клиентский drainer
// интерпретирует 5xx как «ретраить» и бомбит сервер.
func TestUpdateTaskMissingReturns404(t *testing.T) {
	h := newTasksHarness(t)

	missingID := "79811A80-AEFA-4C05-A0E4-A9FAE1C53A43"
	rec := h.do(http.MethodPatch, "/v1/tasks/"+missingID, map[string]any{"summary": "x"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestUpdateTaskPatchesSummary(t *testing.T) {
	h := newTasksHarness(t)
	ctx := context.Background()

	t.Run("plain summary", func(t *testing.T) {
		task, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{Summary: "первое имя"})
		if err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		rec := h.do(http.MethodPatch, "/v1/tasks/"+task.ID, map[string]any{"summary": "второе имя"})
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var resp taskResp
		decodeJSONResp(t, rec.Body, &resp)
		if resp.Summary != "второе имя" {
			t.Fatalf("summary=%q, want %q", resp.Summary, "второе имя")
		}
	})

	t.Run("summary trimmed", func(t *testing.T) {
		task, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{Summary: "до трима"})
		if err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		rec := h.do(http.MethodPatch, "/v1/tasks/"+task.ID, map[string]any{"summary": "  hello  "})
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var resp taskResp
		decodeJSONResp(t, rec.Body, &resp)
		if resp.Summary != "hello" {
			t.Fatalf("summary=%q, want %q", resp.Summary, "hello")
		}
	})

	t.Run("summary with details", func(t *testing.T) {
		task, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{Summary: "оригинал"})
		if err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		rec := h.do(http.MethodPatch, "/v1/tasks/"+task.ID, map[string]any{
			"summary": "новое имя",
			"details": "новые подробности",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var resp taskResp
		decodeJSONResp(t, rec.Body, &resp)
		if resp.Summary != "новое имя" {
			t.Fatalf("summary=%q, want %q", resp.Summary, "новое имя")
		}
		if resp.Details != "новые подробности" {
			t.Fatalf("details=%q, want %q", resp.Details, "новые подробности")
		}
	})

	t.Run("empty summary rejected", func(t *testing.T) {
		task, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{Summary: "не трогай меня"})
		if err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		rec := h.do(http.MethodPatch, "/v1/tasks/"+task.ID, map[string]any{"summary": "   "})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status=%d, want 422; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("non-string summary rejected", func(t *testing.T) {
		task, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{Summary: "не меняй"})
		if err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		rec := h.do(http.MethodPatch, "/v1/tasks/"+task.ID, map[string]any{"summary": 42})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
		}
	})
}

func TestCompleteTaskEndpointSetsStatus(t *testing.T) {
	h := newTasksHarness(t)
	ctx := context.Background()

	task, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{Summary: "будет завершена"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	rec := h.do(http.MethodPost, "/v1/tasks/"+task.ID+"/complete", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp taskResp
	decodeJSONResp(t, rec.Body, &resp)
	if resp.Status != "done" {
		t.Fatalf("status=%q, want done", resp.Status)
	}
	if resp.ClosedAt == nil {
		t.Fatal("closed_at not set")
	}
}

func TestReopenTaskEndpointSetsStatus(t *testing.T) {
	h := newTasksHarness(t)
	ctx := context.Background()

	task, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{Summary: "переоткроем"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := h.taskSvc.CompleteTask(ctx, task.ID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	rec := h.do(http.MethodPost, "/v1/tasks/"+task.ID+"/reopen", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp taskResp
	decodeJSONResp(t, rec.Body, &resp)
	if resp.Status != "open" {
		t.Fatalf("status=%q, want open", resp.Status)
	}
	if resp.ClosedAt != nil {
		t.Fatalf("closed_at must be nil, got %v", resp.ClosedAt)
	}
}

func TestMoveTaskEndpointReassignsNumber(t *testing.T) {
	h := newTasksHarness(t)
	ctx := context.Background()

	target, err := h.projectSvc.CreateProject(ctx, model.CreateProjectRequest{Name: "Цель"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	// Add one task to the target project to verify number re-assignment.
	if _, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{ProjectID: target.ID, Summary: "первая"}); err != nil {
		t.Fatalf("CreateTask target: %v", err)
	}

	task, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{Summary: "будет перенесена"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	originalNumber := task.Number

	rec := h.do(http.MethodPost, "/v1/tasks/"+task.ID+"/move", map[string]string{"projectId": target.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp taskResp
	decodeJSONResp(t, rec.Body, &resp)
	if resp.ProjectID != target.ID {
		t.Fatalf("project_id=%q, want %q", resp.ProjectID, target.ID)
	}
	if resp.ProjectSlug != "tsel" && resp.ProjectSlug != target.Slug {
		t.Fatalf("project_slug=%q, want %q", resp.ProjectSlug, target.Slug)
	}
	if resp.Number == originalNumber {
		t.Fatalf("number was not reassigned: was %d, still %d", originalNumber, resp.Number)
	}
	if resp.Number != 2 {
		t.Fatalf("expected number=2 in target project, got %d", resp.Number)
	}
}

func TestDeleteTaskSoftDelete(t *testing.T) {
	h := newTasksHarness(t)
	ctx := context.Background()

	task, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{Summary: "удалим"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	rec := h.do(http.MethodDelete, "/v1/tasks/"+task.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp taskResp
	decodeJSONResp(t, rec.Body, &resp)
	if resp.Status != "cancelled" {
		t.Fatalf("status=%q, want cancelled", resp.Status)
	}

	// Task is not physically deleted — GET by id still works.
	rec2 := h.do(http.MethodGet, "/v1/tasks/"+task.ID, nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET after delete: status=%d, want 200; body=%s", rec2.Code, rec2.Body.String())
	}
	var again taskResp
	decodeJSONResp(t, rec2.Body, &again)
	if again.Status != "cancelled" {
		t.Fatalf("after-delete status=%q, want cancelled", again.Status)
	}
}

func TestTasksValidationErrors(t *testing.T) {
	h := newTasksHarness(t)

	cases := []struct {
		name   string
		method string
		path   string
		body   any
		want   int
	}{
		{"empty summary", http.MethodPost, "/v1/tasks", map[string]string{"summary": ""}, http.StatusUnprocessableEntity},
		{"whitespace summary", http.MethodPost, "/v1/tasks", map[string]string{"summary": "   "}, http.StatusUnprocessableEntity},
		{"unknown field", http.MethodPost, "/v1/tasks", map[string]any{"summary": "X", "garbage": true}, http.StatusBadRequest},
		{"invalid ref", http.MethodGet, "/v1/tasks/by-ref/broken", nil, http.StatusBadRequest},
		{"not found by id", http.MethodGet, "/v1/tasks/unknown-uuid", nil, http.StatusNotFound},
		{"move without project_id", http.MethodPost, "/v1/tasks/any-id/move", map[string]string{"projectId": ""}, http.StatusUnprocessableEntity},
		{"patch unknown field", http.MethodPatch, "/v1/tasks/any-id", map[string]any{"garbage": "new"}, http.StatusBadRequest},
		{"unknown field with priority", http.MethodPost, "/v1/tasks", map[string]any{"summary": "X", "priority": 1, "garbage": true}, http.StatusBadRequest},
		{"patch unknown field with priority", http.MethodPatch, "/v1/tasks/any-id", map[string]any{"priority": 1, "garbage": "new"}, http.StatusBadRequest},
		{"priority above range", http.MethodPost, "/v1/tasks", map[string]any{"summary": "X", "priority": 4}, http.StatusUnprocessableEntity},
		{"priority below range", http.MethodPost, "/v1/tasks", map[string]any{"summary": "X", "priority": -1}, http.StatusUnprocessableEntity},
		{"priority string", http.MethodPost, "/v1/tasks", map[string]any{"summary": "X", "priority": "high"}, http.StatusUnprocessableEntity},
		{"priority fraction", http.MethodPost, "/v1/tasks", map[string]any{"summary": "X", "priority": 1.5}, http.StatusUnprocessableEntity},
		{"priority null", http.MethodPost, "/v1/tasks", map[string]any{"summary": "X", "priority": nil}, http.StatusUnprocessableEntity},
		{"patch priority above range", http.MethodPatch, "/v1/tasks/any-id", map[string]any{"priority": 4}, http.StatusUnprocessableEntity},
		{"patch priority below range", http.MethodPatch, "/v1/tasks/any-id", map[string]any{"priority": -1}, http.StatusUnprocessableEntity},
		{"patch priority string", http.MethodPatch, "/v1/tasks/any-id", map[string]any{"priority": "2"}, http.StatusUnprocessableEntity},
		{"patch priority fraction", http.MethodPatch, "/v1/tasks/any-id", map[string]any{"priority": 2.5}, http.StatusUnprocessableEntity},
		{"patch priority bool", http.MethodPatch, "/v1/tasks/any-id", map[string]any{"priority": true}, http.StatusUnprocessableEntity},
		{"patch priority null", http.MethodPatch, "/v1/tasks/any-id", map[string]any{"priority": nil}, http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(tc.method, tc.path, tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status=%d, want %d; body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}
