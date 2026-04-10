package pairing_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/pairing"
	"github.com/anadale/huskwoot/internal/storage"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.OpenDB(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func withTx(t *testing.T, db *sql.DB, fn func(tx *sql.Tx) error) error {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func newPairing(deviceName, platform string) *model.PendingPairing {
	expires := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second)
	return &model.PendingPairing{
		ID:         uuid.NewString(),
		DeviceName: deviceName,
		Platform:   platform,
		NonceHash:  "aabbcc112233",
		ExpiresAt:  expires,
	}
}

func TestSQLitePairingStore_CreateAndGet(t *testing.T) {
	db := openTestDB(t)
	store := pairing.NewSQLiteStore(db)
	ctx := context.Background()

	apns := "apns-token"
	p := newPairing("iPhone 17", "ios")
	p.APNSToken = &apns

	err := withTx(t, db, func(tx *sql.Tx) error {
		return store.CreateTx(ctx, tx, p)
	})
	if err != nil {
		t.Fatalf("CreateTx: %v", err)
	}
	if p.CreatedAt.IsZero() {
		t.Fatal("CreatedAt not populated after CreateTx")
	}

	got, err := store.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil for existing record")
	}

	if got.ID != p.ID {
		t.Errorf("ID = %q, want %q", got.ID, p.ID)
	}
	if got.DeviceName != p.DeviceName {
		t.Errorf("DeviceName = %q, want %q", got.DeviceName, p.DeviceName)
	}
	if got.Platform != p.Platform {
		t.Errorf("Platform = %q, want %q", got.Platform, p.Platform)
	}
	if got.NonceHash != p.NonceHash {
		t.Errorf("NonceHash = %q, want %q", got.NonceHash, p.NonceHash)
	}
	if !got.ExpiresAt.Equal(p.ExpiresAt) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, p.ExpiresAt)
	}
	if got.APNSToken == nil || *got.APNSToken != apns {
		t.Errorf("APNSToken = %v, want %q", got.APNSToken, apns)
	}
	if got.FCMToken != nil {
		t.Errorf("FCMToken must be nil, got %v", got.FCMToken)
	}
	if got.ConfirmedAt != nil {
		t.Errorf("ConfirmedAt must be nil, got %v", got.ConfirmedAt)
	}
	if got.IssuedDeviceID != nil {
		t.Errorf("IssuedDeviceID must be nil, got %v", got.IssuedDeviceID)
	}
}

func TestSQLitePairingStore_Get_NotFound(t *testing.T) {
	db := openTestDB(t)
	store := pairing.NewSQLiteStore(db)

	got, err := store.Get(context.Background(), "non-existent-id")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("Get must return nil for non-existent record, got %+v", got)
	}
}

func TestSQLitePairingStore_SetCSRFTx_Updates(t *testing.T) {
	db := openTestDB(t)
	store := pairing.NewSQLiteStore(db)
	ctx := context.Background()

	p := newPairing("iPad", "ios")
	err := withTx(t, db, func(tx *sql.Tx) error {
		return store.CreateTx(ctx, tx, p)
	})
	if err != nil {
		t.Fatalf("CreateTx: %v", err)
	}

	csrfHash := "deadbeefdeadbeef"
	err = withTx(t, db, func(tx *sql.Tx) error {
		return store.SetCSRFTx(ctx, tx, p.ID, csrfHash)
	})
	if err != nil {
		t.Fatalf("SetCSRFTx: %v", err)
	}

	got, err := store.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get after SetCSRFTx: %v", err)
	}
	if got.CSRFHash != csrfHash {
		t.Errorf("CSRFHash = %q, want %q", got.CSRFHash, csrfHash)
	}
}

func TestSQLitePairingStore_MarkConfirmedTx_PopulatesIssuedDeviceID(t *testing.T) {
	db := openTestDB(t)
	store := pairing.NewSQLiteStore(db)
	ctx := context.Background()

	p := newPairing("MacBook", "macos")
	err := withTx(t, db, func(tx *sql.Tx) error {
		return store.CreateTx(ctx, tx, p)
	})
	if err != nil {
		t.Fatalf("CreateTx: %v", err)
	}

	deviceID := uuid.NewString()
	// Must insert a device into the devices table before MarkConfirmedTx
	// due to REFERENCES devices(id). Insert directly via SQL for the test.
	_, err = db.ExecContext(ctx,
		`INSERT INTO devices (id, name, platform, token_hash, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		deviceID, "MacBook", "macos", "dummy-hash", time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("inserting device into DB: %v", err)
	}

	err = withTx(t, db, func(tx *sql.Tx) error {
		return store.MarkConfirmedTx(ctx, tx, p.ID, deviceID)
	})
	if err != nil {
		t.Fatalf("MarkConfirmedTx: %v", err)
	}

	got, err := store.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get after MarkConfirmedTx: %v", err)
	}
	if got.ConfirmedAt == nil {
		t.Fatal("ConfirmedAt must be populated after MarkConfirmedTx")
	}
	if got.IssuedDeviceID == nil || *got.IssuedDeviceID != deviceID {
		t.Errorf("IssuedDeviceID = %v, want %q", got.IssuedDeviceID, deviceID)
	}
}

func TestSQLitePairingStore_DeleteExpired_RemovesOnlyOlder(t *testing.T) {
	db := openTestDB(t)
	store := pairing.NewSQLiteStore(db)
	ctx := context.Background()

	// Expired record: expires_at in the past.
	expired := newPairing("Old Device", "linux")
	expired.ExpiresAt = time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	err := withTx(t, db, func(tx *sql.Tx) error {
		return store.CreateTx(ctx, tx, expired)
	})
	if err != nil {
		t.Fatalf("CreateTx expired: %v", err)
	}

	// Fresh record: expires_at in the future.
	fresh := newPairing("New Device", "ios")
	err = withTx(t, db, func(tx *sql.Tx) error {
		return store.CreateTx(ctx, tx, fresh)
	})
	if err != nil {
		t.Fatalf("CreateTx fresh: %v", err)
	}

	// cutoff = now-1h: only the expired record must be deleted.
	cutoff := time.Now().UTC().Add(-time.Hour)
	deleted, err := store.DeleteExpired(ctx, cutoff)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if deleted != 1 {
		t.Errorf("DeleteExpired returned %d, want 1", deleted)
	}

	// The expired record must be gone.
	got, err := store.Get(ctx, expired.ID)
	if err != nil {
		t.Fatalf("Get expired: %v", err)
	}
	if got != nil {
		t.Fatalf("expired record must be deleted, got %+v", got)
	}

	// The fresh record must remain.
	got, err = store.Get(ctx, fresh.ID)
	if err != nil {
		t.Fatalf("Get fresh: %v", err)
	}
	if got == nil {
		t.Fatal("fresh record must remain after DeleteExpired")
	}
}
