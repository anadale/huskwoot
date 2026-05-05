package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anadale/huskwoot/internal/api"
	"github.com/anadale/huskwoot/internal/devices"
	"github.com/anadale/huskwoot/internal/model"
)

// mockTranscriber implements model.Transcriber for testing.
type mockTranscriber struct{}

func (mockTranscriber) Transcribe(_ context.Context, _ model.AudioInput) (string, error) {
	return "", nil
}
func (mockTranscriber) Name() string { return "mock" }

func newMeTestServer(t *testing.T, transcriber model.Transcriber) (*api.Server, string) {
	t.Helper()
	db := openTestDB(t)
	store := devices.NewSQLiteDeviceStore(db)
	token := "me-test-token"
	createTestDevice(t, db, "me-device", token)
	srv := api.New(api.Config{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          db,
		Devices:     store,
		Transcriber: transcriber,
		Owner: api.OwnerInfo{
			UserName:       "Alice",
			TelegramUserID: 42,
		},
	})
	return srv, token
}

func doGetMe(t *testing.T, srv *api.Server, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestGetMe_VoiceEnabled_WhenTranscriberNotNil(t *testing.T) {
	srv, token := newMeTestServer(t, mockTranscriber{})
	rec := doGetMe(t, srv, token)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	got, ok := body["voiceEnabled"]
	if !ok {
		t.Fatal("voiceEnabled field missing from response")
	}
	if got != true {
		t.Errorf("voiceEnabled = %v, want true", got)
	}
}

func TestGetMe_VoiceDisabled_WhenTranscriberNil(t *testing.T) {
	srv, token := newMeTestServer(t, nil)
	rec := doGetMe(t, srv, token)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	got, ok := body["voiceEnabled"]
	if !ok {
		t.Fatal("voiceEnabled field missing from response")
	}
	if got != false {
		t.Errorf("voiceEnabled = %v, want false", got)
	}
}

func TestGetMe_BackwardsCompat_OtherFieldsUnchanged(t *testing.T) {
	srv, token := newMeTestServer(t, nil)
	rec := doGetMe(t, srv, token)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}

	if body["ownerName"] != "Alice" {
		t.Errorf("ownerName = %v, want Alice", body["ownerName"])
	}
	if body["telegramUserId"] != float64(42) {
		t.Errorf("telegramUserId = %v, want 42", body["telegramUserId"])
	}
	if body["version"] == nil || body["version"] == "" {
		t.Error("version is missing or empty")
	}
	if body["deviceId"] == nil || body["deviceId"] == "" {
		t.Error("deviceId is missing or empty")
	}
}
