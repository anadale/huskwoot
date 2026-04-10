package pairing

import (
	"context"
	"fmt"
	"log/slog"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// TelegramSender sends a magic-link DM to the instance owner via Telegram.
type TelegramSender interface {
	SendMagicLink(ctx context.Context, chatID int64, deviceName, magicURL string) error
}

type botAPISender struct {
	bot    *tgbotapi.BotAPI
	logger *slog.Logger
}

// NewTelegramSender creates a TelegramSender backed by the given BotAPI.
// If bot == nil, returns a noopSender that logs a warning on every call.
func NewTelegramSender(bot *tgbotapi.BotAPI, logger *slog.Logger) TelegramSender {
	if bot == nil {
		return &noopSender{logger: logger}
	}
	return &botAPISender{bot: bot, logger: logger}
}

func (s *botAPISender) SendMagicLink(_ context.Context, chatID int64, deviceName, magicURL string) error {
	text := fmt.Sprintf(
		"Подключить устройство «%s»? Подтвердите по ссылке: %s",
		deviceName, magicURL,
	)
	msg := tgbotapi.NewMessage(chatID, text)
	msg.DisableWebPagePreview = true
	if _, err := s.bot.Send(msg); err != nil {
		return fmt.Errorf("sending magic-link: %w", err)
	}
	return nil
}

type noopSender struct {
	logger *slog.Logger
}

func (s *noopSender) SendMagicLink(_ context.Context, _ int64, deviceName, magicURL string) error {
	s.logger.Warn("pairing: telegram sender not configured, magic-link not sent",
		"device_name", deviceName,
		"magic_url", magicURL,
	)
	return nil
}
