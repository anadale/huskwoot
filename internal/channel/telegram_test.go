package channel

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

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
	mu      sync.Mutex
	cursors map[string]*model.Cursor
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
