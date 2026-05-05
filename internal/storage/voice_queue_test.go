package storage_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/storage"
)

func openVoiceQueueDB(t *testing.T) *storage.SQLiteVoiceQueue {
	t.Helper()
	db := openTestDB(t)
	return storage.NewSQLiteVoiceQueue(db)
}

func makeTelegramVoiceJob(chatID int64, msgID int) model.VoiceJob {
	t := time.Unix(1700000000, 0).UTC()
	return model.VoiceJob{
		SourceKind:    "telegram",
		Payload:       "file_abc",
		ChatID:        chatID,
		MessageID:     msgID,
		MimeType:      "audio/ogg",
		Duration:      10 * time.Second,
		ReceivedAt:    t,
		NextAttemptAt: t,
	}
}

func makeClientVoiceJob(deviceID, clientJobID string) model.VoiceJob {
	t := time.Unix(1700000000, 0).UTC()
	return model.VoiceJob{
		SourceKind:    "client",
		Payload:       "2026/05/" + clientJobID + ".ogg",
		DeviceID:      deviceID,
		ClientJobID:   clientJobID,
		MimeType:      "audio/ogg;codecs=opus",
		Duration:      8 * time.Second,
		ReceivedAt:    t,
		NextAttemptAt: t,
	}
}

func TestSQLiteVoiceQueue_EnqueueAndClaim(t *testing.T) {
	q := openVoiceQueueDB(t)
	ctx := context.Background()
	now := time.Unix(1700000010, 0)

	job := makeTelegramVoiceJob(100, 1)
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	got, err := q.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got == nil {
		t.Fatal("Claim: want non-nil job, got nil")
	}
	if got.SourceKind != "telegram" {
		t.Errorf("SourceKind: want %q, got %q", "telegram", got.SourceKind)
	}
	if got.Payload != "file_abc" {
		t.Errorf("Payload: want %q, got %q", "file_abc", got.Payload)
	}
	if got.ChatID != 100 {
		t.Errorf("ChatID: want 100, got %d", got.ChatID)
	}
	if got.MessageID != 1 {
		t.Errorf("MessageID: want 1, got %d", got.MessageID)
	}
	if got.MimeType != "audio/ogg" {
		t.Errorf("MimeType: want %q, got %q", "audio/ogg", got.MimeType)
	}
	if got.Duration != 10*time.Second {
		t.Errorf("Duration: want 10s, got %v", got.Duration)
	}
	if got.Attempts != 0 {
		t.Errorf("Attempts: want 0, got %d", got.Attempts)
	}
}

func TestSQLiteVoiceQueue_EnqueueIdempotent(t *testing.T) {
	q := openVoiceQueueDB(t)
	ctx := context.Background()
	now := time.Unix(1700000010, 0)

	job := makeTelegramVoiceJob(200, 5)
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("first Enqueue: %v", err)
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("second Enqueue (should be no-op): %v", err)
	}

	got, err := q.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got == nil {
		t.Fatal("Claim: want one job, got nil")
	}

	if err := q.Complete(ctx, got.ID); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got2, err := q.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim after Complete: %v", err)
	}
	if got2 != nil {
		t.Errorf("queue must be empty after completing the only job, got id=%d", got2.ID)
	}
}

func TestSQLiteVoiceQueue_ClaimReturnsNilWhenEmpty(t *testing.T) {
	q := openVoiceQueueDB(t)
	ctx := context.Background()

	got, err := q.Claim(ctx, time.Now())
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got != nil {
		t.Errorf("Claim on empty queue: want nil, got %+v", got)
	}
}

func TestSQLiteVoiceQueue_ClaimRespectsNextAttemptAt(t *testing.T) {
	q := openVoiceQueueDB(t)
	ctx := context.Background()

	job := makeTelegramVoiceJob(300, 9)
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	past := time.Unix(job.ReceivedAt.Unix()-1, 0)
	got, err := q.Claim(ctx, past)
	if err != nil {
		t.Fatalf("Claim (past): %v", err)
	}
	if got != nil {
		t.Error("Claim with past time: want nil (not yet due), got a job")
	}

	got, err = q.Claim(ctx, job.ReceivedAt)
	if err != nil {
		t.Fatalf("Claim (at time): %v", err)
	}
	if got == nil {
		t.Error("Claim with exact time: want job, got nil")
	}
}

func TestSQLiteVoiceQueue_Reschedule(t *testing.T) {
	q := openVoiceQueueDB(t)
	ctx := context.Background()
	now := time.Unix(1700000010, 0)

	job := makeTelegramVoiceJob(400, 3)
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	claimed, err := q.Claim(ctx, now)
	if err != nil || claimed == nil {
		t.Fatalf("Claim: err=%v job=%v", err, claimed)
	}

	nextAt := now.Add(30 * time.Second)
	if err := q.Reschedule(ctx, claimed.ID, nextAt, "stt error"); err != nil {
		t.Fatalf("Reschedule: %v", err)
	}

	early, err := q.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim after reschedule (early): %v", err)
	}
	if early != nil {
		t.Error("job should not be claimable before nextAt")
	}

	late, err := q.Claim(ctx, nextAt)
	if err != nil {
		t.Fatalf("Claim after reschedule (at nextAt): %v", err)
	}
	if late == nil {
		t.Fatal("job should be claimable at nextAt, got nil")
	}
	if late.Attempts != 1 {
		t.Errorf("Attempts after reschedule: want 1, got %d", late.Attempts)
	}
}

