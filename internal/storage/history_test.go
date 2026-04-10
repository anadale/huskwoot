package storage_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/storage"
)

func newTestHistory(t *testing.T, opts storage.SQLiteHistoryOptions) *storage.SQLiteHistory {
	t.Helper()
	db, err := storage.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return storage.NewSQLiteHistory(db, opts)
}

func makeEntry(author, text string, ts time.Time) model.HistoryEntry {
	return model.HistoryEntry{AuthorName: author, Text: text, Timestamp: ts}
}

func TestSQLiteHistory_Add_Recent_Roundtrip(t *testing.T) {
	ctx := context.Background()
	h := newTestHistory(t, storage.SQLiteHistoryOptions{})

	now := time.Now().UTC().Truncate(time.Second)
	entries := []model.HistoryEntry{
		makeEntry("Alice", "привет", now),
		makeEntry("Bob", "как дела?", now.Add(time.Minute)),
		makeEntry("Alice", "хорошо", now.Add(2*time.Minute)),
	}

	for _, e := range entries {
		if err := h.Add(ctx, "chat1", e); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	got, err := h.Recent(ctx, "chat1", 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %d", len(got))
	}
	for i, e := range entries {
		if got[i].AuthorName != e.AuthorName || got[i].Text != e.Text || !got[i].Timestamp.Equal(e.Timestamp) {
			t.Errorf("entry %d: got %+v, want %+v", i, got[i], e)
		}
	}
}

func TestSQLiteHistory_Recent_LimitApplied(t *testing.T) {
	ctx := context.Background()
	h := newTestHistory(t, storage.SQLiteHistoryOptions{})

	now := time.Now().UTC().Truncate(time.Second)
	for i := range 10 {
		e := makeEntry("user", "msg", now.Add(time.Duration(i)*time.Minute))
		if err := h.Add(ctx, "chat1", e); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	got, err := h.Recent(ctx, "chat1", 3)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %d", len(got))
	}
	// The last 3 entries must be returned in chronological order.
	wantFirst := now.Add(7 * time.Minute)
	if !got[0].Timestamp.Equal(wantFirst) {
		t.Errorf("first entry: got %v, want %v", got[0].Timestamp, wantFirst)
	}
}

func TestSQLiteHistory_Recent_EmptySource(t *testing.T) {
	ctx := context.Background()
	h := newTestHistory(t, storage.SQLiteHistoryOptions{})

	got, err := h.Recent(ctx, "nonexistent", 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if got != nil {
		t.Errorf("want nil, got %v", got)
	}
}

func TestSQLiteHistory_Recent_ZeroLimit(t *testing.T) {
	ctx := context.Background()
	h := newTestHistory(t, storage.SQLiteHistoryOptions{})

	if err := h.Add(ctx, "chat1", makeEntry("u", "t", time.Now())); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, err := h.Recent(ctx, "chat1", 0)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if got != nil {
		t.Errorf("want nil when limit=0, got %v", got)
	}
}

func TestSQLiteHistory_RecentActivity_FindsGap(t *testing.T) {
	ctx := context.Background()
	h := newTestHistory(t, storage.SQLiteHistoryOptions{})

	base := time.Now().UTC().Truncate(time.Second)
	// First activity wave (old messages).
	for i := range 3 {
		e := makeEntry("user", "старое", base.Add(time.Duration(i)*time.Minute))
		if err := h.Add(ctx, "chat1", e); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	// 10-minute gap.
	newBase := base.Add(3*time.Minute + 10*time.Minute)
	// Second wave (new messages).
	for i := range 2 {
		e := makeEntry("user", "новое", newBase.Add(time.Duration(i)*time.Minute))
		if err := h.Add(ctx, "chat1", e); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	got, err := h.RecentActivity(ctx, "chat1", 5*time.Minute, 20)
	if err != nil {
		t.Fatalf("RecentActivity: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 entries after pause, got %d", len(got))
	}
	if got[0].Text != "новое" {
		t.Errorf("want text 'новое', got %q", got[0].Text)
	}
}

func TestSQLiteHistory_RecentActivity_FallbackWhenNoGap(t *testing.T) {
	ctx := context.Background()
	h := newTestHistory(t, storage.SQLiteHistoryOptions{})

	base := time.Now().UTC().Truncate(time.Second)
	// All messages in a row, no large gap.
	for i := range 7 {
		e := makeEntry("user", "msg", base.Add(time.Duration(i)*time.Minute))
		if err := h.Add(ctx, "chat1", e); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	got, err := h.RecentActivity(ctx, "chat1", 10*time.Minute, 3)
	if err != nil {
		t.Fatalf("RecentActivity: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want fallback 3 entries, got %d", len(got))
	}
	// The last 3 entries must be returned.
	wantFirst := base.Add(4 * time.Minute)
	if !got[0].Timestamp.Equal(wantFirst) {
		t.Errorf("first entry: got %v, want %v", got[0].Timestamp, wantFirst)
	}
}

func TestSQLiteHistory_RecentActivity_EmptySource(t *testing.T) {
	ctx := context.Background()
	h := newTestHistory(t, storage.SQLiteHistoryOptions{})

	got, err := h.RecentActivity(ctx, "nonexistent", 5*time.Minute, 10)
	if err != nil {
		t.Fatalf("RecentActivity: %v", err)
	}
	if got != nil {
		t.Errorf("want nil, got %v", got)
	}
}

func TestSQLiteHistory_TTL_RemovesOldEntries(t *testing.T) {
	ctx := context.Background()
	ttl := 2 * time.Second
	h := newTestHistory(t, storage.SQLiteHistoryOptions{TTL: ttl})

	base := time.Now().UTC().Truncate(time.Second)
	// Add an old entry (4 seconds ago — past the TTL).
	old := makeEntry("user", "старое", base.Add(-4*time.Second))
	if err := h.Add(ctx, "chat1", old); err != nil {
		t.Fatalf("Add old: %v", err)
	}
	// Add a fresh entry — this triggers the TTL cleanup.
	fresh := makeEntry("user", "новое", base)
	if err := h.Add(ctx, "chat1", fresh); err != nil {
		t.Fatalf("Add fresh: %v", err)
	}

	got, err := h.Recent(ctx, "chat1", 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	// The old entry must be deleted.
	for _, e := range got {
		if e.Text == "старое" {
			t.Errorf("stale entry was not deleted: %+v", e)
		}
	}
	// The fresh entry must remain.
	found := false
	for _, e := range got {
		if e.Text == "новое" {
			found = true
			break
		}
	}
	if !found {
		t.Error("fresh entry must remain after TTL cleanup")
	}
}

func TestSQLiteHistory_MaxMessages_LimitsPerSource(t *testing.T) {
	ctx := context.Background()
	max := 3
	h := newTestHistory(t, storage.SQLiteHistoryOptions{MaxMessages: max})

	now := time.Now().UTC().Truncate(time.Second)
	for i := range 6 {
		e := makeEntry("user", "msg", now.Add(time.Duration(i)*time.Minute))
		if err := h.Add(ctx, "chat1", e); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	got, err := h.Recent(ctx, "chat1", 100)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != max {
		t.Errorf("want %d entries, got %d", max, len(got))
	}
	// The last 3 entries must remain.
	wantFirst := now.Add(3 * time.Minute)
	if !got[0].Timestamp.Equal(wantFirst) {
		t.Errorf("first entry: got %v, want %v", got[0].Timestamp, wantFirst)
	}
}

func TestSQLiteHistory_MaxMessages_IndependentPerSource(t *testing.T) {
	ctx := context.Background()
	h := newTestHistory(t, storage.SQLiteHistoryOptions{MaxMessages: 2})

	now := time.Now().UTC().Truncate(time.Second)
	for i := range 4 {
		e := makeEntry("user", "msg", now.Add(time.Duration(i)*time.Minute))
		if err := h.Add(ctx, "chat1", e); err != nil {
			t.Fatalf("Add chat1: %v", err)
		}
		if err := h.Add(ctx, "chat2", e); err != nil {
			t.Fatalf("Add chat2: %v", err)
		}
	}

	for _, src := range []string{"chat1", "chat2"} {
		got, err := h.Recent(ctx, src, 100)
		if err != nil {
			t.Fatalf("Recent %s: %v", src, err)
		}
		if len(got) != 2 {
			t.Errorf("source %s: want 2 entries, got %d", src, len(got))
		}
	}
}
