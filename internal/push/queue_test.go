package push_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/anadale/huskwoot/internal/devices"
	"github.com/anadale/huskwoot/internal/events"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/push"
	"github.com/anadale/huskwoot/internal/storage"
)

// testFixture holds a prepared schema (device + several events)
// with seeds ready for insertion into push_queue.
type testFixture struct {
	db       *sql.DB
	queue    *push.SQLitePushQueue
	deviceID string
	eventSeq []int64
}

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

func newFixture(t *testing.T, eventCount int) *testFixture {
	t.Helper()
	db := openTestDB(t)
	ctx := context.Background()

	devStore := devices.NewSQLiteDeviceStore(db)
	evStore := events.NewSQLiteEventStore(db)
	queue := push.NewSQLitePushQueue(db)

	dev := &model.Device{
		ID:        uuid.NewString(),
		Name:      "iPhone",
		Platform:  "ios",
		TokenHash: "hash-" + uuid.NewString(),
	}

	seqs := make([]int64, 0, eventCount)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := devStore.Create(ctx, tx, dev); err != nil {
		_ = tx.Rollback()
		t.Fatalf("devStore.Create: %v", err)
	}
	for i := 0; i < eventCount; i++ {
		payload, _ := json.Marshal(map[string]any{"n": i})
		seq, err := evStore.Insert(ctx, tx, model.Event{
			Kind:     model.EventTaskCreated,
			EntityID: uuid.NewString(),
			Payload:  payload,
		})
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("evStore.Insert: %v", err)
		}
		seqs = append(seqs, seq)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	return &testFixture{db: db, queue: queue, deviceID: dev.ID, eventSeq: seqs}
}