func TestSQLiteVoiceQueue_RescheduleStoresLastError(t *testing.T) {
	db := openTestDB(t)
	q := storage.NewSQLiteVoiceQueue(db)
	ctx := context.Background()
	now := time.Unix(1700000010, 0)

	job := makeTelegramVoiceJob(450, 8)
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	claimed, err := q.Claim(ctx, now)
	if err != nil || claimed == nil {
		t.Fatalf("Claim: err=%v job=%v", err, claimed)
	}

	nextAt := now.Add(30 * time.Second)
	const wantErr = "stt timeout"
	if err := q.Reschedule(ctx, claimed.ID, nextAt, wantErr); err != nil {
		t.Fatalf("Reschedule: %v", err)
	}

	var gotErr sql.NullString
	row := db.QueryRowContext(ctx, `SELECT last_error FROM voice_queue WHERE id = ?`, claimed.ID)
	if err := row.Scan(&gotErr); err != nil {
		t.Fatalf("querying last_error: %v", err)
	}
	if !gotErr.Valid || gotErr.String != wantErr {
		t.Errorf("last_error: want %q, got %q (valid=%v)", wantErr, gotErr.String, gotErr.Valid)
	}
}

func TestSQLiteVoiceQueue_Complete(t *testing.T) {
	q := openVoiceQueueDB(t)
	ctx := context.Background()
	now := time.Unix(1700000010, 0)

	job := makeTelegramVoiceJob(500, 7)
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	claimed, err := q.Claim(ctx, now)
	if err != nil || claimed == nil {
		t.Fatalf("Claim: err=%v job=%v", err, claimed)
	}

	if err := q.Complete(ctx, claimed.ID); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	got, err := q.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim after Complete: %v", err)
	}
	if got != nil {
		t.Error("queue should be empty after Complete, got a job")
	}
}

// TestSQLiteVoiceQueue_ClientJobEnqueueAndClaim verifies that client jobs without
// ChatID/MessageID enqueue and claim correctly.
func TestSQLiteVoiceQueue_ClientJobEnqueueAndClaim(t *testing.T) {
	q := openVoiceQueueDB(t)
	ctx := context.Background()
	now := time.Unix(1700000010, 0)

	job := makeClientVoiceJob("dev-1", "550e8400-e29b-41d4-a716-446655440000")
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	got, err := q.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got == nil {
		t.Fatal("Claim: want non-nil job, got nil")
	}
	if got.SourceKind != "client" {
		t.Errorf("SourceKind: want %q, got %q", "client", got.SourceKind)
	}
	if got.Payload != job.Payload {
		t.Errorf("Payload: want %q, got %q", job.Payload, got.Payload)
	}
	if got.DeviceID != "dev-1" {
		t.Errorf("DeviceID: want %q, got %q", "dev-1", got.DeviceID)
	}
	if got.ClientJobID != "550e8400-e29b-41d4-a716-446655440000" {
		t.Errorf("ClientJobID: want %q, got %q", "550e8400-e29b-41d4-a716-446655440000", got.ClientJobID)
	}
	if got.ChatID != 0 {
		t.Errorf("ChatID: want 0 (client job), got %d", got.ChatID)
	}
	if got.MessageID != 0 {
		t.Errorf("MessageID: want 0 (client job), got %d", got.MessageID)
	}
}

// TestSQLiteVoiceQueue_ClientJobDedup verifies that enqueueing the same client_job_id
// twice is a no-op (INSERT OR IGNORE via the UNIQUE index on client_job_id).
func TestSQLiteVoiceQueue_ClientJobDedup(t *testing.T) {
	q := openVoiceQueueDB(t)
	ctx := context.Background()
	now := time.Unix(1700000010, 0)

	jobID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	job := makeClientVoiceJob("dev-1", jobID)
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("first Enqueue: %v", err)
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("second Enqueue (should be no-op): %v", err)
	}

	got, err := q.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got == nil {
		t.Fatal("Claim: want one job, got nil")
	}
	if err := q.Complete(ctx, got.ID); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got2, err := q.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim after Complete: %v", err)
	}
	if got2 != nil {
		t.Errorf("queue must be empty after completing the only job, got id=%d", got2.ID)
	}
}

