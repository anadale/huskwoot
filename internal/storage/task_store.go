package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/anadale/huskwoot/internal/model"
)

const inboxProjectName = "Inbox"
const inboxProjectSlug = "inbox"

// SQLiteTaskStore implements model.TaskStore on top of SQLite.
type SQLiteTaskStore struct {
	db            *sql.DB
	defaultProjID string
}

// NewSQLiteTaskStore creates a new SQLiteTaskStore and ensures the "Inbox" project exists as the default.
func NewSQLiteTaskStore(db *sql.DB) (*SQLiteTaskStore, error) {
	s := &SQLiteTaskStore{db: db}

	id, err := s.ensureInbox(context.Background())
	if err != nil {
		return nil, fmt.Errorf("initializing default project: %w", err)
	}
	s.defaultProjID = id

	return s, nil
}

// DefaultProjectID returns the UUID of the "Inbox" project.
func (s *SQLiteTaskStore) DefaultProjectID() string {
	return s.defaultProjID
}

// ensureInbox creates the "Inbox" project if it doesn't exist yet, and returns its UUID.
func (s *SQLiteTaskStore) ensureInbox(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM projects WHERE name = ?`, inboxProjectName,
	).Scan(&id)

	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("looking up Inbox: %w", err)
	}

	id = uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO projects (id, name, slug, description, task_counter, created_at) VALUES (?, ?, ?, ?, 0, ?)`,
		id, inboxProjectName, inboxProjectSlug, "", now,
	)
	if err != nil {
		return "", fmt.Errorf("creating Inbox: %w", err)
	}
	return id, nil
}

// CreateProjectTx creates a new project within the given transaction. Requires p.Slug != "".
// Populates ID, TaskCounter, and CreatedAt in the passed struct.
func (s *SQLiteTaskStore) CreateProjectTx(ctx context.Context, tx *sql.Tx, p *model.Project) error {
	if p.Slug == "" {
		return fmt.Errorf("creating project %q: slug is required", p.Name)
	}
	p.ID = uuid.NewString()
	p.TaskCounter = 0
	p.CreatedAt = time.Now().UTC()

	_, err := tx.ExecContext(ctx,
		`INSERT INTO projects (id, name, slug, description, task_counter, created_at) VALUES (?, ?, ?, ?, 0, ?)`,
		p.ID, p.Name, p.Slug, p.Description, p.CreatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("creating project %q: %w", p.Name, err)
	}
	return nil
}

// UpdateProjectTx applies changes to a project within the given transaction.
// Returns an error if the project is not found.
func (s *SQLiteTaskStore) UpdateProjectTx(ctx context.Context, tx *sql.Tx, id string, upd model.ProjectUpdate) error {
	setClauses := []string{}
	args := []any{}

	if upd.Name != nil {
		setClauses = append(setClauses, "name = ?")
		args = append(args, *upd.Name)
	}
	if upd.Description != nil {
		setClauses = append(setClauses, "description = ?")
		args = append(args, *upd.Description)
	}
	if upd.Slug != nil {
		setClauses = append(setClauses, "slug = ?")
		args = append(args, *upd.Slug)
	}

	if len(setClauses) == 0 {
		return nil
	}

	query := "UPDATE projects SET " + strings.Join(setClauses, ", ") + " WHERE id = ?"
	args = append(args, id)

	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating project %q: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking project update %q: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("project %q not found", id)
	}
	return nil
}

// GetProject returns a project by UUID. Returns nil, nil if not found.
func (s *SQLiteTaskStore) GetProject(ctx context.Context, id string) (*model.Project, error) {
	return scanProject(ctx, s.db, id)
}

// GetProjectTx reads a project by UUID within the given transaction.
func (s *SQLiteTaskStore) GetProjectTx(ctx context.Context, tx *sql.Tx, id string) (*model.Project, error) {
	return scanProject(ctx, tx, id)
}

// projectQuerier unifies the *sql.DB and *sql.Tx interfaces for a shared SELECT.
type projectQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func scanProject(ctx context.Context, q projectQuerier, id string) (*model.Project, error) {
	var p model.Project
	var createdAtStr string

	err := q.QueryRowContext(ctx,
		`SELECT id, name, slug, description, task_counter, created_at FROM projects WHERE id = ?`, id,
	).Scan(&p.ID, &p.Name, &p.Slug, &p.Description, &p.TaskCounter, &createdAtStr)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting project %q: %w", id, err)
	}

	p.CreatedAt, err = time.Parse(time.RFC3339, createdAtStr)
	if err != nil {
		return nil, fmt.Errorf("parsing created_at for project %q: %w", id, err)
	}
	return &p, nil
}

