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
	mu            sync.Mutex
	jobs          []model.VoiceJob
	nextID        int64
	completedIDs  []int64
	rescheduled   []rescheduleCall
	rescheduleErr error
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

func (q *stubQueue) Enqueue(_ context.Context, job model.VoiceJob) error {
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
	if q.rescheduleErr != nil {
		return q.rescheduleErr
	}
	// Find and restore the job with updated fields so future Claim can return it.
	// Preserve SourceKind so the consumer/downloader map lookup still works.
	var kind string
	for _, j := range q.jobs {
		if j.ID == id {
			kind = j.SourceKind
			break
		}
	}
	q.jobs = append(q.jobs, model.VoiceJob{
		ID:         id,
		SourceKind: kind,
		ReceivedAt: nextAt,
		Attempts:   len(q.rescheduled),
	})
	return nil
}

type stubTranscriber struct {
	mu    sync.Mutex
	fn    func(ctx context.Context, audio model.AudioInput) (string, error)
	calls []model.AudioInput
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

type consumeCall struct {
	job        model.VoiceJob
	transcript string
}

type failCall struct {
	job   model.VoiceJob
	cause error
}

type stubConsumer struct {
	mu         sync.Mutex
	consumed   []consumeCall
	failed     []failCall
	consumeErr error
}

func (s *stubConsumer) Consume(_ context.Context, job model.VoiceJob, transcript string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consumed = append(s.consumed, consumeCall{job: job, transcript: transcript})
	return s.consumeErr
}

func (s *stubConsumer) Fail(_ context.Context, job model.VoiceJob, cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed = append(s.failed, failCall{job: job, cause: cause})
}

type stubDownloader struct {
	mu    sync.Mutex
	calls []model.VoiceJob
	data  []byte
	err   error
}

func (d *stubDownloader) Download(_ context.Context, job model.VoiceJob) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, job)
	return d.data, d.err
}

// --- helpers ---

func makeJob(kind string, chatID int64, msgID int, attempts int) model.VoiceJob {
	return model.VoiceJob{
		ID:         int64(msgID),
		SourceKind: kind,
		ChatID:     chatID,
		MessageID:  msgID,
		Payload:    "file1",
		MimeType:   "audio/ogg",
		Duration:   5 * time.Second,
		ReceivedAt: time.Unix(1700000000, 0).UTC(),
		Attempts:   attempts,
	}
}

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func makeWorker(q *stubQueue, tr *stubTranscriber, dl *stubDownloader, consumer *stubConsumer, kind string, extra ...func(*transcribe.WorkerConfig)) *transcribe.Worker {
	cfg := transcribe.WorkerConfig{
		Queue:       q,
		Transcriber: tr,
		Downloaders: map[string]model.VoiceDownloader{kind: dl},
		Consumers:   map[string]model.VoiceConsumer{kind: consumer},
		Clock:       fixedClock(time.Unix(1700000010, 0)),
		Interval:    10 * time.Millisecond,
	}
	for _, fn := range extra {
		fn(&cfg)
	}
	return transcribe.NewWorker(cfg)
}

// --- tests ---

// TestWorker_TelegramJobCallsConsume verifies the happy path for a telegram job:
// downloader is called, transcriber is called, consumer.Consume is called with the transcript.
func TestWorker_TelegramJobCallsConsume(t *testing.T) {
	job := makeJob("telegram", 100, 1, 0)
	q := newStubQueue(job)
	dl := &stubDownloader{data: []byte("audio")}
	tr := &stubTranscriber{fn: func(_ context.Context, _ model.AudioInput) (string, error) {
		return "hello world", nil
	}}
	consumer := &stubConsumer{}

	w := makeWorker(q, tr, dl, consumer, "telegram")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = w.Run(ctx)

	dl.mu.Lock()
	dlCalls := dl.calls
	dl.mu.Unlock()
	if len(dlCalls) != 1 {
		t.Fatalf("Download called %d times, want 1", len(dlCalls))
	}
	if dlCalls[0].SourceKind != "telegram" {
		t.Errorf("Download got kind=%q, want telegram", dlCalls[0].SourceKind)
	}

	consumer.mu.Lock()
	consumed := consumer.consumed
	consumer.mu.Unlock()
	if len(consumed) != 1 {
		t.Fatalf("Consume called %d times, want 1", len(consumed))
	}
	if consumed[0].transcript != "hello world" {
		t.Errorf("transcript: want %q, got %q", "hello world", consumed[0].transcript)
	}
	if len(q.completedIDs) != 1 {
		t.Errorf("Complete called %d times, want 1", len(q.completedIDs))
	}
}

