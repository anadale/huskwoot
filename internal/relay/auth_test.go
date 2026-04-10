package relay

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/pushproto"
)

type mockLoader struct {
	secrets map[string][]byte
}

func (m *mockLoader) Secret(id string) []byte {
	return m.secrets[id]
}

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func makeSignedRequest(t *testing.T, method, path string, body []byte, secret []byte, instanceID string, ts string) *http.Request {
	t.Helper()
	sig := pushproto.Sign(secret, method, path, ts, body)
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("X-Huskwoot-Instance", instanceID)
	req.Header.Set("X-Huskwoot-Timestamp", ts)
	req.Header.Set("X-Huskwoot-Signature", sig)
	return req
}

func decodeAuthError(t *testing.T, body []byte) authErrorResponse {
	t.Helper()
	var resp authErrorResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("failed to decode response: %v, body: %s", err, body)
	}
	return resp
}

func TestHMACMiddleware_Allows_ValidRequest(t *testing.T) {
	secret := []byte("supersecret")
	now := time.Now()
	loader := &mockLoader{secrets: map[string][]byte{"inst1": secret}}

	handlerCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		if InstanceIDFromContext(r.Context()) != "inst1" {
			t.Error("InstanceIDFromContext returned wrong value")
		}
		w.WriteHeader(http.StatusOK)
	})

	h := HMACMiddleware(loader, fixedClock(now), 5*time.Minute)(next)

	ts := strconv.FormatInt(now.Unix(), 10)
	body := []byte(`{"test":"data"}`)
	req := makeSignedRequest(t, "POST", "/v1/push", body, secret, "inst1", ts)

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rw.Code, rw.Body.String())
	}
	if !handlerCalled {
		t.Fatal("next handler was not called")
	}
}

func TestHMACMiddleware_Rejects_MissingHeaders(t *testing.T) {
	secret := []byte("supersecret")
	now := time.Now()
	loader := &mockLoader{secrets: map[string][]byte{"inst1": secret}}
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := pushproto.Sign(secret, "POST", "/v1/push", ts, nil)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not be called")
	})
	h := HMACMiddleware(loader, fixedClock(now), 5*time.Minute)(next)

	cases := []struct {
		name    string
		prepare func(r *http.Request)
	}{
		{
			"без X-Huskwoot-Instance",
			func(r *http.Request) {
				r.Header.Set("X-Huskwoot-Timestamp", ts)
				r.Header.Set("X-Huskwoot-Signature", sig)
			},
		},
		{
			"без X-Huskwoot-Timestamp",
			func(r *http.Request) {
				r.Header.Set("X-Huskwoot-Instance", "inst1")
				r.Header.Set("X-Huskwoot-Signature", sig)
			},
		},
		{
			"без X-Huskwoot-Signature",
			func(r *http.Request) {
				r.Header.Set("X-Huskwoot-Instance", "inst1")
				r.Header.Set("X-Huskwoot-Timestamp", ts)
			},
		},
		{
			"без всех заголовков",
			func(_ *http.Request) {},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/push", strings.NewReader(""))
			tc.prepare(req)

			rw := httptest.NewRecorder()
			h.ServeHTTP(rw, req)

			if rw.Code != http.StatusUnauthorized {
				t.Fatalf("want 401, got %d", rw.Code)
			}
			resp := decodeAuthError(t, rw.Body.Bytes())
			if resp.Code != "unauthorized" {
				t.Fatalf("want code=unauthorized, got %q", resp.Code)
			}
		})
	}
}

func TestHMACMiddleware_Rejects_StaleTimestamp(t *testing.T) {
	secret := []byte("supersecret")
	now := time.Unix(1700000000, 0)
	loader := &mockLoader{secrets: map[string][]byte{"inst1": secret}}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not be called")
	})
	h := HMACMiddleware(loader, fixedClock(now), 5*time.Minute)(next)

	cases := []struct {
		name string
		ts   time.Time
	}{
		{"слишком старый (6 минут назад)", now.Add(-6 * time.Minute)},
		{"из будущего (6 минут вперёд)", now.Add(6 * time.Minute)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			staleTS := strconv.FormatInt(tc.ts.Unix(), 10)
			req := makeSignedRequest(t, "POST", "/v1/push", []byte("body"), secret, "inst1", staleTS)

			rw := httptest.NewRecorder()
			h.ServeHTTP(rw, req)

			if rw.Code != http.StatusUnauthorized {
				t.Fatalf("want 401, got %d", rw.Code)
			}
			resp := decodeAuthError(t, rw.Body.Bytes())
			if resp.Code != "timestamp_skew" {
				t.Fatalf("want code=timestamp_skew, got %q", resp.Code)
			}
		})
	}
}

