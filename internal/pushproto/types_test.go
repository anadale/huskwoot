package pushproto_test

import (
	"encoding/json"
	"testing"

	"github.com/anadale/huskwoot/internal/pushproto"
)

func TestPushRequest_JSONRoundTrip(t *testing.T) {
	badge := 3
	req := pushproto.PushRequest{
		DeviceID:    "device-uuid-1",
		Priority:    "high",
		CollapseKey: "tasks",
		Notification: pushproto.Notification{
			Title: "Новая задача",
			Body:  "inbox#42: написать тесты",
			Badge: &badge,
		},
		Data: pushproto.Data{
			Kind:      "task_created",
			EventSeq:  100,
			TaskID:    "task-uuid",
			DisplayID: "inbox#42",
		},
	}

	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Verify camelCase keys.
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	for _, key := range []string{"deviceId", "priority", "collapseKey", "notification", "data"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("key %q not found in JSON", key)
		}
	}

	notif, _ := raw["notification"].(map[string]any)
	for _, key := range []string{"title", "body", "badge"} {
		if _, ok := notif[key]; !ok {
			t.Errorf("notification.%q not found in JSON", key)
		}
	}

	data, _ := raw["data"].(map[string]any)
	for _, key := range []string{"kind", "eventSeq", "taskId", "displayId"} {
		if _, ok := data[key]; !ok {
			t.Errorf("data.%q not found in JSON", key)
		}
	}

	// Deserialize back.
	var got pushproto.PushRequest
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.DeviceID != req.DeviceID {
		t.Errorf("DeviceID = %q, want %q", got.DeviceID, req.DeviceID)
	}
	if got.Data.EventSeq != req.Data.EventSeq {
		t.Errorf("EventSeq = %d, want %d", got.Data.EventSeq, req.Data.EventSeq)
	}
	if got.Notification.Badge == nil || *got.Notification.Badge != 3 {
		t.Error("Badge must be 3")
	}
}

func TestPushResponse_JSONRoundTrip(t *testing.T) {
	resp := pushproto.PushResponse{
		Status:     pushproto.StatusUpstreamError,
		RetryAfter: 30,
		Message:    "временная ошибка APNs",
	}

	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	for _, key := range []string{"status", "retryAfter", "message"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("key %q not found in JSON", key)
		}
	}

	var got pushproto.PushResponse
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Status != pushproto.StatusUpstreamError {
		t.Errorf("Status = %q, want %q", got.Status, pushproto.StatusUpstreamError)
	}
	if got.RetryAfter != 30 {
		t.Errorf("RetryAfter = %d, want 30", got.RetryAfter)
	}
}

func TestRegistrationRequest_JSONRoundTrip(t *testing.T) {
	apns := "apns-token-abc"
	req := pushproto.RegistrationRequest{
		APNSToken: &apns,
		FCMToken:  nil,
		Platform:  "ios",
	}

	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if _, ok := raw["apnsToken"]; !ok {
		t.Error("key apnsToken not found in JSON")
	}
	// fcmToken is optional and must be absent from JSON when nil.
	if _, ok := raw["fcmToken"]; ok {
		t.Error("fcmToken must not be in JSON when nil")
	}
	if raw["platform"] != "ios" {
		t.Errorf("platform = %v, want ios", raw["platform"])
	}

	var got pushproto.RegistrationRequest
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.APNSToken == nil || *got.APNSToken != apns {
		t.Error("APNSToken does not match")
	}
}

func TestStatusConstants(t *testing.T) {
	// Verify that constants have the correct string values.
	cases := []struct {
		got  string
		want string
	}{
		{pushproto.StatusSent, "sent"},
		{pushproto.StatusInvalidToken, "invalid_token"},
		{pushproto.StatusUpstreamError, "upstream_error"},
		{pushproto.StatusBadPayload, "bad_payload"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("constant = %q, want %q", tc.got, tc.want)
		}
	}
}