// ListProjects returns all projects ordered by created_at.
func (s *SQLiteTaskStore) ListProjects(ctx context.Context) ([]model.Project, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, slug, description, task_counter, created_at FROM projects ORDER BY created_at`,
	)
	if err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	defer rows.Close()

	var projects []model.Project
	for rows.Next() {
		var p model.Project
		var createdAtStr string
		if err := rows.Scan(&p.ID, &p.Name, &p.Slug, &p.Description, &p.TaskCounter, &createdAtStr); err != nil {
			return nil, fmt.Errorf("scanning project: %w", err)
		}
		p.CreatedAt, err = time.Parse(time.RFC3339, createdAtStr)
		if err != nil {
			return nil, fmt.Errorf("parsing created_at: %w", err)
		}
		projects = append(projects, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating projects: %w", err)
	}
	return projects, nil
}

// FindProjectByName finds a project by name. Returns nil, nil if not found.
func (s *SQLiteTaskStore) FindProjectByName(ctx context.Context, name string) (*model.Project, error) {
	var p model.Project
	var createdAtStr string

	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, slug, description, task_counter, created_at FROM projects WHERE name = ?`, name,
	).Scan(&p.ID, &p.Name, &p.Slug, &p.Description, &p.TaskCounter, &createdAtStr)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("looking up project %q: %w", name, err)
	}

	p.CreatedAt, err = time.Parse(time.RFC3339, createdAtStr)
	if err != nil {
		return nil, fmt.Errorf("parsing created_at for project %q: %w", name, err)
	}
	return &p, nil
}

// CreateTaskTx creates a new task within the given transaction.
// Atomically increments the project's task_counter and assigns the number to the task.
// Populates ID, Number, Status, CreatedAt, UpdatedAt.
func (s *SQLiteTaskStore) CreateTaskTx(ctx context.Context, tx *sql.Tx, task *model.Task) error {
	if task.ProjectID == "" {
		return fmt.Errorf("CreateTask: project_id is required")
	}

	var counter int
	if err := tx.QueryRowContext(ctx,
		`SELECT task_counter FROM projects WHERE id = ?`, task.ProjectID,
	).Scan(&counter); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("CreateTask: project %q not found", task.ProjectID)
		}
		return fmt.Errorf("reading task_counter: %w", err)
	}
	counter++

	if _, err := tx.ExecContext(ctx,
		`UPDATE projects SET task_counter = ? WHERE id = ?`, counter, task.ProjectID,
	); err != nil {
		return fmt.Errorf("UPDATE task_counter: %w", err)
	}

	task.ID = uuid.NewString()
	task.Number = counter
	task.Status = "open"
	now := time.Now().UTC()
	task.CreatedAt = now
	task.UpdatedAt = now

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO tasks (id, project_id, number, summary, details, topic, status, deadline, created_at, updated_at, source_kind, source_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.ProjectID, task.Number,
		task.Summary, task.Details, task.Topic, task.Status,
		nullableTime(task.Deadline),
		now.Format(time.RFC3339), now.Format(time.RFC3339),
		task.Source.Kind, task.Source.ID,
	); err != nil {
		return fmt.Errorf("INSERT task %q: %w", task.Summary, err)
	}

	return nil
}

// GetTask returns a task by UUID. Returns nil, nil if not found.
func (s *SQLiteTaskStore) GetTask(ctx context.Context, id string) (*model.Task, error) {
	return s.scanTask(ctx, s.db.QueryRowContext(ctx,
		`SELECT t.id, t.project_id, p.slug, t.number, t.summary, t.details, t.topic,
		        t.status, t.deadline, t.closed_at, t.created_at, t.updated_at, t.source_kind, t.source_id
		 FROM tasks t JOIN projects p ON t.project_id = p.id
		 WHERE t.id = ?`, id,
	), id)
}

// GetTaskTx reads a task by UUID within the given transaction.
func (s *SQLiteTaskStore) GetTaskTx(ctx context.Context, tx *sql.Tx, id string) (*model.Task, error) {
	return s.scanTask(ctx, tx.QueryRowContext(ctx,
		`SELECT t.id, t.project_id, p.slug, t.number, t.summary, t.details, t.topic,
		        t.status, t.deadline, t.closed_at, t.created_at, t.updated_at, t.source_kind, t.source_id
		 FROM tasks t JOIN projects p ON t.project_id = p.id
		 WHERE t.id = ?`, id,
	), id)
}

