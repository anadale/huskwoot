package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/api"
	"github.com/anadale/huskwoot/internal/devices"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/push"
	"github.com/anadale/huskwoot/internal/pushproto"
)

// devicesTestHarness assembles live dependencies for the /v1/devices endpoints:
// SQLite device store + api.Server + a valid device token.
type devicesTestHarness struct {
	t       *testing.T
	db      *sql.DB
	server  *api.Server
	token   string
	device  *model.Device
	devices model.DeviceStore
}

func newDevicesHarness(t *testing.T) *devicesTestHarness {
	t.Helper()
	db := openTestDB(t)
	store := devices.NewSQLiteDeviceStore(db)

	token := "devices-test-token"
	device := createTestDevice(t, db, "primary-device", token)

	srv := api.New(api.Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:      db,
		Devices: store,
	})

	return &devicesTestHarness{
		t:       t,
		db:      db,
		server:  srv,
		token:   token,
		device:  device,
		devices: store,
	}
}

func (h *devicesTestHarness) do(method, target string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.doWithToken(method, target, body, h.token)
}

func (h *devicesTestHarness) doWithToken(method, target string, body any, token string) *httptest.ResponseRecorder {
	h.t.Helper()
	var r *http.Request
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("marshal body: %v", err)
		}
		r = httptest.NewRequest(method, target, bytes.NewReader(buf))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.server.Handler().ServeHTTP(rec, r)
	return rec
}

// ---- tests ----