func enqueue(t *testing.T, f *testFixture, eventSeq int64) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := f.queue.Enqueue(ctx, tx, f.deviceID, eventSeq); err != nil {
		_ = tx.Rollback()
		t.Fatalf("Enqueue: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func TestPushQueueEnqueueInTx(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()

	enqueue(t, f, f.eventSeq[0])

	jobs, err := f.queue.NextBatch(ctx, 10)
	if err != nil {
		t.Fatalf("NextBatch: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("NextBatch returned %d jobs, want 1", len(jobs))
	}
	if jobs[0].DeviceID != f.deviceID {
		t.Fatalf("jobs[0].DeviceID = %q, want %q", jobs[0].DeviceID, f.deviceID)
	}
	if jobs[0].EventSeq != f.eventSeq[0] {
		t.Fatalf("jobs[0].EventSeq = %d, want %d", jobs[0].EventSeq, f.eventSeq[0])
	}
	if jobs[0].Attempts != 0 {
		t.Fatalf("jobs[0].Attempts = %d, want 0", jobs[0].Attempts)
	}
	if jobs[0].CreatedAt.IsZero() {
		t.Fatal("jobs[0].CreatedAt not populated")
	}
	if jobs[0].NextAttemptAt.IsZero() {
		t.Fatal("jobs[0].NextAttemptAt not populated")
	}
	if jobs[0].ID == 0 {
		t.Fatal("jobs[0].ID not populated")
	}
}

func TestPushQueueEnqueueRolledBack(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()

	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := f.queue.Enqueue(ctx, tx, f.deviceID, f.eventSeq[0]); err != nil {
		_ = tx.Rollback()
		t.Fatalf("Enqueue: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	jobs, err := f.queue.NextBatch(ctx, 10)
	if err != nil {
		t.Fatalf("NextBatch: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("after rollback queue contains %d jobs, want 0", len(jobs))
	}
}

func TestPushQueueEnqueueNilTxReturnsError(t *testing.T) {
	f := newFixture(t, 1)
	err := f.queue.Enqueue(context.Background(), nil, f.deviceID, f.eventSeq[0])
	if err == nil {
		t.Fatal("Enqueue with nil tx should return an error")
	}
}

func TestPushQueueNextBatchOrdersByNextAttempt(t *testing.T) {
	f := newFixture(t, 3)
	ctx := context.Background()

	enqueue(t, f, f.eventSeq[0])
	enqueue(t, f, f.eventSeq[1])
	enqueue(t, f, f.eventSeq[2])

	// Delay the first row's next attempt into the future → it must appear last
	// among the three currently available jobs.
	first, err := f.queue.NextBatch(ctx, 10)
	if err != nil {
		t.Fatalf("NextBatch: %v", err)
	}
	if len(first) < 3 {
		t.Fatalf("NextBatch returned %d, want >=3", len(first))
	}
	targetID := first[0].ID
	future := time.Now().Add(1 * time.Hour)
	if err := f.queue.MarkFailed(ctx, targetID, "temp", future); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	got, err := f.queue.NextBatch(ctx, 10)
	if err != nil {
		t.Fatalf("NextBatch: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("NextBatch returned %d, want 2 (one deferred to future)", len(got))
	}
	for _, j := range got {
		if j.ID == targetID {
			t.Fatalf("deferred job %d appeared in result", targetID)
		}
	}
	// Order must be by NextAttemptAt ASC.
	if !got[0].NextAttemptAt.Before(got[1].NextAttemptAt) &&
		!got[0].NextAttemptAt.Equal(got[1].NextAttemptAt) {
		t.Fatalf("NextBatch not sorted by NextAttemptAt: %v / %v",
			got[0].NextAttemptAt, got[1].NextAttemptAt)
	}
}

func TestPushQueueNextBatchRespectsLimit(t *testing.T) {
	f := newFixture(t, 3)
	ctx := context.Background()

	enqueue(t, f, f.eventSeq[0])
	enqueue(t, f, f.eventSeq[1])
	enqueue(t, f, f.eventSeq[2])

	got, err := f.queue.NextBatch(ctx, 2)
	if err != nil {
		t.Fatalf("NextBatch: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("NextBatch(limit=2) returned %d, want 2", len(got))
	}
}

func TestPushQueueMarkDeliveredExcludesFromBatch(t *testing.T) {
	f := newFixture(t, 2)
	ctx := context.Background()

	enqueue(t, f, f.eventSeq[0])
	enqueue(t, f, f.eventSeq[1])

	all, err := f.queue.NextBatch(ctx, 10)
	if err != nil {
		t.Fatalf("NextBatch: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("first NextBatch returned %d, want 2", len(all))
	}

	if err := f.queue.MarkDelivered(ctx, all[0].ID); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}

	got, err := f.queue.NextBatch(ctx, 10)
	if err != nil {
		t.Fatalf("NextBatch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("after MarkDelivered NextBatch returned %d, want 1", len(got))
	}
	if got[0].ID == all[0].ID {
		t.Fatalf("delivered job %d remained in batch", all[0].ID)
	}
}

func TestPushQueueMarkFailedIncrementsAttempts(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()

	enqueue(t, f, f.eventSeq[0])
	all, err := f.queue.NextBatch(ctx, 10)
	if err != nil {
		t.Fatalf("NextBatch: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("NextBatch returned %d, want 1", len(all))
	}

	jobID := all[0].ID
	past := time.Now().Add(-time.Minute)
	if err := f.queue.MarkFailed(ctx, jobID, "ошибка", past); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if err := f.queue.MarkFailed(ctx, jobID, "снова", past); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	got, err := f.queue.NextBatch(ctx, 10)
	if err != nil {
		t.Fatalf("NextBatch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("NextBatch returned %d, want 1", len(got))
	}
	if got[0].Attempts != 2 {
		t.Fatalf("Attempts = %d, want 2", got[0].Attempts)
	}
	if got[0].LastError != "снова" {
		t.Fatalf("LastError = %q, want %q", got[0].LastError, "снова")
	}
}

func TestPushQueueDropExcludesFromBatch(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()

	enqueue(t, f, f.eventSeq[0])
	all, err := f.queue.NextBatch(ctx, 10)
	if err != nil {
		t.Fatalf("NextBatch: %v", err)
	}
	if err := f.queue.Drop(ctx, all[0].ID, "превышен retry"); err != nil {
		t.Fatalf("Drop: %v", err)
	}

	got, err := f.queue.NextBatch(ctx, 10)
	if err != nil {
		t.Fatalf("NextBatch: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("after Drop NextBatch returned %d, want 0", len(got))
	}
}

func TestPushQueueDeleteDeliveredRespectsCutoff(t *testing.T) {
	f := newFixture(t, 2)
	ctx := context.Background()

	enqueue(t, f, f.eventSeq[0])
	enqueue(t, f, f.eventSeq[1])
	all, err := f.queue.NextBatch(ctx, 10)
	if err != nil {
		t.Fatalf("NextBatch: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("NextBatch returned %d, want 2", len(all))
	}

	// Mark the first as delivered in the past, the second as delivered now.
	// Set delivered_at manually via a test query — MarkDelivered uses time.Now().
	past := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	recent := time.Now().UTC().Format(time.RFC3339)

	if _, err := f.db.ExecContext(ctx,
		`UPDATE push_queue SET delivered_at = ? WHERE id = ?`, past, all[0].ID,
	); err != nil {
		t.Fatalf("UPDATE delivered_at (past): %v", err)
	}
	if _, err := f.db.ExecContext(ctx,
		`UPDATE push_queue SET delivered_at = ? WHERE id = ?`, recent, all[1].ID,
	); err != nil {
		t.Fatalf("UPDATE delivered_at (recent): %v", err)
	}

	cutoff := time.Now().Add(-time.Hour)
	n, err := f.queue.DeleteDelivered(ctx, cutoff)
	if err != nil {
		t.Fatalf("DeleteDelivered: %v", err)
	}
	if n != 1 {
		t.Fatalf("DeleteDelivered deleted %d, want 1", n)
	}

	// Verify exactly one row remains and it is the second one.
	rows, err := f.db.QueryContext(ctx, `SELECT id FROM push_queue`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()
	var remaining []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		remaining = append(remaining, id)
	}
	if len(remaining) != 1 || remaining[0] != all[1].ID {
		t.Fatalf("after DeleteDelivered remaining = %v, want [%d]", remaining, all[1].ID)
	}
}

func TestPushQueueDeleteDeliveredRemovesDropped(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()

	enqueue(t, f, f.eventSeq[0])
	all, err := f.queue.NextBatch(ctx, 10)
	if err != nil {
		t.Fatalf("NextBatch: %v", err)
	}

	past := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := f.db.ExecContext(ctx,
		`UPDATE push_queue SET dropped_at = ?, dropped_reason = ? WHERE id = ?`,
		past, "fail", all[0].ID,
	); err != nil {
		t.Fatalf("UPDATE dropped_at: %v", err)
	}

	cutoff := time.Now().Add(-time.Hour)
	n, err := f.queue.DeleteDelivered(ctx, cutoff)
	if err != nil {
		t.Fatalf("DeleteDelivered: %v", err)
	}
	if n != 1 {
		t.Fatalf("DeleteDelivered deleted %d rows, want 1 (dropped)", n)
	}
}

func TestPushQueueDeleteDeliveredIgnoresPending(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()

	enqueue(t, f, f.eventSeq[0])

	cutoff := time.Now().Add(time.Hour) // even a future cutoff must not delete pending jobs
	n, err := f.queue.DeleteDelivered(ctx, cutoff)
	if err != nil {
		t.Fatalf("DeleteDelivered: %v", err)
	}
	if n != 0 {
		t.Fatalf("DeleteDelivered deleted %d pending rows, want 0", n)
	}
}
