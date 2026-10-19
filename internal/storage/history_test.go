package storage_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/storage"
	"github.com/anadale/huskwoot/internal/storage/migrations"
)

func newTestHistoryDB(t *testing.T, opts storage.SQLiteHistoryOptions) (*storage.SQLiteHistory, *sql.DB) {
	t.Helper()
	db, err := storage.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return storage.NewSQLiteHistory(db, opts), db
}

func newTestHistory(t *testing.T, opts storage.SQLiteHistoryOptions) *storage.SQLiteHistory {
	t.Helper()
	h, _ := newTestHistoryDB(t, opts)
	return h
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

func TestSQLiteHistory_AddTx_CommitVisibleViaRecent(t *testing.T) {
	ctx := context.Background()
	h, db := newTestHistoryDB(t, storage.SQLiteHistoryOptions{})

	now := time.Now().UTC().Truncate(time.Second)
	entry := makeEntry("Alice", "транзакционное сообщение", now)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	msgID, err := h.AddTx(ctx, tx, "chat1", entry)
	if err != nil {
		tx.Rollback()
		t.Fatalf("AddTx: %v", err)
	}
	if msgID == "" {
		tx.Rollback()
		t.Fatal("AddTx: пустой messageID")
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	got, err := h.Recent(ctx, "chat1", 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
	if got[0].Text != entry.Text {
		t.Errorf("text: got %q, want %q", got[0].Text, entry.Text)
	}

	// Проверяем что message_id в БД совпадает с возвращённым.
	var storedID string
	if err := db.QueryRowContext(ctx, `SELECT message_id FROM messages WHERE source_id = ?`, "chat1").Scan(&storedID); err != nil {
		t.Fatalf("SELECT message_id: %v", err)
	}
	if storedID != msgID {
		t.Errorf("stored message_id %q != returned %q", storedID, msgID)
	}
}

func TestSQLiteHistory_AddTx_RollbackNotVisible(t *testing.T) {
	ctx := context.Background()
	h, db := newTestHistoryDB(t, storage.SQLiteHistoryOptions{})

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	if _, err := h.AddTx(ctx, tx, "chat1", makeEntry("Bob", "откатится", time.Now())); err != nil {
		tx.Rollback()
		t.Fatalf("AddTx: %v", err)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	got, err := h.Recent(ctx, "chat1", 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("после Rollback хотим 0 записей, получили %d", len(got))
	}
}

func TestSQLiteHistory_Add_FillsMessageID(t *testing.T) {
	ctx := context.Background()
	h, db := newTestHistoryDB(t, storage.SQLiteHistoryOptions{})

	if err := h.Add(ctx, "chat1", makeEntry("Carol", "через Add", time.Now())); err != nil {
		t.Fatalf("Add: %v", err)
	}

	var msgID string
	if err := db.QueryRowContext(ctx, `SELECT message_id FROM messages WHERE source_id = ?`, "chat1").Scan(&msgID); err != nil {
		t.Fatalf("SELECT message_id: %v", err)
	}
	if msgID == "" {
		t.Error("Add должен заполнять message_id")
	}
}

func TestSQLiteHistory_AddTx_TwoCalls_DifferentMessageIDs(t *testing.T) {
	ctx := context.Background()
	h, db := newTestHistoryDB(t, storage.SQLiteHistoryOptions{})

	now := time.Now().UTC().Truncate(time.Second)

	tx1, _ := db.BeginTx(ctx, nil)
	id1, err := h.AddTx(ctx, tx1, "chat1", makeEntry("u", "первое", now))
	if err != nil {
		tx1.Rollback()
		t.Fatalf("AddTx 1: %v", err)
	}
	tx1.Commit()

	tx2, _ := db.BeginTx(ctx, nil)
	id2, err := h.AddTx(ctx, tx2, "chat1", makeEntry("u", "второе", now.Add(time.Second)))
	if err != nil {
		tx2.Rollback()
		t.Fatalf("AddTx 2: %v", err)
	}
	tx2.Commit()

	if id1 == id2 {
		t.Errorf("два вызова AddTx вернули одинаковый messageID: %q", id1)
	}
	if id1 == "" || id2 == "" {
		t.Error("messageID не должен быть пустым")
	}
}

// addTxCommitted writes entry through AddTx in its own transaction and returns the message id.
func addTxCommitted(t *testing.T, h *storage.SQLiteHistory, db *sql.DB, source string, entry model.HistoryEntry) string {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	id, err := h.AddTx(ctx, tx, source, entry)
	if err != nil {
		t.Fatalf("AddTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return id
}

func TestSQLiteHistory_AddTx_MetadataRoundTrip(t *testing.T) {
	ctx := context.Background()
	h, db := newTestHistoryDB(t, storage.SQLiteHistoryOptions{})
	now := time.Now().UTC().Truncate(time.Second)

	tests := []struct {
		name  string
		entry model.HistoryEntry
		want  model.HistoryEntry
	}{
		{
			name: "voice transcript",
			entry: model.HistoryEntry{
				AuthorName: "dev-1", Text: "call mom", Timestamp: now,
				Role: model.HistoryRoleUser, Kind: model.HistoryKindVoice,
				AudioJobID: "job-1", AudioDurationMs: 9000,
			},
			want: model.HistoryEntry{
				AuthorName: "dev-1", Text: "call mom", Timestamp: now,
				Role: model.HistoryRoleUser, Kind: model.HistoryKindVoice,
				AudioJobID: "job-1", AudioDurationMs: 9000,
			},
		},
		{
			name: "agent reply with duplicate and empty task ids",
			entry: model.HistoryEntry{
				AuthorName: "agent", Text: "done", Timestamp: now.Add(time.Second),
				Role: model.HistoryRoleAgent, Kind: model.HistoryKindText,
				TaskIDs: []string{"t-1", "", "t-2", "t-1"},
			},
			want: model.HistoryEntry{
				AuthorName: "agent", Text: "done", Timestamp: now.Add(time.Second),
				Role: model.HistoryRoleAgent, Kind: model.HistoryKindText,
				TaskIDs: []string{"t-1", "t-2"},
			},
		},
		{
			name: "empty kind defaults to text",
			entry: model.HistoryEntry{
				AuthorName: "dev-1", Text: "hello", Timestamp: now.Add(2 * time.Second),
				Role: model.HistoryRoleUser,
			},
			want: model.HistoryEntry{
				AuthorName: "dev-1", Text: "hello", Timestamp: now.Add(2 * time.Second),
				Role: model.HistoryRoleUser, Kind: model.HistoryKindText,
			},
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := "client:dev-roundtrip-" + string(rune('a'+i))
			id := addTxCommitted(t, h, db, source, tt.entry)

			got, err := h.Recent(ctx, source, 10)
			if err != nil {
				t.Fatalf("Recent: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("want 1 entry, got %d", len(got))
			}
			want := tt.want
			want.MessageID = id
			if !reflect.DeepEqual(got[0], want) {
				t.Errorf("entry:\n got %+v\nwant %+v", got[0], want)
			}
		})
	}
}

func TestSQLiteHistory_Add_ChannelRowsKeepDefaults(t *testing.T) {
	ctx := context.Background()
	h, db := newTestHistoryDB(t, storage.SQLiteHistoryOptions{})
	now := time.Now().UTC().Truncate(time.Second)

	if err := h.Add(ctx, "-100123", makeEntry("Alice", "I'll do it", now)); err != nil {
		t.Fatalf("Add: %v", err)
	}

	var role, kind, audioJobID, taskIDs string
	var durationMs int64
	if err := db.QueryRowContext(ctx,
		`SELECT role, kind, audio_job_id, audio_duration_ms, task_ids FROM messages WHERE source_id = ?`, "-100123",
	).Scan(&role, &kind, &audioJobID, &durationMs, &taskIDs); err != nil {
		t.Fatalf("select: %v", err)
	}
	if role != "" || kind != model.HistoryKindText || audioJobID != "" || durationMs != 0 || taskIDs != "" {
		t.Errorf("channel row columns: role=%q kind=%q audio_job_id=%q audio_duration_ms=%d task_ids=%q",
			role, kind, audioJobID, durationMs, taskIDs)
	}

	got, err := h.Recent(ctx, "-100123", 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
	if got[0].Role != "" || got[0].Kind != model.HistoryKindText || got[0].TaskIDs != nil || got[0].MessageID == "" {
		t.Errorf("channel entry: %+v", got[0])
	}
}

func TestSQLiteHistory_Recent_MalformedTaskIDsYieldNil(t *testing.T) {
	ctx := context.Background()
	h, db := newTestHistoryDB(t, storage.SQLiteHistoryOptions{})

	id := addTxCommitted(t, h, db, "client:dev-1", model.HistoryEntry{
		AuthorName: "agent", Text: "done", Timestamp: time.Now(),
		Role: model.HistoryRoleAgent, TaskIDs: []string{"t-1"},
	})
	if _, err := db.ExecContext(ctx, `UPDATE messages SET task_ids = '{broken' WHERE message_id = ?`, id); err != nil {
		t.Fatalf("corrupt task_ids: %v", err)
	}

	got, err := h.Recent(ctx, "client:dev-1", 10)
	if err != nil {
		t.Fatalf("Recent must not fail on malformed task_ids: %v", err)
	}
	if len(got) != 1 || got[0].TaskIDs != nil {
		t.Fatalf("want one entry with nil TaskIDs, got %+v", got)
	}
}

func TestSQLiteHistory_SetTaskIDs(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		initial []string
		add     []string
		want    []string
	}{
		{name: "no ids yet", initial: nil, add: []string{"t-1", "t-2"}, want: []string{"t-1", "t-2"}},
		{name: "merge keeps order and skips duplicates", initial: []string{"t-1", "t-2"}, add: []string{"t-3", "t-1", "", "t-3"}, want: []string{"t-1", "t-2", "t-3"}},
		{name: "all ids already present", initial: []string{"t-1", "t-2"}, add: []string{"t-2", "t-1"}, want: []string{"t-1", "t-2"}},
		{name: "empty input is a no-op", initial: []string{"t-1"}, add: nil, want: []string{"t-1"}},
		{name: "only empty strings is a no-op", initial: nil, add: []string{""}, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, db := newTestHistoryDB(t, storage.SQLiteHistoryOptions{})
			id := addTxCommitted(t, h, db, "client:dev-1", model.HistoryEntry{
				AuthorName: "agent", Text: "Готово", Timestamp: time.Now(),
				Role: model.HistoryRoleAgent, TaskIDs: tt.initial,
			})

			if err := h.SetTaskIDs(ctx, id, tt.add); err != nil {
				t.Fatalf("SetTaskIDs: %v", err)
			}

			got, err := h.Recent(ctx, "client:dev-1", 10)
			if err != nil {
				t.Fatalf("Recent: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("want 1 entry, got %d", len(got))
			}
			if !reflect.DeepEqual(got[0].TaskIDs, tt.want) {
				t.Errorf("TaskIDs = %v, want %v", got[0].TaskIDs, tt.want)
			}
		})
	}
}

func TestSQLiteHistory_SetTaskIDs_ReplacesMalformedValue(t *testing.T) {
	ctx := context.Background()
	h, db := newTestHistoryDB(t, storage.SQLiteHistoryOptions{})
	id := addTxCommitted(t, h, db, "client:dev-1", model.HistoryEntry{
		AuthorName: "agent", Text: "done", Timestamp: time.Now(), Role: model.HistoryRoleAgent,
	})
	if _, err := db.ExecContext(ctx, `UPDATE messages SET task_ids = 'not json' WHERE message_id = ?`, id); err != nil {
		t.Fatalf("corrupt task_ids: %v", err)
	}

	if err := h.SetTaskIDs(ctx, id, []string{"t-9"}); err != nil {
		t.Fatalf("SetTaskIDs: %v", err)
	}

	got, err := h.Recent(ctx, "client:dev-1", 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0].TaskIDs, []string{"t-9"}) {
		t.Fatalf("want TaskIDs [t-9], got %+v", got)
	}
}

func TestSQLiteHistory_SetTaskIDs_UnknownMessage(t *testing.T) {
	h := newTestHistory(t, storage.SQLiteHistoryOptions{})

	err := h.SetTaskIDs(context.Background(), "missing-id", []string{"t-1"})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("want error wrapping sql.ErrNoRows, got %v", err)
	}
}

func TestSQLiteHistory_ClientCap_TrimsOldestRowsOfSameSource(t *testing.T) {
	ctx := context.Background()
	h, db := newTestHistoryDB(t, storage.SQLiteHistoryOptions{})
	base := time.Now().UTC().Truncate(time.Second).Add(-24 * time.Hour)

	const extra = 3
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	for i := range storage.ClientHistoryCap + extra {
		ts := base.Add(time.Duration(i) * time.Second)
		if _, err := h.AddTx(ctx, tx, "client:dev-1", makeEntry("dev-1", fmt.Sprintf("dev1-%04d", i), ts)); err != nil {
			t.Fatalf("AddTx client dev-1 #%d: %v", i, err)
		}
		// A channel source above the cap must not be trimmed.
		if _, err := h.AddTx(ctx, tx, "-100123", makeEntry("Alice", fmt.Sprintf("chan-%04d", i), ts)); err != nil {
			t.Fatalf("AddTx channel #%d: %v", i, err)
		}
	}
	for i := range 2 {
		ts := base.Add(time.Duration(i) * time.Second)
		if _, err := h.AddTx(ctx, tx, "client:dev-2", makeEntry("dev-2", fmt.Sprintf("dev2-%d", i), ts)); err != nil {
			t.Fatalf("AddTx client dev-2 #%d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	count := func(source string) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE source_id = ?`, source).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", source, err)
		}
		return n
	}
	if got := count("client:dev-1"); got != storage.ClientHistoryCap {
		t.Errorf("client:dev-1 rows = %d, want %d", got, storage.ClientHistoryCap)
	}
	if got := count("client:dev-2"); got != 2 {
		t.Errorf("client:dev-2 rows = %d, want 2", got)
	}
	if got := count("-100123"); got != storage.ClientHistoryCap+extra {
		t.Errorf("channel rows = %d, want %d", got, storage.ClientHistoryCap+extra)
	}

	var oldest string
	if err := db.QueryRowContext(ctx,
		`SELECT text FROM messages WHERE source_id = ? ORDER BY timestamp ASC, id ASC LIMIT 1`, "client:dev-1",
	).Scan(&oldest); err != nil {
		t.Fatalf("oldest: %v", err)
	}
	if want := fmt.Sprintf("dev1-%04d", extra); oldest != want {
		t.Errorf("oldest kept row = %q, want %q", oldest, want)
	}
}

func TestSQLiteHistory_SkipsSystemRows(t *testing.T) {
	ctx := context.Background()
	h := newTestHistory(t, storage.SQLiteHistoryOptions{})
	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	system := func(text string, ts time.Time) model.HistoryEntry {
		return model.HistoryEntry{
			AuthorName: model.HistoryRoleSystem, Text: text, Timestamp: ts,
			Role: model.HistoryRoleSystem, Kind: model.HistoryKindError,
		}
	}
	user := func(text string, ts time.Time) model.HistoryEntry {
		return model.HistoryEntry{AuthorName: "dev-1", Text: text, Timestamp: ts, Role: model.HistoryRoleUser}
	}
	agent := func(text string, ts time.Time) model.HistoryEntry {
		return model.HistoryEntry{AuthorName: "agent", Text: text, Timestamp: ts, Role: model.HistoryRoleAgent}
	}

	// With and without system rows; the conversation rows are identical.
	conversation := []model.HistoryEntry{
		user("u1", t0),
		agent("a1", t0.Add(time.Minute)),
		user("u2", t0.Add(31*time.Minute)),
		agent("a2", t0.Add(32*time.Minute)),
	}
	withSystem := []model.HistoryEntry{
		conversation[0],
		system("fail-1", t0.Add(30*time.Second)),
		conversation[1],
		system("fail-2", t0.Add(30*time.Minute)),
		conversation[2],
		conversation[3],
		system("fail-3", t0.Add(33*time.Minute)),
	}
	for _, e := range conversation {
		if err := h.Add(ctx, "client:plain", e); err != nil {
			t.Fatalf("Add plain: %v", err)
		}
	}
	for _, e := range withSystem {
		if err := h.Add(ctx, "client:mixed", e); err != nil {
			t.Fatalf("Add mixed: %v", err)
		}
	}

	texts := func(entries []model.HistoryEntry) []string {
		out := make([]string, 0, len(entries))
		for _, e := range entries {
			out = append(out, e.Text)
		}
		return out
	}

	tests := []struct {
		name string
		call func(source string) ([]model.HistoryEntry, error)
		want []string
	}{
		{
			name: "Recent returns all conversation rows",
			call: func(source string) ([]model.HistoryEntry, error) { return h.Recent(ctx, source, 10) },
			want: []string{"u1", "a1", "u2", "a2"},
		},
		{
			name: "Recent limit is not consumed by system rows",
			call: func(source string) ([]model.HistoryEntry, error) { return h.Recent(ctx, source, 2) },
			want: []string{"u2", "a2"},
		},
		{
			name: "RecentActivity finds the gap between conversation rows",
			call: func(source string) ([]model.HistoryEntry, error) {
				return h.RecentActivity(ctx, source, 10*time.Minute, 10)
			},
			want: []string{"u2", "a2"},
		},
		{
			name: "RecentActivity fallback skips system rows",
			call: func(source string) ([]model.HistoryEntry, error) {
				return h.RecentActivity(ctx, source, 2*time.Hour, 3)
			},
			want: []string{"a1", "u2", "a2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plain, err := tt.call("client:plain")
			if err != nil {
				t.Fatalf("plain: %v", err)
			}
			mixed, err := tt.call("client:mixed")
			if err != nil {
				t.Fatalf("mixed: %v", err)
			}
			if got := texts(plain); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("plain = %v, want %v", got, tt.want)
			}
			if got := texts(mixed); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("mixed = %v, want %v", got, tt.want)
			}
		})
	}
}

// pageTexts returns the texts of entries in order.
func pageTexts(entries []model.HistoryEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Text)
	}
	return out
}

