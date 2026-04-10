package relay

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Instance represents an authorised instance in the relay database.
type Instance struct {
	ID           string
	OwnerContact string
	SecretHash   string
	CreatedAt    time.Time
	DisabledAt   *time.Time
}

// InstanceSpec describes the configuration of an instance for synchronisation.
// Secret is plaintext; only the sha256 hash is stored in the database.
type InstanceSpec struct {
	ID           string `toml:"id"`
	OwnerContact string `toml:"owner_contact"`
	Secret       string `toml:"secret"`
}

// Registration stores the push tokens for a device associated with a specific instance.
type Registration struct {
	InstanceID string
	DeviceID   string
	APNSToken  *string
	FCMToken   *string
	Platform   string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	LastUsedAt *time.Time
}

// RegistrationFields holds the fields for creating or updating a registration.
type RegistrationFields struct {
	APNSToken *string
	FCMToken  *string
	Platform  string
}

// Store implements the relay store on top of SQLite (tables: instances, registrations).
type Store struct {
	db *sql.DB
}

// NewStore creates a new Store with the given database.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Ping checks database availability via SELECT 1.
func (s *Store) Ping(ctx context.Context) error {
	var one int
	return s.db.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}

// GetInstance returns an instance by ID. Returns nil, nil if the instance
// is not found or is marked as disabled (disabled_at IS NOT NULL).
func (s *Store) GetInstance(ctx context.Context, id string) (*Instance, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, owner_contact, secret_hash, created_at, disabled_at
		 FROM instances
		 WHERE id = ? AND disabled_at IS NULL`,
		id,
	)

	inst, err := scanInstance(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting instance %q: %w", id, err)
	}
	return inst, nil
}

// SyncInstances atomically synchronises the instance allowlist in a transaction:
// instances absent from list get disabled_at = now; present ones are upserted.
// Called on startup and on SIGHUP.
func (s *Store) SyncInstances(ctx context.Context, list []InstanceSpec) error {
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("syncing instances: beginning transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	activeIDs := make(map[string]struct{}, len(list))
	for _, spec := range list {
		activeIDs[spec.ID] = struct{}{}
	}

	if len(list) == 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE instances SET disabled_at = ? WHERE disabled_at IS NULL`,
			now,
		); err != nil {
			return fmt.Errorf("syncing instances: disabling all: %w", err)
		}
	} else {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM instances WHERE disabled_at IS NULL`)
		if err != nil {
			return fmt.Errorf("syncing instances: reading active: %w", err)
		}
		var toDisable []string
		for rows.Next() {
			var id string
			if scanErr := rows.Scan(&id); scanErr != nil {
				rows.Close()
				return fmt.Errorf("syncing instances: scan id: %w", scanErr)
			}
			if _, ok := activeIDs[id]; !ok {
				toDisable = append(toDisable, id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("syncing instances: iterating: %w", err)
		}

		for _, id := range toDisable {
			if _, err := tx.ExecContext(ctx,
				`UPDATE instances SET disabled_at = ? WHERE id = ?`,
				now, id,
			); err != nil {
				return fmt.Errorf("syncing instances: disabling %q: %w", id, err)
			}
		}
	}

	for _, spec := range list {
		h := sha256.Sum256([]byte(spec.Secret))
		secretHash := fmt.Sprintf("%x", h)
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO instances (id, owner_contact, secret_hash)
			 VALUES (?, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET
			     owner_contact = excluded.owner_contact,
			     secret_hash   = excluded.secret_hash,
			     disabled_at   = NULL`,
			spec.ID, spec.OwnerContact, secretHash,
		); err != nil {
			return fmt.Errorf("syncing instances: upsert %q: %w", spec.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("syncing instances: committing: %w", err)
	}
	return nil
}

