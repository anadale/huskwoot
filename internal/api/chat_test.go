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
	"path/filepath"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/api"
	"github.com/anadale/huskwoot/internal/devices"
	"github.com/anadale/huskwoot/internal/events"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/push"
	"github.com/anadale/huskwoot/internal/storage"
	"github.com/anadale/huskwoot/internal/usecase"
)

// fakeChatService is a mock ChatService for testing the handler without a real agent.
// It records the last received message and returns a pre-configured reply.
// If sleep > 0, it sleeps for that duration (used by TestPostChatRespectsTimeout).
type fakeChatService struct {
	reply    model.ChatReply
	err      error
	sleep    time.Duration
	lastMsg  model.Message
	callsNum int
}

func (f *fakeChatService) HandleMessage(ctx context.Context, msg model.Message) (model.ChatReply, error) {
	f.callsNum++
	f.lastMsg = msg
	if f.sleep > 0 {
		select {
		case <-time.After(f.sleep):
		case <-ctx.Done():
			return model.ChatReply{}, ctx.Err()
		}
	}
	if f.err != nil {
		return model.ChatReply{}, f.err
	}
	return f.reply, nil
}

// chatTestHarness assembles an api.Server with a fakeChatService and a real SQLiteHistory
// for testing the /v1/chat and /v1/chat/history endpoint behaviour.
type chatTestHarness struct {
	t       *testing.T
	db      *sql.DB
	server  *api.Server
	token   string
	device  *model.Device
	chat    *fakeChatService
	history *storage.SQLiteHistory
	cfg     api.Config
}

func newChatHarness(t *testing.T) *chatTestHarness {
	t.Helper()
	return newChatHarnessWith(t, &fakeChatService{reply: model.ChatReply{Text: "ok"}}, 0)
}

