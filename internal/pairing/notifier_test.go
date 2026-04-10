package pairing

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// tgNotifierResponse is a minimal Telegram Bot API response.
type tgNotifierResponse struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`

	ErrorCode   int    `json:"error_code,omitempty"`
	Description string `json:"description,omitempty"`
}

var notifierGetMeResult = map[string]any{
	"id":         99999,
	"is_bot":     true,
	"first_name": "PairingBot",
	"username":   "pairing_test_bot",
}

// newNotifierTestServer creates an httptest server that simulates the Telegram Bot API.
// sendHandler is called for requests to the sendMessage method.
func newNotifierTestServer(t *testing.T, sendHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/getMe") {
			result, _ := json.Marshal(notifierGetMeResult)
			json.NewEncoder(w).Encode(tgNotifierResponse{OK: true, Result: result})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			sendHandler(w, r)
			return
		}
		http.Error(w, "unknown method", http.StatusNotFound)
	}))
}

// notifierSuccessSend returns a successful sendMessage response.
func notifierSuccessSend(w http.ResponseWriter, _ *http.Request) {
	result, _ := json.Marshal(map[string]any{
		"message_id": 1,
		"chat":       map[string]any{"id": 111},
		"text":       "ok",
		"date":       1234567890,
	})
	json.NewEncoder(w).Encode(tgNotifierResponse{OK: true, Result: result})
}

// newNotifierTestBot creates a *tgbotapi.BotAPI connected to the test server.
func newNotifierTestBot(t *testing.T, srv *httptest.Server) *tgbotapi.BotAPI {
	t.Helper()
	bot, err := tgbotapi.NewBotAPIWithClient("test-token", srv.URL+"/bot%s/%s", http.DefaultClient)
	if err != nil {
		t.Fatalf("creating test bot: %v", err)
	}
	return bot
}

func TestBotAPISender_SendMagicLink_FormatsMessage(t *testing.T) {
	const (
		wantChatID   = int64(123456789)
		wantDevName  = "iPhone 17"
		wantMagicURL = "https://example.com/pair/confirm/test-pair-id"
	)

	var capturedChatID string
	var capturedText string
	var capturedDisablePreview string

	srv := newNotifierTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parsing request form: %v", err)
		}
		capturedChatID = r.FormValue("chat_id")
		capturedText = r.FormValue("text")
		capturedDisablePreview = r.FormValue("disable_web_page_preview")
		notifierSuccessSend(w, r)
	})
	defer srv.Close()

	bot := newNotifierTestBot(t, srv)
	sender := NewTelegramSender(bot, slog.Default())

	err := sender.SendMagicLink(context.Background(), wantChatID, wantDevName, wantMagicURL)
	if err != nil {
		t.Fatalf("SendMagicLink returned error: %v", err)
	}

	if capturedChatID == "" {
		t.Fatal("sendMessage was not called")
	}

	if capturedChatID != "123456789" {
		t.Errorf("chat_id = %q, want %q", capturedChatID, "123456789")
	}

	if !strings.Contains(capturedText, wantDevName) {
		t.Errorf("text does not contain device name %q:\n%s", wantDevName, capturedText)
	}

	if !strings.Contains(capturedText, wantMagicURL) {
		t.Errorf("text does not contain magic-URL %q:\n%s", wantMagicURL, capturedText)
	}

	if capturedDisablePreview != "true" {
		t.Errorf("disable_web_page_preview = %q, want %q", capturedDisablePreview, "true")
	}
}

func TestBotAPISender_SendMagicLink_APIError(t *testing.T) {
	srv := newNotifierTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tgNotifierResponse{
			OK:          false,
			ErrorCode:   400,
			Description: "Bad Request: chat not found",
		})
	})
	defer srv.Close()

	bot := newNotifierTestBot(t, srv)
	sender := NewTelegramSender(bot, slog.Default())

	err := sender.SendMagicLink(context.Background(), 999, "Test Device", "https://example.com/confirm")
	if err == nil {
		t.Fatal("expected error when API responds with ok=false")
	}
}

func TestNoopSender_DoesNothing(t *testing.T) {
	sendCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sendCalled = true
		http.Error(w, "must not be called", http.StatusInternalServerError)
	}))
	defer srv.Close()

	// NewTelegramSender with a nil bot must return noopSender
	sender := NewTelegramSender(nil, slog.Default())

	err := sender.SendMagicLink(context.Background(), 123, "TestDevice", "https://example.com/confirm/abc")
	if err != nil {
		t.Fatalf("noopSender.SendMagicLink returned error: %v", err)
	}

	if sendCalled {
		t.Fatal("noopSender must not make HTTP requests")
	}
}
