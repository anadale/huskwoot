package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/api"
)

// withDeviceID wraps a handler and injects a fake device_id into the request
// context, the same way AuthMiddleware would.
func withDeviceID(deviceID string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := api.ContextWithDeviceID(r.Context(), deviceID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func TestIdempotencyFirstRequestCallsHandler(t *testing.T) {
	calls := int32(0)
	base := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"task-1"}`))
	})
	mw := api.IdempotencyMiddleware(api.IdempotencyConfig{TTL: time.Hour})
	handler := withDeviceID("device-1", mw(base))

	req := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(`{}`))
	req.Header.Set("Idempotency-Key", "key-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
	if !strings.Contains(rec.Body.String(), `"id":"task-1"`) {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestIdempotencyRepeatReturnsCachedResponse(t *testing.T) {
	calls := int32(0)
	base := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Call-Number", fmt.Sprintf("%d", n))
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"call":%d}`, n)
	})
	mw := api.IdempotencyMiddleware(api.IdempotencyConfig{TTL: time.Hour})
	handler := withDeviceID("device-1", mw(base))

	doRequest := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(`{}`))
		req.Header.Set("Idempotency-Key", "repeat-key")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	rec1 := doRequest()
	rec2 := doRequest()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected one call to base handler, got %d", got)
	}
	if rec1.Code != rec2.Code {
		t.Fatalf("status: first=%d second=%d", rec1.Code, rec2.Code)
	}
	if rec1.Body.String() != rec2.Body.String() {
		t.Fatalf("body mismatch: first=%q second=%q", rec1.Body.String(), rec2.Body.String())
	}
	if rec1.Header().Get("X-Call-Number") != rec2.Header().Get("X-Call-Number") {
		t.Fatalf("cached headers do not match: %q vs %q",
			rec1.Header().Get("X-Call-Number"), rec2.Header().Get("X-Call-Number"))
	}
	if rec2.Header().Get("Content-Type") == "" {
		t.Fatal("Content-Type was not restored from cache")
	}
}

func TestIdempotencyWithoutHeaderBypasses(t *testing.T) {
	calls := int32(0)
	base := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	})
	mw := api.IdempotencyMiddleware(api.IdempotencyConfig{TTL: time.Hour})
	handler := withDeviceID("device-1", mw(base))

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("iteration %d: status = %d", i, rec.Code)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("without Idempotency-Key each request must call the handler, calls=%d", got)
	}
}

func TestIdempotencyKeyIsolatedByDevice(t *testing.T) {
	var counter int32
	base := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&counter, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"n":%d}`, n)
	})
	mw := api.IdempotencyMiddleware(api.IdempotencyConfig{TTL: time.Hour})

	handlerA := withDeviceID("device-A", mw(base))
	handlerB := withDeviceID("device-B", mw(base))

	doReq := func(h http.Handler) string {
		req := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(`{}`))
		req.Header.Set("Idempotency-Key", "shared-key")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Body.String()
	}

	bodyA := doReq(handlerA)
	bodyB := doReq(handlerB)
	bodyA2 := doReq(handlerA)
	bodyB2 := doReq(handlerB)

	if bodyA == bodyB {
		t.Fatalf("different devices must receive different responses: A=%q B=%q", bodyA, bodyB)
	}
	if bodyA != bodyA2 {
		t.Fatalf("retry for device-A must return cached response: %q vs %q", bodyA, bodyA2)
	}
	if bodyB != bodyB2 {
		t.Fatalf("retry for device-B must return cached response: %q vs %q", bodyB, bodyB2)
	}
	if got := atomic.LoadInt32(&counter); got != 2 {
		t.Fatalf("base handler must be called exactly twice, got %d call(s)", got)
	}
}

func TestIdempotencyExpiresAfterTTL(t *testing.T) {
	now := time.Now()
	nowFn := func() time.Time { return now }

	calls := int32(0)
	base := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"n":%d}`, n)
	})
	mw := api.IdempotencyMiddleware(api.IdempotencyConfig{TTL: time.Minute, Now: nowFn})
	handler := withDeviceID("device-exp", mw(base))

	doReq := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(`{}`))
		req.Header.Set("Idempotency-Key", "ttl-key")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	first := doReq().Body.String()
	second := doReq().Body.String()
	if first != second {
		t.Fatalf("before TTL responses must match: %q vs %q", first, second)
	}

	now = now.Add(2 * time.Minute)

	third := doReq().Body.String()
	if third == first {
		t.Fatalf("after TTL expiry expected new response, but got cached: %q", third)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected two real calls (first and after TTL), calls=%d", got)
	}
}

