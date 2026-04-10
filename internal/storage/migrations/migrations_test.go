package migrations_test

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/anadale/huskwoot/internal/storage/migrations"
	"github.com/pressly/goose/v3"
)

func TestUpAppliesBaseline(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening DB: %v", err)
	}
	defer db.Close()

	if err := migrations.Up(db); err != nil {
		t.Fatalf("Up: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('projects','tasks','cursors','channel_projects','messages')`).Scan(&n); err != nil {
		t.Fatalf("checking tables: %v", err)
	}
	if n != 5 {
		t.Fatalf("want 5 tables, got %d", n)
	}
}

func TestUpAppliesAll(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening DB: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}

	if err := migrations.Up(db); err != nil {
		t.Fatalf("Up: %v", err)
	}

	wantTables := []string{
		"projects", "tasks", "cursors", "channel_projects", "messages",
		"devices", "events", "push_queue", "pairing_requests",
	}
	for _, name := range wantTables {
		var got string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&got)
		if err != nil {
			t.Fatalf("table %s not found: %v", name, err)
		}
	}

	var idxName string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_devices_token_hash_active'`).Scan(&idxName); err != nil {
		t.Fatalf("index idx_devices_token_hash_active not found: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO devices(id, name, platform, token_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
		"dev-1", "iPhone", "ios", "hash-a", now); err != nil {
		t.Fatalf("inserting first device: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO devices(id, name, platform, token_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
		"dev-2", "iPad", "ios", "hash-a", now); err == nil {
		t.Fatalf("expected uniqueness error for token_hash of active devices")
	}

	if _, err := db.Exec(`INSERT INTO events(kind, entity_id, payload, created_at) VALUES (?, ?, ?, ?)`,
		"task_created", "task-1", `{"id":"task-1"}`, now); err != nil {
		t.Fatalf("inserting event: %v", err)
	}
	var seq int64
	if err := db.QueryRow(`SELECT seq FROM events WHERE entity_id = 'task-1'`).Scan(&seq); err != nil {
		t.Fatalf("reading seq: %v", err)
	}
	if seq <= 0 {
		t.Fatalf("seq must be > 0, got %d", seq)
	}

	if _, err := db.Exec(`INSERT INTO push_queue(device_id, event_seq, created_at, next_attempt_at) VALUES (?, ?, ?, ?)`,
		"dev-1", seq, now, now); err != nil {
		t.Fatalf("inserting push_queue: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO push_queue(device_id, event_seq, created_at, next_attempt_at) VALUES (?, ?, ?, ?)`,
		"dev-unknown", seq, now, now); err == nil {
		t.Fatalf("expected FK error push_queue.device_id → devices.id")
	}

	// After migration 006 deleting an event cascades to pending push jobs
	// (otherwise the FK blocks the events retention cleanup).
	if _, err := db.Exec(`DELETE FROM events WHERE seq = ?`, seq); err != nil {
		t.Fatalf("deleting event should succeed (ON DELETE CASCADE): %v", err)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM push_queue WHERE event_seq = ?`, seq).Scan(&remaining); err != nil {
		t.Fatalf("count push_queue: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("push_queue should be cascade-cleaned, rows remaining: %d", remaining)
	}
}

func TestUpAppliesPairingRequests(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening DB: %v", err)
	}
	defer db.Close()

	if err := migrations.Up(db); err != nil {
		t.Fatalf("Up: %v", err)
	}

	wantCols := map[string]bool{
		"id":                true,
		"device_name":       true,
		"platform":          true,
		"apns_token":        true,
		"fcm_token":         true,
		"client_nonce_hash": true,
		"csrf_token_hash":   true,
		"created_at":        true,
		"expires_at":        true,
		"confirmed_at":      true,
		"issued_device_id":  true,
	}

	rows, err := db.Query(`PRAGMA table_info(pairing_requests)`)
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
			t.Errorf("column %q missing from pairing_requests", col)
		}
	}

	var idxName string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_pairing_requests_expires'`).Scan(&idxName); err != nil {
		t.Fatalf("index idx_pairing_requests_expires not found: %v", err)
	}
}

func TestUpIdempotent(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening DB: %v", err)
	}
	defer db.Close()

	if err := migrations.Up(db); err != nil {
		t.Fatalf("first Up: %v", err)
	}
	if err := migrations.Up(db); err != nil {
		t.Fatalf("repeated Up should be idempotent: %v", err)
	}
}

func TestUUIDMigrationConvertsExistingRows(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	_ = goose.SetDialect("sqlite3")
	if err := goose.UpTo(db, ".", 1); err != nil {
		t.Fatalf("baseline: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO projects(name, description, created_at) VALUES (?, ?, ?)`, "Inbox", "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects(name, description, created_at) VALUES (?, ?, ?)`, "Проект НА Старт", "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tasks(project_id, summary, created_at, updated_at) VALUES (1, 't1', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tasks(project_id, summary, created_at, updated_at) VALUES (1, 't2', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tasks(project_id, summary, created_at, updated_at) VALUES (2, 't3', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO channel_projects(channel_id, project_id) VALUES ('chat:42', 2)`); err != nil {
		t.Fatal(err)
	}

	if err := goose.UpTo(db, ".", 2); err != nil {
		t.Fatalf("002: %v", err)
	}

	rows, err := db.Query(`SELECT id, slug, task_counter FROM projects ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	type pr struct {
		id, slug string
		cnt      int
	}
	var got []pr
	for rows.Next() {
		var p pr
		if err := rows.Scan(&p.id, &p.slug, &p.cnt); err != nil {
			t.Fatal(err)
		}
		if len(p.id) != 36 {
			t.Fatalf("id does not look like UUID: %q", p.id)
		}
		got = append(got, p)
	}
	rows.Close()
	if len(got) != 2 {
		t.Fatalf("want 2 projects, got %d", len(got))
	}

	var inbox, navstart pr
	for _, p := range got {
		if p.slug == "inbox" {
			inbox = p
		}
		if p.slug == "proekt-na-start" {
			navstart = p
		}
	}
	if inbox.id == "" {
		t.Fatalf("inbox project not found, got: %+v", got)
	}
	if navstart.id == "" {
		t.Fatalf("proekt-na-start project not found, got: %+v", got)
	}
	if inbox.cnt != 2 {
		t.Fatalf("inbox.task_counter=%d, want 2", inbox.cnt)
	}
	if navstart.cnt != 1 {
		t.Fatalf("proekt-na-start.task_counter=%d, want 1", navstart.cnt)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE project_id = ? AND number IN (1, 2)`, inbox.id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("tasks in inbox: %d, want 2", n)
	}

	var pid string
	if err := db.QueryRow(`SELECT project_id FROM channel_projects WHERE channel_id = 'chat:42'`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if pid != navstart.id {
		t.Fatalf("channel_projects.project_id=%q, want %q", pid, navstart.id)
	}

	if _, err := db.Exec(`INSERT INTO tasks(id, project_id, number, summary, created_at, updated_at) VALUES ('dup', ?, 1, 's', ?, ?)`, inbox.id, now, now); err == nil {
		t.Fatalf("expected uniqueness error (project_id, number)")
	}
}
