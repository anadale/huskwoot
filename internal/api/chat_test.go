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
	"reflect"
	"sync"
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
	// tasks is the task service the chat handler resolves history task cards
	// through; nil in harnesses without one.
	tasks model.TaskService
	cfg   api.Config
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

	sqliteTasks, err := storage.NewSQLiteTaskStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteTaskStore: %v", err)
	}
	deviceStore := devices.NewSQLiteDeviceStore(db)
	// The task service gets its own event store and broker: the chat endpoints
	// of this harness run without events (the plain history fallback).
	taskSvc := usecase.NewTaskService(usecase.TaskServiceDeps{
		DB:      db,
		Tasks:   storage.NewCachedTaskStore(sqliteTasks),
		Events:  events.NewSQLiteEventStore(db),
		Devices: deviceStore,
		Queue:   push.NewSQLitePushQueue(db),
		Broker:  events.NewBroker(events.BrokerConfig{}),
	})

	token := "chat-test-token"
	device := createTestDevice(t, db, "test-device", token)

	cfg := api.Config{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          db,
		Devices:     deviceStore,
		Tasks:       taskSvc,
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
		tasks:   taskSvc,
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

// TestPostChatStoresRoleKindAndTasksTouched verifies that POST /v1/chat stores
// the role and kind of both messages and the agent's touched tasks, on the
// transactional path (with an event store) and on the plain history fallback.
func TestPostChatStoresRoleKindAndTasksTouched(t *testing.T) {
	tests := []struct {
		name    string
		harness func(t *testing.T, chat *fakeChatService) *chatTestHarness
	}{
		{
			name: "with events",
			harness: func(t *testing.T, chat *fakeChatService) *chatTestHarness {
				return newChatEventHarness(t, chat).chatTestHarness
			},
		},
		{
			name: "history only",
			harness: func(t *testing.T, chat *fakeChatService) *chatTestHarness {
				return newChatHarnessWith(t, chat, 0)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chat := &fakeChatService{reply: model.ChatReply{
				Text:         "created",
				TasksTouched: []string{"task-1", "task-2"},
			}}
			h := tt.harness(t, chat)

			rec := h.do(http.MethodPost, "/v1/chat", map[string]string{"message": "create two tasks"}, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d, body=%s", rec.Code, rec.Body.String())
			}

			entries, err := h.history.Recent(context.Background(), "client:"+h.device.ID, 10)
			if err != nil {
				t.Fatalf("Recent: %v", err)
			}
			if len(entries) != 2 {
				t.Fatalf("entries=%d, want 2; got %+v", len(entries), entries)
			}
			byRole := map[string]model.HistoryEntry{}
			for _, e := range entries {
				byRole[e.Role] = e
			}

			user, ok := byRole[model.HistoryRoleUser]
			if !ok {
				t.Fatalf("no user entry in %+v", entries)
			}
			if user.Text != "create two tasks" || user.Kind != model.HistoryKindText || user.AuthorName != h.device.ID {
				t.Errorf("user entry = %+v", user)
			}
			if user.TaskIDs != nil || user.AudioJobID != "" {
				t.Errorf("user entry must not carry tasks or audio: %+v", user)
			}

			agent, ok := byRole[model.HistoryRoleAgent]
			if !ok {
				t.Fatalf("no agent entry in %+v", entries)
			}
			if agent.Text != "created" || agent.Kind != model.HistoryKindText || agent.AuthorName != "agent" {
				t.Errorf("agent entry = %+v", agent)
			}
			if want := []string{"task-1", "task-2"}; !reflect.DeepEqual(agent.TaskIDs, want) {
				t.Errorf("agent TaskIDs = %v, want %v", agent.TaskIDs, want)
			}
		})
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
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
	Audio     *struct {
		JobID      string `json:"jobId"`
		DurationMs int64  `json:"durationMs"`
	} `json:"audio"`
	Tasks      []chatHistoryTaskDTO `json:"tasks"`
	AuthorName string               `json:"authorName"`
	Timestamp  time.Time            `json:"timestamp"`
}

// chatHistoryTaskDTO is a local type for decoding history task cards in tests.
type chatHistoryTaskDTO struct {
	ID       string     `json:"id"`
	Ref      string     `json:"ref"`
	Summary  string     `json:"summary"`
	Status   string     `json:"status"`
	Deadline *time.Time `json:"deadline"`
}

// chatHistoryRespDTO is a local type for decoding the /v1/chat/history response in tests.
type chatHistoryRespDTO struct {
	Entries    []chatHistoryEntryDTO `json:"entries"`
	NextBefore *string               `json:"nextBefore"`
}

// addHistory writes entry under source through AddTx and returns the message id.
func (h *chatTestHarness) addHistory(source string, entry model.HistoryEntry) string {
	h.t.Helper()
	ctx := context.Background()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		h.t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	id, err := h.history.AddTx(ctx, tx, source, entry)
	if err != nil {
		h.t.Fatalf("AddTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		h.t.Fatalf("Commit: %v", err)
	}
	return id
}

// getChatHistory requests target and decodes a 200 response.
func (h *chatTestHarness) getChatHistory(target string) chatHistoryRespDTO {
	h.t.Helper()
	rec := h.do(http.MethodGet, target, nil, nil)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("GET %s: status=%d, body=%s", target, rec.Code, rec.Body.String())
	}
	var resp chatHistoryRespDTO
	decodeJSONResp(h.t, rec.Body, &resp)
	return resp
}

func TestChatHistoryPaging(t *testing.T) {
	h := newChatHarness(t)
	source := "client:" + h.device.ID
	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	ids := map[string]string{}
	for i, text := range []string{"m1", "m2", "m3", "m4", "m5"} {
		ids[text] = h.addHistory(source, model.HistoryEntry{
			AuthorName: h.device.ID, Text: text, Timestamp: t0.Add(time.Duration(i) * time.Second),
			Role: model.HistoryRoleUser, Kind: model.HistoryKindText,
		})
		// Rows of another device and of a Telegram chat interleave with ours.
		h.addHistory("client:other-device", model.HistoryEntry{
			AuthorName: "other-device", Text: "other-" + text, Timestamp: t0.Add(time.Duration(i) * time.Second),
			Role: model.HistoryRoleUser, Kind: model.HistoryKindText,
		})
		h.addHistory("123456", makeTelegramEntry("telegram-"+text, t0.Add(time.Duration(i)*time.Second)))
	}

	wantPages := [][]string{{"m4", "m5"}, {"m2", "m3"}, {"m1"}}
	target := "/v1/chat/history?limit=2"
	for i, want := range wantPages {
		resp := h.getChatHistory(target)
		got := make([]string, 0, len(resp.Entries))
		for _, e := range resp.Entries {
			got = append(got, e.Text)
			if e.ID != ids[e.Text] {
				t.Errorf("page %d: %q id=%q, want %q", i, e.Text, e.ID, ids[e.Text])
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("page %d = %v, want %v", i, got, want)
		}

		if i == len(wantPages)-1 {
			if resp.NextBefore != nil {
				t.Fatalf("last page must omit nextBefore, got %q", *resp.NextBefore)
			}
			break
		}
		if resp.NextBefore == nil || *resp.NextBefore != ids[want[0]] {
			t.Fatalf("page %d nextBefore=%v, want the id of %q", i, resp.NextBefore, want[0])
		}
		target = "/v1/chat/history?limit=2&before=" + *resp.NextBefore
	}

	// Without a limit the whole history of this device fits on one page.
	resp := h.getChatHistory("/v1/chat/history")
	if len(resp.Entries) != 5 || resp.NextBefore != nil {
		t.Fatalf("default page: entries=%d nextBefore=%v, want 5 entries and no nextBefore", len(resp.Entries), resp.NextBefore)
	}
}

// makeTelegramEntry builds a channel history row (no role).
func makeTelegramEntry(text string, ts time.Time) model.HistoryEntry {
	return model.HistoryEntry{AuthorName: "owner", Text: text, Timestamp: ts}
}

func TestChatHistoryUnknownBeforeReturns400(t *testing.T) {
	h := newChatHarness(t)
	h.addHistory("client:"+h.device.ID, model.HistoryEntry{
		AuthorName: h.device.ID, Text: "mine", Timestamp: time.Now(), Role: model.HistoryRoleUser,
	})
	otherID := h.addHistory("client:other-device", model.HistoryEntry{
		AuthorName: "other-device", Text: "theirs", Timestamp: time.Now(), Role: model.HistoryRoleUser,
	})
	telegramID := h.addHistory("123456", makeTelegramEntry("telegram", time.Now()))

	tests := []struct {
		name   string
		before string
	}{
		{name: "missing message", before: "00000000-0000-0000-0000-000000000000"},
		{name: "message of another device", before: otherID},
		{name: "telegram message", before: telegramID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := h.do(http.MethodGet, "/v1/chat/history?before="+tt.before, nil, nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			var errResp struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			decodeJSONResp(t, rec.Body, &errResp)
			if errResp.Error.Code != "bad_request" {
				t.Fatalf("error code=%q, want bad_request; body=%s", errResp.Error.Code, rec.Body.String())
			}
		})
	}
}

func TestChatHistoryReturnsMetadataAndTaskCards(t *testing.T) {
	h := newChatHarness(t)
	ctx := context.Background()
	source := "client:" + h.device.ID

	deadline := time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC)
	first, err := h.tasks.CreateTask(ctx, model.CreateTaskRequest{Summary: "Buy tickets", Deadline: &deadline})
	if err != nil {
		t.Fatalf("CreateTask first: %v", err)
	}
	second, err := h.tasks.CreateTask(ctx, model.CreateTaskRequest{Summary: "Call mom"})
	if err != nil {
		t.Fatalf("CreateTask second: %v", err)
	}
	if _, err := h.tasks.CompleteTask(ctx, second.ID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	purged, err := h.tasks.CreateTask(ctx, model.CreateTaskRequest{Summary: "Purged"})
	if err != nil {
		t.Fatalf("CreateTask purged: %v", err)
	}
	// A physically deleted task (closed-task retention) is skipped.
	if _, err := h.db.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, purged.ID); err != nil {
		t.Fatalf("delete task: %v", err)
	}

	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	voiceID := h.addHistory(source, model.HistoryEntry{
		AuthorName: h.device.ID, Text: "buy tickets and call mom", Timestamp: t0,
		Role: model.HistoryRoleUser, Kind: model.HistoryKindVoice,
		AudioJobID: "job-1", AudioDurationMs: 9000,
	})
	agentID := h.addHistory(source, model.HistoryEntry{
		AuthorName: "agent", Text: "Done", Timestamp: t0.Add(time.Second),
		Role: model.HistoryRoleAgent, Kind: model.HistoryKindText,
		TaskIDs: []string{first.ID, purged.ID, "00000000-0000-0000-0000-000000000000", second.ID},
	})
	failureID := h.addHistory(source, model.HistoryEntry{
		AuthorName: model.HistoryRoleSystem, Text: "audio does not contain recognisable speech", Timestamp: t0.Add(2 * time.Second),
		Role: model.HistoryRoleSystem, Kind: model.HistoryKindError,
		AudioJobID: "job-2",
	})
	deletedOnlyID := h.addHistory(source, model.HistoryEntry{
		AuthorName: "agent", Text: "Removed", Timestamp: t0.Add(3 * time.Second),
		Role: model.HistoryRoleAgent, Kind: model.HistoryKindText,
		TaskIDs: []string{purged.ID},
	})

	rec := h.do(http.MethodGet, "/v1/chat/history", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, body=%s", rec.Code, rec.Body.String())
	}
	raw := rec.Body.Bytes()
	var resp chatHistoryRespDTO
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Entries) != 4 {
		t.Fatalf("entries=%d, want 4; body=%s", len(resp.Entries), raw)
	}

	firstCard := chatHistoryTaskDTO{ID: first.ID, Ref: "inbox-1", Summary: "Buy tickets", Status: "open", Deadline: &deadline}
	secondCard := chatHistoryTaskDTO{ID: second.ID, Ref: "inbox-2", Summary: "Call mom", Status: "done"}

	type audio = struct {
		JobID      string `json:"jobId"`
		DurationMs int64  `json:"durationMs"`
	}
	want := []chatHistoryEntryDTO{
		{
			ID: voiceID, Role: "user", Kind: "voice", Text: "buy tickets and call mom", CreatedAt: t0,
			Audio:      &audio{JobID: "job-1", DurationMs: 9000},
			AuthorName: h.device.ID, Timestamp: t0,
		},
		{
			ID: agentID, Role: "agent", Kind: "text", Text: "Done", CreatedAt: t0.Add(time.Second),
			Tasks:      []chatHistoryTaskDTO{firstCard, secondCard},
			AuthorName: "agent", Timestamp: t0.Add(time.Second),
		},
		{
			ID: failureID, Role: "system", Kind: "error", Text: "audio does not contain recognisable speech", CreatedAt: t0.Add(2 * time.Second),
			Audio:      &audio{JobID: "job-2", DurationMs: 0},
			AuthorName: "system", Timestamp: t0.Add(2 * time.Second),
		},
		{
			ID: deletedOnlyID, Role: "agent", Kind: "text", Text: "Removed", CreatedAt: t0.Add(3 * time.Second),
			AuthorName: "agent", Timestamp: t0.Add(3 * time.Second),
		},
	}
	for i := range want {
		got := resp.Entries[i]
		if !got.CreatedAt.Equal(want[i].CreatedAt) || !got.Timestamp.Equal(want[i].Timestamp) {
			t.Errorf("entry %d: createdAt=%v timestamp=%v, want %v", i, got.CreatedAt, got.Timestamp, want[i].CreatedAt)
		}
		got.CreatedAt, got.Timestamp = want[i].CreatedAt, want[i].Timestamp
		if !reflect.DeepEqual(got, want[i]) {
			t.Errorf("entry %d:\n got %+v\nwant %+v", i, got, want[i])
		}
	}

	// Optional keys are omitted, not sent empty.
	var rawResp struct {
		Entries []map[string]json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(raw, &rawResp); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	for i, key := range map[int]string{1: "audio", 3: "tasks"} {
		if _, ok := rawResp.Entries[i][key]; ok {
			t.Errorf("entry %d must omit %q: %s", i, key, raw)
		}
	}
	if _, ok := rawResp.Entries[0]["tasks"]; ok {
		t.Errorf("user entry must omit tasks: %s", raw)
	}
}

// TestChatHistoryWithoutTaskServiceOmitsTaskCards verifies that the history is
// still served when the server has no task service; task cards are left out.
func TestChatHistoryWithoutTaskServiceOmitsTaskCards(t *testing.T) {
	h := newChatEventHarness(t, &fakeChatService{reply: model.ChatReply{Text: "ok"}})
	h.addHistory("client:"+h.device.ID, model.HistoryEntry{
		AuthorName: "agent", Text: "Done", Timestamp: time.Now(),
		Role: model.HistoryRoleAgent, Kind: model.HistoryKindText, TaskIDs: []string{"task-1"},
	})

	resp := h.getChatHistory("/v1/chat/history")
	if len(resp.Entries) != 1 || resp.Entries[0].Role != "agent" || resp.Entries[0].Tasks != nil {
		t.Fatalf("entries = %+v, want one agent entry without tasks", resp.Entries)
	}
}

// TestChatHistoryRowsWithoutRoleGetRoleFromAuthor verifies that a client row
// without a stored role is classified by author_name, like the migration 017
// backfill.
func TestChatHistoryRowsWithoutRoleGetRoleFromAuthor(t *testing.T) {
	h := newChatHarness(t)
	source := "client:" + h.device.ID
	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	h.addHistory(source, model.HistoryEntry{AuthorName: h.device.ID, Text: "question", Timestamp: t0})
	h.addHistory(source, model.HistoryEntry{AuthorName: "agent", Text: "answer", Timestamp: t0.Add(time.Second)})

	resp := h.getChatHistory("/v1/chat/history")
	got := make([]string, 0, len(resp.Entries))
	for _, e := range resp.Entries {
		got = append(got, e.Text+":"+e.Role+":"+e.Kind)
	}
	if want := []string{"question:user:text", "answer:agent:text"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
}

// ---- chat_message_created SSE event tests ----

// fakeBroker is a minimal mock for model.Broker that records Notify calls.
type fakeBroker struct {
	mu     sync.Mutex
	events []model.Event
}

func (b *fakeBroker) Subscribe(_ string) (<-chan model.Event, func()) {
	ch := make(chan model.Event, 1)
	return ch, func() {}
}

func (b *fakeBroker) IsActive(_ string) bool { return false }

func (b *fakeBroker) Notify(ev model.Event) {
	b.mu.Lock()
	b.events = append(b.events, ev)
	b.mu.Unlock()
}

func (b *fakeBroker) notified() []model.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]model.Event, len(b.events))
	copy(out, b.events)
	return out
}

