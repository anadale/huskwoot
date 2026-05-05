package transcribe

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// ErrEmptyTranscript is passed to VoiceConsumer.Fail when transcription succeeds
// but produces no text after trimming whitespace.
var ErrEmptyTranscript = errors.New("empty transcript")

// ErrUnsupportedFormat is returned by a Transcriber when the audio MIME type is not
// supported by the backend. The worker drops the job immediately on this error
// instead of retrying — the format will not change between attempts.
var ErrUnsupportedFormat = errors.New("unsupported audio format")

// voiceBackoff defines the wait intervals between retry attempts for voice transcription.
// After attempt N (0-based), the worker waits voiceBackoff[min(N, len-1)] before retrying.
var voiceBackoff = []time.Duration{
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
}

// WorkerConfig holds dependencies for the voice transcription worker.
type WorkerConfig struct {
	Queue       model.VoiceQueue
	Transcriber model.Transcriber
	// Downloaders maps source_kind → VoiceDownloader. Jobs with unknown source_kind are dropped.
	Downloaders map[string]model.VoiceDownloader
	// Consumers maps source_kind → VoiceConsumer. Jobs with unknown source_kind are dropped.
	Consumers   map[string]model.VoiceConsumer
	Interval    time.Duration
	MaxAttempts int
	Clock       func() time.Time
	Logger      *slog.Logger
}

// Worker polls voice_queue and transcribes voice messages asynchronously.
// On success: calls Consumer.Consume, then Complete.
// On empty transcript: calls Consumer.Fail(ErrEmptyTranscript), then Complete.
// On transient error: Reschedule with exponential backoff.
// On final failure (MaxAttempts reached): Consumer.Fail, then Complete.
type Worker struct {
	queue       model.VoiceQueue
	transcriber model.Transcriber
	downloaders map[string]model.VoiceDownloader
	consumers   map[string]model.VoiceConsumer
	interval    time.Duration
	maxAttempts int
	clock       func() time.Time
	logger      *slog.Logger
}

// NewWorker creates a Worker with the given configuration.
func NewWorker(cfg WorkerConfig) *Worker {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Second
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 4
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Worker{
		queue:       cfg.Queue,
		transcriber: cfg.Transcriber,
		downloaders: cfg.Downloaders,
		consumers:   cfg.Consumers,
		interval:    cfg.Interval,
		maxAttempts: cfg.MaxAttempts,
		clock:       cfg.Clock,
		logger:      cfg.Logger,
	}
}

// Run starts the worker polling loop. Blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		for {
			job, err := w.queue.Claim(ctx, w.clock())
			if err != nil {
				w.logger.WarnContext(ctx, "voice: queue claim error", "err", err)
				break
			}
			if job == nil {
				break
			}
			w.processOne(ctx, *job)
		}
	}
}

func (w *Worker) processOne(ctx context.Context, job model.VoiceJob) {
	downloader, ok := w.downloaders[job.SourceKind]
	if !ok {
		w.logger.ErrorContext(ctx, "voice: unknown source kind, dropping job",
			"kind", job.SourceKind, "job_id", job.ID)
		w.completeJob(ctx, job.ID)
		return
	}
	consumer, ok := w.consumers[job.SourceKind]
	if !ok {
		w.logger.ErrorContext(ctx, "voice: no consumer for source kind, dropping job",
			"kind", job.SourceKind, "job_id", job.ID)
		w.completeJob(ctx, job.ID)
		return
	}

	data, err := downloader.Download(ctx, job)
	if err != nil {
		w.logger.ErrorContext(ctx, "voice: download failed", "job_id", job.ID, "kind", job.SourceKind, "err", err)
		w.rescheduleOrDrop(ctx, job, consumer, err)
		return
	}

	text, err := w.transcriber.Transcribe(ctx, model.AudioInput{
		Data:     data,
		MimeType: job.MimeType,
		Duration: job.Duration,
	})
	if err != nil {
		w.logger.ErrorContext(ctx, "voice: transcription failed", "job_id", job.ID,
			"transcriber", w.transcriber.Name(), "err", err)
		if errors.Is(err, ErrUnsupportedFormat) {
			consumer.Fail(ctx, job, err)
			w.completeJob(ctx, job.ID)
			return
		}
		w.rescheduleOrDrop(ctx, job, consumer, err)
		return
	}

	text = strings.TrimSpace(text)
	if text == "" {
		w.logger.InfoContext(ctx, "voice: empty transcription result", "job_id", job.ID)
		consumer.Fail(ctx, job, ErrEmptyTranscript)
		w.completeJob(ctx, job.ID)
		return
	}

	if err := consumer.Consume(ctx, job, text); err != nil {
		w.logger.ErrorContext(ctx, "voice: consumer failed", "job_id", job.ID, "err", err)
		w.rescheduleOrDrop(ctx, job, consumer, err)
		return
	}
	w.completeJob(ctx, job.ID)
}

func (w *Worker) rescheduleOrDrop(ctx context.Context, job model.VoiceJob, consumer model.VoiceConsumer, cause error) {
	attempts := job.Attempts + 1
	if attempts >= w.maxAttempts {
		w.logger.WarnContext(ctx, "voice: max attempts reached, dropping job",
			"job_id", job.ID, "kind", job.SourceKind, "attempts", attempts)
		consumer.Fail(ctx, job, cause)
		w.completeJob(ctx, job.ID)
		return
	}
	next := voiceNextAttempt(attempts, w.clock())
	if err := w.queue.Reschedule(ctx, job.ID, next, cause.Error()); err != nil {
		w.logger.WarnContext(ctx, "voice: failed to reschedule job, dropping to prevent spin",
			"job_id", job.ID, "err", err)
		consumer.Fail(ctx, job, cause)
		w.completeJob(ctx, job.ID)
	}
}

func (w *Worker) completeJob(ctx context.Context, id int64) {
	if err := w.queue.Complete(ctx, id); err != nil {
		w.logger.WarnContext(ctx, "voice: failed to complete job", "job_id", id, "err", err)
	}
}

// voiceNextAttempt computes the next attempt time using voiceBackoff.
// attempts is the attempt count after the current failure (already incremented).
func voiceNextAttempt(attempts int, now time.Time) time.Time {
	idx := attempts - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(voiceBackoff) {
		idx = len(voiceBackoff) - 1
	}
	return now.Add(voiceBackoff[idx])
}
