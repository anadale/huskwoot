package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// mockBufferForDispatcher satisfies model.ForwardBuffer for dispatcher tests.
type mockBufferForDispatcher struct {
	mu      sync.Mutex
	batches map[string][]model.ForwardEntry
	loadErr error
}

func newMockBufferForDispatcher(batches map[string][]model.ForwardEntry) *mockBufferForDispatcher {
	if batches == nil {
		batches = make(map[string][]model.ForwardEntry)
	}
	return &mockBufferForDispatcher{batches: batches}
}

func (m *mockBufferForDispatcher) Append(_ context.Context, _ model.ForwardEntry) error {
	return nil
}

func (m *mockBufferForDispatcher) LastReceivedAt(_ context.Context, _ string) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

func (m *mockBufferForDispatcher) LoadBatch(_ context.Context, chatID string) ([]model.ForwardEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	return m.batches[chatID], nil
}

func (m *mockBufferForDispatcher) DeleteBatchTx(_ context.Context, _ *sql.Tx, _ string) error {
	return nil
}

func (m *mockBufferForDispatcher) ListPendingChats(_ context.Context, _ time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var chats []string
	for k := range m.batches {
		chats = append(chats, k)
	}
	return chats, nil
}

// --- Tests ---

func TestForwardDispatcher_EmptyBuffer_PipelineNotCalled(t *testing.T) {
	// Buffer has no entries for this chat — pipeline.Process must not be called.
	buf := newMockBufferForDispatcher(nil)

	p := New(Config{Logger: discardLogger})

	sigCh := make(chan model.ForwardDispatchSignal, 4)
	d := NewForwardDispatcher(buf, sigCh, p, nil, discardLogger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.Run(ctx)
	}()

	d.Submit(model.ForwardDispatchSignal{
		ChatID: "chat-empty",
		Source: "test",
		ReplyFn: func(_ context.Context, _ string) error { return nil },
	})

	// Give the dispatcher time to drain the signal.
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	// Test passes if no panic occurs and Run exits cleanly.
}

func TestForwardDispatcher_CtxDone_ExitsRun(t *testing.T) {
	buf := newMockBufferForDispatcher(nil)
	sigCh := make(chan model.ForwardDispatchSignal, 4)
	d := NewForwardDispatcher(buf, sigCh, New(Config{Logger: discardLogger}), nil, discardLogger)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.Run(ctx)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after ctx cancellation")
	}
}

func TestForwardDispatcher_ClosedChannel_ExitsRun(t *testing.T) {
	buf := newMockBufferForDispatcher(nil)
	sigCh := make(chan model.ForwardDispatchSignal, 4)
	d := NewForwardDispatcher(buf, sigCh, New(Config{Logger: discardLogger}), nil, discardLogger)

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.Run(context.Background())
	}()

	close(sigCh)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after channel close")
	}
}

func TestForwardDispatcher_Submit_NonBlocking_DropOnFull(t *testing.T) {
	buf := newMockBufferForDispatcher(nil)
	// Capacity 0 → always full when nobody is reading.
	sigCh := make(chan model.ForwardDispatchSignal, 0)
	d := NewForwardDispatcher(buf, sigCh, New(Config{Logger: discardLogger}), nil, discardLogger)

	// Must not block or panic.
	d.Submit(model.ForwardDispatchSignal{ChatID: "chat-1", Source: "test"})
}

func TestForwardDispatcher_TwoSignals_ProcessedSequentially(t *testing.T) {
	now := time.Now()
	entries := []model.ForwardEntry{
		{ChatID: "chat-seq", MessageID: "1", IsSelfAuthored: true,
			OriginalDate: now, ReceivedAt: now, Text: "обещание"},
	}

	var (
		mu        sync.Mutex
		inFlight  int
		maxFlight int
		loadCalls int
	)

	// Verify sequential invariant by checking that LoadBatch is never called concurrently.
	// Since processSignal is always called from the single Run goroutine, max concurrent
	// LoadBatch calls should be 1.
	serialBuf := &serialCheckBuffer{
		batches: map[string][]model.ForwardEntry{"chat-seq": entries},
		onLoad: func() {
			mu.Lock()
			inFlight++
			if inFlight > maxFlight {
				maxFlight = inFlight
			}
			mu.Unlock()

			time.Sleep(20 * time.Millisecond)

			mu.Lock()
			inFlight--
			loadCalls++
			mu.Unlock()
		},
	}

	sigCh := make(chan model.ForwardDispatchSignal, 4)
	d := NewForwardDispatcher(serialBuf, sigCh, New(Config{Logger: discardLogger}), nil, discardLogger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.Run(ctx)
	}()

	replyFn := func(_ context.Context, _ string) error { return nil }
	for i := 0; i < 2; i++ {
		d.Submit(model.ForwardDispatchSignal{
			ChatID:  "chat-seq",
			Source:  "test",
			ReplyFn: replyFn,
		})
	}

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if maxFlight > 1 {
		t.Errorf("max concurrent processSignal calls = %d, want <= 1 (sequential)", maxFlight)
	}
}

