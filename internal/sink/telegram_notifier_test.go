package sink

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

// tgResponse is a helper type for Bot API responses.
type tgResponse struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	// Error fields.
	ErrorCode   int    `json:"error_code,omitempty"`
	Description string `json:"description,omitempty"`
}

// getMeResult is a minimal getMe response.
var getMeResult = map[string]any{
	"id":         12345,
	"is_bot":     true,
	"first_name": "TestBot",
	"username":   "test_bot",
}

// newTGTestServer creates an httptest server that mimics the Telegram Bot API.
// sendHandler is called for the sendMessage method.
func newTGTestServer(t *testing.T, sendHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/getMe") {
			result, _ := json.Marshal(getMeResult)
			json.NewEncoder(w).Encode(tgResponse{OK: true, Result: result})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			sendHandler(w, r)
			return
		}
		http.Error(w, "unknown method", http.StatusNotFound)
	}))
}

// successSendHandler returns a successful sendMessage response.
func successSendHandler(w http.ResponseWriter, _ *http.Request) {
	result, _ := json.Marshal(map[string]any{
		"message_id": 1,
		"chat":       map[string]any{"id": 123},
		"text":       "ok",
		"date":       1234567890,
	})
	json.NewEncoder(w).Encode(tgResponse{OK: true, Result: result})
}

func makeTestTask(deadline *time.Time) model.Task {
	t := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	return model.Task{
		ID:         "1",
		Summary:    "Подготовить отчёт",
		Deadline:   deadline,
		Confidence: 0.9,
		Source: model.Source{
			Kind: "telegram",
			ID:   "-1001234567890",
			Name: "Рабочий чат",
		},
		SourceMessage: model.Message{
			ID:   "42",
			Text: "Можешь подготовить отчёт?",
		},
		CreatedAt: t,
	}
}

// newTestBot creates a *tgbotapi.BotAPI connected to the test server.
func newTestBot(t *testing.T, srv *httptest.Server) *tgbotapi.BotAPI {
	t.Helper()
	bot, err := tgbotapi.NewBotAPIWithClient("test-token", srv.URL+"/bot%s/%s", http.DefaultClient)
	if err != nil {
		t.Fatalf("creating test bot: %v", err)
	}
	return bot
}

func makeLocalizer(t *testing.T, lang string) *goI18n.Localizer {
	t.Helper()
	bundle, err := huskwootI18n.NewBundle(lang)
	if err != nil {
		t.Fatalf("NewBundle(%s): %v", lang, err)
	}
	return huskwootI18n.NewLocalizer(bundle, lang)
}

func TestTelegramNotifier_Notify_RussianLocalizer(t *testing.T) {
	var capturedText string
	srv := newTGTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parsing request form: %v", err)
		}
		capturedText = r.FormValue("text")
		successSendHandler(w, r)
	})
	defer srv.Close()

	loc := makeLocalizer(t, "ru")
	notifier := NewTelegramNotifier(newTestBot(t, srv), 123, loc)

	if err := notifier.Notify(context.Background(), []model.Task{makeTestTask(nil)}); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}
	if !strings.Contains(capturedText, "Новые задачи") {
		t.Errorf("Russian message must contain 'Новые задачи':\n%s", capturedText)
	}
}

func TestTelegramNotifier_Notify_EnglishLocalizer(t *testing.T) {
	var capturedText string
	srv := newTGTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parsing request form: %v", err)
		}
		capturedText = r.FormValue("text")
		successSendHandler(w, r)
	})
	defer srv.Close()

	loc := makeLocalizer(t, "en")
	notifier := NewTelegramNotifier(newTestBot(t, srv), 123, loc)

	if err := notifier.Notify(context.Background(), []model.Task{makeTestTask(nil)}); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}
	if !strings.Contains(capturedText, "New tasks") {
		t.Errorf("English message must contain 'New tasks':\n%s", capturedText)
	}
}

