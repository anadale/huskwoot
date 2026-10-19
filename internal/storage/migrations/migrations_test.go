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
		"devices", "events", "push_queue", "pairing_requests", "project_aliases",
		"forward_buffer",
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

func TestMigrationsCreatesProjectAliasesTable(t *testing.T) {
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

	wantCols := map[string]bool{
		"project_id": true,
		"alias":      true,
		"created_at": true,
	}

	rows, err := db.Query(`PRAGMA table_info(project_aliases)`)
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
			t.Errorf("column %q missing from project_aliases", col)
		}
	}

	var idxName string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_project_aliases_project'`).Scan(&idxName); err != nil {
		t.Fatalf("index idx_project_aliases_project not found: %v", err)
	}

	fkRows, err := db.Query(`PRAGMA foreign_key_list(project_aliases)`)
	if err != nil {
		t.Fatalf("PRAGMA foreign_key_list: %v", err)
	}
	defer fkRows.Close()

	var foundFK bool
	for fkRows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := fkRows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatalf("scan fk: %v", err)
		}
		if table == "projects" && from == "project_id" && onDelete == "CASCADE" {
			foundFK = true
		}
	}
	if !foundFK {
		t.Fatal("project_aliases must have FK project_id → projects ON DELETE CASCADE")
	}
}

func TestProjectAliasesCascadeDelete(t *testing.T) {
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

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO projects(id, name, slug, description, task_counter, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"proj-1", "Test Project", "test-project", "", 0, now); err != nil {
		t.Fatalf("inserting project: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO project_aliases(project_id, alias, created_at) VALUES (?, ?, ?)`,
		"proj-1", "testAlias", now); err != nil {
		t.Fatalf("inserting alias: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM project_aliases WHERE project_id = 'proj-1'`).Scan(&count); err != nil {
		t.Fatalf("count aliases: %v", err)
	}
	if count != 1 {
		t.Fatalf("want 1 alias, got %d", count)
	}

	if _, err := db.Exec(`DELETE FROM projects WHERE id = 'proj-1'`); err != nil {
		t.Fatalf("deleting project: %v", err)
	}

	if err := db.QueryRow(`SELECT COUNT(*) FROM project_aliases WHERE project_id = 'proj-1'`).Scan(&count); err != nil {
		t.Fatalf("count aliases after delete: %v", err)
	}
	if count != 0 {
		t.Fatalf("aliases should be cascade-deleted, got %d remaining", count)
	}
}

func TestMigrationsCreatesForwardBufferTable(t *testing.T) {
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
		"chat_id":           true,
		"message_id":        true,
		"forward_from_id":   true,
		"forward_from_name": true,
		"is_self_authored":  true,
		"original_date":     true,
		"text":              true,
		"received_at":       true,
	}

	rows, err := db.Query(`PRAGMA table_info(forward_buffer)`)
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
			t.Errorf("column %q missing from forward_buffer", col)
		}
	}

	var idxName string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_forward_buffer_chat_received'`).Scan(&idxName); err != nil {
		t.Fatalf("index idx_forward_buffer_chat_received not found: %v", err)
	}

	// Verify UNIQUE constraint on (chat_id, message_id)
	now := time.Now().Unix()
	if _, err := db.Exec(`INSERT INTO forward_buffer(chat_id, message_id, is_self_authored, original_date, received_at) VALUES (?, ?, ?, ?, ?)`,
		"chat-1", "msg-1", 1, now, now); err != nil {
		t.Fatalf("inserting forward entry: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO forward_buffer(chat_id, message_id, is_self_authored, original_date, received_at) VALUES (?, ?, ?, ?, ?)`,
		"chat-1", "msg-1", 0, now, now); err == nil {
		t.Fatal("expected UNIQUE constraint violation on (chat_id, message_id)")
	}
}

