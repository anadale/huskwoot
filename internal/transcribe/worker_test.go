package transcribe_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/transcribe"
)

// --- stubs ---

type stubQueue struct {
	mu          sync.Mutex
	jobs        []model.VoiceJob
	nextID      int64
	completedIDs []int64
	rescheduled  []rescheduleCall
}

type rescheduleCall struct {
	id      int64
	nextAt  time.Time
	lastErr string
}

func newStubQueue(jobs ...model.VoiceJob) *stubQueue {
	q := &stubQueue{}
	for i, j := range jobs {
		j.ID = int64(i + 1)
		q.jobs = append(q.jobs, j)
	}
	q.nextID = int64(len(jobs) + 1)
	return q
}

func (q *stubQueue) Enqueue(ctx context.Context, job model.VoiceJob) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.nextID++
	job.ID = q.nextID
	q.jobs = append(q.jobs, job)
	return nil
}

func (q *stubQueue) Claim(_ context.Context, now time.Time) (*model.VoiceJob, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, j := range q.jobs {
		if now.Unix() >= j.ReceivedAt.Unix() {
			// Remove from the list so we don't return it twice.
			q.jobs = append(q.jobs[:i], q.jobs[i+1:]...)
			return &j, nil
		}
	}
	return nil, nil
}

func (q *stubQueue) Complete(_ context.Context, id int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.completedIDs = append(q.completedIDs, id)
	return nil
}

func (q *stubQueue) Reschedule(_ context.Context, id int64, nextAt time.Time, lastErr string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rescheduled = append(q.rescheduled, rescheduleCall{id: id, nextAt: nextAt, lastErr: lastErr})
	// Put the job back with updated next_attempt_at so future Claim works.
	q.jobs = append(q.jobs, model.VoiceJob{
		ID:         id,
		ChatID:     0,
		ReceivedAt: nextAt,
		Attempts:   int(int64(len(q.rescheduled))),
	})
	return nil
}

type stubTranscriber struct {
	mu      sync.Mutex
	fn      func(ctx context.Context, audio model.AudioInput) (string, error)
	calls   []model.AudioInput
}

func (s *stubTranscriber) Transcribe(ctx context.Context, audio model.AudioInput) (string, error) {
	s.mu.Lock()
	s.calls = append(s.calls, audio)
	s.mu.Unlock()
	if s.fn != nil {
		return s.fn(ctx, audio)
	}
	return "", nil
}

func (s *stubTranscriber) Name() string { return "stub" }

// --- helpers ---

func makeJob(chatID int64, msgID int, attempts int) model.VoiceJob {
	return model.VoiceJob{
		ID:         int64(msgID),
		ChatID:     chatID,
		MessageID:  msgID,
		FileID:     "file1",
		MimeType:   "audio/ogg",
		Duration:   5 * time.Second,
		ReceivedAt: time.Unix(1700000000, 0).UTC(),
		Attempts:   attempts,
	}
}

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// --- tests ---

