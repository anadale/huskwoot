package storage_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"

	"github.com/anadale/huskwoot/internal/storage"
)

// setMetaHelper wraps store.SetTx in a short transaction for test use.
func setMetaHelper(t *testing.T, db *sql.DB, store *storage.SQLiteMetaStore, key, value string) error {
	t.Helper()
	return withTx(t, db, func(tx *sql.Tx) error {
		return store.SetTx(context.Background(), tx, key, value)
	})
}

func TestSQLiteMetaStore_Get_EmptyDB(t *testing.T) {
	db := openTestDB(t)
	store := storage.NewSQLiteMetaStore(db)

	value, err := store.Get(context.Background(), "project:ch1")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if value != "" {
		t.Errorf("want empty string, got %q", value)
	}
}

func TestSQLiteMetaStore_SetAndGet(t *testing.T) {
	db := openTestDB(t)
	store := storage.NewSQLiteMetaStore(db)
	ctx := context.Background()

	if err := setMetaHelper(t, db, store, "project:ch1", "Бекенд"); err != nil {
		t.Fatalf("SetTx: %v", err)
	}

	got, err := store.Get(ctx, "project:ch1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "Бекенд" {
		t.Errorf("want %q, got %q", "Бекенд", got)
	}
}

func TestSQLiteMetaStore_Set_Overwrite(t *testing.T) {
	db := openTestDB(t)
	store := storage.NewSQLiteMetaStore(db)
	ctx := context.Background()

	if err := setMetaHelper(t, db, store, "project:ch1", "Старый проект"); err != nil {
		t.Fatalf("first SetTx: %v", err)
	}
	if err := setMetaHelper(t, db, store, "project:ch1", "Новый проект"); err != nil {
		t.Fatalf("second SetTx: %v", err)
	}

	got, err := store.Get(ctx, "project:ch1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "Новый проект" {
		t.Errorf("want %q, got %q", "Новый проект", got)
	}
}

func TestSQLiteMetaStore_Get_UnknownKey_ReturnsEmpty(t *testing.T) {
	db := openTestDB(t)
	store := storage.NewSQLiteMetaStore(db)

	// Keys without the "project:" prefix are not supported — return "".
	got, err := store.Get(context.Background(), "unknown:ch1")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got != "" {
		t.Errorf("want empty string, got %q", got)
	}
}

func TestSQLiteMetaStore_Set_UnsupportedKey_ReturnsError(t *testing.T) {
	db := openTestDB(t)
	store := storage.NewSQLiteMetaStore(db)

	err := setMetaHelper(t, db, store, "unknown:ch1", "значение")
	if err == nil {
		t.Error("SetTx with unsupported key should return an error")
	}
}

func TestSQLiteMetaStore_SetTx_NilTx_ReturnsError(t *testing.T) {
	db := openTestDB(t)
	store := storage.NewSQLiteMetaStore(db)

	err := store.SetTx(context.Background(), nil, "project:ch1", "Проект")
	if err == nil {
		t.Error("SetTx with nil tx should return an error")
	}
}

func TestMetaStoreSetTxRolledBack(t *testing.T) {
	db := openTestDB(t)
	store := storage.NewSQLiteMetaStore(db)
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := store.SetTx(ctx, tx, "project:ch-rb", "значение-в-tx"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("SetTx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	got, err := store.Get(ctx, "project:ch-rb")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "" {
		t.Fatalf("after rollback expected empty value, got %q", got)
	}
}

func TestSQLiteMetaStore_Values_WrongPrefix_ReturnsNil(t *testing.T) {
	db := openTestDB(t)
	store := storage.NewSQLiteMetaStore(db)
	ctx := context.Background()

	if err := setMetaHelper(t, db, store, "project:ch1", "Проект"); err != nil {
		t.Fatalf("SetTx: %v", err)
	}

	got, err := store.Values(ctx, "other:")
	if err != nil {
		t.Fatalf("Values returned error: %v", err)
	}
	if got != nil {
		t.Errorf("want nil for unsupported prefix, got %v", got)
	}
}

func TestSQLiteMetaStore_Values_Empty(t *testing.T) {
	db := openTestDB(t)
	store := storage.NewSQLiteMetaStore(db)

	values, err := store.Values(context.Background(), "project:")
	if err != nil {
		t.Fatalf("Values returned error: %v", err)
	}
	if values != nil {
		t.Errorf("want nil, got %v", values)
	}
}

func TestSQLiteMetaStore_Values_ReturnsUniqueProjects(t *testing.T) {
	db := openTestDB(t)
	store := storage.NewSQLiteMetaStore(db)
	ctx := context.Background()

	entries := map[string]string{
		"project:ch1": "Backend",
		"project:ch2": "Frontend",
		"project:ch3": "Backend", // duplicate value
	}
	for k, v := range entries {
		if err := setMetaHelper(t, db, store, k, v); err != nil {
			t.Fatalf("SetTx(%q): %v", k, err)
		}
	}

	values, err := store.Values(ctx, "project:")
	if err != nil {
		t.Fatalf("Values: %v", err)
	}

	// Only unique values must be returned.
	want := map[string]bool{"Backend": true, "Frontend": true}
	if len(values) != len(want) {
		t.Errorf("want %d unique values, got %d: %v", len(want), len(values), values)
	}
	for _, v := range values {
		if !want[v] {
			t.Errorf("unexpected value: %q", v)
		}
	}
}

func TestSQLiteMetaStore_Values_MultipleChannels(t *testing.T) {
	db := openTestDB(t)
	store := storage.NewSQLiteMetaStore(db)
	ctx := context.Background()

	projects := []string{"Альфа", "Бета", "Гамма"}
	for i, p := range projects {
		key := "project:ch" + string(rune('1'+i))
		if err := setMetaHelper(t, db, store, key, p); err != nil {
			t.Fatalf("SetTx: %v", err)
		}
	}

	values, err := store.Values(ctx, "project:")
	if err != nil {
		t.Fatalf("Values: %v", err)
	}
	if len(values) != len(projects) {
		t.Errorf("want %d values, got %d: %v", len(projects), len(values), values)
	}
}

func TestMetaStoreSetGetStringProjectID(t *testing.T) {
	db := openTestDB(t)
	s := storage.NewSQLiteMetaStore(db)
	pid := uuid.NewString()
	if err := setMetaHelper(t, db, s, "project:chat:42", pid); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(context.Background(), "project:chat:42")
	if err != nil {
		t.Fatal(err)
	}
	if got != pid {
		t.Fatalf("Get=%q, want %q", got, pid)
	}
}

func TestSQLiteMetaStore_IsolatedByChannel(t *testing.T) {
	db := openTestDB(t)
	store := storage.NewSQLiteMetaStore(db)
	ctx := context.Background()

	if err := setMetaHelper(t, db, store, "project:ch1", "Проект-А"); err != nil {
		t.Fatalf("SetTx ch1: %v", err)
	}
	if err := setMetaHelper(t, db, store, "project:ch2", "Проект-Б"); err != nil {
		t.Fatalf("SetTx ch2: %v", err)
	}

	got1, err := store.Get(ctx, "project:ch1")
	if err != nil {
		t.Fatalf("Get ch1: %v", err)
	}
	if got1 != "Проект-А" {
		t.Errorf("ch1: want %q, got %q", "Проект-А", got1)
	}

	got2, err := store.Get(ctx, "project:ch2")
	if err != nil {
		t.Fatalf("Get ch2: %v", err)
	}
	if got2 != "Проект-Б" {
		t.Errorf("ch2: want %q, got %q", "Проект-Б", got2)
	}
}
