// Package push implements a push job queue for devices that are not connected
// to SSE at the time an event is published.
package push

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// pushTimeLayout is the unified timestamp format for the push_queue table. Always UTC
// with a "Z" suffix so that lexicographic comparison matches chronological order
// (used in DeleteDelivered and NextBatch).
const pushTimeLayout = time.RFC3339

// SQLitePushQueue implements model.PushQueue on top of SQLite.
type SQLitePushQueue struct {
	db *sql.DB
}

// NewSQLitePushQueue creates a SQLitePushQueue backed by the given database.
func NewSQLitePushQueue(db *sql.DB) *SQLitePushQueue {
	return &SQLitePushQueue{db: db}
}

// Enqueue enqueues a single job within the given transaction. Sets
// created_at to the current time, next_attempt_at = created_at, attempts = 0.
func (q *SQLitePushQueue) Enqueue(ctx context.Context, tx *sql.Tx, deviceID string, eventSeq int64) error {
	if tx == nil {
		return fmt.Errorf("enqueuing push job for device %s: tx cannot be nil", deviceID)
	}
	if deviceID == "" {
		return fmt.Errorf("enqueuing push job: deviceID is required")
	}
	now := time.Now().UTC().Format(pushTimeLayout)
	_, err := tx.ExecContext(ctx,
		`INSERT INTO push_queue (device_id, event_seq, created_at, attempts, next_attempt_at)
		 VALUES (?, ?, ?, 0, ?)`,
		deviceID, eventSeq, now, now,
	)
	if err != nil {
		return fmt.Errorf("enqueuing push job for device %s: %w", deviceID, err)
	}
	return nil
}

// NextBatch returns up to limit pending jobs (delivered_at IS NULL and
// dropped_at IS NULL) whose next_attempt_at has already passed. Ordered by
// next_attempt_at ASC, id ASC. limit <= 0 means no limit.
func (q *SQLitePushQueue) NextBatch(ctx context.Context, limit int) ([]model.PushJob, error) {
	query := `SELECT id, device_id, event_seq, created_at, attempts, last_error, next_attempt_at
	          FROM push_queue
	          WHERE delivered_at IS NULL AND dropped_at IS NULL AND next_attempt_at <= ?
	          ORDER BY next_attempt_at ASC, id ASC`
	args := []any{time.Now().UTC().Format(pushTimeLayout)}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading push batch: %w", err)
	}
	defer rows.Close()

	var jobs []model.PushJob
	for rows.Next() {
		job, err := scanPushJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning push job row: %w", err)
		}
		jobs = append(jobs, *job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating push jobs: %w", err)
	}
	return jobs, nil
}

// MarkDelivered marks a job as delivered (delivered_at = now).
func (q *SQLitePushQueue) MarkDelivered(ctx context.Context, id int64) error {
	now := time.Now().UTC().Format(pushTimeLayout)
	_, err := q.db.ExecContext(ctx,
		`UPDATE push_queue SET delivered_at = ? WHERE id = ?`,
		now, id,
	)
	if err != nil {
		return fmt.Errorf("marking push job %d as delivered: %w", id, err)
	}
	return nil
}

// MarkFailed increments attempts, saves the error text, and schedules
// the next attempt no earlier than nextAttempt.
func (q *SQLitePushQueue) MarkFailed(ctx context.Context, id int64, errText string, nextAttempt time.Time) error {
	_, err := q.db.ExecContext(ctx,
		`UPDATE push_queue
		 SET attempts = attempts + 1,
		     last_error = ?,
		     next_attempt_at = ?
		 WHERE id = ?`,
		errText, nextAttempt.UTC().Format(pushTimeLayout), id,
	)
	if err != nil {
		return fmt.Errorf("marking push job %d as failed: %w", id, err)
	}
	return nil
}

// Drop marks a job as permanently failed (dropped_at = now,
// dropped_reason = reason).
func (q *SQLitePushQueue) Drop(ctx context.Context, id int64, reason string) error {
	now := time.Now().UTC().Format(pushTimeLayout)
	_, err := q.db.ExecContext(ctx,
		`UPDATE push_queue SET dropped_at = ?, dropped_reason = ? WHERE id = ?`,
		now, reason, id,
	)
	if err != nil {
		return fmt.Errorf("dropping push job %d: %w", id, err)
	}
	return nil
}

// DeleteDelivered deletes completed jobs (delivered_at OR dropped_at)
// whose completion timestamp is before cutoff. Returns the number of deleted rows.
func (q *SQLitePushQueue) DeleteDelivered(ctx context.Context, cutoff time.Time) (int64, error) {
	cutoffStr := cutoff.UTC().Format(pushTimeLayout)
	res, err := q.db.ExecContext(ctx,
		`DELETE FROM push_queue
		 WHERE (delivered_at IS NOT NULL AND delivered_at < ?)
		    OR (dropped_at   IS NOT NULL AND dropped_at   < ?)`,
		cutoffStr, cutoffStr,
	)
	if err != nil {
		return 0, fmt.Errorf("cleaning push_queue: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("counting deleted push jobs: %w", err)
	}
	return n, nil
}

// rowScanner abstracts sql.Row and sql.Rows for shared scanning.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanPushJob(r rowScanner) (*model.PushJob, error) {
	var (
		job            model.PushJob
		createdAtStr   string
		lastErrNS      sql.NullString
		nextAttemptStr string
	)
	if err := r.Scan(
		&job.ID, &job.DeviceID, &job.EventSeq,
		&createdAtStr, &job.Attempts, &lastErrNS, &nextAttemptStr,
	); err != nil {
		return nil, err
	}

	created, err := time.Parse(pushTimeLayout, createdAtStr)
	if err != nil {
		return nil, fmt.Errorf("parsing created_at %q: %w", createdAtStr, err)
	}
	job.CreatedAt = created

	next, err := time.Parse(pushTimeLayout, nextAttemptStr)
	if err != nil {
		return nil, fmt.Errorf("parsing next_attempt_at %q: %w", nextAttemptStr, err)
	}
	job.NextAttemptAt = next

	if lastErrNS.Valid {
		job.LastError = lastErrNS.String
	}

	return &job, nil
}
