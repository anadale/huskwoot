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
		DB:      db,
		Tasks:   tasks,
		Meta:    meta,
		Events:  eventStore,
		Devices: nil,
		Queue:   pushQueue,
		Broker:  broker,
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
