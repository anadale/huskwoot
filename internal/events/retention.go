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

// RegistrationRevoker removes a device registration from the push relay.
// Concrete implementation is push.RelayClient, but the events package does not
// depend on push to avoid pulling transport concerns into retention.
type RegistrationRevoker interface {
	DeleteRegistration(ctx context.Context, deviceID string) error
}

// RunnerConfig groups the dependencies and retention windows of the Runner.
type RunnerConfig struct {
	// Events is the event store; required.
	Events model.EventStore
	// Queue is the push job queue; required.
	Queue model.PushQueue
	// Pairing is the pairing store; nil disables pairing_requests cleanup.
	Pairing model.PairingStore
	// Devices is the device store; nil disables devices cleanup.
	Devices model.DeviceStore
	// Revoker is used to notify the push relay when a stale device is auto-revoked.
	// nil is treated as a no-op; events retention does not block on relay errors.
	Revoker RegistrationRevoker
	// EventRetention is the retention horizon for events and delivered push jobs.
	EventRetention time.Duration
	// DeviceInactive is the idle period after which a non-revoked device is
	// auto-revoked. Zero disables the inactivity sweep.
	DeviceInactive time.Duration
	// DeviceRetention is the time after which a revoked device is physically
	// deleted from the store. Zero disables the physical deletion sweep.
	DeviceRetention time.Duration
	// Logger is the structured logger; slog.Default is used when nil.
	Logger *slog.Logger
}

// Runner periodically deletes stale domain events, delivered push jobs,
// expired pairing requests and revokes / deletes inactive devices.
type Runner struct {
	events          model.EventStore
	queue           model.PushQueue
	pairing         model.PairingStore
	devices         model.DeviceStore
	revoker         RegistrationRevoker
	eventRetention  time.Duration
	deviceInactive  time.Duration
	deviceRetention time.Duration
	interval        time.Duration
	logger          *slog.Logger
}

// NewRunner creates a Runner with the given configuration.
func NewRunner(cfg RunnerConfig) *Runner {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		events:          cfg.Events,
		queue:           cfg.Queue,
		pairing:         cfg.Pairing,
		devices:         cfg.Devices,
		revoker:         cfg.Revoker,
		eventRetention:  cfg.EventRetention,
		deviceInactive:  cfg.DeviceInactive,
		deviceRetention: cfg.DeviceRetention,
		interval:        defaultRetentionInterval,
		logger:          logger,
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

// Tick performs one cleanup cycle: pairing_requests first, then device sweeps
// (auto-revoke inactive + delete long-revoked), then push_queue (to release FK
// references on events), then events themselves. Errors from each subsystem
// are independent — a failure in one does not skip the others.
func (r *Runner) Tick(ctx context.Context, now time.Time) error {
	if r.pairing != nil {
		pairingCutoff := now.Add(-pairingCleanupDelay)
		if _, err := r.pairing.DeleteExpired(ctx, pairingCutoff); err != nil {
			r.logger.Warn("pairing_requests cleanup", "error", err)
		}
	}

	r.sweepDevices(ctx, now)

	cutoff := now.Add(-r.eventRetention)

	if _, err := r.queue.DeleteDelivered(ctx, cutoff); err != nil {
		r.logger.Warn("push_queue cleanup", "error", err)
	}
	if _, err := r.events.DeleteOlderThan(ctx, cutoff); err != nil {
		r.logger.Warn("events cleanup", "error", err)
	}
	return nil
}

// sweepDevices auto-revokes inactive devices and deletes long-revoked ones.
// Both sweeps are skipped when the corresponding window is zero or the store
// is not configured.
func (r *Runner) sweepDevices(ctx context.Context, now time.Time) {
	if r.devices == nil {
		return
	}

	if r.deviceInactive > 0 {
		inactiveCutoff := now.Add(-r.deviceInactive)
		inactive, err := r.devices.ListInactive(ctx, inactiveCutoff)
		if err != nil {
			r.logger.Warn("devices: list inactive", "error", err)
		} else {
			for _, d := range inactive {
				if err := r.devices.Revoke(ctx, d.ID); err != nil {
					r.logger.Warn("devices: auto-revoke",
						"device_id", d.ID, "error", err)
					continue
				}
				r.logger.Info("devices: auto-revoked inactive device",
					"device_id", d.ID, "name", d.Name,
					"inactive_since", lastActivity(d))
				if r.revoker != nil {
					if err := r.revoker.DeleteRegistration(ctx, d.ID); err != nil {
						r.logger.Warn("devices: relay delete registration after auto-revoke",
							"device_id", d.ID, "error", err)
					}
				}
			}
		}
	}

	if r.deviceRetention > 0 {
		revokedCutoff := now.Add(-r.deviceRetention)
		n, err := r.devices.DeleteRevokedOlderThan(ctx, revokedCutoff)
		if err != nil {
			r.logger.Warn("devices: delete revoked", "error", err)
		} else if n > 0 {
			r.logger.Info("devices: deleted long-revoked devices", "count", n)
		}
	}
}

// lastActivity returns the most informative timestamp describing when the
// device was last active: last_seen_at if set, otherwise created_at.
func lastActivity(d model.Device) time.Time {
	if d.LastSeenAt != nil {
		return *d.LastSeenAt
	}
	return d.CreatedAt
}