func TestSQLiteHistory_ListClient_Paging(t *testing.T) {
	ctx := context.Background()
	h, db := newTestHistoryDB(t, storage.SQLiteHistoryOptions{})
	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	// u1 and a1 share a timestamp, as a user message and the agent reply written
	// within the same second do; the insertion order breaks the tie.
	rows := []struct {
		source string
		entry  model.HistoryEntry
	}{
		{"client:dev-1", model.HistoryEntry{AuthorName: "dev-1", Text: "u1", Timestamp: t0, Role: model.HistoryRoleUser}},
		{"client:dev-2", model.HistoryEntry{AuthorName: "dev-2", Text: "other-1", Timestamp: t0, Role: model.HistoryRoleUser}},
		{"client:dev-1", model.HistoryEntry{AuthorName: "agent", Text: "a1", Timestamp: t0, Role: model.HistoryRoleAgent}},
		{"client:dev-1", model.HistoryEntry{AuthorName: "system", Text: "fail", Timestamp: t0.Add(time.Second), Role: model.HistoryRoleSystem, Kind: model.HistoryKindError}},
		{"-100123", model.HistoryEntry{AuthorName: "Alice", Text: "channel", Timestamp: t0.Add(time.Second)}},
		{"client:dev-1", model.HistoryEntry{AuthorName: "dev-1", Text: "u2", Timestamp: t0.Add(2 * time.Second), Role: model.HistoryRoleUser}},
		{"client:dev-2", model.HistoryEntry{AuthorName: "dev-2", Text: "other-2", Timestamp: t0.Add(2 * time.Second), Role: model.HistoryRoleUser}},
		{"client:dev-1", model.HistoryEntry{AuthorName: "agent", Text: "a2", Timestamp: t0.Add(2 * time.Second), Role: model.HistoryRoleAgent}},
	}
	ids := map[string]string{}
	for _, r := range rows {
		ids[r.entry.Text] = addTxCommitted(t, h, db, r.source, r.entry)
	}

	tests := []struct {
		name      string
		limit     int
		wantPages [][]string
	}{
		{name: "pages of two", limit: 2, wantPages: [][]string{{"u2", "a2"}, {"a1", "fail"}, {"u1"}}},
		{name: "pages of three", limit: 3, wantPages: [][]string{{"fail", "u2", "a2"}, {"u1", "a1"}}},
		{name: "exact fit has no next page", limit: 5, wantPages: [][]string{{"u1", "a1", "fail", "u2", "a2"}}},
		{name: "limit above total", limit: 50, wantPages: [][]string{{"u1", "a1", "fail", "u2", "a2"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := ""
			for i, want := range tt.wantPages {
				entries, next, err := h.ListClient(ctx, "client:dev-1", before, tt.limit)
				if err != nil {
					t.Fatalf("page %d: ListClient: %v", i, err)
				}
				if got := pageTexts(entries); !reflect.DeepEqual(got, want) {
					t.Fatalf("page %d = %v, want %v", i, got, want)
				}
				for _, e := range entries {
					if e.MessageID != ids[e.Text] {
						t.Errorf("page %d: %q MessageID=%q, want %q", i, e.Text, e.MessageID, ids[e.Text])
					}
				}
				last := i == len(tt.wantPages)-1
				if last && next != "" {
					t.Fatalf("page %d is the last one but nextBefore=%q", i, next)
				}
				if !last && next != ids[want[0]] {
					t.Fatalf("page %d nextBefore=%q, want the id of %q (%q)", i, next, want[0], ids[want[0]])
				}
				before = next
			}
		})
	}
}

func TestSQLiteHistory_ListClient_ReturnsMetadata(t *testing.T) {
	ctx := context.Background()
	h, db := newTestHistoryDB(t, storage.SQLiteHistoryOptions{})
	now := time.Now().UTC().Truncate(time.Second)

	entry := model.HistoryEntry{
		AuthorName: "dev-1", Text: "call mom", Timestamp: now,
		Role: model.HistoryRoleUser, Kind: model.HistoryKindVoice,
		AudioJobID: "job-1", AudioDurationMs: 9000, TaskIDs: []string{"t-1"},
	}
	id := addTxCommitted(t, h, db, "client:dev-1", entry)

	got, next, err := h.ListClient(ctx, "client:dev-1", "", 10)
	if err != nil {
		t.Fatalf("ListClient: %v", err)
	}
	want := entry
	want.MessageID = id
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) || next != "" {
		t.Fatalf("ListClient = %+v, next=%q; want [%+v], next empty", got, next, want)
	}
}

