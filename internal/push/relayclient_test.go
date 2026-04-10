package push

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/pushproto"
)

var testSecret = []byte("test-secret-key")
var fixedNow = time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

func newTestClient(server *httptest.Server) RelayClient {
	return NewHTTPRelayClient(HTTPRelayClientConfig{
		BaseURL:    server.URL,
		InstanceID: "test-instance",
		Secret:     testSecret,
		Timeout:    5 * time.Second,
		Clock:      func() time.Time { return fixedNow },
	})
}

func TestRelayClient_Push_SignsRequest(t *testing.T) {
	var capturedMethod, capturedPath, capturedTS, capturedSig, capturedInstance string
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedPath = r.URL.Path
		capturedTS = r.Header.Get("X-Huskwoot-Timestamp")
		capturedSig = r.Header.Get("X-Huskwoot-Signature")
		capturedInstance = r.Header.Get("X-Huskwoot-Instance")

		var buf [4096]byte
		n, _ := r.Body.Read(buf[:])
		capturedBody = buf[:n]

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pushproto.PushResponse{Status: pushproto.StatusSent})
	}))
	defer srv.Close()

	client := newTestClient(srv)
	req := pushproto.PushRequest{
		DeviceID: "device-1",
		Priority: "high",
		Notification: pushproto.Notification{
			Title: "Тест",
			Body:  "Тестовое уведомление",
		},
	}

	resp, err := client.Push(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != pushproto.StatusSent {
		t.Errorf("status = %q, want %q", resp.Status, pushproto.StatusSent)
	}
	if capturedMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", capturedMethod)
	}
	if capturedPath != "/v1/push" {
		t.Errorf("path = %q, want /v1/push", capturedPath)
	}
	if capturedInstance != "test-instance" {
		t.Errorf("X-Huskwoot-Instance = %q", capturedInstance)
	}
	expectedTS := strconv.FormatInt(fixedNow.Unix(), 10)
	if capturedTS != expectedTS {
		t.Errorf("X-Huskwoot-Timestamp = %q, want %q", capturedTS, expectedTS)
	}

	// Verify the signature
	expectedSig := pushproto.Sign(testSecret, http.MethodPost, "/v1/push", capturedTS, capturedBody)
	if capturedSig != expectedSig {
		t.Errorf("signature mismatch: got %q, want %q", capturedSig, expectedSig)
	}

	// Verify via pushproto.Verify
	if err := pushproto.Verify(testSecret, capturedSig, http.MethodPost, "/v1/push", capturedTS, capturedBody); err != nil {
		t.Errorf("Verify returned error: %v", err)
	}
}

func TestRelayClient_Push_ParsesSentResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pushproto.PushResponse{Status: pushproto.StatusSent})
	}))
	defer srv.Close()

	client := newTestClient(srv)
	resp, err := client.Push(context.Background(), pushproto.PushRequest{DeviceID: "d1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != pushproto.StatusSent {
		t.Errorf("Status = %q, want %q", resp.Status, pushproto.StatusSent)
	}
}

func TestRelayClient_Push_ParsesInvalidTokenResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pushproto.PushResponse{
			Status:  pushproto.StatusInvalidToken,
			Message: "токен недействителен",
		})
	}))
	defer srv.Close()

	client := newTestClient(srv)
	resp, err := client.Push(context.Background(), pushproto.PushRequest{DeviceID: "d1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != pushproto.StatusInvalidToken {
		t.Errorf("Status = %q, want %q", resp.Status, pushproto.StatusInvalidToken)
	}
}

func TestRelayClient_Push_ParsesUpstreamError_WithRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pushproto.PushResponse{
			Status:     pushproto.StatusUpstreamError,
			RetryAfter: 30,
		})
	}))
	defer srv.Close()

	client := newTestClient(srv)
	resp, err := client.Push(context.Background(), pushproto.PushRequest{DeviceID: "d1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != pushproto.StatusUpstreamError {
		t.Errorf("Status = %q", resp.Status)
	}
	if resp.RetryAfter != 30 {
		t.Errorf("RetryAfter = %d, want 30", resp.RetryAfter)
	}
}

