package relay_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/anadale/huskwoot/internal/relay"
	"github.com/anadale/huskwoot/internal/relay/migrations"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening DB: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if err := migrations.Up(db); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func secretHash(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return fmt.Sprintf("%x", h)
}

func TestRelayStore_SyncInstances_AddsNewAndDisablesMissing(t *testing.T) {
	db := openTestDB(t)
	store := relay.NewStore(db)
	ctx := context.Background()

	// Initial sync: two instances.
	err := store.SyncInstances(ctx, []relay.InstanceSpec{
		{ID: "alpha", OwnerContact: "@alpha", Secret: "secret-alpha"},
		{ID: "beta", OwnerContact: "@beta", Secret: "secret-beta"},
	})
	if err != nil {
		t.Fatalf("SyncInstances (initial): %v", err)
	}

	// Both must be active.
	for _, id := range []string{"alpha", "beta"} {
		inst, err := store.GetInstance(ctx, id)
		if err != nil {
			t.Fatalf("GetInstance(%q): %v", id, err)
		}
		if inst == nil {
			t.Fatalf("GetInstance(%q): want instance, got nil", id)
		}
	}

	// Second sync: alpha only; beta must be disabled.
	err = store.SyncInstances(ctx, []relay.InstanceSpec{
		{ID: "alpha", OwnerContact: "@alpha-new", Secret: "secret-alpha2"},
	})
	if err != nil {
		t.Fatalf("SyncInstances (second): %v", err)
	}

	// alpha is active with the updated owner_contact.
	alpha, err := store.GetInstance(ctx, "alpha")
	if err != nil {
		t.Fatalf("GetInstance(alpha): %v", err)
	}
	if alpha == nil {
		t.Fatal("GetInstance(alpha): want instance, got nil")
	}
	if alpha.OwnerContact != "@alpha-new" {
		t.Errorf("owner_contact = %q, want %q", alpha.OwnerContact, "@alpha-new")
	}

	// secret_hash was updated.
	wantHash := secretHash("secret-alpha2")
	if alpha.SecretHash != wantHash {
		t.Errorf("secret_hash = %q, want %q", alpha.SecretHash, wantHash)
	}

	// beta is disabled — GetInstance must return nil.
	beta, err := store.GetInstance(ctx, "beta")
	if err != nil {
		t.Fatalf("GetInstance(beta): %v", err)
	}
	if beta != nil {
		t.Fatal("GetInstance(beta): want nil (disabled), got instance")
	}
}

func TestRelayStore_GetInstance_ReturnsNilWhenDisabled(t *testing.T) {
	db := openTestDB(t)
	store := relay.NewStore(db)
	ctx := context.Background()

	// Add an instance, then immediately disable it via SyncInstances with an empty list.
	if err := store.SyncInstances(ctx, []relay.InstanceSpec{
		{ID: "gamma", OwnerContact: "@gamma", Secret: "s"},
	}); err != nil {
		t.Fatalf("SyncInstances: %v", err)
	}

	// Instance is active.
	got, err := store.GetInstance(ctx, "gamma")
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if got == nil {
		t.Fatal("GetInstance: want instance, got nil")
	}

	// Disable via empty list.
	if err := store.SyncInstances(ctx, nil); err != nil {
		t.Fatalf("SyncInstances (empty): %v", err)
	}

	// Now GetInstance must return nil.
	got, err = store.GetInstance(ctx, "gamma")
	if err != nil {
		t.Fatalf("GetInstance after disabling: %v", err)
	}
	if got != nil {
		t.Fatalf("GetInstance(gamma): want nil (disabled), got %+v", got)
	}
}