// TestMigration012UpPreservesTelegramData verifies that existing telegram rows
// are correctly migrated: source_kind='telegram', payload=file_id, chat_id/message_id
// preserved, device_id/client_job_id/project_id are NULL.
func TestMigration012UpPreservesTelegramData(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening DB: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	_ = goose.SetDialect("sqlite3")
	if err := goose.UpTo(db, ".", 11); err != nil {
		t.Fatalf("UpTo 11: %v", err)
	}

	now := time.Now().Unix()
	if _, err := db.Exec(`INSERT INTO voice_queue(chat_id, message_id, file_id, mime_type, duration_ms, received_at, next_attempt_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		100, 200, "file-abc", "audio/ogg", 5000, now, now); err != nil {
		t.Fatalf("insert telegram job 1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO voice_queue(chat_id, message_id, file_id, mime_type, duration_ms, received_at, next_attempt_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		100, 201, "file-xyz", "audio/mp4", 10000, now, now); err != nil {
		t.Fatalf("insert telegram job 2: %v", err)
	}

	if err := goose.UpTo(db, ".", 12); err != nil {
		t.Fatalf("UpTo 12: %v", err)
	}

	rows, err := db.Query(`SELECT source_kind, payload, chat_id, message_id, device_id, client_job_id, project_id FROM voice_queue ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	type jobRow struct {
		sourceKind  string
		payload     string
		chatID      int64
		messageID   int64
		deviceID    sql.NullString
		clientJobID sql.NullString
		projectID   sql.NullString
	}
	var got []jobRow
	for rows.Next() {
		var r jobRow
		if err := rows.Scan(&r.sourceKind, &r.payload, &r.chatID, &r.messageID, &r.deviceID, &r.clientJobID, &r.projectID); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	rows.Close()

	if len(got) != 2 {
		t.Fatalf("want 2 rows after migration, got %d", len(got))
	}
	for i, r := range got {
		if r.sourceKind != "telegram" {
			t.Errorf("row %d: source_kind=%q, want 'telegram'", i, r.sourceKind)
		}
		if r.deviceID.Valid {
			t.Errorf("row %d: device_id should be NULL, got %q", i, r.deviceID.String)
		}
		if r.clientJobID.Valid {
			t.Errorf("row %d: client_job_id should be NULL, got %q", i, r.clientJobID.String)
		}
		if r.projectID.Valid {
			t.Errorf("row %d: project_id should be NULL, got %q", i, r.projectID.String)
		}
	}
	if got[0].payload != "file-abc" || got[0].chatID != 100 || got[0].messageID != 200 {
		t.Errorf("row 0: payload=%q chat_id=%d message_id=%d, want file-abc/100/200", got[0].payload, got[0].chatID, got[0].messageID)
	}
	if got[1].payload != "file-xyz" || got[1].chatID != 100 || got[1].messageID != 201 {
		t.Errorf("row 1: payload=%q chat_id=%d message_id=%d, want file-xyz/100/201", got[1].payload, got[1].chatID, got[1].messageID)
	}
}

// TestMigration012ClientJobNullableFields verifies a client job can be inserted
// with NULL chat_id, message_id, project_id and the values are read back correctly.
func TestMigration012ClientJobNullableFields(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening DB: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	_ = goose.SetDialect("sqlite3")
	if err := goose.UpTo(db, ".", 12); err != nil {
		t.Fatalf("UpTo 12: %v", err)
	}

	now := time.Now().Unix()
	if _, err := db.Exec(
		`INSERT INTO voice_queue(source_kind, payload, mime_type, duration_ms, received_at, next_attempt_at, device_id, client_job_id, project_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		"client", "2026/05/abc.ogg", "audio/ogg", 5000, now, now, "dev-1", "job-uuid-1",
	); err != nil {
		t.Fatalf("insert client job: %v", err)
	}

	var sourceKind, payload, deviceID, clientJobID string
	var projectID sql.NullString
	var chatID, messageID sql.NullInt64
	err = db.QueryRow(`SELECT source_kind, payload, device_id, client_job_id, project_id, chat_id, message_id FROM voice_queue WHERE source_kind='client'`).
		Scan(&sourceKind, &payload, &deviceID, &clientJobID, &projectID, &chatID, &messageID)
	if err != nil {
		t.Fatalf("scan client job: %v", err)
	}
	if sourceKind != "client" {
		t.Errorf("source_kind=%q, want 'client'", sourceKind)
	}
	if payload != "2026/05/abc.ogg" {
		t.Errorf("payload=%q, want '2026/05/abc.ogg'", payload)
	}
	if deviceID != "dev-1" {
		t.Errorf("device_id=%q, want 'dev-1'", deviceID)
	}
	if clientJobID != "job-uuid-1" {
		t.Errorf("client_job_id=%q, want 'job-uuid-1'", clientJobID)
	}
	if projectID.Valid {
		t.Errorf("project_id should be NULL, got %q", projectID.String)
	}
	if chatID.Valid {
		t.Errorf("chat_id should be NULL for client job, got %d", chatID.Int64)
	}
	if messageID.Valid {
		t.Errorf("message_id should be NULL for client job, got %d", messageID.Int64)
	}
}

// TestMigration012ClientJobProjectID verifies that project_id is stored and read back correctly.
func TestMigration012ClientJobProjectID(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening DB: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	_ = goose.SetDialect("sqlite3")
	if err := goose.UpTo(db, ".", 12); err != nil {
		t.Fatalf("UpTo 12: %v", err)
	}

	projectUUID := "550e8400-e29b-41d4-a716-446655440000"
	now := time.Now().Unix()
	if _, err := db.Exec(
		`INSERT INTO voice_queue(source_kind, payload, mime_type, duration_ms, received_at, next_attempt_at, device_id, client_job_id, project_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"client", "2026/05/xyz.ogg", "audio/ogg", 3000, now, now, "dev-2", "job-uuid-2", projectUUID,
	); err != nil {
		t.Fatalf("insert client job with project_id: %v", err)
	}

	var gotProjectID sql.NullString
	if err := db.QueryRow(`SELECT project_id FROM voice_queue WHERE client_job_id='job-uuid-2'`).Scan(&gotProjectID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !gotProjectID.Valid {
		t.Fatal("project_id should not be NULL")
	}
	if gotProjectID.String != projectUUID {
		t.Errorf("project_id=%q, want %q", gotProjectID.String, projectUUID)
	}
}

// TestMigration012TelegramDedupInsertOrIgnore verifies the partial unique index on
// (chat_id, message_id) WHERE source_kind='telegram': a duplicate INSERT OR IGNORE is a noop.
func TestMigration012TelegramDedupInsertOrIgnore(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening DB: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	_ = goose.SetDialect("sqlite3")
	if err := goose.UpTo(db, ".", 12); err != nil {
		t.Fatalf("UpTo 12: %v", err)
	}

	now := time.Now().Unix()
	if _, err := db.Exec(
		`INSERT INTO voice_queue(source_kind, payload, mime_type, duration_ms, received_at, next_attempt_at, chat_id, message_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"telegram", "file-id-1", "audio/ogg", 5000, now, now, 111, 222,
	); err != nil {
		t.Fatalf("insert first telegram job: %v", err)
	}

	if _, err := db.Exec(
		`INSERT OR IGNORE INTO voice_queue(source_kind, payload, mime_type, duration_ms, received_at, next_attempt_at, chat_id, message_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"telegram", "file-id-2", "audio/ogg", 5000, now, now, 111, 222,
	); err != nil {
		t.Fatalf("INSERT OR IGNORE should not error: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM voice_queue`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("want 1 row after dedup INSERT OR IGNORE, got %d", count)
	}
}

// TestMigration012ClientJobIDUnique verifies that duplicate client_job_id is rejected.
func TestMigration012ClientJobIDUnique(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening DB: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	_ = goose.SetDialect("sqlite3")
	if err := goose.UpTo(db, ".", 12); err != nil {
		t.Fatalf("UpTo 12: %v", err)
	}

	now := time.Now().Unix()
	if _, err := db.Exec(
		`INSERT INTO voice_queue(source_kind, payload, mime_type, duration_ms, received_at, next_attempt_at, device_id, client_job_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"client", "2026/05/a.ogg", "audio/ogg", 5000, now, now, "dev-1", "unique-job-id",
	); err != nil {
		t.Fatalf("insert first: %v", err)
	}

	_, err = db.Exec(
		`INSERT INTO voice_queue(source_kind, payload, mime_type, duration_ms, received_at, next_attempt_at, device_id, client_job_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"client", "2026/05/b.ogg", "audio/ogg", 5000, now, now, "dev-1", "unique-job-id",
	)
	if err == nil {
		t.Fatal("expected UNIQUE constraint violation for duplicate client_job_id")
	}
}

// TestMigration012DownPreservesTelegramDropsClient verifies the Down migration:
// telegram rows are preserved (payload maps back to file_id), client rows are silently dropped.
func TestMigration012DownPreservesTelegramDropsClient(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening DB: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	_ = goose.SetDialect("sqlite3")
	if err := goose.UpTo(db, ".", 12); err != nil {
		t.Fatalf("UpTo 12: %v", err)
	}

	now := time.Now().Unix()
	if _, err := db.Exec(
		`INSERT INTO voice_queue(source_kind, payload, mime_type, duration_ms, received_at, next_attempt_at, chat_id, message_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"telegram", "file-tg-1", "audio/ogg", 5000, now, now, 123, 456,
	); err != nil {
		t.Fatalf("insert telegram row: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO voice_queue(source_kind, payload, mime_type, duration_ms, received_at, next_attempt_at, device_id, client_job_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"client", "2026/05/abc.ogg", "audio/ogg", 3000, now, now, "dev-1", "job-uuid-down",
	); err != nil {
		t.Fatalf("insert client row: %v", err)
	}

	if err := goose.DownTo(db, ".", 11); err != nil {
		t.Fatalf("DownTo 11: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM voice_queue`).Scan(&count); err != nil {
		t.Fatalf("count after Down: %v", err)
	}
	if count != 1 {
		t.Errorf("want 1 row (only telegram), got %d", count)
	}

	var fileID string
	var chatID, messageID int64
	if err := db.QueryRow(`SELECT file_id, chat_id, message_id FROM voice_queue`).Scan(&fileID, &chatID, &messageID); err != nil {
		t.Fatalf("scan after Down: %v", err)
	}
	if fileID != "file-tg-1" {
		t.Errorf("file_id=%q after Down, want 'file-tg-1' (was payload)", fileID)
	}
	if chatID != 123 {
		t.Errorf("chat_id=%d, want 123", chatID)
	}
	if messageID != 456 {
		t.Errorf("message_id=%d, want 456", messageID)
	}
}

// TestMigration013BackfillsMessageID verifies that existing messages rows get a
// non-empty, unique 32-char hex message_id after migration 013.
func TestMigration013BackfillsMessageID(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	_ = goose.SetDialect("sqlite3")
	if err := goose.UpTo(db, ".", 12); err != nil {
		t.Fatalf("UpTo 12: %v", err)
	}

	now := time.Now().Unix()
	for _, args := range [][3]string{
		{"src-1", "Alice", "hello"},
		{"src-2", "Bot", "world"},
	} {
		if _, err := db.Exec(
			`INSERT INTO messages(source_id, author_name, text, timestamp) VALUES (?, ?, ?, ?)`,
			args[0], args[1], args[2], now,
		); err != nil {
			t.Fatalf("insert message %v: %v", args, err)
		}
	}

	if err := goose.UpTo(db, ".", 13); err != nil {
		t.Fatalf("UpTo 13: %v", err)
	}

	rows, err := db.Query(`SELECT message_id FROM messages ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	seen := map[string]bool{}
	for rows.Next() {
		var msgID string
		if err := rows.Scan(&msgID); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if msgID == "" {
			t.Error("message_id must not be empty after migration 013")
		}
		if len(msgID) != 32 {
			t.Errorf("message_id=%q has length %d, want 32 (hex-encoded randomblob(16))", msgID, len(msgID))
		}
		if seen[msgID] {
			t.Errorf("duplicate message_id found: %q", msgID)
		}
		seen[msgID] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("want 2 rows with unique message_id, got %d", len(seen))
	}
}

// TestMigration013UniqueConstraintRejectsduplicate verifies that inserting a row with
// a duplicate message_id is rejected by the UNIQUE index after migration 013.
func TestMigration013UniqueConstraintRejectsdup(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	_ = goose.SetDialect("sqlite3")
	if err := goose.UpTo(db, ".", 13); err != nil {
		t.Fatalf("UpTo 13: %v", err)
	}

	now := time.Now().Unix()
	if _, err := db.Exec(
		`INSERT INTO messages(source_id, author_name, text, timestamp, message_id) VALUES (?, ?, ?, ?, ?)`,
		"src-1", "Alice", "hello", now, "abcdef1234567890abcdef1234567890",
	); err != nil {
		t.Fatalf("insert first message: %v", err)
	}

	_, err = db.Exec(
		`INSERT INTO messages(source_id, author_name, text, timestamp, message_id) VALUES (?, ?, ?, ?, ?)`,
		"src-2", "Bob", "world", now, "abcdef1234567890abcdef1234567890",
	)
	if err == nil {
		t.Fatal("expected UNIQUE constraint violation for duplicate message_id")
	}
}

// TestMigration013DownDropsColumn verifies that rolling back migration 013 removes the
// message_id column while preserving all other data.
func TestMigration013DownDropsColumn(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	_ = goose.SetDialect("sqlite3")
	if err := goose.UpTo(db, ".", 13); err != nil {
		t.Fatalf("UpTo 13: %v", err)
	}

	now := time.Now().Unix()
	if _, err := db.Exec(
		`INSERT INTO messages(source_id, author_name, text, timestamp, message_id) VALUES (?, ?, ?, ?, ?)`,
		"src-1", "Alice", "hello world", now, "deadbeefdeadbeefdeadbeefdeadbeef",
	); err != nil {
		t.Fatalf("insert message: %v", err)
	}

	if err := goose.DownTo(db, ".", 12); err != nil {
		t.Fatalf("DownTo 12: %v", err)
	}

	// Data should still be there.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("want 1 row after Down, got %d", count)
	}

	// message_id column must not exist.
	if _, err := db.Query(`SELECT message_id FROM messages`); err == nil {
		t.Fatal("expected error selecting message_id column after Down migration")
	}

	// Other columns intact.
	var srcID, authorName, text string
	if err := db.QueryRow(`SELECT source_id, author_name, text FROM messages`).Scan(&srcID, &authorName, &text); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if srcID != "src-1" || authorName != "Alice" || text != "hello world" {
		t.Errorf("data mismatch: source_id=%q author_name=%q text=%q", srcID, authorName, text)
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

// columnInfo is a single row of PRAGMA table_info.
type columnInfo struct {
	Name    string
	Type    string
	NotNull bool
	Default sql.NullString
}

// tableColumns returns the columns of table in declaration order.
func tableColumns(t *testing.T, db *sql.DB, table string) []columnInfo {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()

	var cols []columnInfo
	for rows.Next() {
		var cid, notNull, pk int
		var c columnInfo
		if err := rows.Scan(&cid, &c.Name, &c.Type, &notNull, &c.Default, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		c.NotNull = notNull != 0
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err table_info(%s): %v", table, err)
	}
	return cols
}

// findColumn returns the named column or nil when it is absent.
func findColumn(cols []columnInfo, name string) *columnInfo {
	for i := range cols {
		if cols[i].Name == name {
			return &cols[i]
		}
	}
	return nil
}

// openGooseDB opens an in-memory database with goose configured for the embedded migrations.
func openGooseDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// A single connection keeps the in-memory database shared across queries.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("SetDialect: %v", err)
	}
	return db
}

// TestMigrations016And017FreshDB verifies the new task and message columns exist
// with the expected types, NOT NULL constraints and defaults on a fresh DB.
func TestMigrations016And017FreshDB(t *testing.T) {
	db := openGooseDB(t)
	if err := migrations.Up(db); err != nil {
		t.Fatalf("Up: %v", err)
	}

	tests := []struct {
		table       string
		column      string
		wantType    string
		wantDefault string
	}{
		{"tasks", "priority", "INTEGER", "0"},
		{"tasks", "source_details", "TEXT", "''"},
		{"messages", "role", "TEXT", "''"},
		{"messages", "kind", "TEXT", "'text'"},
		{"messages", "audio_job_id", "TEXT", "''"},
		{"messages", "audio_duration_ms", "INTEGER", "0"},
		{"messages", "task_ids", "TEXT", "''"},
	}
	for _, tt := range tests {
		t.Run(tt.table+"."+tt.column, func(t *testing.T) {
			col := findColumn(tableColumns(t, db, tt.table), tt.column)
			if col == nil {
				t.Fatalf("column %s.%s not found", tt.table, tt.column)
			}
			if col.Type != tt.wantType {
				t.Errorf("type=%q, want %q", col.Type, tt.wantType)
			}
			if !col.NotNull {
				t.Errorf("column must be NOT NULL")
			}
			if !col.Default.Valid || col.Default.String != tt.wantDefault {
				t.Errorf("default=%v, want %q", col.Default, tt.wantDefault)
			}
		})
	}
}

// TestMigration016DefaultsForExistingTasks verifies that tasks created before
// migration 016 get priority 0 and empty source details.
func TestMigration016DefaultsForExistingTasks(t *testing.T) {
	db := openGooseDB(t)
	if err := goose.UpTo(db, ".", 15); err != nil {
		t.Fatalf("UpTo 15: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO projects(id, name, slug, description, task_counter, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"proj-1", "Work", "work", "", 2, now); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	for i, id := range []string{"task-1", "task-2"} {
		if _, err := db.Exec(`INSERT INTO tasks(id, project_id, number, summary, created_at, updated_at, source_kind, source_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, "proj-1", i+1, "summary "+id, now, now, "telegram", "-100123"); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	if err := goose.UpTo(db, ".", 16); err != nil {
		t.Fatalf("UpTo 16: %v", err)
	}

	rows, err := db.Query(`SELECT id, summary, source_kind, priority, source_details FROM tasks ORDER BY number`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	var n int
	for rows.Next() {
		var id, summary, sourceKind, details string
		var priority int
		if err := rows.Scan(&id, &summary, &sourceKind, &priority, &details); err != nil {
			t.Fatalf("scan: %v", err)
		}
		n++
		if summary != "summary "+id || sourceKind != "telegram" {
			t.Errorf("%s: existing data changed: summary=%q source_kind=%q", id, summary, sourceKind)
		}
		if priority != 0 {
			t.Errorf("%s: priority=%d, want 0", id, priority)
		}
		if details != "" {
			t.Errorf("%s: source_details=%q, want empty", id, details)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if n != 2 {
		t.Fatalf("want 2 tasks, got %d", n)
	}
}

// TestMigration017BackfillsRole verifies that existing messages get the default
// metadata and that client chat rows get their role from author_name while
// channel rows keep an empty role.
func TestMigration017BackfillsRole(t *testing.T) {
	db := openGooseDB(t)
	if err := goose.UpTo(db, ".", 16); err != nil {
		t.Fatalf("UpTo 16: %v", err)
	}

	now := time.Now().Unix()
	seed := []struct {
		messageID string
		sourceID  string
		author    string
		wantRole  string
	}{
		{"m-client-user", "client:dev-1", "dev-1", "user"},
		{"m-client-agent", "client:dev-1", "agent", "agent"},
		{"m-client-other", "client:dev-2", "dev-2", "user"},
		{"m-telegram-user", "123456", "Alice", ""},
		{"m-telegram-agent", "-100987", "agent", ""},
		{"m-imap", "imap:me@example.com:INBOX", "Bob", ""},
	}
	for _, s := range seed {
		if _, err := db.Exec(`INSERT INTO messages(source_id, author_name, text, timestamp, message_id) VALUES (?, ?, ?, ?, ?)`,
			s.sourceID, s.author, "text of "+s.messageID, now, s.messageID); err != nil {
			t.Fatalf("insert %s: %v", s.messageID, err)
		}
	}

	if err := goose.UpTo(db, ".", 17); err != nil {
		t.Fatalf("UpTo 17: %v", err)
	}

	for _, s := range seed {
		t.Run(s.messageID, func(t *testing.T) {
			var role, kind, audioJobID, taskIDs, author, text string
			var durationMs int64
			err := db.QueryRow(`SELECT role, kind, audio_job_id, audio_duration_ms, task_ids, author_name, text FROM messages WHERE message_id = ?`, s.messageID).
				Scan(&role, &kind, &audioJobID, &durationMs, &taskIDs, &author, &text)
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			if role != s.wantRole {
				t.Errorf("role=%q, want %q", role, s.wantRole)
			}
			if kind != "text" {
				t.Errorf("kind=%q, want 'text'", kind)
			}
			if audioJobID != "" || durationMs != 0 || taskIDs != "" {
				t.Errorf("audio_job_id=%q audio_duration_ms=%d task_ids=%q, want defaults", audioJobID, durationMs, taskIDs)
			}
			if author != s.author || text != "text of "+s.messageID {
				t.Errorf("existing data changed: author_name=%q text=%q", author, text)
			}
		})
	}
}

// TestMigrations016And017DownRestoreSchema verifies that rolling back both
// migrations restores the previous tasks and messages schema and keeps the data.
func TestMigrations016And017DownRestoreSchema(t *testing.T) {
	db := openGooseDB(t)
	if err := goose.UpTo(db, ".", 15); err != nil {
		t.Fatalf("UpTo 15: %v", err)
	}
	wantTasks := tableColumns(t, db, "tasks")
	wantMessages := tableColumns(t, db, "messages")

	if err := goose.UpTo(db, ".", 17); err != nil {
		t.Fatalf("UpTo 17: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO projects(id, name, slug, description, task_counter, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"proj-1", "Work", "work", "", 1, now); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO tasks(id, project_id, number, summary, created_at, updated_at, priority, source_details) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"task-1", "proj-1", 1, "keep me", now, now, 3, `{"via":"api"}`); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO messages(source_id, author_name, text, timestamp, message_id, role, kind, audio_job_id, audio_duration_ms, task_ids) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"client:dev-1", "agent", "done", time.Now().Unix(), "m-1", "agent", "text", "", 0, `["task-1"]`); err != nil {
		t.Fatalf("insert message: %v", err)
	}

	if err := goose.DownTo(db, ".", 16); err != nil {
		t.Fatalf("DownTo 16: %v", err)
	}
	if got := tableColumns(t, db, "messages"); !equalColumns(got, wantMessages) {
		t.Errorf("messages schema after Down 017:\n got %+v\nwant %+v", got, wantMessages)
	}

	if err := goose.DownTo(db, ".", 15); err != nil {
		t.Fatalf("DownTo 15: %v", err)
	}
	if got := tableColumns(t, db, "tasks"); !equalColumns(got, wantTasks) {
		t.Errorf("tasks schema after Down 016:\n got %+v\nwant %+v", got, wantTasks)
	}

	var summary string
	if err := db.QueryRow(`SELECT summary FROM tasks WHERE id = 'task-1'`).Scan(&summary); err != nil {
		t.Fatalf("task lost after Down: %v", err)
	}
	if summary != "keep me" {
		t.Errorf("summary=%q, want 'keep me'", summary)
	}
	var text string
	if err := db.QueryRow(`SELECT text FROM messages WHERE message_id = 'm-1'`).Scan(&text); err != nil {
		t.Fatalf("message lost after Down: %v", err)
	}
	if text != "done" {
		t.Errorf("text=%q, want 'done'", text)
	}

	// Re-applying after a rollback must succeed.
	if err := goose.UpTo(db, ".", 17); err != nil {
		t.Fatalf("re-applying UpTo 17: %v", err)
	}
}

func equalColumns(a, b []columnInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
