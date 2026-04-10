package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// SQLiteStateStore implements model.StateStore on top of SQLite.
type SQLiteStateStore struct {
	db *sql.DB
}

// NewSQLiteStateStore creates a new SQLiteStateStore with the given database.
func NewSQLiteStateStore(db *sql.DB) *SQLiteStateStore {
	return &SQLiteStateStore{db: db}
}

// GetCursor returns the saved cursor for a channel.
// Returns nil, nil if no cursor is found.
func (s *SQLiteStateStore) GetCursor(ctx context.Context, channelID string) (*model.Cursor, error) {
	var messageID, folderID string
	var updatedAtUnix int64

	err := s.db.QueryRowContext(ctx,
		`SELECT message_id, folder_id, updated_at FROM cursors WHERE channel_id = ?`,
		channelID,
	).Scan(&messageID, &folderID, &updatedAtUnix)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting cursor %q: %w", channelID, err)
	}

	return &model.Cursor{
		MessageID: messageID,
		FolderID:  folderID,
		UpdatedAt: time.Unix(updatedAtUnix, 0).UTC(),
	}, nil
}

// SaveCursor saves the cursor for a channel, overwriting any existing one.
func (s *SQLiteStateStore) SaveCursor(ctx context.Context, channelID string, cursor model.Cursor) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO cursors (channel_id, message_id, folder_id, updated_at)
		 VALUES (?, ?, ?, ?)`,
		channelID,
		cursor.MessageID,
		cursor.FolderID,
		cursor.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("saving cursor %q: %w", channelID, err)
	}
	return nil
}
