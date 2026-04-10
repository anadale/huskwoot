// Package events implements the domain event store and an in-memory broker
// for fanning out events to SSE subscribers.
package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// eventTimeLayout is the fixed timestamp format for events.created_at.
// Always UTC with a "Z" suffix so that strings compare correctly lexicographically
// in SQL queries (used in DeleteOlderThan and for sorting).
const eventTimeLayout = time.RFC3339

// SQLiteEventStore implements model.EventStore on top of SQLite.
type SQLiteEventStore struct {
	db *sql.DB
}

// NewSQLiteEventStore creates a new SQLiteEventStore with the given database.
func NewSQLiteEventStore(db *sql.DB) *SQLiteEventStore {
	return &SQLiteEventStore{db: db}
}

// Insert saves an event within the given transaction and returns the assigned
// seq. If ev.CreatedAt is zero, the current time (UTC) is used.
func (s *SQLiteEventStore) Insert(ctx context.Context, tx *sql.Tx, ev model.Event) (int64, error) {
	if tx == nil {
		return 0, fmt.Errorf("inserting event %s: tx cannot be nil", ev.Kind)
	}
	if ev.Kind == "" {
		return 0, fmt.Errorf("inserting event: Kind is required")
	}
	if len(ev.Payload) == 0 {
		return 0, fmt.Errorf("inserting event %s: Payload is required", ev.Kind)
	}

	createdAt := ev.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	} else {
		createdAt = createdAt.UTC()
	}

	var seq int64
	err := tx.QueryRowContext(ctx,
		`INSERT INTO events (kind, entity_id, payload, created_at)
		 VALUES (?, ?, ?, ?)
		 RETURNING seq`,
		string(ev.Kind), ev.EntityID, string(ev.Payload), createdAt.Format(eventTimeLayout),
	).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("inserting event %s: %w", ev.Kind, err)
	}
	return seq, nil
}

// SinceSeq returns events with seq > afterSeq in ascending order.
// limit <= 0 means no limit.
func (s *SQLiteEventStore) SinceSeq(ctx context.Context, afterSeq int64, limit int) ([]model.Event, error) {
	query := `SELECT seq, kind, entity_id, payload, created_at
	          FROM events WHERE seq > ? ORDER BY seq ASC`
	args := []any{afterSeq}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading events with seq > %d: %w", afterSeq, err)
	}
	defer rows.Close()

	var events []model.Event
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning event row: %w", err)
		}
		events = append(events, *ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating events: %w", err)
	}
	return events, nil
}

// MaxSeq returns the maximum seq stored in the table. Returns 0 for an empty table.
func (s *SQLiteEventStore) MaxSeq(ctx context.Context) (int64, error) {
	var seq sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(seq) FROM events`).Scan(&seq)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("getting MAX(seq): %w", err)
	}
	if !seq.Valid {
		return 0, nil
	}
	return seq.Int64, nil
}

// MinSeq returns the minimum seq stored in the table. Returns 0 for an empty table.
// Used by SSE replay to distinguish retention loss from a natural AUTOINCREMENT gap:
// retention deletes from the head of the sequence, so MinSeq > cursor+1 means the
// events the client needed have been deleted.
func (s *SQLiteEventStore) MinSeq(ctx context.Context) (int64, error) {
	var seq sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MIN(seq) FROM events`).Scan(&seq)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("getting MIN(seq): %w", err)
	}
	if !seq.Valid {
		return 0, nil
	}
	return seq.Int64, nil
}

// GetBySeq returns an event by seq. Returns nil, nil if the event
// is absent (deleted by retention or never existed).
func (s *SQLiteEventStore) GetBySeq(ctx context.Context, seq int64) (*model.Event, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT seq, kind, entity_id, payload, created_at FROM events WHERE seq = ?`, seq,
	)
	ev, err := scanEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting event seq=%d: %w", seq, err)
	}
	return ev, nil
}

// DeleteOlderThan deletes events with CreatedAt < cutoff and returns the number
// of deleted rows.
func (s *SQLiteEventStore) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM events WHERE created_at < ?`,
		cutoff.UTC().Format(eventTimeLayout),
	)
	if err != nil {
		return 0, fmt.Errorf("deleting stale events: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("counting deleted events: %w", err)
	}
	return n, nil
}

// rowScanner abstracts sql.Row and sql.Rows for shared scanning.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanEvent(r rowScanner) (*model.Event, error) {
	var (
		ev           model.Event
		payloadStr   string
		createdAtStr string
	)
	if err := r.Scan(&ev.Seq, &ev.Kind, &ev.EntityID, &payloadStr, &createdAtStr); err != nil {
		return nil, err
	}
	ev.Payload = json.RawMessage(payloadStr)

	created, err := time.Parse(eventTimeLayout, createdAtStr)
	if err != nil {
		return nil, fmt.Errorf("parsing created_at %q: %w", createdAtStr, err)
	}
	ev.CreatedAt = created
	return &ev, nil
}
