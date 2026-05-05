package channel

import (
	"context"
	"errors"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/transcribe"
)

// makeTelegramVoiceConsumer creates a TelegramChannel with a mock bot and wraps it
// in a TelegramVoiceConsumer. Returns the consumer, the mock bot, and a slice
// that collects messages passed to the handler.
func makeTelegramVoiceConsumer(t *testing.T, chatID int64) (*TelegramVoiceConsumer, *mockBot, *[]model.Message) {
	t.Helper()

	bot := newMockBot(0)
	state := newMockStateStore()
	ch := newTelegramChannel(bot, TelegramChannelConfig{
		Localizer: makeTestLocalizer(t, "ru"),
	}, state, nil, nil, HistoryConfig{})

	var received []model.Message
	handler := func(_ context.Context, msg model.Message) error {
		received = append(received, msg)
		return nil
	}

	return NewTelegramVoiceConsumer(ch, handler), bot, &received
}

func makeTestVoiceJob(chatID int64, msgID int) model.VoiceJob {
	return model.VoiceJob{
		ID:         1,
		SourceKind: "telegram",
		Payload:    "file123",
		ChatID:     chatID,
		MessageID:  msgID,
		MimeType:   "audio/ogg",
		Duration:   5 * time.Second,
		ReceivedAt: time.Unix(1700000000, 0),
	}
}

// TestTelegramVoiceConsumer_Consume_CallsHandlerWithCorrectMessage verifies that
// Consume builds a DM message with the transcript and passes it to the handler.
func TestTelegramVoiceConsumer_Consume_CallsHandlerWithCorrectMessage(t *testing.T) {
	const chatID int64 = 9001
	consumer, bot, received := makeTelegramVoiceConsumer(t, chatID)
	job := makeTestVoiceJob(chatID, 42)

	if err := consumer.Consume(context.Background(), job, "позвони маме"); err != nil {
		t.Fatalf("Consume returned error: %v", err)
	}

	if len(*received) != 1 {
		t.Fatalf("handler must be called once, got %d", len(*received))
	}
	msg := (*received)[0]
	if msg.Text != "позвони маме" {
		t.Errorf("Text: want %q, got %q", "позвони маме", msg.Text)
	}
	if msg.Kind != model.MessageKindDM {
		t.Errorf("Kind: want DM, got %q", msg.Kind)
	}
	if msg.Source.Kind != "telegram" {
		t.Errorf("Source.Kind: want %q, got %q", "telegram", msg.Source.Kind)
	}
	// no i18n reply should be sent on success
	bot.mu.Lock()
	sends := len(bot.sendCalled)
	bot.mu.Unlock()
	if sends != 0 {
		t.Errorf("bot.Send must not be called on success, got %d calls", sends)
	}
}

// TestTelegramVoiceConsumer_Fail_SendsVoiceError verifies that Fail sends a
// "voice.error" reply for non-empty-transcript errors.
func TestTelegramVoiceConsumer_Fail_SendsVoiceError(t *testing.T) {
	const chatID int64 = 9001
	consumer, bot, received := makeTelegramVoiceConsumer(t, chatID)
	job := makeTestVoiceJob(chatID, 42)

	consumer.Fail(context.Background(), job, errors.New("stt failure"))

	if len(*received) != 0 {
		t.Errorf("handler must NOT be called in Fail, got %d calls", len(*received))
	}
	bot.mu.Lock()
	sends := bot.sendCalled
	bot.mu.Unlock()
	if len(sends) != 1 {
		t.Fatalf("bot.Send must be called once (voice.error), got %d", len(sends))
	}
	msg, ok := sends[0].(tgbotapi.MessageConfig)
	if !ok {
		t.Fatalf("expected MessageConfig, got %T", sends[0])
	}
	if msg.ChatID != chatID {
		t.Errorf("ChatID: want %d, got %d", chatID, msg.ChatID)
	}
}

// TestTelegramVoiceConsumer_Fail_ErrEmptyTranscript verifies that Fail sends
// "voice.empty" when cause is ErrEmptyTranscript.
func TestTelegramVoiceConsumer_Fail_ErrEmptyTranscript(t *testing.T) {
	const chatID int64 = 9001
	consumer, bot, _ := makeTelegramVoiceConsumer(t, chatID)
	job := makeTestVoiceJob(chatID, 42)

	consumer.Fail(context.Background(), job, transcribe.ErrEmptyTranscript)

	bot.mu.Lock()
	sends := bot.sendCalled
	bot.mu.Unlock()
	if len(sends) != 1 {
		t.Fatalf("bot.Send must be called once (voice.empty), got %d", len(sends))
	}
	msg, ok := sends[0].(tgbotapi.MessageConfig)
	if !ok {
		t.Fatalf("expected MessageConfig, got %T", sends[0])
	}
	if msg.ChatID != chatID {
		t.Errorf("ChatID: want %d, got %d", chatID, msg.ChatID)
	}
}

// TestTelegramVoiceConsumer_Consume_ReplyFnWorks verifies that the ReplyFn
// built by BuildVoiceMessage sends via the bot.
func TestTelegramVoiceConsumer_Consume_ReplyFnWorks(t *testing.T) {
	const chatID int64 = 9001
	consumer, bot, _ := makeTelegramVoiceConsumer(t, chatID)
	job := makeTestVoiceJob(chatID, 42)

	var capturedMsg model.Message
	consumer.handler = func(_ context.Context, msg model.Message) error {
		capturedMsg = msg
		return nil
	}

	_ = consumer.Consume(context.Background(), job, "тест")

	if capturedMsg.ReplyFn == nil {
		t.Fatal("ReplyFn must not be nil")
	}
	if err := capturedMsg.ReplyFn(context.Background(), "ответ агента"); err != nil {
		t.Fatalf("ReplyFn returned error: %v", err)
	}

	bot.mu.Lock()
	sends := bot.sendCalled
	bot.mu.Unlock()
	if len(sends) != 1 {
		t.Fatalf("expected 1 bot.Send call from ReplyFn, got %d", len(sends))
	}
}
