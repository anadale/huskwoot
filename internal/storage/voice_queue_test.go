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

func makeVoiceJob(chatID int64, msgID int) model.VoiceJob {
	return model.VoiceJob{
		ChatID:     chatID,
		MessageID:  msgID,
		FileID:     "file_abc",
		MimeType:   "audio/ogg",
		Duration:   10 * time.Second,
		ReceivedAt: time.Unix(1700000000, 0).UTC(),
	}
}

func TestSQLiteVoiceQueue_EnqueueAndClaim(t *testing.T) {
	q := openVoiceQueueDB(t)
	ctx := context.Background()
	now := time.Unix(1700000010, 0)

	job := makeVoiceJob(100, 1)
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
	if got.ChatID != 100 {
		t.Errorf("ChatID: want 100, got %d", got.ChatID)
	}
	if got.MessageID != 1 {
		t.Errorf("MessageID: want 1, got %d", got.MessageID)
	}
	if got.FileID != "file_abc" {
		t.Errorf("FileID: want %q, got %q", "file_abc", got.FileID)
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

	job := makeVoiceJob(200, 5)
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("first Enqueue: %v", err)
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("second Enqueue (should be no-op): %v", err)
	}

	// Claim should return exactly one job.
	got, err := q.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got == nil {
		t.Fatal("Claim: want one job, got nil")
	}

	// Complete the job — the queue should now be empty (only one row was inserted).
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

	job := makeVoiceJob(300, 9)
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Claim with a time BEFORE the job's next_attempt_at (received_at).
	past := time.Unix(job.ReceivedAt.Unix()-1, 0)
	got, err := q.Claim(ctx, past)
	if err != nil {
		t.Fatalf("Claim (past): %v", err)
	}
	if got != nil {
		t.Error("Claim with past time: want nil (not yet due), got a job")
	}

	// Claim with a time AT or AFTER received_at.
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

	job := makeVoiceJob(400, 3)
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

	// Job should not be available before nextAt.
	early, err := q.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim after reschedule (early): %v", err)
	}
	if early != nil {
		t.Error("job should not be claimable before nextAt")
	}

	// Job should be available at or after nextAt.
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

	job := makeVoiceJob(450, 8)
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

	job := makeVoiceJob(500, 7)
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

	// Queue should be empty now.
	got, err := q.Claim(ctx, now)
	if err != nil {
		t.Fatalf("Claim after Complete: %v", err)
	}
	if got != nil {
		t.Error("queue should be empty after Complete, got a job")
	}
}
