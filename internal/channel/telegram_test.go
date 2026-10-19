package channel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

// mockBot implements botAPI for testing without a real Telegram bot.
type mockBot struct {
	mu            sync.Mutex
	history       []tgbotapi.Update
	sendCalled    []tgbotapi.Chattable
	sendErr       error
	sendResult    tgbotapi.Message
	makeReqCalls  []string
	makeReqParams []tgbotapi.Params
	makeReqErr    error
	updateBatches []json.RawMessage
	cancelFn      func()
	// makeReqResponder, if non-nil, is called for every MakeRequest and overrides
	// the default logic. The call is tracked in makeReqCalls before the responder fires.
	makeReqResponder func(endpoint string, params tgbotapi.Params) (*tgbotapi.APIResponse, error)
}

func newMockBot(_ int) *mockBot {
	return &mockBot{}
}

// setUpdates serialises a batch of updates to JSON and enqueues them as getUpdates responses.
func (m *mockBot) setUpdates(updates []rawUpdate) {
	data, _ := json.Marshal(updates)
	m.mu.Lock()
	m.updateBatches = append(m.updateBatches, data)
	m.mu.Unlock()
}

func (m *mockBot) GetUpdates(cfg tgbotapi.UpdateConfig) ([]tgbotapi.Update, error) {
	_ = cfg
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.history, nil
}

func (m *mockBot) Send(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sendCalled = append(m.sendCalled, c)
	return m.sendResult, m.sendErr
}

func (m *mockBot) MakeRequest(endpoint string, params tgbotapi.Params) (*tgbotapi.APIResponse, error) {
	m.mu.Lock()
	m.makeReqCalls = append(m.makeReqCalls, endpoint)
	m.makeReqParams = append(m.makeReqParams, params)

	if m.makeReqResponder != nil {
		responder := m.makeReqResponder
		m.mu.Unlock()
		return responder(endpoint, params)
	}

	if m.makeReqErr != nil {
		m.mu.Unlock()
		return nil, m.makeReqErr
	}

	if endpoint == "getUpdates" {
		var batch json.RawMessage
		var callCancel func()
		if len(m.updateBatches) > 0 {
			batch = m.updateBatches[0]
			m.updateBatches = m.updateBatches[1:]
		}
		if len(m.updateBatches) == 0 && m.cancelFn != nil {
			callCancel = m.cancelFn
		}
		m.mu.Unlock()
		if callCancel != nil {
			callCancel()
		}
		if batch == nil {
			batch = json.RawMessage("[]")
		}
		return &tgbotapi.APIResponse{Ok: true, Result: batch}, nil
	}

	m.mu.Unlock()
	return &tgbotapi.APIResponse{Ok: true}, nil
}

// mockStateStore implements model.StateStore in memory for testing.
type mockStateStore struct {
	mu              sync.Mutex
	cursors         map[string]*model.Cursor
	saveCursorCalls int
}

func newMockStateStore() *mockStateStore {
	return &mockStateStore{cursors: make(map[string]*model.Cursor)}
}

func (s *mockStateStore) GetCursor(_ context.Context, channelID string) (*model.Cursor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursors[channelID], nil
}

func (s *mockStateStore) SaveCursor(_ context.Context, channelID string, cursor model.Cursor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := cursor
	s.cursors[channelID] = &c
	s.saveCursorCalls++
	return nil
}

// Helper functions for creating test data.

func makeUser(id int64, username, first, last string) *tgbotapi.User {
	return &tgbotapi.User{
		ID:        id,
		UserName:  username,
		FirstName: first,
		LastName:  last,
	}
}

func makeChat(id int64, title string) *tgbotapi.Chat {
	return &tgbotapi.Chat{
		ID:    id,
		Title: title,
	}
}

func makePrivateChat(id int64) *tgbotapi.Chat {
	return &tgbotapi.Chat{
		ID:   id,
		Type: "private",
	}
}

func makeMessage(msgID int, chat *tgbotapi.Chat, from *tgbotapi.User, text string, date int) *tgbotapi.Message {
	return &tgbotapi.Message{
		MessageID: msgID,
		Chat:      chat,
		From:      from,
		Text:      text,
		Date:      date,
	}
}

func makeUpdate(updateID int, msg *tgbotapi.Message) tgbotapi.Update {
	return tgbotapi.Update{
		UpdateID: updateID,
		Message:  msg,
	}
}

func makeRawUpdate(id int, msg *tgbotapi.Message) rawUpdate {
	return rawUpdate{
		UpdateID: id,
		Message:  msg,
	}
}

// TestConvertUpdate_SimpleMessage verifies conversion of a plain message.
func TestConvertUpdate_SimpleMessage(t *testing.T) {
	const groupID int64 = -1001234567890
	chat := makeChat(groupID, "Рабочий чат")
	user := makeUser(100, "gregoryn", "Григорий", "Николаев")
	msg := makeMessage(42, chat, user, "сделаю завтра", 1700000000)
	update := makeUpdate(1, msg)

	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(update)

	if !ok {
		t.Fatal("want ok=true, got false")
	}
	if got.ID != "42" {
		t.Errorf("ID: want %q, got %q", "42", got.ID)
	}
	if got.Source.Kind != "telegram" {
		t.Errorf("Source.Kind: want %q, got %q", "telegram", got.Source.Kind)
	}
	if got.Source.ID != "-1001234567890" {
		t.Errorf("Source.ID: want %q, got %q", "-1001234567890", got.Source.ID)
	}
	if got.Source.Name != "Рабочий чат" {
		t.Errorf("Source.Name: want %q, got %q", "Рабочий чат", got.Source.Name)
	}
	if got.Source.AccountID != "" {
		t.Errorf("Source.AccountID: want %q (empty, no ID in config), got %q", "", got.Source.AccountID)
	}
	if got.Author != "100" {
		t.Errorf("Author: want %q, got %q", "100", got.Author)
	}
	if got.AuthorName != "gregoryn" {
		t.Errorf("AuthorName: want %q, got %q", "gregoryn", got.AuthorName)
	}
	if got.Text != "сделаю завтра" {
		t.Errorf("Text: want %q, got %q", "сделаю завтра", got.Text)
	}
	if got.Timestamp != time.Unix(1700000000, 0) {
		t.Errorf("Timestamp: want %v, got %v", time.Unix(1700000000, 0), got.Timestamp)
	}
	if got.ReplyTo != nil {
		t.Error("ReplyTo: want nil, got non-nil")
	}
}

// TestConvertUpdate_AccountIDEmpty verifies that Source.AccountID is empty when cfg.ID is empty.
func TestConvertUpdate_AccountIDEmpty(t *testing.T) {
	const groupID int64 = -100
	chat := makeChat(groupID, "Чат")

	origAuthor := makeUser(200, "boss", "", "")
	origMsg := makeMessage(10, chat, origAuthor, "нужно сделать", 1699000000)

	replyAuthor := makeUser(100, "gregoryn", "", "")
	replyMsg := makeMessage(11, chat, replyAuthor, "сделаю", 1699001000)
	replyMsg.ReplyToMessage = origMsg

	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(5, replyMsg))

	if !ok {
		t.Fatal("want ok=true")
	}
	if got.Source.AccountID != "" {
		t.Errorf("Source.AccountID: want %q, got %q", "", got.Source.AccountID)
	}
	if got.ReplyTo == nil {
		t.Fatal("ReplyTo: want non-nil, got nil")
	}
	if got.ReplyTo.Source.AccountID != "" {
		t.Errorf("ReplyTo.Source.AccountID: want %q, got %q", "", got.ReplyTo.Source.AccountID)
	}
}

// TestConvertUpdate_AuthorNameFallback verifies that the name is taken from FirstName+LastName
// when username is not set.
func TestConvertUpdate_AuthorNameFallback(t *testing.T) {
	const groupID int64 = -100
	chat := makeChat(groupID, "Чат")
	user := makeUser(1, "", "Иван", "Петров")
	msg := makeMessage(1, chat, user, "ok", 0)

	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, msg))

	if !ok {
		t.Fatal("want ok=true")
	}
	if got.AuthorName != "Иван Петров" {
		t.Errorf("AuthorName: want %q, got %q", "Иван Петров", got.AuthorName)
	}
}

// TestConvertUpdate_ReplyTo verifies that the reply-to message is correctly converted.
func TestConvertUpdate_ReplyTo(t *testing.T) {
	const groupID int64 = -100
	chat := makeChat(groupID, "Чат")

	origAuthor := makeUser(200, "boss", "", "")
	origMsg := makeMessage(10, chat, origAuthor, "нужно сделать отчёт", 1699000000)

	replyAuthor := makeUser(100, "gregoryn", "", "")
	replyMsg := makeMessage(11, chat, replyAuthor, "сделаю к пятнице", 1699001000)
	replyMsg.ReplyToMessage = origMsg

	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(5, replyMsg))

	if !ok {
		t.Fatal("want ok=true")
	}
	if got.ReplyTo == nil {
		t.Fatal("ReplyTo: want non-nil, got nil")
	}
	if got.ReplyTo.ID != "10" {
		t.Errorf("ReplyTo.ID: want %q, got %q", "10", got.ReplyTo.ID)
	}
	if got.ReplyTo.Author != "200" {
		t.Errorf("ReplyTo.Author: want %q, got %q", "200", got.ReplyTo.Author)
	}
	if got.ReplyTo.Text != "нужно сделать отчёт" {
		t.Errorf("ReplyTo.Text: want %q, got %q", "нужно сделать отчёт", got.ReplyTo.Text)
	}
}

// TestConvertUpdate_UnknownType verifies that unknown update types
// (including reactions in tgbotapi v5+) are handled without errors.
// Note: MessageReactionUpdated requires tgbotapi v6+ (Telegram Bot API 7.0).
// In v5 such updates fall into the default branch and return ok=false.
func TestConvertUpdate_UnknownType(t *testing.T) {
	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{}, newMockStateStore(), nil, nil, HistoryConfig{})

	// Update without Message and EditedMessage — simulates a reaction or other unknown type.
	update := tgbotapi.Update{UpdateID: 42}

	_, ok := w.convertUpdate(update)

	if ok {
		t.Error("want ok=false for unknown update type")
	}
}

// TestWatch_ProcessesMessages verifies that Watch calls the handler for each message
// from the monitored group.
func TestWatch_ProcessesMessages(t *testing.T) {
	const groupID int64 = -100
	bot := newMockBot(0)
	state := newMockStateStore()

	chat := makeChat(groupID, "Чат")
	user := makeUser(1, "u", "", "")

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel
	bot.setUpdates([]rawUpdate{
		makeRawUpdate(1, makeMessage(1, chat, user, "привет", 1000)),
		makeRawUpdate(2, makeMessage(2, chat, user, "пока", 2000)),
	})

	w := newTelegramChannel(bot, TelegramChannelConfig{
		OnJoin: "monitor",
	}, state, nil, nil, HistoryConfig{})

	var received []model.Message
	_ = w.Watch(ctx, func(_ context.Context, msg model.Message) error {
		received = append(received, msg)
		return nil
	})

	if len(received) != 2 {
		t.Fatalf("want 2 messages, got %d", len(received))
	}
	if received[0].Text != "привет" {
		t.Errorf("first message: want %q, got %q", "привет", received[0].Text)
	}
	if received[1].Text != "пока" {
		t.Errorf("second message: want %q, got %q", "пока", received[1].Text)
	}
}

