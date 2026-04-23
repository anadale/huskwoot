package model

import (
	"context"
	"database/sql"
	"time"
)

// Channel watches a message source and delivers new messages to a handler.
type Channel interface {
	// ID returns the unique channel identifier.
	ID() string

	// Watch starts monitoring the channel and calls handler for each new message.
	// Blocks until the context is cancelled.
	Watch(ctx context.Context, handler func(context.Context, Message) error) error

	// FetchHistory fetches message history to build context.
	FetchHistory(ctx context.Context, source Source, limit int) ([]Message, error)
}

// Extractor extracts structured tasks from a promise message.
type Extractor interface {
	// Extract builds tasks from a message using dialog history context.
	// Returns an empty slice if no promises are found.
	Extract(ctx context.Context, msg Message, history []HistoryEntry) ([]Task, error)
}

// Notifier sends the user a notification about new tasks.
type Notifier interface {
	// Notify sends a notification about a batch of tasks.
	Notify(ctx context.Context, tasks []Task) error
	// Name returns the human-readable notifier name for logging.
	Name() string
}

// History stores message history per channel.
type History interface {
	// Add adds a record to the history for the given source channel.
	Add(ctx context.Context, source string, entry HistoryEntry) error

	// Recent returns the last limit records from the given source channel.
	Recent(ctx context.Context, source string, limit int) ([]HistoryEntry, error)

	// RecentActivity returns records from the most recent activity wave:
	// looks for a stretch of records after a pause >= silenceGap.
	// If no such stretch is found, returns the last fallbackLimit records.
	RecentActivity(ctx context.Context, source string, silenceGap time.Duration, fallbackLimit int) ([]HistoryEntry, error)
}

// GuardStore persists pending guard approvals so they survive service restarts.
type GuardStore interface {
	// UpsertPending saves or updates a pending approval record.
	UpsertPending(ctx context.Context, chatID int64, welcomeMsgID int, deadline time.Time) error
	// DeletePending removes the pending record for a chat (confirmed or manually cleared).
	DeletePending(ctx context.Context, chatID int64) error
	// ListPending returns all pending approval records.
	ListPending(ctx context.Context) ([]GuardPending, error)
}

// StateStore persists and restores the read position per channel.
type StateStore interface {
	// GetCursor returns the saved cursor for a channel.
	// Returns nil, nil if no cursor is found.
	GetCursor(ctx context.Context, channelID string) (*Cursor, error)

	// SaveCursor saves the cursor for a channel.
	SaveCursor(ctx context.Context, channelID string, cursor Cursor) error
}

// Classifier classifies a message as Skip or Promise.
type Classifier interface {
	// Classify returns the classification of a message.
	Classify(ctx context.Context, msg Message) (Classification, error)
}

// TaskStore stores user projects and tasks.
//
// Write methods accept an external transaction (*sql.Tx) — the use-case layer
// owns the transaction so it can atomically write to multiple stores (events,
// push_queue) together with task or project changes. Read methods have no tx
// and go through the internal *sql.DB.
type TaskStore interface {
	// CreateProjectTx creates a new project within the given transaction. Requires p.Slug != "".
	// Returns an error on name duplication.
	CreateProjectTx(ctx context.Context, tx *sql.Tx, project *Project) error
	// UpdateProjectTx applies changes to a project within the given transaction.
	UpdateProjectTx(ctx context.Context, tx *sql.Tx, id string, upd ProjectUpdate) error
	// GetProject returns a project by UUID. Returns nil, nil if not found.
	GetProject(ctx context.Context, id string) (*Project, error)
	// GetProjectTx reads a project by UUID within the given transaction.
	// Needed by the use-case layer when it needs to read the current state
	// after UpdateProjectTx without releasing the single SQLite connection.
	GetProjectTx(ctx context.Context, tx *sql.Tx, id string) (*Project, error)
	// ListProjects returns all projects.
	ListProjects(ctx context.Context) ([]Project, error)
	// FindProjectByName finds a project by name. Returns nil, nil if not found.
	FindProjectByName(ctx context.Context, name string) (*Project, error)
	// CreateTaskTx creates a new task within the given transaction; sets Number, ID,
	// CreatedAt, UpdatedAt, Status="open".
	CreateTaskTx(ctx context.Context, tx *sql.Tx, task *Task) error
	// GetTask returns a task by UUID. Returns nil, nil if not found.
	GetTask(ctx context.Context, id string) (*Task, error)
	// GetTaskTx reads a task by UUID within the given transaction.
	// Needed by the use-case layer to read the current state after
	// UpdateTaskTx/MoveTaskTx without releasing the single SQLite connection.
	GetTaskTx(ctx context.Context, tx *sql.Tx, id string) (*Task, error)
	// GetTaskByRef finds a task by its human-readable reference "<slug>#<number>".
	// Returns nil, nil if not found.
	GetTaskByRef(ctx context.Context, projectSlug string, number int) (*Task, error)
	// ListTasks returns tasks matching the filter.
	// projectID="" means all projects.
	ListTasks(ctx context.Context, projectID string, filter TaskFilter) ([]Task, error)
	// UpdateTaskTx applies changes to a task within the given transaction.
	UpdateTaskTx(ctx context.Context, tx *sql.Tx, id string, update TaskUpdate) error
	// MoveTaskTx moves a task to another project within the given transaction,
	// assigning it a new number.
	MoveTaskTx(ctx context.Context, tx *sql.Tx, taskID, newProjectID string) error
	// DefaultProjectID returns the UUID of the "Inbox" project.
	DefaultProjectID() string
}

