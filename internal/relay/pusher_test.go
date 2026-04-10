package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/pushproto"
)

// mockPusherStore implements pusherStore for tests.
type mockPusherStore struct {
	reg           *Registration
	getErr        error
	deleteErr     error
	deletedKey    string
	markedUsedKey string
}

func (m *mockPusherStore) GetRegistration(_ context.Context, _, _ string) (*Registration, error) {
	return m.reg, m.getErr
}

func (m *mockPusherStore) DeleteRegistration(_ context.Context, instanceID, deviceID string) error {
	m.deletedKey = instanceID + "/" + deviceID
	return m.deleteErr
}

func (m *mockPusherStore) MarkUsed(_ context.Context, instanceID, deviceID string, _ time.Time) error {
	m.markedUsedKey = instanceID + "/" + deviceID
	return nil
}

// mockPushSender implements PushSender for tests.
type mockPushSender struct {
	sendErr   error
	lastToken string
	lastReq   pushproto.PushRequest
}

func (m *mockPushSender) Send(_ context.Context, deviceToken string, req pushproto.PushRequest) error {
	m.lastToken = deviceToken
	m.lastReq = req
	return m.sendErr
}

func makePusherRequest(deviceID, instanceID string) *http.Request {
	body, _ := json.Marshal(pushproto.PushRequest{
		DeviceID: deviceID,
		Priority: "high",
		Notification: pushproto.Notification{
			Title: "Тест",
			Body:  "Тестовое уведомление",
		},
		Data: pushproto.Data{Kind: "task_created", EventSeq: 42},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/push", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), instanceIDKey, instanceID)
	return req.WithContext(ctx)
}

func TestPusher_Success_Returns200Sent(t *testing.T) {
	apnsToken := "apns-abc"
	store := &mockPusherStore{
		reg: &Registration{
			InstanceID: "inst1",
			DeviceID:   "dev1",
			APNSToken:  &apnsToken,
			Platform:   "ios",
		},
	}
	sender := &mockPushSender{}
	pusher := NewPusher(store, sender, nil, time.Now)

	rw := httptest.NewRecorder()
	pusher.Push(rw, makePusherRequest("dev1", "inst1"))

	if rw.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rw.Code, rw.Body.String())
	}
	var resp pushproto.PushResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Status != pushproto.StatusSent {
		t.Fatalf("want status=%q, got %q", pushproto.StatusSent, resp.Status)
	}
	if sender.lastToken != apnsToken {
		t.Fatalf("want token=%q, got %q", apnsToken, sender.lastToken)
	}
	if store.markedUsedKey != "inst1/dev1" {
		t.Fatalf("MarkUsed not called for inst1/dev1, called for %q", store.markedUsedKey)
	}
}

func TestPusher_InvalidToken_DeletesRegistration(t *testing.T) {
	apnsToken := "bad-token"
	store := &mockPusherStore{
		reg: &Registration{
			InstanceID: "inst1",
			DeviceID:   "dev1",
			APNSToken:  &apnsToken,
			Platform:   "ios",
		},
	}
	sender := &mockPushSender{sendErr: ErrInvalidToken}
	pusher := NewPusher(store, sender, nil, time.Now)

	rw := httptest.NewRecorder()
	pusher.Push(rw, makePusherRequest("dev1", "inst1"))

	if rw.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rw.Code, rw.Body.String())
	}
	var resp pushproto.PushResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Status != pushproto.StatusInvalidToken {
		t.Fatalf("want status=%q, got %q", pushproto.StatusInvalidToken, resp.Status)
	}
	if store.deletedKey != "inst1/dev1" {
		t.Fatalf("DeleteRegistration not called for inst1/dev1, called for %q", store.deletedKey)
	}
}

