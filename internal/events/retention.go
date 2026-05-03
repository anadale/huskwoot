package events

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// orphanVoiceAge is the minimum file age before a voice upload without an active
// voice_queue row is considered an orphan and eligible for removal.
const orphanVoiceAge = 24 * time.Hour

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

// VoiceOrphanChecker checks whether a client voice upload still has an active
// job in the voice queue. Used by the orphan-file sweep in the retention runner.
// The concrete implementation is storage.SQLiteVoiceQueue.HasClientPayload.
type VoiceOrphanChecker interface {
	HasClientPayload(ctx context.Context, payload string) (bool, error)
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
	// VoiceChecker checks whether a client voice upload file still has an active
	// voice_queue row. When nil or UploadsDir is empty, the orphan-file sweep is skipped.
	VoiceChecker VoiceOrphanChecker
	// UploadsDir is the root directory for client voice uploads.
	// Empty string disables the orphan-file sweep.
	UploadsDir string
	// EventRetention is the retention horizon for events and delivered push jobs.
	EventRetention time.Duration
	// DeviceInactive is the idle period after which a non-revoked device is
	// auto-revoked. Zero disables the inactivity sweep.
	DeviceInactive time.Duration
	// DeviceRetention is the time after which a revoked device is physically
	// deleted from the store. Zero disables the physical deletion sweep.
	DeviceRetention time.Duration
	// Tasks is the task store; nil disables closed-task cleanup.
	Tasks model.TaskStore
	// TaskClosedRetention is the period after closed_at before a done/cancelled
	// task is physically deleted. Zero or negative disables the cleanup.
	TaskClosedRetention time.Duration
	// TaskCleanupHour and TaskCleanupMinute select the local time-of-day at
	// which the daily task cleanup runs (e.g. hour=4, minute=0 for 04:00).
	TaskCleanupHour   int
	TaskCleanupMinute int
	// TaskCleanupTZ is the timezone used to interpret the cleanup time.
	// nil falls back to time.UTC.
	TaskCleanupTZ *time.Location
	// Logger is the structured logger; slog.Default is used when nil.
	Logger *slog.Logger
}

// Runner periodically deletes stale domain events, delivered push jobs,
// expired pairing requests, revokes / deletes inactive devices, and sweeps
// orphaned voice upload files.
type Runner struct {
	events              model.EventStore
	queue               model.PushQueue
	pairing             model.PairingStore
	devices             model.DeviceStore
	revoker             RegistrationRevoker
	voiceChecker        VoiceOrphanChecker
	uploadsDir          string
	eventRetention      time.Duration
	deviceInactive      time.Duration
	deviceRetention     time.Duration
	tasks               model.TaskStore
	taskClosedRetention time.Duration
	taskCleanupHour     int
	taskCleanupMinute   int
	taskCleanupTZ       *time.Location
	nextTaskCleanup     time.Time
	interval            time.Duration
	logger              *slog.Logger
}

