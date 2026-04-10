package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// applyMiddlewareChain models the Server.routes() middleware stack for isolated
// tests: request-id → logging → recover → handler.
func applyMiddlewareChain(logger *slog.Logger, h http.Handler) http.Handler {
	return requestIDMiddleware(loggingMiddleware(logger)(recoverMiddleware(logger)(h)))
}

func TestRecoverMiddlewareConvertsPanicTo500(t *testing.T) {
	logBuf := &strings.Builder{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelError}))

	panicked := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	h := applyMiddlewareChain(logger, panicked)

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"internal"`) {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if !strings.Contains(logBuf.String(), "handler panic") {
		t.Fatalf("log does not contain panic message: %q", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "boom") {
		t.Fatalf("log does not contain panic payload: %q", logBuf.String())
	}
}

func TestRecoverMiddlewareRepanicsOnAbortHandler(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	aborter := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})
	h := recoverMiddleware(logger)(aborter)

	defer func() {
		if rv := recover(); rv == nil {
			t.Fatal("expected re-panic with http.ErrAbortHandler")
		}
	}()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
}

func TestRequestIDFromContextReturnsInjectedValue(t *testing.T) {
	var seenID string
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seenID = RequestIDFromContext(r.Context())
	})
	h := requestIDMiddleware(inner)

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	clientID := "11111111-2222-3333-4444-555555555555"
	req.Header.Set("X-Request-ID", clientID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if seenID != clientID {
		t.Fatalf("RequestIDFromContext = %q, want %q", seenID, clientID)
	}
}

func TestRequestIDFromContextEmptyOutsideMiddleware(t *testing.T) {
	if got := RequestIDFromContext(t.Context()); got != "" {
		t.Fatalf("without middleware expected empty string, got %q", got)
	}
}

func TestLoggingMiddlewareRecordsStatus(t *testing.T) {
	logBuf := &strings.Builder{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	h := loggingMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !strings.Contains(logBuf.String(), "status=418") {
		t.Fatalf("log does not contain status=418: %q", logBuf.String())
	}
}
