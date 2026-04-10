package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/api"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/usecase"
)

// mockPairingService is a hand-written mock for model.PairingService.
type mockPairingService struct {
	requestFn func(ctx context.Context, req model.PairingRequest) (*model.PendingPairing, error)
	pollFn    func(ctx context.Context, pairID, nonce string) (*model.PairingResult, error)
	prepareFn func(ctx context.Context, pairID, csrf string) (*model.PendingPairing, error)
	confirmFn func(ctx context.Context, pairID, csrf string) (*model.Device, error)
}

func (m *mockPairingService) RequestPairing(ctx context.Context, req model.PairingRequest) (*model.PendingPairing, error) {
	if m.requestFn != nil {
		return m.requestFn(ctx, req)
	}
	return nil, nil
}

func (m *mockPairingService) PollStatus(ctx context.Context, pairID, nonce string) (*model.PairingResult, error) {
	if m.pollFn != nil {
		return m.pollFn(ctx, pairID, nonce)
	}
	return &model.PairingResult{Status: model.PairingStatusPending}, nil
}

func (m *mockPairingService) PrepareConfirm(ctx context.Context, pairID, csrf string) (*model.PendingPairing, error) {
	if m.prepareFn != nil {
		return m.prepareFn(ctx, pairID, csrf)
	}
	return nil, nil
}

func (m *mockPairingService) ConfirmWithCSRF(ctx context.Context, pairID, csrf string) (*model.Device, error) {
	if m.confirmFn != nil {
		return m.confirmFn(ctx, pairID, csrf)
	}
	return nil, nil
}

func newPairingServer(t *testing.T, svc model.PairingService, perHour int) *api.Server {
	t.Helper()
	db := openTestDB(t)
	return api.New(api.Config{
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:               db,
		PairingService:   svc,
		PairingRateLimit: perHour,
		PairingSecure:    true,
	})
}

