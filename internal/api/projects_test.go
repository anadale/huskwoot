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
	"strings"
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

// projectsTestHarness assembles live ProjectService dependencies (SQLite + broker
// + push queue) together with an api.Server and prepares a valid device token for
// authenticated requests.
type projectsTestHarness struct {
	t          *testing.T
	db         *sql.DB
	server     *api.Server
	token      string
	device     *model.Device
	projectSvc model.ProjectService
}

func newProjectsHarness(t *testing.T) *projectsTestHarness {
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

	projectSvc := usecase.NewProjectService(usecase.ProjectServiceDeps{
		DB:        db,
		Tasks:     tasks,
		Meta:      meta,
		AutoTasks: storage.NewSQLiteAutoTaskStore(db),
		Events:    eventStore,
		Devices:   nil,
		Queue:     pushQueue,
		Broker:    broker,
	})

	token := "projects-test-token"
	device := createTestDevice(t, db, "test-device", token)

	srv := api.New(api.Config{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:       db,
		Devices:  devices.NewSQLiteDeviceStore(db),
		Projects: projectSvc,
		Owner:    api.OwnerInfo{UserName: "Oliver", TelegramUserID: 42},
	})

	return &projectsTestHarness{
		t:          t,
		db:         db,
		server:     srv,
		token:      token,
		device:     device,
		projectSvc: projectSvc,
	}
}