// TestWorker_ClientJobCallsConsume verifies the happy path for a client job.
func TestWorker_ClientJobCallsConsume(t *testing.T) {
	job := model.VoiceJob{
		ID:          1,
		SourceKind:  "client",
		DeviceID:    "dev-1",
		ClientJobID: "uuid-1",
		Payload:     "2026/05/uuid-1.ogg",
		MimeType:    "audio/ogg",
		ReceivedAt:  time.Unix(1700000000, 0).UTC(),
	}
	q := newStubQueue(job)
	dl := &stubDownloader{data: []byte("client-audio")}
	tr := &stubTranscriber{fn: func(_ context.Context, _ model.AudioInput) (string, error) {
		return "create task call mom", nil
	}}
	consumer := &stubConsumer{}

	w := makeWorker(q, tr, dl, consumer, "client")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = w.Run(ctx)

	consumer.mu.Lock()
	consumed := consumer.consumed
	consumer.mu.Unlock()
	if len(consumed) != 1 {
		t.Fatalf("Consume called %d times, want 1", len(consumed))
	}
	if consumed[0].job.ClientJobID != "uuid-1" {
		t.Errorf("ClientJobID: want uuid-1, got %q", consumed[0].job.ClientJobID)
	}
	if consumed[0].transcript != "create task call mom" {
		t.Errorf("transcript: want %q, got %q", "create task call mom", consumed[0].transcript)
	}
}