// chatEventHarness extends chatTestHarness with a real EventStore and mock Broker.
type chatEventHarness struct {
	*chatTestHarness
	eventStore *events.SQLiteEventStore
	broker     *fakeBroker
}

func newChatEventHarness(t *testing.T, chat *fakeChatService) *chatEventHarness {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.OpenDB(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	history := storage.NewSQLiteHistory(db, storage.SQLiteHistoryOptions{})
	eventStore := events.NewSQLiteEventStore(db)
	broker := &fakeBroker{}

	token := "chat-event-token"
	device := createTestDevice(t, db, "test-event-device", token)

	cfg := api.Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:      db,
		Devices: devices.NewSQLiteDeviceStore(db),
		Chat:    chat,
		History: history,
		Events:  eventStore,
		Broker:  broker,
		Owner:   api.OwnerInfo{UserName: "Oliver"},
	}
	srv := api.New(cfg)

	base := &chatTestHarness{
		t:       t,
		db:      db,
		server:  srv,
		token:   token,
		device:  device,
		chat:    chat,
		history: history,
		cfg:     cfg,
	}
	return &chatEventHarness{
		chatTestHarness: base,
		eventStore:      eventStore,
		broker:          broker,
	}
}

func TestPostChatEmitsChatMessageCreatedEvents(t *testing.T) {
	chat := &fakeChatService{reply: model.ChatReply{Text: "ответ агента"}}
	h := newChatEventHarness(t, chat)

	rec := h.do(http.MethodPost, "/v1/chat", map[string]string{"message": "привет"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	evs, err := h.eventStore.SinceSeq(context.Background(), 0, 10)
	if err != nil {
		t.Fatalf("SinceSeq: %v", err)
	}

	chatEvs := make([]model.Event, 0, 2)
	for _, ev := range evs {
		if ev.Kind == model.EventChatMessageCreated {
			chatEvs = append(chatEvs, ev)
		}
	}
	if len(chatEvs) != 2 {
		t.Fatalf("want 2 chat_message_created events, got %d", len(chatEvs))
	}

	var userPayload struct {
		MessageID string `json:"messageId"`
		Source    struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		} `json:"source"`
		Role       string    `json:"role"`
		Text       string    `json:"text"`
		CreatedAt  time.Time `json:"createdAt"`
		AudioJobID string    `json:"audioJobId,omitempty"`
	}
	if err := json.Unmarshal(chatEvs[0].Payload, &userPayload); err != nil {
		t.Fatalf("unmarshal user event: %v", err)
	}
	if userPayload.Role != "user" {
		t.Fatalf("first event role=%q, want user", userPayload.Role)
	}
	if userPayload.Text != "привет" {
		t.Fatalf("first event text=%q", userPayload.Text)
	}
	if userPayload.Source.Kind != "client" {
		t.Fatalf("source.kind=%q, want client", userPayload.Source.Kind)
	}
	if userPayload.Source.ID != h.device.ID {
		t.Fatalf("source.id=%q, want %q", userPayload.Source.ID, h.device.ID)
	}
	if userPayload.MessageID == "" {
		t.Fatal("user event messageId is empty")
	}
	if userPayload.AudioJobID != "" {
		t.Fatalf("user text event must not have audioJobId, got %q", userPayload.AudioJobID)
	}

	var agentPayload struct {
		Role string `json:"role"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(chatEvs[1].Payload, &agentPayload); err != nil {
		t.Fatalf("unmarshal agent event: %v", err)
	}
	if agentPayload.Role != "agent" {
		t.Fatalf("second event role=%q, want agent", agentPayload.Role)
	}
	if agentPayload.Text != "ответ агента" {
		t.Fatalf("second event text=%q", agentPayload.Text)
	}
}

// TestPostChatEventPayloadCarriesKindAndTaskIDs verifies the chat_message_created
// payload keys added for live rendering: kind on both messages and taskIds on
// the agent reply; audioDurationMs is absent for text messages.
func TestPostChatEventPayloadCarriesKindAndTaskIDs(t *testing.T) {
	chat := &fakeChatService{reply: model.ChatReply{Text: "created", TasksTouched: []string{"t-1", "t-2"}}}
	h := newChatEventHarness(t, chat)

	rec := h.do(http.MethodPost, "/v1/chat", map[string]string{"message": "create two tasks"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	evs, err := h.eventStore.SinceSeq(context.Background(), 0, 10)
	if err != nil {
		t.Fatalf("SinceSeq: %v", err)
	}
	var payloads []map[string]json.RawMessage
	for _, ev := range evs {
		if ev.Kind != model.EventChatMessageCreated {
			continue
		}
		var p map[string]json.RawMessage
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		payloads = append(payloads, p)
	}
	if len(payloads) != 2 {
		t.Fatalf("want 2 chat_message_created events, got %d", len(payloads))
	}

	tests := []struct {
		name        string
		payload     map[string]json.RawMessage
		wantKind    string
		wantTaskIDs string
	}{
		{name: "user", payload: payloads[0], wantKind: `"text"`},
		{name: "agent", payload: payloads[1], wantKind: `"text"`, wantTaskIDs: `["t-1","t-2"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(tt.payload["kind"]); got != tt.wantKind {
				t.Errorf("kind=%s, want %s", got, tt.wantKind)
			}
			if got := string(tt.payload["taskIds"]); got != tt.wantTaskIDs {
				t.Errorf("taskIds=%s, want %q", got, tt.wantTaskIDs)
			}
			if _, ok := tt.payload["audioDurationMs"]; ok {
				t.Errorf("text message must not carry audioDurationMs: %v", tt.payload)
			}
		})
	}

	// The live messageId matches the history entry id.
	var userID string
	if err := json.Unmarshal(payloads[0]["messageId"], &userID); err != nil {
		t.Fatalf("unmarshal messageId: %v", err)
	}
	resp := h.getChatHistory("/v1/chat/history")
	if len(resp.Entries) != 2 || resp.Entries[0].ID != userID {
		t.Fatalf("history entries = %+v, want the user entry with id %q first", resp.Entries, userID)
	}
}