func TestListDevicesReturnsAll(t *testing.T) {
	h := newDevicesHarness(t)

	second := createTestDevice(t, h.db, "second-device", "second-token")
	third := createTestDevice(t, h.db, "third-device", "third-token")
	if err := h.devices.Revoke(context.Background(), third.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	rec := h.do(http.MethodGet, "/v1/devices", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Devices []struct {
			ID         string     `json:"id"`
			Name       string     `json:"name"`
			Platform   string     `json:"platform"`
			CreatedAt  time.Time  `json:"createdAt"`
			LastSeenAt *time.Time `json:"lastSeenAt,omitempty"`
			RevokedAt  *time.Time `json:"revokedAt,omitempty"`
		} `json:"devices"`
	}
	decodeJSONResp(t, rec.Body, &resp)

	if len(resp.Devices) != 3 {
		t.Fatalf("got %d devices, want 3", len(resp.Devices))
	}

	ids := map[string]bool{}
	var revokedFound bool
	for _, d := range resp.Devices {
		ids[d.ID] = true
		if d.ID == third.ID {
			if d.RevokedAt == nil {
				t.Fatalf("device %s must be revoked", d.ID)
			}
			revokedFound = true
		}
	}
	if !ids[h.device.ID] || !ids[second.ID] || !ids[third.ID] {
		t.Fatalf("response does not contain all devices: %+v", ids)
	}
	if !revokedFound {
		t.Fatal("revoked device with populated revoked_at not found")
	}
}

func TestListDevicesDoesNotExposeTokenHash(t *testing.T) {
	h := newDevicesHarness(t)

	rec := h.do(http.MethodGet, "/v1/devices", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if bytes.Contains([]byte(body), []byte("token_hash")) {
		t.Fatalf("response must not contain token_hash: %s", body)
	}
}

func TestListDevicesUnauthenticatedReturns401(t *testing.T) {
	h := newDevicesHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/devices", nil)
	rec := httptest.NewRecorder()
	h.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", rec.Code)
	}
}

func TestPatchDevicesMeUpdatesPushTokens(t *testing.T) {
	h := newDevicesHarness(t)

	apns := "apns-token-abc"
	fcm := "fcm-token-xyz"
	rec := h.do(http.MethodPatch, "/v1/devices/me", map[string]any{
		"apnsToken": apns,
		"fcmToken":  fcm,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		ID        string  `json:"id"`
		APNSToken *string `json:"apnsToken"`
		FCMToken  *string `json:"fcmToken"`
	}
	decodeJSONResp(t, rec.Body, &resp)

	if resp.ID != h.device.ID {
		t.Fatalf("id=%q, want %q", resp.ID, h.device.ID)
	}
	if resp.APNSToken == nil || *resp.APNSToken != apns {
		t.Fatalf("apns_token=%v, want %q", resp.APNSToken, apns)
	}
	if resp.FCMToken == nil || *resp.FCMToken != fcm {
		t.Fatalf("fcm_token=%v, want %q", resp.FCMToken, fcm)
	}

	// Verify the value is actually persisted in the DB.
	devs, err := h.devices.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var stored *model.Device
	for i := range devs {
		if devs[i].ID == h.device.ID {
			stored = &devs[i]
			break
		}
	}
	if stored == nil {
		t.Fatal("device disappeared from list")
	}
	if stored.APNSToken == nil || *stored.APNSToken != apns {
		t.Fatalf("APNSToken in DB=%v", stored.APNSToken)
	}
	if stored.FCMToken == nil || *stored.FCMToken != fcm {
		t.Fatalf("FCMToken in DB=%v", stored.FCMToken)
	}
}

func TestPatchDevicesMeClearsTokensOnNull(t *testing.T) {
	h := newDevicesHarness(t)

	// Set initial token values first.
	apns := "first"
	fcm := "second"
	if err := h.devices.UpdatePushTokens(context.Background(), h.device.ID, &apns, &fcm); err != nil {
		t.Fatalf("UpdatePushTokens: %v", err)
	}

	// null clears both tokens.
	rec := h.do(http.MethodPatch, "/v1/devices/me", map[string]any{
		"apnsToken": nil,
		"fcmToken":  nil,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	devs, err := h.devices.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, d := range devs {
		if d.ID == h.device.ID {
			if d.APNSToken != nil {
				t.Fatalf("APNSToken=%v, want nil", d.APNSToken)
			}
			if d.FCMToken != nil {
				t.Fatalf("FCMToken=%v, want nil", d.FCMToken)
			}
			return
		}
	}
	t.Fatal("device not found in DB")
}

func TestPatchDevicesMePartialUpdate(t *testing.T) {
	h := newDevicesHarness(t)

	existing := "keep-me"
	if err := h.devices.UpdatePushTokens(context.Background(), h.device.ID, nil, &existing); err != nil {
		t.Fatalf("UpdatePushTokens: %v", err)
	}

	apns := "new-apns"
	rec := h.do(http.MethodPatch, "/v1/devices/me", map[string]any{"apnsToken": apns})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	devs, err := h.devices.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, d := range devs {
		if d.ID == h.device.ID {
			if d.APNSToken == nil || *d.APNSToken != apns {
				t.Fatalf("APNSToken=%v, want %q", d.APNSToken, apns)
			}
			if d.FCMToken == nil || *d.FCMToken != existing {
				t.Fatalf("FCMToken=%v, want %q (field not sent, must remain unchanged)", d.FCMToken, existing)
			}
			return
		}
	}
	t.Fatal("device not found")
}

func TestPatchDevicesMeEmptyPayloadReturns422(t *testing.T) {
	h := newDevicesHarness(t)

	rec := h.do(http.MethodPatch, "/v1/devices/me", map[string]any{})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d, want 422; body=%s", rec.Code, rec.Body.String())
	}
}

func TestPatchDevicesMeRejectsUnknownFields(t *testing.T) {
	h := newDevicesHarness(t)

	rec := h.do(http.MethodPatch, "/v1/devices/me", map[string]any{"garbage": "x"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeleteDeviceRevokes(t *testing.T) {
	h := newDevicesHarness(t)
	second := createTestDevice(t, h.db, "target", "target-token")

	rec := h.do(http.MethodDelete, "/v1/devices/"+second.ID, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	devs, err := h.devices.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, d := range devs {
		if d.ID == second.ID {
			if d.RevokedAt == nil {
				t.Fatal("устройство не отозвано")
			}
			return
		}
	}
	t.Fatal("device not found")
}

func TestDeleteDeviceNotFoundReturns404(t *testing.T) {
	h := newDevicesHarness(t)

	rec := h.do(http.MethodDelete, "/v1/devices/несуществующий", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	body := readErrorBody(t, rec.Body)
	if body.Error.Code != api.ErrorCodeNotFound {
		t.Fatalf("code=%q, want %q", body.Error.Code, api.ErrorCodeNotFound)
	}
}

// mockRelayForDevices is a mock push.RelayClient for devicesHandler tests.
type mockRelayForDevices struct {
	mu          sync.Mutex
	upsertCalls []mockDevicesUpsertCall
	upsertErr   error
	deleteCalls []string
	deleteErr   error
}

type mockDevicesUpsertCall struct {
	deviceID string
	req      pushproto.RegistrationRequest
}

func (m *mockRelayForDevices) Push(_ context.Context, _ pushproto.PushRequest) (pushproto.PushResponse, error) {
	return pushproto.PushResponse{}, nil
}

func (m *mockRelayForDevices) UpsertRegistration(_ context.Context, deviceID string, r pushproto.RegistrationRequest) error {
	m.mu.Lock()
	m.upsertCalls = append(m.upsertCalls, mockDevicesUpsertCall{deviceID, r})
	m.mu.Unlock()
	return m.upsertErr
}

func (m *mockRelayForDevices) DeleteRegistration(_ context.Context, deviceID string) error {
	m.mu.Lock()
	m.deleteCalls = append(m.deleteCalls, deviceID)
	m.mu.Unlock()
	return m.deleteErr
}

func newDevicesHarnessWithRelay(t *testing.T, relay push.RelayClient) *devicesTestHarness {
	t.Helper()
	db := openTestDB(t)
	store := devices.NewSQLiteDeviceStore(db)

	token := "devices-relay-token"
	device := createTestDevice(t, db, "primary-device", token)

	srv := api.New(api.Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:      db,
		Devices: store,
		Relay:   relay,
	})

	return &devicesTestHarness{
		t:       t,
		db:      db,
		server:  srv,
		token:   token,
		device:  device,
		devices: store,
	}
}

func TestDevicesHandler_PatchMe_CallsRelayUpsert(t *testing.T) {
	relay := &mockRelayForDevices{}
	h := newDevicesHarnessWithRelay(t, relay)

	apns := "apns-relay-test"
	rec := h.do(http.MethodPatch, "/v1/devices/me", map[string]any{
		"apnsToken": apns,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	relay.mu.Lock()
	calls := relay.upsertCalls
	relay.mu.Unlock()

	if len(calls) != 1 {
		t.Fatalf("expected 1 UpsertRegistration call, got %d", len(calls))
	}
	if calls[0].deviceID != h.device.ID {
		t.Errorf("deviceID = %q, want %q", calls[0].deviceID, h.device.ID)
	}
	if calls[0].req.APNSToken == nil || *calls[0].req.APNSToken != apns {
		t.Errorf("apnsToken = %v, want %q", calls[0].req.APNSToken, apns)
	}
}

func TestDevicesHandler_PatchMe_RelayError_ReturnsSuccess_Warning(t *testing.T) {
	relay := &mockRelayForDevices{upsertErr: errors.New("relay недоступен")}
	h := newDevicesHarnessWithRelay(t, relay)

	apns := "apns-relay-err"
	rec := h.do(http.MethodPatch, "/v1/devices/me", map[string]any{
		"apnsToken": apns,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("relay error must not block patchMe: status=%d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestDevicesHandler_Delete_CallsRelayDelete(t *testing.T) {
	relay := &mockRelayForDevices{}
	h := newDevicesHarnessWithRelay(t, relay)
	second := createTestDevice(t, h.db, "to-delete", "delete-token")

	rec := h.do(http.MethodDelete, "/v1/devices/"+second.ID, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	relay.mu.Lock()
	calls := relay.deleteCalls
	relay.mu.Unlock()

	if len(calls) != 1 {
		t.Fatalf("expected 1 DeleteRegistration call, got %d", len(calls))
	}
	if calls[0] != second.ID {
		t.Errorf("deleteID = %q, want %q", calls[0], second.ID)
	}
}

func TestDevicesHandler_Delete_RelayError_ReturnsSuccess_Warning(t *testing.T) {
	relay := &mockRelayForDevices{deleteErr: errors.New("relay недоступен")}
	h := newDevicesHarnessWithRelay(t, relay)
	second := createTestDevice(t, h.db, "to-delete-relay-err", "delete-relay-err-token")

	rec := h.do(http.MethodDelete, "/v1/devices/"+second.ID, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("relay error must not block delete: status=%d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeleteOwnDeviceRevokesAndSubsequentRequestsFail(t *testing.T) {
	h := newDevicesHarness(t)

	rec := h.do(http.MethodDelete, "/v1/devices/"+h.device.ID, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	// The next request with the same token must return 401.
	rec2 := h.do(http.MethodGet, "/v1/devices", nil)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("after self-revoke status=%d, want 401", rec2.Code)
	}
}
