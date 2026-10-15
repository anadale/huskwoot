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

func newTestForwardBuffer(t *testing.T) (*storage.SQLiteForwardBuffer, *sql.DB) {
	t.Helper()
	db, err := storage.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return storage.NewSQLiteForwardBuffer(db), db
}

func makeForwardEntry(chatID, msgID string, selfAuthored bool, origDate, receivedAt time.Time, text string) model.ForwardEntry {
	return model.ForwardEntry{
		ChatID:          chatID,
		MessageID:       msgID,
		ForwardFromID:   "user123",
		ForwardFromName: "Alice",
		IsSelfAuthored:  selfAuthored,
		OriginalDate:    origDate,
		ReceivedAt:      receivedAt,
		Text:            text,
	}
}

func TestSQLiteForwardBuffer_Append_Idempotent(t *testing.T) {
	ctx := context.Background()
	buf, db := newTestForwardBuffer(t)

	now := time.Now().UTC().Truncate(time.Second)
	entry := makeForwardEntry("chat1", "msg1", true, now, now, "hello")

	if err := buf.Append(ctx, entry); err != nil {
		t.Fatalf("first Append: %v", err)
	}
	if err := buf.Append(ctx, entry); err != nil {
		t.Fatalf("second Append (duplicate): %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM forward_buffer WHERE chat_id = ?", "chat1").Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Errorf("want 1 row after duplicate Append, got %d", count)
	}
}