func TestPostChatBrokerNotifiedTwice(t *testing.T) {
	chat := &fakeChatService{reply: model.ChatReply{Text: "пинг"}}
	h := newChatEventHarness(t, chat)

	rec := h.do(http.MethodPost, "/v1/chat", map[string]string{"message": "понг"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	notified := h.broker.notified()
	count := 0
	for _, ev := range notified {
		if ev.Kind == model.EventChatMessageCreated {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("broker notified %d chat_message_created events, want 2", count)
	}
}

func TestPostChatHTTPResponseUnchangedWithEvents(t *testing.T) {
	chat := &fakeChatService{reply: model.ChatReply{
		Text:            "ответ",
		TasksTouched:    []string{"t-1"},
		ProjectsTouched: []string{"p-1"},
	}}
	h := newChatEventHarness(t, chat)

	rec := h.do(http.MethodPost, "/v1/chat", map[string]string{"message": "запрос"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Reply           string   `json:"reply"`
		TasksTouched    []string `json:"tasksTouched"`
		ProjectsTouched []string `json:"projectsTouched"`
	}
	decodeJSONResp(t, rec.Body, &resp)
	if resp.Reply != "ответ" {
		t.Fatalf("reply=%q", resp.Reply)
	}
	if len(resp.TasksTouched) != 1 || resp.TasksTouched[0] != "t-1" {
		t.Fatalf("tasksTouched=%v", resp.TasksTouched)
	}
	if len(resp.ProjectsTouched) != 1 || resp.ProjectsTouched[0] != "p-1" {
		t.Fatalf("projectsTouched=%v", resp.ProjectsTouched)
	}
}

func TestPostChatAgentErrorPreservesUserEvent(t *testing.T) {
	chat := &fakeChatService{err: errors.New("агент сломан")}
	h := newChatEventHarness(t, chat)

	rec := h.do(http.MethodPost, "/v1/chat", map[string]string{"message": "привет"}, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500", rec.Code)
	}

	evs, err := h.eventStore.SinceSeq(context.Background(), 0, 10)
	if err != nil {
		t.Fatalf("SinceSeq: %v", err)
	}

	chatEvs := make([]model.Event, 0)
	for _, ev := range evs {
		if ev.Kind == model.EventChatMessageCreated {
			chatEvs = append(chatEvs, ev)
		}
	}
	if len(chatEvs) != 1 {
		t.Fatalf("want 1 chat_message_created (user only) on agent error, got %d", len(chatEvs))
	}
	var payload struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(chatEvs[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Role != "user" {
		t.Fatalf("event role=%q, want user", payload.Role)
	}

	// Broker must have been notified exactly once (user event only).
	notified := h.broker.notified()
	count := 0
	for _, ev := range notified {
		if ev.Kind == model.EventChatMessageCreated {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("broker notified %d times, want 1 (user only)", count)
	}
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