func TestRelayStore_UpsertRegistration_InsertThenUpdate(t *testing.T) {
	db := openTestDB(t)
	store := relay.NewStore(db)
	ctx := context.Background()

	// An active instance is required (FK constraint).
	if err := store.SyncInstances(ctx, []relay.InstanceSpec{
		{ID: "inst-1", OwnerContact: "@user", Secret: "s"},
	}); err != nil {
		t.Fatalf("SyncInstances: %v", err)
	}

	apns1 := "apns-token-v1"
	fcm1 := "fcm-token-v1"

	// First call — INSERT.
	err := store.UpsertRegistration(ctx, "inst-1", "dev-1", relay.RegistrationFields{
		APNSToken: &apns1,
		FCMToken:  &fcm1,
		Platform:  "ios",
	})
	if err != nil {
		t.Fatalf("UpsertRegistration (insert): %v", err)
	}

	reg1, err := store.GetRegistration(ctx, "inst-1", "dev-1")
	if err != nil {
		t.Fatalf("GetRegistration: %v", err)
	}
	if reg1 == nil {
		t.Fatal("GetRegistration: want record, got nil")
	}
	if reg1.APNSToken == nil || *reg1.APNSToken != apns1 {
		t.Errorf("APNSToken = %v, want %q", reg1.APNSToken, apns1)
	}
	updated1 := reg1.UpdatedAt

	// Small delay to ensure updated_at actually changes.
	time.Sleep(10 * time.Millisecond)

	apns2 := "apns-token-v2"

	// Second call — UPDATE.
	err = store.UpsertRegistration(ctx, "inst-1", "dev-1", relay.RegistrationFields{
		APNSToken: &apns2,
		Platform:  "ios",
	})
	if err != nil {
		t.Fatalf("UpsertRegistration (update): %v", err)
	}

	reg2, err := store.GetRegistration(ctx, "inst-1", "dev-1")
	if err != nil {
		t.Fatalf("GetRegistration after update: %v", err)
	}
	if reg2 == nil {
		t.Fatal("GetRegistration: want record, got nil")
	}
	if reg2.APNSToken == nil || *reg2.APNSToken != apns2 {
		t.Errorf("APNSToken = %v, want %q", reg2.APNSToken, apns2)
	}
	// fcm_token must be cleared (nil in the request).
	if reg2.FCMToken != nil {
		t.Errorf("FCMToken = %q, want nil", *reg2.FCMToken)
	}
	if !reg2.UpdatedAt.After(updated1) {
		t.Errorf("updated_at did not change: was %v, now %v", updated1, reg2.UpdatedAt)
	}
}

func TestRelayStore_DeleteRegistration_Idempotent(t *testing.T) {
	db := openTestDB(t)
	store := relay.NewStore(db)
	ctx := context.Background()

	if err := store.SyncInstances(ctx, []relay.InstanceSpec{
		{ID: "inst-2", OwnerContact: "@u", Secret: "s"},
	}); err != nil {
		t.Fatalf("SyncInstances: %v", err)
	}

	tok := "apns-tok"
	if err := store.UpsertRegistration(ctx, "inst-2", "dev-2", relay.RegistrationFields{
		APNSToken: &tok,
		Platform:  "ios",
	}); err != nil {
		t.Fatalf("UpsertRegistration: %v", err)
	}

	// First deletion.
	if err := store.DeleteRegistration(ctx, "inst-2", "dev-2"); err != nil {
		t.Fatalf("DeleteRegistration (1): %v", err)
	}

	reg, err := store.GetRegistration(ctx, "inst-2", "dev-2")
	if err != nil {
		t.Fatalf("GetRegistration after deletion: %v", err)
	}
	if reg != nil {
		t.Fatal("GetRegistration: want nil after deletion")
	}

	// Second deletion — idempotent, no error.
	if err := store.DeleteRegistration(ctx, "inst-2", "dev-2"); err != nil {
		t.Fatalf("DeleteRegistration (2, idempotent): %v", err)
	}
}

func TestRelayStore_GetRegistration_ReturnsNilForMissing(t *testing.T) {
	db := openTestDB(t)
	store := relay.NewStore(db)
	ctx := context.Background()

	reg, err := store.GetRegistration(ctx, "no-inst", "no-dev")
	if err != nil {
		t.Fatalf("GetRegistration: %v", err)
	}
	if reg != nil {
		t.Fatalf("GetRegistration: want nil, got %+v", reg)
	}
}

func TestRelayStore_MarkUsed_UpdatesLastUsedAt(t *testing.T) {
	db := openTestDB(t)
	store := relay.NewStore(db)
	ctx := context.Background()

	if err := store.SyncInstances(ctx, []relay.InstanceSpec{
		{ID: "inst-3", OwnerContact: "@u", Secret: "s"},
	}); err != nil {
		t.Fatalf("SyncInstances: %v", err)
	}

	tok := "tok"
	if err := store.UpsertRegistration(ctx, "inst-3", "dev-3", relay.RegistrationFields{
		APNSToken: &tok,
		Platform:  "ios",
	}); err != nil {
		t.Fatalf("UpsertRegistration: %v", err)
	}

	reg, err := store.GetRegistration(ctx, "inst-3", "dev-3")
	if err != nil {
		t.Fatalf("GetRegistration: %v", err)
	}
	if reg.LastUsedAt != nil {
		t.Fatalf("LastUsedAt must be nil before MarkUsed, got %v", *reg.LastUsedAt)
	}

	usedAt := time.Now().UTC().Truncate(time.Second)
	if err := store.MarkUsed(ctx, "inst-3", "dev-3", usedAt); err != nil {
		t.Fatalf("MarkUsed: %v", err)
	}

	reg2, err := store.GetRegistration(ctx, "inst-3", "dev-3")
	if err != nil {
		t.Fatalf("GetRegistration after MarkUsed: %v", err)
	}
	if reg2.LastUsedAt == nil {
		t.Fatal("LastUsedAt is nil after MarkUsed")
	}
	if !reg2.LastUsedAt.Equal(usedAt) {
		t.Errorf("LastUsedAt = %v, want %v", *reg2.LastUsedAt, usedAt)
	}
}