func TestSQLiteHistory_ListClient_EmptyAndZeroLimit(t *testing.T) {
	ctx := context.Background()
	h, db := newTestHistoryDB(t, storage.SQLiteHistoryOptions{})
	addTxCommitted(t, h, db, "client:dev-1", makeEntry("dev-1", "hello", time.Now()))

	tests := []struct {
		name   string
		source string
		limit  int
	}{
		{name: "source without rows", source: "client:dev-2", limit: 10},
		{name: "zero limit", source: "client:dev-1", limit: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, next, err := h.ListClient(ctx, tt.source, "", tt.limit)
			if err != nil {
				t.Fatalf("ListClient: %v", err)
			}
			if len(got) != 0 || next != "" {
				t.Fatalf("ListClient = %+v, next=%q; want empty", got, next)
			}
		})
	}
}

func TestSQLiteHistory_ListClient_UnknownCursor(t *testing.T) {
	ctx := context.Background()
	h, db := newTestHistoryDB(t, storage.SQLiteHistoryOptions{})
	addTxCommitted(t, h, db, "client:dev-1", makeEntry("dev-1", "mine", time.Now()))
	otherID := addTxCommitted(t, h, db, "client:dev-2", makeEntry("dev-2", "theirs", time.Now()))

	tests := []struct {
		name   string
		before string
	}{
		{name: "missing message", before: "00000000-0000-0000-0000-000000000000"},
		{name: "message of another source", before: otherID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, next, err := h.ListClient(ctx, "client:dev-1", tt.before, 10)
			if !errors.Is(err, model.ErrHistoryCursorNotFound) {
				t.Fatalf("want error wrapping ErrHistoryCursorNotFound, got %v", err)
			}
			if got != nil || next != "" {
				t.Fatalf("want no entries on error, got %+v, next=%q", got, next)
			}
		})
	}
}

