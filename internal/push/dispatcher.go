package push

import (
	"context"
	"log/slog"
	"time"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/pushproto"
)

var defaultBackoff = []time.Duration{
	5 * time.Second,
	30 * time.Second,
	5 * time.Minute,
	30 * time.Minute,
}

// TemplateResolver builds a push request from a domain event.
type TemplateResolver interface {
	Resolve(ctx context.Context, ev *model.Event) (*pushproto.PushRequest, bool, error)
}

// DispatcherDeps holds dependencies for the push notification dispatcher.
type DispatcherDeps struct {
	Queue       model.PushQueue
	Events      model.EventStore
	Devices     model.DeviceStore
	Relay       RelayClient
	Templates   TemplateResolver
	Clock       func() time.Time
	Logger      *slog.Logger
	Interval    time.Duration
	BatchSize   int
	MaxAttempts int
}

// Dispatcher is a ticker goroutine that reads push_queue, builds notifications
// via Templates, and sends them to the push relay.
type Dispatcher struct {
	queue       model.PushQueue
	events      model.EventStore
	devices     model.DeviceStore
	relay       RelayClient
	templates   TemplateResolver
	clock       func() time.Time
	logger      *slog.Logger
	interval    time.Duration
	batchSize   int
	maxAttempts int
}

// NewDispatcher creates a dispatcher with the given dependencies and defaults.
func NewDispatcher(deps DispatcherDeps) *Dispatcher {
	if deps.Clock == nil {
		deps.Clock = time.Now
	}
	if deps.Interval <= 0 {
		deps.Interval = 2 * time.Second
	}
	if deps.BatchSize <= 0 {
		deps.BatchSize = 32
	}
	if deps.MaxAttempts <= 0 {
		deps.MaxAttempts = 4
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &Dispatcher{
		queue:       deps.Queue,
		events:      deps.Events,
		devices:     deps.Devices,
		relay:       deps.Relay,
		templates:   deps.Templates,
		clock:       deps.Clock,
		logger:      deps.Logger,
		interval:    deps.Interval,
		batchSize:   deps.BatchSize,
		maxAttempts: deps.MaxAttempts,
	}
}

// Run starts the dispatcher loop. Blocks until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) error {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		batch, err := d.queue.NextBatch(ctx, d.batchSize)
		if err != nil {
			d.logger.WarnContext(ctx, "push: queue read error", "err", err)
			continue
		}
		for _, job := range batch {
			d.processOne(ctx, job)
		}
	}
}

func (d *Dispatcher) processOne(ctx context.Context, job model.PushJob) {
	ev, err := d.events.GetBySeq(ctx, job.EventSeq)
	if err != nil {
		d.logger.WarnContext(ctx, "push: failed to load event", "seq", job.EventSeq, "err", err)
		return
	}
	if ev == nil {
		d.logger.WarnContext(ctx, "push: event deleted by retention", "seq", job.EventSeq, "job_id", job.ID)
		d.dropJob(ctx, job.ID, "event_missing")
		return
	}

	req, ok, err := d.templates.Resolve(ctx, ev)
	if err != nil {
		d.logger.ErrorContext(ctx, "push: template error", "seq", ev.Seq, "err", err)
		d.dropJob(ctx, job.ID, "bad_payload")
		return
	}
	if !ok {
		d.dropJob(ctx, job.ID, "not_pushable")
		return
	}

	dev, err := d.devices.Get(ctx, job.DeviceID)
	if err != nil {
		d.logger.WarnContext(ctx, "push: failed to load device", "device_id", job.DeviceID, "err", err)
		return
	}
	if dev == nil || dev.RevokedAt != nil {
		d.dropJob(ctx, job.ID, "device_revoked")
		return
	}

	if dev.APNSToken == nil && dev.FCMToken == nil {
		d.dropJob(ctx, job.ID, "no_tokens")
		return
	}

	req.DeviceID = dev.ID
	resp, err := d.relay.Push(ctx, *req)
	if err != nil {
		d.handleNetworkError(ctx, job, err)
		return
	}

	switch resp.Status {
	case pushproto.StatusSent:
		if err := d.queue.MarkDelivered(ctx, job.ID); err != nil {
			d.logger.WarnContext(ctx, "push: failed to mark delivered", "job_id", job.ID, "err", err)
		}

	case pushproto.StatusInvalidToken:
		if err := d.devices.UpdatePushTokens(ctx, dev.ID, nil, nil); err != nil {
			d.logger.WarnContext(ctx, "push: failed to clear push tokens", "device_id", dev.ID, "err", err)
		}
		d.dropJob(ctx, job.ID, "invalid_token")

	case pushproto.StatusUpstreamError:
		retryAfter := time.Duration(resp.RetryAfter) * time.Second
		attempts := job.Attempts + 1
		if attempts >= d.maxAttempts {
			d.dropJob(ctx, job.ID, "max_attempts")
		} else {
			next := nextAttempt(attempts, d.clock(), retryAfter)
			if err := d.queue.MarkFailed(ctx, job.ID, resp.Message, next); err != nil {
				d.logger.WarnContext(ctx, "push: failed to update retry status", "job_id", job.ID, "err", err)
			}
		}

	case pushproto.StatusBadPayload:
		d.logger.ErrorContext(ctx, "push: bad_payload — instance-side error",
			"job_id", job.ID, "msg", resp.Message)
		d.dropJob(ctx, job.ID, "bad_payload")

	default:
		d.logger.ErrorContext(ctx, "push: unknown response status, dropping", "status", resp.Status, "job_id", job.ID)
		d.dropJob(ctx, job.ID, "unknown_status")
	}
}

func (d *Dispatcher) handleNetworkError(ctx context.Context, job model.PushJob, err error) {
	attempts := job.Attempts + 1
	if attempts >= d.maxAttempts {
		d.logger.WarnContext(ctx, "push: retry limit reached, dropping",
			"job_id", job.ID, "attempts", attempts)
		d.dropJob(ctx, job.ID, "max_attempts")
		return
	}
	next := nextAttempt(attempts, d.clock(), 0)
	if qErr := d.queue.MarkFailed(ctx, job.ID, err.Error(), next); qErr != nil {
		d.logger.WarnContext(ctx, "push: failed to update retry status", "job_id", job.ID, "err", qErr)
	}
}

func (d *Dispatcher) dropJob(ctx context.Context, jobID int64, reason string) {
	if err := d.queue.Drop(ctx, jobID, reason); err != nil {
		d.logger.WarnContext(ctx, "push: failed to drop job", "job_id", jobID, "reason", reason, "err", err)
	}
}

// nextAttempt computes the next attempt time using the backoff table.
// attempts is the attempt count after the current one (already incremented).
func nextAttempt(attempts int, now time.Time, retryAfter time.Duration) time.Time {
	idx := attempts - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(defaultBackoff) {
		idx = len(defaultBackoff) - 1
	}
	d := defaultBackoff[idx]
	if retryAfter > d {
		d = retryAfter
	}
	return now.Add(d)
}
