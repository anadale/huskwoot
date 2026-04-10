// Package devices implements the store for client devices subscribed
// to instance events (SSE and push notifications).
package devices

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// SQLiteDeviceStore implements model.DeviceStore on top of SQLite.
type SQLiteDeviceStore struct {
	db *sql.DB
}

// NewSQLiteDeviceStore creates a new SQLiteDeviceStore with the given database.
func NewSQLiteDeviceStore(db *sql.DB) *SQLiteDeviceStore {
	return &SQLiteDeviceStore{db: db}
}

// Create registers a new device within the given transaction.
// Sets CreatedAt; ID, Name, Platform, and TokenHash must be provided.
func (s *SQLiteDeviceStore) Create(ctx context.Context, tx *sql.Tx, d *model.Device) error {
	if tx == nil {
		return fmt.Errorf("registering device %q: tx cannot be nil", d.Name)
	}
	if d.ID == "" {
		return fmt.Errorf("registering device %q: ID is required", d.Name)
	}
	if d.TokenHash == "" {
		return fmt.Errorf("registering device %q: TokenHash is required", d.Name)
	}
	d.CreatedAt = time.Now().UTC()

	_, err := tx.ExecContext(ctx,
		`INSERT INTO devices (id, name, platform, token_hash, apns_token, fcm_token, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.Name, d.Platform, d.TokenHash,
		nullableString(d.APNSToken), nullableString(d.FCMToken),
		d.CreatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("registering device %q: %w", d.Name, err)
	}
	return nil
}

// FindByTokenHash looks up an active device by the SHA256 hash of its bearer token.
// Returns nil, nil if the device is not found or has been revoked.
func (s *SQLiteDeviceStore) FindByTokenHash(ctx context.Context, hash string) (*model.Device, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, platform, token_hash, apns_token, fcm_token,
		        created_at, last_seen_at, revoked_at
		 FROM devices
		 WHERE token_hash = ? AND revoked_at IS NULL`,
		hash,
	)

	d, err := scanDevice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("looking up device by token_hash: %w", err)
	}
	return d, nil
}

// UpdateLastSeen updates the timestamp of the device's last successful request.
func (s *SQLiteDeviceStore) UpdateLastSeen(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE devices SET last_seen_at = ? WHERE id = ?`,
		at.UTC().Format(time.RFC3339), id,
	)
	if err != nil {
		return fmt.Errorf("updating last_seen_at for device %s: %w", id, err)
	}
	return nil
}

// Revoke marks a device as revoked. Repeated calls for an already-revoked
// device leave RevokedAt unchanged.
func (s *SQLiteDeviceStore) Revoke(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`UPDATE devices SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		now, id,
	)
	if err != nil {
		return fmt.Errorf("revoking device %s: %w", id, err)
	}
	return nil
}

// List returns all devices (including revoked ones) ordered by CreatedAt.
func (s *SQLiteDeviceStore) List(ctx context.Context) ([]model.Device, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, platform, token_hash, apns_token, fcm_token,
		        created_at, last_seen_at, revoked_at
		 FROM devices
		 ORDER BY created_at ASC, id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("listing devices: %w", err)
	}
	defer rows.Close()

	var devs []model.Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning device row: %w", err)
		}
		devs = append(devs, *d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating devices: %w", err)
	}
	return devs, nil
}

// ListActiveIDs returns the IDs of all non-revoked devices.
func (s *SQLiteDeviceStore) ListActiveIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM devices WHERE revoked_at IS NULL ORDER BY created_at ASC, id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("listing active devices: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning device id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating active devices: %w", err)
	}
	return ids, nil
}

// Get returns a device by ID (including revoked ones).
// Returns nil, nil if the device is not found.
func (s *SQLiteDeviceStore) Get(ctx context.Context, id string) (*model.Device, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, platform, token_hash, apns_token, fcm_token,
		        created_at, last_seen_at, revoked_at
		 FROM devices WHERE id = ?`,
		id,
	)
	d, err := scanDevice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting device %s: %w", id, err)
	}
	return d, nil
}

// UpdatePushTokens updates the APNs/FCM push tokens for a device. A nil value
// clears the corresponding token.
func (s *SQLiteDeviceStore) UpdatePushTokens(ctx context.Context, id string, apns, fcm *string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE devices SET apns_token = ?, fcm_token = ? WHERE id = ?`,
		nullableString(apns), nullableString(fcm), id,
	)
	if err != nil {
		return fmt.Errorf("updating push tokens for device %s: %w", id, err)
	}
	return nil
}

// rowScanner abstracts sql.Row and sql.Rows for shared device scanning.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanDevice(r rowScanner) (*model.Device, error) {
	var (
		d            model.Device
		apnsToken    sql.NullString
		fcmToken     sql.NullString
		createdAtStr string
		lastSeenStr  sql.NullString
		revokedAtStr sql.NullString
	)
	err := r.Scan(
		&d.ID, &d.Name, &d.Platform, &d.TokenHash,
		&apnsToken, &fcmToken,
		&createdAtStr, &lastSeenStr, &revokedAtStr,
	)
	if err != nil {
		return nil, err
	}

	if apnsToken.Valid {
		v := apnsToken.String
		d.APNSToken = &v
	}
	if fcmToken.Valid {
		v := fcmToken.String
		d.FCMToken = &v
	}

	created, err := time.Parse(time.RFC3339, createdAtStr)
	if err != nil {
		return nil, fmt.Errorf("parsing created_at %q: %w", createdAtStr, err)
	}
	d.CreatedAt = created

	if lastSeenStr.Valid {
		t, err := time.Parse(time.RFC3339, lastSeenStr.String)
		if err != nil {
			return nil, fmt.Errorf("parsing last_seen_at %q: %w", lastSeenStr.String, err)
		}
		d.LastSeenAt = &t
	}
	if revokedAtStr.Valid {
		t, err := time.Parse(time.RFC3339, revokedAtStr.String)
		if err != nil {
			return nil, fmt.Errorf("parsing revoked_at %q: %w", revokedAtStr.String, err)
		}
		d.RevokedAt = &t
	}

	return &d, nil
}

// nullableString converts *string to a driver-compatible nil/value.
func nullableString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
