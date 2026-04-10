package storage_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/storage"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.OpenDB(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSQLiteStateStore_GetCursor_EmptyDB(t *testing.T) {
	db := openTestDB(t)
	store := storage.NewSQLiteStateStore(db)

	cursor, err := store.GetCursor(context.Background(), "ch1")
	if err != nil {
		t.Fatalf("GetCursor returned error: %v", err)
	}
	if cursor != nil {
		t.Errorf("want nil, got %+v", cursor)
	}
}

func TestSQLiteStateStore_SaveAndGetCursor(t *testing.T) {
	db := openTestDB(t)
	store := storage.NewSQLiteStateStore(db)
	ctx := context.Background()

	want := model.Cursor{
		MessageID: "msg-42",
		FolderID:  "INBOX",
		UpdatedAt: time.Unix(1_700_000_000, 0).UTC(),
	}

	if err := store.SaveCursor(ctx, "ch1", want); err != nil {
		t.Fatalf("SaveCursor: %v", err)
	}

	got, err := store.GetCursor(ctx, "ch1")
	if err != nil {
		t.Fatalf("GetCursor: %v", err)
	}
	if got == nil {
		t.Fatal("GetCursor returned nil after SaveCursor")
	}
	if got.MessageID != want.MessageID {
		t.Errorf("MessageID: want %q, got %q", want.MessageID, got.MessageID)
	}
	if got.FolderID != want.FolderID {
		t.Errorf("FolderID: want %q, got %q", want.FolderID, got.FolderID)
	}
	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("UpdatedAt: want %v, got %v", want.UpdatedAt, got.UpdatedAt)
	}
}

func TestSQLiteStateStore_SaveCursor_Overwrites(t *testing.T) {
	db := openTestDB(t)
	store := storage.NewSQLiteStateStore(db)
	ctx := context.Background()

	first := model.Cursor{MessageID: "msg-1", FolderID: "", UpdatedAt: time.Unix(1_000, 0).UTC()}
	second := model.Cursor{MessageID: "msg-99", FolderID: "SENT", UpdatedAt: time.Unix(2_000, 0).UTC()}

	if err := store.SaveCursor(ctx, "ch1", first); err != nil {
		t.Fatalf("first SaveCursor: %v", err)
	}
	if err := store.SaveCursor(ctx, "ch1", second); err != nil {
		t.Fatalf("second SaveCursor: %v", err)
	}

	got, err := store.GetCursor(ctx, "ch1")
	if err != nil {
		t.Fatalf("GetCursor: %v", err)
	}
	if got == nil {
		t.Fatal("GetCursor returned nil")
	}
	if got.MessageID != second.MessageID {
		t.Errorf("MessageID: want %q, got %q", second.MessageID, got.MessageID)
	}
	if got.FolderID != second.FolderID {
		t.Errorf("FolderID: want %q, got %q", second.FolderID, got.FolderID)
	}
	if !got.UpdatedAt.Equal(second.UpdatedAt) {
		t.Errorf("UpdatedAt: want %v, got %v", second.UpdatedAt, got.UpdatedAt)
	}
}

func TestSQLiteStateStore_MultipleChannels(t *testing.T) {
	db := openTestDB(t)
	store := storage.NewSQLiteStateStore(db)
	ctx := context.Background()

	cursors := map[string]model.Cursor{
		"ch-a": {MessageID: "a1", FolderID: "", UpdatedAt: time.Unix(100, 0).UTC()},
		"ch-b": {MessageID: "b1", FolderID: "INBOX", UpdatedAt: time.Unix(200, 0).UTC()},
	}
	for id, c := range cursors {
		if err := store.SaveCursor(ctx, id, c); err != nil {
			t.Fatalf("SaveCursor(%q): %v", id, err)
		}
	}

	for id, want := range cursors {
		got, err := store.GetCursor(ctx, id)
		if err != nil {
			t.Fatalf("GetCursor(%q): %v", id, err)
		}
		if got == nil {
			t.Fatalf("GetCursor(%q) returned nil", id)
		}
		if got.MessageID != want.MessageID {
			t.Errorf("ch %q MessageID: want %q, got %q", id, want.MessageID, got.MessageID)
		}
	}
}
