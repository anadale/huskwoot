package model

import "time"

// Device describes a client device subscribed to instance events.
// TokenHash is the SHA256 hash of the bearer token (hex); the token itself is not stored.
type Device struct {
	// ID is the device UUID.
	ID string
	// Name is the human-readable device name (e.g. "iPhone 17 Pro").
	Name string
	// Platform is the device platform: ios|android|macos|windows|linux.
	Platform string
	// TokenHash is the hex-encoded SHA256 of the bearer token.
	TokenHash string
	// APNSToken is the APNs push token (nil if not registered).
	APNSToken *string
	// FCMToken is the FCM push token (nil if not registered).
	FCMToken *string
	// CreatedAt is the time the device was registered.
	CreatedAt time.Time
	// LastSeenAt is the time of the last successful request (nil if no requests yet).
	LastSeenAt *time.Time
	// RevokedAt is the time the device was revoked (nil if active).
	RevokedAt *time.Time
}
