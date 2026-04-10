package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/anadale/huskwoot/internal/pushproto"
)

type mockRegStore struct {
	registrations map[string]*RegistrationFields
	upsertErr     error
	deleteErr     error
}

func newMockRegStore() *mockRegStore {
	return &mockRegStore{registrations: make(map[string]*RegistrationFields)}
}

func (m *mockRegStore) UpsertRegistration(_ context.Context, instanceID, deviceID string, reg RegistrationFields) error {
	if m.upsertErr != nil {
		return m.upsertErr
	}
	cp := reg
	m.registrations[instanceID+"/"+deviceID] = &cp
	return nil
}

func (m *mockRegStore) DeleteRegistration(_ context.Context, instanceID, deviceID string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.registrations, instanceID+"/"+deviceID)
	return nil
}

func registrarReq(method, path string, body []byte, instanceID, deviceID string) *http.Request {
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}

	ctx := context.WithValue(req.Context(), instanceIDKey, instanceID)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("deviceID", deviceID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

func TestRegistrar_Put_UpsertsRegistration(t *testing.T) {
	store := newMockRegStore()
	rg := NewRegistrar(store)

	apnsToken := "apns-token-abc"
	body, _ := json.Marshal(pushproto.RegistrationRequest{
		APNSToken: &apnsToken,
		Platform:  "ios",
	})

	rw := httptest.NewRecorder()
	rg.Put(rw, registrarReq(http.MethodPut, "/v1/registrations/device-123", body, "inst1", "device-123"))

	if rw.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", rw.Code, rw.Body.String())
	}

	got, ok := store.registrations["inst1/device-123"]
	if !ok {
		t.Fatal("record not created in store")
	}
	if got.APNSToken == nil || *got.APNSToken != apnsToken {
		t.Fatalf("invalid APNSToken: %v", got.APNSToken)
	}
	if got.Platform != "ios" {
		t.Fatalf("invalid Platform: %q", got.Platform)
	}
}

func TestRegistrar_Put_RejectsEmptyTokens(t *testing.T) {
	store := newMockRegStore()
	rg := NewRegistrar(store)

	body, _ := json.Marshal(pushproto.RegistrationRequest{Platform: "ios"})

	rw := httptest.NewRecorder()
	rg.Put(rw, registrarReq(http.MethodPut, "/v1/registrations/device-123", body, "inst1", "device-123"))

	if rw.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", rw.Code, rw.Body.String())
	}

	var resp authErrorResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Code != "empty_tokens" {
		t.Fatalf("want code=empty_tokens, got %q", resp.Code)
	}
}

func TestRegistrar_Delete_Idempotent(t *testing.T) {
	store := newMockRegStore()
	rg := NewRegistrar(store)

	for i := range 2 {
		rw := httptest.NewRecorder()
		rg.Delete(rw, registrarReq(http.MethodDelete, "/v1/registrations/device-123", nil, "inst1", "device-123"))

		if rw.Code != http.StatusNoContent {
			t.Fatalf("call %d: want 204, got %d", i+1, rw.Code)
		}
	}
}

func TestRegistrar_Put_InvalidJSON_Returns400(t *testing.T) {
	store := newMockRegStore()
	rg := NewRegistrar(store)

	rw := httptest.NewRecorder()
	rg.Put(rw, registrarReq(http.MethodPut, "/v1/registrations/device-123", []byte(`{not valid json}`), "inst1", "device-123"))

	if rw.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rw.Code, rw.Body.String())
	}

	var resp authErrorResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Code != "bad_json" {
		t.Fatalf("want code=bad_json, got %q", resp.Code)
	}
}