func TestRelayClient_Push_NetworkError_WrapsAsErrRelayUnavailable(t *testing.T) {
	// Closed server — connection will be refused
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	client := newTestClient(srv)
	_, err := client.Push(context.Background(), pushproto.PushRequest{DeviceID: "d1"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrRelayUnavailable) {
		t.Errorf("error %v does not contain ErrRelayUnavailable", err)
	}
}

func TestRelayClient_Push_5xxError_WrapsAsErrRelayUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	_, err := client.Push(context.Background(), pushproto.PushRequest{DeviceID: "d1"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrRelayUnavailable) {
		t.Errorf("error %v does not contain ErrRelayUnavailable", err)
	}
}

func TestRelayClient_UpsertRegistration_Returns204(t *testing.T) {
	var capturedMethod, capturedPath string
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedPath = r.URL.Path
		var buf [4096]byte
		n, _ := r.Body.Read(buf[:])
		capturedBody = buf[:n]
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	apnsToken := "apns-token-xyz"
	err := client.UpsertRegistration(context.Background(), "device-abc", pushproto.RegistrationRequest{
		APNSToken: &apnsToken,
		Platform:  "ios",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", capturedMethod)
	}
	if capturedPath != "/v1/registrations/device-abc" {
		t.Errorf("path = %q", capturedPath)
	}

	// Verify JSON
	var reg pushproto.RegistrationRequest
	if err := json.Unmarshal(capturedBody, &reg); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if reg.APNSToken == nil || *reg.APNSToken != "apns-token-xyz" {
		t.Errorf("apnsToken = %v", reg.APNSToken)
	}
}

func TestRelayClient_DeleteRegistration_Idempotent(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Method != http.MethodDelete {
			t.Errorf("method = %q, want DELETE", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := newTestClient(srv)

	// First call
	if err := client.DeleteRegistration(context.Background(), "device-xyz"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Second call — idempotent
	if err := client.DeleteRegistration(context.Background(), "device-xyz"); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if callCount != 2 {
		t.Errorf("calls = %d, want 2", callCount)
	}
}

func TestRelayClient_Push_Timeout_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep for a long time — the client must abort due to timeout
		time.Sleep(200 * time.Millisecond)
		json.NewEncoder(w).Encode(pushproto.PushResponse{Status: pushproto.StatusSent})
	}))
	defer srv.Close()

	client := NewHTTPRelayClient(HTTPRelayClientConfig{
		BaseURL:    srv.URL,
		InstanceID: "test-instance",
		Secret:     testSecret,
		Timeout:    50 * time.Millisecond, // very short timeout
		Clock:      func() time.Time { return fixedNow },
	})

	_, err := client.Push(context.Background(), pushproto.PushRequest{DeviceID: "d1"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, ErrRelayUnavailable) {
		t.Errorf("error %v should contain ErrRelayUnavailable", err)
	}
}

func TestNilRelayClient_Push_ReturnsEmpty(t *testing.T) {
	client := NilRelayClient{}
	resp, err := client.Push(context.Background(), pushproto.PushRequest{DeviceID: "d1"})
	if err != nil {
		t.Fatalf("NilRelayClient.Push returned error: %v", err)
	}
	if resp.Status != "" {
		t.Errorf("status is not empty: %q", resp.Status)
	}
}

func TestNilRelayClient_UpsertRegistration_ReturnsNil(t *testing.T) {
	client := NilRelayClient{}
	if err := client.UpsertRegistration(context.Background(), "d1", pushproto.RegistrationRequest{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNilRelayClient_DeleteRegistration_ReturnsNil(t *testing.T) {
	client := NilRelayClient{}
	if err := client.DeleteRegistration(context.Background(), "d1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRelayClient_DeleteRegistration_SignsRequest(t *testing.T) {
	var capturedSig, capturedTS string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedSig = r.Header.Get("X-Huskwoot-Signature")
		capturedTS = r.Header.Get("X-Huskwoot-Timestamp")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	if err := client.DeleteRegistration(context.Background(), "dev-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Empty body for DELETE
	if err := pushproto.Verify(testSecret, capturedSig, http.MethodDelete, "/v1/registrations/dev-1", capturedTS, []byte{}); err != nil {
		t.Errorf("DELETE signature is invalid: %v", err)
	}
}