// GetTaskByRef finds a task by its human-readable reference (project slug + number).
// Returns nil, nil if not found.
func (s *SQLiteTaskStore) GetTaskByRef(ctx context.Context, projectSlug string, number int) (*model.Task, error) {
	ref := fmt.Sprintf("%s#%d", projectSlug, number)
	return s.scanTask(ctx, s.db.QueryRowContext(ctx,
		`SELECT t.id, t.project_id, p.slug, t.number, t.summary, t.details, t.topic,
		        t.status, t.deadline, t.closed_at, t.created_at, t.updated_at, t.source_kind, t.source_id
		 FROM tasks t JOIN projects p ON t.project_id = p.id
		 WHERE p.slug = ? AND t.number = ?`, projectSlug, number,
	), ref)
}

// scanTask scans a task row from a SELECT query with a JOIN on projects.
func (s *SQLiteTaskStore) scanTask(ctx context.Context, row *sql.Row, ref any) (*model.Task, error) {
	var task model.Task
	var createdAtStr, updatedAtStr string
	var deadlineStr, closedAtStr *string

	err := row.Scan(
		&task.ID, &task.ProjectID, &task.ProjectSlug, &task.Number,
		&task.Summary, &task.Details, &task.Topic, &task.Status,
		&deadlineStr, &closedAtStr,
		&createdAtStr, &updatedAtStr,
		&task.Source.Kind, &task.Source.ID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting task %v: %w", ref, err)
	}

	task.CreatedAt, err = time.Parse(time.RFC3339, createdAtStr)
	if err != nil {
		return nil, fmt.Errorf("parsing created_at for task %v: %w", ref, err)
	}
	task.UpdatedAt, err = time.Parse(time.RFC3339, updatedAtStr)
	if err != nil {
		return nil, fmt.Errorf("parsing updated_at for task %v: %w", ref, err)
	}
	if deadlineStr != nil {
		dl, err := time.Parse(time.RFC3339, *deadlineStr)
		if err != nil {
			return nil, fmt.Errorf("parsing deadline for task %v: %w", ref, err)
		}
		task.Deadline = &dl
	}
	if closedAtStr != nil {
		ca, err := parseClosedAt(*closedAtStr)
		if err != nil {
			return nil, fmt.Errorf("parsing closed_at for task %v: %w", ref, err)
		}
		task.ClosedAt = &ca
	}
	return &task, nil
}

// ListTasks returns tasks matching the filter.
// projectID="" means all projects.
func (s *SQLiteTaskStore) ListTasks(ctx context.Context, projectID string, filter model.TaskFilter) ([]model.Task, error) {
	query := `SELECT t.id, t.project_id, p.slug, t.number, t.summary, t.details, t.topic,
	                 t.status, t.deadline, t.closed_at, t.created_at, t.updated_at, t.source_kind, t.source_id
	          FROM tasks t JOIN projects p ON t.project_id = p.id WHERE 1=1`
	var args []any

	if projectID != "" {
		query += ` AND t.project_id = ?`
		args = append(args, projectID)
	}
	if filter.Status != "" {
		query += ` AND t.status = ?`
		args = append(args, filter.Status)
	}

	if filter.Status == "done" || filter.Status == "cancelled" {
		query += ` ORDER BY t.closed_at DESC, t.created_at DESC`
	} else {
		query += ` ORDER BY p.name, t.created_at`
	}

	if filter.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, filter.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing tasks (project=%q): %w", projectID, err)
	}
	defer rows.Close()

	var tasks []model.Task
	for rows.Next() {
		var task model.Task
		var createdAtStr, updatedAtStr string
		var deadlineStr, closedAtStr *string

		if err := rows.Scan(
			&task.ID, &task.ProjectID, &task.ProjectSlug, &task.Number,
			&task.Summary, &task.Details, &task.Topic, &task.Status,
			&deadlineStr, &closedAtStr,
			&createdAtStr, &updatedAtStr,
			&task.Source.Kind, &task.Source.ID,
		); err != nil {
			return nil, fmt.Errorf("scanning task: %w", err)
		}

		task.CreatedAt, err = time.Parse(time.RFC3339, createdAtStr)
		if err != nil {
			return nil, fmt.Errorf("parsing created_at: %w", err)
		}
		task.UpdatedAt, err = time.Parse(time.RFC3339, updatedAtStr)
		if err != nil {
			return nil, fmt.Errorf("parsing updated_at: %w", err)
		}
		if deadlineStr != nil {
			dl, err := time.Parse(time.RFC3339, *deadlineStr)
			if err != nil {
				return nil, fmt.Errorf("parsing deadline: %w", err)
			}
			task.Deadline = &dl
		}
		if closedAtStr != nil {
			ca, err := parseClosedAt(*closedAtStr)
			if err != nil {
				return nil, fmt.Errorf("parsing closed_at: %w", err)
			}
			task.ClosedAt = &ca
		}

		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating tasks: %w", err)
	}

	if filter.Query != "" {
		tasks = filterByQuery(tasks, filter.Query)
	}

	return tasks, nil
}

