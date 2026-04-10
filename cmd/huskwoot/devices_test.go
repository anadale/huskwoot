package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	devicesstore "github.com/anadale/huskwoot/internal/devices"
	"github.com/anadale/huskwoot/internal/push"
	"github.com/anadale/huskwoot/internal/pushproto"
	"github.com/anadale/huskwoot/internal/storage"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.OpenDB(filepath.Join(dir, "huskwoot.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestDevicesCreateWritesBearerAndInsertsRow(t *testing.T) {
	db := openTestDB(t)
	store := devicesstore.NewSQLiteDeviceStore(db)

	var out bytes.Buffer
	if err := runDevicesCreate(context.Background(), &out, db, store, "iPhone 17", "ios"); err != nil {
		t.Fatalf("runDevicesCreate: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "device_id:") {
		t.Fatalf("expected device_id in output, got: %q", output)
	}
	if !strings.Contains(output, "bearer:") {
		t.Fatalf("expected bearer in output, got: %q", output)
	}

	bearer := extractBearer(t, output)
	if len(bearer) < 32 {
		t.Fatalf("bearer is suspiciously short: %q", bearer)
	}
	deviceID := extractDeviceID(t, output)

	found, err := store.FindByTokenHash(context.Background(), sha256Hex(bearer))
	if err != nil {
		t.Fatalf("FindByTokenHash: %v", err)
	}
	if found == nil {
		t.Fatalf("device not found by bearer hash")
	}
	if found.ID != deviceID {
		t.Fatalf("device_id from stdout %q != id from DB %q", deviceID, found.ID)
	}
	if found.Name != "iPhone 17" {
		t.Fatalf("name mismatch: %q", found.Name)
	}
	if found.Platform != "ios" {
		t.Fatalf("platform mismatch: %q", found.Platform)
	}
	if found.TokenHash == bearer {
		t.Fatalf("DB stores the raw bearer instead of its hash")
	}
}

func TestDevicesCreateRejectsInvalidPlatform(t *testing.T) {
	if err := validatePlatform("toaster"); err == nil {
		t.Fatalf("expected error for invalid platform")
	}
	for _, p := range allowedPlatforms {
		if err := validatePlatform(p); err != nil {
			t.Fatalf("platform %q should be accepted: %v", p, err)
		}
	}
}

func TestDevicesListShowsRegisteredWithStatus(t *testing.T) {
	db := openTestDB(t)
	store := devicesstore.NewSQLiteDeviceStore(db)
	ctx := context.Background()

	var created bytes.Buffer
	if err := runDevicesCreate(ctx, &created, db, store, "MacBook", "macos"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := runDevicesCreate(ctx, &created, db, store, "Pixel", "android"); err != nil {
		t.Fatalf("create: %v", err)
	}

	var listed bytes.Buffer
	if err := runDevicesList(ctx, &listed, store); err != nil {
		t.Fatalf("list: %v", err)
	}

	output := listed.String()
	if !strings.Contains(output, "MacBook") {
		t.Fatalf("list does not contain MacBook: %q", output)
	}
	if !strings.Contains(output, "Pixel") {
		t.Fatalf("list does not contain Pixel: %q", output)
	}
	if !strings.Contains(output, "active") {
		t.Fatalf("expected status active in output: %q", output)
	}
}

func TestDevicesListEmpty(t *testing.T) {
	db := openTestDB(t)
	store := devicesstore.NewSQLiteDeviceStore(db)

	var out bytes.Buffer
	if err := runDevicesList(context.Background(), &out, store); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "no devices registered") {
		t.Fatalf("expected placeholder for empty list: %q", out.String())
	}
}

func TestDevicesRevokeMarksRow(t *testing.T) {
	db := openTestDB(t)
	store := devicesstore.NewSQLiteDeviceStore(db)
	ctx := context.Background()

	var created bytes.Buffer
	if err := runDevicesCreate(ctx, &created, db, store, "Windows PC", "windows"); err != nil {
		t.Fatalf("create: %v", err)
	}
	deviceID := extractDeviceID(t, created.String())

	var out bytes.Buffer
	if err := runDevicesRevoke(ctx, &out, store, push.NilRelayClient{}, deviceID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if !strings.Contains(out.String(), "revoked") {
		t.Fatalf("expected revocation message: %q", out.String())
	}

	devs, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(devs) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devs))
	}
	if devs[0].RevokedAt == nil {
		t.Fatalf("revoked_at should be set")
	}

	// list must mark the device as revoked.
	var listed bytes.Buffer
	if err := runDevicesList(ctx, &listed, store); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(listed.String(), "revoked") {
		t.Fatalf("list should mark revoked devices: %q", listed.String())
	}
}

