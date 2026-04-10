package events

import (
	"context"
	"log/slog"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// defaultRetentionInterval is how often the retention cleanup runs.
const defaultRetentionInterval = time.Hour

// pairingCleanupDelay is how much later than expires_at a pairing record is deleted.
// Spec §3: "deleted one hour after expires_at".
const pairingCleanupDelay = time.Hour

// Runner periodically deletes stale domain events and delivered push jobs,
// as well as expired pairing requests.
// Cutoff for events/push_queue is computed as now - retention;
// cutoff for pairing_requests is now - pairingCleanupDelay (hard-coded).
type Runner struct {
	events    model.EventStore
	queue     model.PushQueue
	pairing   model.PairingStore // nil — pairing_requests cleanup is skipped
	retention time.Duration
	interval  time.Duration
	logger    *slog.Logger
}

// NewRunner creates a Runner with the given retention horizon. If logger is nil,
// slog.Default is used. pairingStore may be nil — pairing_requests cleanup is
// then skipped.
func NewRunner(ev model.EventStore, queue model.PushQueue, pairingStore model.PairingStore, retention time.Duration, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		events:    ev,
		queue:     queue,
		pairing:   pairingStore,
		retention: retention,
		interval:  defaultRetentionInterval,
		logger:    logger,
	}
}

// Run blocks the caller and runs Tick every interval. Returns when ctx is
// cancelled. Tick errors are only logged — they do not break the loop.
// The first Tick runs immediately on entry: otherwise a daemon that restarts
// frequently loses a full interval on every restart, letting the event and
// queue tables grow without cleanup.
func (r *Runner) Run(ctx context.Context) {
	if err := ctx.Err(); err != nil {
		return
	}
	_ = r.Tick(ctx, time.Now().UTC())

	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			_ = r.Tick(ctx, now.UTC())
		}
	}
}

// Tick performs one cleanup cycle: pairing_requests first, then push_queue
// (to release FK references on events), then events themselves. Errors are
// independent — each subsystem is cleaned up regardless of the others.
func (r *Runner) Tick(ctx context.Context, now time.Time) error {
	if r.pairing != nil {
		pairingCutoff := now.Add(-pairingCleanupDelay)
		if _, err := r.pairing.DeleteExpired(ctx, pairingCutoff); err != nil {
			r.logger.Warn("pairing_requests cleanup", "error", err)
		}
	}

	cutoff := now.Add(-r.retention)

	if _, err := r.queue.DeleteDelivered(ctx, cutoff); err != nil {
		r.logger.Warn("push_queue cleanup", "error", err)
	}
	if _, err := r.events.DeleteOlderThan(ctx, cutoff); err != nil {
		r.logger.Warn("events cleanup", "error", err)
	}
	return nil
}
