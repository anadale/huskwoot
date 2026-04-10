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
		DB:      db,
		Tasks:   tasks,
		Meta:    meta,
		Events:  eventStore,
		Devices: deviceStore,
		Queue:   pushQueue,
		Broker:  broker,
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
	ID          string     `json:"id"`
	Number      int        `json:"number"`
	Ref         string     `json:"ref"`
	ProjectID   string     `json:"projectId"`
	ProjectSlug string     `json:"projectSlug"`
	Summary     string     `json:"summary"`
	Details     string     `json:"details"`
	Topic       string     `json:"topic"`
	Status      string     `json:"status"`
	Deadline    *time.Time `json:"deadline,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	ClosedAt    *time.Time `json:"closedAt,omitempty"`
}

type listResp struct {
	Tasks      []taskResp `json:"tasks"`
	NextCursor string     `json:"nextCursor"`
}

// ---- tests ----

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
		{"patch unknown field", http.MethodPatch, "/v1/tasks/any-id", map[string]any{"summary": "new"}, http.StatusBadRequest},
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
