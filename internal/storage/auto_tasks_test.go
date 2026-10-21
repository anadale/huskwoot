package storage_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
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

func TestAutoTaskStore_ReassignProjectTx(t *testing.T) {
	for _, commit := range []bool{true, false} {
		name := "rollback"
		if commit {
			name = "commit"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			tasks, db := newTaskStore(t)
			store := storage.NewSQLiteAutoTaskStore(db)
			source := &model.Project{Name: "Source", Slug: "source"}
			if err := createProjectHelper(t, db, tasks, source); err != nil {
				t.Fatal(err)
			}
			destination := tasks.DefaultProjectID()
			now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
			decided := now.Add(time.Minute)
			var candidates []model.TaskCandidate
			for _, status := range []string{"pending", "created", "rejected", "duplicate"} {
				candidates = append(candidates, model.TaskCandidate{
					ID:         "candidate-" + status,
					Task:       model.CreateTaskRequest{ProjectID: source.ID, Summary: "Send report", Details: "Include totals"},
					SourceText: "Original request", Status: status, Reason: "Existing decision",
					MatchID: "task:existing", MatchSummary: "Earlier report", MatchDisplayID: "source#1",
					CreatedAt: now, DecidedAt: &decided, CreatedTaskID: "created-task",
					QuestionChatID: 7, QuestionMsgID: 8,
				})
			}
			unrelated := candidates[0]
			unrelated.ID, unrelated.Task.ProjectID = "unrelated", destination
			candidates = append(candidates, unrelated)
			if err := withTx(t, db, func(tx *sql.Tx) error {
				for _, candidate := range candidates {
					if err := store.InsertCandidateTx(ctx, tx, candidate); err != nil {
						return err
					}
				}
				// Sent-question columns can be newer than the stored JSON payload.
				_, err := tx.ExecContext(ctx, `UPDATE auto_task_candidates SET question_chat_id = 70, question_message_id = 80 WHERE id = ?`, candidates[0].ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			candidates[0].QuestionChatID, candidates[0].QuestionMsgID = 70, 80

			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := store.ReassignProjectTx(ctx, tx, source.ID, destination); err != nil {
				t.Fatal(err)
			}
			for _, candidate := range candidates {
				want := candidate
				want.Task.ProjectID = destination
				got, err := store.GetCandidateTx(ctx, tx, candidate.ID)
				if err != nil || !reflect.DeepEqual(got, &want) {
					t.Fatalf("candidate in transaction: got=%+v want=%+v err=%v", got, want, err)
				}
			}
			if commit {
				err = tx.Commit()
			} else {
				err = tx.Rollback()
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, candidate := range candidates {
				want := candidate
				if commit {
					want.Task.ProjectID = destination
				}
				got, err := store.GetCandidate(ctx, candidate.ID)
				if err != nil || !reflect.DeepEqual(got, &want) {
					t.Fatalf("persisted candidate: got=%+v want=%+v err=%v", got, want, err)
				}
				var projectID, payload, status string
				var chatID int64
				var messageID int
				if err := db.QueryRowContext(ctx, `SELECT project_id, payload, status, question_chat_id, question_message_id FROM auto_task_candidates WHERE id = ?`, candidate.ID).Scan(&projectID, &payload, &status, &chatID, &messageID); err != nil {
					t.Fatal(err)
				}
				var decoded model.TaskCandidate
				if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
					t.Fatal(err)
				}
				if projectID != want.Task.ProjectID || decoded.Task.ProjectID != want.Task.ProjectID {
					t.Fatalf("project mismatch: column=%q payload=%q want=%q", projectID, decoded.Task.ProjectID, want.Task.ProjectID)
				}
				wantPayload := want
				if !commit && candidate.ID == candidates[0].ID {
					wantPayload.QuestionChatID, wantPayload.QuestionMsgID = 7, 8
				}
				if !reflect.DeepEqual(decoded, wantPayload) {
					t.Fatalf("payload metadata changed: got=%+v want=%+v", decoded, wantPayload)
				}
				if status != want.Status || chatID != want.QuestionChatID || messageID != want.QuestionMsgID {
					t.Fatalf("candidate metadata changed: status=%q chat=%d message=%d", status, chatID, messageID)
				}
			}
		})
	}
}

func TestAutoTaskStore_ReassignProjectTxMalformedPayload(t *testing.T) {
	ctx := context.Background()
	tasks, db := newTaskStore(t)
	store := storage.NewSQLiteAutoTaskStore(db)
	source := &model.Project{Name: "Source", Slug: "source"}
	if err := createProjectHelper(t, db, tasks, source); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"valid", "malformed"} {
		candidate := model.TaskCandidate{ID: id, Task: model.CreateTaskRequest{ProjectID: source.ID, Summary: id}, Status: "pending", CreatedAt: time.Now().UTC()}
		if err := withTx(t, db, func(tx *sql.Tx) error {
			return store.InsertCandidateTx(ctx, tx, candidate)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE auto_task_candidates SET payload = '{' WHERE id = 'malformed'`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	err = store.ReassignProjectTx(ctx, tx, source.ID, tasks.DefaultProjectID())
	var syntaxError *json.SyntaxError
	if !errors.As(err, &syntaxError) {
		t.Fatalf("malformed payload must return a wrapped JSON error: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auto_task_candidates WHERE project_id = ?`, source.ID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 2 {
		t.Fatalf("candidate reassignment survived rollback: remaining=%d", remaining)
	}
	got, err := store.GetCandidate(ctx, "valid")
	if err != nil || got == nil || got.Task.ProjectID != source.ID {
		t.Fatalf("valid candidate changed after failure: got=%+v err=%v", got, err)
	}
}