func (h *projectsTestHarness) do(method, target string, body any) *httptest.ResponseRecorder {
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

func decodeJSONResp(t *testing.T, body io.Reader, dst any) {
	t.Helper()
	if err := json.NewDecoder(body).Decode(dst); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
}

// ---- tests ----

func TestDeleteProjectEmptyReturnsArray(t *testing.T) {
	h := newProjectsHarness(t)
	p, err := h.projectSvc.CreateProject(context.Background(), model.CreateProjectRequest{Name: "Empty"})
	if err != nil {
		t.Fatal(err)
	}
	rec := h.do(http.MethodDelete, "/v1/projects/"+p.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response map[string]json.RawMessage
	decodeJSONResp(t, rec.Body, &response)
	if string(response["movedTasks"]) != "[]" {
		t.Fatalf("movedTasks=%s want []", response["movedTasks"])
	}
	rec = h.do(http.MethodGet, "/v1/projects/"+p.ID, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET deleted project status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeleteProjectInboxAndUnknown(t *testing.T) {
	h := newTasksHarness(t)
	for _, tc := range []struct {
		name   string
		id     string
		status int
		code   string
	}{
		{name: "Inbox", id: h.inboxID, status: http.StatusForbidden, code: api.ErrorCodeInboxProtected},
		{name: "unknown", id: "00000000-0000-4000-8000-000000000099", status: http.StatusNotFound, code: api.ErrorCodeNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := events.NewSQLiteEventStore(h.db).MaxSeq(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			rec := h.do(http.MethodDelete, "/v1/projects/"+tc.id, nil)
			if rec.Code != tc.status {
				t.Fatalf("DELETE status=%d want=%d body=%s", rec.Code, tc.status, rec.Body.String())
			}
			if body := readErrorBody(t, rec.Body); body.Error.Code != tc.code {
				t.Fatalf("DELETE code=%q want=%q", body.Error.Code, tc.code)
			}
			after, err := events.NewSQLiteEventStore(h.db).MaxSeq(context.Background())
			if err != nil || after != before {
				t.Fatalf("rejected deletion emitted events: before=%d after=%d err=%v", before, after, err)
			}
		})
	}
	rec := h.do(http.MethodGet, "/v1/projects/"+h.inboxID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET Inbox status=%d body=%s", rec.Code, rec.Body.String())
	}
}
func TestDeleteProjectMovesAllStatusesInOldNumberOrder(t *testing.T) {
	h := newTasksHarness(t)
	ctx := context.Background()
	p, err := h.projectSvc.CreateProject(ctx, model.CreateProjectRequest{Name: "Delete via HTTP"})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{Summary: "Existing Inbox task"}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Date(2026, time.November, 1, 10, 0, 0, 0, time.UTC)
	statuses := []string{"open", "done", "cancelled"}
	ids := make([]string, 0, len(statuses))
	for _, status := range statuses {
		task, err := h.taskSvc.CreateTask(ctx, model.CreateTaskRequest{
			ProjectID: p.ID, Summary: "Task " + status, Details: "Preserved details",
			Topic: "Release", Priority: 2, Deadline: &deadline,
			Source:        model.Source{Kind: "client", ID: h.device.ID},
			SourceDetails: &model.TaskSourceDetails{Text: "Synthetic source", Via: "api"},
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.ID)
		switch status {
		case "done":
			if _, err := h.taskSvc.CompleteTask(ctx, task.ID); err != nil {
				t.Fatal(err)
			}
		case "cancelled":
			if _, err := h.taskSvc.UpdateTask(ctx, task.ID, model.TaskUpdate{Status: &status}); err != nil {
				t.Fatal(err)
			}
		}
	}
	rec := h.do(http.MethodDelete, "/v1/projects/"+p.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		MovedTasks []json.RawMessage `json:"movedTasks"`
	}
	decodeJSONResp(t, rec.Body, &response)
	if len(response.MovedTasks) != len(ids) {
		t.Fatalf("movedTasks count=%d want=%d", len(response.MovedTasks), len(ids))
	}
	for i, raw := range response.MovedTasks {
		var task taskResp
		if err := json.Unmarshal(raw, &task); err != nil {
			t.Fatal(err)
		}
		if task.ID != ids[i] || task.Number != i+3 || task.ProjectID != h.inboxID ||
			task.ProjectSlug != "inbox" || task.Status != statuses[i] {
			t.Fatalf("moved task[%d]=%+v", i, task)
		}
		if task.Ref == "" || task.Summary != "Task "+statuses[i] || task.Details != "Preserved details" ||
			task.Topic != "Release" || task.Priority != 2 || task.Deadline == nil ||
			!task.Deadline.Equal(deadline) || task.Source == nil || task.Source.Text != "Synthetic source" ||
			task.CreatedAt.IsZero() || task.UpdatedAt.IsZero() || (task.ClosedAt != nil) != (statuses[i] != "open") {
			t.Fatalf("incomplete moved Task DTO[%d]=%+v", i, task)
		}
		got := h.do(http.MethodGet, "/v1/tasks/"+task.ID, nil)
		if got.Code != http.StatusOK || !bytes.Equal(raw, bytes.TrimSpace(got.Body.Bytes())) {
			t.Fatalf("moved Task DTO differs from GET: moved=%s GET status=%d body=%s", raw, got.Code, got.Body.String())
		}
	}
	rec = h.do(http.MethodGet, "/v1/projects/"+p.ID, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET deleted project status=%d body=%s", rec.Code, rec.Body.String())
	}
	if body := readErrorBody(t, rec.Body); body.Error.Code != api.ErrorCodeNotFound {
		t.Fatalf("GET deleted project error=%q", body.Error.Code)
	}
}

func TestProjectColorCreateAndReads(t *testing.T) {
	for _, tc := range []struct {
		name  string
		color *string
		want  string
	}{
		{name: "absent defaults to Auto"},
		{name: "explicit Auto", color: new("")},
		{name: "blue", color: new("blue"), want: "blue"},
		{name: "purple", color: new("purple"), want: "purple"},
		{name: "teal", color: new("teal"), want: "teal"},
		{name: "orange", color: new("orange"), want: "orange"},
		{name: "pink", color: new("pink"), want: "pink"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newProjectsHarness(t)
			body := map[string]string{"name": "Palette"}
			if tc.color != nil {
				body["color"] = *tc.color
			}
			rec := h.do(http.MethodPost, "/v1/projects", body)
			if rec.Code != http.StatusCreated {
				t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
			}
			var created map[string]any
			decodeJSONResp(t, rec.Body, &created)
			if value, ok := created["color"]; !ok || value != tc.want {
				t.Fatalf("created color=%v present=%v want=%q", value, ok, tc.want)
			}
			id, ok := created["id"].(string)
			if !ok || id == "" {
				t.Fatal("missing project ID")
			}
			rec = h.do(http.MethodGet, "/v1/projects/"+id, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("get status=%d body=%s", rec.Code, rec.Body.String())
			}
			var got map[string]any
			decodeJSONResp(t, rec.Body, &got)
			if value, ok := got["color"]; !ok || value != tc.want {
				t.Fatalf("get color=%v present=%v want=%q", value, ok, tc.want)
			}
			rec = h.do(http.MethodGet, "/v1/projects", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
			}
			var listed struct {
				Projects []map[string]any `json:"projects"`
			}
			decodeJSONResp(t, rec.Body, &listed)
			found := false
			for _, p := range listed.Projects {
				value, ok := p["color"]
				if !ok {
					t.Fatal("list must emit color, including Auto")
				}
				if p["id"] == id {
					found = true
					if value != tc.want {
						t.Fatalf("list color=%v want=%q", value, tc.want)
					}
				}
			}
			if !found {
				t.Fatal("created project missing from list")
			}
		})
	}
}

func TestProjectColorPatchOnlyPreserveAndReset(t *testing.T) {
	h := newProjectsHarness(t)
	p, err := h.projectSvc.CreateProject(context.Background(), model.CreateProjectRequest{Name: "Palette", Color: "purple"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		body map[string]string
		want string
	}{
		{"absent preserves", map[string]string{"description": "Updated"}, "purple"},
		{"color only", map[string]string{"color": "blue"}, "blue"},
		{"reset Auto", map[string]string{"color": ""}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(http.MethodPatch, "/v1/projects/"+p.ID, tc.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("PATCH status=%d body=%s", rec.Code, rec.Body.String())
			}
			var got map[string]any
			decodeJSONResp(t, rec.Body, &got)
			if value, ok := got["color"]; !ok || value != tc.want {
				t.Fatalf("PATCH color=%v present=%v want=%q", value, ok, tc.want)
			}
			persisted, err := h.projectSvc.FindProjectByName(context.Background(), "Palette")
			if err != nil || persisted == nil || persisted.Color != tc.want {
				t.Fatalf("persisted project=%+v err=%v want color=%q", persisted, err, tc.want)
			}
		})
	}
}

func TestProjectColorInvalidReturns422WithoutMutation(t *testing.T) {
	for _, color := range []string{"Blue", " blue", "blue ", "#007AFF", "green", "auto"} {
		for _, method := range []string{http.MethodPost, http.MethodPatch} {
			t.Run(method+"_"+color, func(t *testing.T) {
				h := newProjectsHarness(t)
				p, err := h.projectSvc.CreateProject(context.Background(), model.CreateProjectRequest{Name: "Palette", Color: "pink"})
				if err != nil {
					t.Fatal(err)
				}
				before, err := events.NewSQLiteEventStore(h.db).MaxSeq(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				target := "/v1/projects"
				if method == http.MethodPatch {
					target += "/" + p.ID
				}
				rec := h.do(method, target, map[string]string{"name": "Not persisted", "color": color})
				if rec.Code != http.StatusUnprocessableEntity {
					t.Fatalf("status=%d want 422 body=%s", rec.Code, rec.Body.String())
				}
				body := readErrorBody(t, rec.Body)
				if body.Error.Code != api.ErrorCodeColorInvalid {
					t.Fatalf("error code=%q want color_invalid", body.Error.Code)
				}
				projects, err := h.projectSvc.ListProjects(context.Background())
				if err != nil || len(projects) != 2 {
					t.Fatalf("projects=%+v err=%v", projects, err)
				}
				original, err := h.projectSvc.FindProjectByName(context.Background(), "Palette")
				if err != nil || original == nil || original.Color != "pink" {
					t.Fatalf("original mutated: project=%+v err=%v", original, err)
				}
				after, err := events.NewSQLiteEventStore(h.db).MaxSeq(context.Background())
				if err != nil || after != before {
					t.Fatalf("invalid request recorded event: before=%d after=%d err=%v", before, after, err)
				}
			})
		}
	}
}

func TestGetMeReturnsOwnerAndVersion(t *testing.T) {
	h := newProjectsHarness(t)

	rec := h.do(http.MethodGet, "/v1/me", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		DeviceID       string `json:"deviceId"`
		OwnerName      string `json:"ownerName"`
		TelegramUserID int64  `json:"telegramUserId"`
		Version        string `json:"version"`
	}
	decodeJSONResp(t, rec.Body, &resp)
	if resp.DeviceID != h.device.ID {
		t.Fatalf("device_id=%q, want %q", resp.DeviceID, h.device.ID)
	}
	if resp.OwnerName != "Oliver" {
		t.Fatalf("owner_name=%q, want Oliver", resp.OwnerName)
	}
	if resp.TelegramUserID != 42 {
		t.Fatalf("telegram_user_id=%d, want 42", resp.TelegramUserID)
	}
	if resp.Version == "" {
		t.Fatal("version is empty")
	}
}

func TestGetMeUnauthenticatedReturns401(t *testing.T) {
	h := newProjectsHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	rec := httptest.NewRecorder()
	h.server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", rec.Code)
	}
}

func TestListProjectsReturnsAll(t *testing.T) {
	h := newProjectsHarness(t)

	// Create 2 projects via the service — Inbox is also created automatically.
	_, err := h.projectSvc.CreateProject(context.Background(), model.CreateProjectRequest{Name: "Работа"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	_, err = h.projectSvc.CreateProject(context.Background(), model.CreateProjectRequest{Name: "Дом"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	rec := h.do(http.MethodGet, "/v1/projects", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Projects []struct {
			ID          string    `json:"id"`
			Slug        string    `json:"slug"`
			Name        string    `json:"name"`
			Description string    `json:"description"`
			TaskCounter int       `json:"taskCounter"`
			CreatedAt   time.Time `json:"createdAt"`
		} `json:"projects"`
	}
	decodeJSONResp(t, rec.Body, &resp)
	if len(resp.Projects) != 3 {
		t.Fatalf("expected exactly 3 projects (Inbox+2), got %d", len(resp.Projects))
	}
	names := map[string]bool{}
	for _, p := range resp.Projects {
		names[p.Name] = true
	}
	if !names["Работа"] || !names["Дом"] {
		t.Fatalf("expected projects 'Работа' and 'Дом', got %+v", names)
	}
}

func TestGetProjectByID(t *testing.T) {
	h := newProjectsHarness(t)
	created, err := h.projectSvc.CreateProject(context.Background(), model.CreateProjectRequest{Name: "Хобби"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	rec := h.do(http.MethodGet, "/v1/projects/"+created.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	decodeJSONResp(t, rec.Body, &resp)
	if resp.ID != created.ID {
		t.Fatalf("id=%q, want %q", resp.ID, created.ID)
	}
	if resp.Name != "Хобби" {
		t.Fatalf("name=%q, want 'Хобби'", resp.Name)
	}
}

func TestGetProjectNotFoundReturns404(t *testing.T) {
	h := newProjectsHarness(t)

	rec := h.do(http.MethodGet, "/v1/projects/несуществующий-id", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	body := readErrorBody(t, rec.Body)
	if body.Error.Code != api.ErrorCodeNotFound {
		t.Fatalf("code=%q, want %q", body.Error.Code, api.ErrorCodeNotFound)
	}
}

func TestCreateProjectAutoGeneratesSlug(t *testing.T) {
	h := newProjectsHarness(t)

	rec := h.do(http.MethodPost, "/v1/projects", map[string]string{
		"name":        "Новый Проект",
		"description": "ага",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ID          string `json:"id"`
		Slug        string `json:"slug"`
		Name        string `json:"name"`
		Description string `json:"description"`
		TaskCounter int    `json:"task_counter"`
	}
	decodeJSONResp(t, rec.Body, &resp)
	if resp.Slug != "novyy-proekt" {
		t.Fatalf("slug=%q, want novyy-proekt", resp.Slug)
	}
	if resp.Name != "Новый Проект" {
		t.Fatalf("name=%q", resp.Name)
	}
	if resp.Description != "ага" {
		t.Fatalf("description=%q", resp.Description)
	}
	if resp.ID == "" {
		t.Fatal("id is empty")
	}
}

func TestCreateProjectUsesExplicitSlug(t *testing.T) {
	h := newProjectsHarness(t)

	rec := h.do(http.MethodPost, "/v1/projects", map[string]string{
		"name": "Работа",
		"slug": "custom-work",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d, want 201", rec.Code)
	}
	var resp struct {
		Slug string `json:"slug"`
	}
	decodeJSONResp(t, rec.Body, &resp)
	if resp.Slug != "custom-work" {
		t.Fatalf("slug=%q", resp.Slug)
	}
}

func TestCreateProjectConflictOnDuplicateName(t *testing.T) {
	h := newProjectsHarness(t)
	if _, err := h.projectSvc.CreateProject(context.Background(), model.CreateProjectRequest{Name: "Один"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	rec := h.do(http.MethodPost, "/v1/projects", map[string]string{"name": "Один"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	body := readErrorBody(t, rec.Body)
	if body.Error.Code != api.ErrorCodeConflict {
		t.Fatalf("code=%q, want %q", body.Error.Code, api.ErrorCodeConflict)
	}
}

func TestCreateProjectValidation(t *testing.T) {
	h := newProjectsHarness(t)

	cases := []struct {
		name string
		body any
		want int
	}{
		{"пустой name", map[string]string{"name": ""}, http.StatusUnprocessableEntity},
		{"пробелы", map[string]string{"name": "   "}, http.StatusUnprocessableEntity},
		{"лишние поля", map[string]any{"name": "X", "garbage": true}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(http.MethodPost, "/v1/projects", tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status=%d, want %d; body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestCreateProjectEmptyBody(t *testing.T) {
	h := newProjectsHarness(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/projects", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+h.token)
	rec := httptest.NewRecorder()
	h.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rec.Code)
	}
}

func TestUpdateProjectChangesName(t *testing.T) {
	h := newProjectsHarness(t)
	created, err := h.projectSvc.CreateProject(context.Background(), model.CreateProjectRequest{Name: "Старое имя"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	rec := h.do(http.MethodPatch, "/v1/projects/"+created.ID, map[string]string{"name": "Новое имя"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Name string `json:"name"`
		ID   string `json:"id"`
	}
	decodeJSONResp(t, rec.Body, &resp)
	if resp.Name != "Новое имя" {
		t.Fatalf("name=%q", resp.Name)
	}
	if resp.ID != created.ID {
		t.Fatalf("id changed")
	}
}

func TestUpdateProjectNotFoundReturns404(t *testing.T) {
	h := newProjectsHarness(t)

	rec := h.do(http.MethodPatch, "/v1/projects/unknown-uuid", map[string]string{"name": "X"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestUpdateProjectEmptyPayloadReturns422(t *testing.T) {
	h := newProjectsHarness(t)
	created, err := h.projectSvc.CreateProject(context.Background(), model.CreateProjectRequest{Name: "Пусто"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	rec := h.do(http.MethodPatch, "/v1/projects/"+created.ID, map[string]string{})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d, want 422; body=%s", rec.Code, rec.Body.String())
	}
}

func TestProjectsUnauthenticatedReturns401(t *testing.T) {
	h := newProjectsHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/projects", nil)
	rec := httptest.NewRecorder()
	h.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", rec.Code)
	}
}

func TestGetProjectIncludesAliases(t *testing.T) {
	h := newProjectsHarness(t)
	created, err := h.projectSvc.CreateProject(context.Background(), model.CreateProjectRequest{
		Name:    "Книги",
		Aliases: []string{"книги", "books"},
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	rec := h.do(http.MethodGet, "/v1/projects/"+created.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ID      string   `json:"id"`
		Aliases []string `json:"aliases"`
	}
	decodeJSONResp(t, rec.Body, &resp)
	if resp.ID != created.ID {
		t.Fatalf("id=%q, want %q", resp.ID, created.ID)
	}
	if len(resp.Aliases) != 2 {
		t.Fatalf("aliases=%v, want [books книги]", resp.Aliases)
	}

	// Project without aliases returns an empty array, not null.
	noAlias, err := h.projectSvc.CreateProject(context.Background(), model.CreateProjectRequest{Name: "Без алиасов"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	rec2 := h.do(http.MethodGet, "/v1/projects/"+noAlias.ID, nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec2.Code)
	}
	var resp2 struct {
		Aliases []string `json:"aliases"`
	}
	decodeJSONResp(t, rec2.Body, &resp2)
	if resp2.Aliases == nil {
		t.Fatal("aliases must be an empty array, not null")
	}
	if len(resp2.Aliases) != 0 {
		t.Fatalf("aliases=%v, want []", resp2.Aliases)
	}
}

func TestPostProjectWithAliases(t *testing.T) {
	h := newProjectsHarness(t)

	rec := h.do(http.MethodPost, "/v1/projects", map[string]any{
		"name":    "Сад",
		"aliases": []string{"сад", "garden"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ID      string   `json:"id"`
		Aliases []string `json:"aliases"`
	}
	decodeJSONResp(t, rec.Body, &resp)
	if len(resp.Aliases) != 2 {
		t.Fatalf("aliases=%v, want 2 items", resp.Aliases)
	}
	found := map[string]bool{}
	for _, a := range resp.Aliases {
		found[a] = true
	}
	if !found["сад"] || !found["garden"] {
		t.Fatalf("aliases=%v, want [сад garden]", resp.Aliases)
	}
}

func TestPatchProjectAddsAlias(t *testing.T) {
	h := newProjectsHarness(t)
	created, err := h.projectSvc.CreateProject(context.Background(), model.CreateProjectRequest{Name: "Музыка"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	rec := h.do(http.MethodPatch, "/v1/projects/"+created.ID, map[string]any{
		"aliases": []string{"музыка"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ID      string   `json:"id"`
		Aliases []string `json:"aliases"`
	}
	decodeJSONResp(t, rec.Body, &resp)
	if len(resp.Aliases) != 1 || resp.Aliases[0] != "музыка" {
		t.Fatalf("aliases=%v, want [музыка]", resp.Aliases)
	}
}

func TestPatchProjectRemovesAlias(t *testing.T) {
	h := newProjectsHarness(t)
	created, err := h.projectSvc.CreateProject(context.Background(), model.CreateProjectRequest{
		Name:    "Спорт",
		Aliases: []string{"спорт"},
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Replace with empty set — removes all aliases.
	rec := h.do(http.MethodPatch, "/v1/projects/"+created.ID, map[string]any{
		"aliases": []string{},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Aliases []string `json:"aliases"`
	}
	decodeJSONResp(t, rec.Body, &resp)
	if len(resp.Aliases) != 0 {
		t.Fatalf("aliases=%v, want []", resp.Aliases)
	}
}

func TestPatchProjectReplaceSet(t *testing.T) {
	h := newProjectsHarness(t)
	created, err := h.projectSvc.CreateProject(context.Background(), model.CreateProjectRequest{
		Name:    "Кино",
		Aliases: []string{"кино"},
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	rec := h.do(http.MethodPatch, "/v1/projects/"+created.ID, map[string]any{
		"aliases": []string{"cinema", "films"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Aliases []string `json:"aliases"`
	}
	decodeJSONResp(t, rec.Body, &resp)
	found := map[string]bool{}
	for _, a := range resp.Aliases {
		found[a] = true
	}
	if found["кино"] {
		t.Fatal("old alias 'кино' should have been removed")
	}
	if !found["cinema"] || !found["films"] {
		t.Fatalf("aliases=%v, want [cinema films]", resp.Aliases)
	}
}

func TestPatchProjectAliasErrors(t *testing.T) {
	h := newProjectsHarness(t)

	// Create a project to act as the alias owner for ErrAliasTaken.
	owner, err := h.projectSvc.CreateProject(context.Background(), model.CreateProjectRequest{
		Name:    "Владелец",
		Aliases: []string{"taken-alias"},
	})
	if err != nil {
		t.Fatalf("CreateProject owner: %v", err)
	}
	_ = owner

	target, err := h.projectSvc.CreateProject(context.Background(), model.CreateProjectRequest{Name: "Цель"})
	if err != nil {
		t.Fatalf("CreateProject target: %v", err)
	}

	// Find the Inbox project ID.
	projects, err := h.projectSvc.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	var inboxID string
	for _, p := range projects {
		if p.Slug == "inbox" {
			inboxID = p.ID
			break
		}
	}
	if inboxID == "" {
		t.Fatal("inbox project not found")
	}

	cases := []struct {
		name       string
		projectID  string
		aliases    []string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "invalid alias format",
			projectID:  target.ID,
			aliases:    []string{"_bad_alias"},
			wantStatus: http.StatusBadRequest,
			wantCode:   api.ErrorCodeAliasInvalid,
		},
		{
			name:       "alias already taken",
			projectID:  target.ID,
			aliases:    []string{"taken-alias"},
			wantStatus: http.StatusConflict,
			wantCode:   api.ErrorCodeAliasTaken,
		},
		{
			name:       "alias limit reached",
			projectID:  target.ID,
			aliases:    []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9", "a10", "a11"},
			wantStatus: http.StatusConflict,
			wantCode:   api.ErrorCodeAliasLimitReached,
		},
		{
			name:       "forbidden for inbox",
			projectID:  inboxID,
			aliases:    []string{"inbox-alias"},
			wantStatus: http.StatusForbidden,
			wantCode:   api.ErrorCodeAliasForbiddenForInbox,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(http.MethodPatch, "/v1/projects/"+tc.projectID, map[string]any{
				"aliases": tc.aliases,
			})
			if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			body := readErrorBody(t, rec.Body)
			if body.Error.Code != tc.wantCode {
				t.Fatalf("code=%q, want %q", body.Error.Code, tc.wantCode)
			}
		})
	}
}

func TestPatchProjectAliasConflictsWithName(t *testing.T) {
	h := newProjectsHarness(t)

	// Create a project whose lowercased name will be used as an alias on another project.
	_, err := h.projectSvc.CreateProject(context.Background(), model.CreateProjectRequest{Name: "conflicting"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	target, err := h.projectSvc.CreateProject(context.Background(), model.CreateProjectRequest{Name: "Другой"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// "conflicting" is both a project name and an attempted alias — should conflict.
	rec := h.do(http.MethodPatch, "/v1/projects/"+target.ID, map[string]any{
		"aliases": []string{"conflicting"},
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	body := readErrorBody(t, rec.Body)
	if body.Error.Code != api.ErrorCodeAliasConflictsWithName {
		t.Fatalf("code=%q, want %q", body.Error.Code, api.ErrorCodeAliasConflictsWithName)
	}
}

func TestProjectInboxProtectionAndEditableMetadata(t *testing.T) {
	h := newProjectsHarness(t)
	store, err := storage.NewSQLiteTaskStore(h.db)
	if err != nil {
		t.Fatal(err)
	}
	id := store.DefaultProjectID()
	for _, body := range []map[string]string{
		{"name": "Inbox"}, {"name": ""}, {"slug": "inbox"}, {"slug": ""},
		{"name": "Other", "color": "teal", "description": "blocked"},
	} {
		rec := h.do(http.MethodPatch, "/v1/projects/"+id, body)
		var response struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		decodeJSONResp(t, rec.Body, &response)
		if rec.Code != http.StatusForbidden || response.Error.Code != "inbox_protected" {
			t.Fatalf("body=%v status=%d error=%s", body, rec.Code, response.Error.Code)
		}
	}
	rec := h.do(http.MethodPatch, "/v1/projects/"+id, map[string]string{"color": "teal", "description": "Editable"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var p model.Project
	decodeJSONResp(t, rec.Body, &p)
	if p.Name != "Inbox" || p.Slug != "inbox" || p.Color != "teal" || p.Description != "Editable" {
		t.Fatalf("updated Inbox=%+v", p)
	}
}

func TestProjectGeneratedSlugsAndExplicitConflict(t *testing.T) {
	h := newProjectsHarness(t)
	create := func(name, slug string) model.Project {
		t.Helper()
		rec := h.do(http.MethodPost, "/v1/projects", map[string]string{"name": name, "slug": slug})
		if rec.Code != http.StatusCreated {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var p model.Project
		decodeJSONResp(t, rec.Body, &p)
		return p
	}
	first := create("Работа", "")
	rec := h.do(http.MethodPatch, "/v1/projects/"+first.ID, map[string]string{"name": "Дом"})
	var renamed model.Project
	decodeJSONResp(t, rec.Body, &renamed)
	if rec.Code != http.StatusOK || renamed.Slug != "rabota" {
		t.Fatalf("rename status=%d project=%+v", rec.Code, renamed)
	}
	if got := create("Работа", "").Slug; got != "rabota-2" {
		t.Fatalf("slug=%q", got)
	}
	for i, name := range []string{"X", "X!", "X?"} {
		want := []string{"x", "x-2", "x-3"}[i]
		if got := create(name, "").Slug; got != want {
			t.Fatalf("slug=%q want=%q", got, want)
		}
	}
	rec = h.do(http.MethodPost, "/v1/projects", map[string]string{"name": "Explicit", "slug": "x"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
