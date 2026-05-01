package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// SQLiteVoiceQueue implements model.VoiceQueue backed by the voice_queue table.
type SQLiteVoiceQueue struct {
	db *sql.DB
}

// NewSQLiteVoiceQueue creates a new SQLiteVoiceQueue backed by db.
func NewSQLiteVoiceQueue(db *sql.DB) *SQLiteVoiceQueue {
	return &SQLiteVoiceQueue{db: db}
}

// Enqueue inserts a voice job. Duplicate (chat_id, message_id) pairs are silently ignored.
func (q *SQLiteVoiceQueue) Enqueue(ctx context.Context, job model.VoiceJob) error {
	_, err := q.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO voice_queue
			(chat_id, message_id, file_id, mime_type, duration_ms, received_at, attempts, next_attempt_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, ?)`,
		job.ChatID,
		job.MessageID,
		job.FileID,
		job.MimeType,
		job.Duration.Milliseconds(),
		job.ReceivedAt.Unix(),
		job.ReceivedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("enqueueing voice job (chat=%d msg=%d): %w", job.ChatID, job.MessageID, err)
	}
	return nil
}

// Claim returns the first job with next_attempt_at <= now, ordered by id ASC.
// Returns nil, nil when the queue is empty or no job is due.
func (q *SQLiteVoiceQueue) Claim(ctx context.Context, now time.Time) (*model.VoiceJob, error) {
	row := q.db.QueryRowContext(ctx,
		`SELECT id, chat_id, message_id, file_id, mime_type, duration_ms, received_at, attempts
		FROM voice_queue
		WHERE next_attempt_at <= ?
		ORDER BY id ASC
		LIMIT 1`,
		now.Unix(),
	)
	var job model.VoiceJob
	var durationMs int64
	var receivedAt int64
	err := row.Scan(&job.ID, &job.ChatID, &job.MessageID, &job.FileID, &job.MimeType, &durationMs, &receivedAt, &job.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claiming voice job: %w", err)
	}
	job.Duration = time.Duration(durationMs) * time.Millisecond
	job.ReceivedAt = time.Unix(receivedAt, 0).UTC()
	return &job, nil
}

// Complete permanently removes a job from the queue.
func (q *SQLiteVoiceQueue) Complete(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM voice_queue WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("completing voice job %d: %w", id, err)
	}
	return nil
}

// Reschedule increments attempts, records the last error, and updates next_attempt_at.
func (q *SQLiteVoiceQueue) Reschedule(ctx context.Context, id int64, nextAt time.Time, lastErr string) error {
	_, err := q.db.ExecContext(ctx,
		`UPDATE voice_queue SET attempts = attempts + 1, next_attempt_at = ?, last_error = ? WHERE id = ?`,
		nextAt.Unix(), lastErr, id,
	)
	if err != nil {
		return fmt.Errorf("rescheduling voice job %d: %w", id, err)
	}
	return nil
}
