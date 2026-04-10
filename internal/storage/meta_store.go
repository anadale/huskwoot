package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const projectKeyPrefix = "project:"

// SQLiteMetaStore implements model.MetaStore on top of SQLite.
// Supports keys of the form "project:<channelID>", mapped to the channel_projects table.
type SQLiteMetaStore struct {
	db *sql.DB
}

// NewSQLiteMetaStore creates a new SQLiteMetaStore with the given database.
func NewSQLiteMetaStore(db *sql.DB) *SQLiteMetaStore {
	return &SQLiteMetaStore{db: db}
}

// Get returns the value for the given key. Returns "", nil if the key is not found.
// Supported key format: "project:<channelID>".
func (s *SQLiteMetaStore) Get(ctx context.Context, key string) (string, error) {
	channelID, ok := trimProjectPrefix(key)
	if !ok {
		return "", nil
	}

	var projectID string
	err := s.db.QueryRowContext(ctx,
		`SELECT project_id FROM channel_projects WHERE channel_id = ?`,
		channelID,
	).Scan(&projectID)

	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("getting metadata %q: %w", key, err)
	}
	return projectID, nil
}

// SetTx sets the value for the given key within the given transaction.
// Supported key format: "project:<channelID>".
func (s *SQLiteMetaStore) SetTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	if tx == nil {
		return fmt.Errorf("saving metadata %q: tx cannot be nil", key)
	}
	channelID, ok := trimProjectPrefix(key)
	if !ok {
		return fmt.Errorf("unsupported MetaStore key: %q", key)
	}

	_, err := tx.ExecContext(ctx,
		`INSERT OR REPLACE INTO channel_projects (channel_id, project_id) VALUES (?, ?)`,
		channelID, value,
	)
	if err != nil {
		return fmt.Errorf("saving metadata %q: %w", key, err)
	}
	return nil
}

// Values returns all values whose keys start with the given prefix.
// Returns nil, nil if no matching keys are found.
// Supported prefix: "project:".
func (s *SQLiteMetaStore) Values(ctx context.Context, prefix string) ([]string, error) {
	if prefix != projectKeyPrefix {
		return nil, nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT project_id FROM channel_projects ORDER BY project_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("listing metadata (prefix=%q): %w", prefix, err)
	}
	defer rows.Close()

	var values []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scanning metadata row: %w", err)
		}
		if name != "" {
			values = append(values, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating metadata: %w", err)
	}
	if len(values) == 0 {
		return nil, nil
	}
	return values, nil
}

// trimProjectPrefix strips the "project:" prefix from the key.
// Returns false if the key does not start with "project:".
func trimProjectPrefix(key string) (string, bool) {
	after, ok := strings.CutPrefix(key, projectKeyPrefix)
	return after, ok
}
