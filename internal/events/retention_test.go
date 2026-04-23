package events_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
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

	runner := events.NewRunner(events.RunnerConfig{
		Events:         f.events,
		Queue:          f.queue,
		EventRetention: retention,
		Logger:         silentLogger(),
	})
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

	runner := events.NewRunner(events.RunnerConfig{
		Events:         f.events,
		Queue:          f.queue,
		EventRetention: retention,
		Logger:         silentLogger(),
	})
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

// stubDeviceStore implements model.DeviceStore for device-sweep tests.
type stubDeviceStore struct {
	model.DeviceStore
	inactive        []model.Device
	inactiveErr     error
	inactiveCalls   atomic.Int32
	inactiveLastCut time.Time
	revokeCalls     atomic.Int32
	revokedIDs      []string
	revokeErr       error
	deleteCalls     atomic.Int32
	deleteLastCut   time.Time
	deleteReturned  int64
	deleteErr       error
	mu              sync.Mutex
}

func (s *stubDeviceStore) ListInactive(_ context.Context, cutoff time.Time) ([]model.Device, error) {
	s.inactiveCalls.Add(1)
	s.inactiveLastCut = cutoff
	return s.inactive, s.inactiveErr
}

func (s *stubDeviceStore) Revoke(_ context.Context, id string) error {
	s.revokeCalls.Add(1)
	if s.revokeErr != nil {
		return s.revokeErr
	}
	s.mu.Lock()
	s.revokedIDs = append(s.revokedIDs, id)
	s.mu.Unlock()
	return nil
}

func (s *stubDeviceStore) DeleteRevokedOlderThan(_ context.Context, cutoff time.Time) (int64, error) {
	s.deleteCalls.Add(1)
	s.deleteLastCut = cutoff
	return s.deleteReturned, s.deleteErr
}

// stubRevoker captures DeleteRegistration calls.
type stubRevoker struct {
	calls atomic.Int32
	ids   []string
	err   error
	mu    sync.Mutex
}

func (r *stubRevoker) DeleteRegistration(_ context.Context, id string) error {
	r.calls.Add(1)
	r.mu.Lock()
	r.ids = append(r.ids, id)
	r.mu.Unlock()
	return r.err
}

func TestRetentionRunner_AutoRevokesInactiveDevices(t *testing.T) {
	eventsStub := &stubEventStore{}
	queueStub := &stubPushQueue{}
	created := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	seen := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	devStub := &stubDeviceStore{
		inactive: []model.Device{
			{ID: "dev-1", Name: "Old iPhone", CreatedAt: created, LastSeenAt: &seen},
			{ID: "dev-2", Name: "Fresh install", CreatedAt: created, LastSeenAt: nil},
		},
	}
	revoker := &stubRevoker{}

	now := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	runner := events.NewRunner(events.RunnerConfig{
		Events:         eventsStub,
		Queue:          queueStub,
		Devices:        devStub,
		Revoker:        revoker,
		EventRetention: time.Hour,
		DeviceInactive: 30 * 24 * time.Hour,
		Logger:         silentLogger(),
	})

	if err := runner.Tick(context.Background(), now); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if devStub.inactiveCalls.Load() != 1 {
		t.Fatalf("ListInactive called %d times, want 1", devStub.inactiveCalls.Load())
	}
	expectedCut := now.Add(-30 * 24 * time.Hour)
	if !devStub.inactiveLastCut.Equal(expectedCut) {
		t.Fatalf("inactive cutoff = %v, want %v", devStub.inactiveLastCut, expectedCut)
	}
	if devStub.revokeCalls.Load() != 2 {
		t.Fatalf("Revoke called %d times, want 2", devStub.revokeCalls.Load())
	}
	if revoker.calls.Load() != 2 {
		t.Fatalf("DeleteRegistration called %d times, want 2", revoker.calls.Load())
	}
}