// NewRunner creates a Runner with the given configuration.
func NewRunner(cfg RunnerConfig) *Runner {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	tz := cfg.TaskCleanupTZ
	if tz == nil {
		tz = time.UTC
	}
	return &Runner{
		events:              cfg.Events,
		queue:               cfg.Queue,
		pairing:             cfg.Pairing,
		devices:             cfg.Devices,
		revoker:             cfg.Revoker,
		voiceChecker:        cfg.VoiceChecker,
		uploadsDir:          cfg.UploadsDir,
		eventRetention:      cfg.EventRetention,
		deviceInactive:      cfg.DeviceInactive,
		deviceRetention:     cfg.DeviceRetention,
		tasks:               cfg.Tasks,
		taskClosedRetention: cfg.TaskClosedRetention,
		taskCleanupHour:     cfg.TaskCleanupHour,
		taskCleanupMinute:   cfg.TaskCleanupMinute,
		taskCleanupTZ:       tz,
		interval:            defaultRetentionInterval,
		logger:              logger,
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
	r.sweepOrphanVoiceUploads(ctx, now)

	cutoff := now.Add(-r.eventRetention)

	if _, err := r.queue.DeleteDelivered(ctx, cutoff); err != nil {
		r.logger.Warn("push_queue cleanup", "error", err)
	}
	if _, err := r.events.DeleteOlderThan(ctx, cutoff); err != nil {
		r.logger.Warn("events cleanup", "error", err)
	}

	r.sweepClosedTasks(ctx, now)
	return nil
}

// sweepClosedTasks runs the daily cleanup of done/cancelled tasks if the
// scheduled time-of-day has been reached. The next-fire time is recomputed
// after each successful trigger so the sweep runs at most once per local day.
func (r *Runner) sweepClosedTasks(ctx context.Context, now time.Time) {
	if r.tasks == nil || r.taskClosedRetention <= 0 {
		return
	}
	if r.nextTaskCleanup.IsZero() {
		r.nextTaskCleanup = nextDailyAt(now, r.taskCleanupHour, r.taskCleanupMinute, r.taskCleanupTZ)
		return
	}
	if now.Before(r.nextTaskCleanup) {
		return
	}

	cutoff := now.Add(-r.taskClosedRetention)
	n, err := r.tasks.DeleteClosedOlderThan(ctx, cutoff)
	if err != nil {
		r.logger.Warn("tasks: closed cleanup", "error", err)
	} else if n > 0 {
		r.logger.Info("tasks: deleted closed tasks", "count", n,
			"older_than", cutoff.Format(time.RFC3339))
	}
	r.nextTaskCleanup = nextDailyAt(now, r.taskCleanupHour, r.taskCleanupMinute, r.taskCleanupTZ)
}

// nextDailyAt returns the next instant strictly after now whose local time
// (in tz) equals hour:minute. If today's slot has not yet passed, it returns
// today's slot; otherwise tomorrow's.
func nextDailyAt(now time.Time, hour, minute int, tz *time.Location) time.Time {
	local := now.In(tz)
	candidate := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, tz)
	if !candidate.After(local) {
		candidate = candidate.AddDate(0, 0, 1)
	}
	return candidate
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

// sweepOrphanVoiceUploads removes client voice upload files that are older than
// orphanVoiceAge and have no corresponding row in voice_queue. Empty subdirectories
// left after file removal are also cleaned up. Errors are logged as warnings and
// never block other retention steps.
func (r *Runner) sweepOrphanVoiceUploads(ctx context.Context, now time.Time) {
	if r.uploadsDir == "" || r.voiceChecker == nil {
		return
	}

	cutoff := now.Add(-orphanVoiceAge)
	var dirs []string

	_ = filepath.WalkDir(r.uploadsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			r.logger.Warn("voice uploads sweep: walk error", "path", path, "error", err)
			return nil
		}
		if d.IsDir() {
			if path != r.uploadsDir {
				dirs = append(dirs, path)
			}
			return nil
		}

		info, statErr := d.Info()
		if statErr != nil {
			r.logger.Warn("voice uploads sweep: stat", "path", path, "error", statErr)
			return nil
		}
		if !info.ModTime().Before(cutoff) {
			return nil
		}

		rel, relErr := filepath.Rel(r.uploadsDir, path)
		if relErr != nil {
			r.logger.Warn("voice uploads sweep: rel path", "path", path, "error", relErr)
			return nil
		}

		has, hasErr := r.voiceChecker.HasClientPayload(ctx, rel)
		if hasErr != nil {
			r.logger.Warn("voice uploads sweep: check payload", "payload", rel, "error", hasErr)
			return nil
		}
		if has {
			return nil
		}

		if rmErr := os.Remove(path); rmErr != nil {
			r.logger.Warn("voice uploads sweep: remove orphan", "path", path, "error", rmErr)
		}
		return nil
	})

	// Remove empty subdirectories. Walking produces dirs in lexical order,
	// so reversing gives innermost-first which is the correct removal order.
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // no-op on non-empty dirs
	}
}