// TestWorker_SuccessCallsHandlerAndCompletes verifies that a successful transcription
// calls the handler with the right model.Message and then completes the job.
func TestWorker_SuccessCallsHandlerAndCompletes(t *testing.T) {
	job := makeJob(100, 1, 0)
	q := newStubQueue(job)

	tr := &stubTranscriber{fn: func(_ context.Context, _ model.AudioInput) (string, error) {
		return "hello world", nil
	}}

	var handledMsgs []model.Message
	handler := func(_ context.Context, msg model.Message) error {
		handledMsgs = append(handledMsgs, msg)
		return nil
	}

	builtMsgs := 0
	buildMessage := func(j model.VoiceJob, text string) model.Message {
		builtMsgs++
		return model.Message{
			Kind: model.MessageKindDM,
			Text: text,
		}
	}

	now := time.Unix(1700000010, 0)
	w := transcribe.NewWorker(transcribe.WorkerDeps{
		Queue:        q,
		Transcriber:  tr,
		DownloadFn:   func(_ context.Context, _ string) ([]byte, error) { return []byte("data"), nil },
		Handler:      handler,
		BuildMessage: buildMessage,
		Clock:        fixedClock(now),
		Interval:     10 * time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = w.Run(ctx)

	if len(handledMsgs) != 1 {
		t.Fatalf("handler called %d times, want 1", len(handledMsgs))
	}
	if handledMsgs[0].Text != "hello world" {
		t.Errorf("handler message text: want %q, got %q", "hello world", handledMsgs[0].Text)
	}
	if builtMsgs != 1 {
		t.Errorf("BuildMessage called %d times, want 1", builtMsgs)
	}
	if len(q.completedIDs) != 1 || q.completedIDs[0] != job.ID {
		t.Errorf("Complete: want [%d], got %v", job.ID, q.completedIDs)
	}
}

// TestWorker_TranscribeErrorSchedulesReschedule verifies that a transcription error
// calls Reschedule with the correct backoff times (30s → 2m → 10m).
func TestWorker_TranscribeErrorSchedulesReschedule(t *testing.T) {
	tests := []struct {
		name        string
		attempts    int    // existing attempts on the job
		wantBackoff time.Duration
	}{
		{name: "first_failure", attempts: 0, wantBackoff: 30 * time.Second},
		{name: "second_failure", attempts: 1, wantBackoff: 2 * time.Minute},
		{name: "third_failure", attempts: 2, wantBackoff: 10 * time.Minute},
		{name: "capped_failure", attempts: 5, wantBackoff: 10 * time.Minute},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			job := makeJob(200, 2, tc.attempts)
			q := newStubQueue(job)

			tr := &stubTranscriber{fn: func(_ context.Context, _ model.AudioInput) (string, error) {
				return "", errors.New("stt unavailable")
			}}

			now := time.Unix(1700000010, 0)
			w := transcribe.NewWorker(transcribe.WorkerDeps{
				Queue:        q,
				Transcriber:  tr,
				DownloadFn:   func(_ context.Context, _ string) ([]byte, error) { return []byte("data"), nil },
				Handler:      func(_ context.Context, _ model.Message) error { return nil },
				BuildMessage: func(_ model.VoiceJob, _ string) model.Message { return model.Message{} },
				Clock:        fixedClock(now),
				Interval:     10 * time.Millisecond,
				MaxAttempts:  10, // high enough to not drop
			})

			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_ = w.Run(ctx)

			if len(q.rescheduled) == 0 {
				t.Fatal("Reschedule not called")
			}
			got := q.rescheduled[0].nextAt
			want := now.Add(tc.wantBackoff)
			if !got.Equal(want) {
				t.Errorf("nextAt: want %v, got %v", want, got)
			}
		})
	}
}

