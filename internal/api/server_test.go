package api_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/api"
	"github.com/anadale/huskwoot/internal/storage"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.OpenDB(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newTestServer(t *testing.T, db *sql.DB) *api.Server {
	t.Helper()
	return api.New(api.Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:     db,
	})
}

func readErrorBody(t *testing.T, body io.Reader) api.ErrorResponse {
	t.Helper()
	var resp api.ErrorResponse
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		t.Fatalf("JSON decoding: %v", err)
	}
	return resp
}

func TestServerHealthzReturnsOK(t *testing.T) {
	srv := newTestServer(t, openTestDB(t))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestServerReadyzChecksDB(t *testing.T) {
	db := openTestDB(t)
	srv := newTestServer(t, db)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthy DB: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Close the DB — readyz must return 503.
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	req2 := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed DB: status = %d, want 503", rec2.Code)
	}
	body := readErrorBody(t, rec2.Body)
	if body.Error.Code != api.ErrorCodeUnavailable {
		t.Fatalf("code = %q, want %q", body.Error.Code, api.ErrorCodeUnavailable)
	}
}

func TestServerReadyzWithoutDBReturns503(t *testing.T) {
	srv := api.New(api.Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestServerRequestIDEchoedInHeader(t *testing.T) {
	srv := newTestServer(t, openTestDB(t))

	t.Run("генерация нового request-id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if got := rec.Header().Get("X-Request-ID"); got == "" {
			t.Fatal("X-Request-ID is empty, expected generated UUID")
		}
	})

	t.Run("принимаемый клиентский UUID", func(t *testing.T) {
		clientID := "11111111-2222-3333-4444-555555555555"
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Header.Set("X-Request-ID", clientID)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if got := rec.Header().Get("X-Request-ID"); got != clientID {
			t.Fatalf("X-Request-ID = %q, want %q", got, clientID)
		}
	})

	t.Run("невалидный клиентский id заменяется", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Header.Set("X-Request-ID", "не-uuid")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		got := rec.Header().Get("X-Request-ID")
		if got == "" || got == "не-uuid" {
			t.Fatalf("X-Request-ID = %q: expected generated UUID", got)
		}
	})
}

func TestServerRequestIDAppearsInLogs(t *testing.T) {
	logBuf := &strings.Builder{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	srv := api.New(api.Config{Logger: logger, DB: openTestDB(t)})

	clientID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-ID", clientID)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if !strings.Contains(logBuf.String(), clientID) {
		t.Fatalf("log does not contain request-id %q: %q", clientID, logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "http request") {
		t.Fatalf("log does not contain request handling entry: %q", logBuf.String())
	}
}

func TestServerNotFoundReturnsStructuredError(t *testing.T) {
	srv := newTestServer(t, openTestDB(t))

	req := httptest.NewRequest(http.MethodGet, "/нет-такого-пути", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	body := readErrorBody(t, rec.Body)
	if body.Error.Code != api.ErrorCodeNotFound {
		t.Fatalf("code = %q, want %q", body.Error.Code, api.ErrorCodeNotFound)
	}
	if body.Error.Message == "" {
		t.Fatal("message is empty")
	}
}

func TestServerMethodNotAllowedReturnsStructuredError(t *testing.T) {
	srv := newTestServer(t, openTestDB(t))

	req := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	body := readErrorBody(t, rec.Body)
	if body.Error.Code != api.ErrorCodeMethodNotAllowed {
		t.Fatalf("code = %q, want %q", body.Error.Code, api.ErrorCodeMethodNotAllowed)
	}
}

func TestServerRunStartsAndStopsGracefully(t *testing.T) {
	addr := pickLocalAddr(t)
	srv := api.New(api.Config{
		ListenAddr: addr,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         openTestDB(t),
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run(ctx) }()

	waitForListen(t, addr, 2*time.Second)

	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		cancel()
		<-errCh
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		cancel()
		<-errCh
		t.Fatalf("status = %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not finish within 5 seconds after cancel")
	}
}

func TestServerRunRequiresListenAddr(t *testing.T) {
	srv := api.New(api.Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:     openTestDB(t),
	})
	if err := srv.Run(context.Background()); err == nil {
		t.Fatal("want error due to empty ListenAddr")
	}
}

func pickLocalAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func waitForListen(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server did not start on %s within %s", addr, timeout)
}