func TestTelegramNotifier_NotifySuccess(t *testing.T) {
	var capturedText string
	srv := newTGTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parsing request form: %v", err)
		}
		capturedText = r.FormValue("text")
		successSendHandler(w, r)
	})
	defer srv.Close()

	loc := makeLocalizer(t, "ru")
	notifier := NewTelegramNotifier(newTestBot(t, srv), 123, loc)

	dl := time.Date(2026, 4, 11, 18, 0, 0, 0, time.UTC)
	task := makeTestTask(&dl)

	if err := notifier.Notify(context.Background(), []model.Task{task}); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}

	if capturedText == "" {
		t.Fatal("sendMessage was not called")
	}
	if !strings.Contains(capturedText, "Подготовить отчёт") {
		t.Errorf("message does not contain task summary:\n%s", capturedText)
	}
	if !strings.Contains(capturedText, "Рабочий чат") {
		t.Errorf("message does not contain source name:\n%s", capturedText)
	}
}

func TestTelegramNotifier_APIError(t *testing.T) {
	srv := newTGTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tgResponse{
			OK:          false,
			ErrorCode:   400,
			Description: "Bad Request: chat not found",
		})
	})
	defer srv.Close()

	loc := makeLocalizer(t, "ru")
	notifier := NewTelegramNotifier(newTestBot(t, srv), 999, loc)

	err := notifier.Notify(context.Background(), []model.Task{makeTestTask(nil)})
	if err == nil {
		t.Fatal("want an error when API responds with ok=false")
	}
}

func TestTelegramNotifier_EmptyTasks(t *testing.T) {
	sendCalled := false
	srv := newTGTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		sendCalled = true
		successSendHandler(w, r)
	})
	defer srv.Close()

	loc := makeLocalizer(t, "ru")
	notifier := NewTelegramNotifier(newTestBot(t, srv), 123, loc)

	if err := notifier.Notify(context.Background(), []model.Task{}); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}
	if sendCalled {
		t.Fatal("sendMessage must not be called for an empty task list")
	}
}

func TestFormatTaskMessage_OneTask(t *testing.T) {
	loc := makeLocalizer(t, "ru")
	dl := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)
	task := makeTestTask(&dl)
	task.Details = "обсуждали на встрече"
	msg := formatTaskMessage(loc, []model.Task{task})

	checks := []struct {
		name    string
		contain string
	}{
		{"header", "✍️ Новые задачи записаны!"},
		{"source name", "Рабочий чат"},
		{"source kind", "telegram"},
		{"summary", "Подготовить отчёт"},
		{"deadline", "11.04.2026"},
		{"context", "обсуждали на встрече"},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(msg, c.contain) {
				t.Errorf("message does not contain %q:\n%s", c.contain, msg)
			}
		})
	}
}

func TestFormatTaskMessage_MultipleTasks(t *testing.T) {
	loc := makeLocalizer(t, "ru")
	dl := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	task1 := makeTestTask(nil)
	task1.Summary = "Исправить ошибку"
	task1.Details = "по результатам ревью"
	task2 := makeTestTask(&dl)
	task2.Summary = "Обновить документацию"
	task2.Details = "обновили API"

	msg := formatTaskMessage(loc, []model.Task{task1, task2})

	checks := []struct {
		name    string
		contain string
	}{
		{"header", "✍️ Новые задачи записаны!"},
		{"task1 summary", "Исправить ошибку"},
		{"task1 context", "по результатам ревью"},
		{"task2 summary", "Обновить документацию"},
		{"task2 deadline", "15.04.2026"},
		{"task2 context", "обновили API"},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(msg, c.contain) {
				t.Errorf("message does not contain %q:\n%s", c.contain, msg)
			}
		})
	}
}

