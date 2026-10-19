package api_test

import (
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

type syncTestHarness struct {
	t          *testing.T
	db         *sql.DB
	server     *api.Server
	token      string
	taskSvc    model.TaskService
	projectSvc model.ProjectService
}

func newSyncHarness(t *testing.T) *syncTestHarness {
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
		DB: db, Tasks: tasks, Meta: meta, Events: eventStore,
		Devices: deviceStore, Queue: pushQueue, Broker: broker,
	})
	taskSvc := usecase.NewTaskService(usecase.TaskServiceDeps{
		DB: db, Tasks: tasks, Events: eventStore,
		Devices: deviceStore, Queue: pushQueue, Broker: broker,
	})

	token := "sync-test-token"
	createTestDevice(t, db, "sync-device", token)

	srv := api.New(api.Config{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:       db,
		Devices:  deviceStore,
		Projects: projectSvc,
		Tasks:    taskSvc,
		Events:   eventStore,
		Broker:   broker,
	})

	return &syncTestHarness{
		t: t, db: db, server: srv, token: token,
		taskSvc: taskSvc, projectSvc: projectSvc,
	}
}

func (h *syncTestHarness) get(target string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Authorization", "Bearer "+h.token)
	rec := httptest.NewRecorder()
	h.server.Handler().ServeHTTP(rec, req)
	return rec
}

func TestSyncSnapshotReturnsProjectsAndOpenTasksAndLastSeq(t *testing.T) {
	h := newSyncHarness(t)
	ctx := context.Background()

	// Create a project and three tasks, close one.
	proj, err := h.projectSvc.CreateProject(ctx, model.CreateProjectRequest{Name: "Работа"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	t1, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{ProjectID: proj.ID, Summary: "a"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{ProjectID: proj.ID, Summary: "b"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := h.taskSvc.CompleteTask(ctx, t1.ID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	rec := h.get("/v1/sync/snapshot")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Projects []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		} `json:"projects"`
		OpenTasks []struct {
			ID        string `json:"id"`
			Summary   string `json:"summary"`
			Status    string `json:"status"`
			ProjectID string `json:"projectId"`
		} `json:"openTasks"`
		LastSeq int64 `json:"lastSeq"`
	}
	decodeJSONResp(t, rec.Body, &resp)

	// Inbox + Work = at least 2 projects.
	if len(resp.Projects) < 2 {
		t.Fatalf("projects len=%d, want >=2", len(resp.Projects))
	}
	names := map[string]bool{}
	for _, p := range resp.Projects {
		names[p.Name] = true
	}
	if !names["Работа"] {
		t.Fatalf("project 'Работа' not found in %+v", names)
	}

	if len(resp.OpenTasks) != 1 {
		t.Fatalf("open_tasks len=%d, want 1 (second task)", len(resp.OpenTasks))
	}
	if resp.OpenTasks[0].Summary != "b" {
		t.Fatalf("open_tasks[0].summary=%q, want b", resp.OpenTasks[0].Summary)
	}
	if resp.OpenTasks[0].Status != "open" {
		t.Fatalf("open_tasks[0].status=%q, want open", resp.OpenTasks[0].Status)
	}

	// 2 task_created + 1 task_completed + 1 project_created = seq must be >= 4.
	if resp.LastSeq < 4 {
		t.Fatalf("last_seq=%d, want >=4", resp.LastSeq)
	}
}

func TestSyncSnapshotIncludesPriorityAndSource(t *testing.T) {
	h := newSyncHarness(t)
	ctx := context.Background()

	at := time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC)
	if _, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{
		Summary:  "ответить на письмо",
		Priority: model.TaskPriorityHigh,
		Source:   model.Source{Kind: "imap", ID: "owner@example.com"},
		SourceDetails: &model.TaskSourceDetails{
			Name:    "owner@example.com",
			Subject: "Договор",
			Author:  "Алиса",
			Text:    "Пришлю договор до пятницы",
			At:      &at,
			Via:     model.SourceViaMessage,
		},
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{Summary: "без источника"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	rec := h.get("/v1/sync/snapshot")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OpenTasks []map[string]json.RawMessage `json:"openTasks"`
	}
	decodeJSONResp(t, rec.Body, &resp)
	if len(resp.OpenTasks) != 2 {
		t.Fatalf("open_tasks len=%d, want 2", len(resp.OpenTasks))
	}

	bySummary := map[string]map[string]json.RawMessage{}
	for _, task := range resp.OpenTasks {
		var summary string
		if err := json.Unmarshal(task["summary"], &summary); err != nil {
			t.Fatalf("summary: %v", err)
		}
		bySummary[summary] = task
	}

	withSource := bySummary["ответить на письмо"]
	if got := string(withSource["priority"]); got != "3" {
		t.Errorf("priority=%s, want 3", got)
	}
	var src struct {
		Kind    string     `json:"kind"`
		Subject string     `json:"subject"`
		Author  string     `json:"author"`
		Text    string     `json:"text"`
		At      *time.Time `json:"at"`
		Via     string     `json:"via"`
	}
	if err := json.Unmarshal(withSource["source"], &src); err != nil {
		t.Fatalf("source: %v (raw %s)", err, withSource["source"])
	}
	if src.Kind != "imap" || src.Subject != "Договор" || src.Author != "Алиса" ||
		src.Text != "Пришлю договор до пятницы" || src.Via != model.SourceViaMessage {
		t.Errorf("source=%+v", src)
	}
	if src.At == nil || !src.At.Equal(at) {
		t.Errorf("source.at=%v, want %v", src.At, at)
	}

	plain := bySummary["без источника"]
	if got := string(plain["priority"]); got != "0" {
		t.Errorf("plain priority=%s, want 0", got)
	}
	if v, ok := plain["source"]; ok {
		t.Errorf("plain source=%s, want the key omitted", v)
	}
}

func TestSyncSnapshotUnauthenticatedReturns401(t *testing.T) {
	h := newSyncHarness(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/sync/snapshot", nil)
	rec := httptest.NewRecorder()
	h.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", rec.Code)
	}
}

func TestSyncSnapshotEmpty(t *testing.T) {
	h := newSyncHarness(t)
	rec := h.get("/v1/sync/snapshot")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	var resp struct {
		Projects  []map[string]any `json:"projects"`
		OpenTasks []map[string]any `json:"openTasks"`
		LastSeq   int64            `json:"lastSeq"`
	}
	decodeJSONResp(t, rec.Body, &resp)
	// Inbox is always present; no tasks; last_seq=0.
	if len(resp.OpenTasks) != 0 {
		t.Fatalf("open_tasks len=%d, want 0", len(resp.OpenTasks))
	}
	if resp.LastSeq != 0 {
		t.Fatalf("last_seq=%d, want 0", resp.LastSeq)
	}
}
