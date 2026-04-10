package api_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/api"
	"github.com/anadale/huskwoot/internal/devices"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/google/uuid"
)

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func createTestDevice(t *testing.T, db *sql.DB, name, token string) *model.Device {
	t.Helper()
	store := devices.NewSQLiteDeviceStore(db)
	ctx := context.Background()
	d := &model.Device{
		ID:        uuid.NewString(),
		Name:      name,
		Platform:  "linux",
		TokenHash: hashToken(token),
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()
	if err := store.Create(ctx, tx, d); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return d
}

// handler that reads device_id from context — verifies that the middleware
// stored it there.
func deviceIDEchoHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := api.DeviceIDFromContext(r.Context())
		if id == "" {
			t.Error("device_id is missing from handler context")
		}
		w.Header().Set("X-Device-ID", id)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
}

func newAuthMiddleware(t *testing.T, db *sql.DB) func(http.Handler) http.Handler {
	t.Helper()
	store := devices.NewSQLiteDeviceStore(db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return api.AuthMiddleware(store, logger)
}

func TestAuthMissingTokenReturns401(t *testing.T) {
	db := openTestDB(t)
	mw := newAuthMiddleware(t, db)
	handler := mw(deviceIDEchoHandler(t))

	req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	body := readErrorBody(t, rec.Body)
	if body.Error.Code != api.ErrorCodeUnauthorized {
		t.Fatalf("code = %q, want %q", body.Error.Code, api.ErrorCodeUnauthorized)
	}
}

func TestAuthMalformedHeaderReturns401(t *testing.T) {
	db := openTestDB(t)
	mw := newAuthMiddleware(t, db)
	handler := mw(deviceIDEchoHandler(t))

	cases := []struct {
		name   string
		header string
	}{
		{"без префикса Bearer", "abcdef"},
		{"чужая схема", "Basic dXNlcjpwYXNz"},
		{"пустой токен", "Bearer "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
			req.Header.Set("Authorization", tc.header)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
		})
	}
}

func TestAuthInvalidTokenReturns401(t *testing.T) {
	db := openTestDB(t)
	mw := newAuthMiddleware(t, db)
	handler := mw(deviceIDEchoHandler(t))

	req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer totally-unknown-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	body := readErrorBody(t, rec.Body)
	if body.Error.Code != api.ErrorCodeUnauthorized {
		t.Fatalf("code = %q, want %q", body.Error.Code, api.ErrorCodeUnauthorized)
	}
}

func TestAuthRevokedTokenReturns401(t *testing.T) {
	db := openTestDB(t)
	token := "revoked-token-xyz"
	d := createTestDevice(t, db, "revoked-device", token)

	store := devices.NewSQLiteDeviceStore(db)
	if err := store.Revoke(context.Background(), d.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	mw := newAuthMiddleware(t, db)
	handler := mw(deviceIDEchoHandler(t))

	req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAuthValidTokenPutsDeviceIDInContext(t *testing.T) {
	db := openTestDB(t)
	token := "valid-token-abc"
	d := createTestDevice(t, db, "iPhone 17", token)

	mw := newAuthMiddleware(t, db)

	// The handler explicitly checks device_id from context (not via deviceIDEchoHandler,
	// to avoid calling t.Error from a background goroutine).
	var captured string
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = api.DeviceIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if captured != d.ID {
		t.Fatalf("device_id = %q, want %q", captured, d.ID)
	}
}

func TestAuthUpdatesLastSeen(t *testing.T) {
	db := openTestDB(t)
	token := "last-seen-token"
	d := createTestDevice(t, db, "last-seen-device", token)

	mw := newAuthMiddleware(t, db)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	before := time.Now().UTC().Add(-time.Second)

	req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	// Give UpdateLastSeen time to run (the middleware calls it synchronously).
	store := devices.NewSQLiteDeviceStore(db)
	devs, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var updated *model.Device
	for i := range devs {
		if devs[i].ID == d.ID {
			updated = &devs[i]
			break
		}
	}
	if updated == nil {
		t.Fatal("device not found after request")
	}
	if updated.LastSeenAt == nil {
		t.Fatal("LastSeenAt = nil after request")
	}
	if updated.LastSeenAt.Before(before) {
		t.Fatalf("LastSeenAt=%v < before=%v", updated.LastSeenAt, before)
	}
}

// TestAuthLastSeenErrorDoesNotFailRequest — an UpdateLastSeen error is only
// logged; the request still succeeds.
func TestAuthLastSeenErrorDoesNotFailRequest(t *testing.T) {
	db := openTestDB(t)
	token := "best-effort-token"
	_ = createTestDevice(t, db, "best-effort", token)

	store := devices.NewSQLiteDeviceStore(db)
	failingStore := &lastSeenFailingStore{DeviceStore: store}
	logBuf := &strings.Builder{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	mw := api.AuthMiddleware(failingStore, logger)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(logBuf.String(), "last_seen") {
		t.Fatalf("expected warning about last_seen in logs, got: %q", logBuf.String())
	}
}

// lastSeenFailingStore wraps a real DeviceStore, but UpdateLastSeen always
// returns an error — for verifying best-effort semantics.
type lastSeenFailingStore struct {
	model.DeviceStore
}

func (s *lastSeenFailingStore) UpdateLastSeen(_ context.Context, _ string, _ time.Time) error {
	return errFakeLastSeen
}

var errFakeLastSeen = errFakeLastSeenT("fake last_seen failure")

type errFakeLastSeenT string

func (e errFakeLastSeenT) Error() string { return string(e) }