func TestPusher_UpstreamError_ReturnsRetryAfter(t *testing.T) {
	apnsToken := "apns-xyz"
	store := &mockPusherStore{
		reg: &Registration{
			InstanceID: "inst1",
			DeviceID:   "dev1",
			APNSToken:  &apnsToken,
			Platform:   "ios",
		},
	}
	sender := &mockPushSender{sendErr: &TemporaryError{RetryAfter: 30, Cause: errors.New("временный сбой")}}
	pusher := NewPusher(store, sender, nil, time.Now)

	rw := httptest.NewRecorder()
	pusher.Push(rw, makePusherRequest("dev1", "inst1"))

	if rw.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rw.Code)
	}
	var resp pushproto.PushResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Status != pushproto.StatusUpstreamError {
		t.Fatalf("want status=%q, got %q", pushproto.StatusUpstreamError, resp.Status)
	}
	if resp.RetryAfter != 30 {
		t.Fatalf("want retryAfter=30, got %d", resp.RetryAfter)
	}
}

func TestPusher_UnknownDevice_ReturnsInvalidToken(t *testing.T) {
	store := &mockPusherStore{reg: nil}
	pusher := NewPusher(store, &mockPushSender{}, nil, time.Now)

	rw := httptest.NewRecorder()
	pusher.Push(rw, makePusherRequest("unknown-dev", "inst1"))

	if rw.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", rw.Code, rw.Body.String())
	}
	var resp pushproto.PushResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Status != pushproto.StatusInvalidToken {
		t.Fatalf("want status=%q, got %q", pushproto.StatusInvalidToken, resp.Status)
	}
}

func TestPusher_NoTokens_ReturnsBadPayload(t *testing.T) {
	store := &mockPusherStore{
		reg: &Registration{
			InstanceID: "inst1",
			DeviceID:   "dev1",
			APNSToken:  nil,
			FCMToken:   nil,
			Platform:   "ios",
		},
	}
	pusher := NewPusher(store, &mockPushSender{}, &mockPushSender{}, time.Now)

	rw := httptest.NewRecorder()
	pusher.Push(rw, makePusherRequest("dev1", "inst1"))

	if rw.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rw.Code, rw.Body.String())
	}
	var resp pushproto.PushResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Status != pushproto.StatusBadPayload {
		t.Fatalf("want status=%q, got %q", pushproto.StatusBadPayload, resp.Status)
	}
}

func TestPusher_IOSRegistration_UsesAPNs(t *testing.T) {
	apnsToken := "ios-token"
	fcmToken := "fcm-token"
	store := &mockPusherStore{
		reg: &Registration{
			InstanceID: "inst1",
			DeviceID:   "dev1",
			APNSToken:  &apnsToken,
			FCMToken:   &fcmToken,
			Platform:   "ios",
		},
	}
	apnsSender := &mockPushSender{}
	fcmSender := &mockPushSender{}
	pusher := NewPusher(store, apnsSender, fcmSender, time.Now)

	rw := httptest.NewRecorder()
	pusher.Push(rw, makePusherRequest("dev1", "inst1"))

	if apnsSender.lastToken != apnsToken {
		t.Fatalf("expected APNs to be used with token=%q, lastToken=%q", apnsToken, apnsSender.lastToken)
	}
	if fcmSender.lastToken != "" {
		t.Fatalf("FCM must not be called for iOS, but was called with token=%q", fcmSender.lastToken)
	}
}

func TestPusher_AndroidRegistration_UsesFCM(t *testing.T) {
	fcmToken := "fcm-android-token"
	store := &mockPusherStore{
		reg: &Registration{
			InstanceID: "inst1",
			DeviceID:   "dev1",
			FCMToken:   &fcmToken,
			Platform:   "android",
		},
	}
	apnsSender := &mockPushSender{}
	fcmSender := &mockPushSender{}
	pusher := NewPusher(store, apnsSender, fcmSender, time.Now)

	rw := httptest.NewRecorder()
	pusher.Push(rw, makePusherRequest("dev1", "inst1"))

	if fcmSender.lastToken != fcmToken {
		t.Fatalf("expected FCM to be used with token=%q, lastToken=%q", fcmToken, fcmSender.lastToken)
	}
	if apnsSender.lastToken != "" {
		t.Fatalf("APNs must not be called for Android, but was called with token=%q", apnsSender.lastToken)
	}
}
