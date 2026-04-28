package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// SQLiteForwardBuffer implements model.ForwardBuffer using a SQLite table.
type SQLiteForwardBuffer struct {
	db *sql.DB
}

// NewSQLiteForwardBuffer creates a new SQLiteForwardBuffer backed by db.
func NewSQLiteForwardBuffer(db *sql.DB) *SQLiteForwardBuffer {
	return &SQLiteForwardBuffer{db: db}
}

// Append inserts a forward entry into the buffer.
// Duplicate (ChatID, MessageID) pairs are silently ignored (INSERT OR IGNORE).
func (b *SQLiteForwardBuffer) Append(ctx context.Context, e model.ForwardEntry) error {
	_, err := b.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO forward_buffer
			(chat_id, message_id, forward_from_id, forward_from_name, is_self_authored, original_date, text, received_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ChatID,
		e.MessageID,
		e.ForwardFromID,
		e.ForwardFromName,
		boolToInt(e.IsSelfAuthored),
		e.OriginalDate.Unix(),
		e.Text,
		e.ReceivedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("appending forward entry (chat=%s msg=%s): %w", e.ChatID, e.MessageID, err)
	}
	return nil
}

// LastReceivedAt returns the maximum ReceivedAt for the given chat.
// Returns zero time and false when the chat has no entries.
func (b *SQLiteForwardBuffer) LastReceivedAt(ctx context.Context, chatID string) (time.Time, bool, error) {
	var unixSec sql.NullInt64
	err := b.db.QueryRowContext(ctx,
		`SELECT MAX(received_at) FROM forward_buffer WHERE chat_id = ?`,
		chatID,
	).Scan(&unixSec)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("querying last received_at for chat %s: %w", chatID, err)
	}
	if !unixSec.Valid {
		return time.Time{}, false, nil
	}
	return time.Unix(unixSec.Int64, 0).UTC(), true, nil
}

// LoadBatch returns all entries for the given chat ordered by OriginalDate ASC, id ASC.
func (b *SQLiteForwardBuffer) LoadBatch(ctx context.Context, chatID string) ([]model.ForwardEntry, error) {
	rows, err := b.db.QueryContext(ctx,
		`SELECT chat_id, message_id, forward_from_id, forward_from_name, is_self_authored, original_date, text, received_at
		FROM forward_buffer
		WHERE chat_id = ?
		ORDER BY original_date ASC, id ASC`,
		chatID,
	)
	if err != nil {
		return nil, fmt.Errorf("loading forward batch for chat %s: %w", chatID, err)
	}
	defer rows.Close()

	var entries []model.ForwardEntry
	for rows.Next() {
		var e model.ForwardEntry
		var selfAuthored int
		var origDate, receivedAt int64

		if err := rows.Scan(
			&e.ChatID, &e.MessageID,
			&e.ForwardFromID, &e.ForwardFromName,
			&selfAuthored, &origDate, &e.Text, &receivedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning forward entry: %w", err)
		}
		e.IsSelfAuthored = selfAuthored != 0
		e.OriginalDate = time.Unix(origDate, 0).UTC()
		e.ReceivedAt = time.Unix(receivedAt, 0).UTC()
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating forward buffer rows for chat %s: %w", chatID, err)
	}
	return entries, nil
}

// DeleteBatchTx deletes all entries for the given chat within the provided transaction.
func (b *SQLiteForwardBuffer) DeleteBatchTx(ctx context.Context, tx *sql.Tx, chatID string) error {
	_, err := tx.ExecContext(ctx,
		`DELETE FROM forward_buffer WHERE chat_id = ?`,
		chatID,
	)
	if err != nil {
		return fmt.Errorf("deleting forward batch for chat %s: %w", chatID, err)
	}
	return nil
}

// ListPendingChats returns chat IDs whose MAX(received_at) is strictly before the given threshold.
func (b *SQLiteForwardBuffer) ListPendingChats(ctx context.Context, before time.Time) ([]string, error) {
	rows, err := b.db.QueryContext(ctx,
		`SELECT chat_id FROM forward_buffer
		GROUP BY chat_id
		HAVING MAX(received_at) < ?`,
		before.Unix(),
	)
	if err != nil {
		return nil, fmt.Errorf("listing pending forward chats: %w", err)
	}
	defer rows.Close()

	var chatIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning pending chat id: %w", err)
		}
		chatIDs = append(chatIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating pending forward chats: %w", err)
	}
	return chatIDs, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