func TestIdempotencyLRUEvictsOldestEntries(t *testing.T) {
	var counter int32
	base := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&counter, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"n":%d}`, n)
	})
	mw := api.IdempotencyMiddleware(api.IdempotencyConfig{TTL: time.Hour, MaxEntries: 2})
	handler := withDeviceID("device-lru", mw(base))

	doReq := func(key string) string {
		req := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(`{}`))
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Body.String()
	}

	first := doReq("k1")
	_ = doReq("k2")
	_ = doReq("k3") // should evict k1
	repeatFirst := doReq("k1")

	if repeatFirst == first {
		t.Fatalf("k1 should have been evicted from LRU, but response matched: %q", repeatFirst)
	}
	if got := atomic.LoadInt32(&counter); got != 4 {
		t.Fatalf("expected 4 real calls, counter=%d", got)
	}
}

func TestIdempotencySafeMethodsBypass(t *testing.T) {
	calls := int32(0)
	base := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	})
	mw := api.IdempotencyMiddleware(api.IdempotencyConfig{TTL: time.Hour})
	handler := withDeviceID("device-safe", mw(base))

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/tasks", nil)
		req.Header.Set("Idempotency-Key", "safe-key")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("GET requests must not be cached, calls=%d", got)
	}
}

// TestIdempotencyDoesNotCacheServerErrors — when the handler returns 5xx, the
// cache is not committed and a retry executes the real request again.
func TestIdempotencyDoesNotCacheServerErrors(t *testing.T) {
	var counter int32
	base := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&counter, 1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"n":%d}`, n)
	})
	mw := api.IdempotencyMiddleware(api.IdempotencyConfig{TTL: time.Hour})
	handler := withDeviceID("device-5xx", mw(base))

	req1 := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(`{}`))
	req1.Header.Set("Idempotency-Key", "retry-after-500")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusInternalServerError {
		t.Fatalf("first response: status=%d, want 500", rec1.Code)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(`{}`))
	req2.Header.Set("Idempotency-Key", "retry-after-500")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("retry after 5xx: status=%d, want 201 (not cached)", rec2.Code)
	}
	if got := atomic.LoadInt32(&counter); got != 2 {
		t.Fatalf("expected two real calls, counter=%d", got)
	}
}

// TestIdempotencyConcurrentSameKeySerialized — two concurrent requests with the
// same Idempotency-Key: the first runs, the second waits and gets the cached
// response, and the base handler is called exactly once.
func TestIdempotencyConcurrentSameKeySerialized(t *testing.T) {
	var counter int32
	release := make(chan struct{})
	base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&counter, 1)
		// The first request waits for a signal so the second is guaranteed to
		// have entered the middleware and seen the key as already occupied.
		if n == 1 {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"n":%d}`, n)
	})
	mw := api.IdempotencyMiddleware(api.IdempotencyConfig{TTL: time.Hour})
	handler := withDeviceID("device-conc", mw(base))

	type result struct {
		code int
		body string
	}
	results := make(chan result, 2)
	runReq := func() {
		req := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(`{}`)).
			WithContext(context.Background())
		req.Header.Set("Idempotency-Key", "same-key")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		results <- result{code: rec.Code, body: rec.Body.String()}
	}

	go runReq()
	// Let the first request enter the base handler.
	time.Sleep(50 * time.Millisecond)
	go runReq()
	// Another small pause to let the second request start waiting for the cache.
	time.Sleep(50 * time.Millisecond)
	close(release)

	got := make([]result, 0, 2)
	for i := 0; i < 2; i++ {
		select {
		case r := <-results:
			got = append(got, r)
		case <-time.After(2 * time.Second):
			t.Fatal("requests did not complete in time")
		}
	}

	if got[0].body != got[1].body {
		t.Fatalf("same key must yield same response: %q vs %q", got[0].body, got[1].body)
	}
	if c := atomic.LoadInt32(&counter); c != 1 {
		t.Fatalf("base handler must be called once, counter=%d", c)
	}
}

// TestIdempotencyPanicDoesNotOrphanEntry — if the first request panics before
// sending a response, waiters on the same key must not block on entry.ready.
// Without the defer-cleanup, a retry would hang until its own context expired
// and the pending entry would stay in the LRU for the full TTL.
func TestIdempotencyPanicDoesNotOrphanEntry(t *testing.T) {
	var calls int32
	base := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			panic("имитация паники в хэндлере")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"call":%d}`, n)
	})
	mw := api.IdempotencyMiddleware(api.IdempotencyConfig{TTL: time.Hour})
	handler := withDeviceID("device-panic", mw(base))

	// First request — the test's recover catches the panic; verify that defer
	// cleaned up the pending entry.
	func() {
		defer func() { _ = recover() }()
		req := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(`{}`))
		req.Header.Set("Idempotency-Key", "panic-key")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}()

	// Second request: if the entry had been orphaned, the handler would block
	// until context expiry. Use a short context — it should still complete in time.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(`{}`)).WithContext(ctx)
	req.Header.Set("Idempotency-Key", "panic-key")
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("retry request hung — pending entry not cleaned up after panic")
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (handler must execute again)", rec.Code)
	}
	if c := atomic.LoadInt32(&calls); c != 2 {
		t.Fatalf("calls = %d, want 2 (first panic + retry request)", c)
	}
}