func TestHMACMiddleware_Rejects_UnknownInstance(t *testing.T) {
	now := time.Now()
	loader := &mockLoader{secrets: map[string][]byte{}} // empty list

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not be called")
	})
	h := HMACMiddleware(loader, fixedClock(now), 5*time.Minute)(next)

	ts := strconv.FormatInt(now.Unix(), 10)
	req := makeSignedRequest(t, "POST", "/v1/push", []byte("body"), []byte("anysecret"), "unknown", ts)

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rw.Code)
	}
	resp := decodeAuthError(t, rw.Body.Bytes())
	if resp.Code != "unknown_instance" {
		t.Fatalf("want code=unknown_instance, got %q", resp.Code)
	}
}

func TestHMACMiddleware_Rejects_TamperedBody(t *testing.T) {
	secret := []byte("supersecret")
	now := time.Now()
	loader := &mockLoader{secrets: map[string][]byte{"inst1": secret}}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not be called")
	})
	h := HMACMiddleware(loader, fixedClock(now), 5*time.Minute)(next)

	ts := strconv.FormatInt(now.Unix(), 10)
	originalBody := []byte(`{"deviceId":"abc"}`)
	tamperedBody := []byte(`{"deviceId":"HACKED"}`)

	// Sign the original body, send the tampered one.
	sig := pushproto.Sign(secret, "POST", "/v1/push", ts, originalBody)
	req := httptest.NewRequest("POST", "/v1/push", bytes.NewReader(tamperedBody))
	req.Header.Set("X-Huskwoot-Instance", "inst1")
	req.Header.Set("X-Huskwoot-Timestamp", ts)
	req.Header.Set("X-Huskwoot-Signature", sig)

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rw.Code)
	}
	resp := decodeAuthError(t, rw.Body.Bytes())
	if resp.Code != "bad_signature" {
		t.Fatalf("want code=bad_signature, got %q", resp.Code)
	}
}

func TestHMACMiddleware_RestoresBodyForHandler(t *testing.T) {
	secret := []byte("supersecret")
	now := time.Now()
	loader := &mockLoader{secrets: map[string][]byte{"inst1": secret}}
	expected := []byte(`{"hello":"world"}`)

	var readBody []byte
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		readBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading body in handler: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	})

	h := HMACMiddleware(loader, fixedClock(now), 5*time.Minute)(next)

	ts := strconv.FormatInt(now.Unix(), 10)
	req := makeSignedRequest(t, "POST", "/v1/push", expected, secret, "inst1", ts)

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rw.Code)
	}
	if !bytes.Equal(readBody, expected) {
		t.Fatalf("handler received wrong body: %q, want %q", readBody, expected)
	}
}

func TestHMACMiddleware_EnforcesMaxBody(t *testing.T) {
	secret := []byte("supersecret")
	now := time.Now()
	loader := &mockLoader{secrets: map[string][]byte{"inst1": secret}}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not be called")
	})
	h := HMACMiddleware(loader, fixedClock(now), 5*time.Minute)(next)

	// Body slightly over 1 MiB.
	bigBody := make([]byte, maxBodySize+1)
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := pushproto.Sign(secret, "POST", "/v1/push", ts, bigBody)

	req := httptest.NewRequest("POST", "/v1/push", bytes.NewReader(bigBody))
	req.Header.Set("X-Huskwoot-Instance", "inst1")
	req.Header.Set("X-Huskwoot-Timestamp", ts)
	req.Header.Set("X-Huskwoot-Signature", sig)

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)

	if rw.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("want 413, got %d", rw.Code)
	}
}
