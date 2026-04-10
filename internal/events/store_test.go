package events_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/events"
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

// insertEvents opens a tx, inserts events, commits, and returns the seqs.
func insertEvents(t *testing.T, db *sql.DB, store *events.SQLiteEventStore, evs ...model.Event) []int64 {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	seqs := make([]int64, 0, len(evs))
	for _, ev := range evs {
		seq, err := store.Insert(ctx, tx, ev)
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("Insert(%s): %v", ev.Kind, err)
		}
		seqs = append(seqs, seq)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return seqs
}

func newEvent(kind model.EventKind, entityID string, payload map[string]any) model.Event {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return model.Event{Kind: kind, EntityID: entityID, Payload: raw}
}

func TestEventStoreInsertReturnsMonotonicSeq(t *testing.T) {
	db := openTestDB(t)
	store := events.NewSQLiteEventStore(db)

	seqs := insertEvents(t, db, store,
		newEvent(model.EventTaskCreated, "task-1", map[string]any{"id": "task-1"}),
		newEvent(model.EventTaskUpdated, "task-1", map[string]any{"summary": "обновление"}),
		newEvent(model.EventTaskCompleted, "task-1", map[string]any{"status": "done"}),
	)

	if len(seqs) != 3 {
		t.Fatalf("got %d seq, want 3", len(seqs))
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("seq is not monotonic: %v", seqs)
		}
	}
}

func TestEventStoreInsertNilTxReturnsError(t *testing.T) {
	db := openTestDB(t)
	store := events.NewSQLiteEventStore(db)

	_, err := store.Insert(context.Background(), nil,
		newEvent(model.EventTaskCreated, "task-1", map[string]any{"k": "v"}))
	if err == nil {
		t.Fatal("Insert with nil tx should return an error")
	}
}