// TestWatch_MonitorMode verifies that in "monitor" mode Watch starts from cursor+1.
func TestWatch_MonitorMode(t *testing.T) {
	bot := newMockBot(0)
	state := newMockStateStore()
	_ = state.SaveCursor(context.Background(), telegramStateKey, model.Cursor{
		MessageID: "10",
		UpdatedAt: time.Now(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel

	w := newTelegramChannel(bot, TelegramChannelConfig{
		OnJoin: "monitor",
	}, state, nil, nil, HistoryConfig{})

	_ = w.Watch(ctx, func(_ context.Context, _ model.Message) error { return nil })

	if len(bot.makeReqParams) == 0 {
		t.Fatal("MakeRequest was not called")
	}
	if got := bot.makeReqParams[0]["offset"]; got != "11" {
		t.Errorf("offset: want %q, got %q", "11", got)
	}
}

// TestWatch_MonitorModeWithID verifies that in "monitor" mode Watch reads the cursor
// from the compound key "telegram/<id>" when cfg.ID is set.
func TestWatch_MonitorModeWithID(t *testing.T) {
	bot := newMockBot(0)
	state := newMockStateStore()
	_ = state.SaveCursor(context.Background(), "telegram/work", model.Cursor{
		MessageID: "20",
		UpdatedAt: time.Now(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel

	w := newTelegramChannel(bot, TelegramChannelConfig{
		ID:     "work",
		OnJoin: "monitor",
	}, state, nil, nil, HistoryConfig{})

	_ = w.Watch(ctx, func(_ context.Context, _ model.Message) error { return nil })

	if len(bot.makeReqParams) == 0 {
		t.Fatal("MakeRequest was not called")
	}
	if got := bot.makeReqParams[0]["offset"]; got != "21" {
		t.Errorf("offset: want %q (cursor+1), got %q", "21", got)
	}
}

// TestWatch_BackfillMode verifies that in "backfill" mode Watch starts from offset=0
// (even when a saved cursor exists).
func TestWatch_BackfillMode(t *testing.T) {
	bot := newMockBot(0)
	state := newMockStateStore()
	_ = state.SaveCursor(context.Background(), telegramStateKey, model.Cursor{
		MessageID: "99",
		UpdatedAt: time.Now(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel

	w := newTelegramChannel(bot, TelegramChannelConfig{
		OnJoin: "backfill",
	}, state, nil, nil, HistoryConfig{})

	_ = w.Watch(ctx, func(_ context.Context, _ model.Message) error { return nil })

	if len(bot.makeReqParams) == 0 {
		t.Fatal("MakeRequest was not called")
	}
	if got := bot.makeReqParams[0]["offset"]; got != "0" {
		t.Errorf("offset: want %q, got %q", "0", got)
	}
}

// TestWatch_CursorUpdated verifies that the cursor is updated after each update.
func TestWatch_CursorUpdated(t *testing.T) {
	const groupID int64 = -100
	bot := newMockBot(0)
	state := newMockStateStore()

	chat := makeChat(groupID, "Чат")
	user := makeUser(1, "u", "", "")

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel
	bot.setUpdates([]rawUpdate{makeRawUpdate(7, makeMessage(1, chat, user, "тест", 0))})

	w := newTelegramChannel(bot, TelegramChannelConfig{
		OnJoin: "monitor",
	}, state, nil, nil, HistoryConfig{})

	_ = w.Watch(ctx, func(_ context.Context, _ model.Message) error { return nil })

	cursor, _ := state.GetCursor(context.Background(), telegramStateKey)
	if cursor == nil {
		t.Fatal("cursor was not saved")
	}
	if cursor.MessageID != "7" {
		t.Errorf("MessageID курсора: want %q, got %q", "7", cursor.MessageID)
	}
}

// TestWatch_ContextCancellation verifies that Watch terminates cleanly
// when the context is cancelled.
func TestWatch_ContextCancellation(t *testing.T) {
	bot := newMockBot(0)
	state := newMockStateStore()

	w := newTelegramChannel(bot, TelegramChannelConfig{
		OnJoin: "monitor",
	}, state, nil, nil, HistoryConfig{})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- w.Watch(ctx, func(_ context.Context, _ model.Message) error { return nil })
	}()

	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("want context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Watch did not finish after context cancellation timeout")
	}
}

// TestFetchHistory_BackfillMode verifies that FetchHistory returns messages
// from the monitored group in "backfill" mode.
func TestFetchHistory_BackfillMode(t *testing.T) {
	const groupID int64 = -100
	bot := newMockBot(0)
	chat := makeChat(groupID, "Чат")
	user := makeUser(1, "u", "", "")

	bot.history = []tgbotapi.Update{
		makeUpdate(1, makeMessage(1, chat, user, "первое", 1000)),
		makeUpdate(2, makeMessage(2, chat, user, "второе", 2000)),
	}

	w := newTelegramChannel(bot, TelegramChannelConfig{
		OnJoin: "backfill",
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	source := model.Source{Kind: "telegram", ID: "-100"}
	msgs, err := w.FetchHistory(context.Background(), source, 10)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages, got %d", len(msgs))
	}
}

// TestWatch_StateKeyWithID verifies that when ID is set the cursor is stored
// under the key "telegram/<id>", not under "telegram".
func TestWatch_StateKeyWithID(t *testing.T) {
	const groupID int64 = -100
	bot := newMockBot(0)
	state := newMockStateStore()

	chat := makeChat(groupID, "Чат")
	user := makeUser(1, "u", "", "")

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel
	bot.setUpdates([]rawUpdate{makeRawUpdate(42, makeMessage(1, chat, user, "тест", 0))})

	w := newTelegramChannel(bot, TelegramChannelConfig{
		ID:     "work",
		OnJoin: "monitor",
	}, state, nil, nil, HistoryConfig{})

	_ = w.Watch(ctx, func(_ context.Context, _ model.Message) error { return nil })

	// Cursor must be stored under the key "telegram/work", not under "telegram".
	cursorDefault, _ := state.GetCursor(context.Background(), telegramStateKey)
	cursorWork, _ := state.GetCursor(context.Background(), "telegram/work")

	if cursorDefault != nil {
		t.Error("cursor must not be under key 'telegram' when ID is present")
	}
	if cursorWork == nil {
		t.Error("cursor must be under key 'telegram/work'")
	}
	if cursorWork != nil && cursorWork.MessageID != "42" {
		t.Errorf("MessageID курсора: want %q, got %q", "42", cursorWork.MessageID)
	}
}

// TestFetchHistory_MonitorMode verifies that FetchHistory returns nil
// in "monitor" mode.
func TestFetchHistory_MonitorMode(t *testing.T) {
	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{
		OnJoin: "monitor",
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	source := model.Source{Kind: "telegram", ID: "-100"}
	msgs, err := w.FetchHistory(context.Background(), source, 10)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgs != nil {
		t.Errorf("want nil, got %v", msgs)
	}
}

// TestConvertMessage_DM_OwnerPrivateChat verifies that a private chat from the owner
// is recognised as DM: Source.ID == "dm", Source.Name == "DM".
func TestConvertMessage_DM_OwnerPrivateChat(t *testing.T) {
	owner := makeUser(12345, "gregoryn", "Григорий", "")
	chat := makePrivateChat(12345)
	msg := makeMessage(1, chat, owner, "опубликую бекенд сегодня", 1700000000)

	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{
		OwnerIDs: []string{"12345"},
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, msg))

	if !ok {
		t.Fatal("want ok=true for owner DM")
	}
	if got.Source.ID != "dm" {
		t.Errorf("Source.ID: want %q, got %q", "dm", got.Source.ID)
	}
	if got.Source.Name != "DM" {
		t.Errorf("Source.Name: want %q, got %q", "DM", got.Source.Name)
	}
	if got.Source.Kind != "telegram" {
		t.Errorf("Source.Kind: want %q, got %q", "telegram", got.Source.Kind)
	}
	if got.Source.AccountID != "" {
		t.Errorf("Source.AccountID: want %q, got %q", "", got.Source.AccountID)
	}
	if got.Text != "опубликую бекенд сегодня" {
		t.Errorf("Text: want %q, got %q", "опубликую бекенд сегодня", got.Text)
	}
}

// TestConvertMessage_DM_CaptionFallback verifies that when m.Text is empty
// (e.g. photo/document with caption), the caption is used as the message body.
func TestConvertMessage_DM_CaptionFallback(t *testing.T) {
	owner := makeUser(12345, "gregoryn", "Григорий", "")
	chat := makePrivateChat(12345)
	msg := makeMessage(1, chat, owner, "", 1700000000)
	msg.Caption = "сделаю до вечера"

	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{
		OwnerIDs: []string{"12345"},
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, msg))

	if !ok {
		t.Fatal("want ok=true for owner DM with caption")
	}
	if got.Text != "сделаю до вечера" {
		t.Errorf("Text: want caption fallback %q, got %q", "сделаю до вечера", got.Text)
	}
}

// TestConvertMessage_Group_CaptionFallback verifies caption fallback for group messages.
func TestConvertMessage_Group_CaptionFallback(t *testing.T) {
	const groupID int64 = -1001234567890
	chat := makeChat(groupID, "Рабочий чат")
	chat.Type = "supergroup"
	user := makeUser(100, "gregoryn", "Григорий", "")
	msg := makeMessage(42, chat, user, "", 1700000000)
	msg.Caption = "пришлю отчёт завтра"

	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, msg))

	if !ok {
		t.Fatal("want ok=true for group message with caption")
	}
	if got.Text != "пришлю отчёт завтра" {
		t.Errorf("Text: want caption fallback %q, got %q", "пришлю отчёт завтра", got.Text)
	}
}

// TestConvertMessage_Group_ReplyTo_CaptionFallback verifies that when a group
// message replies to a media message (photo/video/document) with caption, the
// caption is exposed as ReplyTo.Text. Without this, the AI classifier and
// extractor lose the original promise context (the goal/target stays in
// the caption while the user's reply is just an agreement like "да").
func TestConvertMessage_Group_ReplyTo_CaptionFallback(t *testing.T) {
	const groupID int64 = -1001234567890
	chat := makeChat(groupID, "Рабочий чат")
	chat.Type = "supergroup"

	colleague := makeUser(200, "colleague", "Коллега", "")
	origMsg := makeMessage(10, chat, colleague, "", 1699000000)
	origMsg.Caption = "Сделай отчёт до завтра"

	owner := makeUser(100, "gregoryn", "Григорий", "")
	replyMsg := makeMessage(11, chat, owner, "да", 1699001000)
	replyMsg.ReplyToMessage = origMsg

	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, replyMsg))

	if !ok {
		t.Fatal("want ok=true for group reply with caption parent")
	}
	if got.ReplyTo == nil {
		t.Fatal("ReplyTo: want non-nil, got nil")
	}
	if got.ReplyTo.Text != "Сделай отчёт до завтра" {
		t.Errorf("ReplyTo.Text: want caption fallback %q, got %q", "Сделай отчёт до завтра", got.ReplyTo.Text)
	}
}

// TestConvertMessage_DM_ReplyTo verifies that a DM message replying to another message
// correctly populates ReplyTo.
func TestConvertMessage_DM_ReplyTo(t *testing.T) {
	owner := makeUser(12345, "gregoryn", "Григорий", "")
	chat := makePrivateChat(12345)

	origMsg := makeMessage(10, chat, owner, "напомни про релиз", 1699000000)
	replyMsg := makeMessage(11, chat, owner, "да, сделаю сегодня вечером", 1699001000)
	replyMsg.ReplyToMessage = origMsg

	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{
		ID:       "work",
		OwnerIDs: []string{"12345"},
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, replyMsg))

	if !ok {
		t.Fatal("want ok=true for DM with reply")
	}
	if got.Source.ID != "dm" {
		t.Errorf("Source.ID: want %q, got %q", "dm", got.Source.ID)
	}
	if got.ReplyTo == nil {
		t.Fatal("ReplyTo: want non-nil, got nil")
	}
	if got.ReplyTo.ID != "10" {
		t.Errorf("ReplyTo.ID: want %q, got %q", "10", got.ReplyTo.ID)
	}
	if got.ReplyTo.Text != "напомни про релиз" {
		t.Errorf("ReplyTo.Text: want %q, got %q", "напомни про релиз", got.ReplyTo.Text)
	}
}

// TestConvertMessage_DM_UnknownUserPrivateChat verifies that a private chat from an unknown
// user is filtered out.
func TestConvertMessage_DM_UnknownUserPrivateChat(t *testing.T) {
	stranger := makeUser(99999, "stranger", "", "")
	chat := makePrivateChat(99999)
	msg := makeMessage(1, chat, stranger, "привет", 1700000000)

	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{
		ID:       "work",
		OwnerIDs: []string{"12345"},
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	_, ok := w.convertUpdate(makeUpdate(1, msg))

	if ok {
		t.Error("want ok=false for private chat from unknown user")
	}
}

// TestConvertMessage_DM_GroupChatUnchanged verifies that group messages
// are processed unchanged when OwnerIDs is set in the config.
func TestConvertMessage_DM_GroupChatUnchanged(t *testing.T) {
	const groupID int64 = -100
	user := makeUser(12345, "gregoryn", "Григорий", "")
	chat := makeChat(groupID, "Рабочий чат")
	msg := makeMessage(42, chat, user, "сделаю завтра", 1700000000)

	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{
		ID:       "work",
		OwnerIDs: []string{"12345"},
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, msg))

	if !ok {
		t.Fatal("want ok=true for group message")
	}
	if got.Source.ID != "-100" {
		t.Errorf("Source.ID: want %q, got %q", "-100", got.Source.ID)
	}
	if got.Source.Name != "Рабочий чат" {
		t.Errorf("Source.Name: want %q, got %q", "Рабочий чат", got.Source.Name)
	}
}

// TestFetchHistory_FiltersBySource verifies that FetchHistory returns only
// messages from the requested source.
func TestFetchHistory_FiltersBySource(t *testing.T) {
	const targetGroup int64 = -100
	const otherGroup int64 = -999
	bot := newMockBot(0)

	targetChat := makeChat(targetGroup, "Нужный")
	otherChat := makeChat(otherGroup, "Другой")
	user := makeUser(1, "u", "", "")

	bot.history = []tgbotapi.Update{
		makeUpdate(1, makeMessage(1, targetChat, user, "целевое", 1000)),
		makeUpdate(2, makeMessage(2, otherChat, user, "чужое", 2000)),
	}

	w := newTelegramChannel(bot, TelegramChannelConfig{
		OnJoin: "backfill",
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	source := model.Source{Kind: "telegram", ID: "-100"}
	msgs, err := w.FetchHistory(context.Background(), source, 10)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want 1 message, got %d", len(msgs))
	}
	if msgs[0].Text != "целевое" {
		t.Errorf("want %q, got %q", "целевое", msgs[0].Text)
	}
}

// TestTelegramChannel_ID verifies the ID() method.
func TestTelegramChannel_ID(t *testing.T) {
	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{
		ID: "work",
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	if w.ID() != "work" {
		t.Errorf("ID: want %q, got %q", "work", w.ID())
	}
}

// TestConvertMessage_GroupKind verifies that a group message gets Kind=Group.
func TestConvertMessage_GroupKind(t *testing.T) {
	const groupID int64 = -100
	chat := makeChat(groupID, "Рабочий чат")
	user := makeUser(1, "u", "", "")
	msg := makeMessage(42, chat, user, "сделаю", 0)

	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, msg))

	if !ok {
		t.Fatal("want ok=true")
	}
	if got.Kind != model.MessageKindGroup {
		t.Errorf("Kind: want %q, got %q", model.MessageKindGroup, got.Kind)
	}
}

// TestConvertMessage_DMKind verifies that a DM message from the owner gets Kind=DM.
func TestConvertMessage_DMKind(t *testing.T) {
	owner := makeUser(12345, "gregoryn", "Григорий", "")
	chat := makePrivateChat(12345)
	msg := makeMessage(1, chat, owner, "опубликую сегодня", 0)

	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{
		OwnerIDs: []string{"12345"},
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, msg))

	if !ok {
		t.Fatal("want ok=true for owner DM")
	}
	if got.Kind != model.MessageKindDM {
		t.Errorf("Kind: want %q, got %q", model.MessageKindDM, got.Kind)
	}
}

// TestConvertMessage_ReactFn_CallsMakeRequest verifies that ReactFn calls MakeRequest
// with the setMessageReaction endpoint.
func TestConvertMessage_ReactFn_CallsMakeRequest(t *testing.T) {
	const groupID int64 = -100
	bot := newMockBot(0)
	chat := makeChat(groupID, "Чат")
	user := makeUser(1, "u", "", "")
	msg := makeMessage(42, chat, user, "сделаю", 0)

	w := newTelegramChannel(bot, TelegramChannelConfig{
		ReactionEnabled: true,
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, msg))
	if !ok {
		t.Fatal("want ok=true")
	}
	if got.ReactFn == nil {
		t.Fatal("ReactFn must not be nil for group message")
	}

	if err := got.ReactFn(context.Background(), "✍️"); err != nil {
		t.Fatalf("ReactFn returned error: %v", err)
	}

	bot.mu.Lock()
	calls := bot.makeReqCalls
	bot.mu.Unlock()

	if len(calls) != 1 {
		t.Fatalf("MakeRequest called %d times, want 1", len(calls))
	}
	if calls[0] != "setMessageReaction" {
		t.Errorf("endpoint: want %q, got %q", "setMessageReaction", calls[0])
	}
}

// TestConvertMessage_ReplyFn_CallsSend verifies that ReplyFn calls bot.Send.
func TestConvertMessage_ReplyFn_CallsSend(t *testing.T) {
	const groupID int64 = -100
	bot := newMockBot(0)
	chat := makeChat(groupID, "Чат")
	user := makeUser(1, "u", "", "")
	msg := makeMessage(42, chat, user, "сделаю", 0)

	w := newTelegramChannel(bot, TelegramChannelConfig{}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, msg))
	if !ok {
		t.Fatal("want ok=true")
	}
	if got.ReplyFn == nil {
		t.Fatal("ReplyFn must not be nil for group message")
	}

	if err := got.ReplyFn(context.Background(), "Запомнил!"); err != nil {
		t.Fatalf("ReplyFn returned error: %v", err)
	}

	bot.mu.Lock()
	sends := bot.sendCalled
	bot.mu.Unlock()

	if len(sends) != 1 {
		t.Fatalf("Send called %d times, want 1", len(sends))
	}
}

// TestNewTelegramChannel_HistoryConfigDefaults verifies that the constructor applies
// default HistoryConfig values for zero-value fields.
func TestNewTelegramChannel_HistoryConfigDefaults(t *testing.T) {
	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{}, newMockStateStore(), nil, nil, HistoryConfig{})

	if w.historyCfg.SilenceGap != 5*time.Minute {
		t.Errorf("SilenceGap: want %v, got %v", 5*time.Minute, w.historyCfg.SilenceGap)
	}
	if w.historyCfg.FallbackLimit != 20 {
		t.Errorf("FallbackLimit: want %d, got %d", 20, w.historyCfg.FallbackLimit)
	}
}

// TestNewTelegramChannel_HistoryConfigCustom verifies that the constructor preserves
// explicitly provided HistoryConfig values without overwriting them with defaults.
func TestNewTelegramChannel_HistoryConfigCustom(t *testing.T) {
	cfg := HistoryConfig{
		SilenceGap:    2 * time.Minute,
		FallbackLimit: 10,
	}
	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{}, newMockStateStore(), nil, nil, cfg)

	if w.historyCfg.SilenceGap != 2*time.Minute {
		t.Errorf("SilenceGap: want %v, got %v", 2*time.Minute, w.historyCfg.SilenceGap)
	}
	if w.historyCfg.FallbackLimit != 10 {
		t.Errorf("FallbackLimit: want %d, got %d", 10, w.historyCfg.FallbackLimit)
	}
}

// mockHistory implements model.History for testing.
type mockHistory struct {
	mu             sync.Mutex
	addCalls       []addHistoryArgs
	addErr         error
	recentResult   []model.HistoryEntry
	recentErr      error
	recentCallArgs []recentActivityArgs
}

type addHistoryArgs struct {
	source string
	entry  model.HistoryEntry
}

type recentActivityArgs struct {
	source        string
	silenceGap    time.Duration
	fallbackLimit int
}

func (m *mockHistory) Add(_ context.Context, source string, entry model.HistoryEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addCalls = append(m.addCalls, addHistoryArgs{source: source, entry: entry})
	return m.addErr
}

func (m *mockHistory) AddTx(_ context.Context, _ *sql.Tx, source string, entry model.HistoryEntry) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addCalls = append(m.addCalls, addHistoryArgs{source: source, entry: entry})
	return "mock-message-id", m.addErr
}

func (m *mockHistory) SetTaskIDs(_ context.Context, _ string, _ []string) error {
	return nil
}

func (m *mockHistory) ListClient(_ context.Context, _, _ string, _ int) ([]model.HistoryEntry, string, error) {
	return nil, "", nil
}

