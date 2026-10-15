package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// SQLiteAutoTaskStore stores durable automatic-task receipts and decisions.
type SQLiteAutoTaskStore struct{ db *sql.DB }

func NewSQLiteAutoTaskStore(db *sql.DB) *SQLiteAutoTaskStore { return &SQLiteAutoTaskStore{db: db} }

func (s *SQLiteAutoTaskStore) Processed(ctx context.Context, key string) (bool, error) {
	return autoInputProcessed(s.db.QueryRowContext(ctx, `SELECT 1 FROM auto_task_inputs WHERE input_key = ?`, key))
}

func (s *SQLiteAutoTaskStore) ProcessedTx(ctx context.Context, tx *sql.Tx, key string) (bool, error) {
	return autoInputProcessed(tx.QueryRowContext(ctx, `SELECT 1 FROM auto_task_inputs WHERE input_key = ?`, key))
}

func autoInputProcessed(row *sql.Row) (bool, error) {
	var found int
	if err := row.Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("read automatic task receipt: %w", err)
	}
	return true, nil
}

func (s *SQLiteAutoTaskStore) RecordInputTx(ctx context.Context, tx *sql.Tx, key string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO auto_task_inputs(input_key, created_at) VALUES (?, ?)`, key, at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("record automatic task receipt: %w", err)
	}
	return nil
}

func (s *SQLiteAutoTaskStore) InsertCandidateTx(ctx context.Context, tx *sql.Tx, c model.TaskCandidate) error {
	payload, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode automatic task candidate: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO auto_task_candidates(id, project_id, status, payload, created_at, question_chat_id, question_message_id) VALUES (?, ?, ?, ?, ?, ?, ?)`, c.ID, c.Task.ProjectID, c.Status, string(payload), c.CreatedAt.UTC().Format(time.RFC3339Nano), c.QuestionChatID, c.QuestionMsgID)
	if err != nil {
		return fmt.Errorf("insert automatic task candidate: %w", err)
	}
	return nil
}

func (s *SQLiteAutoTaskStore) UpdateCandidateTx(ctx context.Context, tx *sql.Tx, c model.TaskCandidate) error {
	payload, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode automatic task candidate: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE auto_task_candidates SET status = ?, payload = ?, question_chat_id = ?, question_message_id = ? WHERE id = ?`, c.Status, string(payload), c.QuestionChatID, c.QuestionMsgID, c.ID)
	if err != nil {
		return fmt.Errorf("update automatic task candidate: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read candidate update count: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("automatic task candidate %q not found", c.ID)
	}
	return nil
}

func scanAutoCandidate(row interface{ Scan(...any) error }) (*model.TaskCandidate, error) {
	var payload string
	var chatID int64
	var messageID int
	if err := row.Scan(&payload, &chatID, &messageID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("read automatic task candidate: %w", err)
	}
	var c model.TaskCandidate
	if err := json.Unmarshal([]byte(payload), &c); err != nil {
		return nil, fmt.Errorf("decode automatic task candidate: %w", err)
	}
	c.QuestionChatID, c.QuestionMsgID = chatID, messageID
	return &c, nil
}

func (s *SQLiteAutoTaskStore) GetCandidate(ctx context.Context, id string) (*model.TaskCandidate, error) {
	return scanAutoCandidate(s.db.QueryRowContext(ctx, `SELECT payload, question_chat_id, question_message_id FROM auto_task_candidates WHERE id = ?`, id))
}

func (s *SQLiteAutoTaskStore) GetCandidateTx(ctx context.Context, tx *sql.Tx, id string) (*model.TaskCandidate, error) {
	return scanAutoCandidate(tx.QueryRowContext(ctx, `SELECT payload, question_chat_id, question_message_id FROM auto_task_candidates WHERE id = ?`, id))
}

func (s *SQLiteAutoTaskStore) PendingQuestions(ctx context.Context, limit int) ([]model.TaskCandidate, error) {
	query := `SELECT payload, question_chat_id, question_message_id FROM auto_task_candidates WHERE status = 'pending' AND question_message_id = 0 ORDER BY created_at, id`
	var args []any
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list pending automatic tasks: %w", err)
	}
	defer rows.Close()
	var candidates []model.TaskCandidate
	for rows.Next() {
		c, err := scanAutoCandidate(rows)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending automatic tasks: %w", err)
	}
	return candidates, nil
}

func (s *SQLiteAutoTaskStore) MarkQuestionSent(ctx context.Context, id string, chatID int64, messageID int) error {
	if chatID == 0 || messageID <= 0 {
		return fmt.Errorf("invalid automatic task question identity")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE auto_task_candidates SET question_chat_id = ?, question_message_id = ? WHERE id = ? AND status = 'pending' AND question_message_id = 0`, chatID, messageID, id)
	if err != nil {
		return fmt.Errorf("mark automatic task question sent: %w", err)
	}
	return nil
}

func (s *SQLiteAutoTaskStore) ReferencesTx(ctx context.Context, tx *sql.Tx, projectID string, closedSince time.Time) ([]model.TaskReference, error) {
	rows, err := tx.QueryContext(ctx, `SELECT t.id, p.slug, t.number, t.summary, t.details, t.topic, t.status, t.deadline, t.created_at, t.closed_at, t.source_kind, t.source_id FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.project_id = ? AND (t.status = 'open' OR (t.status IN ('done', 'cancelled') AND julianday(t.closed_at) >= julianday(?))) ORDER BY t.id`, projectID, closedSince.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("list automatic task references: %w", err)
	}
	var refs []model.TaskReference
	for rows.Next() {
		var ref model.TaskReference
		var id, slug, created string
		var number int
		var deadline, closed *string
		ref.Task.ProjectID = projectID
		if err := rows.Scan(&id, &slug, &number, &ref.Task.Summary, &ref.Task.Details, &ref.Task.Topic, &ref.State, &deadline, &created, &closed, &ref.Task.Source.Kind, &ref.Task.Source.ID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan automatic task reference: %w", err)
		}
		ref.ID, ref.DisplayID = "task:"+id, fmt.Sprintf("%s#%d", slug, number)
		ref.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("parse reference creation time: %w", err)
		}
		if deadline != nil {
			value, err := time.Parse(time.RFC3339Nano, *deadline)
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("parse reference deadline: %w", err)
			}
			ref.Task.Deadline = &value
		}
		if closed != nil {
			value, err := parseClosedAt(*closed)
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("parse reference closure time: %w", err)
			}
			ref.ClosedAt = &value
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate automatic task references: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close automatic task references: %w", err)
	}
	rows, err = tx.QueryContext(ctx, `SELECT payload, question_chat_id, question_message_id FROM auto_task_candidates WHERE project_id = ? AND status IN ('pending', 'rejected') ORDER BY id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list candidate references: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanAutoCandidate(rows)
		if err != nil {
			return nil, err
		}
		refs = append(refs, model.TaskReference{ID: "candidate:" + c.ID, Task: c.Task, State: c.Status, CreatedAt: c.CreatedAt, SourceText: c.SourceText})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate candidate references: %w", err)
	}
	return refs, nil
}

var _ model.AutoTaskStore = (*SQLiteAutoTaskStore)(nil)