func TestEventStoreInsertRolledBack(t *testing.T) {
	db := openTestDB(t)
	store := events.NewSQLiteEventStore(db)
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if _, err := store.Insert(ctx, tx,
		newEvent(model.EventTaskCreated, "task-1", map[string]any{"k": "v"})); err != nil {
		_ = tx.Rollback()
		t.Fatalf("Insert: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	max, err := store.MaxSeq(ctx)
	if err != nil {
		t.Fatalf("MaxSeq: %v", err)
	}
	if max != 0 {
		t.Fatalf("after rollback MaxSeq = %d, want 0", max)
	}
}

func TestEventStoreSinceSeqReturnsOrdered(t *testing.T) {
	db := openTestDB(t)
	store := events.NewSQLiteEventStore(db)
	ctx := context.Background()

	seqs := insertEvents(t, db, store,
		newEvent(model.EventTaskCreated, "t1", map[string]any{"n": 1}),
		newEvent(model.EventTaskCreated, "t2", map[string]any{"n": 2}),
		newEvent(model.EventTaskCreated, "t3", map[string]any{"n": 3}),
		newEvent(model.EventTaskCreated, "t4", map[string]any{"n": 4}),
	)

	got, err := store.SinceSeq(ctx, seqs[0], 0)
	if err != nil {
		t.Fatalf("SinceSeq: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("SinceSeq returned %d events, want 3", len(got))
	}
	for i, ev := range got {
		if ev.Seq != seqs[i+1] {
			t.Fatalf("got[%d].Seq = %d, want %d", i, ev.Seq, seqs[i+1])
		}
	}

	// Verify payload.
	var payload struct {
		N int `json:"n"`
	}
	if err := json.Unmarshal(got[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.N != 2 {
		t.Fatalf("payload.N = %d, want 2", payload.N)
	}
}

func TestEventStoreSinceSeqLimit(t *testing.T) {
	db := openTestDB(t)
	store := events.NewSQLiteEventStore(db)
	ctx := context.Background()

	insertEvents(t, db, store,
		newEvent(model.EventTaskCreated, "t1", map[string]any{"n": 1}),
		newEvent(model.EventTaskCreated, "t2", map[string]any{"n": 2}),
		newEvent(model.EventTaskCreated, "t3", map[string]any{"n": 3}),
	)

	got, err := store.SinceSeq(ctx, 0, 2)
	if err != nil {
		t.Fatalf("SinceSeq: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("SinceSeq with limit=2 returned %d events, want 2", len(got))
	}
}

func TestEventStoreSinceSeqEmptyResult(t *testing.T) {
	db := openTestDB(t)
	store := events.NewSQLiteEventStore(db)
	ctx := context.Background()

	got, err := store.SinceSeq(ctx, 42, 0)
	if err != nil {
		t.Fatalf("SinceSeq: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("SinceSeq on empty table = %v, want empty slice", got)
	}
}

func TestEventStoreMaxSeqEmptyTable(t *testing.T) {
	db := openTestDB(t)
	store := events.NewSQLiteEventStore(db)

	max, err := store.MaxSeq(context.Background())
	if err != nil {
		t.Fatalf("MaxSeq: %v", err)
	}
	if max != 0 {
		t.Fatalf("MaxSeq on empty table = %d, want 0", max)
	}
}

func TestEventStoreMaxSeqAfterInserts(t *testing.T) {
	db := openTestDB(t)
	store := events.NewSQLiteEventStore(db)

	seqs := insertEvents(t, db, store,
		newEvent(model.EventTaskCreated, "t1", map[string]any{}),
		newEvent(model.EventTaskCreated, "t2", map[string]any{}),
	)

	max, err := store.MaxSeq(context.Background())
	if err != nil {
		t.Fatalf("MaxSeq: %v", err)
	}
	if max != seqs[len(seqs)-1] {
		t.Fatalf("MaxSeq = %d, want %d", max, seqs[len(seqs)-1])
	}
}

func TestEventStoreDeleteOlderThan(t *testing.T) {
	db := openTestDB(t)
	store := events.NewSQLiteEventStore(db)
	ctx := context.Background()

	old := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

	evOld := newEvent(model.EventTaskCreated, "old", map[string]any{})
	evOld.CreatedAt = old
	evRecent := newEvent(model.EventTaskCreated, "recent", map[string]any{})
	evRecent.CreatedAt = recent

	insertEvents(t, db, store, evOld, evRecent)

	cutoff := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	n, err := store.DeleteOlderThan(ctx, cutoff)
	if err != nil {
		t.Fatalf("DeleteOlderThan: %v", err)
	}
	if n != 1 {
		t.Fatalf("DeleteOlderThan deleted %d rows, want 1", n)
	}

	// Verify that the remaining event is the recent one.
	remaining, err := store.SinceSeq(ctx, 0, 0)
	if err != nil {
		t.Fatalf("SinceSeq: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("after cleanup remaining events = %d, want 1", len(remaining))
	}
	if remaining[0].EntityID != "recent" {
		t.Fatalf("after cleanup EntityID = %q, want %q", remaining[0].EntityID, "recent")
	}
}

func TestEventStoreDeleteOlderThanNoMatches(t *testing.T) {
	db := openTestDB(t)
	store := events.NewSQLiteEventStore(db)
	ctx := context.Background()

	ev := newEvent(model.EventTaskCreated, "recent", map[string]any{})
	ev.CreatedAt = time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	insertEvents(t, db, store, ev)

	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	n, err := store.DeleteOlderThan(ctx, cutoff)
	if err != nil {
		t.Fatalf("DeleteOlderThan: %v", err)
	}
	if n != 0 {
		t.Fatalf("DeleteOlderThan deleted %d rows, want 0", n)
	}
}

func TestEventStore_GetBySeq_Returns_Event(t *testing.T) {
	db := openTestDB(t)
	store := events.NewSQLiteEventStore(db)

	seqs := insertEvents(t, db, store,
		newEvent(model.EventTaskCreated, "task-1", map[string]any{"x": 1}),
		newEvent(model.EventTaskUpdated, "task-1", map[string]any{"x": 2}),
	)

	got, err := store.GetBySeq(context.Background(), seqs[0])
	if err != nil {
		t.Fatalf("GetBySeq: %v", err)
	}
	if got == nil {
		t.Fatal("GetBySeq returned nil")
	}
	if got.Seq != seqs[0] {
		t.Fatalf("Seq = %d, want %d", got.Seq, seqs[0])
	}
	if got.Kind != model.EventTaskCreated {
		t.Fatalf("Kind = %q, want %q", got.Kind, model.EventTaskCreated)
	}
	if got.EntityID != "task-1" {
		t.Fatalf("EntityID = %q, want %q", got.EntityID, "task-1")
	}
}

func TestEventStore_GetBySeq_ReturnsNilForMissing(t *testing.T) {
	db := openTestDB(t)
	store := events.NewSQLiteEventStore(db)

	got, err := store.GetBySeq(context.Background(), 9999)
	if err != nil {
		t.Fatalf("GetBySeq: %v", err)
	}
	if got != nil {
		t.Fatalf("GetBySeq should return nil for missing seq, got %+v", got)
	}
}

func TestEventStoreInsertRequiresKindAndPayload(t *testing.T) {
	db := openTestDB(t)
	store := events.NewSQLiteEventStore(db)
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := store.Insert(ctx, tx,
		model.Event{Kind: "", EntityID: "x", Payload: json.RawMessage(`{}`)}); err == nil {
		t.Error("Insert without Kind should return an error")
	}
	if _, err := store.Insert(ctx, tx,
		model.Event{Kind: model.EventTaskCreated, EntityID: "x"}); err == nil {
		t.Error("Insert without Payload should return an error")
	}
}
