package migrations_test

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/anadale/huskwoot/internal/relay/migrations"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening DB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestRelayMigrations_UpCreatesTables(t *testing.T) {
	db := openDB(t)

	if err := migrations.Up(db); err != nil {
		t.Fatalf("Up: %v", err)
	}

	wantTables := []string{"instances", "registrations"}
	for _, name := range wantTables {
		var got string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&got)
		if err != nil {
			t.Fatalf("table %q not found: %v", name, err)
		}
	}
}

func TestRelayMigrations_UpCreatesIndex(t *testing.T) {
	db := openDB(t)

	if err := migrations.Up(db); err != nil {
		t.Fatalf("Up: %v", err)
	}

	var idxName string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_registrations_last_used'`).Scan(&idxName)
	if err != nil {
		t.Fatalf("index idx_registrations_last_used not found: %v", err)
	}
}

func TestRelayMigrations_InstancesColumns(t *testing.T) {
	db := openDB(t)

	if err := migrations.Up(db); err != nil {
		t.Fatalf("Up: %v", err)
	}

	wantCols := map[string]bool{
		"id":            true,
		"owner_contact": true,
		"secret_hash":   true,
		"created_at":    true,
		"disabled_at":   true,
	}

	rows, err := db.Query(`PRAGMA table_info(instances)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()

	got := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dfltValue, pk interface{}
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[name] = true
	}

	for col := range wantCols {
		if !got[col] {
			t.Errorf("column %q missing in instances", col)
		}
	}
}

func TestRelayMigrations_RegistrationsColumns(t *testing.T) {
	db := openDB(t)

	if err := migrations.Up(db); err != nil {
		t.Fatalf("Up: %v", err)
	}

	wantCols := map[string]bool{
		"instance_id":  true,
		"device_id":    true,
		"apns_token":   true,
		"fcm_token":    true,
		"platform":     true,
		"created_at":   true,
		"updated_at":   true,
		"last_used_at": true,
	}

	rows, err := db.Query(`PRAGMA table_info(registrations)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()

	got := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dfltValue, pk interface{}
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[name] = true
	}

	for col := range wantCols {
		if !got[col] {
			t.Errorf("column %q missing in registrations", col)
		}
	}
}

func TestRelayMigrations_ForeignKeyCascade(t *testing.T) {
	db := openDB(t)

	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}

	if err := migrations.Up(db); err != nil {
		t.Fatalf("Up: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO instances(id, owner_contact, secret_hash) VALUES ('inst-1', '@user', 'hash1')`); err != nil {
		t.Fatalf("inserting instance: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO registrations(instance_id, device_id, platform) VALUES ('inst-1', 'dev-1', 'ios')`); err != nil {
		t.Fatalf("inserting registration: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM instances WHERE id = 'inst-1'`); err != nil {
		t.Fatalf("deleting instance must succeed with ON DELETE CASCADE: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM registrations WHERE instance_id = 'inst-1'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("registrations must be cascade-deleted, remaining: %d", count)
	}
}

func TestRelayMigrations_Idempotent(t *testing.T) {
	db := openDB(t)

	if err := migrations.Up(db); err != nil {
		t.Fatalf("first Up: %v", err)
	}
	if err := migrations.Up(db); err != nil {
		t.Fatalf("repeated Up must be idempotent: %v", err)
	}
}