// UpsertRegistration creates or updates the push token registration for a device.
func (s *Store) UpsertRegistration(ctx context.Context, instanceID, deviceID string, reg RegistrationFields) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO registrations (instance_id, device_id, apns_token, fcm_token, platform, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(instance_id, device_id) DO UPDATE SET
		     apns_token = excluded.apns_token,
		     fcm_token  = excluded.fcm_token,
		     platform   = excluded.platform,
		     updated_at = excluded.updated_at`,
		instanceID, deviceID, nullableString(reg.APNSToken), nullableString(reg.FCMToken), reg.Platform, now,
	)
	if err != nil {
		return fmt.Errorf("upserting registration (%s/%s): %w", instanceID, deviceID, err)
	}
	return nil
}

// DeleteRegistration removes a device registration. Idempotent.
func (s *Store) DeleteRegistration(ctx context.Context, instanceID, deviceID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM registrations WHERE instance_id = ? AND device_id = ?`,
		instanceID, deviceID,
	)
	if err != nil {
		return fmt.Errorf("deleting registration (%s/%s): %w", instanceID, deviceID, err)
	}
	return nil
}

// GetRegistration returns a device registration. Returns nil, nil if not found.
func (s *Store) GetRegistration(ctx context.Context, instanceID, deviceID string) (*Registration, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT instance_id, device_id, apns_token, fcm_token, platform,
		        created_at, updated_at, last_used_at
		 FROM registrations
		 WHERE instance_id = ? AND device_id = ?`,
		instanceID, deviceID,
	)

	reg, err := scanRegistration(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting registration (%s/%s): %w", instanceID, deviceID, err)
	}
	return reg, nil
}

// MarkUsed updates last_used_at for a device registration.
func (s *Store) MarkUsed(ctx context.Context, instanceID, deviceID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE registrations SET last_used_at = ? WHERE instance_id = ? AND device_id = ?`,
		at.UTC().Format(time.RFC3339), instanceID, deviceID,
	)
	if err != nil {
		return fmt.Errorf("updating last_used_at (%s/%s): %w", instanceID, deviceID, err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanInstance(r rowScanner) (*Instance, error) {
	var (
		inst          Instance
		createdAtStr  string
		disabledAtStr sql.NullString
	)
	err := r.Scan(
		&inst.ID, &inst.OwnerContact, &inst.SecretHash,
		&createdAtStr, &disabledAtStr,
	)
	if err != nil {
		return nil, err
	}

	created, parseErr := parseTimeFlex(createdAtStr)
	if parseErr != nil {
		return nil, fmt.Errorf("parsing created_at %q: %w", createdAtStr, parseErr)
	}
	inst.CreatedAt = created

	if disabledAtStr.Valid {
		t, parseErr := parseTimeFlex(disabledAtStr.String)
		if parseErr != nil {
			return nil, fmt.Errorf("parsing disabled_at %q: %w", disabledAtStr.String, parseErr)
		}
		inst.DisabledAt = &t
	}

	return &inst, nil
}

func scanRegistration(r rowScanner) (*Registration, error) {
	var (
		reg          Registration
		apnsToken    sql.NullString
		fcmToken     sql.NullString
		createdAtStr string
		updatedAtStr string
		lastUsedStr  sql.NullString
	)
	err := r.Scan(
		&reg.InstanceID, &reg.DeviceID,
		&apnsToken, &fcmToken, &reg.Platform,
		&createdAtStr, &updatedAtStr, &lastUsedStr,
	)
	if err != nil {
		return nil, err
	}

	if apnsToken.Valid {
		v := apnsToken.String
		reg.APNSToken = &v
	}
	if fcmToken.Valid {
		v := fcmToken.String
		reg.FCMToken = &v
	}

	created, err := parseTimeFlex(createdAtStr)
	if err != nil {
		return nil, fmt.Errorf("parsing created_at %q: %w", createdAtStr, err)
	}
	reg.CreatedAt = created

	updated, err := parseTimeFlex(updatedAtStr)
	if err != nil {
		return nil, fmt.Errorf("parsing updated_at %q: %w", updatedAtStr, err)
	}
	reg.UpdatedAt = updated

	if lastUsedStr.Valid {
		t, err := parseTimeFlex(lastUsedStr.String)
		if err != nil {
			return nil, fmt.Errorf("parsing last_used_at %q: %w", lastUsedStr.String, err)
		}
		reg.LastUsedAt = &t
	}

	return &reg, nil
}

// parseTimeFlex parses a timestamp in RFC3339Nano, RFC3339, or SQLite CURRENT_TIMESTAMP format.
func parseTimeFlex(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		t, err := time.Parse(layout, s)
		if err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unknown time format: %q", s)
}

func nullableString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