func TestSQLiteForwardBuffer_Append_DifferentMessages(t *testing.T) {
	ctx := context.Background()
	buf, db := newTestForwardBuffer(t)

	now := time.Now().UTC().Truncate(time.Second)
	entries := []model.ForwardEntry{
		makeForwardEntry("chat1", "msg1", true, now, now, "first"),
		makeForwardEntry("chat1", "msg2", false, now.Add(time.Minute), now.Add(time.Minute), "second"),
	}
	for _, e := range entries {
		if err := buf.Append(ctx, e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM forward_buffer WHERE chat_id = ?", "chat1").Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 2 {
		t.Errorf("want 2 rows, got %d", count)
	}
}

func TestSQLiteForwardBuffer_LastReceivedAt_Empty(t *testing.T) {
	ctx := context.Background()
	buf, _ := newTestForwardBuffer(t)

	ts, ok, err := buf.LastReceivedAt(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("LastReceivedAt: %v", err)
	}
	if ok {
		t.Errorf("want ok=false for empty buffer, got ok=true with ts=%v", ts)
	}
	if !ts.IsZero() {
		t.Errorf("want zero time for empty buffer, got %v", ts)
	}
}

func TestSQLiteForwardBuffer_LastReceivedAt_AfterAppend(t *testing.T) {
	ctx := context.Background()
	buf, _ := newTestForwardBuffer(t)

	base := time.Now().UTC().Truncate(time.Second)
	entries := []model.ForwardEntry{
		makeForwardEntry("chat1", "msg1", true, base, base, "a"),
		makeForwardEntry("chat1", "msg2", false, base.Add(time.Minute), base.Add(2*time.Minute), "b"),
		makeForwardEntry("chat1", "msg3", false, base.Add(2*time.Minute), base.Add(time.Minute), "c"),
	}
	for _, e := range entries {
		if err := buf.Append(ctx, e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	ts, ok, err := buf.LastReceivedAt(ctx, "chat1")
	if err != nil {
		t.Fatalf("LastReceivedAt: %v", err)
	}
	if !ok {
		t.Fatal("want ok=true, got false")
	}
	wantMax := base.Add(2 * time.Minute)
	if !ts.Equal(wantMax) {
		t.Errorf("want max received_at=%v, got %v", wantMax, ts)
	}
}

func TestSQLiteForwardBuffer_LoadBatch_Order(t *testing.T) {
	ctx := context.Background()
	buf, _ := newTestForwardBuffer(t)

	base := time.Now().UTC().Truncate(time.Second)
	entries := []model.ForwardEntry{
		makeForwardEntry("chat1", "msg3", false, base.Add(2*time.Minute), base, "third"),
		makeForwardEntry("chat1", "msg1", true, base, base, "first"),
		makeForwardEntry("chat1", "msg2", false, base.Add(time.Minute), base, "second"),
	}
	for _, e := range entries {
		if err := buf.Append(ctx, e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	batch, err := buf.LoadBatch(ctx, "chat1")
	if err != nil {
		t.Fatalf("LoadBatch: %v", err)
	}
	if len(batch) != 3 {
		t.Fatalf("want 3 entries, got %d", len(batch))
	}

	wantOrder := []string{"first", "second", "third"}
	for i, want := range wantOrder {
		if batch[i].Text != want {
			t.Errorf("batch[%d].Text = %q, want %q", i, batch[i].Text, want)
		}
	}
}

func TestSQLiteForwardBuffer_LoadBatch_SelfAuthoredRoundtrip(t *testing.T) {
	ctx := context.Background()
	buf, _ := newTestForwardBuffer(t)

	now := time.Now().UTC().Truncate(time.Second)
	cases := []struct {
		msgID        string
		selfAuthored bool
	}{
		{"msg1", true},
		{"msg2", false},
	}
	for _, c := range cases {
		e := makeForwardEntry("chat1", c.msgID, c.selfAuthored, now, now, "text")
		if err := buf.Append(ctx, e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	batch, err := buf.LoadBatch(ctx, "chat1")
	if err != nil {
		t.Fatalf("LoadBatch: %v", err)
	}
	if len(batch) != 2 {
		t.Fatalf("want 2 entries, got %d", len(batch))
	}
	if batch[0].IsSelfAuthored != true {
		t.Errorf("batch[0].IsSelfAuthored: want true, got false")
	}
	if batch[1].IsSelfAuthored != false {
		t.Errorf("batch[1].IsSelfAuthored: want false, got true")
	}
}

func TestSQLiteForwardBuffer_LoadBatch_Empty(t *testing.T) {
	ctx := context.Background()
	buf, _ := newTestForwardBuffer(t)

	batch, err := buf.LoadBatch(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("LoadBatch: %v", err)
	}
	if len(batch) != 0 {
		t.Errorf("want empty batch, got %d entries", len(batch))
	}
}

func TestSQLiteForwardBuffer_DeleteEntriesTx_CommitDeletesOnlySnapshot(t *testing.T) {
	ctx := context.Background()
	buf, db := newTestForwardBuffer(t)

	now := time.Now().UTC().Truncate(time.Second)
	entries := []model.ForwardEntry{
		makeForwardEntry("chat1", "msg1", true, now, now, "a"),
		makeForwardEntry("chat1", "msg2", false, now.Add(time.Minute), now, "b"),
		makeForwardEntry("chat2", "msg3", true, now, now, "c"),
	}
	for _, e := range entries {
		if err := buf.Append(ctx, e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := buf.DeleteEntriesTx(ctx, tx, "chat1", []string{"msg1"}); err != nil {
		tx.Rollback() //nolint:errcheck
		t.Fatalf("DeleteEntriesTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	batch1, err := buf.LoadBatch(ctx, "chat1")
	if err != nil {
		t.Fatalf("LoadBatch chat1: %v", err)
	}
	if len(batch1) != 1 || batch1[0].MessageID != "msg2" {
		t.Errorf("chat1 should preserve the new arrival, got %v", batch1)
	}

	batch2, err := buf.LoadBatch(ctx, "chat2")
	if err != nil {
		t.Fatalf("LoadBatch chat2: %v", err)
	}
	if len(batch2) != 1 {
		t.Errorf("chat2 should still have 1 entry, got %d", len(batch2))
	}
}

func TestSQLiteForwardBuffer_DeleteEntriesTx_RollbackPreservesData(t *testing.T) {
	ctx := context.Background()
	buf, db := newTestForwardBuffer(t)

	now := time.Now().UTC().Truncate(time.Second)
	entry := makeForwardEntry("chat1", "msg1", true, now, now, "hello")
	if err := buf.Append(ctx, entry); err != nil {
		t.Fatalf("Append: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := buf.DeleteEntriesTx(ctx, tx, "chat1", []string{"msg1"}); err != nil {
		tx.Rollback() //nolint:errcheck
		t.Fatalf("DeleteEntriesTx: %v", err)
	}
	tx.Rollback() //nolint:errcheck

	batch, err := buf.LoadBatch(ctx, "chat1")
	if err != nil {
		t.Fatalf("LoadBatch: %v", err)
	}
	if len(batch) != 1 {
		t.Errorf("want 1 entry after rollback, got %d", len(batch))
	}
}

func TestSQLiteForwardBuffer_ListPendingChats(t *testing.T) {
	ctx := context.Background()
	buf, _ := newTestForwardBuffer(t)

	base := time.Now().UTC().Truncate(time.Second)
	// chat1: last received 5 minutes ago
	if err := buf.Append(ctx, makeForwardEntry("chat1", "m1", true, base.Add(-6*time.Minute), base.Add(-5*time.Minute), "old")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// chat2: last received 1 minute ago (recent)
	if err := buf.Append(ctx, makeForwardEntry("chat2", "m2", true, base.Add(-2*time.Minute), base.Add(-time.Minute), "recent")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// chat3: two entries, last one 10 minutes ago
	if err := buf.Append(ctx, makeForwardEntry("chat3", "m3", true, base.Add(-12*time.Minute), base.Add(-11*time.Minute), "old1")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := buf.Append(ctx, makeForwardEntry("chat3", "m4", false, base.Add(-11*time.Minute), base.Add(-10*time.Minute), "old2")); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// threshold: 3 minutes ago — chat1 and chat3 qualify; chat2 does not
	threshold := base.Add(-3 * time.Minute)
	pending, err := buf.ListPendingChats(ctx, threshold)
	if err != nil {
		t.Fatalf("ListPendingChats: %v", err)
	}

	pendingSet := make(map[string]bool, len(pending))
	for _, id := range pending {
		pendingSet[id] = true
	}
	if !pendingSet["chat1"] {
		t.Error("chat1 should be pending")
	}
	if pendingSet["chat2"] {
		t.Error("chat2 should NOT be pending (received too recently)")
	}
	if !pendingSet["chat3"] {
		t.Error("chat3 should be pending")
	}
}

func TestSQLiteForwardBuffer_ListPendingChats_Empty(t *testing.T) {
	ctx := context.Background()
	buf, _ := newTestForwardBuffer(t)

	pending, err := buf.ListPendingChats(ctx, time.Now())
	if err != nil {
		t.Fatalf("ListPendingChats: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("want empty result, got %d chats", len(pending))
	}
}
