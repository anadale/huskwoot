package storage

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"

	"github.com/anadale/huskwoot/internal/storage/migrations"
)

// OpenDB opens (or creates) a SQLite file at the given path,
// configures WAL mode and foreign keys, and applies migrations via goose.
func OpenDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	// SQLite allows only one writer — limit the pool to one connection
	// to avoid SQLITE_BUSY under concurrent goroutine access.
	db.SetMaxOpenConns(1)

	if err := applyPragmas(db); err != nil {
		db.Close()
		return nil, err
	}

	if err := migrations.Up(db); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

func applyPragmas(db *sql.DB) error {
	// journal_mode returns the active mode as a string — use QueryRow to verify it.
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return fmt.Errorf("setting WAL mode: %w", err)
	}
	if mode != "wal" {
		return fmt.Errorf("failed to enable WAL mode, current mode: %q", mode)
	}

	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		return fmt.Errorf("enabling foreign keys: %w", err)
	}
	return nil
}