// TestSQLiteHistory_ListClient_RowsBeforeMigration017 verifies that client rows
// written before migration 017 come back with their backfilled role and the
// default kind.
func TestSQLiteHistory_ListClient_RowsBeforeMigration017(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("SetDialect: %v", err)
	}
	if err := goose.UpTo(db, ".", 16); err != nil {
		t.Fatalf("UpTo 16: %v", err)
	}

	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	for i, author := range []string{"dev-1", "agent"} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO messages(source_id, author_name, text, timestamp, message_id) VALUES (?, ?, ?, ?, ?)`,
			"client:dev-1", author, "old "+author, t0.Add(time.Duration(i)*time.Second).Unix(), fmt.Sprintf("old-%d", i),
		); err != nil {
			t.Fatalf("insert %s: %v", author, err)
		}
	}

	if err := migrations.Up(db); err != nil {
		t.Fatalf("Up: %v", err)
	}

	h := storage.NewSQLiteHistory(db, storage.SQLiteHistoryOptions{})
	got, _, err := h.ListClient(ctx, "client:dev-1", "", 10)
	if err != nil {
		t.Fatalf("ListClient: %v", err)
	}
	want := []model.HistoryEntry{
		{MessageID: "old-0", AuthorName: "dev-1", Text: "old dev-1", Timestamp: t0, Role: model.HistoryRoleUser, Kind: model.HistoryKindText},
		{MessageID: "old-1", AuthorName: "agent", Text: "old agent", Timestamp: t0.Add(time.Second), Role: model.HistoryRoleAgent, Kind: model.HistoryKindText},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListClient:\n got %+v\nwant %+v", got, want)
	}
}