// SummaryDeliverer delivers a task digest to a single channel (Telegram, email, ...).
type SummaryDeliverer interface {
	// Deliver sends the digest to the recipient.
	Deliver(ctx context.Context, summary Summary) error
	// Name returns the human-readable deliverer name for logging.
	Name() string
}

// MetaStore stores arbitrary channel metadata as key-value pairs.
//
// Write methods accept an external transaction (*sql.Tx) — the use-case layer
// owns the transaction so it can atomically write metadata together with other stores.
// Read methods have no tx and go through the internal *sql.DB.
type MetaStore interface {
	// Get returns the value for the given key. Returns "", nil if the key is not found.
	Get(ctx context.Context, key string) (string, error)
	// SetTx sets the value for the given key within the given transaction.
	SetTx(ctx context.Context, tx *sql.Tx, key, value string) error
	// Values returns all values whose keys start with the given prefix.
	// Returns nil, nil if no matching keys are found.
	Values(ctx context.Context, prefix string) ([]string, error)
}

// DeviceStore stores client devices subscribed to instance events.
//
// Create accepts an external transaction because device registration may occur
// atomically with other records (events, push_queue) in future phases. Other
// write methods operate through the internal *sql.DB — they update a single
// row with no relation to other tables.
type DeviceStore interface {
	// Create registers a new device within the given transaction.
	// Populates CreatedAt; ID, Name, Platform, and TokenHash must already be set.
	Create(ctx context.Context, tx *sql.Tx, d *Device) error
	// FindByTokenHash looks up an active (non-revoked) device by its SHA256 bearer token hash.
	// Returns nil, nil if the device is not found or is revoked.
	FindByTokenHash(ctx context.Context, hash string) (*Device, error)
	// UpdateLastSeen updates the time of the device's last successful request.
	UpdateLastSeen(ctx context.Context, id string, at time.Time) error
	// Revoke marks a device as revoked. Repeated calls for an already-revoked device
	// do not change RevokedAt.
	Revoke(ctx context.Context, id string) error
	// List returns all devices (including revoked), sorted by CreatedAt ascending.
	List(ctx context.Context) ([]Device, error)
	// ListActiveIDs returns the IDs of all non-revoked devices.
	ListActiveIDs(ctx context.Context) ([]string, error)
	// UpdatePushTokens updates the APNs/FCM push tokens for a device.
	// A nil value clears the corresponding token.
	UpdatePushTokens(ctx context.Context, id string, apns, fcm *string) error
	// Get returns a device by ID (including revoked).
	// Returns nil, nil if the device is not found.
	Get(ctx context.Context, id string) (*Device, error)
	// ListInactive returns active (non-revoked) devices whose last activity
	// (COALESCE(last_seen_at, created_at)) is strictly older than cutoff.
	// Used by the retention runner to auto-revoke stale devices.
	ListInactive(ctx context.Context, cutoff time.Time) ([]Device, error)
	// DeleteRevokedOlderThan deletes devices that were revoked strictly before cutoff.
	// Returns the number of deleted rows.
	DeleteRevokedOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
}

