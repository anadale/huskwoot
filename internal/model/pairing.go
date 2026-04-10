package model

import (
	"context"
	"database/sql"
	"time"
)

// PairingStatus describes the current state of a pairing request.
type PairingStatus string

const (
	// PairingStatusPending — the request is awaiting owner confirmation.
	PairingStatusPending PairingStatus = "pending"
	// PairingStatusConfirmed — the request was confirmed and the device registered.
	PairingStatusConfirmed PairingStatus = "confirmed"
	// PairingStatusExpired — the request expired without confirmation.
	PairingStatusExpired PairingStatus = "expired"
)

// PairingRequest holds input parameters for a device pairing request.
type PairingRequest struct {
	// DeviceName is the human-readable device name (e.g. "iPhone 17").
	DeviceName string
	// Platform is the device platform: ios|android|macos|windows|linux.
	Platform string
	// ClientNonce is a random string from the client; stored as its sha256 hash.
	ClientNonce string
	// APNSToken is the APNs push token (optional).
	APNSToken *string
	// FCMToken is the FCM push token (optional).
	FCMToken *string
}

// PendingPairing describes a pairing request record in the store.
type PendingPairing struct {
	// ID is the request UUID; used in the magic-link URL.
	ID string
	// DeviceName is the device name from the request.
	DeviceName string
	// Platform is the device platform from the request.
	Platform string
	// APNSToken is the APNs push token (nil if not provided).
	APNSToken *string
	// FCMToken is the FCM push token (nil if not provided).
	FCMToken *string
	// NonceHash is the hex-encoded SHA256 of ClientNonce.
	NonceHash string
	// CSRFHash is the hex-encoded SHA256 of the CSRF token; empty until generated.
	CSRFHash string
	// CreatedAt is the time the request was created.
	CreatedAt time.Time
	// ExpiresAt is the time the request expires (the magic link is invalid after this).
	ExpiresAt time.Time
	// ConfirmedAt is the time the owner confirmed the request (nil if not yet confirmed).
	ConfirmedAt *time.Time
	// IssuedDeviceID is the UUID of the created device (nil before confirmation).
	IssuedDeviceID *string
}

// PairingResult is the result of polling a pairing request status.
type PairingResult struct {
	// PairID is the request UUID.
	PairID string
	// Status is the current status.
	Status PairingStatus
	// DeviceID is the device UUID; populated only when Status==PairingStatusConfirmed.
	DeviceID string
	// BearerToken is the device bearer token; shown ONCE via the broadcaster.
	BearerToken string
}

// PairingStore manages pairing request records in the store.
//
// Write methods (CreateTx, SetCSRFTx, MarkConfirmedTx) accept an external
// transaction — the use-case layer owns tx to atomically write the pairing record
// together with the device. Read methods and DeleteExpired operate without a transaction.
type PairingStore interface {
	// CreateTx creates a new pairing request within the given transaction.
	// Populates CreatedAt; all other fields must be set by the caller.
	CreateTx(ctx context.Context, tx *sql.Tx, p *PendingPairing) error
	// Get returns a request by UUID. Returns nil, nil if not found.
	Get(ctx context.Context, id string) (*PendingPairing, error)
	// SetCSRFTx saves the SHA256 hash of the CSRF token within the given transaction.
	SetCSRFTx(ctx context.Context, tx *sql.Tx, id, csrfHash string) error
	// MarkConfirmedTx marks the request as confirmed and records the deviceID within the given transaction.
	MarkConfirmedTx(ctx context.Context, tx *sql.Tx, id, deviceID string) error
	// DeleteExpired deletes records whose expires_at < cutoff.
	// Returns the number of deleted rows.
	DeleteExpired(ctx context.Context, cutoff time.Time) (int64, error)
}