func TestRetentionRunner_RelayErrorDoesNotBlockNextDevice(t *testing.T) {
	eventsStub := &stubEventStore{}
	queueStub := &stubPushQueue{}
	devStub := &stubDeviceStore{
		inactive: []model.Device{
			{ID: "dev-1", Name: "A"},
			{ID: "dev-2", Name: "B"},
		},
	}
	revoker := &stubRevoker{err: errors.New("relay недоступен")}

	runner := events.NewRunner(events.RunnerConfig{
		Events:         eventsStub,
		Queue:          queueStub,
		Devices:        devStub,
		Revoker:        revoker,
		EventRetention: time.Hour,
		DeviceInactive: 30 * 24 * time.Hour,
		Logger:         silentLogger(),
	})

	if err := runner.Tick(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if devStub.revokeCalls.Load() != 2 {
		t.Fatalf("both devices must be revoked locally despite relay errors, got %d", devStub.revokeCalls.Load())
	}
	if revoker.calls.Load() != 2 {
		t.Fatalf("DeleteRegistration must be attempted for both, got %d", revoker.calls.Load())
	}
}

func TestRetentionRunner_DeletesLongRevokedDevices(t *testing.T) {
	eventsStub := &stubEventStore{}
	queueStub := &stubPushQueue{}
	devStub := &stubDeviceStore{deleteReturned: 3}

	now := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	runner := events.NewRunner(events.RunnerConfig{
		Events:          eventsStub,
		Queue:           queueStub,
		Devices:         devStub,
		EventRetention:  time.Hour,
		DeviceRetention: 90 * 24 * time.Hour,
		Logger:          silentLogger(),
	})

	if err := runner.Tick(context.Background(), now); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if devStub.deleteCalls.Load() != 1 {
		t.Fatalf("DeleteRevokedOlderThan called %d times, want 1", devStub.deleteCalls.Load())
	}
	expectedCut := now.Add(-90 * 24 * time.Hour)
	if !devStub.deleteLastCut.Equal(expectedCut) {
		t.Fatalf("delete cutoff = %v, want %v", devStub.deleteLastCut, expectedCut)
	}
}

func TestRetentionRunner_DeviceSweepsDisabledWhenZero(t *testing.T) {
	eventsStub := &stubEventStore{}
	queueStub := &stubPushQueue{}
	devStub := &stubDeviceStore{}

	runner := events.NewRunner(events.RunnerConfig{
		Events:         eventsStub,
		Queue:          queueStub,
		Devices:        devStub,
		EventRetention: time.Hour,
		// DeviceInactive and DeviceRetention intentionally zero
		Logger: silentLogger(),
	})

	if err := runner.Tick(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if devStub.inactiveCalls.Load() != 0 {
		t.Fatalf("ListInactive must not be called when DeviceInactive is zero")
	}
	if devStub.deleteCalls.Load() != 0 {
		t.Fatalf("DeleteRevokedOlderThan must not be called when DeviceRetention is zero")
	}
}

func TestRetentionRunner_ListInactiveErrorSkipsRevokes(t *testing.T) {
	eventsStub := &stubEventStore{}
	queueStub := &stubPushQueue{}
	devStub := &stubDeviceStore{inactiveErr: errors.New("db down")}

	runner := events.NewRunner(events.RunnerConfig{
		Events:         eventsStub,
		Queue:          queueStub,
		Devices:        devStub,
		EventRetention: time.Hour,
		DeviceInactive: time.Hour,
		Logger:         silentLogger(),
	})

	if err := runner.Tick(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if devStub.revokeCalls.Load() != 0 {
		t.Fatalf("Revoke must not be called when ListInactive fails")
	}
	// events/push_queue must still be cleaned even when devices listing fails
	if eventsStub.calls.Load() != 1 {
		t.Fatalf("events cleanup was skipped after device error")
	}
}

func TestRetentionPushQueueErrorDoesNotBlockEvents(t *testing.T) {
	eventsStub := &stubEventStore{}
	queueStub := &stubPushQueue{err: errors.New("ошибка очистки")}

	runner := events.NewRunner(events.RunnerConfig{
		Events:         eventsStub,
		Queue:          queueStub,
		EventRetention: time.Hour,
		Logger:         silentLogger(),
	})
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

	runner := events.NewRunner(events.RunnerConfig{
		Events:         eventsStub,
		Queue:          queueStub,
		EventRetention: time.Hour,
		Logger:         silentLogger(),
	})
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
	runner := events.NewRunner(events.RunnerConfig{
		Events:         eventsStub,
		Queue:          queueStub,
		Pairing:        pairingStub,
		EventRetention: time.Hour,
		Logger:         silentLogger(),
	})
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
	runner := events.NewRunner(events.RunnerConfig{
		Events:         eventsStub,
		Queue:          queueStub,
		Pairing:        pairingStub,
		EventRetention: time.Hour,
		Logger:         silentLogger(),
	})
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