func newChatHarnessWith(t *testing.T, chat *fakeChatService, chatTimeout time.Duration) *chatTestHarness {
	t.Helper()
	// Use a separate DB file so we can access both history and the device-store.
	// openTestDB opens the DB in TempDir.
	dir := t.TempDir()
	db, err := storage.OpenDB(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	history := storage.NewSQLiteHistory(db, storage.SQLiteHistoryOptions{})

	token := "chat-test-token"
	device := createTestDevice(t, db, "test-device", token)

	cfg := api.Config{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          db,
		Devices:     devices.NewSQLiteDeviceStore(db),
		Chat:        chat,
		History:     history,
		ChatTimeout: chatTimeout,
		Owner:       api.OwnerInfo{UserName: "Oliver"},
	}
	srv := api.New(cfg)

	return &chatTestHarness{
		t:       t,
		db:      db,
		server:  srv,
		token:   token,
		device:  device,
		chat:    chat,
		history: history,
		cfg:     cfg,
	}
}

func (h *chatTestHarness) do(method, target string, body any, headers map[string]string) *httptest.ResponseRecorder {
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
	r.Header.Set("Authorization", "Bearer "+h.token)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.server.Handler().ServeHTTP(rec, r)
	return rec
}

// ---- POST /v1/chat tests ----

func TestPostChatReturnsReplyAndTouched(t *testing.T) {
	chat := &fakeChatService{reply: model.ChatReply{
		Text:            "вот ответ",
		TasksTouched:    []string{"t-1", "t-2"},
		ProjectsTouched: []string{"p-1"},
	}}
	h := newChatHarnessWith(t, chat, 0)

	rec := h.do(http.MethodPost, "/v1/chat", map[string]string{"message": "привет"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Reply           string   `json:"reply"`
		TasksTouched    []string `json:"tasksTouched"`
		ProjectsTouched []string `json:"projectsTouched"`
	}
	decodeJSONResp(t, rec.Body, &resp)
	if resp.Reply != "вот ответ" {
		t.Fatalf("reply=%q", resp.Reply)
	}
	if len(resp.TasksTouched) != 2 || resp.TasksTouched[0] != "t-1" {
		t.Fatalf("tasks_touched=%v", resp.TasksTouched)
	}
	if len(resp.ProjectsTouched) != 1 || resp.ProjectsTouched[0] != "p-1" {
		t.Fatalf("projects_touched=%v", resp.ProjectsTouched)
	}

	if chat.callsNum != 1 {
		t.Fatalf("chat.calls=%d, want 1", chat.callsNum)
	}
	if chat.lastMsg.Text != "привет" {
		t.Fatalf("msg.Text=%q", chat.lastMsg.Text)
	}
	if chat.lastMsg.Kind != model.MessageKindDM {
		t.Fatalf("msg.Kind=%q, want dm", chat.lastMsg.Kind)
	}
	wantAccount := "client:" + h.device.ID
	if chat.lastMsg.Source.AccountID != wantAccount {
		t.Fatalf("Source.AccountID=%q, want %q", chat.lastMsg.Source.AccountID, wantAccount)
	}
	if chat.lastMsg.Source.Kind != "client" {
		t.Fatalf("Source.Kind=%q, want client", chat.lastMsg.Source.Kind)
	}
}

func TestPostChatEmptyMessageReturns422(t *testing.T) {
	h := newChatHarness(t)

	rec := h.do(http.MethodPost, "/v1/chat", map[string]string{"message": "   "}, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	body := readErrorBody(t, rec.Body)
	if body.Error.Code != api.ErrorCodeUnprocessable {
		t.Fatalf("code=%q", body.Error.Code)
	}
}

func TestPostChatBadJSONReturns400(t *testing.T) {
	h := newChatHarness(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat", bytes.NewReader([]byte("not json")))
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rec.Code)
	}
}

func TestPostChatRespectsTimeout(t *testing.T) {
	// Agent sleeps longer than ChatTimeout → should return 504.
	chat := &fakeChatService{sleep: 200 * time.Millisecond}
	h := newChatHarnessWith(t, chat, 20*time.Millisecond)

	rec := h.do(http.MethodPost, "/v1/chat", map[string]string{"message": "долго"}, nil)
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status=%d, want 504; body=%s", rec.Code, rec.Body.String())
	}
	body := readErrorBody(t, rec.Body)
	if body.Error.Code != api.ErrorCodeTimeout {
		t.Fatalf("code=%q, want %q", body.Error.Code, api.ErrorCodeTimeout)
	}
}

func TestPostChatAgentErrorReturns500(t *testing.T) {
	chat := &fakeChatService{err: errors.New("boom")}
	h := newChatHarnessWith(t, chat, 0)

	rec := h.do(http.MethodPost, "/v1/chat", map[string]string{"message": "ой"}, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}

func TestPostChatIdempotencyKey(t *testing.T) {
	chat := &fakeChatService{reply: model.ChatReply{Text: "один раз"}}
	h := newChatHarnessWith(t, chat, 0)

	headers := map[string]string{api.IdempotencyHeader: "same-key"}
	rec1 := h.do(http.MethodPost, "/v1/chat", map[string]string{"message": "привет"}, headers)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first status=%d, body=%s", rec1.Code, rec1.Body.String())
	}
	rec2 := h.do(http.MethodPost, "/v1/chat", map[string]string{"message": "привет"}, headers)
	if rec2.Code != http.StatusOK {
		t.Fatalf("retry status=%d", rec2.Code)
	}
	if rec1.Body.String() != rec2.Body.String() {
		t.Fatalf("retry response differs:\n%s\n vs \n%s", rec1.Body.String(), rec2.Body.String())
	}
	if chat.callsNum != 1 {
		t.Fatalf("chat.calls=%d, want 1 (second request must return cached response)", chat.callsNum)
	}
}

// ---- GET /v1/chat/history tests ----

func TestChatHistoryReturnsClientSourceOnly(t *testing.T) {
	h := newChatHarness(t)
	ctx := context.Background()

	// Entry in a foreign source: owner's Telegram DM — must not appear in the response.
	if err := h.history.Add(ctx, "telegram:12345", model.HistoryEntry{
		AuthorName: "owner",
		Text:       "секретный телеграм",
		Timestamp:  time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("Add telegram: %v", err)
	}

	// Entry in the client chat for the same device.
	clientSource := "client:" + h.device.ID
	if err := h.history.Add(ctx, clientSource, model.HistoryEntry{
		AuthorName: h.device.ID,
		Text:       "привет, это клиент",
		Timestamp:  time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("Add client: %v", err)
	}

	rec := h.do(http.MethodGet, "/v1/chat/history", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Entries []struct {
			AuthorName string    `json:"authorName"`
			Text       string    `json:"text"`
			Timestamp  time.Time `json:"timestamp"`
		} `json:"entries"`
	}
	decodeJSONResp(t, rec.Body, &resp)
	if len(resp.Entries) != 1 {
		t.Fatalf("entries=%d, want 1; got %+v", len(resp.Entries), resp.Entries)
	}
	if resp.Entries[0].Text != "привет, это клиент" {
		t.Fatalf("text=%q", resp.Entries[0].Text)
	}
}

func TestPostChatStoresInHistoryUnderClientSource(t *testing.T) {
	chat := &fakeChatService{reply: model.ChatReply{Text: "привет-привет"}}
	h := newChatHarnessWith(t, chat, 0)

	rec := h.do(http.MethodPost, "/v1/chat", map[string]string{"message": "как дела?"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, body=%s", rec.Code, rec.Body.String())
	}

	source := "client:" + h.device.ID
	entries, err := h.history.Recent(context.Background(), source, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries=%d, want 2 (user + agent); got %+v", len(entries), entries)
	}
	// Order of entries with the same timestamp is not guaranteed by SQLite —
	// check set-membership: both texts must be present, and the agent reply
	// must have AuthorName="agent".
	texts := map[string]string{}
	for _, e := range entries {
		texts[e.Text] = e.AuthorName
	}
	if author, ok := texts["как дела?"]; !ok || author != h.device.ID {
		t.Fatalf("user message missing or has wrong author: %+v", texts)
	}
	if author, ok := texts["привет-привет"]; !ok || author != "agent" {
		t.Fatalf("agent reply missing or has wrong author: %+v", texts)
	}
}

func TestChatHistoryLimit(t *testing.T) {
	h := newChatHarness(t)
	ctx := context.Background()

	source := "client:" + h.device.ID
	for i := 0; i < 5; i++ {
		if err := h.history.Add(ctx, source, model.HistoryEntry{
			AuthorName: h.device.ID,
			Text:       "msg",
			Timestamp:  time.Now().Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	rec := h.do(http.MethodGet, "/v1/chat/history?limit=2", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Entries []chatHistoryEntryDTO `json:"entries"`
	}
	decodeJSONResp(t, rec.Body, &resp)
	if len(resp.Entries) != 2 {
		t.Fatalf("entries=%d, want 2", len(resp.Entries))
	}
}

func TestChatHistoryInvalidLimitReturns400(t *testing.T) {
	h := newChatHarness(t)

	rec := h.do(http.MethodGet, "/v1/chat/history?limit=abc", nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rec.Code)
	}
}

func TestChatUnauthenticatedReturns401(t *testing.T) {
	h := newChatHarness(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat", bytes.NewReader([]byte(`{"message":"hi"}`)))
	rec := httptest.NewRecorder()
	h.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", rec.Code)
	}
}

// chatHistoryEntryDTO is a local type for decoding /v1/chat/history responses in tests.
type chatHistoryEntryDTO struct {
	AuthorName string    `json:"authorName"`
	Text       string    `json:"text"`
	Timestamp  time.Time `json:"timestamp"`
}

// TestChatEndpointsUnavailableIfServiceNotConfigured — a guard test: when
// api.Config has no ChatService, /v1/chat and /v1/chat/history are not mounted
// and return 404. This prevents breaking an already-running instance with a
// partial configuration.
func TestChatEndpointsUnavailableIfServiceNotConfigured(t *testing.T) {
	db := openTestDB(t)
	token := "no-chat-token"
	createTestDevice(t, db, "no-chat-device", token)

	// Wire up a full ProjectService so /v1/* mounts, but without Chat.
	sqliteTasks, err := storage.NewSQLiteTaskStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteTaskStore: %v", err)
	}
	tasks := storage.NewCachedTaskStore(sqliteTasks)
	meta := storage.NewSQLiteMetaStore(db)
	eventStore := events.NewSQLiteEventStore(db)
	pushQueue := push.NewSQLitePushQueue(db)
	broker := events.NewBroker(events.BrokerConfig{})

	projectSvc := usecase.NewProjectService(usecase.ProjectServiceDeps{
		DB: db, Tasks: tasks, Meta: meta, Events: eventStore, Queue: pushQueue, Broker: broker,
	})

	srv := api.New(api.Config{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:       db,
		Devices:  devices.NewSQLiteDeviceStore(db),
		Projects: projectSvc,
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat", bytes.NewReader([]byte(`{"message":"hi"}`)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", rec.Code)
	}
}