func TestFormatTaskMessage_WithDeadlineAndContext(t *testing.T) {
	loc := makeLocalizer(t, "ru")
	dl := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	task := makeTestTask(&dl)
	task.Details = "по запросу команды"
	msg := formatTaskMessage(loc, []model.Task{task})

	if !strings.Contains(msg, "📅 20.04.2026") {
		t.Errorf("message does not contain deadline:\n%s", msg)
	}
	if !strings.Contains(msg, "Контекст: по запросу команды") {
		t.Errorf("message does not contain context:\n%s", msg)
	}
}

func TestFormatTaskMessage_WithoutDeadlineAndContext(t *testing.T) {
	loc := makeLocalizer(t, "ru")
	task := makeTestTask(nil)
	task.Details = ""
	msg := formatTaskMessage(loc, []model.Task{task})

	if strings.Contains(msg, "📅") {
		t.Errorf("message must not contain deadline:\n%s", msg)
	}
	if strings.Contains(msg, "Контекст:") {
		t.Errorf("message must not contain context:\n%s", msg)
	}
	if !strings.Contains(msg, "Подготовить отчёт") {
		t.Errorf("message must contain summary:\n%s", msg)
	}
}

func TestFormatTaskMessage_IMAPSource(t *testing.T) {
	loc := makeLocalizer(t, "ru")
	task := makeTestTask(nil)
	task.Source.Kind = "imap"
	task.Source.Name = "inbox"
	task.SourceMessage.Subject = "Встреча по проекту"
	task.ProjectSlug = "rabochaya-pochta"
	task.Details = "обсуждали дорожную карту"
	msg := formatTaskMessage(loc, []model.Task{task})

	if !strings.Contains(msg, "Встреча по проекту") {
		t.Errorf("message must contain Subject:\n%s", msg)
	}
	if !strings.Contains(msg, "rabochaya-pochta") {
		t.Errorf("message must contain ProjectSlug:\n%s", msg)
	}
	if strings.Contains(msg, "inbox") {
		t.Errorf("message must not contain Source.Name for IMAP:\n%s", msg)
	}
}

func TestFormatTaskMessage_IMAPEmptySubject(t *testing.T) {
	loc := makeLocalizer(t, "ru")
	task := makeTestTask(nil)
	task.Source.Kind = "imap"
	task.Source.Name = "inbox"
	task.SourceMessage.Subject = ""
	task.ProjectSlug = "rabochaya-pochta"
	msg := formatTaskMessage(loc, []model.Task{task})

	if !strings.Contains(msg, "rabochaya-pochta") {
		t.Errorf("message must contain ProjectSlug when Subject is empty:\n%s", msg)
	}
	// Ensure there is no double space in the source line.
	if strings.Contains(msg, "Источник:  ") {
		t.Errorf("message contains extra space in Источник line:\n%s", msg)
	}
}

func TestFormatTaskMessage_IMAPEmptyAccount(t *testing.T) {
	loc := makeLocalizer(t, "ru")
	task := makeTestTask(nil)
	task.Source.Kind = "imap"
	task.Source.Name = "inbox"
	task.SourceMessage.Subject = "Встреча по проекту"
	task.ProjectSlug = ""
	msg := formatTaskMessage(loc, []model.Task{task})

	if !strings.Contains(msg, "Встреча по проекту") {
		t.Errorf("message must contain Subject:\n%s", msg)
	}
	// Ensure there are no empty parentheses "()".
	if strings.Contains(msg, "()") {
		t.Errorf("message contains empty parentheses when Account is empty:\n%s", msg)
	}
}

func TestFormatTaskMessage_IMAPFallbackToSourceName(t *testing.T) {
	loc := makeLocalizer(t, "ru")
	task := makeTestTask(nil)
	task.Source.Kind = "imap"
	task.Source.Name = "inbox"
	task.SourceMessage.Subject = ""
	task.ProjectSlug = ""
	msg := formatTaskMessage(loc, []model.Task{task})

	if !strings.Contains(msg, "inbox") {
		t.Errorf("message must contain Source.Name when Subject and Account are empty:\n%s", msg)
	}
}