// TestWorker_DropAfterMaxAttempts verifies that after MaxAttempts failures, the job is
// dropped (Complete called) and OnDrop is invoked.
func TestWorker_DropAfterMaxAttempts(t *testing.T) {
	// Job already has 3 attempts; MaxAttempts = 4 → next failure triggers drop.
	job := makeJob(300, 3, 3)
	q := newStubQueue(job)

	tr := &stubTranscriber{fn: func(_ context.Context, _ model.AudioInput) (string, error) {
		return "", errors.New("stt error")
	}}

	var droppedChatIDs []int64
	onDrop := func(_ context.Context, chatID int64) {
		droppedChatIDs = append(droppedChatIDs, chatID)
	}

	now := time.Unix(1700000010, 0)
	w := transcribe.NewWorker(transcribe.WorkerDeps{
		Queue:        q,
		Transcriber:  tr,
		DownloadFn:   func(_ context.Context, _ string) ([]byte, error) { return []byte("data"), nil },
		Handler:      func(_ context.Context, _ model.Message) error { return nil },
		BuildMessage: func(_ model.VoiceJob, _ string) model.Message { return model.Message{} },
		OnDrop:       onDrop,
		Clock:        fixedClock(now),
		Interval:     10 * time.Millisecond,
		MaxAttempts:  4,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = w.Run(ctx)

	if len(q.rescheduled) != 0 {
		t.Errorf("Reschedule must not be called on final drop, got %d calls", len(q.rescheduled))
	}
	if len(q.completedIDs) != 1 {
		t.Fatalf("Complete must be called once, got %d", len(q.completedIDs))
	}
	if len(droppedChatIDs) != 1 || droppedChatIDs[0] != 300 {
		t.Errorf("OnDrop: want chatID=300, got %v", droppedChatIDs)
	}
}

// TestWorker_DownloadErrorSchedulesReschedule verifies that a download failure
// causes the job to be rescheduled with the correct backoff.
func TestWorker_DownloadErrorSchedulesReschedule(t *testing.T) {
	job := makeJob(400, 4, 0)
	q := newStubQueue(job)

	tr := &stubTranscriber{fn: func(_ context.Context, _ model.AudioInput) (string, error) {
		return "", errors.New("should not be called")
	}}

	now := time.Unix(1700000010, 0)
	w := transcribe.NewWorker(transcribe.WorkerDeps{
		Queue:        q,
		Transcriber:  tr,
		DownloadFn:   func(_ context.Context, _ string) ([]byte, error) { return nil, errors.New("network error") },
		Handler:      func(_ context.Context, _ model.Message) error { return nil },
		BuildMessage: func(_ model.VoiceJob, _ string) model.Message { return model.Message{} },
		Clock:        fixedClock(now),
		Interval:     10 * time.Millisecond,
		MaxAttempts:  10,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = w.Run(ctx)

	tr.mu.Lock()
	calls := tr.calls
	tr.mu.Unlock()
	if len(calls) != 0 {
		t.Errorf("Transcribe must not be called after download error, got %d calls", len(calls))
	}
	if len(q.rescheduled) == 0 {
		t.Fatal("Reschedule not called after download error")
	}
	want := now.Add(30 * time.Second)
	if !q.rescheduled[0].nextAt.Equal(want) {
		t.Errorf("nextAt: want %v, got %v", want, q.rescheduled[0].nextAt)
	}
}

// TestWorker_EmptyTranscriptionCallsOnEmpty verifies that an empty transcription
// result triggers OnEmpty and completes the job without rescheduling.
func TestWorker_EmptyTranscriptionCallsOnEmpty(t *testing.T) {
	job := makeJob(500, 5, 0)
	q := newStubQueue(job)

	tr := &stubTranscriber{fn: func(_ context.Context, _ model.AudioInput) (string, error) {
		return "   ", nil
	}}

	var emptyChatIDs []int64
	onEmpty := func(_ context.Context, chatID int64) {
		emptyChatIDs = append(emptyChatIDs, chatID)
	}

	now := time.Unix(1700000010, 0)
	w := transcribe.NewWorker(transcribe.WorkerDeps{
		Queue:        q,
		Transcriber:  tr,
		DownloadFn:   func(_ context.Context, _ string) ([]byte, error) { return []byte("data"), nil },
		Handler:      func(_ context.Context, _ model.Message) error { return nil },
		BuildMessage: func(_ model.VoiceJob, _ string) model.Message { return model.Message{} },
		OnEmpty:      onEmpty,
		Clock:        fixedClock(now),
		Interval:     10 * time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = w.Run(ctx)

	if len(emptyChatIDs) != 1 || emptyChatIDs[0] != 500 {
		t.Errorf("OnEmpty: want chatID=500, got %v", emptyChatIDs)
	}
	if len(q.rescheduled) != 0 {
		t.Errorf("Reschedule must not be called for empty result, got %d calls", len(q.rescheduled))
	}
	if len(q.completedIDs) != 1 {
		t.Errorf("Complete must be called once for empty result, got %d calls", len(q.completedIDs))
	}
}

// TestWorker_NoJobsIsNoop verifies that a tick with an empty queue does not call
// any callbacks.
func TestWorker_NoJobsIsNoop(t *testing.T) {
	q := newStubQueue()
	tr := &stubTranscriber{}

	handlerCalled := 0
	w := transcribe.NewWorker(transcribe.WorkerDeps{
		Queue:        q,
		Transcriber:  tr,
		DownloadFn:   func(_ context.Context, _ string) ([]byte, error) { return nil, errors.New("should not be called") },
		Handler:      func(_ context.Context, _ model.Message) error { handlerCalled++; return nil },
		BuildMessage: func(_ model.VoiceJob, _ string) model.Message { return model.Message{} },
		Clock:        fixedClock(time.Now()),
		Interval:     10 * time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = w.Run(ctx)

	if handlerCalled != 0 {
		t.Errorf("handler must not be called on empty queue, got %d calls", handlerCalled)
	}
	tr.mu.Lock()
	calls := tr.calls
	tr.mu.Unlock()
	if len(calls) != 0 {
		t.Errorf("Transcribe must not be called on empty queue, got %d calls", len(calls))
	}
}