func (m *mockHistory) Recent(_ context.Context, _ string, _ int) ([]model.HistoryEntry, error) {
	return m.recentResult, m.recentErr
}

func (m *mockHistory) RecentActivity(_ context.Context, source string, silenceGap time.Duration, fallbackLimit int) ([]model.HistoryEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recentCallArgs = append(m.recentCallArgs, recentActivityArgs{source, silenceGap, fallbackLimit})
	return m.recentResult, m.recentErr
}

// TestWatch_GroupMessage_HistoryAddCalledAndHistoryFnSet verifies that for a group
// message history.Add is called and msg.HistoryFn is set.
func TestWatch_GroupMessage_HistoryAddCalledAndHistoryFnSet(t *testing.T) {
	const groupID int64 = -100
	bot := newMockBot(0)
	state := newMockStateStore()
	hist := &mockHistory{}

	chat := makeChat(groupID, "Чат")
	user := makeUser(1, "u", "", "")

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel
	bot.setUpdates([]rawUpdate{makeRawUpdate(1, makeMessage(1, chat, user, "сделаю", 1000))})

	w := newTelegramChannel(bot, TelegramChannelConfig{}, state, nil, hist, HistoryConfig{})

	var received []model.Message
	_ = w.Watch(ctx, func(_ context.Context, msg model.Message) error {
		received = append(received, msg)
		return nil
	})

	if len(received) != 1 {
		t.Fatalf("want 1 message, got %d", len(received))
	}

	hist.mu.Lock()
	addCalls := hist.addCalls
	hist.mu.Unlock()

	if len(addCalls) != 1 {
		t.Errorf("history.Add must be called 1 time, got %d", len(addCalls))
	} else {
		wantSource := "-100"
		if addCalls[0].source != wantSource {
			t.Errorf("Add source: want %q, got %q", wantSource, addCalls[0].source)
		}
		if addCalls[0].entry.Text != "сделаю" {
			t.Errorf("Add entry.Text: want %q, got %q", "сделаю", addCalls[0].entry.Text)
		}
	}
	if received[0].HistoryFn == nil {
		t.Error("HistoryFn must be set for group message")
	}
}

// TestWatch_GroupMessage_HistoryFnDelegatesToRecentActivity verifies that HistoryFn
// delegates to history.RecentActivity with the correct parameters.
func TestWatch_GroupMessage_HistoryFnDelegatesToRecentActivity(t *testing.T) {
	const groupID int64 = -100
	bot := newMockBot(0)
	state := newMockStateStore()
	expectedMsgs := []model.HistoryEntry{{AuthorName: "Автор", Text: "прошлое"}}
	hist := &mockHistory{recentResult: expectedMsgs}

	chat := makeChat(groupID, "Чат")
	user := makeUser(1, "u", "", "")

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel
	bot.setUpdates([]rawUpdate{makeRawUpdate(1, makeMessage(1, chat, user, "сделаю", 1000))})

	historyCfg := HistoryConfig{
		SilenceGap:    3 * time.Minute,
		FallbackLimit: 15,
	}
	w := newTelegramChannel(bot, TelegramChannelConfig{}, state, nil, hist, historyCfg)

	var received []model.Message
	_ = w.Watch(ctx, func(_ context.Context, msg model.Message) error {
		received = append(received, msg)
		return nil
	})

	if len(received) != 1 || received[0].HistoryFn == nil {
		t.Fatal("did not receive message with HistoryFn")
	}

	result, err := received[0].HistoryFn(context.Background())
	if err != nil {
		t.Fatalf("HistoryFn returned error: %v", err)
	}
	if len(result) != 1 || result[0].Text != "прошлое" {
		t.Errorf("unexpected HistoryFn result: %v", result)
	}

	hist.mu.Lock()
	args := hist.recentCallArgs
	hist.mu.Unlock()

	if len(args) != 1 {
		t.Fatalf("RecentActivity called %d times, want 1", len(args))
	}
	if args[0].source != "-100" {
		t.Errorf("source: want %q, got %q", "-100", args[0].source)
	}
	if args[0].silenceGap != 3*time.Minute {
		t.Errorf("silenceGap: want %v, got %v", 3*time.Minute, args[0].silenceGap)
	}
	if args[0].fallbackLimit != 15 {
		t.Errorf("fallbackLimit: want %d, got %d", 15, args[0].fallbackLimit)
	}
}

// TestWatch_DMMessage_HistoryAddCalledAndHistoryFnSet verifies that for a DM message
// history.Add is called with source="dm" and msg.HistoryFn is set.
func TestWatch_DMMessage_HistoryAddCalledAndHistoryFnSet(t *testing.T) {
	bot := newMockBot(0)
	state := newMockStateStore()
	hist := &mockHistory{}

	owner := makeUser(12345, "gregoryn", "Григорий", "")
	chat := makePrivateChat(12345)

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel
	bot.setUpdates([]rawUpdate{makeRawUpdate(1, makeMessage(1, chat, owner, "опубликую сегодня", 1000))})

	w := newTelegramChannel(bot, TelegramChannelConfig{
		OwnerIDs: []string{"12345"},
	}, state, nil, hist, HistoryConfig{})

	var received []model.Message
	_ = w.Watch(ctx, func(_ context.Context, msg model.Message) error {
		received = append(received, msg)
		return nil
	})

	if len(received) != 1 {
		t.Fatalf("want 1 message, got %d", len(received))
	}

	hist.mu.Lock()
	addCalls := hist.addCalls
	hist.mu.Unlock()

	if len(addCalls) != 1 {
		t.Errorf("history.Add must be called 1 time, got %d", len(addCalls))
	} else {
		if addCalls[0].source != "dm" {
			t.Errorf("Add source: want %q, got %q", "dm", addCalls[0].source)
		}
		if addCalls[0].entry.Text != "опубликую сегодня" {
			t.Errorf("Add entry.Text: want %q, got %q", "опубликую сегодня", addCalls[0].entry.Text)
		}
	}
	if received[0].HistoryFn == nil {
		t.Error("HistoryFn must be set for DM message")
	}
}

// TestWatch_DMMessage_HistoryFnDelegatesToRecentActivity_ExcludesCurrent verifies that
// HistoryFn for DM delegates to RecentActivity with the correct parameters
// and excludes the current message from the results by (Timestamp, Text).
func TestWatch_DMMessage_HistoryFnDelegatesToRecentActivity_ExcludesCurrent(t *testing.T) {
	bot := newMockBot(5)
	state := newMockStateStore()

	msgTimestamp := time.Unix(1700000000, 0)
	currentEntry := model.HistoryEntry{
		AuthorName: "gregoryn",
		Text:       "опубликую сегодня",
		Timestamp:  msgTimestamp,
	}
	otherEntry := model.HistoryEntry{
		AuthorName: "gregoryn",
		Text:       "и ещё одно старое",
		Timestamp:  msgTimestamp.Add(-10 * time.Minute),
	}
	hist := &mockHistory{recentResult: []model.HistoryEntry{otherEntry, currentEntry}}

	owner := makeUser(12345, "gregoryn", "Григорий", "")
	chat := makePrivateChat(12345)

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel
	bot.setUpdates([]rawUpdate{makeRawUpdate(1, makeMessage(1, chat, owner, "опубликую сегодня", 1700000000))})

	historyCfg := HistoryConfig{
		SilenceGap:    3 * time.Minute,
		FallbackLimit: 10,
	}
	w := newTelegramChannel(bot, TelegramChannelConfig{
		OwnerIDs: []string{"12345"},
	}, state, nil, hist, historyCfg)

	var received []model.Message
	_ = w.Watch(ctx, func(_ context.Context, msg model.Message) error {
		received = append(received, msg)
		return nil
	})

	if len(received) != 1 || received[0].HistoryFn == nil {
		t.Fatal("did not receive DM message with HistoryFn")
	}

	result, err := received[0].HistoryFn(context.Background())
	if err != nil {
		t.Fatalf("HistoryFn returned error: %v", err)
	}

	hist.mu.Lock()
	args := hist.recentCallArgs
	hist.mu.Unlock()

	if len(args) != 1 {
		t.Fatalf("RecentActivity called %d times, want 1", len(args))
	}
	if args[0].source != "dm" {
		t.Errorf("source: want %q, got %q", "dm", args[0].source)
	}
	if args[0].silenceGap != 3*time.Minute {
		t.Errorf("silenceGap: want %v, got %v", 3*time.Minute, args[0].silenceGap)
	}
	if args[0].fallbackLimit != 10 {
		t.Errorf("fallbackLimit: want %d, got %d", 10, args[0].fallbackLimit)
	}

	// The current message must be excluded from history
	if len(result) != 1 {
		t.Fatalf("want 1 entry (excluding current), got %d", len(result))
	}
	if result[0].Text != otherEntry.Text {
		t.Errorf("want %q, got %q", otherEntry.Text, result[0].Text)
	}
}

// TestWatch_NilHistory_HistoryFnNil verifies that when history == nil
// HistoryFn is not set for any message type.
func TestWatch_NilHistory_HistoryFnNil(t *testing.T) {
	const groupID int64 = -100
	bot := newMockBot(0)
	state := newMockStateStore()

	chat := makeChat(groupID, "Чат")
	user := makeUser(1, "u", "", "")

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel
	bot.setUpdates([]rawUpdate{makeRawUpdate(1, makeMessage(1, chat, user, "сообщение", 1000))})

	w := newTelegramChannel(bot, TelegramChannelConfig{}, state, nil, nil, HistoryConfig{})

	var received []model.Message
	_ = w.Watch(ctx, func(_ context.Context, msg model.Message) error {
		received = append(received, msg)
		return nil
	})

	if len(received) != 1 {
		t.Fatalf("want 1 message, got %d", len(received))
	}
	if received[0].HistoryFn != nil {
		t.Error("HistoryFn must be nil when history == nil")
	}
}

// TestWatch_HistoryAddError_HandlerStillCalled verifies that a history.Add error
// is logged but does not interrupt processing — the handler is called and HistoryFn is set.
func TestWatch_HistoryAddError_HandlerStillCalled(t *testing.T) {
	const groupID int64 = -100
	bot := newMockBot(0)
	state := newMockStateStore()
	hist := &mockHistory{addErr: errors.New("ошибка истории")}

	chat := makeChat(groupID, "Чат")
	user := makeUser(1, "u", "", "")

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel
	bot.setUpdates([]rawUpdate{makeRawUpdate(1, makeMessage(1, chat, user, "сообщение", 1000))})

	w := newTelegramChannel(bot, TelegramChannelConfig{}, state, nil, hist, HistoryConfig{})

	var received []model.Message
	_ = w.Watch(ctx, func(_ context.Context, msg model.Message) error {
		received = append(received, msg)
		return nil
	})

	if len(received) != 1 {
		t.Fatalf("handler must be called 1 time, got %d", len(received))
	}
	if received[0].HistoryFn == nil {
		t.Error("HistoryFn must be set even on history.Add error")
	}
}

// TestWatch_DMMessage_HistoryAddError_HandlerStillCalled verifies that a history.Add error
// for a DM message is logged but does not interrupt processing — the handler is called and HistoryFn is set.
func TestWatch_DMMessage_HistoryAddError_HandlerStillCalled(t *testing.T) {
	bot := newMockBot(0)
	state := newMockStateStore()
	hist := &mockHistory{addErr: errors.New("ошибка истории")}

	owner := makeUser(12345, "gregoryn", "Григорий", "")
	chat := makePrivateChat(12345)

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel
	bot.setUpdates([]rawUpdate{makeRawUpdate(1, makeMessage(1, chat, owner, "сделаю", 1000))})

	w := newTelegramChannel(bot, TelegramChannelConfig{
		OwnerIDs: []string{"12345"},
	}, state, nil, hist, HistoryConfig{})

	var received []model.Message
	_ = w.Watch(ctx, func(_ context.Context, msg model.Message) error {
		received = append(received, msg)
		return nil
	})

	if len(received) != 1 {
		t.Fatalf("handler must be called 1 time, got %d", len(received))
	}
	if received[0].HistoryFn == nil {
		t.Error("HistoryFn must be set even on history.Add error")
	}
}

// TestWatch_EmptyTextGroupMessage_NotAddedToHistory verifies that group messages
// without text (stickers, photos, service messages) are not added to history and do not get HistoryFn.
func TestWatch_EmptyTextGroupMessage_NotAddedToHistory(t *testing.T) {
	const groupID int64 = -100
	bot := newMockBot(0)
	state := newMockStateStore()
	hist := &mockHistory{}

	chat := makeChat(groupID, "Чат")
	user := makeUser(1, "u", "", "")

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel
	bot.setUpdates([]rawUpdate{makeRawUpdate(1, makeMessage(1, chat, user, "", 1000))})

	w := newTelegramChannel(bot, TelegramChannelConfig{}, state, nil, hist, HistoryConfig{})

	var received []model.Message
	_ = w.Watch(ctx, func(_ context.Context, msg model.Message) error {
		received = append(received, msg)
		return nil
	})

	if len(received) != 1 {
		t.Fatalf("handler must be called 1 time, got %d", len(received))
	}
	if len(hist.addCalls) > 0 {
		t.Error("history.Add must not be called for message with empty text")
	}
	if received[0].HistoryFn != nil {
		t.Error("HistoryFn must be nil for message with empty text")
	}
}

// TestWatch_EmptyTextDMMessage_NotAddedToHistory verifies that DM messages
// without text (stickers, photos, service messages) are not added to history and do not get HistoryFn.
func TestWatch_EmptyTextDMMessage_NotAddedToHistory(t *testing.T) {
	bot := newMockBot(0)
	state := newMockStateStore()
	hist := &mockHistory{}

	owner := makeUser(12345, "gregoryn", "Григорий", "")
	chat := makePrivateChat(12345)

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel
	bot.setUpdates([]rawUpdate{makeRawUpdate(1, makeMessage(1, chat, owner, "", 1000))})

	w := newTelegramChannel(bot, TelegramChannelConfig{
		OwnerIDs: []string{"12345"},
	}, state, nil, hist, HistoryConfig{})

	var received []model.Message
	_ = w.Watch(ctx, func(_ context.Context, msg model.Message) error {
		received = append(received, msg)
		return nil
	})

	if len(received) != 1 {
		t.Fatalf("handler must be called 1 time, got %d", len(received))
	}
	if len(hist.addCalls) > 0 {
		t.Error("history.Add must not be called for DM message with empty text")
	}
	if received[0].HistoryFn != nil {
		t.Error("HistoryFn must be nil for DM message with empty text")
	}
}

// TestConvertMessage_GroupDirect_BotMention verifies that a group message
// mentioning the bot gets Kind=GroupDirect.
func TestConvertMessage_GroupDirect_BotMention(t *testing.T) {
	const groupID int64 = -100
	chat := makeChat(groupID, "Рабочий чат")
	user := makeUser(1, "user1", "Иван", "")
	msg := makeMessage(42, chat, user, "@testbot сделай задачу", 0)
	msg.Entities = []tgbotapi.MessageEntity{
		{Type: "mention", Offset: 0, Length: 8}, // "@testbot"
	}

	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{
		BotUsername: "testbot",
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, msg))

	if !ok {
		t.Fatal("want ok=true")
	}
	if got.Kind != model.MessageKindGroupDirect {
		t.Errorf("Kind: want %q, got %q", model.MessageKindGroupDirect, got.Kind)
	}
}

// TestConvertMessage_GroupDirect_BotMentionCaseInsensitive verifies that a bot mention
// is case-insensitive.
func TestConvertMessage_GroupDirect_BotMentionCaseInsensitive(t *testing.T) {
	const groupID int64 = -100
	chat := makeChat(groupID, "Чат")
	user := makeUser(1, "u", "", "")
	msg := makeMessage(1, chat, user, "@TestBot покажи задачи", 0)
	msg.Entities = []tgbotapi.MessageEntity{
		{Type: "mention", Offset: 0, Length: 8}, // "@TestBot"
	}

	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{
		BotUsername: "testbot",
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, msg))

	if !ok {
		t.Fatal("want ok=true")
	}
	if got.Kind != model.MessageKindGroupDirect {
		t.Errorf("Kind: want %q, got %q", model.MessageKindGroupDirect, got.Kind)
	}
}

