package model

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPairingStatus_JSONMarshal(t *testing.T) {
	tests := []struct {
		name   string
		status PairingStatus
		want   string
	}{
		{"pending", PairingStatusPending, `"pending"`},
		{"confirmed", PairingStatusConfirmed, `"confirmed"`},
		{"expired", PairingStatusExpired, `"expired"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.status)
			if err != nil {
				t.Fatalf("json.Marshal(%q) returned error: %v", tc.status, err)
			}
			if string(got) != tc.want {
				t.Errorf("json.Marshal(%q) = %s, want %s", tc.status, got, tc.want)
			}
		})
	}
}

func TestPairingStatus_JSONUnmarshal(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  PairingStatus
	}{
		{"pending", `"pending"`, PairingStatusPending},
		{"confirmed", `"confirmed"`, PairingStatusConfirmed},
		{"expired", `"expired"`, PairingStatusExpired},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got PairingStatus
			if err := json.Unmarshal([]byte(tc.input), &got); err != nil {
				t.Fatalf("json.Unmarshal(%s) returned error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("json.Unmarshal(%s) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestPendingPairing_ZeroValue(t *testing.T) {
	var p PendingPairing
	if p.ID != "" {
		t.Errorf("want empty ID, got %q", p.ID)
	}
	if p.ConfirmedAt != nil {
		t.Error("ConfirmedAt must be nil for zero value")
	}
	if p.IssuedDeviceID != nil {
		t.Error("IssuedDeviceID must be nil for zero value")
	}
}

func TestPendingPairing_WithOptionalFields(t *testing.T) {
	apns := "apns-token-123"
	deviceID := "device-uuid-456"
	now := time.Now()

	p := PendingPairing{
		ID:             "pair-uuid",
		DeviceName:     "iPhone 17",
		Platform:       "ios",
		APNSToken:      &apns,
		NonceHash:      "abc123",
		CSRFHash:       "def456",
		CreatedAt:      now,
		ExpiresAt:      now.Add(5 * 60 * 1e9),
		IssuedDeviceID: &deviceID,
	}

	if p.APNSToken == nil || *p.APNSToken != apns {
		t.Errorf("APNSToken = %v, want %q", p.APNSToken, apns)
	}
	if p.FCMToken != nil {
		t.Error("FCMToken must be nil")
	}
	if p.IssuedDeviceID == nil || *p.IssuedDeviceID != deviceID {
		t.Errorf("IssuedDeviceID = %v, want %q", p.IssuedDeviceID, deviceID)
	}
}

func TestPairingResult_Fields(t *testing.T) {
	r := PairingResult{
		PairID:      "pair-1",
		Status:      PairingStatusConfirmed,
		DeviceID:    "device-1",
		BearerToken: "token-xyz",
	}
	if r.Status != PairingStatusConfirmed {
		t.Errorf("Status = %q, want %q", r.Status, PairingStatusConfirmed)
	}
	if r.BearerToken != "token-xyz" {
		t.Errorf("BearerToken = %q, want token-xyz", r.BearerToken)
	}
}