// UpdateTaskTx applies changes to a task within the given transaction.
// Returns an error if the task is not found.
func (s *SQLiteTaskStore) UpdateTaskTx(ctx context.Context, tx *sql.Tx, id string, update model.TaskUpdate) error {
	now := time.Now().UTC()
	setClauses := []string{"updated_at = ?"}
	args := []any{now.Format(time.RFC3339)}

	if update.Status != nil {
		setClauses = append(setClauses, "status = ?")
		args = append(args, *update.Status)
		switch *update.Status {
		case "done", "cancelled":
			if update.ClosedAt == nil {
				setClauses = append(setClauses, "closed_at = ?")
				args = append(args, now.Format(time.RFC3339Nano))
			}
		case "open":
			if update.ClosedAt == nil {
				setClauses = append(setClauses, "closed_at = ?")
				args = append(args, nil)
			}
		}
	}
	if update.ClosedAt != nil {
		setClauses = append(setClauses, "closed_at = ?")
		if *update.ClosedAt == nil {
			args = append(args, nil)
		} else {
			args = append(args, (*update.ClosedAt).UTC().Format(time.RFC3339Nano))
		}
	}
	if update.Summary != nil {
		setClauses = append(setClauses, "summary = ?")
		args = append(args, *update.Summary)
	}
	if update.Details != nil {
		setClauses = append(setClauses, "details = ?")
		args = append(args, *update.Details)
	}
	if update.Topic != nil {
		setClauses = append(setClauses, "topic = ?")
		args = append(args, *update.Topic)
	}
	if update.Deadline != nil {
		setClauses = append(setClauses, "deadline = ?")
		if *update.Deadline == nil {
			args = append(args, nil)
		} else {
			args = append(args, (*update.Deadline).Format(time.RFC3339))
		}
	}

	query := "UPDATE tasks SET " + strings.Join(setClauses, ", ") + " WHERE id = ?"
	args = append(args, id)

	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating task %q: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking task update %q: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("task %q not found", id)
	}
	return nil
}

// MoveTaskTx moves a task to another project within the given transaction.
// Atomically increments the target project's task_counter and assigns the task a new number.
// The source project's counter is not rolled back.
func (s *SQLiteTaskStore) MoveTaskTx(ctx context.Context, tx *sql.Tx, taskID, newProjectID string) error {
	var counter int
	if err := tx.QueryRowContext(ctx,
		`SELECT task_counter FROM projects WHERE id = ?`, newProjectID,
	).Scan(&counter); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("MoveTask: target project %q not found", newProjectID)
		}
		return fmt.Errorf("reading task_counter for target project: %w", err)
	}
	counter++

	if _, err := tx.ExecContext(ctx,
		`UPDATE projects SET task_counter = ? WHERE id = ?`, counter, newProjectID,
	); err != nil {
		return fmt.Errorf("UPDATE task_counter: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	result, err := tx.ExecContext(ctx,
		`UPDATE tasks SET project_id = ?, number = ?, updated_at = ? WHERE id = ?`,
		newProjectID, counter, now, taskID,
	)
	if err != nil {
		return fmt.Errorf("moving task %q: %w", taskID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking task move %q: %w", taskID, err)
	}
	if affected == 0 {
		return fmt.Errorf("MoveTask: task %q not found", taskID)
	}

	return nil
}

// nullableTime converts *time.Time to *string for SQLite storage.
func nullableTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.RFC3339)
	return &s
}

// minQueryWordLen is the minimum word length (in runes) for word-based search.
const minQueryWordLen = 4

// filterByQuery filters tasks by query string.
func filterByQuery(tasks []model.Task, query string) []model.Task {
	q := strings.ToLower(query)

	var exact []model.Task
	for _, t := range tasks {
		if strings.Contains(strings.ToLower(t.Summary), q) {
			exact = append(exact, t)
		}
	}
	if len(exact) > 0 {
		return exact
	}

	words := strings.Fields(q)
	var significant []string
	for _, w := range words {
		if utf8.RuneCountInString(w) >= minQueryWordLen {
			significant = append(significant, w)
		}
	}
	if len(significant) == 0 {
		return nil
	}

	var result []model.Task
	for _, t := range tasks {
		lower := strings.ToLower(t.Summary)
		for _, w := range significant {
			if strings.Contains(lower, w) {
				result = append(result, t)
				break
			}
		}
	}
	return result
}

// parseClosedAt parses the closed_at string, supporting both RFC3339Nano and RFC3339.
func parseClosedAt(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}
