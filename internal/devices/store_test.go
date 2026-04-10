package devices_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/anadale/huskwoot/internal/devices"
	"github.com/anadale/huskwoot/internal/model"
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

// withTx opens a transaction, executes fn, and commits the result.
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

func mustCreate(t *testing.T, db *sql.DB, store *devices.SQLiteDeviceStore, d *model.Device) {
	t.Helper()
	err := withTx(t, db, func(tx *sql.Tx) error {
		return store.Create(context.Background(), tx, d)
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
}

func newDevice(name, platform, tokenHash string) *model.Device {
	return &model.Device{
		ID:        uuid.NewString(),
		Name:      name,
		Platform:  platform,
		TokenHash: tokenHash,
	}
}

func TestDeviceStoreCreateAndFind(t *testing.T) {
	db := openTestDB(t)
	store := devices.NewSQLiteDeviceStore(db)
	ctx := context.Background()

	d := newDevice("iPhone 17", "ios", "abc123")
	mustCreate(t, db, store, d)

	if d.CreatedAt.IsZero() {
		t.Fatal("CreatedAt not populated after Create")
	}

	got, err := store.FindByTokenHash(ctx, "abc123")
	if err != nil {
		t.Fatalf("FindByTokenHash: %v", err)
	}
	if got == nil {
		t.Fatal("FindByTokenHash returned nil")
	}
	if got.ID != d.ID || got.Name != d.Name || got.Platform != d.Platform {
		t.Fatalf("FindByTokenHash = %+v, wanted ID=%s Name=%q Platform=%q", got, d.ID, d.Name, d.Platform)
	}
}

func TestDeviceStoreCreateRolledBack(t *testing.T) {
	db := openTestDB(t)
	store := devices.NewSQLiteDeviceStore(db)
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	d := newDevice("iPhone 17", "ios", "rollback-hash")
	if err := store.Create(ctx, tx, d); err != nil {
		_ = tx.Rollback()
		t.Fatalf("Create: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	got, err := store.FindByTokenHash(ctx, "rollback-hash")
	if err != nil {
		t.Fatalf("FindByTokenHash: %v", err)
	}
	if got != nil {
		t.Fatalf("after rollback expected nil, got %+v", got)
	}
}

func TestDeviceStoreCreateNilTxReturnsError(t *testing.T) {
	db := openTestDB(t)
	store := devices.NewSQLiteDeviceStore(db)

	err := store.Create(context.Background(), nil, newDevice("X", "linux", "nil-tx"))
	if err == nil {
		t.Error("Create with nil tx should return error")
	}
}

func TestDeviceStoreFindByTokenHashNotFound(t *testing.T) {
	db := openTestDB(t)
	store := devices.NewSQLiteDeviceStore(db)

	got, err := store.FindByTokenHash(context.Background(), "does-not-exist")
	if err != nil {
		t.Fatalf("FindByTokenHash: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestDeviceStoreRevokeMarksDevice(t *testing.T) {
	db := openTestDB(t)
	store := devices.NewSQLiteDeviceStore(db)
	ctx := context.Background()

	d := newDevice("iPad", "ios", "revoke-hash")
	mustCreate(t, db, store, d)

	if err := store.Revoke(ctx, d.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	// Verify via List — the device must have RevokedAt set.
	all, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("List returned %d devices, expected 1", len(all))
	}
	if all[0].RevokedAt == nil {
		t.Fatal("after Revoke RevokedAt should be populated")
	}
}

func TestDeviceStoreRevokeIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	store := devices.NewSQLiteDeviceStore(db)
	ctx := context.Background()

	d := newDevice("iPad", "ios", "revoke-twice")
	mustCreate(t, db, store, d)

	if err := store.Revoke(ctx, d.ID); err != nil {
		t.Fatalf("first Revoke: %v", err)
	}
	all, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	firstRevokedAt := *all[0].RevokedAt

	// Pause so that the TEXT timestamp would differ if revoke had updated the time.
	time.Sleep(10 * time.Millisecond)

	if err := store.Revoke(ctx, d.ID); err != nil {
		t.Fatalf("second Revoke: %v", err)
	}
	all, err = store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !all[0].RevokedAt.Equal(firstRevokedAt) {
		t.Fatalf("repeated Revoke changed RevokedAt: was %v, became %v", firstRevokedAt, *all[0].RevokedAt)
	}
}

func TestDeviceStoreFindByTokenHashSkipsRevoked(t *testing.T) {
	db := openTestDB(t)
	store := devices.NewSQLiteDeviceStore(db)
	ctx := context.Background()

	d := newDevice("Old iPhone", "ios", "skip-revoked")
	mustCreate(t, db, store, d)

	if err := store.Revoke(ctx, d.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	got, err := store.FindByTokenHash(ctx, "skip-revoked")
	if err != nil {
		t.Fatalf("FindByTokenHash: %v", err)
	}
	if got != nil {
		t.Fatalf("FindByTokenHash should skip revoked, got %+v", got)
	}
}

func TestDeviceStoreReuseTokenAfterRevoke(t *testing.T) {
	db := openTestDB(t)
	store := devices.NewSQLiteDeviceStore(db)
	ctx := context.Background()

	d1 := newDevice("Old", "ios", "reused-hash")
	mustCreate(t, db, store, d1)
	if err := store.Revoke(ctx, d1.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	// After revocation the same token_hash may be reused
	// thanks to the partial unique index on (token_hash) WHERE revoked_at IS NULL.
	d2 := newDevice("New", "ios", "reused-hash")
	mustCreate(t, db, store, d2)

	got, err := store.FindByTokenHash(ctx, "reused-hash")
	if err != nil {
		t.Fatalf("FindByTokenHash: %v", err)
	}
	if got == nil || got.ID != d2.ID {
		t.Fatalf("FindByTokenHash should return new device %s, got %+v", d2.ID, got)
	}
}

func TestDeviceStoreListActiveIDsExcludesRevoked(t *testing.T) {
	db := openTestDB(t)
	store := devices.NewSQLiteDeviceStore(db)
	ctx := context.Background()

	active := newDevice("Active", "ios", "active-hash")
	revoked := newDevice("Revoked", "ios", "revoked-hash")
	mustCreate(t, db, store, active)
	mustCreate(t, db, store, revoked)

	if err := store.Revoke(ctx, revoked.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	ids, err := store.ListActiveIDs(ctx)
	if err != nil {
		t.Fatalf("ListActiveIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != active.ID {
		t.Fatalf("ListActiveIDs = %v, wanted [%s]", ids, active.ID)
	}
}

func TestDeviceStoreListReturnsAllInCreatedOrder(t *testing.T) {
	db := openTestDB(t)
	store := devices.NewSQLiteDeviceStore(db)
	ctx := context.Background()

	first := newDevice("First", "ios", "first")
	mustCreate(t, db, store, first)
	// created_at is stored at second precision (RFC3339) — a pause >= 1s
	// is required to guarantee monotonic order when using ORDER BY created_at.
	time.Sleep(1100 * time.Millisecond)
	second := newDevice("Second", "android", "second")
	mustCreate(t, db, store, second)

	all, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("List returned %d devices, expected 2", len(all))
	}
	if all[0].ID != first.ID || all[1].ID != second.ID {
		t.Fatalf("device order wrong: [%s, %s], expected [%s, %s]",
			all[0].ID, all[1].ID, first.ID, second.ID)
	}
}

func TestDeviceStoreUpdateLastSeen(t *testing.T) {
	db := openTestDB(t)
	store := devices.NewSQLiteDeviceStore(db)
	ctx := context.Background()

	d := newDevice("Laptop", "linux", "last-seen")
	mustCreate(t, db, store, d)

	seen := time.Date(2026, 4, 18, 10, 0, 0, 0, time.UTC)
	if err := store.UpdateLastSeen(ctx, d.ID, seen); err != nil {
		t.Fatalf("UpdateLastSeen: %v", err)
	}

	got, err := store.FindByTokenHash(ctx, "last-seen")
	if err != nil {
		t.Fatalf("FindByTokenHash: %v", err)
	}
	if got == nil || got.LastSeenAt == nil {
		t.Fatalf("LastSeenAt not populated: got=%+v", got)
	}
	if !got.LastSeenAt.Equal(seen) {
		t.Fatalf("LastSeenAt = %v, wanted %v", *got.LastSeenAt, seen)
	}
}

func TestDeviceStoreUpdatePushTokens(t *testing.T) {
	db := openTestDB(t)
	store := devices.NewSQLiteDeviceStore(db)
	ctx := context.Background()

	d := newDevice("Phone", "ios", "push-tokens")
	mustCreate(t, db, store, d)

	apns := "apns-token-value"
	fcm := "fcm-token-value"
	if err := store.UpdatePushTokens(ctx, d.ID, &apns, &fcm); err != nil {
		t.Fatalf("UpdatePushTokens: %v", err)
	}

	got, err := store.FindByTokenHash(ctx, "push-tokens")
	if err != nil {
		t.Fatalf("FindByTokenHash: %v", err)
	}
	if got == nil {
		t.Fatal("FindByTokenHash returned nil")
	}
	if got.APNSToken == nil || *got.APNSToken != apns {
		t.Fatalf("APNSToken = %v, wanted %q", got.APNSToken, apns)
	}
	if got.FCMToken == nil || *got.FCMToken != fcm {
		t.Fatalf("FCMToken = %v, wanted %q", got.FCMToken, fcm)
	}

	// Clear tokens by passing nil.
	if err := store.UpdatePushTokens(ctx, d.ID, nil, nil); err != nil {
		t.Fatalf("UpdatePushTokens(nil): %v", err)
	}
	got, err = store.FindByTokenHash(ctx, "push-tokens")
	if err != nil {
		t.Fatalf("FindByTokenHash: %v", err)
	}
	if got.APNSToken != nil || got.FCMToken != nil {
		t.Fatalf("tokens not cleared: APNS=%v FCM=%v", got.APNSToken, got.FCMToken)
	}
}

func TestDeviceStoreListActiveIDsEmpty(t *testing.T) {
	db := openTestDB(t)
	store := devices.NewSQLiteDeviceStore(db)

	ids, err := store.ListActiveIDs(context.Background())
	if err != nil {
		t.Fatalf("ListActiveIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("ListActiveIDs on empty DB = %v, wanted empty slice", ids)
	}
}

func TestDeviceStore_Get_ReturnsRevokedDevices(t *testing.T) {
	db := openTestDB(t)
	store := devices.NewSQLiteDeviceStore(db)
	ctx := context.Background()

	d := newDevice("Laptop", "linux", "get-revoked")
	mustCreate(t, db, store, d)
	if err := store.Revoke(ctx, d.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	got, err := store.Get(ctx, d.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get should return revoked device, got nil")
	}
	if got.ID != d.ID {
		t.Fatalf("ID = %q, wanted %q", got.ID, d.ID)
	}
	if got.RevokedAt == nil {
		t.Fatal("RevokedAt should be populated for revoked device")
	}
}

func TestDeviceStore_Get_ReturnsNilForMissing(t *testing.T) {
	db := openTestDB(t)
	store := devices.NewSQLiteDeviceStore(db)

	got, err := store.Get(context.Background(), "non-existent-id")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("Get should return nil for non-existent ID, got %+v", got)
	}
}

func TestDeviceStoreListEmpty(t *testing.T) {
	db := openTestDB(t)
	store := devices.NewSQLiteDeviceStore(db)

	all, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("List on empty DB = %v, wanted empty slice", all)
	}
}