// TestConvertMessage_GroupDirect_BotReply verifies that a reply to a bot message
// in a group chat gets Kind=GroupDirect.
func TestConvertMessage_GroupDirect_BotReply(t *testing.T) {
	const groupID int64 = -100
	const botID int64 = 999

	chat := makeChat(groupID, "Чат")
	botUser := makeUser(botID, "testbot", "TestBot", "")
	botMsg := makeMessage(10, chat, botUser, "Задача создана!", 999)

	user := makeUser(1, "user1", "Иван", "")
	replyMsg := makeMessage(11, chat, user, "а когда готово?", 1000)
	replyMsg.ReplyToMessage = botMsg

	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{
		BotID: "999",
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, replyMsg))

	if !ok {
		t.Fatal("want ok=true")
	}
	if got.Kind != model.MessageKindGroupDirect {
		t.Errorf("Kind: want %q, got %q", model.MessageKindGroupDirect, got.Kind)
	}
}

// TestConvertMessage_GroupKind_NoMentionNoReply verifies that a plain group
// message (without mention/reply to the bot) gets Kind=Group.
func TestConvertMessage_GroupKind_NoMentionNoReply(t *testing.T) {
	const groupID int64 = -100

	chat := makeChat(groupID, "Чат")
	user := makeUser(1, "u", "", "")
	msg := makeMessage(1, chat, user, "сделаю к завтрашнему утру", 0)
	// no entities and no reply

	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{
		BotID:       "999",
		BotUsername: "testbot",
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, msg))

	if !ok {
		t.Fatal("want ok=true")
	}
	if got.Kind != model.MessageKindGroup {
		t.Errorf("Kind: want %q, got %q", model.MessageKindGroup, got.Kind)
	}
}

// TestConvertMessage_GroupKind_ReplyToOtherUser verifies that a reply to a message
// from another user (not the bot) does not change Kind to GroupDirect.
func TestConvertMessage_GroupKind_ReplyToOtherUser(t *testing.T) {
	const groupID int64 = -100
	const botID int64 = 999

	chat := makeChat(groupID, "Чат")
	otherUser := makeUser(42, "other", "Другой", "")
	otherMsg := makeMessage(10, chat, otherUser, "привет", 999)

	user := makeUser(1, "u", "", "")
	replyMsg := makeMessage(11, chat, user, "и тебе!", 1000)
	replyMsg.ReplyToMessage = otherMsg

	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{
		BotID: "999",
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, replyMsg))

	if !ok {
		t.Fatal("want ok=true")
	}
	if got.Kind != model.MessageKindGroup {
		t.Errorf("Kind: want %q, got %q", model.MessageKindGroup, got.Kind)
	}
}

// TestConvertMessage_GroupKind_MentionOtherUser verifies that a mention of another user
// (not the bot) does not change Kind to GroupDirect.
func TestConvertMessage_GroupKind_MentionOtherUser(t *testing.T) {
	const groupID int64 = -100

	chat := makeChat(groupID, "Чат")
	user := makeUser(1, "u", "", "")
	msg := makeMessage(1, chat, user, "@anotheruser привет!", 0)
	msg.Entities = []tgbotapi.MessageEntity{
		{Type: "mention", Offset: 0, Length: 12}, // "@anotheruser"
	}

	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{
		BotUsername: "testbot",
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, msg))

	if !ok {
		t.Fatal("want ok=true")
	}
	if got.Kind != model.MessageKindGroup {
		t.Errorf("Kind: want %q, got %q", model.MessageKindGroup, got.Kind)
	}
}

// TestConvertMessage_GroupKind_NoBotIDOrUsername verifies that when BotID and BotUsername
// are absent from the config messages remain Kind=Group (no GroupDirect).
func TestConvertMessage_GroupKind_NoBotIDOrUsername(t *testing.T) {
	const groupID int64 = -100

	chat := makeChat(groupID, "Чат")
	botUser := makeUser(999, "testbot", "Bot", "")
	botMsg := makeMessage(5, chat, botUser, "ок", 1)

	user := makeUser(1, "u", "", "")
	msg := makeMessage(6, chat, user, "спасибо", 2)
	msg.ReplyToMessage = botMsg // reply to bot, but BotID not configured

	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{
		// BotID and BotUsername are not set
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, msg))

	if !ok {
		t.Fatal("want ok=true")
	}
	if got.Kind != model.MessageKindGroup {
		t.Errorf("Kind: want %q, got %q", model.MessageKindGroup, got.Kind)
	}
}

// TestConvertMessage_Group_ReplyFn_WritesHistory verifies that ReplyFn for a group message
// writes the bot reply to history with the correct source and AuthorName.
func TestConvertMessage_Group_ReplyFn_WritesHistory(t *testing.T) {
	const groupID int64 = -100
	bot := newMockBot(0)
	hist := &mockHistory{}
	chat := makeChat(groupID, "Чат")
	user := makeUser(1, "u", "", "")
	msg := makeMessage(42, chat, user, "сделаю", 1000)

	w := newTelegramChannel(bot, TelegramChannelConfig{
		BotUsername: "testbot",
	}, newMockStateStore(), nil, hist, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, msg))
	if !ok {
		t.Fatal("want ok=true")
	}
	if err := got.ReplyFn(context.Background(), "Запомнил!"); err != nil {
		t.Fatalf("ReplyFn returned error: %v", err)
	}

	hist.mu.Lock()
	addCalls := hist.addCalls
	hist.mu.Unlock()

	if len(addCalls) != 1 {
		t.Fatalf("history.Add called %d times, want 1", len(addCalls))
	}
	if addCalls[0].source != "-100" {
		t.Errorf("source: want %q, got %q", "-100", addCalls[0].source)
	}
	if addCalls[0].entry.AuthorName != "testbot" {
		t.Errorf("AuthorName: want %q, got %q", "testbot", addCalls[0].entry.AuthorName)
	}
	if addCalls[0].entry.Text != "Запомнил!" {
		t.Errorf("Text: want %q, got %q", "Запомнил!", addCalls[0].entry.Text)
	}
}

// TestConvertMessage_ReplyFn_UsesDateFromSentMessage verifies that the history entry Timestamp
// is taken from the Date field of the bot's sent message (time.Unix(date, 0)).
func TestConvertMessage_ReplyFn_UsesDateFromSentMessage(t *testing.T) {
	const groupID int64 = -100
	const sentDate = int(1700000000)
	bot := newMockBot(0)
	bot.sendResult = tgbotapi.Message{Date: sentDate}
	hist := &mockHistory{}
	chat := makeChat(groupID, "Чат")
	user := makeUser(1, "u", "", "")
	msg := makeMessage(1, chat, user, "сделаю", 0)

	w := newTelegramChannel(bot, TelegramChannelConfig{
		BotUsername: "testbot",
	}, newMockStateStore(), nil, hist, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, msg))
	if !ok {
		t.Fatal("want ok=true")
	}
	if err := got.ReplyFn(context.Background(), "Готово"); err != nil {
		t.Fatalf("ReplyFn returned error: %v", err)
	}

	hist.mu.Lock()
	addCalls := hist.addCalls
	hist.mu.Unlock()

	if len(addCalls) != 1 {
		t.Fatalf("history.Add called %d times, want 1", len(addCalls))
	}
	wantTS := time.Unix(int64(sentDate), 0)
	if !addCalls[0].entry.Timestamp.Equal(wantTS) {
		t.Errorf("Timestamp: want %v, got %v", wantTS, addCalls[0].entry.Timestamp)
	}
}

// TestConvertMessage_DM_ReplyFn_WritesHistory verifies that ReplyFn for a DM message
// writes the bot reply to history with source="dm".
func TestConvertMessage_DM_ReplyFn_WritesHistory(t *testing.T) {
	bot := newMockBot(0)
	hist := &mockHistory{}
	owner := makeUser(12345, "gregoryn", "Григорий", "")
	chat := makePrivateChat(12345)
	msg := makeMessage(1, chat, owner, "опубликую сегодня", 1000)

	w := newTelegramChannel(bot, TelegramChannelConfig{
		OwnerIDs:    []string{"12345"},
		BotUsername: "testbot",
	}, newMockStateStore(), nil, hist, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, msg))
	if !ok {
		t.Fatal("want ok=true for owner DM")
	}
	if err := got.ReplyFn(context.Background(), "Понял, записал!"); err != nil {
		t.Fatalf("ReplyFn returned error: %v", err)
	}

	hist.mu.Lock()
	addCalls := hist.addCalls
	hist.mu.Unlock()

	if len(addCalls) != 1 {
		t.Fatalf("history.Add called %d times, want 1", len(addCalls))
	}
	if addCalls[0].source != "dm" {
		t.Errorf("source: want %q, got %q", "dm", addCalls[0].source)
	}
	if addCalls[0].entry.AuthorName != "testbot" {
		t.Errorf("AuthorName: want %q, got %q", "testbot", addCalls[0].entry.AuthorName)
	}
	if addCalls[0].entry.Text != "Понял, записал!" {
		t.Errorf("Text: want %q, got %q", "Понял, записал!", addCalls[0].entry.Text)
	}
}

// TestConvertMessage_ReplyFn_EmptyBotUsername_DefaultsToBot verifies that when
// BotUsername is empty the history entry uses AuthorName == "bot".
func TestConvertMessage_ReplyFn_EmptyBotUsername_DefaultsToBot(t *testing.T) {
	const groupID int64 = -100
	bot := newMockBot(0)
	hist := &mockHistory{}
	chat := makeChat(groupID, "Чат")
	user := makeUser(1, "u", "", "")
	msg := makeMessage(1, chat, user, "сделаю", 0)

	w := newTelegramChannel(bot, TelegramChannelConfig{
		BotUsername: "",
	}, newMockStateStore(), nil, hist, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, msg))
	if !ok {
		t.Fatal("want ok=true")
	}
	if err := got.ReplyFn(context.Background(), "Ок"); err != nil {
		t.Fatalf("ReplyFn returned error: %v", err)
	}

	hist.mu.Lock()
	addCalls := hist.addCalls
	hist.mu.Unlock()

	if len(addCalls) != 1 {
		t.Fatalf("history.Add called %d times, want 1", len(addCalls))
	}
	if addCalls[0].entry.AuthorName != "bot" {
		t.Errorf("AuthorName: want %q, got %q", "bot", addCalls[0].entry.AuthorName)
	}
}

// TestConvertMessage_ReplyFn_NilHistory_NoPanic verifies that ReplyFn does not panic
// when history is nil.
func TestConvertMessage_ReplyFn_NilHistory_NoPanic(t *testing.T) {
	const groupID int64 = -100
	bot := newMockBot(0)
	chat := makeChat(groupID, "Чат")
	user := makeUser(1, "u", "", "")
	msg := makeMessage(1, chat, user, "сделаю", 0)

	w := newTelegramChannel(bot, TelegramChannelConfig{}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, msg))
	if !ok {
		t.Fatal("want ok=true")
	}
	if err := got.ReplyFn(context.Background(), "ок"); err != nil {
		t.Fatalf("ReplyFn returned error with nil history: %v", err)
	}
	if len(bot.sendCalled) != 1 {
		t.Errorf("bot.Send must be called 1 time, got %d", len(bot.sendCalled))
	}
}

// TestConvertMessage_ReplyFn_HistoryAddError_StillReturnsNil verifies that a
// history.Add error does not propagate as a ReplyFn error.
func TestConvertMessage_ReplyFn_HistoryAddError_StillReturnsNil(t *testing.T) {
	const groupID int64 = -100
	bot := newMockBot(0)
	hist := &mockHistory{addErr: errors.New("ошибка")}
	chat := makeChat(groupID, "Чат")
	user := makeUser(1, "u", "", "")
	msg := makeMessage(1, chat, user, "сделаю", 0)

	w := newTelegramChannel(bot, TelegramChannelConfig{}, newMockStateStore(), nil, hist, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, msg))
	if !ok {
		t.Fatal("want ok=true")
	}
	if err := got.ReplyFn(context.Background(), "ок"); err != nil {
		t.Errorf("ReplyFn must return nil even on history.Add error, got: %v", err)
	}
}

