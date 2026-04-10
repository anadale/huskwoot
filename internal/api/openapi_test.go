package api_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/anadale/huskwoot/internal/api"
	"github.com/anadale/huskwoot/internal/devices"
	"github.com/anadale/huskwoot/internal/events"
	"github.com/anadale/huskwoot/internal/push"
	"github.com/anadale/huskwoot/internal/storage"
	"github.com/anadale/huskwoot/internal/usecase"
)

// TestOpenAPIYAMLIsValid verifies that the embedded YAML is parseable and
// contains the required OpenAPI 3.1 fields: openapi, info.title and paths.
func TestOpenAPIYAMLIsValid(t *testing.T) {
	raw := api.OpenAPISpec()
	if len(raw) == 0 {
		t.Fatal("OpenAPISpec() returned empty content")
	}

	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}

	openapi, _ := doc["openapi"].(string)
	if !strings.HasPrefix(openapi, "3.1") {
		t.Fatalf("openapi = %q, expected prefix 3.1", openapi)
	}

	info, _ := doc["info"].(map[string]any)
	if info == nil {
		t.Fatalf("info is missing from OpenAPI document")
	}
	if title, _ := info["title"].(string); strings.TrimSpace(title) == "" {
		t.Fatalf("info.title is empty")
	}
	if version, _ := info["version"].(string); strings.TrimSpace(version) == "" {
		t.Fatalf("info.version is empty")
	}

	paths, _ := doc["paths"].(map[string]any)
	if len(paths) == 0 {
		t.Fatalf("paths are empty")
	}
}

// TestGetOpenAPIServesEmbeddedYAML verifies that GET /v1/openapi.yaml returns
// the embedded YAML with the correct Content-Type.
func TestGetOpenAPIServesEmbeddedYAML(t *testing.T) {
	srv := api.New(api.Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:     openTestDB(t),
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/openapi.yaml", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/yaml") {
		t.Fatalf("Content-Type = %q, want application/yaml", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "openapi:") {
		t.Fatalf("body does not contain 'openapi:'; first 200 chars: %q", truncate(body, 200))
	}
	if !strings.Contains(body, "3.1") {
		t.Fatalf("body does not contain version 3.1; first 200 chars: %q", truncate(body, 200))
	}
}

// TestOpenAPIAccessibleWithoutAuth verifies that the schema is accessible without
// a bearer token: SDK generators and documentation need open access to the spec.
func TestOpenAPIAccessibleWithoutAuth(t *testing.T) {
	db := openTestDB(t)
	srv := api.New(api.Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:      db,
		Devices: devices.NewSQLiteDeviceStore(db),
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/openapi.yaml", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("without auth status = %d, want 200", rec.Code)
	}
}

// TestOpenAPICoverageMatchesRoutes verifies that every registered route (except
// excluded ones) has a corresponding paths entry in the yaml.
func TestOpenAPICoverageMatchesRoutes(t *testing.T) {
	srv := newFullServer(t)

	raw := api.OpenAPISpec()
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	paths, _ := doc["paths"].(map[string]any)
	if paths == nil {
		t.Fatalf("paths is missing")
	}

	excluded := map[string]bool{
		"/healthz":         true,
		"/readyz":          true,
		"/v1/openapi.yaml": true,
	}

	root, ok := srv.Handler().(chi.Router)
	if !ok {
		t.Fatalf("srv.Handler() не chi.Router: %T", srv.Handler())
	}

	var missing []string
	err := chi.Walk(root, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if excluded[route] {
			return nil
		}
		entry, ok := paths[route].(map[string]any)
		if !ok {
			missing = append(missing, method+" "+route+" (no path in OpenAPI)")
			return nil
		}
		methodKey := strings.ToLower(method)
		if _, ok := entry[methodKey]; !ok {
			missing = append(missing, method+" "+route+" (no method in OpenAPI)")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
	if len(missing) > 0 {
		t.Fatalf("not covered in OpenAPI:\n  %s", strings.Join(missing, "\n  "))
	}
}

// newFullServer starts an api.Server with a full set of dependencies so that
// chi.Walk sees all registered /v1/* routes.
func newFullServer(t *testing.T) *api.Server {
	t.Helper()
	db := openTestDB(t)

	sqliteTasks, err := storage.NewSQLiteTaskStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteTaskStore: %v", err)
	}
	taskStore := storage.NewCachedTaskStore(sqliteTasks)
	meta := storage.NewSQLiteMetaStore(db)
	eventStore := events.NewSQLiteEventStore(db)
	pushQueue := push.NewSQLitePushQueue(db)
	broker := events.NewBroker(events.BrokerConfig{})
	deviceStore := devices.NewSQLiteDeviceStore(db)

	projectSvc := usecase.NewProjectService(usecase.ProjectServiceDeps{
		DB: db, Tasks: taskStore, Meta: meta, Events: eventStore,
		Devices: deviceStore, Queue: pushQueue, Broker: broker,
	})
	taskSvc := usecase.NewTaskService(usecase.TaskServiceDeps{
		DB: db, Tasks: taskStore, Events: eventStore,
		Devices: deviceStore, Queue: pushQueue, Broker: broker,
	})
	chatSvc := &fakeChatService{}

	return api.New(api.Config{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:             db,
		Devices:        deviceStore,
		Projects:       projectSvc,
		Tasks:          taskSvc,
		Chat:           chatSvc,
		Events:         eventStore,
		Broker:         broker,
		PairingService: &mockPairingService{},
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