// serialCheckBuffer wraps mockBufferForDispatcher and calls onLoad on each LoadBatch.
type serialCheckBuffer struct {
	batches map[string][]model.ForwardEntry
	onLoad  func()
}

func (s *serialCheckBuffer) Append(_ context.Context, _ model.ForwardEntry) error { return nil }
func (s *serialCheckBuffer) LastReceivedAt(_ context.Context, _ string) (time.Time, bool, error) {
	return time.Time{}, false, nil
}
func (s *serialCheckBuffer) LoadBatch(_ context.Context, chatID string) ([]model.ForwardEntry, error) {
	if s.onLoad != nil {
		s.onLoad()
	}
	return s.batches[chatID], nil
}
func (s *serialCheckBuffer) DeleteBatchTx(_ context.Context, _ *sql.Tx, _ string) error { return nil }
func (s *serialCheckBuffer) ListPendingChats(_ context.Context, _ time.Time) ([]string, error) {
	return nil, nil
}

func TestForwardDispatcher_EnrichFn_CalledForSweeperSignal(t *testing.T) {
	now := time.Now()
	entries := []model.ForwardEntry{
		{ChatID: "chat-enrich", MessageID: "1", IsSelfAuthored: false,
			ForwardFromName: "Вася", OriginalDate: now, ReceivedAt: now, Text: "привет"},
	}
	buf := newMockBufferForDispatcher(map[string][]model.ForwardEntry{
		"chat-enrich": entries,
	})

	var enrichCalled atomic.Bool

	enrichFn := EnrichFn(func(_ string) (
		func(context.Context, []string) error,
		func(context.Context, string) error,
		error,
	) {
		enrichCalled.Store(true)
		replyFn := func(_ context.Context, _ string) error { return nil }
		return nil, replyFn, nil
	})

	sigCh := make(chan model.ForwardDispatchSignal, 4)
	d := NewForwardDispatcher(buf, sigCh, New(Config{Logger: discardLogger}), enrichFn, discardLogger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.Run(ctx)
	}()

	// Sweeper signal: no ReplyFn.
	d.Submit(model.ForwardDispatchSignal{
		ChatID: "chat-enrich",
		Source: "sweeper",
	})

	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	if !enrichCalled.Load() {
		t.Error("enrichFn must be called for sweeper signal with nil ReplyFn")
	}
}

func TestForwardDispatcher_EnrichFn_Error_SignalDropped(t *testing.T) {
	now := time.Now()
	buf := newMockBufferForDispatcher(map[string][]model.ForwardEntry{
		"chat-err": {{ChatID: "chat-err", MessageID: "1", OriginalDate: now, Text: "test"}},
	})

	enrichErr := errors.New("enrich failed")
	enrichFn := EnrichFn(func(_ string) (func(context.Context, []string) error, func(context.Context, string) error, error) {
		return nil, nil, enrichErr
	})

	sigCh := make(chan model.ForwardDispatchSignal, 4)
	d := NewForwardDispatcher(buf, sigCh, New(Config{Logger: discardLogger}), enrichFn, discardLogger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.Run(ctx)
	}()

	d.Submit(model.ForwardDispatchSignal{ChatID: "chat-err", Source: "sweeper"})

	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done
	// No panic means signal was gracefully dropped on enrichFn error.
}

func TestMostFrequentCounterpart(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		forwards []model.ForwardEntry
		want     string
	}{
		{
			name: "all self-authored",
			forwards: []model.ForwardEntry{
				{IsSelfAuthored: true, ForwardFromName: "ignored", OriginalDate: now},
			},
			want: "",
		},
		{
			name: "single counterpart",
			forwards: []model.ForwardEntry{
				{IsSelfAuthored: false, ForwardFromName: "Вася", OriginalDate: now},
				{IsSelfAuthored: true, ForwardFromName: "", OriginalDate: now},
			},
			want: "Вася",
		},
		{
			name: "two counterparts Vася more frequent",
			forwards: []model.ForwardEntry{
				{IsSelfAuthored: false, ForwardFromName: "Вася", OriginalDate: now},
				{IsSelfAuthored: false, ForwardFromName: "Вася", OriginalDate: now},
				{IsSelfAuthored: false, ForwardFromName: "Петя", OriginalDate: now},
			},
			want: "Вася",
		},
		{
			name: "empty name is ignored",
			forwards: []model.ForwardEntry{
				{IsSelfAuthored: false, ForwardFromName: "", OriginalDate: now},
				{IsSelfAuthored: false, ForwardFromName: "Маша", OriginalDate: now},
			},
			want: "Маша",
		},
		{
			name:     "empty slice",
			forwards: nil,
			want:     "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mostFrequentCounterpart(tc.forwards)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
