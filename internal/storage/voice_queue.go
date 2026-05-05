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

// Enqueue inserts a voice job. For telegram jobs, duplicate (chat_id, message_id)
// pairs are silently ignored via the partial unique index. For client jobs,
// duplicate client_job_id values are silently ignored.
func (q *SQLiteVoiceQueue) Enqueue(ctx context.Context, job model.VoiceJob) error {
	var chatID, messageID *int64
	if job.ChatID != 0 {
		chatID = &job.ChatID
	}
	if job.MessageID != 0 {
		msgID := int64(job.MessageID)
		messageID = &msgID
	}

	var deviceID, clientJobID, projectID *string
	if job.DeviceID != "" {
		deviceID = &job.DeviceID
	}
	if job.ClientJobID != "" {
		clientJobID = &job.ClientJobID
	}
	if job.ProjectID != "" {
		projectID = &job.ProjectID
	}

	_, err := q.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO voice_queue
			(source_kind, payload, mime_type, duration_ms, received_at, attempts,
			 next_attempt_at, chat_id, message_id, device_id, client_job_id, project_id)
		VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?)`,
		job.SourceKind,
		job.Payload,
		job.MimeType,
		job.Duration.Milliseconds(),
		job.ReceivedAt.Unix(),
		job.NextAttemptAt.Unix(),
		chatID,
		messageID,
		deviceID,
		clientJobID,
		projectID,
	)
	if err != nil {
		return fmt.Errorf("enqueueing voice job (source=%s): %w", job.SourceKind, err)
	}
	return nil
}

// Claim returns the first job with next_attempt_at <= now, ordered by id ASC.
// Returns nil, nil when the queue is empty or no job is due.
func (q *SQLiteVoiceQueue) Claim(ctx context.Context, now time.Time) (*model.VoiceJob, error) {
	row := q.db.QueryRowContext(ctx,
		`SELECT id, source_kind, payload, mime_type, duration_ms, received_at,
		        attempts, next_attempt_at, last_error,
		        chat_id, message_id, device_id, client_job_id, project_id
		FROM voice_queue
		WHERE next_attempt_at <= ?
		ORDER BY id ASC
		LIMIT 1`,
		now.Unix(),
	)

	var job model.VoiceJob
	var durationMs, receivedAt, nextAttemptAt int64
	var lastError sql.NullString
	var chatID, messageID sql.NullInt64
	var deviceID, clientJobID, projectID sql.NullString

	err := row.Scan(
		&job.ID, &job.SourceKind, &job.Payload, &job.MimeType,
		&durationMs, &receivedAt, &job.Attempts, &nextAttemptAt,
		&lastError, &chatID, &messageID, &deviceID, &clientJobID, &projectID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claiming voice job: %w", err)
	}

	job.Duration = time.Duration(durationMs) * time.Millisecond
	job.ReceivedAt = time.Unix(receivedAt, 0).UTC()
	job.NextAttemptAt = time.Unix(nextAttemptAt, 0).UTC()
	if lastError.Valid {
		job.LastError = lastError.String
	}
	if chatID.Valid {
		job.ChatID = chatID.Int64
	}
	if messageID.Valid {
		job.MessageID = int(messageID.Int64)
	}
	if deviceID.Valid {
		job.DeviceID = deviceID.String
	}
	if clientJobID.Valid {
		job.ClientJobID = clientJobID.String
	}
	if projectID.Valid {
		job.ProjectID = projectID.String
	}
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

// HasClientPayload reports whether a client voice upload file (identified by its
// relative path within uploads_dir) still has an active row in voice_queue.
// Implements events.VoiceOrphanChecker — used by the retention runner sweep.
func (q *SQLiteVoiceQueue) HasClientPayload(ctx context.Context, payload string) (bool, error) {
	var exists bool
	err := q.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM voice_queue WHERE source_kind = 'client' AND payload = ?)`,
		payload,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking client payload in voice_queue: %w", err)
	}
	return exists, nil
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
