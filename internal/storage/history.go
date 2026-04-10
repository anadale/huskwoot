package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// SQLiteHistoryOptions holds configuration for SQLiteHistory.
type SQLiteHistoryOptions struct {
	// MaxMessages is the maximum number of messages per source (0 = no limit).
	MaxMessages int
	// TTL is the record lifetime (0 = no limit).
	TTL time.Duration
}

// SQLiteHistory implements model.History on top of SQLite.
type SQLiteHistory struct {
	db   *sql.DB
	opts SQLiteHistoryOptions
}

// NewSQLiteHistory creates a new SQLiteHistory with the given database and options.
func NewSQLiteHistory(db *sql.DB, opts SQLiteHistoryOptions) *SQLiteHistory {
	return &SQLiteHistory{db: db, opts: opts}
}

// Add adds a record to the history for the given source.
// After insertion, removes stale (TTL) and excess (MaxMessages) records.
// All operations run in a single transaction for consistency.
func (h *SQLiteHistory) Add(ctx context.Context, source string, entry model.HistoryEntry) error {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning history transaction for %q: %w", source, err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO messages (source_id, author_name, text, timestamp) VALUES (?, ?, ?, ?)`,
		source,
		entry.AuthorName,
		entry.Text,
		entry.Timestamp.Unix(),
	); err != nil {
		return fmt.Errorf("adding history entry for %q: %w", source, err)
	}

	if h.opts.TTL > 0 {
		cutoff := time.Now().Add(-h.opts.TTL).Unix()
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM messages WHERE source_id = ? AND timestamp < ?`,
			source, cutoff,
		); err != nil {
			return fmt.Errorf("TTL cleanup of history for %q: %w", source, err)
		}
	}

	if h.opts.MaxMessages > 0 {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM messages WHERE source_id = ? AND id NOT IN (
				SELECT id FROM messages WHERE source_id = ? ORDER BY timestamp DESC, id DESC LIMIT ?
			)`,
			source, source, h.opts.MaxMessages,
		); err != nil {
			return fmt.Errorf("cleaning excess history entries for %q: %w", source, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing history transaction for %q: %w", source, err)
	}
	return nil
}

// Recent returns the last limit records from the given source in chronological order.
func (h *SQLiteHistory) Recent(ctx context.Context, source string, limit int) ([]model.HistoryEntry, error) {
	if limit <= 0 {
		return nil, nil
	}

	rows, err := h.db.QueryContext(ctx,
		`SELECT author_name, text, timestamp FROM (
			SELECT author_name, text, timestamp FROM messages
			WHERE source_id = ?
			ORDER BY timestamp DESC, id DESC
			LIMIT ?
		) ORDER BY timestamp ASC`,
		source, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("getting history for %q: %w", source, err)
	}
	defer rows.Close()

	return scanEntries(rows)
}

// recentActivityScanLimit is the maximum number of records loaded when searching for an activity gap.
// Protects against OOM on large tables; records outside this window are always beyond the pause.
const recentActivityScanLimit = 500

// RecentActivity returns records from the most recent activity wave:
// searches for a stretch of records after a pause >= silenceGap (from end to start).
// If no pause is found, returns the last fallbackLimit records.
func (h *SQLiteHistory) RecentActivity(ctx context.Context, source string, silenceGap time.Duration, fallbackLimit int) ([]model.HistoryEntry, error) {
	// Load records for gap search: at least recentActivityScanLimit,
	// but not less than fallbackLimit — so fallback can return the requested count.
	scanLimit := max(recentActivityScanLimit, fallbackLimit)
	rows, err := h.db.QueryContext(ctx,
		`SELECT author_name, text, timestamp FROM (
			SELECT author_name, text, timestamp FROM messages
			WHERE source_id = ?
			ORDER BY timestamp DESC, id DESC
			LIMIT ?
		) ORDER BY timestamp ASC`,
		source, scanLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("getting activity history for %q: %w", source, err)
	}
	defer rows.Close()

	entries, err := scanEntries(rows)
	if err != nil {
		return nil, err
	}

	if len(entries) == 0 {
		return nil, nil
	}

	// Walk backwards from the end, looking for the first pause >= silenceGap.
	for i := len(entries) - 1; i > 0; i-- {
		if entries[i].Timestamp.Sub(entries[i-1].Timestamp) >= silenceGap {
			return entries[i:], nil
		}
	}

	// No pause found — return fallback.
	if fallbackLimit <= 0 {
		return nil, nil
	}
	if len(entries) > fallbackLimit {
		entries = entries[len(entries)-fallbackLimit:]
	}
	return entries, nil
}

// scanEntries reads rows into a slice of HistoryEntry.
func scanEntries(rows *sql.Rows) ([]model.HistoryEntry, error) {
	var entries []model.HistoryEntry
	for rows.Next() {
		var authorName, text string
		var timestampUnix int64

		if err := rows.Scan(&authorName, &text, &timestampUnix); err != nil {
			return nil, fmt.Errorf("scanning history entry: %w", err)
		}
		entries = append(entries, model.HistoryEntry{
			AuthorName: authorName,
			Text:       text,
			Timestamp:  time.Unix(timestampUnix, 0).UTC(),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating history entries: %w", err)
	}
	return entries, nil
}
