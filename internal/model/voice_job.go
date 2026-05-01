package model

import "time"

// VoiceJob represents a single voice transcription task persisted in voice_queue.
type VoiceJob struct {
	// ID is the auto-increment voice_queue row identifier (0 before insertion).
	ID int64
	// ChatID is the Telegram chat ID from which the voice message was received.
	ChatID int64
	// MessageID is the Telegram message ID of the voice message.
	MessageID int
	// FileID is the Telegram file ID used to download the audio.
	FileID string
	// MimeType is the audio MIME type (e.g. "audio/ogg").
	MimeType string
	// Duration is the audio length.
	Duration time.Duration
	// ReceivedAt is the time the message was originally received by Telegram.
	ReceivedAt time.Time
	// Attempts is the number of processing attempts already made.
	Attempts int
}