// TestWorker_DownloadErrorReschedulesWithBackoff verifies that a download failure
// causes the job to be rescheduled with the correct backoff times.
func TestWorker_DownloadErrorReschedulesWithBackoff(t *testing.T) {
	tests := []struct {
		name        string
		attempts    int
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
			job := makeJob("telegram", 200, 2, tc.attempts)
			q := newStubQueue(job)
			dl := &stubDownloader{err: errors.New("network error")}
			tr := &stubTranscriber{}
			consumer := &stubConsumer{}

			now := time.Unix(1700000010, 0)
			w := transcribe.NewWorker(transcribe.WorkerConfig{
				Queue:       q,
				Transcriber: tr,
				Downloaders: map[string]model.VoiceDownloader{"telegram": dl},
				Consumers:   map[string]model.VoiceConsumer{"telegram": consumer},
				Clock:       fixedClock(now),
				Interval:    10 * time.Millisecond,
				MaxAttempts: 10,
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

// TestWorker_MaxAttemptsCallsConsumerFail verifies that after MaxAttempts failures,
// Consumer.Fail is called and the job is completed (not rescheduled).
func TestWorker_MaxAttemptsCallsConsumerFail(t *testing.T) {
	// Job already has 3 attempts; MaxAttempts=4 → next failure triggers drop.
	job := makeJob("telegram", 300, 3, 3)
	q := newStubQueue(job)
	dlErr := errors.New("stt error")
	dl := &stubDownloader{err: dlErr}
	tr := &stubTranscriber{}
	consumer := &stubConsumer{}

	w := transcribe.NewWorker(transcribe.WorkerConfig{
		Queue:       q,
		Transcriber: tr,
		Downloaders: map[string]model.VoiceDownloader{"telegram": dl},
		Consumers:   map[string]model.VoiceConsumer{"telegram": consumer},
		Clock:       fixedClock(time.Unix(1700000010, 0)),
		Interval:    10 * time.Millisecond,
		MaxAttempts: 4,
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
	consumer.mu.Lock()
	failed := consumer.failed
	consumer.mu.Unlock()
	if len(failed) != 1 {
		t.Fatalf("Fail must be called once, got %d", len(failed))
	}
	if failed[0].job.ChatID != 300 {
		t.Errorf("Fail got chatID=%d, want 300", failed[0].job.ChatID)
	}
	if !errors.Is(failed[0].cause, dlErr) {
		t.Errorf("Fail got cause=%v, want download error %v", failed[0].cause, dlErr)
	}
}

// TestWorker_UnknownSourceKindDropsJob verifies that a job with an unrecognised
// source_kind is completed (dropped) without calling any consumer.
func TestWorker_UnknownSourceKindDropsJob(t *testing.T) {
	job := makeJob("unknown", 0, 99, 0)
	q := newStubQueue(job)
	dl := &stubDownloader{data: []byte("data")}
	tr := &stubTranscriber{}
	consumer := &stubConsumer{}

	// Register only "telegram"; "unknown" is absent.
	w := transcribe.NewWorker(transcribe.WorkerConfig{
		Queue:       q,
		Transcriber: tr,
		Downloaders: map[string]model.VoiceDownloader{"telegram": dl},
		Consumers:   map[string]model.VoiceConsumer{"telegram": consumer},
		Clock:       fixedClock(time.Unix(1700000010, 0)),
		Interval:    10 * time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = w.Run(ctx)

	if len(q.completedIDs) != 1 {
		t.Fatalf("Complete must be called once for unknown kind, got %d", len(q.completedIDs))
	}
	consumer.mu.Lock()
	defer consumer.mu.Unlock()
	if len(consumer.consumed) != 0 || len(consumer.failed) != 0 {
		t.Errorf("consumer must not be called for unknown kind: consumed=%d, failed=%d",
			len(consumer.consumed), len(consumer.failed))
	}
}

// TestWorker_EmptyTranscriptCallsConsumerFail verifies that an empty transcript
// causes Consumer.Fail(ErrEmptyTranscript) and completes the job (no reschedule).
func TestWorker_EmptyTranscriptCallsConsumerFail(t *testing.T) {
	job := makeJob("telegram", 500, 5, 0)
	q := newStubQueue(job)
	dl := &stubDownloader{data: []byte("data")}
	tr := &stubTranscriber{fn: func(_ context.Context, _ model.AudioInput) (string, error) {
		return "   ", nil // whitespace only
	}}
	consumer := &stubConsumer{}

	w := makeWorker(q, tr, dl, consumer, "telegram")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = w.Run(ctx)

	consumer.mu.Lock()
	consumed := consumer.consumed
	failed := consumer.failed
	consumer.mu.Unlock()

	if len(consumed) != 0 {
		t.Errorf("Consume must not be called for empty transcript, got %d calls", len(consumed))
	}
	if len(failed) != 1 {
		t.Fatalf("Fail must be called once for empty transcript, got %d", len(failed))
	}
	if !errors.Is(failed[0].cause, transcribe.ErrEmptyTranscript) {
		t.Errorf("Fail cause: want ErrEmptyTranscript, got %v", failed[0].cause)
	}
	if len(q.rescheduled) != 0 {
		t.Errorf("Reschedule must not be called for empty transcript, got %d calls", len(q.rescheduled))
	}
	if len(q.completedIDs) != 1 {
		t.Errorf("Complete must be called once for empty transcript, got %d", len(q.completedIDs))
	}
}

// TestWorker_NoJobsIsNoop verifies that an empty queue causes no callbacks.
func TestWorker_NoJobsIsNoop(t *testing.T) {
	q := newStubQueue()
	dl := &stubDownloader{}
	tr := &stubTranscriber{}
	consumer := &stubConsumer{}

	w := makeWorker(q, tr, dl, consumer, "telegram")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = w.Run(ctx)

	dl.mu.Lock()
	dlCalls := dl.calls
	dl.mu.Unlock()
	if len(dlCalls) != 0 {
		t.Errorf("Download must not be called on empty queue, got %d calls", len(dlCalls))
	}
	consumer.mu.Lock()
	defer consumer.mu.Unlock()
	if len(consumer.consumed) != 0 || len(consumer.failed) != 0 {
		t.Errorf("consumer must not be called on empty queue")
	}
}

// TestWorker_RescheduleFailCallsConsumerFail verifies that when Reschedule returns a DB
// error, consumer.Fail is still invoked so the client receives a voice_failed event
// instead of waiting forever.
func TestWorker_RescheduleFailCallsConsumerFail(t *testing.T) {
	job := makeJob("telegram", 200, 1, 0)
	q := newStubQueue(job)
	q.rescheduleErr = errors.New("db write error")
	dl := &stubDownloader{data: []byte("audio")}
	tr := &stubTranscriber{fn: func(_ context.Context, _ model.AudioInput) (string, error) {
		return "", errors.New("stt unavailable")
	}}
	consumer := &stubConsumer{}

	w := makeWorker(q, tr, dl, consumer, "telegram")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = w.Run(ctx)

	if len(consumer.failed) == 0 {
		t.Fatal("consumer.Fail not called when Reschedule returns an error")
	}
	if len(q.completedIDs) == 0 {
		t.Fatal("job not completed after reschedule failure")
	}
}

// TestWorker_TranscribeErrorSchedulesReschedule verifies that a transcription error
// causes a reschedule with the correct backoff.
func TestWorker_TranscribeErrorSchedulesReschedule(t *testing.T) {
	job := makeJob("telegram", 200, 2, 0)
	q := newStubQueue(job)
	dl := &stubDownloader{data: []byte("data")}
	tr := &stubTranscriber{fn: func(_ context.Context, _ model.AudioInput) (string, error) {
		return "", errors.New("stt unavailable")
	}}
	consumer := &stubConsumer{}

	now := time.Unix(1700000010, 0)
	w := transcribe.NewWorker(transcribe.WorkerConfig{
		Queue:       q,
		Transcriber: tr,
		Downloaders: map[string]model.VoiceDownloader{"telegram": dl},
		Consumers:   map[string]model.VoiceConsumer{"telegram": consumer},
		Clock:       fixedClock(now),
		Interval:    10 * time.Millisecond,
		MaxAttempts: 10,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = w.Run(ctx)

	if len(q.rescheduled) == 0 {
		t.Fatal("Reschedule not called after transcription error")
	}
	got := q.rescheduled[0].nextAt
	want := now.Add(30 * time.Second)
	if !got.Equal(want) {
		t.Errorf("nextAt: want %v, got %v", want, got)
	}
}
