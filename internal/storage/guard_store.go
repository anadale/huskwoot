package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// SQLiteGuardStore implements model.GuardStore on top of SQLite.
type SQLiteGuardStore struct {
	db *sql.DB
}

// NewSQLiteGuardStore creates a new SQLiteGuardStore.
func NewSQLiteGuardStore(db *sql.DB) *SQLiteGuardStore {
	return &SQLiteGuardStore{db: db}
}

// UpsertPending saves or updates a pending approval record.
func (s *SQLiteGuardStore) UpsertPending(ctx context.Context, chatID int64, welcomeMsgID int, deadline time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO guard_pending(chat_id, welcome_msg_id, deadline) VALUES(?, ?, ?)
		 ON CONFLICT(chat_id) DO UPDATE SET welcome_msg_id = excluded.welcome_msg_id, deadline = excluded.deadline`,
		chatID, welcomeMsgID, deadline.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upserting guard_pending chat %d: %w", chatID, err)
	}
	return nil
}

// DeletePending removes the pending record for a chat.
func (s *SQLiteGuardStore) DeletePending(ctx context.Context, chatID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM guard_pending WHERE chat_id = ?`, chatID)
	if err != nil {
		return fmt.Errorf("deleting guard_pending chat %d: %w", chatID, err)
	}
	return nil
}

// ListPending returns all pending approval records.
func (s *SQLiteGuardStore) ListPending(ctx context.Context) ([]model.GuardPending, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT chat_id, welcome_msg_id, deadline FROM guard_pending`)
	if err != nil {
		return nil, fmt.Errorf("listing guard_pending: %w", err)
	}
	defer rows.Close()

	var result []model.GuardPending
	for rows.Next() {
		var p model.GuardPending
		var deadlineUnix int64
		if err := rows.Scan(&p.ChatID, &p.WelcomeMsgID, &deadlineUnix); err != nil {
			return nil, fmt.Errorf("scanning guard_pending row: %w", err)
		}
		p.Deadline = time.Unix(deadlineUnix, 0).UTC()
		result = append(result, p)
	}
	return result, rows.Err()
}
