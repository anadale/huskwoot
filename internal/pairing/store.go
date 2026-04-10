// Package pairing implements the store and helper components for the pairing flow.
package pairing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// SQLitePairingStore implements model.PairingStore on top of SQLite.
type SQLitePairingStore struct {
	db *sql.DB
}

// NewSQLiteStore creates a new SQLitePairingStore with the given database.
func NewSQLiteStore(db *sql.DB) *SQLitePairingStore {
	return &SQLitePairingStore{db: db}
}

// CreateTx creates a new pairing request within the given transaction.
// Fills CreatedAt; all other fields must be set by the caller.
func (s *SQLitePairingStore) CreateTx(ctx context.Context, tx *sql.Tx, p *model.PendingPairing) error {
	if tx == nil {
		return fmt.Errorf("creating pairing request %q: tx cannot be nil", p.ID)
	}
	p.CreatedAt = time.Now().UTC()
	_, err := tx.ExecContext(ctx,
		`INSERT INTO pairing_requests
			(id, device_name, platform, apns_token, fcm_token,
			 client_nonce_hash, csrf_token_hash, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.DeviceName, p.Platform,
		nullableString(p.APNSToken), nullableString(p.FCMToken),
		p.NonceHash, p.CSRFHash,
		p.CreatedAt.Format(time.RFC3339),
		p.ExpiresAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("creating pairing request %q: %w", p.ID, err)
	}
	return nil
}

// Get returns a pairing request by UUID. Returns nil, nil if not found.
func (s *SQLitePairingStore) Get(ctx context.Context, id string) (*model.PendingPairing, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, device_name, platform, apns_token, fcm_token,
		        client_nonce_hash, csrf_token_hash,
		        created_at, expires_at, confirmed_at, issued_device_id
		 FROM pairing_requests
		 WHERE id = ?`,
		id,
	)
	p, err := scanPairing(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting pairing request %q: %w", id, err)
	}
	return p, nil
}

// SetCSRFTx saves the SHA256 hash of the CSRF token within the given transaction.
// Returns sql.ErrNoRows if the record is not found.
func (s *SQLitePairingStore) SetCSRFTx(ctx context.Context, tx *sql.Tx, id, csrfHash string) error {
	if tx == nil {
		return fmt.Errorf("setting csrf_token_hash for %q: tx cannot be nil", id)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE pairing_requests SET csrf_token_hash = ? WHERE id = ?`,
		csrfHash, id,
	)
	if err != nil {
		return fmt.Errorf("setting csrf_token_hash for %q: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("setting csrf_token_hash for %q rowsAffected: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("установка csrf_token_hash для %q: %w", id, sql.ErrNoRows)
	}
	return nil
}

// MarkConfirmedTx marks a request as confirmed and records the deviceID.
// Returns sql.ErrNoRows if the request is not found or already confirmed.
func (s *SQLitePairingStore) MarkConfirmedTx(ctx context.Context, tx *sql.Tx, id, deviceID string) error {
	if tx == nil {
		return fmt.Errorf("confirming pairing request %q: tx cannot be nil", id)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := tx.ExecContext(ctx,
		`UPDATE pairing_requests SET confirmed_at = ?, issued_device_id = ? WHERE id = ? AND confirmed_at IS NULL`,
		now, deviceID, id,
	)
	if err != nil {
		return fmt.Errorf("confirming pairing request %q: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirming pairing request %q rowsAffected: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("подтверждение pairing-запроса %q: %w", id, sql.ErrNoRows)
	}
	return nil
}

// DeleteExpired deletes records whose expires_at < cutoff.
// Returns the number of deleted rows.
func (s *SQLitePairingStore) DeleteExpired(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM pairing_requests WHERE expires_at < ?`,
		cutoff.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, fmt.Errorf("deleting expired pairing requests: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("getting count of deleted pairing requests: %w", err)
	}
	return n, nil
}

// rowScanner abstracts sql.Row and sql.Rows for shared record scanning.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanPairing(r rowScanner) (*model.PendingPairing, error) {
	var (
		p            model.PendingPairing
		apnsToken    sql.NullString
		fcmToken     sql.NullString
		createdAtStr string
		expiresAtStr string
		confirmedStr sql.NullString
		issuedDevID  sql.NullString
	)
	err := r.Scan(
		&p.ID, &p.DeviceName, &p.Platform,
		&apnsToken, &fcmToken,
		&p.NonceHash, &p.CSRFHash,
		&createdAtStr, &expiresAtStr, &confirmedStr, &issuedDevID,
	)
	if err != nil {
		return nil, err
	}

	if apnsToken.Valid {
		v := apnsToken.String
		p.APNSToken = &v
	}
	if fcmToken.Valid {
		v := fcmToken.String
		p.FCMToken = &v
	}

	created, err := time.Parse(time.RFC3339, createdAtStr)
	if err != nil {
		return nil, fmt.Errorf("parsing created_at %q: %w", createdAtStr, err)
	}
	p.CreatedAt = created

	expires, err := time.Parse(time.RFC3339, expiresAtStr)
	if err != nil {
		return nil, fmt.Errorf("parsing expires_at %q: %w", expiresAtStr, err)
	}
	p.ExpiresAt = expires

	if confirmedStr.Valid {
		t, err := time.Parse(time.RFC3339, confirmedStr.String)
		if err != nil {
			return nil, fmt.Errorf("parsing confirmed_at %q: %w", confirmedStr.String, err)
		}
		p.ConfirmedAt = &t
	}
	if issuedDevID.Valid {
		v := issuedDevID.String
		p.IssuedDeviceID = &v
	}

	return &p, nil
}

func nullableString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
