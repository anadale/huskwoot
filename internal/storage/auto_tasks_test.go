package storage_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/storage"
)

func TestAutoTaskStore_ReceiptAndCandidateRollbackTogether(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenDB(filepath.Join(t.TempDir(), "auto.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := storage.NewSQLiteAutoTaskStore(db)
	tasks, err := storage.NewSQLiteTaskStore(db)
	if err != nil {
		t.Fatal(err)
	}
	candidate := model.TaskCandidate{ID: "candidate-1", Task: model.CreateTaskRequest{ProjectID: tasks.DefaultProjectID(), Summary: "Send report"}, Status: "pending", CreatedAt: time.Now().UTC()}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordInputTx(ctx, tx, "input-1", candidate.CreatedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertCandidateTx(ctx, tx, candidate); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	processed, err := store.Processed(ctx, "input-1")
	if err != nil || processed {
		t.Fatalf("rolled-back receipt: processed=%v err=%v", processed, err)
	}
	got, err := store.GetCandidate(ctx, candidate.ID)
	if err != nil || got != nil {
		t.Fatalf("rolled-back candidate: got=%v err=%v", got, err)
	}

	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordInputTx(ctx, tx, "input-1", candidate.CreatedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertCandidateTx(ctx, tx, candidate); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkQuestionSent(ctx, candidate.ID, 7, 8); err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingQuestions(ctx, 20)
	if err != nil || len(pending) != 0 {
		t.Fatalf("sent question returned again: %v, %v", pending, err)
	}
	got, err = store.GetCandidate(ctx, candidate.ID)
	if err != nil || got.QuestionChatID != 7 || got.QuestionMsgID != 8 {
		t.Fatalf("question identity not durable: %v, %v", got, err)
	}
}
