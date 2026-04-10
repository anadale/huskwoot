package events_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/anadale/huskwoot/internal/devices"
	"github.com/anadale/huskwoot/internal/events"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/push"
	"github.com/anadale/huskwoot/internal/storage"
)

// retentionFixture sets up the SQLite schema and store instances for retention tests.
type retentionFixture struct {
	db       *sql.DB
	events   *events.SQLiteEventStore
	queue    *push.SQLitePushQueue
	deviceID string
}

func newRetentionFixture(t *testing.T) *retentionFixture {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.OpenDB(filepath.Join(dir, "retention.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })

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
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := devStore.Create(ctx, tx, dev); err != nil {
		_ = tx.Rollback()
		t.Fatalf("devices.Create: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	return &retentionFixture{db: db, events: evStore, queue: queue, deviceID: dev.ID}
}

// insertEventAt inserts a single event with the given created_at and returns its seq.
func (f *retentionFixture) insertEventAt(t *testing.T, createdAt time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	payload, _ := json.Marshal(map[string]any{"id": uuid.NewString()})
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	seq, err := f.events.Insert(ctx, tx, model.Event{
		Kind:      model.EventTaskCreated,
		EntityID:  uuid.NewString(),
		Payload:   payload,
		CreatedAt: createdAt,
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("events.Insert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return seq
}

// enqueueDelivered adds a push job and manually sets delivered_at.
func (f *retentionFixture) enqueueDelivered(t *testing.T, eventSeq int64, deliveredAt time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := f.queue.Enqueue(ctx, tx, f.deviceID, eventSeq); err != nil {
		_ = tx.Rollback()
		t.Fatalf("queue.Enqueue: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	var id int64
	if err := f.db.QueryRowContext(ctx,
		`SELECT id FROM push_queue WHERE event_seq = ? ORDER BY id DESC LIMIT 1`, eventSeq,
	).Scan(&id); err != nil {
		t.Fatalf("SELECT id: %v", err)
	}
	if _, err := f.db.ExecContext(ctx,
		`UPDATE push_queue SET delivered_at = ? WHERE id = ?`,
		deliveredAt.UTC().Format(time.RFC3339), id,
	); err != nil {
		t.Fatalf("UPDATE delivered_at: %v", err)
	}
	return id
}

// countEvents returns the number of rows in the events table.
func (f *retentionFixture) countEvents(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		t.Fatalf("COUNT events: %v", err)
	}
	return n
}

// countPushQueue returns the number of rows in the push_queue table.
func (f *retentionFixture) countPushQueue(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM push_queue`).Scan(&n); err != nil {
		t.Fatalf("COUNT push_queue: %v", err)
	}
	return n
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRetentionDeletesOldEventsAndPushQueue(t *testing.T) {
	f := newRetentionFixture(t)
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	retention := 24 * time.Hour

	oldSeq := f.insertEventAt(t, now.Add(-48*time.Hour))
	freshSeq := f.insertEventAt(t, now.Add(-time.Hour))

	f.enqueueDelivered(t, oldSeq, now.Add(-48*time.Hour))
	f.enqueueDelivered(t, freshSeq, now.Add(-time.Hour))

	runner := events.NewRunner(f.events, f.queue, nil, retention, silentLogger())
	if err := runner.Tick(context.Background(), now); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if got := f.countEvents(t); got != 1 {
		t.Fatalf("after Tick, events has %d rows, want 1", got)
	}
	if got := f.countPushQueue(t); got != 1 {
		t.Fatalf("after Tick, push_queue has %d rows, want 1", got)
	}

	remaining, err := f.events.SinceSeq(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("SinceSeq: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Seq != freshSeq {
		t.Fatalf("events has %+v, want seq %d", remaining, freshSeq)
	}
}

func TestRetentionKeepsPendingPushQueue(t *testing.T) {
	f := newRetentionFixture(t)
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	retention := 24 * time.Hour

	freshSeq := f.insertEventAt(t, now.Add(-time.Hour))

	// A pending push job (no delivered_at / dropped_at) must never be deleted
	// — the dispatcher is still waiting for it.
	ctx := context.Background()
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := f.queue.Enqueue(ctx, tx, f.deviceID, freshSeq); err != nil {
		_ = tx.Rollback()
		t.Fatalf("Enqueue: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	runner := events.NewRunner(f.events, f.queue, nil, retention, silentLogger())
	if err := runner.Tick(ctx, now); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if got := f.countPushQueue(t); got != 1 {
		t.Fatalf("pending push_queue was deleted: %d rows remain, want 1", got)
	}
}

// errPushQueue simulates a DeleteDelivered error and counts calls to EventStore.
type stubEventStore struct {
	model.EventStore
	calls   atomic.Int32
	lastCut time.Time
}

func (s *stubEventStore) DeleteOlderThan(_ context.Context, cutoff time.Time) (int64, error) {
	s.calls.Add(1)
	s.lastCut = cutoff
	return 0, nil
}

type stubPushQueue struct {
	model.PushQueue
	err   error
	calls atomic.Int32
}

func (q *stubPushQueue) DeleteDelivered(_ context.Context, _ time.Time) (int64, error) {
	q.calls.Add(1)
	return 0, q.err
}

type stubPairingStore struct {
	model.PairingStore
	err     error
	calls   atomic.Int32
	lastCut time.Time
}

func (s *stubPairingStore) DeleteExpired(_ context.Context, cutoff time.Time) (int64, error) {
	s.calls.Add(1)
	s.lastCut = cutoff
	return 0, s.err
}

func TestRetentionPushQueueErrorDoesNotBlockEvents(t *testing.T) {
	eventsStub := &stubEventStore{}
	queueStub := &stubPushQueue{err: errors.New("ошибка очистки")}

	runner := events.NewRunner(eventsStub, queueStub, nil, time.Hour, silentLogger())
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	if err := runner.Tick(context.Background(), now); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if queueStub.calls.Load() != 1 {
		t.Fatalf("DeleteDelivered called %d times, want 1", queueStub.calls.Load())
	}
	if eventsStub.calls.Load() != 1 {
		t.Fatalf("DeleteOlderThan called %d times, want 1", eventsStub.calls.Load())
	}
	expected := now.Add(-time.Hour)
	if !eventsStub.lastCut.Equal(expected) {
		t.Fatalf("cutoff = %v, want %v", eventsStub.lastCut, expected)
	}
}

func TestRetentionRunStopsOnContextCancel(t *testing.T) {
	eventsStub := &stubEventStore{}
	queueStub := &stubPushQueue{}

	runner := events.NewRunner(eventsStub, queueStub, nil, time.Hour, silentLogger())
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		runner.Run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func TestRetentionRunner_DeletesExpiredPairings(t *testing.T) {
	eventsStub := &stubEventStore{}
	queueStub := &stubPushQueue{}
	pairingStub := &stubPairingStore{}

	now := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	runner := events.NewRunner(eventsStub, queueStub, pairingStub, time.Hour, silentLogger())
	if err := runner.Tick(context.Background(), now); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if pairingStub.calls.Load() != 1 {
		t.Fatalf("DeleteExpired called %d times, want 1", pairingStub.calls.Load())
	}
	// cutoff is hard-coded: now - 1h (spec §3)
	expected := now.Add(-time.Hour)
	if !pairingStub.lastCut.Equal(expected) {
		t.Fatalf("cutoff pairing = %v, want %v", pairingStub.lastCut, expected)
	}
}

func TestRetentionRunner_ContinuesIfPairingFails(t *testing.T) {
	eventsStub := &stubEventStore{}
	queueStub := &stubPushQueue{}
	pairingStub := &stubPairingStore{err: errors.New("pairing: ошибка очистки")}

	now := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	runner := events.NewRunner(eventsStub, queueStub, pairingStub, time.Hour, silentLogger())
	if err := runner.Tick(context.Background(), now); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// events and push_queue must be cleaned even when pairing fails
	if eventsStub.calls.Load() != 1 {
		t.Fatalf("DeleteOlderThan called %d times, want 1", eventsStub.calls.Load())
	}
	if queueStub.calls.Load() != 1 {
		t.Fatalf("DeleteDelivered called %d times, want 1", queueStub.calls.Load())
	}
}
