package storage_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anadale/huskwoot/internal/storage"
)

func TestOpenDB_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := storage.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB returned error: %v", err)
	}
	defer db.Close()

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("database file was not created")
	}
}

func TestOpenDB_RepeatedOpenWorks(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db1, err := storage.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("first OpenDB returned error: %v", err)
	}
	db1.Close()

	db2, err := storage.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("repeated OpenDB returned error: %v", err)
	}
	defer db2.Close()

	if err := db2.Ping(); err != nil {
		t.Errorf("Ping on repeatedly opened DB: %v", err)
	}
}

func TestOpenDB_TablesExist(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := storage.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB returned error: %v", err)
	}
	defer db.Close()

	tables := []string{"cursors", "channel_projects", "messages"}
	for _, table := range tables {
		t.Run(table, func(t *testing.T) {
			rows, err := db.Query("SELECT * FROM " + table + " LIMIT 0")
			if err != nil {
				t.Fatalf("table %q not accessible: %v", table, err)
			}
			rows.Close()
		})
	}
}

func TestOpenDB_IndexExists(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := storage.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB returned error: %v", err)
	}
	defer db.Close()

	var count int
	err = db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_messages_source_time'",
	).Scan(&count)
	if err != nil {
		t.Fatalf("index query: %v", err)
	}
	if count != 1 {
		t.Error("index idx_messages_source_time not found")
	}
}
