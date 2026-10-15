package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// mockBufferForSweeper is a minimal ForwardBuffer mock for sweeper tests.
type mockBufferForSweeper struct {
	mu      sync.Mutex
	pending []string
	listErr error
}

func (m *mockBufferForSweeper) Append(_ context.Context, _ model.ForwardEntry) error { return nil }
func (m *mockBufferForSweeper) LastReceivedAt(_ context.Context, _ string) (time.Time, bool, error) {
	return time.Time{}, false, nil
}
func (m *mockBufferForSweeper) LoadBatch(_ context.Context, _ string) ([]model.ForwardEntry, error) {
	return nil, nil
}
func (m *mockBufferForSweeper) DeleteEntriesTx(_ context.Context, _ *sql.Tx, _ string, _ []string) error {
	return nil
}
func (m *mockBufferForSweeper) ListPendingChats(_ context.Context, _ time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.pending...), m.listErr
}

// drainSignals reads all available signals from ch without blocking.
func drainSignals(ch chan model.ForwardDispatchSignal) []model.ForwardDispatchSignal {
	var out []model.ForwardDispatchSignal
	for {
		select {
		case s := <-ch:
			out = append(out, s)
		default:
			return out
		}
	}
}

func TestForwardBatchSweeper_Tick_SubmitsForPendingChats(t *testing.T) {
	buf := &mockBufferForSweeper{pending: []string{"chat-1", "chat-2"}}

	sigCh := make(chan model.ForwardDispatchSignal, 32)
	d := NewForwardDispatcher(buf, sigCh, New(Config{Logger: discardLogger}), nil, discardLogger)

	fixedNow := time.Now()
	sweeper := NewForwardBatchSweeper(
		buf, d,
		10*time.Second,
		20*time.Millisecond,
		func() time.Time { return fixedNow },
		discardLogger,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		sweeper.Run(ctx)
	}()

	// Wait for at least one tick.
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	sigs := drainSignals(sigCh)
	if len(sigs) < 2 {
		t.Fatalf("expected at least 2 signals, got %d", len(sigs))
	}

	seen := make(map[string]bool)
	for _, s := range sigs {
		if s.Source != "sweeper" {
			t.Errorf("signal source = %q, want sweeper", s.Source)
		}
		seen[s.ChatID] = true
	}

	var chatIDs []string
	for id := range seen {
		chatIDs = append(chatIDs, id)
	}
	sort.Strings(chatIDs)

	if len(chatIDs) != 2 || chatIDs[0] != "chat-1" || chatIDs[1] != "chat-2" {
		t.Errorf("unexpected unique chat IDs: %v", chatIDs)
	}
}

func TestForwardBatchSweeper_CtxDone_ExitsRun(t *testing.T) {
	buf := &mockBufferForSweeper{}
	sigCh := make(chan model.ForwardDispatchSignal, 4)
	d := NewForwardDispatcher(buf, sigCh, New(Config{Logger: discardLogger}), nil, discardLogger)

	sweeper := NewForwardBatchSweeper(buf, d, 10*time.Second, 5*time.Second, nil, discardLogger)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		sweeper.Run(ctx)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after ctx cancellation")
	}
}

func TestForwardBatchSweeper_ZeroTimeout_DoesNotStart(t *testing.T) {
	buf := &mockBufferForSweeper{pending: []string{"chat-1"}}
	sigCh := make(chan model.ForwardDispatchSignal, 4)
	d := NewForwardDispatcher(buf, sigCh, New(Config{Logger: discardLogger}), nil, discardLogger)

	// timeout=0 → feature disabled; Run must return synchronously.
	sweeper := NewForwardBatchSweeper(buf, d, 0, 10*time.Millisecond, nil, discardLogger)

	done := make(chan struct{})
	go func() {
		defer close(done)
		sweeper.Run(context.Background())
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Run did not return immediately for zero timeout")
	}

	if sig, ok := func() (model.ForwardDispatchSignal, bool) {
		select {
		case s := <-sigCh:
			return s, true
		default:
			return model.ForwardDispatchSignal{}, false
		}
	}(); ok {
		t.Errorf("unexpected signal for zero-timeout sweeper: %v", sig)
	}
}

func TestForwardBatchSweeper_ListError_DoesNotPanic(t *testing.T) {
	buf := &mockBufferForSweeper{listErr: errors.New("sweep test error")}
	sigCh := make(chan model.ForwardDispatchSignal, 4)
	d := NewForwardDispatcher(buf, sigCh, New(Config{Logger: discardLogger}), nil, discardLogger)

	sweeper := NewForwardBatchSweeper(buf, d, 10*time.Second, 20*time.Millisecond, nil, discardLogger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		sweeper.Run(ctx)
	}()

	time.Sleep(80 * time.Millisecond)
	cancel()
	<-done

	// No signals should arrive when ListPendingChats errors.
	if sigs := drainSignals(sigCh); len(sigs) > 0 {
		t.Errorf("unexpected signals on list error: %v", sigs)
	}
}