func postPairingRequest(t *testing.T, srv *api.Server, body any, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	var buf *bytes.Buffer
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/pair/request", buf)
	req.Header.Set("Content-Type", "application/json")
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// ---- tests ----

func TestPairingHandler_RequestPairing_Success(t *testing.T) {
	expiresAt := time.Now().Add(5 * time.Minute).UTC().Truncate(time.Second)
	pairID := "test-pair-uuid"
	svc := &mockPairingService{
		requestFn: func(_ context.Context, req model.PairingRequest) (*model.PendingPairing, error) {
			return &model.PendingPairing{
				ID:        pairID,
				ExpiresAt: expiresAt,
			}, nil
		},
	}

	srv := newPairingServer(t, svc, 10)
	rec := postPairingRequest(t, srv, map[string]string{
		"deviceName":  "iPhone 17",
		"platform":    "ios",
		"clientNonce": "base64nonce",
	}, "1.2.3.4:1234")

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		PairID    string    `json:"pairId"`
		PollURL   string    `json:"pollUrl"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	decodeJSONResp(t, rec.Body, &resp)
	if resp.PairID != pairID {
		t.Fatalf("pairId=%q, want %q", resp.PairID, pairID)
	}
	if resp.PollURL != "/v1/pair/status/"+pairID {
		t.Fatalf("pollUrl=%q", resp.PollURL)
	}
	if resp.ExpiresAt.IsZero() {
		t.Fatal("expiresAt is empty")
	}
}

func TestPairingHandler_RequestPairing_InvalidBody_Returns400(t *testing.T) {
	svc := &mockPairingService{}
	srv := newPairingServer(t, svc, 10)

	cases := []struct {
		name string
		body any
	}{
		{"отсутствует clientNonce", map[string]string{"deviceName": "iPhone", "platform": "ios"}},
		{"отсутствует deviceName", map[string]string{"platform": "ios", "clientNonce": "nonce"}},
		{"отсутствует platform", map[string]string{"deviceName": "iPhone", "clientNonce": "nonce"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postPairingRequest(t, srv, tc.body, "1.2.3.4:1234")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestPairingHandler_RequestPairing_RateLimit_Returns429(t *testing.T) {
	svc := &mockPairingService{
		requestFn: func(_ context.Context, req model.PairingRequest) (*model.PendingPairing, error) {
			return &model.PendingPairing{
				ID:        "some-id",
				ExpiresAt: time.Now().Add(5 * time.Minute),
			}, nil
		},
	}
	// Limit: 5 requests per hour.
	srv := newPairingServer(t, svc, 5)

	ip := "10.0.0.1:9999"
	validBody := map[string]string{
		"deviceName":  "Device",
		"platform":    "linux",
		"clientNonce": "nonce",
	}

	// The first 5 requests must succeed.
	for i := 0; i < 5; i++ {
		rec := postPairingRequest(t, srv, validBody, ip)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("request %d: status=%d, want 202", i+1, rec.Code)
		}
	}

	// The 6th request must return 429.
	rec := postPairingRequest(t, srv, validBody, ip)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("6th request: status=%d, want 429; body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After header is missing")
	}
}

func getPairingStatus(t *testing.T, srv *api.Server, pairID, nonce string) *httptest.ResponseRecorder {
	t.Helper()
	url := "/v1/pair/status/" + pairID
	if nonce != "" {
		url += "?nonce=" + nonce
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestPairingHandler_Status_Pending_AfterTimeout(t *testing.T) {
	svc := &mockPairingService{
		pollFn: func(_ context.Context, pairID, nonce string) (*model.PairingResult, error) {
			return &model.PairingResult{PairID: pairID, Status: model.PairingStatusPending}, nil
		},
	}
	srv := newPairingServer(t, svc, 10)
	rec := getPairingStatus(t, srv, "some-pair-id", "my-nonce")

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Status string `json:"status"`
	}
	decodeJSONResp(t, rec.Body, &resp)
	if resp.Status != "pending" {
		t.Fatalf("status=%q, want pending", resp.Status)
	}
}

func TestPairingHandler_Status_Confirmed(t *testing.T) {
	svc := &mockPairingService{
		pollFn: func(_ context.Context, pairID, nonce string) (*model.PairingResult, error) {
			return &model.PairingResult{
				PairID:      pairID,
				Status:      model.PairingStatusConfirmed,
				DeviceID:    "device-uuid-123",
				BearerToken: "secret-bearer-token",
			}, nil
		},
	}
	srv := newPairingServer(t, svc, 10)
	rec := getPairingStatus(t, srv, "some-pair-id", "my-nonce")

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Status      string `json:"status"`
		DeviceID    string `json:"deviceId"`
		BearerToken string `json:"bearerToken"`
	}
	decodeJSONResp(t, rec.Body, &resp)
	if resp.Status != "confirmed" {
		t.Fatalf("status=%q, want confirmed", resp.Status)
	}
	if resp.DeviceID != "device-uuid-123" {
		t.Fatalf("deviceId=%q, want device-uuid-123", resp.DeviceID)
	}
	if resp.BearerToken != "secret-bearer-token" {
		t.Fatalf("bearerToken=%q, want secret-bearer-token", resp.BearerToken)
	}
}

func TestPairingHandler_Status_NonceMismatch_Returns403(t *testing.T) {
	svc := &mockPairingService{
		pollFn: func(_ context.Context, pairID, nonce string) (*model.PairingResult, error) {
			return nil, usecase.ErrNonceMismatch
		},
	}
	srv := newPairingServer(t, svc, 10)
	rec := getPairingStatus(t, srv, "some-pair-id", "wrong-nonce")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestPairingHandler_Status_Expired_Returns410(t *testing.T) {
	svc := &mockPairingService{
		pollFn: func(_ context.Context, pairID, nonce string) (*model.PairingResult, error) {
			return &model.PairingResult{PairID: pairID, Status: model.PairingStatusExpired}, nil
		},
	}
	srv := newPairingServer(t, svc, 10)
	rec := getPairingStatus(t, srv, "expired-pair-id", "some-nonce")

	if rec.Code != http.StatusGone {
		t.Fatalf("status=%d, want 410; body=%s", rec.Code, rec.Body.String())
	}
}

func TestPairingHandler_Status_MissingNonceQuery_Returns400(t *testing.T) {
	svc := &mockPairingService{}
	srv := newPairingServer(t, svc, 10)

	req := httptest.NewRequest(http.MethodGet, "/v1/pair/status/some-id", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func getConfirmPage(t *testing.T, srv *api.Server, pairID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/pair/confirm/"+pairID, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestPairingHandler_ConfirmPage_RendersHTML(t *testing.T) {
	createdAt := time.Date(2026, 4, 19, 10, 0, 0, 0, time.UTC)
	svc := &mockPairingService{
		prepareFn: func(_ context.Context, pairID, csrf string) (*model.PendingPairing, error) {
			return &model.PendingPairing{
				ID:         pairID,
				DeviceName: "iPhone 17",
				Platform:   "ios",
				CreatedAt:  createdAt,
				ExpiresAt:  createdAt.Add(5 * time.Minute),
			}, nil
		},
	}
	srv := newPairingServer(t, svc, 10)
	rec := getConfirmPage(t, srv, "test-pair-id")

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if ct == "" || ct[:9] != "text/html" {
		t.Fatalf("Content-Type=%q, want text/html", ct)
	}
	body := rec.Body.String()
	if !contains(body, "iPhone 17") {
		t.Fatalf("body does not contain deviceName; body=%q", body)
	}
	if !contains(body, `name="csrf"`) {
		t.Fatalf("body does not contain csrf input; body=%q", body)
	}
}

func TestPairingHandler_ConfirmPage_SetsCSRFCookie(t *testing.T) {
	pairID := "cookie-test-id"
	svc := &mockPairingService{
		prepareFn: func(_ context.Context, id, csrf string) (*model.PendingPairing, error) {
			return &model.PendingPairing{
				ID:         id,
				DeviceName: "Android",
				Platform:   "android",
				CreatedAt:  time.Now(),
				ExpiresAt:  time.Now().Add(5 * time.Minute),
			}, nil
		},
	}
	srv := newPairingServer(t, svc, 10)
	rec := getConfirmPage(t, srv, pairID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	resp := rec.Result()
	var csrfCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "__Host-csrf" {
			csrfCookie = c
			break
		}
	}
	if csrfCookie == nil {
		t.Fatal("__Host-csrf cookie is not set")
	}
	if csrfCookie.Value == "" {
		t.Fatal("__Host-csrf cookie is empty")
	}
	if !csrfCookie.Secure {
		t.Fatal("cookie is not marked Secure")
	}
	if csrfCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("SameSite=%v, want Strict", csrfCookie.SameSite)
	}
	// __Host- prefix requires Path=/ per RFC 6265bis.
	if csrfCookie.Path != "/" {
		t.Fatalf("Path=%q, want %q", csrfCookie.Path, "/")
	}
}

func TestPairingHandler_ConfirmPage_ExpiredPairing_Returns410HTML(t *testing.T) {
	svc := &mockPairingService{
		prepareFn: func(_ context.Context, pairID, csrf string) (*model.PendingPairing, error) {
			return nil, usecase.ErrPairingExpired
		},
	}
	srv := newPairingServer(t, svc, 10)
	rec := getConfirmPage(t, srv, "expired-id")

	if rec.Code != http.StatusGone {
		t.Fatalf("status=%d, want 410; body=%s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if ct == "" || ct[:9] != "text/html" {
		t.Fatalf("Content-Type=%q, want text/html", ct)
	}
}

func TestPairingHandler_ConfirmPage_StoresCSRFHash(t *testing.T) {
	var capturedCSRF string
	pairID := "hash-test-id"
	svc := &mockPairingService{
		prepareFn: func(_ context.Context, id, csrf string) (*model.PendingPairing, error) {
			capturedCSRF = csrf
			return &model.PendingPairing{
				ID:         id,
				DeviceName: "Test Device",
				Platform:   "linux",
				CreatedAt:  time.Now(),
				ExpiresAt:  time.Now().Add(5 * time.Minute),
			}, nil
		},
	}
	srv := newPairingServer(t, svc, 10)
	rec := getConfirmPage(t, srv, pairID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	if capturedCSRF == "" {
		t.Fatal("PrepareConfirm was not called")
	}

	resp := rec.Result()
	var csrfCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "__Host-csrf" {
			csrfCookie = c
			break
		}
	}
	if csrfCookie == nil {
		t.Fatal("__Host-csrf cookie is not set")
	}
	if csrfCookie.Value != capturedCSRF {
		t.Fatalf("cookie.Value=%q != capturedCSRF=%q — tokens do not match", csrfCookie.Value, capturedCSRF)
	}
}

// postConfirmSubmit sends POST /pair/confirm/{id} with form data.
func postConfirmSubmit(t *testing.T, srv *api.Server, pairID, cookieCSRF, formCSRF string) *httptest.ResponseRecorder {
	t.Helper()
	body := "csrf=" + formCSRF
	req := httptest.NewRequest(http.MethodPost, "/pair/confirm/"+pairID, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookieCSRF != "" {
		req.AddCookie(&http.Cookie{Name: "__Host-csrf", Value: cookieCSRF})
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestPairingHandler_ConfirmSubmit_Success_RendersOKHTML(t *testing.T) {
	pairID := "confirm-ok-id"
	svc := &mockPairingService{
		confirmFn: func(_ context.Context, id, csrf string) (*model.Device, error) {
			return &model.Device{ID: "device-uuid"}, nil
		},
	}
	srv := newPairingServer(t, svc, 10)

	rec := postConfirmSubmit(t, srv, pairID, "mycsrf", "mycsrf")

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if ct == "" || ct[:9] != "text/html" {
		t.Fatalf("Content-Type=%q, want text/html", ct)
	}
	body := rec.Body.String()
	if !contains(body, "connected") && !contains(body, "Connected") {
		t.Fatalf("body does not contain confirmation; body=%q", body)
	}
}

func TestPairingHandler_ConfirmSubmit_MissingCookie_Returns403(t *testing.T) {
	svc := &mockPairingService{}
	srv := newPairingServer(t, svc, 10)

	rec := postConfirmSubmit(t, srv, "some-id", "", "somecsrf")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if ct == "" || ct[:9] != "text/html" {
		t.Fatalf("Content-Type=%q, want text/html", ct)
	}
}

func TestPairingHandler_ConfirmSubmit_FormCSRFMismatchCookie_Returns403(t *testing.T) {
	svc := &mockPairingService{}
	srv := newPairingServer(t, svc, 10)

	rec := postConfirmSubmit(t, srv, "some-id", "cookietoken", "differenttoken")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestPairingHandler_ConfirmSubmit_ServiceError_RendersErrorHTML(t *testing.T) {
	svc := &mockPairingService{
		confirmFn: func(_ context.Context, id, csrf string) (*model.Device, error) {
			return nil, usecase.ErrCSRFMismatch
		},
	}
	srv := newPairingServer(t, svc, 10)

	rec := postConfirmSubmit(t, srv, "some-id", "tok", "tok")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if ct == "" || ct[:9] != "text/html" {
		t.Fatalf("Content-Type=%q, want text/html", ct)
	}
}

func TestPairingHandler_ConfirmSubmit_AlreadyConfirmed_Returns410(t *testing.T) {
	svc := &mockPairingService{
		confirmFn: func(_ context.Context, id, csrf string) (*model.Device, error) {
			return nil, usecase.ErrAlreadyConfirmed
		},
	}
	srv := newPairingServer(t, svc, 10)

	rec := postConfirmSubmit(t, srv, "some-id", "tok", "tok")

	if rec.Code != http.StatusGone {
		t.Fatalf("status=%d, want 410; body=%s", rec.Code, rec.Body.String())
	}
}

// contains reports whether sub is a substring of s.
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || findInString(s, sub))
}

func findInString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestPairingHandler_RequestPairing_DifferentIPsIndependent(t *testing.T) {
	callCount := 0
	svc := &mockPairingService{
		requestFn: func(_ context.Context, req model.PairingRequest) (*model.PendingPairing, error) {
			callCount++
			return &model.PendingPairing{
				ID:        fmt.Sprintf("id-%d", callCount),
				ExpiresAt: time.Now().Add(5 * time.Minute),
			}, nil
		},
	}
	// Limit: 1 per hour — one request per IP succeeds.
	srv := newPairingServer(t, svc, 1)

	validBody := map[string]string{
		"deviceName":  "Device",
		"platform":    "linux",
		"clientNonce": "nonce",
	}

	for i := 0; i < 3; i++ {
		ip := fmt.Sprintf("192.168.0.%d:8888", i+1)
		rec := postPairingRequest(t, srv, validBody, ip)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("IP %s: status=%d, want 202; body=%s", ip, rec.Code, rec.Body.String())
		}
	}
}
