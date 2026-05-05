package channel

import (
	"context"
	"errors"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/transcribe"
)

// TelegramVoiceConsumer implements model.VoiceConsumer for Telegram DM voice messages.
// On success it builds a pipeline message via the channel and calls the handler.
// On failure it sends a localised i18n reply to the originating chat.
type TelegramVoiceConsumer struct {
	channel *TelegramChannel
	handler func(context.Context, model.Message) error
}

// NewTelegramVoiceConsumer creates a TelegramVoiceConsumer.
func NewTelegramVoiceConsumer(ch *TelegramChannel, handler func(context.Context, model.Message) error) *TelegramVoiceConsumer {
	return &TelegramVoiceConsumer{channel: ch, handler: handler}
}

// Consume builds a pipeline DM message from the transcribed voice job and calls the handler.
func (c *TelegramVoiceConsumer) Consume(ctx context.Context, job model.VoiceJob, transcript string) error {
	msg := c.channel.BuildVoiceMessage(job, transcript)
	return c.handler(ctx, msg)
}

// Fail sends a localised reply to the chat. Distinguishes ErrEmptyTranscript
// (voice.empty) from all other permanent failures (voice.error).
func (c *TelegramVoiceConsumer) Fail(ctx context.Context, job model.VoiceJob, cause error) {
	if errors.Is(cause, transcribe.ErrEmptyTranscript) {
		c.channel.ReplyVoiceEmpty(ctx, job.ChatID)
		return
	}
	c.channel.ReplyVoiceError(ctx, job.ChatID)
}
