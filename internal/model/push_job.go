package model

import "time"

// PushJob describes a single row in the push notification queue. A job is created
// by the use case for inactive (offline) devices and later processed by the
// push relay dispatcher. The queue is populated but not consumed until the
// dispatcher is running.
type PushJob struct {
	// ID is the auto-increment push_queue row identifier.
	ID int64
	// DeviceID is the target device.
	DeviceID string
	// EventSeq is the seq of the event to deliver.
	EventSeq int64
	// CreatedAt is the time the job was enqueued.
	CreatedAt time.Time
	// Attempts is the number of delivery attempts already made.
	Attempts int
	// LastError is the error text from the last failed attempt (empty if none).
	LastError string
	// NextAttemptAt is the earliest time the next attempt should be made.
	NextAttemptAt time.Time
}
