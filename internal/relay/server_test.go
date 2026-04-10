package relay

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/anadale/huskwoot/internal/pushproto"
	"github.com/anadale/huskwoot/internal/relay/migrations"
)

func openServerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening DB: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if err := migrations.Up(db); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func buildTestServer(t *testing.T) (*http.Server, *Store, []byte, string) {
	t.Helper()
	secret := []byte("test-secret-server")
	instanceID := "test-instance"

	db := openServerTestDB(t)
	store := NewStore(db)

	ctx := context.Background()
	if err := store.SyncInstances(ctx, []InstanceSpec{{
		ID:           instanceID,
		OwnerContact: "@test",
		Secret:       string(secret),
	}}); err != nil {
		t.Fatalf("SyncInstances: %v", err)
	}

	loader := &mockLoader{secrets: map[string][]byte{instanceID: secret}}
	apnsSender := &mockPushSender{}
	fcmSender := &mockPushSender{}

	srv := NewServer(ServerDeps{
		Store:  store,
		Loader: loader,
		APNs:   apnsSender,
		FCM:    fcmSender,
		Logger: slog.Default(),
		Clock:  time.Now,
		Skew:   5 * time.Minute,
	})
	return srv, store, secret, instanceID
}

func signedReq(t *testing.T, method, path string, body []byte, secret []byte, instanceID string) *http.Request {
	t.Helper()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := pushproto.Sign(secret, method, path, ts, body)
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("X-Huskwoot-Instance", instanceID)
	req.Header.Set("X-Huskwoot-Timestamp", ts)
	req.Header.Set("X-Huskwoot-Signature", sig)
	return req
}

// TestRelayServer_HealthIsUnauthenticated verifies that GET /healthz does not require HMAC.
func TestRelayServer_HealthIsUnauthenticated(t *testing.T) {
	srv, _, _, _ := buildTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
}

// TestRelayServer_V1PathsRequireHMAC verifies that /v1/* paths require HMAC authentication.
func TestRelayServer_V1PathsRequireHMAC(t *testing.T) {
	srv, _, _, _ := buildTestServer(t)

	paths := []struct {
		method string
		path   string
	}{
		{http.MethodPut, "/v1/registrations/dev-1"},
		{http.MethodDelete, "/v1/registrations/dev-1"},
		{http.MethodPost, "/v1/push"},
	}

	for _, p := range paths {
		t.Run(p.method+" "+p.path, func(t *testing.T) {
			req := httptest.NewRequest(p.method, p.path, nil)
			w := httptest.NewRecorder()
			srv.Handler.ServeHTTP(w, req)

			if w.Code != http.StatusUnauthorized {
				t.Errorf("want 401, got %d", w.Code)
			}
		})
	}
}

// TestRelayServer_RoutesRegistered verifies that all routes are registered
// and reachable after HMAC authentication.
func TestRelayServer_RoutesRegistered(t *testing.T) {
	srv, store, secret, instanceID := buildTestServer(t)

	// PUT /v1/registrations/{deviceID} → 204
	t.Run("PUT registration returns 204", func(t *testing.T) {
		body := []byte(`{"platform":"ios","apnsToken":"device-token-abc"}`)
		req := signedReq(t, http.MethodPut, "/v1/registrations/dev-1", body, secret, instanceID)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.Handler.ServeHTTP(w, req)

		if w.Code != http.StatusNoContent {
			t.Errorf("want 204, got %d; body: %s", w.Code, w.Body.String())
		}
	})

	// DELETE /v1/registrations/{deviceID} → 204
	t.Run("DELETE registration returns 204", func(t *testing.T) {
		req := signedReq(t, http.MethodDelete, "/v1/registrations/dev-1", nil, secret, instanceID)
		w := httptest.NewRecorder()
		srv.Handler.ServeHTTP(w, req)

		if w.Code != http.StatusNoContent {
			t.Errorf("want 204, got %d; body: %s", w.Code, w.Body.String())
		}
	})

	// POST /v1/push with an existing registration → 200 {status:"sent"}
	t.Run("POST push returns 200 sent", func(t *testing.T) {
		// Re-create the registration (the previous DELETE removed it).
		apnsToken := "ios-token-xyz"
		if err := store.UpsertRegistration(context.Background(), instanceID, "dev-2", RegistrationFields{
			APNSToken: &apnsToken,
			Platform:  "ios",
		}); err != nil {
			t.Fatalf("UpsertRegistration: %v", err)
		}

		body := []byte(`{"deviceId":"dev-2","priority":"high","notification":{"title":"Тест","body":"Тело"}}`)
		req := signedReq(t, http.MethodPost, "/v1/push", body, secret, instanceID)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.Handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("want 200, got %d; body: %s", w.Code, w.Body.String())
		}
	})
}
