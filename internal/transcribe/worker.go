package transcribe

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// voiceBackoff defines the wait intervals between retry attempts for voice transcription.
// After attempt N (0-based), the worker waits voiceBackoff[min(N, len-1)] before retrying.
var voiceBackoff = []time.Duration{
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
}

// WorkerDeps holds dependencies for the voice transcription worker.
type WorkerDeps struct {
	Queue        model.VoiceQueue
	Transcriber  model.Transcriber
	DownloadFn   func(ctx context.Context, fileID string) ([]byte, error)
	Handler      func(ctx context.Context, msg model.Message) error
	BuildMessage func(job model.VoiceJob, text string) model.Message
	// OnDrop is called when a job is permanently dropped (max attempts reached).
	// Typically sends a "voice.error" reply to the user. May be nil.
	OnDrop func(ctx context.Context, chatID int64)
	// OnEmpty is called when transcription produces an empty result.
	// Typically sends a "voice.empty" reply to the user. May be nil.
	OnEmpty  func(ctx context.Context, chatID int64)
	Interval    time.Duration
	MaxAttempts int
	Clock       func() time.Time
	Logger      *slog.Logger
}

// Worker polls voice_queue and transcribes voice messages asynchronously.
// On success: calls Handler with the transcribed model.Message, then Complete.
// On error: Reschedule with exponential backoff; after MaxAttempts: OnDrop + Complete.
type Worker struct {
	queue        model.VoiceQueue
	transcriber  model.Transcriber
	downloadFn   func(ctx context.Context, fileID string) ([]byte, error)
	handler      func(ctx context.Context, msg model.Message) error
	buildMessage func(job model.VoiceJob, text string) model.Message
	onDrop       func(ctx context.Context, chatID int64)
	onEmpty      func(ctx context.Context, chatID int64)
	interval     time.Duration
	maxAttempts  int
	clock        func() time.Time
	logger       *slog.Logger
}

// NewWorker creates a Worker with the given dependencies.
func NewWorker(deps WorkerDeps) *Worker {
	if deps.Clock == nil {
		deps.Clock = time.Now
	}
	if deps.Interval <= 0 {
		deps.Interval = 5 * time.Second
	}
	if deps.MaxAttempts <= 0 {
		deps.MaxAttempts = 4
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &Worker{
		queue:        deps.Queue,
		transcriber:  deps.Transcriber,
		downloadFn:   deps.DownloadFn,
		handler:      deps.Handler,
		buildMessage: deps.BuildMessage,
		onDrop:       deps.OnDrop,
		onEmpty:      deps.OnEmpty,
		interval:     deps.Interval,
		maxAttempts:  deps.MaxAttempts,
		clock:        deps.Clock,
		logger:       deps.Logger,
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
	data, err := w.downloadFn(ctx, job.FileID)
	if err != nil {
		w.logger.ErrorContext(ctx, "voice: download failed", "job_id", job.ID, "chat_id", job.ChatID, "err", err)
		w.rescheduleOrDrop(ctx, job, err.Error())
		return
	}

	text, err := w.transcriber.Transcribe(ctx, model.AudioInput{
		Data:     data,
		MimeType: job.MimeType,
		Duration: job.Duration,
	})
	if err != nil {
		w.logger.ErrorContext(ctx, "voice: transcription failed", "job_id", job.ID, "chat_id", job.ChatID,
			"transcriber", w.transcriber.Name(), "err", err)
		w.rescheduleOrDrop(ctx, job, err.Error())
		return
	}

	text = strings.TrimSpace(text)
	if text == "" {
		w.logger.InfoContext(ctx, "voice: empty transcription result", "job_id", job.ID, "chat_id", job.ChatID)
		if w.onEmpty != nil {
			w.onEmpty(ctx, job.ChatID)
		}
		w.completeJob(ctx, job.ID)
		return
	}

	msg := w.buildMessage(job, text)
	if err := w.handler(ctx, msg); err != nil {
		w.logger.ErrorContext(ctx, "voice: handler failed", "job_id", job.ID, "err", err)
		w.rescheduleOrDrop(ctx, job, err.Error())
		return
	}
	w.completeJob(ctx, job.ID)
}

func (w *Worker) rescheduleOrDrop(ctx context.Context, job model.VoiceJob, errText string) {
	attempts := job.Attempts + 1
	if attempts >= w.maxAttempts {
		w.logger.WarnContext(ctx, "voice: max attempts reached, dropping job",
			"job_id", job.ID, "chat_id", job.ChatID, "attempts", attempts)
		if w.onDrop != nil {
			w.onDrop(ctx, job.ChatID)
		}
		w.completeJob(ctx, job.ID)
		return
	}
	next := voiceNextAttempt(attempts, w.clock())
	if err := w.queue.Reschedule(ctx, job.ID, next, errText); err != nil {
		w.logger.WarnContext(ctx, "voice: failed to reschedule job, dropping to prevent spin",
			"job_id", job.ID, "err", err)
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
