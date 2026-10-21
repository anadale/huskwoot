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
	t           *testing.T
	db          *sql.DB
	server      *api.Server
	token       string
	taskSvc     model.TaskService
	projectSvc  model.ProjectService
	rawTasks    model.TaskStore
	cachedTasks *storage.CachedTaskStore
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
		AutoTasks: storage.NewSQLiteAutoTaskStore(db),
		Devices:   deviceStore, Queue: pushQueue, Broker: broker,
	})
	taskSvc := usecase.NewTaskService(usecase.TaskServiceDeps{
		DB: db, Tasks: tasks, Events: eventStore,
		Devices: deviceStore, Queue: pushQueue, Broker: broker,
	})

	token := "sync-test-token"
	createTestDevice(t, db, "sync-device", token)

	srv := api.New(api.Config{
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:        db,
		Devices:   deviceStore,
		Projects:  projectSvc,
		Tasks:     taskSvc,
		TaskStore: sqliteTasks,
		Events:    eventStore,
		Broker:    broker,
	})

	return &syncTestHarness{
		t: t, db: db, server: srv, token: token,
		taskSvc: taskSvc, projectSvc: projectSvc,
		rawTasks: sqliteTasks, cachedTasks: tasks,
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

type heldSyncInvalidationStore struct {
	*storage.CachedTaskStore
	entered chan struct{}
	release chan struct{}
}

func (s *heldSyncInvalidationStore) Invalidate() {
	close(s.entered)
	<-s.release
	s.CachedTaskStore.Invalidate()
}

func TestSyncSnapshotOmitsDeletedProjectBeforeCacheInvalidation(t *testing.T) {
	h := newSyncHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	project, err := h.projectSvc.CreateProject(ctx, model.CreateProjectRequest{Name: "Snapshot deletion"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{ProjectID: project.ID, Summary: "Moved open task"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	warm, err := h.cachedTasks.ListProjects(ctx)
	if err != nil {
		t.Fatalf("warm cache: %v", err)
	}
	if !syncProjectsContain(warm, project.ID) {
		t.Fatal("warm cache does not contain project")
	}
	held := &heldSyncInvalidationStore{
		CachedTaskStore: h.cachedTasks,
		entered:         make(chan struct{}),
		release:         make(chan struct{}),
	}
	deletion := usecase.NewProjectService(usecase.ProjectServiceDeps{
		DB: h.db, Tasks: held, Meta: storage.NewSQLiteMetaStore(h.db),
		AutoTasks: storage.NewSQLiteAutoTaskStore(h.db),
		Events:    events.NewSQLiteEventStore(h.db),
		Devices:   devices.NewSQLiteDeviceStore(h.db), Queue: push.NewSQLitePushQueue(h.db),
		Broker: events.NewBroker(events.BrokerConfig{}),
	})
	done := make(chan error, 1)
	go func() {
		_, err := deletion.DeleteProject(ctx, project.ID)
		done <- err
	}()
	defer func() {
		close(held.release)
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("DeleteProject: %v", err)
			}
		case <-ctx.Done():
			t.Error("DeleteProject did not finish after releasing cache invalidation")
		}
	}()
	select {
	case <-held.entered:
	case err := <-done:
		t.Fatalf("DeleteProject returned before cache invalidation: %v", err)
	case <-ctx.Done():
		t.Fatal("cache invalidation was not reached")
	}
	eventStore := events.NewSQLiteEventStore(h.db)
	lastSeq, err := eventStore.MaxSeq(ctx)
	if err != nil {
		t.Fatalf("MaxSeq: %v", err)
	}
	durable, err := eventStore.SinceSeq(ctx, lastSeq-1, 1)
	if err != nil {
		t.Fatalf("SinceSeq: %v", err)
	}
	if len(durable) != 1 || durable[0].Kind != model.EventProjectDeleted || durable[0].EntityID != project.ID {
		t.Fatalf("last durable event=%+v, want project_deleted for %s", durable, project.ID)
	}
	rec := h.get("/v1/sync/snapshot")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var snapshot struct {
		Projects  []model.Project `json:"projects"`
		OpenTasks []struct {
			ProjectID string `json:"projectId"`
		} `json:"openTasks"`
		LastSeq int64 `json:"lastSeq"`
	}
	decodeJSONResp(t, rec.Body, &snapshot)
	if snapshot.LastSeq != lastSeq {
		t.Errorf("lastSeq=%d, want committed deletion seq %d", snapshot.LastSeq, lastSeq)
	}
	if syncProjectsContain(snapshot.Projects, project.ID) {
		t.Error("snapshot contains the deleted project")
	}
	if len(snapshot.OpenTasks) != 1 || snapshot.OpenTasks[0].ProjectID != h.rawTasks.DefaultProjectID() {
		t.Errorf("openTasks=%+v, want the moved task in Inbox", snapshot.OpenTasks)
	}
	stale, err := h.cachedTasks.ListProjects(ctx)
	if err != nil {
		t.Fatalf("cached ListProjects: %v", err)
	}
	if !syncProjectsContain(stale, project.ID) {
		t.Fatal("cache was invalidated before the snapshot assertion")
	}
}

func syncProjectsContain(projects []model.Project, id string) bool {
	for _, project := range projects {
		if project.ID == id {
			return true
		}
	}
	return false
}

func TestSyncSnapshotSQLProjectReadFailureDoesNotUseWarmCache(t *testing.T) {
	h := newSyncHarness(t)
	ctx := context.Background()
	warm, err := h.projectSvc.ListProjects(ctx)
	if err != nil || len(warm) == 0 {
		t.Fatalf("warm cache=%+v, err=%v", warm, err)
	}
	if _, err := h.db.ExecContext(ctx, "DROP TABLE projects"); err != nil {
		t.Fatalf("drop isolated projects table: %v", err)
	}
	rec := h.get("/v1/sync/snapshot")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	var response api.ErrorResponse
	decodeJSONResp(t, rec.Body, &response)
	if response.Error.Code != api.ErrorCodeInternal || response.Error.Message != "failed to retrieve projects" {
		t.Errorf("error=%+v, want project-read internal error", response.Error)
	}
	cached, err := h.projectSvc.ListProjects(ctx)
	if err != nil || len(cached) != len(warm) {
		t.Fatalf("cached projects=%+v, err=%v; want warm cache still available", cached, err)
	}
}

func TestSyncSnapshotDoesNotRequireProjectService(t *testing.T) {
	h := newSyncHarness(t)
	h.server = api.New(api.Config{
		DB:        h.db,
		Devices:   devices.NewSQLiteDeviceStore(h.db),
		Tasks:     h.taskSvc,
		TaskStore: h.rawTasks,
		Events:    events.NewSQLiteEventStore(h.db),
	})
	rec := h.get("/v1/sync/snapshot")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 without ProjectService; body=%s", rec.Code, rec.Body.String())
	}
	var snapshot struct {
		Projects []model.Project `json:"projects"`
	}
	decodeJSONResp(t, rec.Body, &snapshot)
	if !syncProjectsContain(snapshot.Projects, h.rawTasks.DefaultProjectID()) {
		t.Fatal("snapshot does not contain Inbox without ProjectService")
	}
}