// TestConvertDMMessage_ReactFn verifies that a DM message populates ReactFn.
func TestConvertDMMessage_ReactFn(t *testing.T) {
	bot := newMockBot(0)
	owner := makeUser(12345, "gregoryn", "Григорий", "")
	chat := makePrivateChat(12345)
	msg := makeMessage(7, chat, owner, "сделаю", 0)

	w := newTelegramChannel(bot, TelegramChannelConfig{
		OwnerIDs:        []string{"12345"},
		ReactionEnabled: true,
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	got, ok := w.convertUpdate(makeUpdate(1, msg))
	if !ok {
		t.Fatal("want ok=true")
	}
	if got.ReactFn == nil {
		t.Fatal("ReactFn must not be nil for DM message")
	}
	if got.ReplyFn == nil {
		t.Fatal("ReplyFn must not be nil for DM message")
	}
}

// --- Guard method tests ---

func makeJoinUpdate(chatID int64, newStatus, oldStatus string) *tgbotapi.ChatMemberUpdated {
	botUser := &tgbotapi.User{ID: 999, UserName: "testbot"}
	return &tgbotapi.ChatMemberUpdated{
		Chat: tgbotapi.Chat{ID: chatID, Title: "Test Group"},
		NewChatMember: tgbotapi.ChatMember{
			User:   botUser,
			Status: newStatus,
		},
		OldChatMember: tgbotapi.ChatMember{
			User:   botUser,
			Status: oldStatus,
		},
	}
}

func TestHandleJoin_SendsWelcomeAndRegistersPending(t *testing.T) {
	const chatID int64 = -1001111111111
	bot := newMockBot(0)
	bot.sendResult = tgbotapi.Message{MessageID: 42}
	w := newTelegramChannel(bot, TelegramChannelConfig{
		OwnerIDs:       []string{"100"},
		ConfirmTimeout: 1 * time.Minute,
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	w.handleJoin(context.Background(), makeJoinUpdate(chatID, "member", "left"))

	if len(bot.sendCalled) != 1 {
		t.Fatalf("Send called %d times, want 1", len(bot.sendCalled))
	}
	w.pendingMu.Lock()
	pa := w.pending[chatID]
	w.pendingMu.Unlock()
	if pa == nil {
		t.Fatal("pending was not registered")
	}
	if pa.welcomeMsgID != 42 {
		t.Errorf("welcomeMsgID = %d, want 42", pa.welcomeMsgID)
	}
}

func TestHandleJoin_ConfirmTimeoutZero_NoGuard(t *testing.T) {
	const chatID int64 = -1002222222222
	bot := newMockBot(0)
	w := newTelegramChannel(bot, TelegramChannelConfig{
		OwnerIDs:       []string{"100"},
		ConfirmTimeout: 0,
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	w.handleJoin(context.Background(), makeJoinUpdate(chatID, "member", "left"))

	if len(bot.sendCalled) != 0 {
		t.Error("Send must not be called when ConfirmTimeout == 0")
	}
}

func TestIsReplyConfirmation_OwnerWithCorrectReply(t *testing.T) {
	const chatID int64 = -100
	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{
		OwnerIDs: []string{"100"},
	}, newMockStateStore(), nil, nil, HistoryConfig{})
	w.pending[chatID] = &pendingApproval{welcomeMsgID: 77, deadline: time.Now().Add(time.Minute)}

	msg := &tgbotapi.Message{
		Chat:           &tgbotapi.Chat{ID: chatID},
		From:           &tgbotapi.User{ID: 100},
		ReplyToMessage: &tgbotapi.Message{MessageID: 77},
	}
	if !w.isReplyConfirmation(msg) {
		t.Error("want true — owner reply to welcomeMsgID")
	}
}

func TestIsReplyConfirmation_NotOwner(t *testing.T) {
	const chatID int64 = -100
	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{
		OwnerIDs: []string{"100"},
	}, newMockStateStore(), nil, nil, HistoryConfig{})
	w.pending[chatID] = &pendingApproval{welcomeMsgID: 77, deadline: time.Now().Add(time.Minute)}

	msg := &tgbotapi.Message{
		Chat:           &tgbotapi.Chat{ID: chatID},
		From:           &tgbotapi.User{ID: 999},
		ReplyToMessage: &tgbotapi.Message{MessageID: 77},
	}
	if w.isReplyConfirmation(msg) {
		t.Error("want false — reply from non-owner")
	}
}

func TestIsReactionConfirmation_OwnerOnCorrectMsg(t *testing.T) {
	const chatID int64 = -100
	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{
		OwnerIDs: []string{"100"},
	}, newMockStateStore(), nil, nil, HistoryConfig{})
	w.pending[chatID] = &pendingApproval{welcomeMsgID: 55, deadline: time.Now().Add(time.Minute)}

	r := &rawMessageReaction{
		Chat:      tgbotapi.Chat{ID: chatID},
		MessageID: 55,
		User:      &tgbotapi.User{ID: 100},
	}
	if !w.isReactionConfirmation(r) {
		t.Error("want true — owner reaction to welcomeMsgID")
	}
}

func TestCheckAndExpirePending_Expired_LeaveChatCalled(t *testing.T) {
	const chatID int64 = -100
	bot := newMockBot(0)
	w := newTelegramChannel(bot, TelegramChannelConfig{
		OwnerIDs: []string{"100"},
	}, newMockStateStore(), nil, nil, HistoryConfig{})
	w.pending[chatID] = &pendingApproval{
		welcomeMsgID: 1,
		deadline:     time.Now().Add(-1 * time.Second),
	}

	w.checkAndExpirePending(context.Background())

	leaveCalled := false
	for _, ep := range bot.makeReqCalls {
		if ep == "leaveChat" {
			leaveCalled = true
			break
		}
	}
	if !leaveCalled {
		t.Error("leaveChat was not called for expired pending")
	}
	w.pendingMu.Lock()
	_, stillPending := w.pending[chatID]
	w.pendingMu.Unlock()
	if stillPending {
		t.Error("pending must be removed after timeout expiry")
	}
}

func TestCheckAndExpirePending_NotExpired_LeaveNotCalled(t *testing.T) {
	const chatID int64 = -100
	bot := newMockBot(0)
	w := newTelegramChannel(bot, TelegramChannelConfig{
		OwnerIDs: []string{"100"},
	}, newMockStateStore(), nil, nil, HistoryConfig{})
	w.pending[chatID] = &pendingApproval{
		welcomeMsgID: 1,
		deadline:     time.Now().Add(1 * time.Minute),
	}

	w.checkAndExpirePending(context.Background())

	for _, ep := range bot.makeReqCalls {
		if ep == "leaveChat" {
			t.Error("leaveChat must not be called for non-expired pending")
		}
	}
}

// --- Watch guard integration tests ---

// TestWatch_Guard_BotAdded_SendsWelcome verifies that on a my_chat_member update Watch
// sends a welcome message.
func TestWatch_Guard_BotAdded_SendsWelcome(t *testing.T) {
	const chatID int64 = -1009999999999
	bot := newMockBot(0)
	bot.sendResult = tgbotapi.Message{MessageID: 100}

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel

	joinUpd := rawUpdate{
		UpdateID: 1,
		MyChatMember: &tgbotapi.ChatMemberUpdated{
			Chat: tgbotapi.Chat{ID: chatID},
			NewChatMember: tgbotapi.ChatMember{
				User:   &tgbotapi.User{ID: 999},
				Status: "member",
			},
			OldChatMember: tgbotapi.ChatMember{
				User:   &tgbotapi.User{ID: 999},
				Status: "left",
			},
		},
	}
	bot.setUpdates([]rawUpdate{joinUpd})

	w := newTelegramChannel(bot, TelegramChannelConfig{
		OwnerIDs:       []string{"100"},
		ConfirmTimeout: 1 * time.Minute,
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	_ = w.Watch(ctx, func(_ context.Context, _ model.Message) error { return nil })

	if len(bot.sendCalled) == 0 {
		t.Error("Send was not called — expected welcome message")
	}
}

// TestWatch_Guard_ReplyConfirms verifies that a reply from the owner confirms a pending chat.
func TestWatch_Guard_ReplyConfirms(t *testing.T) {
	const chatID int64 = -100
	const ownerID int64 = 42
	const welcomeMsgID = 77
	bot := newMockBot(0)
	bot.sendResult = tgbotapi.Message{MessageID: welcomeMsgID}

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel

	ownerUser := &tgbotapi.User{ID: ownerID}
	groupChat := makeChat(chatID, "Тест")

	// Batch 1: bot added
	joinUpd := rawUpdate{
		UpdateID: 1,
		MyChatMember: &tgbotapi.ChatMemberUpdated{
			Chat:          tgbotapi.Chat{ID: chatID},
			NewChatMember: tgbotapi.ChatMember{User: ownerUser, Status: "member"},
			OldChatMember: tgbotapi.ChatMember{User: ownerUser, Status: "left"},
		},
	}
	// Batch 2: owner reply to the welcome message
	replyMsg := makeMessage(2, groupChat, ownerUser, "ok", 1000)
	replyMsg.ReplyToMessage = &tgbotapi.Message{MessageID: welcomeMsgID}
	replyUpd := rawUpdate{UpdateID: 2, Message: replyMsg}
	// Batch 3: normal message — must be processed (chat confirmed)
	normalMsg := makeMessage(3, groupChat, &tgbotapi.User{ID: 1, UserName: "user"}, "привет", 2000)
	normalUpd := rawUpdate{UpdateID: 3, Message: normalMsg}

	bot.setUpdates([]rawUpdate{joinUpd})
	bot.setUpdates([]rawUpdate{replyUpd})
	bot.setUpdates([]rawUpdate{normalUpd})

	w := newTelegramChannel(bot, TelegramChannelConfig{
		OwnerIDs:       []string{"42"},
		ConfirmTimeout: 1 * time.Minute,
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	var received []model.Message
	_ = w.Watch(ctx, func(_ context.Context, msg model.Message) error {
		received = append(received, msg)
		return nil
	})

	if len(received) != 1 || received[0].Text != "привет" {
		t.Errorf("want 1 normal message 'привет', got: %v", received)
	}
	w.pendingMu.Lock()
	_, stillPending := w.pending[chatID]
	w.pendingMu.Unlock()
	if stillPending {
		t.Error("chat must be confirmed (pending removed) after owner reply")
	}
}

// TestWatch_Guard_ReactionConfirms verifies that a reaction from the owner confirms a pending chat.
func TestWatch_Guard_ReactionConfirms(t *testing.T) {
	const chatID int64 = -100
	const ownerID int64 = 42
	const welcomeMsgID = 55
	bot := newMockBot(0)
	bot.sendResult = tgbotapi.Message{MessageID: welcomeMsgID}

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel

	ownerUser := &tgbotapi.User{ID: ownerID}

	joinUpd := rawUpdate{
		UpdateID: 1,
		MyChatMember: &tgbotapi.ChatMemberUpdated{
			Chat:          tgbotapi.Chat{ID: chatID},
			NewChatMember: tgbotapi.ChatMember{User: ownerUser, Status: "member"},
			OldChatMember: tgbotapi.ChatMember{User: ownerUser, Status: "left"},
		},
	}
	reactionUpd := rawUpdate{
		UpdateID: 2,
		MessageReaction: &rawMessageReaction{
			Chat:      tgbotapi.Chat{ID: chatID},
			MessageID: welcomeMsgID,
			User:      ownerUser,
		},
	}
	bot.setUpdates([]rawUpdate{joinUpd})
	bot.setUpdates([]rawUpdate{reactionUpd})

	w := newTelegramChannel(bot, TelegramChannelConfig{
		OwnerIDs:       []string{"42"},
		ConfirmTimeout: 1 * time.Minute,
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	_ = w.Watch(ctx, func(_ context.Context, _ model.Message) error { return nil })

	w.pendingMu.Lock()
	_, stillPending := w.pending[chatID]
	w.pendingMu.Unlock()
	if stillPending {
		t.Error("chat must be confirmed after owner reaction")
	}
}

// TestWatch_Guard_PendingMessagesSkipped verifies that normal messages in a pending chat
// are not forwarded to the handler.
func TestWatch_Guard_PendingMessagesSkipped(t *testing.T) {
	const chatID int64 = -100
	bot := newMockBot(0)

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel

	groupChat := makeChat(chatID, "Тест")
	randomUser := &tgbotapi.User{ID: 999, UserName: "stranger"}
	normalMsg := makeMessage(1, groupChat, randomUser, "хочу взломать", 1000)
	normalUpd := rawUpdate{UpdateID: 1, Message: normalMsg}
	bot.setUpdates([]rawUpdate{normalUpd})

	w := newTelegramChannel(bot, TelegramChannelConfig{
		OwnerIDs:       []string{"42"},
		ConfirmTimeout: 1 * time.Minute,
	}, newMockStateStore(), nil, nil, HistoryConfig{})
	// Set pending manually
	w.pending[chatID] = &pendingApproval{welcomeMsgID: 1, deadline: time.Now().Add(time.Minute)}

	var received []model.Message
	_ = w.Watch(ctx, func(_ context.Context, msg model.Message) error {
		received = append(received, msg)
		return nil
	})

	if len(received) != 0 {
		t.Errorf("handler must not be called for pending chat, got %d messages", len(received))
	}
}

// --- Voice transcription tests ---

// mockTranscriber implements model.Transcriber for testing.
type mockTranscriber struct {
	mu           sync.Mutex
	transcribeFn func(ctx context.Context, audio model.AudioInput) (string, error)
	calls        []model.AudioInput
}

func (m *mockTranscriber) Transcribe(ctx context.Context, audio model.AudioInput) (string, error) {
	m.mu.Lock()
	m.calls = append(m.calls, audio)
	m.mu.Unlock()
	if m.transcribeFn != nil {
		return m.transcribeFn(ctx, audio)
	}
	return "", nil
}

func (m *mockTranscriber) Name() string { return "mock" }

func makeTestLocalizer(t *testing.T, lang string) *goI18n.Localizer {
	t.Helper()
	bundle, err := huskwootI18n.NewBundle(lang)
	if err != nil {
		t.Fatalf("NewBundle(%s): %v", lang, err)
	}
	return huskwootI18n.NewLocalizer(bundle, lang)
}

func makeVoiceMessage(msgID int, chat *tgbotapi.Chat, from *tgbotapi.User, fileID string, duration, date int) *tgbotapi.Message {
	return &tgbotapi.Message{
		MessageID: msgID,
		Chat:      chat,
		From:      from,
		Date:      date,
		Voice:     &tgbotapi.Voice{FileID: fileID, Duration: duration},
	}
}

// watchVoice is a test helper that delivers a single voice rawUpdate through Watch
// and returns all messages received by the handler plus bot.Send calls.
func watchVoice(t *testing.T, cfg TelegramChannelConfig, tgMsg *tgbotapi.Message) (received []model.Message, sendCalled []tgbotapi.Chattable) {
	t.Helper()
	bot := newMockBot(0)
	state := newMockStateStore()

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel
	bot.setUpdates([]rawUpdate{makeRawUpdate(1, tgMsg)})

	w := newTelegramChannel(bot, cfg, state, nil, nil, HistoryConfig{})
	_ = w.Watch(ctx, func(_ context.Context, msg model.Message) error {
		received = append(received, msg)
		return nil
	})

	bot.mu.Lock()
	sendCalled = append([]tgbotapi.Chattable{}, bot.sendCalled...)
	bot.mu.Unlock()
	return received, sendCalled
}

// TestWatch_VoiceDM_TranscriberReturnsText verifies that a voice DM is transcribed and
// the handler receives a message with the recognised text.
func TestWatch_VoiceDM_TranscriberReturnsText(t *testing.T) {
	const ownerID int64 = 12345
	audio := []byte("ogg-bytes")

	tr := &mockTranscriber{
		transcribeFn: func(_ context.Context, _ model.AudioInput) (string, error) {
			return "привет мир", nil
		},
	}
	cfg := TelegramChannelConfig{
		OwnerIDs:       []string{"12345"},
		Transcriber:    tr,
		Localizer:      makeTestLocalizer(t, "ru"),
		DownloadFileFn: func(_ context.Context, _ string) ([]byte, error) { return audio, nil },
	}
	tgMsg := makeVoiceMessage(1, makePrivateChat(ownerID), &tgbotapi.User{ID: ownerID}, "file123", 10, 1700000000)

	received, sendCalled := watchVoice(t, cfg, tgMsg)

	if len(received) != 1 {
		t.Fatalf("handler must be called once, got %d", len(received))
	}
	if received[0].Text != "привет мир" {
		t.Errorf("Text: want %q, got %q", "привет мир", received[0].Text)
	}
	if received[0].Kind != model.MessageKindDM {
		t.Errorf("Kind: want DM, got %q", received[0].Kind)
	}
	// no i18n reply should be sent
	if len(sendCalled) != 0 {
		t.Errorf("bot.Send must not be called on success, got %d calls", len(sendCalled))
	}

	tr.mu.Lock()
	calls := tr.calls
	tr.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("Transcribe must be called once, got %d", len(calls))
	}
	if string(calls[0].Data) != string(audio) {
		t.Errorf("Transcribe received wrong data")
	}
}

// TestWatch_VoiceDM_TranscriberError verifies that a transcription error triggers a
// "voice.error" reply and the handler is NOT called.
func TestWatch_VoiceDM_TranscriberError(t *testing.T) {
	const ownerID int64 = 12345

	tr := &mockTranscriber{
		transcribeFn: func(_ context.Context, _ model.AudioInput) (string, error) {
			return "", errors.New("stt unavailable")
		},
	}
	cfg := TelegramChannelConfig{
		OwnerIDs:       []string{"12345"},
		Transcriber:    tr,
		Localizer:      makeTestLocalizer(t, "ru"),
		DownloadFileFn: func(_ context.Context, _ string) ([]byte, error) { return []byte("data"), nil },
	}
	tgMsg := makeVoiceMessage(1, makePrivateChat(ownerID), &tgbotapi.User{ID: ownerID}, "file123", 10, 1700000000)

	received, sendCalled := watchVoice(t, cfg, tgMsg)

	if len(received) != 0 {
		t.Errorf("handler must NOT be called on transcription error, got %d messages", len(received))
	}
	if len(sendCalled) != 1 {
		t.Fatalf("bot.Send must be called once with error reply, got %d", len(sendCalled))
	}
	msg, ok := sendCalled[0].(tgbotapi.MessageConfig)
	if !ok {
		t.Fatal("sent message is not a MessageConfig")
	}
	want := "Не удалось распознать голос, попробуй ещё раз"
	if msg.Text != want {
		t.Errorf("reply text: want %q, got %q", want, msg.Text)
	}
}

// TestWatch_VoiceDM_NilTranscriber verifies that voice DM with nil Transcriber gets a
// "voice.disabled" reply and the handler is NOT called.
func TestWatch_VoiceDM_NilTranscriber(t *testing.T) {
	const ownerID int64 = 12345

	cfg := TelegramChannelConfig{
		OwnerIDs:    []string{"12345"},
		Transcriber: nil,
		Localizer:   makeTestLocalizer(t, "ru"),
	}
	tgMsg := makeVoiceMessage(1, makePrivateChat(ownerID), &tgbotapi.User{ID: ownerID}, "file123", 10, 0)

	received, sendCalled := watchVoice(t, cfg, tgMsg)

	if len(received) != 0 {
		t.Errorf("handler must NOT be called when transcriber is nil, got %d messages", len(received))
	}
	if len(sendCalled) != 1 {
		t.Fatalf("bot.Send must be called once with disabled reply, got %d", len(sendCalled))
	}
	msg, ok := sendCalled[0].(tgbotapi.MessageConfig)
	if !ok {
		t.Fatal("sent message is not a MessageConfig")
	}
	want := "Распознавание голоса не настроено"
	if msg.Text != want {
		t.Errorf("reply text: want %q, got %q", want, msg.Text)
	}
}

// TestWatch_VoiceDM_TooLong verifies that a voice message exceeding MaxVoiceDuration gets
// a "voice.too_long" reply, and the Transcriber is NOT called.
func TestWatch_VoiceDM_TooLong(t *testing.T) {
	const ownerID int64 = 12345

	tr := &mockTranscriber{}
	cfg := TelegramChannelConfig{
		OwnerIDs:         []string{"12345"},
		Transcriber:      tr,
		MaxVoiceDuration: 30 * time.Second,
		Localizer:        makeTestLocalizer(t, "ru"),
		DownloadFileFn:   func(_ context.Context, _ string) ([]byte, error) { return []byte("data"), nil },
	}
	// 60 seconds > 30 second limit
	tgMsg := makeVoiceMessage(1, makePrivateChat(ownerID), &tgbotapi.User{ID: ownerID}, "file123", 60, 0)

	received, sendCalled := watchVoice(t, cfg, tgMsg)

	if len(received) != 0 {
		t.Errorf("handler must NOT be called for too-long voice, got %d messages", len(received))
	}

	tr.mu.Lock()
	calls := tr.calls
	tr.mu.Unlock()
	if len(calls) != 0 {
		t.Errorf("Transcribe must NOT be called for too-long voice, got %d calls", len(calls))
	}

	if len(sendCalled) != 1 {
		t.Fatalf("bot.Send must be called once with too_long reply, got %d", len(sendCalled))
	}
	msg, ok := sendCalled[0].(tgbotapi.MessageConfig)
	if !ok {
		t.Fatal("sent message is not a MessageConfig")
	}
	want := "Голосовое слишком длинное (лимит 30s)"
	if msg.Text != want {
		t.Errorf("reply text: want %q, got %q", want, msg.Text)
	}
}

// TestWatch_VoiceDM_TooLong_NoTranscriber verifies that a voice message exceeding
// MaxVoiceDuration gets "voice.too_long" even when Transcriber is nil.
// Duration check must happen before the nil-transcriber check.
func TestWatch_VoiceDM_TooLong_NoTranscriber(t *testing.T) {
	const ownerID int64 = 12345

	cfg := TelegramChannelConfig{
		OwnerIDs:         []string{"12345"},
		Transcriber:      nil,
		MaxVoiceDuration: 30 * time.Second,
		Localizer:        makeTestLocalizer(t, "ru"),
		DownloadFileFn:   func(_ context.Context, _ string) ([]byte, error) { return []byte("data"), nil },
	}
	// 60 seconds > 30 second limit
	tgMsg := makeVoiceMessage(1, makePrivateChat(ownerID), &tgbotapi.User{ID: ownerID}, "file123", 60, 0)

	received, sendCalled := watchVoice(t, cfg, tgMsg)

	if len(received) != 0 {
		t.Errorf("handler must NOT be called for too-long voice, got %d messages", len(received))
	}

	if len(sendCalled) != 1 {
		t.Fatalf("bot.Send must be called once with too_long reply, got %d", len(sendCalled))
	}
	msg, ok := sendCalled[0].(tgbotapi.MessageConfig)
	if !ok {
		t.Fatal("sent message is not a MessageConfig")
	}
	want := "Голосовое слишком длинное (лимит 30s)"
	if msg.Text != want {
		t.Errorf("reply text: want %q, got %q", want, msg.Text)
	}
}

// TestWatch_VoiceDM_WhitespaceResult verifies that a transcription returning only
// whitespace triggers "voice.empty" and the handler is NOT called.
func TestWatch_VoiceDM_WhitespaceResult(t *testing.T) {
	const ownerID int64 = 12345

	tr := &mockTranscriber{
		transcribeFn: func(_ context.Context, _ model.AudioInput) (string, error) {
			return "   \t\n  ", nil
		},
	}
	cfg := TelegramChannelConfig{
		OwnerIDs:       []string{"12345"},
		Transcriber:    tr,
		Localizer:      makeTestLocalizer(t, "ru"),
		DownloadFileFn: func(_ context.Context, _ string) ([]byte, error) { return []byte("data"), nil },
	}
	tgMsg := makeVoiceMessage(1, makePrivateChat(ownerID), &tgbotapi.User{ID: ownerID}, "file123", 5, 0)

	received, sendCalled := watchVoice(t, cfg, tgMsg)

	if len(received) != 0 {
		t.Errorf("handler must NOT be called for empty transcription, got %d messages", len(received))
	}
	if len(sendCalled) != 1 {
		t.Fatalf("bot.Send must be called once with empty reply, got %d", len(sendCalled))
	}
	msg, ok := sendCalled[0].(tgbotapi.MessageConfig)
	if !ok {
		t.Fatal("sent message is not a MessageConfig")
	}
	want := "Не разобрал, повтори, пожалуйста"
	if msg.Text != want {
		t.Errorf("reply text: want %q, got %q", want, msg.Text)
	}
}

// TestWatch_VoiceGroupMessage_TranscriptionNotLaunched verifies that a voice message
// in a group chat does NOT trigger transcription.
func TestWatch_VoiceGroupMessage_TranscriptionNotLaunched(t *testing.T) {
	const groupID int64 = -100

	tr := &mockTranscriber{
		transcribeFn: func(_ context.Context, _ model.AudioInput) (string, error) {
			return "text", nil
		},
	}
	cfg := TelegramChannelConfig{
		Transcriber:    tr,
		Localizer:      makeTestLocalizer(t, "ru"),
		DownloadFileFn: func(_ context.Context, _ string) ([]byte, error) { return []byte("data"), nil },
	}

	user := makeUser(1, "u", "", "")
	chat := makeChat(groupID, "Группа")
	groupVoiceMsg := &tgbotapi.Message{
		MessageID: 1,
		Chat:      chat,
		From:      user,
		Date:      1000,
		Voice:     &tgbotapi.Voice{FileID: "file123", Duration: 10},
	}

	bot := newMockBot(0)
	state := newMockStateStore()
	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel
	bot.setUpdates([]rawUpdate{makeRawUpdate(1, groupVoiceMsg)})

	w := newTelegramChannel(bot, cfg, state, nil, nil, HistoryConfig{})
	_ = w.Watch(ctx, func(_ context.Context, _ model.Message) error { return nil })

	tr.mu.Lock()
	calls := tr.calls
	tr.mu.Unlock()

	if len(calls) != 0 {
		t.Errorf("Transcribe must NOT be called for group voice message, got %d calls", len(calls))
	}
}

// TestWatch_VoiceDM_DownloadError verifies that a download failure triggers a
// "voice.error" reply and the handler is NOT called.
func TestWatch_VoiceDM_DownloadError(t *testing.T) {
	const ownerID int64 = 12345

	tr := &mockTranscriber{}
	cfg := TelegramChannelConfig{
		OwnerIDs:       []string{"12345"},
		Transcriber:    tr,
		Localizer:      makeTestLocalizer(t, "ru"),
		DownloadFileFn: func(_ context.Context, _ string) ([]byte, error) { return nil, errors.New("network error") },
	}
	tgMsg := makeVoiceMessage(1, makePrivateChat(ownerID), &tgbotapi.User{ID: ownerID}, "file123", 10, 1700000000)

	received, sendCalled := watchVoice(t, cfg, tgMsg)

	if len(received) != 0 {
		t.Errorf("handler must NOT be called on download error, got %d messages", len(received))
	}
	if len(sendCalled) != 1 {
		t.Fatalf("bot.Send must be called once with error reply, got %d", len(sendCalled))
	}
	msg, ok := sendCalled[0].(tgbotapi.MessageConfig)
	if !ok {
		t.Fatal("sent message is not a MessageConfig")
	}
	want := "Не удалось распознать голос, попробуй ещё раз"
	if msg.Text != want {
		t.Errorf("reply text: want %q, got %q", want, msg.Text)
	}

	tr.mu.Lock()
	calls := tr.calls
	tr.mu.Unlock()
	if len(calls) != 0 {
		t.Errorf("Transcribe must NOT be called on download error, got %d calls", len(calls))
	}
}

// TestWatch_VoiceDM_AudioMessage verifies that a Telegram Audio attachment in a DM
// is transcribed the same way as a Voice message.
func TestWatch_VoiceDM_AudioMessage(t *testing.T) {
	const ownerID int64 = 12345
	audio := []byte("mp3-bytes")

	tr := &mockTranscriber{
		transcribeFn: func(_ context.Context, _ model.AudioInput) (string, error) {
			return "текст из аудио", nil
		},
	}
	cfg := TelegramChannelConfig{
		OwnerIDs:       []string{"12345"},
		Transcriber:    tr,
		Localizer:      makeTestLocalizer(t, "ru"),
		DownloadFileFn: func(_ context.Context, _ string) ([]byte, error) { return audio, nil },
	}
	tgMsg := &tgbotapi.Message{
		MessageID: 1,
		Chat:      makePrivateChat(ownerID),
		From:      &tgbotapi.User{ID: ownerID},
		Date:      1700000000,
		Audio:     &tgbotapi.Audio{FileID: "audio123", Duration: 15, MimeType: "audio/mpeg"},
	}

	received, sendCalled := watchVoice(t, cfg, tgMsg)

	if len(received) != 1 {
		t.Fatalf("handler must be called once, got %d", len(received))
	}
	if received[0].Text != "текст из аудио" {
		t.Errorf("Text: want %q, got %q", "текст из аудио", received[0].Text)
	}
	if len(sendCalled) != 0 {
		t.Errorf("bot.Send must not be called on success, got %d calls", len(sendCalled))
	}

	tr.mu.Lock()
	calls := tr.calls
	tr.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("Transcribe must be called once, got %d", len(calls))
	}
	if string(calls[0].Data) != string(audio) {
		t.Errorf("Transcribe received wrong data")
	}
}

// mockForwardBufferFull implements model.ForwardBuffer for use in TelegramChannel tests.
type mockForwardBufferFull struct {
	mu          sync.Mutex
	appendCalls []model.ForwardEntry
	appendErr   error
	lastTime    time.Time
	hasLast     bool
	lastErr     error
}

func newMockForwardBuffer() *mockForwardBufferFull {
	return &mockForwardBufferFull{}
}

func (m *mockForwardBufferFull) Append(_ context.Context, e model.ForwardEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.appendErr != nil {
		return m.appendErr
	}
	m.appendCalls = append(m.appendCalls, e)
	return nil
}

func (m *mockForwardBufferFull) LastReceivedAt(_ context.Context, _ string) (time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastTime, m.hasLast, m.lastErr
}

func (m *mockForwardBufferFull) LoadBatch(_ context.Context, _ string) ([]model.ForwardEntry, error) {
	return nil, nil
}

func (m *mockForwardBufferFull) DeleteEntriesTx(_ context.Context, _ *sql.Tx, _ string, _ []string) error {
	return nil
}

func (m *mockForwardBufferFull) ListPendingChats(_ context.Context, _ time.Time) ([]string, error) {
	return nil, nil
}

// TestHandlePrivateDM_ForwardFromOwner_AccumulatedAsSelf verifies that a forwarded
// message originally sent by the owner is accumulated with IsSelfAuthored=true.
func TestHandlePrivateDM_ForwardFromOwner_AccumulatedAsSelf(t *testing.T) {
	const ownerID int64 = 12345

	bot := newMockBot(0)
	state := newMockStateStore()
	buf := newMockForwardBuffer()

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel

	fwdMsg := &tgbotapi.Message{
		MessageID:   10,
		Chat:        makePrivateChat(ownerID),
		From:        makeUser(ownerID, "owner", "Owner", ""),
		Text:        "Завтра пришлю слайды",
		Date:        1700001000,
		ForwardFrom: makeUser(ownerID, "owner", "Owner", ""),
		ForwardDate: 1700000000,
	}
	bot.setUpdates([]rawUpdate{makeRawUpdate(1, fwdMsg)})

	sigCh := make(chan model.ForwardDispatchSignal, 1)
	w := newTelegramChannel(bot, TelegramChannelConfig{
		OwnerIDs:      []string{strconv.FormatInt(ownerID, 10)},
		ForwardBuffer: buf,
		ForwardSignal: sigCh,
	}, state, nil, nil, HistoryConfig{})

	var handlerCalled bool
	_ = w.Watch(ctx, func(_ context.Context, _ model.Message) error {
		handlerCalled = true
		return nil
	})

	if handlerCalled {
		t.Error("handler must not be called for forward message")
	}

	buf.mu.Lock()
	appended := buf.appendCalls
	buf.mu.Unlock()

	if len(appended) != 1 {
		t.Fatalf("Append must be called once, got %d", len(appended))
	}
	e := appended[0]
	if e.ChatID != strconv.FormatInt(ownerID, 10) {
		t.Errorf("ChatID: want %q, got %q", strconv.FormatInt(ownerID, 10), e.ChatID)
	}
	if e.MessageID != "10" {
		t.Errorf("MessageID: want %q, got %q", "10", e.MessageID)
	}
	if !e.IsSelfAuthored {
		t.Error("IsSelfAuthored: want true for owner forward")
	}
	if e.ForwardFromID != strconv.FormatInt(ownerID, 10) {
		t.Errorf("ForwardFromID: want %q, got %q", strconv.FormatInt(ownerID, 10), e.ForwardFromID)
	}
	if e.Text != "Завтра пришлю слайды" {
		t.Errorf("Text: want %q, got %q", "Завтра пришлю слайды", e.Text)
	}
}

// TestHandlePrivateDM_ForwardFromCounterpart_AccumulatedAsNotSelf verifies that a forwarded
// message from the conversation partner is accumulated with IsSelfAuthored=false.
func TestHandlePrivateDM_ForwardFromCounterpart_AccumulatedAsNotSelf(t *testing.T) {
	const ownerID int64 = 12345
	const contactID int64 = 67890

	bot := newMockBot(0)
	state := newMockStateStore()
	buf := newMockForwardBuffer()

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel

	fwdMsg := &tgbotapi.Message{
		MessageID:   11,
		Chat:        makePrivateChat(ownerID),
		From:        makeUser(ownerID, "owner", "Owner", ""),
		Text:        "Жду слайды",
		Date:        1700001100,
		ForwardFrom: makeUser(contactID, "contact", "Ivan", ""),
		ForwardDate: 1700000100,
	}
	bot.setUpdates([]rawUpdate{makeRawUpdate(2, fwdMsg)})

	w := newTelegramChannel(bot, TelegramChannelConfig{
		OwnerIDs:      []string{strconv.FormatInt(ownerID, 10)},
		ForwardBuffer: buf,
	}, state, nil, nil, HistoryConfig{})

	_ = w.Watch(ctx, func(_ context.Context, _ model.Message) error { return nil })

	buf.mu.Lock()
	appended := buf.appendCalls
	buf.mu.Unlock()

	if len(appended) != 1 {
		t.Fatalf("Append must be called once, got %d", len(appended))
	}
	e := appended[0]
	if e.IsSelfAuthored {
		t.Error("IsSelfAuthored: want false for counterpart forward")
	}
	if e.ForwardFromName != "contact" {
		t.Errorf("ForwardFromName: want %q, got %q", "contact", e.ForwardFromName)
	}
}

// TestHandlePrivateDM_HiddenPrivacyForward_AccumulatedWithNameOnly verifies that a forward
// from a user with hidden privacy is accumulated using ForwardSenderName with no ID.
func TestHandlePrivateDM_HiddenPrivacyForward_AccumulatedWithNameOnly(t *testing.T) {
	const ownerID int64 = 12345

	bot := newMockBot(0)
	state := newMockStateStore()
	buf := newMockForwardBuffer()

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel

	fwdMsg := &tgbotapi.Message{
		MessageID:         12,
		Chat:              makePrivateChat(ownerID),
		From:              makeUser(ownerID, "owner", "Owner", ""),
		Text:              "Скрытый отправитель",
		Date:              1700001200,
		ForwardSenderName: "Hidden User",
		ForwardDate:       1700000200,
	}
	bot.setUpdates([]rawUpdate{makeRawUpdate(3, fwdMsg)})

	w := newTelegramChannel(bot, TelegramChannelConfig{
		OwnerIDs:      []string{strconv.FormatInt(ownerID, 10)},
		ForwardBuffer: buf,
	}, state, nil, nil, HistoryConfig{})

	_ = w.Watch(ctx, func(_ context.Context, _ model.Message) error { return nil })

	buf.mu.Lock()
	appended := buf.appendCalls
	buf.mu.Unlock()

	if len(appended) != 1 {
		t.Fatalf("Append must be called once, got %d", len(appended))
	}
	e := appended[0]
	if e.ForwardFromID != "" {
		t.Errorf("ForwardFromID: want empty for hidden privacy, got %q", e.ForwardFromID)
	}
	if e.ForwardFromName != "Hidden User" {
		t.Errorf("ForwardFromName: want %q, got %q", "Hidden User", e.ForwardFromName)
	}
	if e.IsSelfAuthored {
		t.Error("IsSelfAuthored: want false when ID is unknown")
	}
}

// TestHandlePrivateDM_ForwardFromChannel_NotAccumulated verifies that a message forwarded
// from a channel is NOT accumulated and goes through the normal DM flow.
func TestHandlePrivateDM_ForwardFromChannel_NotAccumulated(t *testing.T) {
	const ownerID int64 = 12345

	bot := newMockBot(0)
	state := newMockStateStore()
	buf := newMockForwardBuffer()

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel

	channel := &tgbotapi.Chat{ID: -100999, Type: "channel", Title: "My Channel"}
	fwdMsg := &tgbotapi.Message{
		MessageID:       13,
		Chat:            makePrivateChat(ownerID),
		From:            makeUser(ownerID, "owner", "Owner", ""),
		Text:            "Пост из канала",
		Date:            1700001300,
		ForwardFromChat: channel,
		ForwardDate:     1700000300,
	}
	bot.setUpdates([]rawUpdate{makeRawUpdate(4, fwdMsg)})

	var handlerCalled bool
	w := newTelegramChannel(bot, TelegramChannelConfig{
		OwnerIDs:      []string{strconv.FormatInt(ownerID, 10)},
		ForwardBuffer: buf,
	}, state, nil, nil, HistoryConfig{})

	_ = w.Watch(ctx, func(_ context.Context, _ model.Message) error {
		handlerCalled = true
		return nil
	})

	buf.mu.Lock()
	appended := buf.appendCalls
	buf.mu.Unlock()

	if len(appended) != 0 {
		t.Errorf("Append must NOT be called for channel forward, got %d calls", len(appended))
	}
	if !handlerCalled {
		t.Error("handler must be called for channel-forward DM (treated as regular DM)")
	}
}

// TestHandlePrivateDM_RegularDM_EmptyBuffer_NormalFlow verifies that a regular DM
// with an empty buffer goes through the normal pipeline flow.
func TestHandlePrivateDM_RegularDM_EmptyBuffer_NormalFlow(t *testing.T) {
	const ownerID int64 = 12345

	bot := newMockBot(0)
	state := newMockStateStore()
	buf := newMockForwardBuffer() // hasLast = false by default

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel

	dm := makeMessage(20, makePrivateChat(ownerID), makeUser(ownerID, "owner", "", ""), "задача из головы", 1700002000)
	bot.setUpdates([]rawUpdate{makeRawUpdate(5, dm)})

	sigCh := make(chan model.ForwardDispatchSignal, 1)
	var handlerMessages []model.Message
	w := newTelegramChannel(bot, TelegramChannelConfig{
		OwnerIDs:      []string{strconv.FormatInt(ownerID, 10)},
		ForwardBuffer: buf,
		ForwardSignal: sigCh,
	}, state, nil, nil, HistoryConfig{})

	_ = w.Watch(ctx, func(_ context.Context, msg model.Message) error {
		handlerMessages = append(handlerMessages, msg)
		return nil
	})

	if len(handlerMessages) != 1 {
		t.Fatalf("handler must be called once for regular DM, got %d", len(handlerMessages))
	}
	if handlerMessages[0].Text != "задача из головы" {
		t.Errorf("Text: want %q, got %q", "задача из головы", handlerMessages[0].Text)
	}
	if len(sigCh) != 0 {
		t.Error("forward signal must NOT be sent when buffer is empty")
	}
}

// TestHandlePrivateDM_RegularDM_NonEmptyBuffer_SendsTriggerSignal verifies that a regular
// DM sent while the buffer is non-empty triggers a ForwardDispatchSignal instead of being
// forwarded to the pipeline handler.
func TestHandlePrivateDM_RegularDM_NonEmptyBuffer_SendsTriggerSignal(t *testing.T) {
	const ownerID int64 = 12345

	bot := newMockBot(0)
	state := newMockStateStore()
	buf := newMockForwardBuffer()
	buf.hasLast = true
	buf.lastTime = time.Unix(1700000000, 0)

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel

	dm := makeMessage(21, makePrivateChat(ownerID), makeUser(ownerID, "owner", "", ""), "обработай", 1700003000)
	bot.setUpdates([]rawUpdate{makeRawUpdate(6, dm)})

	sigCh := make(chan model.ForwardDispatchSignal, 1)
	var handlerCalled bool
	w := newTelegramChannel(bot, TelegramChannelConfig{
		OwnerIDs:      []string{strconv.FormatInt(ownerID, 10)},
		ForwardBuffer: buf,
		ForwardSignal: sigCh,
	}, state, nil, nil, HistoryConfig{})

	_ = w.Watch(ctx, func(_ context.Context, _ model.Message) error {
		handlerCalled = true
		return nil
	})

	if handlerCalled {
		t.Error("handler must NOT be called when trigger signal is sent")
	}
	if len(sigCh) != 1 {
		t.Fatalf("exactly one forward signal must be sent, got %d", len(sigCh))
	}
	sig := <-sigCh
	if sig.ChatID != strconv.FormatInt(ownerID, 10) {
		t.Errorf("signal ChatID: want %q, got %q", strconv.FormatInt(ownerID, 10), sig.ChatID)
	}
	if sig.TriggerText != "обработай" {
		t.Errorf("signal TriggerText: want %q, got %q", "обработай", sig.TriggerText)
	}
	if sig.TriggerID != "21" {
		t.Errorf("signal TriggerID: want %q, got %q", "21", sig.TriggerID)
	}
	if sig.Source != "user-trigger" {
		t.Errorf("signal Source: want %q, got %q", "user-trigger", sig.Source)
	}
	if sig.ReplyFn == nil {
		t.Error("signal ReplyFn must not be nil")
	}
	if sig.DeleteFn != nil {
		t.Error("signal DeleteFn must be nil when DeleteForwardsAfterExtract=false")
	}
}

// TestHandlePrivateDM_DeleteForwardsEnabled_SignalHasDeleteFn verifies that when
// DeleteForwardsAfterExtract is true the dispatch signal carries a non-nil DeleteFn.
func TestHandlePrivateDM_DeleteForwardsEnabled_SignalHasDeleteFn(t *testing.T) {
	const ownerID int64 = 12345

	bot := newMockBot(0)
	state := newMockStateStore()
	buf := newMockForwardBuffer()
	buf.hasLast = true
	buf.lastTime = time.Unix(1700000000, 0)

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel

	dm := makeMessage(30, makePrivateChat(ownerID), makeUser(ownerID, "owner", "", ""), "готово", 1700004000)
	bot.setUpdates([]rawUpdate{makeRawUpdate(7, dm)})

	sigCh := make(chan model.ForwardDispatchSignal, 1)
	w := newTelegramChannel(bot, TelegramChannelConfig{
		OwnerIDs:                   []string{strconv.FormatInt(ownerID, 10)},
		ForwardBuffer:              buf,
		ForwardSignal:              sigCh,
		DeleteForwardsAfterExtract: true,
	}, state, nil, nil, HistoryConfig{})

	_ = w.Watch(ctx, func(_ context.Context, _ model.Message) error { return nil })

	if len(sigCh) != 1 {
		t.Fatalf("one signal expected, got %d", len(sigCh))
	}
	sig := <-sigCh
	if sig.DeleteFn == nil {
		t.Error("signal DeleteFn must not be nil when DeleteForwardsAfterExtract=true")
	}
}

// TestBuildDeleteFn_CallsDeleteMessagesAPI verifies that BuildDeleteFn returns a function
// that calls the Telegram deleteMessages endpoint.
func TestBuildDeleteFn_CallsDeleteMessagesAPI(t *testing.T) {
	const chatID int64 = 12345

	bot := newMockBot(0)
	w := newTelegramChannel(bot, TelegramChannelConfig{}, newMockStateStore(), nil, nil, HistoryConfig{})

	deleteFn := w.BuildDeleteFn(chatID)
	if err := deleteFn(context.Background(), []string{"1", "2", "3"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	bot.mu.Lock()
	calls := bot.makeReqCalls
	bot.mu.Unlock()

	if len(calls) == 0 {
		t.Fatal("MakeRequest must be called at least once")
	}
	if calls[0] != "deleteMessages" {
		t.Errorf("first call: want %q, got %q", "deleteMessages", calls[0])
	}
}

// TestWatch_BackoffOnError verifies that Watch exponentially backs off on
// consecutive getUpdates errors: 1s → 2s → 4s.
func TestWatch_BackoffOnError(t *testing.T) {
	bot := newMockBot(0)
	state := newMockStateStore()

	var mu sync.Mutex
	var sleptDurations []time.Duration

	ctx, cancel := context.WithCancel(context.Background())

	callCount := 0
	bot.makeReqResponder = func(endpoint string, _ tgbotapi.Params) (*tgbotapi.APIResponse, error) {
		if endpoint != "getUpdates" {
			return &tgbotapi.APIResponse{Ok: true}, nil
		}
		mu.Lock()
		callCount++
		n := callCount
		mu.Unlock()
		switch n {
		case 1, 2, 3:
			return nil, errors.New("network error")
		default:
			cancel()
			return &tgbotapi.APIResponse{Ok: true, Result: json.RawMessage("[]")}, nil
		}
	}

	w := newTelegramChannel(bot, TelegramChannelConfig{OnJoin: "monitor"}, state, nil, nil, HistoryConfig{})
	w.sleeper = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		sleptDurations = append(sleptDurations, d)
		mu.Unlock()
		return nil
	}

	_ = w.Watch(ctx, func(_ context.Context, _ model.Message) error { return nil })

	mu.Lock()
	got := append([]time.Duration(nil), sleptDurations...)
	mu.Unlock()

	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if len(got) != len(want) {
		t.Fatalf("sleep count: want %d, got %d (%v)", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("sleep[%d]: want %v, got %v", i, w, got[i])
		}
	}
}

// TestWatch_RetryAfterRespected verifies that Watch uses RetryAfter from a
// Telegram API error instead of the exponential backoff duration.
func TestWatch_RetryAfterRespected(t *testing.T) {
	bot := newMockBot(0)
	state := newMockStateStore()

	var mu sync.Mutex
	var sleptDurations []time.Duration

	ctx, cancel := context.WithCancel(context.Background())

	callCount := 0
	bot.makeReqResponder = func(endpoint string, _ tgbotapi.Params) (*tgbotapi.APIResponse, error) {
		if endpoint != "getUpdates" {
			return &tgbotapi.APIResponse{Ok: true}, nil
		}
		mu.Lock()
		callCount++
		n := callCount
		mu.Unlock()
		if n == 1 {
			return nil, &tgbotapi.Error{
				ResponseParameters: tgbotapi.ResponseParameters{RetryAfter: 7},
			}
		}
		cancel()
		return &tgbotapi.APIResponse{Ok: true, Result: json.RawMessage("[]")}, nil
	}

	w := newTelegramChannel(bot, TelegramChannelConfig{OnJoin: "monitor"}, state, nil, nil, HistoryConfig{})
	w.sleeper = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		sleptDurations = append(sleptDurations, d)
		mu.Unlock()
		return nil
	}

	_ = w.Watch(ctx, func(_ context.Context, _ model.Message) error { return nil })

	mu.Lock()
	got := append([]time.Duration(nil), sleptDurations...)
	mu.Unlock()

	if len(got) != 1 {
		t.Fatalf("sleep count: want 1, got %d (%v)", len(got), got)
	}
	if got[0] != 7*time.Second {
		t.Errorf("sleep duration: want 7s, got %v", got[0])
	}
}

// TestWatch_BackoffResetsOnSuccess verifies that a successful getUpdates resets
// the backoff to 1s so the next error starts from the beginning.
func TestWatch_BackoffResetsOnSuccess(t *testing.T) {
	bot := newMockBot(0)
	state := newMockStateStore()

	var mu sync.Mutex
	var sleptDurations []time.Duration

	ctx, cancel := context.WithCancel(context.Background())

	callCount := 0
	bot.makeReqResponder = func(endpoint string, _ tgbotapi.Params) (*tgbotapi.APIResponse, error) {
		if endpoint != "getUpdates" {
			return &tgbotapi.APIResponse{Ok: true}, nil
		}
		mu.Lock()
		callCount++
		n := callCount
		mu.Unlock()
		switch n {
		case 1:
			return nil, errors.New("first error")
		case 2:
			return &tgbotapi.APIResponse{Ok: true, Result: json.RawMessage("[]")}, nil
		case 3:
			return nil, errors.New("second error")
		default:
			cancel()
			return &tgbotapi.APIResponse{Ok: true, Result: json.RawMessage("[]")}, nil
		}
	}

	w := newTelegramChannel(bot, TelegramChannelConfig{OnJoin: "monitor"}, state, nil, nil, HistoryConfig{})
	w.sleeper = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		sleptDurations = append(sleptDurations, d)
		mu.Unlock()
		return nil
	}

	_ = w.Watch(ctx, func(_ context.Context, _ model.Message) error { return nil })

	mu.Lock()
	got := append([]time.Duration(nil), sleptDurations...)
	mu.Unlock()

	want := []time.Duration{time.Second, time.Second}
	if len(got) != len(want) {
		t.Fatalf("sleep count: want %d, got %d (%v)", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("sleep[%d]: want %v, got %v", i, w, got[i])
		}
	}
}

// TestWatch_BatchCursorSavedOnce verifies that for a batch of 5 updates where all
// processRawUpdate calls return true, SaveCursor is called exactly once with the
// update_id of the last update.
func TestWatch_BatchCursorSavedOnce(t *testing.T) {
	const groupID int64 = -100
	bot := newMockBot(0)
	state := newMockStateStore()

	chat := makeChat(groupID, "Чат")
	user := makeUser(1, "u", "", "")

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel

	// All 5 updates in a single batch.
	bot.setUpdates([]rawUpdate{
		makeRawUpdate(1, makeMessage(1, chat, user, "раз", 1000)),
		makeRawUpdate(2, makeMessage(2, chat, user, "два", 2000)),
		makeRawUpdate(3, makeMessage(3, chat, user, "три", 3000)),
		makeRawUpdate(4, makeMessage(4, chat, user, "четыре", 4000)),
		makeRawUpdate(5, makeMessage(5, chat, user, "пять", 5000)),
	})

	w := newTelegramChannel(bot, TelegramChannelConfig{OnJoin: "monitor"}, state, nil, nil, HistoryConfig{})
	_ = w.Watch(ctx, func(_ context.Context, _ model.Message) error { return nil })

	state.mu.Lock()
	calls := state.saveCursorCalls
	cursor := state.cursors[telegramStateKey]
	state.mu.Unlock()

	if calls != 1 {
		t.Errorf("SaveCursor must be called 1 time, got %d", calls)
	}
	if cursor == nil {
		t.Fatal("cursor must be saved")
	}
	if cursor.MessageID != "5" {
		t.Errorf("cursor: want %q, got %q", "5", cursor.MessageID)
	}
}

// TestWatch_BatchCursorStopsAtFirstFalse verifies that when the third update in a batch
// causes processRawUpdate to return false (forward buffer append error), the cursor is
// saved at the second update and further updates in the batch are not processed.
func TestWatch_BatchCursorStopsAtFirstFalse(t *testing.T) {
	const ownerID int64 = 12345
	const groupID int64 = -100

	bot := newMockBot(0)
	state := newMockStateStore()
	buf := newMockForwardBuffer()
	buf.appendErr = errors.New("db error")

	ctx, cancel := context.WithCancel(context.Background())
	bot.cancelFn = cancel

	groupChat := makeChat(groupID, "Чат")
	regularUser := makeUser(1, "u", "", "")
	ownerUser := makeUser(ownerID, "owner", "Owner", "")
	privateChat := makePrivateChat(ownerID)

	// Forwarded private message: triggers handlePrivateDMForBuffer, Append fails, returns false.
	fwdMsg := &tgbotapi.Message{
		MessageID:   3,
		Chat:        privateChat,
		From:        ownerUser,
		Text:        "Завтра пришлю слайды",
		Date:        1700001000,
		ForwardFrom: ownerUser,
		ForwardDate: 1700000000,
	}

	// All 5 updates in one batch.
	bot.setUpdates([]rawUpdate{
		makeRawUpdate(1, makeMessage(1, groupChat, regularUser, "раз", 1000)),
		makeRawUpdate(2, makeMessage(2, groupChat, regularUser, "два", 2000)),
		makeRawUpdate(3, fwdMsg),
		makeRawUpdate(4, makeMessage(4, groupChat, regularUser, "четыре", 4000)),
		makeRawUpdate(5, makeMessage(5, groupChat, regularUser, "пять", 5000)),
	})

	sigCh := make(chan model.ForwardDispatchSignal, 1)
	var handlerCount int
	w := newTelegramChannel(bot, TelegramChannelConfig{
		OwnerIDs:      []string{strconv.FormatInt(ownerID, 10)},
		ForwardBuffer: buf,
		ForwardSignal: sigCh,
	}, state, nil, nil, HistoryConfig{})

	_ = w.Watch(ctx, func(_ context.Context, _ model.Message) error {
		handlerCount++
		return nil
	})

	state.mu.Lock()
	calls := state.saveCursorCalls
	cursor := state.cursors[telegramStateKey]
	state.mu.Unlock()

	if calls != 1 {
		t.Errorf("SaveCursor must be called 1 time, got %d", calls)
	}
	if cursor == nil {
		t.Fatal("cursor must be saved")
	}
	// Cursor stops at update 2 (before the failing update 3).
	if cursor.MessageID != "2" {
		t.Errorf("cursor: want %q (last good update), got %q", "2", cursor.MessageID)
	}
	// Only updates 1 and 2 processed (update 3 failed, 4 and 5 not reached).
	if handlerCount != 2 {
		t.Errorf("handler call count: want 2, got %d", handlerCount)
	}
}

// A failed group handler must retain its update until durable processing succeeds.
func TestWatch_GroupHandlerFailureRetriesWithoutAcknowledging(t *testing.T) {
	bot := newMockBot(0)
	state := newMockStateStore()
	chat := makeChat(-100, "Group")
	user := makeUser(1, "user", "", "")
	update := func(id int) rawUpdate {
		return makeRawUpdate(id, makeMessage(id, chat, user, "Promise", 1000))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := 0
	bot.makeReqResponder = func(endpoint string, params tgbotapi.Params) (*tgbotapi.APIResponse, error) {
		if endpoint != "getUpdates" {
			t.Fatalf("unexpected request %s", endpoint)
		}
		requests++
		var batch []rawUpdate
		switch requests {
		case 1:
			batch = []rawUpdate{update(6), update(7), update(8)}
		case 2:
			if params["offset"] != "7" {
				t.Fatalf("failed update acknowledged by offset %s", params["offset"])
			}
			cursor, _ := state.GetCursor(ctx, telegramStateKey)
			if cursor == nil || cursor.MessageID != "6" {
				t.Fatalf("cursor advanced past failed update: %v", cursor)
			}
			batch = []rawUpdate{update(7), update(8)}
		default:
			if params["offset"] != "9" {
				t.Fatalf("successful retry not acknowledged: offset=%s", params["offset"])
			}
			cancel()
		}
		data, err := json.Marshal(batch)
		return &tgbotapi.APIResponse{Ok: true, Result: data}, err
	}
	w := newTelegramChannel(bot, TelegramChannelConfig{OnJoin: "monitor"}, state, nil, nil, HistoryConfig{})
	waits := 0
	w.sleeper = func(ctx context.Context, delay time.Duration) error {
		if delay <= 0 {
			t.Fatal("failed processing retried without delay")
		}
		waits++
		return ctx.Err()
	}
	attempts := make(map[string]int)
	completed := make(map[string]int)
	err := w.Watch(ctx, func(_ context.Context, msg model.Message) error {
		attempts[msg.ID]++
		if msg.ID == "7" && attempts[msg.ID] == 1 {
			return errors.New("database unavailable")
		}
		completed[msg.ID]++
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Watch error=%v", err)
	}
	if waits != 1 || attempts["7"] != 2 {
		t.Fatalf("waits=%d, failed update attempts=%d", waits, attempts["7"])
	}
	for _, id := range []string{"6", "7", "8"} {
		if completed[id] != 1 {
			t.Fatalf("update %s completed %d times", id, completed[id])
		}
	}
	cursor, _ := state.GetCursor(context.Background(), telegramStateKey)
	if cursor == nil || cursor.MessageID != "8" {
		t.Fatalf("final cursor=%v", cursor)
	}
}

func TestWatch_DMHandlerErrorStillSavesCursor(t *testing.T) {
	bot := newMockBot(0)
	state := newMockStateStore()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bot.cancelFn = cancel
	bot.setUpdates([]rawUpdate{makeRawUpdate(7, makeMessage(1, makePrivateChat(12345), makeUser(12345, "owner", "", ""), "Hello", 1000))})
	w := newTelegramChannel(bot, TelegramChannelConfig{OwnerIDs: []string{"12345"}, OnJoin: "monitor"}, state, nil, nil, HistoryConfig{})
	_ = w.Watch(ctx, func(context.Context, model.Message) error { return errors.New("agent unavailable") })
	cursor, _ := state.GetCursor(context.Background(), telegramStateKey)
	if cursor == nil || cursor.MessageID != "7" {
		t.Fatalf("DM handler error changed cursor behavior: %v", cursor)
	}
}

// TestForwardSignal_BlocksUntilCapacity verifies that handlePrivateDMForBuffer blocks
// when the ForwardSignal channel is full, and succeeds once capacity is available.
func TestForwardSignal_BlocksUntilCapacity(t *testing.T) {
	const ownerID int64 = 12345

	buf := newMockForwardBuffer()
	buf.hasLast = true
	buf.lastTime = time.Unix(1700000000, 0)

	sigCh := make(chan model.ForwardDispatchSignal, 1)
	// Fill the channel so it has no capacity.
	sigCh <- model.ForwardDispatchSignal{}

	// Drain the channel after 50ms to free capacity.
	go func() {
		time.Sleep(50 * time.Millisecond)
		<-sigCh
	}()

	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{
		OwnerIDs:             []string{strconv.FormatInt(ownerID, 10)},
		ForwardBuffer:        buf,
		ForwardSignal:        sigCh,
		ForwardSignalTimeout: 500 * time.Millisecond,
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	m := makeMessage(21, makePrivateChat(ownerID), makeUser(ownerID, "owner", "", ""), "обработай", 1700003000)
	consumed, ok := w.handlePrivateDMForBuffer(context.Background(), m)

	if !consumed {
		t.Error("want consumed=true")
	}
	if !ok {
		t.Error("want ok=true — signal was eventually sent after capacity freed")
	}
	select {
	case sig := <-sigCh:
		if sig.ChatID != strconv.FormatInt(ownerID, 10) {
			t.Errorf("signal ChatID: want %q, got %q", strconv.FormatInt(ownerID, 10), sig.ChatID)
		}
	default:
		t.Error("signal should be in the channel after successful send")
	}
}

// TestForwardSignal_TimeoutLogsAndReturnsFalse verifies that when the ForwardSignal channel
// remains full until the timeout expires, handlePrivateDMForBuffer returns (true, false),
// signalling that the cursor must not advance and the trigger will be retried.
func TestForwardSignal_TimeoutLogsAndReturnsFalse(t *testing.T) {
	const ownerID int64 = 12345

	buf := newMockForwardBuffer()
	buf.hasLast = true
	buf.lastTime = time.Unix(1700000000, 0)

	sigCh := make(chan model.ForwardDispatchSignal, 1)
	// Fill the channel — nobody will drain it.
	sigCh <- model.ForwardDispatchSignal{}

	w := newTelegramChannel(newMockBot(0), TelegramChannelConfig{
		OwnerIDs:             []string{strconv.FormatInt(ownerID, 10)},
		ForwardBuffer:        buf,
		ForwardSignal:        sigCh,
		ForwardSignalTimeout: 30 * time.Millisecond,
	}, newMockStateStore(), nil, nil, HistoryConfig{})

	m := makeMessage(22, makePrivateChat(ownerID), makeUser(ownerID, "owner", "", ""), "обработай", 1700003000)
	consumed, ok := w.handlePrivateDMForBuffer(context.Background(), m)

	if !consumed {
		t.Error("want consumed=true — message was intercepted as a trigger")
	}
	if ok {
		t.Error("want ok=false — timeout means cursor must not advance, trigger retried on restart")
	}
}

// --- VoiceEnqueueFn tests ---

// TestWatch_VoiceDM_EnqueueFnCalled verifies that when VoiceEnqueueFn is set,
// a voice DM is routed to the enqueue function (not inline transcription),
// and the handler is NOT called during the Watch loop.
func TestWatch_VoiceDM_EnqueueFnCalled(t *testing.T) {
	const ownerID int64 = 12345

	tr := &mockTranscriber{}

	var enqueued []model.VoiceJob
	enqueueFn := func(_ context.Context, job model.VoiceJob) error {
		enqueued = append(enqueued, job)
		return nil
	}

	cfg := TelegramChannelConfig{
		OwnerIDs:       []string{"12345"},
		Transcriber:    tr,
		Localizer:      makeTestLocalizer(t, "ru"),
		DownloadFileFn: func(_ context.Context, _ string) ([]byte, error) { return []byte("data"), nil },
		VoiceEnqueueFn: enqueueFn,
	}
	tgMsg := makeVoiceMessage(5, makePrivateChat(ownerID), &tgbotapi.User{ID: ownerID}, "voice_file_1", 15, 1700001000)

	received, _ := watchVoice(t, cfg, tgMsg)

	if len(received) != 0 {
		t.Errorf("handler must NOT be called when VoiceEnqueueFn is set, got %d messages", len(received))
	}
	if len(enqueued) != 1 {
		t.Fatalf("VoiceEnqueueFn must be called once, got %d", len(enqueued))
	}
	if enqueued[0].ChatID != ownerID {
		t.Errorf("enqueued ChatID: want %d, got %d", ownerID, enqueued[0].ChatID)
	}
	if enqueued[0].MessageID != 5 {
		t.Errorf("enqueued MessageID: want 5, got %d", enqueued[0].MessageID)
	}
	if enqueued[0].Payload != "voice_file_1" {
		t.Errorf("enqueued Payload: want %q, got %q", "voice_file_1", enqueued[0].Payload)
	}
	if enqueued[0].MimeType != "audio/ogg" {
		t.Errorf("enqueued MimeType: want %q, got %q", "audio/ogg", enqueued[0].MimeType)
	}
	if enqueued[0].Duration != 15*time.Second {
		t.Errorf("enqueued Duration: want 15s, got %v", enqueued[0].Duration)
	}

	// Inline transcriber must NOT be called — transcription is async.
	tr.mu.Lock()
	calls := tr.calls
	tr.mu.Unlock()
	if len(calls) != 0 {
		t.Errorf("inline Transcriber must NOT be called when VoiceEnqueueFn is set, got %d calls", len(calls))
	}
}

// TestWatch_VoiceDM_EnqueueFn_TooLong verifies that the duration check fires synchronously
// even when VoiceEnqueueFn is set: the user gets an immediate reply and nothing is enqueued.
func TestWatch_VoiceDM_EnqueueFn_TooLong(t *testing.T) {
	const ownerID int64 = 12345

	tr := &mockTranscriber{}
	var enqueued []model.VoiceJob

	cfg := TelegramChannelConfig{
		OwnerIDs:         []string{"12345"},
		Transcriber:      tr,
		MaxVoiceDuration: 30 * time.Second,
		Localizer:        makeTestLocalizer(t, "ru"),
		DownloadFileFn:   func(_ context.Context, _ string) ([]byte, error) { return []byte("data"), nil },
		VoiceEnqueueFn: func(_ context.Context, job model.VoiceJob) error {
			enqueued = append(enqueued, job)
			return nil
		},
	}
	// 60s > 30s limit
	tgMsg := makeVoiceMessage(6, makePrivateChat(ownerID), &tgbotapi.User{ID: ownerID}, "voice_long", 60, 0)

	received, sendCalled := watchVoice(t, cfg, tgMsg)

	if len(received) != 0 {
		t.Errorf("handler must NOT be called for too-long voice, got %d messages", len(received))
	}
	if len(enqueued) != 0 {
		t.Errorf("VoiceEnqueueFn must NOT be called for too-long voice, got %d calls", len(enqueued))
	}
	if len(sendCalled) != 1 {
		t.Fatalf("bot.Send must be called once with too_long reply, got %d", len(sendCalled))
	}
	msg, ok := sendCalled[0].(tgbotapi.MessageConfig)
	if !ok {
		t.Fatal("sent message is not a MessageConfig")
	}
	want := "Голосовое слишком длинное (лимит 30s)"
	if msg.Text != want {
		t.Errorf("reply text: want %q, got %q", want, msg.Text)
	}
}

// TestWatch_VoiceDM_EnqueueFn_Error verifies that when VoiceEnqueueFn returns an
// error, the bot sends a "voice.error" reply and the pipeline handler is not called.
func TestWatch_VoiceDM_EnqueueFn_Error(t *testing.T) {
	const ownerID int64 = 12345

	tr := &mockTranscriber{}

	cfg := TelegramChannelConfig{
		OwnerIDs:    []string{"12345"},
		Transcriber: tr,
		Localizer:   makeTestLocalizer(t, "ru"),
		VoiceEnqueueFn: func(_ context.Context, _ model.VoiceJob) error {
			return errors.New("db full")
		},
	}
	tgMsg := makeVoiceMessage(8, makePrivateChat(ownerID), &tgbotapi.User{ID: ownerID}, "voice_err", 10, 0)

	received, sendCalled := watchVoice(t, cfg, tgMsg)

	if len(received) != 0 {
		t.Errorf("handler must NOT be called when enqueue fails, got %d messages", len(received))
	}
	if len(sendCalled) != 1 {
		t.Fatalf("bot.Send must be called once with error reply, got %d", len(sendCalled))
	}
	msg, ok := sendCalled[0].(tgbotapi.MessageConfig)
	if !ok {
		t.Fatal("sent message is not a MessageConfig")
	}
	want := "Не удалось распознать голос, попробуй ещё раз"
	if msg.Text != want {
		t.Errorf("reply text: want %q, got %q", want, msg.Text)
	}
}

// TestWatch_VoiceDM_EnqueueFn_NilTranscriber verifies that when Transcriber is nil
// the "voice.disabled" reply fires synchronously even with VoiceEnqueueFn set,
// and nothing is enqueued.
func TestWatch_VoiceDM_EnqueueFn_NilTranscriber(t *testing.T) {
	const ownerID int64 = 12345

	var enqueued []model.VoiceJob

	cfg := TelegramChannelConfig{
		OwnerIDs:    []string{"12345"},
		Transcriber: nil,
		Localizer:   makeTestLocalizer(t, "ru"),
		VoiceEnqueueFn: func(_ context.Context, job model.VoiceJob) error {
			enqueued = append(enqueued, job)
			return nil
		},
	}
	tgMsg := makeVoiceMessage(7, makePrivateChat(ownerID), &tgbotapi.User{ID: ownerID}, "voice_no_tr", 10, 0)

	received, sendCalled := watchVoice(t, cfg, tgMsg)

	if len(received) != 0 {
		t.Errorf("handler must NOT be called, got %d messages", len(received))
	}
	if len(enqueued) != 0 {
		t.Errorf("VoiceEnqueueFn must NOT be called when Transcriber is nil, got %d calls", len(enqueued))
	}
	if len(sendCalled) != 1 {
		t.Fatalf("bot.Send must be called once with disabled reply, got %d", len(sendCalled))
	}
	msg, ok := sendCalled[0].(tgbotapi.MessageConfig)
	if !ok {
		t.Fatal("sent message is not a MessageConfig")
	}
	want := "Распознавание голоса не настроено"
	if msg.Text != want {
		t.Errorf("reply text: want %q, got %q", want, msg.Text)
	}
}
