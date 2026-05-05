package model

import "time"

// VoiceJob represents a single voice transcription task persisted in voice_queue.
type VoiceJob struct {
	// ID is the auto-increment voice_queue row identifier (0 before insertion).
	ID int64
	// SourceKind identifies the originating channel: "telegram" or "client".
	SourceKind string
	// Payload is the audio locator: file_id for telegram, relative path for client.
	Payload string
	// MimeType is the audio MIME type (e.g. "audio/ogg").
	MimeType string
	// Duration is the audio length.
	Duration time.Duration
	// ReceivedAt is the time the message was originally received.
	ReceivedAt time.Time
	// Attempts is the number of processing attempts already made.
	Attempts int
	// NextAttemptAt is the scheduled time for the next processing attempt.
	NextAttemptAt time.Time
	// LastError holds the most recent processing error message, if any.
	LastError string

	// Telegram-only fields:
	ChatID    int64
	MessageID int

	// Client-only fields:
	DeviceID    string
	ClientJobID string // UUID v4 returned to the client in the 202 response
	ProjectID   string // optional project context hint (UUID)
}
