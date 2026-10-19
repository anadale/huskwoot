package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/anadale/huskwoot/internal/model"
)

// SQLiteHistoryOptions holds configuration for SQLiteHistory.
type SQLiteHistoryOptions struct {
	// MaxMessages is the maximum number of messages per source (0 = no limit).
	MaxMessages int
	// TTL is the record lifetime (0 = no limit).
	TTL time.Duration
}

// ClientHistoryCap is the maximum number of messages kept per client chat
// source. Older rows are deleted after each client write.
const ClientHistoryCap = 1000

// clientSourcePrefix marks client chat sources ("client:<device id>").
const clientSourcePrefix = "client:"

// historyColumns is the column list read into model.HistoryEntry by scanEntries.
const historyColumns = `message_id, author_name, text, timestamp, role, kind, audio_job_id, audio_duration_ms, task_ids`

// SQLiteHistory implements model.History on top of SQLite.
type SQLiteHistory struct {
	db   *sql.DB
	opts SQLiteHistoryOptions
}

// NewSQLiteHistory creates a new SQLiteHistory with the given database and options.
func NewSQLiteHistory(db *sql.DB, opts SQLiteHistoryOptions) *SQLiteHistory {
	return &SQLiteHistory{db: db, opts: opts}
}

// AddTx inserts a history record within the caller-owned transaction.
// Returns the generated messageID (UUID v4). TTL and MaxMessages cleanup
// are not performed — use Add for standalone writes that need cleanup.
// For client chat sources the oldest rows beyond ClientHistoryCap are
// deleted in the same transaction.
func (h *SQLiteHistory) AddTx(ctx context.Context, tx *sql.Tx, source string, entry model.HistoryEntry) (string, error) {
	msgID := uuid.New().String()
	kind := entry.Kind
	if kind == "" {
		kind = model.HistoryKindText
	}
	taskIDs, err := encodeTaskIDs(mergeTaskIDs(nil, entry.TaskIDs))
	if err != nil {
		return "", fmt.Errorf("encoding task ids for %q: %w", source, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO messages (source_id, author_name, text, timestamp, message_id, role, kind, audio_job_id, audio_duration_ms, task_ids)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		source,
		entry.AuthorName,
		entry.Text,
		entry.Timestamp.Unix(),
		msgID,
		entry.Role,
		kind,
		entry.AudioJobID,
		entry.AudioDurationMs,
		taskIDs,
	); err != nil {
		return "", fmt.Errorf("adding history entry for %q: %w", source, err)
	}

	if strings.HasPrefix(source, clientSourcePrefix) {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM messages WHERE id IN (
				SELECT id FROM messages WHERE source_id = ? ORDER BY timestamp DESC, id DESC LIMIT -1 OFFSET ?
			)`,
			source, ClientHistoryCap,
		); err != nil {
			return "", fmt.Errorf("capping client history for %q: %w", source, err)
		}
	}
	return msgID, nil
}

// SetTaskIDs merges ids into the task_ids of the message with messageID.
// Existing ids keep their order; new ids are appended without duplicates.
// Returns an error wrapping sql.ErrNoRows when the message does not exist.
func (h *SQLiteHistory) SetTaskIDs(ctx context.Context, messageID string, ids []string) error {
	if len(mergeTaskIDs(nil, ids)) == 0 {
		return nil
	}

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning task ids transaction for message %q: %w", messageID, err)
	}
	defer tx.Rollback() //nolint:errcheck

	var raw string
	if err := tx.QueryRowContext(ctx,
		`SELECT task_ids FROM messages WHERE message_id = ?`, messageID,
	).Scan(&raw); err != nil {
		return fmt.Errorf("reading task ids of message %q: %w", messageID, err)
	}

	merged, err := encodeTaskIDs(mergeTaskIDs(decodeTaskIDs(messageID, raw), ids))
	if err != nil {
		return fmt.Errorf("encoding task ids for message %q: %w", messageID, err)
	}
	if merged == raw {
		return nil
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE messages SET task_ids = ? WHERE message_id = ?`, merged, messageID,
	); err != nil {
		return fmt.Errorf("updating task ids of message %q: %w", messageID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing task ids for message %q: %w", messageID, err)
	}
	return nil
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

	if _, err := h.AddTx(ctx, tx, source, entry); err != nil {
		return err
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
// System records are skipped.
func (h *SQLiteHistory) Recent(ctx context.Context, source string, limit int) ([]model.HistoryEntry, error) {
	if limit <= 0 {
		return nil, nil
	}

	rows, err := h.db.QueryContext(ctx,
		`SELECT `+historyColumns+` FROM (
			SELECT `+historyColumns+` FROM messages
			WHERE source_id = ? AND role <> ?
			ORDER BY timestamp DESC, id DESC
			LIMIT ?
		) ORDER BY timestamp ASC`,
		source, model.HistoryRoleSystem, limit,
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
// System records are skipped.
func (h *SQLiteHistory) RecentActivity(ctx context.Context, source string, silenceGap time.Duration, fallbackLimit int) ([]model.HistoryEntry, error) {
	// Load records for gap search: at least recentActivityScanLimit,
	// but not less than fallbackLimit — so fallback can return the requested count.
	scanLimit := max(recentActivityScanLimit, fallbackLimit)
	rows, err := h.db.QueryContext(ctx,
		`SELECT `+historyColumns+` FROM (
			SELECT `+historyColumns+` FROM messages
			WHERE source_id = ? AND role <> ?
			ORDER BY timestamp DESC, id DESC
			LIMIT ?
		) ORDER BY timestamp ASC`,
		source, model.HistoryRoleSystem, scanLimit,
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

// ListClient returns one page of the history of source: the newest limit
// records older than the message before (exclusive; empty means from the newest
// record), ordered oldest to newest. System records are included. Records are
// ordered by (timestamp, id), the order used by Recent and the client cap.
// nextBefore is the message id of the oldest record of the page when older
// records exist, and empty on the last page. Returns an error wrapping
// model.ErrHistoryCursorNotFound when before is not a message of source.
func (h *SQLiteHistory) ListClient(ctx context.Context, source, before string, limit int) ([]model.HistoryEntry, string, error) {
	if limit <= 0 {
		return nil, "", nil
	}

	query := `SELECT ` + historyColumns + ` FROM messages WHERE source_id = ?`
	args := []any{source}
	if before != "" {
		var cursorTS, cursorID int64
		err := h.db.QueryRowContext(ctx,
			`SELECT timestamp, id FROM messages WHERE source_id = ? AND message_id = ?`,
			source, before,
		).Scan(&cursorTS, &cursorID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", fmt.Errorf("listing history for %q before %q: %w", source, before, model.ErrHistoryCursorNotFound)
		}
		if err != nil {
			return nil, "", fmt.Errorf("resolving history cursor %q for %q: %w", before, source, err)
		}
		query += ` AND (timestamp < ? OR (timestamp = ? AND id < ?))`
		args = append(args, cursorTS, cursorTS, cursorID)
	}
	// One extra row tells whether an older page exists.
	query += ` ORDER BY timestamp DESC, id DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("listing history for %q: %w", source, err)
	}
	defer rows.Close()

	entries, err := scanEntries(rows)
	if err != nil {
		return nil, "", err
	}

	var nextBefore string
	if len(entries) > limit {
		entries = entries[:limit]
		nextBefore = entries[limit-1].MessageID
	}
	slices.Reverse(entries)
	return entries, nextBefore, nil
}

// scanEntries reads rows selected with historyColumns into a slice of HistoryEntry.
func scanEntries(rows *sql.Rows) ([]model.HistoryEntry, error) {
	var entries []model.HistoryEntry
	for rows.Next() {
		var e model.HistoryEntry
		var timestampUnix int64
		var taskIDs string

		if err := rows.Scan(
			&e.MessageID, &e.AuthorName, &e.Text, &timestampUnix,
			&e.Role, &e.Kind, &e.AudioJobID, &e.AudioDurationMs, &taskIDs,
		); err != nil {
			return nil, fmt.Errorf("scanning history entry: %w", err)
		}
		e.Timestamp = time.Unix(timestampUnix, 0).UTC()
		e.TaskIDs = decodeTaskIDs(e.MessageID, taskIDs)
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating history entries: %w", err)
	}
	return entries, nil
}

// mergeTaskIDs appends ids to existing, skipping empty values and duplicates,
// and keeps the original order. Returns nil when the result is empty.
func mergeTaskIDs(existing, ids []string) []string {
	var out []string
	seen := make(map[string]struct{}, len(existing)+len(ids))
	for _, list := range [][]string{existing, ids} {
		for _, id := range list {
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

// encodeTaskIDs serialises ids as a JSON array; an empty list becomes "".
func encodeTaskIDs(ids []string) (string, error) {
	if len(ids) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// decodeTaskIDs parses the task_ids column. An empty or malformed value
// yields nil so that a broken row never fails a history read.
func decodeTaskIDs(messageID, raw string) []string {
	if raw == "" {
		return nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		slog.Warn("ignoring malformed history task ids", "message_id", messageID, "error", err)
		return nil
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}