// TestSQLiteVoiceQueue_ClientJobWithProjectID verifies that project_id is stored and
// restored correctly, and that an empty ProjectID maps to NULL (read back as "").
func TestSQLiteVoiceQueue_ClientJobWithProjectID(t *testing.T) {
	q := openVoiceQueueDB(t)
	ctx := context.Background()
	now := time.Unix(1700000010, 0)

	projectUUID := "11111111-2222-3333-4444-555555555555"
	jobID := "cccccccc-dddd-eeee-ffff-000000000000"
	job := makeClientVoiceJob("dev-2", jobID)
	job.ProjectID = projectUUID

	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("Enqueue with ProjectID: %v", err)
	}

	got, err := q.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got == nil {
		t.Fatal("Claim: want non-nil job, got nil")
	}
	if got.ProjectID != projectUUID {
		t.Errorf("ProjectID: want %q, got %q", projectUUID, got.ProjectID)
	}

	// Verify empty ProjectID maps to NULL → read back as "".
	if err := q.Complete(ctx, got.ID); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	jobID2 := "aaaabbbb-cccc-dddd-eeee-ffffaaaabbbb"
	job2 := makeClientVoiceJob("dev-2", jobID2)
	// job2.ProjectID is "" (zero value)
	if err := q.Enqueue(ctx, job2); err != nil {
		t.Fatalf("Enqueue without ProjectID: %v", err)
	}
	got2, err := q.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim (no project): %v", err)
	}
	if got2 == nil {
		t.Fatal("Claim (no project): want non-nil, got nil")
	}
	if got2.ProjectID != "" {
		t.Errorf("ProjectID: want empty string, got %q", got2.ProjectID)
	}
}

// TestSQLiteVoiceQueue_HasClientPayload verifies that HasClientPayload returns true for
// an active client job and false when the job is absent or belongs to telegram.
func TestSQLiteVoiceQueue_HasClientPayload(t *testing.T) {
	q := openVoiceQueueDB(t)
	ctx := context.Background()

	payload := "2026/05/abc-job.ogg"
	job := model.VoiceJob{
		SourceKind:  "client",
		Payload:     payload,
		DeviceID:    "dev-1",
		ClientJobID: "abc-job-uuid",
		MimeType:    "audio/ogg;codecs=opus",
		Duration:    5 * time.Second,
		ReceivedAt:  time.Unix(1700000000, 0).UTC(),
	}

	// Before enqueue: not found.
	has, err := q.HasClientPayload(ctx, payload)
	if err != nil {
		t.Fatalf("HasClientPayload (before enqueue): %v", err)
	}
	if has {
		t.Error("HasClientPayload: want false before enqueue, got true")
	}

	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// After enqueue: found.
	has, err = q.HasClientPayload(ctx, payload)
	if err != nil {
		t.Fatalf("HasClientPayload (after enqueue): %v", err)
	}
	if !has {
		t.Error("HasClientPayload: want true after enqueue, got false")
	}

	// A telegram job with the same payload string is not counted as client.
	tgJob := model.VoiceJob{
		SourceKind: "telegram",
		Payload:    payload, // same string, different source_kind
		ChatID:     123,
		MessageID:  1,
		MimeType:   "audio/ogg",
		Duration:   5 * time.Second,
		ReceivedAt: time.Unix(1700000000, 0).UTC(),
	}
	if err := q.Enqueue(ctx, tgJob); err != nil {
		t.Fatalf("Enqueue telegram: %v", err)
	}

	// Claim and complete the client job.
	claimed, err := q.Claim(ctx, time.Unix(1700000010, 0))
	if err != nil || claimed == nil {
		t.Fatalf("Claim client job: err=%v job=%v", err, claimed)
	}
	if err := q.Complete(ctx, claimed.ID); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// After client job completion: not found even though telegram job with same payload exists.
	has, err = q.HasClientPayload(ctx, payload)
	if err != nil {
		t.Fatalf("HasClientPayload (after complete): %v", err)
	}
	if has {
		t.Error("HasClientPayload: want false after client job completed, got true")
	}
}

// TestSQLiteVoiceQueue_TelegramSourceKindPreserved verifies SourceKind="telegram" and
// Payload=file_id are correctly stored and retrieved after migration.
func TestSQLiteVoiceQueue_TelegramSourceKindPreserved(t *testing.T) {
	q := openVoiceQueueDB(t)
	ctx := context.Background()
	now := time.Unix(1700000010, 0)

	job := model.VoiceJob{
		SourceKind: "telegram",
		Payload:    "tg_file_xyz",
		ChatID:     999,
		MessageID:  42,
		MimeType:   "audio/ogg",
		Duration:   5 * time.Second,
		ReceivedAt: time.Unix(1700000000, 0).UTC(),
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	got, err := q.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got == nil {
		t.Fatal("Claim returned nil")
	}
	if got.SourceKind != "telegram" {
		t.Errorf("SourceKind: want %q, got %q", "telegram", got.SourceKind)
	}
	if got.Payload != "tg_file_xyz" {
		t.Errorf("Payload: want %q, got %q", "tg_file_xyz", got.Payload)
	}
	if got.ChatID != 999 {
		t.Errorf("ChatID: want 999, got %d", got.ChatID)
	}
	if got.MessageID != 42 {
		t.Errorf("MessageID: want 42, got %d", got.MessageID)
	}
	if got.DeviceID != "" {
		t.Errorf("DeviceID: want empty (telegram job), got %q", got.DeviceID)
	}
	if got.ClientJobID != "" {
		t.Errorf("ClientJobID: want empty (telegram job), got %q", got.ClientJobID)
	}
}