// EventStore stores domain events (task/project/chat/...) published by use cases.
// Insert accepts an external transaction — the use case owns tx so it can atomically
// save entity changes together with the corresponding event and push_queue records.
// Read methods operate through the internal *sql.DB and require no transaction.
type EventStore interface {
	// Insert saves an event within the given transaction and returns
	// the assigned seq value (monotonically increasing).
	Insert(ctx context.Context, tx *sql.Tx, ev Event) (int64, error)
	// SinceSeq returns events with seq > afterSeq in ascending seq order,
	// limited to limit records. limit <= 0 means no limit.
	SinceSeq(ctx context.Context, afterSeq int64, limit int) ([]Event, error)
	// MaxSeq returns the maximum seq in the table. Returns 0, nil for an empty table.
	MaxSeq(ctx context.Context) (int64, error)
	// MinSeq returns the minimum seq in the table. Used to distinguish a retention
	// gap from a natural AUTOINCREMENT gap (a rolled-back transaction loses its seq
	// without deleting an event). Returns 0, nil for an empty table.
	MinSeq(ctx context.Context) (int64, error)
	// GetBySeq returns an event by seq. Returns nil, nil if the event does not exist
	// (deleted by retention or never created).
	GetBySeq(ctx context.Context, seq int64) (*Event, error)
	// DeleteOlderThan deletes events with CreatedAt strictly before cutoff.
	// Returns the number of deleted rows.
	DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
}

// PushQueue stores the push job queue for devices not connected to SSE at the time
// an event is published. The use case enqueues a job within the same transaction as
// the event insert — therefore Enqueue accepts *sql.Tx. Other methods read/update
// the queue outside a transaction and are used by the push dispatcher and retention goroutine.
type PushQueue interface {
	// Enqueue adds a single job to the queue within the given transaction.
	// Sets CreatedAt to now, NextAttemptAt = CreatedAt, Attempts = 0.
	Enqueue(ctx context.Context, tx *sql.Tx, deviceID string, eventSeq int64) error
	// NextBatch returns up to limit undelivered and non-dropped jobs
	// with NextAttemptAt <= now, sorted by NextAttemptAt ASC, id ASC.
	NextBatch(ctx context.Context, limit int) ([]PushJob, error)
	// MarkDelivered marks a job as successfully delivered (sets delivered_at = now).
	MarkDelivered(ctx context.Context, id int64) error
	// MarkFailed increments attempts, stores the error text, and schedules
	// the next retry no earlier than nextAttempt.
	MarkFailed(ctx context.Context, id int64, errText string, nextAttempt time.Time) error
	// Drop marks a job as permanently failed (dropped_at = now) with the given reason.
	Drop(ctx context.Context, id int64, reason string) error
	// DeleteDelivered removes delivered or dropped jobs whose completion
	// (delivered_at or dropped_at) occurred before cutoff.
	// Returns the number of deleted rows.
	DeleteDelivered(ctx context.Context, cutoff time.Time) (int64, error)
}

// Broker fans out published events to active SSE subscribers within a single process.
// No database access: the broker receives an already-saved event (after the use-case
// commit) and fans it out over local channels.
//
// Contract: Subscribe creates a subscription for a specific device. Returns an event
// channel and an unsubscribe function. Notify does not block the caller: if a
// subscriber's channel is full, the broker drops and closes it — the client will
// reconnect and receive missed events via events.SinceSeq using Last-Event-ID.
type Broker interface {
	// Subscribe registers a new subscriber for the given deviceID. Channels for
	// different subscribers of the same device are independent. The returned
	// unsubscribe function is idempotent and safe to call multiple times.
	Subscribe(deviceID string) (<-chan Event, func())
	// IsActive reports whether there is at least one subscriber for the given device.
	IsActive(deviceID string) bool
	// Notify fans the event out to all subscribers. Slow subscribers are dropped.
	Notify(ev Event)
}