func TestDevicesRevokeEmptyIDFails(t *testing.T) {
	db := openTestDB(t)
	store := devicesstore.NewSQLiteDeviceStore(db)

	var out bytes.Buffer
	if err := runDevicesRevoke(context.Background(), &out, store, push.NilRelayClient{}, "   "); err == nil {
		t.Fatalf("expected error for empty device-id")
	}
}

// mockRelayForCLI is a mock of push.RelayClient for CLI tests.
type mockRelayForCLI struct {
	deleteCalls []string
	deleteErr   error
}

func (m *mockRelayForCLI) Push(_ context.Context, _ pushproto.PushRequest) (pushproto.PushResponse, error) {
	return pushproto.PushResponse{}, nil
}

func (m *mockRelayForCLI) UpsertRegistration(_ context.Context, _ string, _ pushproto.RegistrationRequest) error {
	return nil
}

func (m *mockRelayForCLI) DeleteRegistration(_ context.Context, deviceID string) error {
	m.deleteCalls = append(m.deleteCalls, deviceID)
	return m.deleteErr
}

func TestDevicesCLI_Revoke_CallsRelayDelete(t *testing.T) {
	db := openTestDB(t)
	store := devicesstore.NewSQLiteDeviceStore(db)
	ctx := context.Background()

	var created bytes.Buffer
	if err := runDevicesCreate(ctx, &created, db, store, "Test Device", "ios"); err != nil {
		t.Fatalf("create: %v", err)
	}
	deviceID := extractDeviceID(t, created.String())

	relay := &mockRelayForCLI{}
	var out bytes.Buffer
	if err := runDevicesRevoke(ctx, &out, store, relay, deviceID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if len(relay.deleteCalls) != 1 {
		t.Fatalf("expected 1 DeleteRegistration call, got %d", len(relay.deleteCalls))
	}
	if relay.deleteCalls[0] != deviceID {
		t.Errorf("deleteID = %q, want %q", relay.deleteCalls[0], deviceID)
	}
}

func TestDevicesCLI_Revoke_RelayError_DoesNotFailRevoke(t *testing.T) {
	db := openTestDB(t)
	store := devicesstore.NewSQLiteDeviceStore(db)
	ctx := context.Background()

	var created bytes.Buffer
	if err := runDevicesCreate(ctx, &created, db, store, "Device Relay Fail", "android"); err != nil {
		t.Fatalf("create: %v", err)
	}
	deviceID := extractDeviceID(t, created.String())

	relay := &mockRelayForCLI{deleteErr: errors.New("relay недоступен")}
	var out bytes.Buffer
	if err := runDevicesRevoke(ctx, &out, store, relay, deviceID); err != nil {
		t.Fatalf("relay error should not cause revoke to fail: %v", err)
	}
	if !strings.Contains(out.String(), "revoked") {
		t.Fatalf("expected revocation message: %q", out.String())
	}
}

func TestSHA256HexIsDeterministic(t *testing.T) {
	a := sha256Hex("hello")
	b := sha256Hex("hello")
	if a != b {
		t.Fatalf("sha256Hex should be deterministic")
	}
	if len(a) != 64 {
		t.Fatalf("hex SHA256 should be 64 chars long, got %d", len(a))
	}
}

func TestGenerateBearerTokenUnique(t *testing.T) {
	a, err := generateBearerToken()
	if err != nil {
		t.Fatalf("generateBearerToken: %v", err)
	}
	b, err := generateBearerToken()
	if err != nil {
		t.Fatalf("generateBearerToken: %v", err)
	}
	if a == b {
		t.Fatalf("two calls should return different tokens")
	}
}

// extractBearer extracts the bearer field value from the runDevicesCreate output.
func extractBearer(t *testing.T, output string) string {
	t.Helper()
	return extractField(t, output, "bearer:")
}

func extractDeviceID(t *testing.T, output string) string {
	t.Helper()
	return extractField(t, output, "device_id:")
}

func extractField(t *testing.T, output, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	t.Fatalf("output does not contain line %q: %q", prefix, output)
	return ""
}
