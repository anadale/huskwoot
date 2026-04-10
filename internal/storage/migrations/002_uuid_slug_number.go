package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upUUIDSlugNumber, nil)
}

func upUUIDSlugNumber(ctx context.Context, tx *sql.Tx) error {
	schema := []string{
		`CREATE TABLE projects_new (
			id           TEXT PRIMARY KEY,
			name         TEXT NOT NULL UNIQUE,
			slug         TEXT NOT NULL UNIQUE,
			description  TEXT NOT NULL DEFAULT '',
			task_counter INTEGER NOT NULL DEFAULT 0,
			created_at   TEXT NOT NULL
		)`,
		`CREATE TABLE tasks_new (
			id          TEXT PRIMARY KEY,
			project_id  TEXT NOT NULL REFERENCES projects_new(id),
			number      INTEGER NOT NULL,
			summary     TEXT NOT NULL,
			details     TEXT NOT NULL DEFAULT '',
			topic       TEXT NOT NULL DEFAULT '',
			status      TEXT NOT NULL DEFAULT 'open',
			deadline    TEXT,
			closed_at   TEXT,
			created_at  TEXT NOT NULL,
			updated_at  TEXT NOT NULL,
			source_kind TEXT NOT NULL DEFAULT '',
			source_id   TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE channel_projects_new (
			channel_id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL
		)`,
	}
	for _, s := range schema {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("creating table: %w", err)
		}
	}

	rows, err := tx.QueryContext(ctx, `SELECT id, name, description, created_at FROM projects ORDER BY id`)
	if err != nil {
		return fmt.Errorf("reading projects: %w", err)
	}
	type proj struct {
		oldID int64
		newID string
	}
	var projects []proj
	usedSlugs := map[string]int{}
	for rows.Next() {
		var (
			oldID                  int64
			name, descr, createdAt string
		)
		if err := rows.Scan(&oldID, &name, &descr, &createdAt); err != nil {
			rows.Close()
			return fmt.Errorf("scanning projects: %w", err)
		}
		slug := uniqueSlugFrozen(name, usedSlugs)
		newID := uuid.NewString()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO projects_new(id, name, slug, description, task_counter, created_at) VALUES (?, ?, ?, ?, 0, ?)`,
			newID, name, slug, descr, createdAt,
		); err != nil {
			rows.Close()
			return fmt.Errorf("insert projects_new: %w", err)
		}
		projects = append(projects, proj{oldID: oldID, newID: newID})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating projects: %w", err)
	}

	pidMap := make(map[int64]string, len(projects))
	for _, p := range projects {
		pidMap[p.oldID] = p.newID
	}

	counters := make(map[string]int, len(projects))
	rows, err = tx.QueryContext(ctx,
		`SELECT id, project_id, summary, details, topic, status, deadline, closed_at, created_at, updated_at, source_kind, source_id
		 FROM tasks ORDER BY project_id, created_at, id`)
	if err != nil {
		return fmt.Errorf("reading tasks: %w", err)
	}
	for rows.Next() {
		var (
			oldID                                      int64
			oldPID                                     int64
			summary, details, topic, status            string
			createdAt, updatedAt, sourceKind, sourceID string
			deadline, closedAt                         sql.NullString
		)
		if err := rows.Scan(
			&oldID, &oldPID, &summary, &details, &topic, &status,
			&deadline, &closedAt, &createdAt, &updatedAt, &sourceKind, &sourceID,
		); err != nil {
			rows.Close()
			return fmt.Errorf("scanning tasks: %w", err)
		}
		newPID, ok := pidMap[oldPID]
		if !ok {
			rows.Close()
			return fmt.Errorf("orphaned task %d, project_id=%d not found", oldID, oldPID)
		}
		counters[newPID]++
		n := counters[newPID]
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO tasks_new(id, project_id, number, summary, details, topic, status, deadline, closed_at, created_at, updated_at, source_kind, source_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			uuid.NewString(), newPID, n, summary, details, topic, status,
			deadline, closedAt, createdAt, updatedAt, sourceKind, sourceID,
		); err != nil {
			rows.Close()
			return fmt.Errorf("insert tasks_new: %w", err)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating tasks: %w", err)
	}

	for pid, c := range counters {
		if _, err := tx.ExecContext(ctx, `UPDATE projects_new SET task_counter = ? WHERE id = ?`, c, pid); err != nil {
			return fmt.Errorf("update task_counter: %w", err)
		}
	}

	rows, err = tx.QueryContext(ctx, `SELECT channel_id, project_id FROM channel_projects`)
	if err != nil {
		return fmt.Errorf("reading channel_projects: %w", err)
	}
	for rows.Next() {
		var (
			channelID string
			oldPID    int64
		)
		if err := rows.Scan(&channelID, &oldPID); err != nil {
			rows.Close()
			return fmt.Errorf("scanning channel_projects: %w", err)
		}
		newPID, ok := pidMap[oldPID]
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO channel_projects_new(channel_id, project_id) VALUES (?, ?)`,
			channelID, newPID,
		); err != nil {
			rows.Close()
			return fmt.Errorf("insert channel_projects_new: %w", err)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating channel_projects: %w", err)
	}

	drops := []string{
		`DROP TABLE channel_projects`,
		`DROP TABLE tasks`,
		`DROP TABLE projects`,
	}
	for _, s := range drops {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("drop: %w", err)
		}
	}
	renames := []string{
		`ALTER TABLE projects_new RENAME TO projects`,
		`ALTER TABLE tasks_new RENAME TO tasks`,
		`ALTER TABLE channel_projects_new RENAME TO channel_projects`,
		`CREATE UNIQUE INDEX uniq_tasks_project_number ON tasks(project_id, number)`,
		`CREATE INDEX idx_tasks_project_status ON tasks(project_id, status)`,
	}
	for _, s := range renames {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("rename/index: %w", err)
		}
	}

	return nil
}

var translitFrozen = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "yo",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "h", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

func slugifyFrozen(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case unicode.IsDigit(r) || (r >= 'a' && r <= 'z'):
			b.WriteRune(r)
		case translitFrozen[r] != "":
			b.WriteString(translitFrozen[r])
		default:
			b.WriteByte('-')
		}
	}
	s := b.String()
	var out strings.Builder
	var prev byte
	for i := 0; i < len(s); i++ {
		if s[i] == '-' && prev == '-' {
			continue
		}
		out.WriteByte(s[i])
		prev = s[i]
	}
	s = strings.Trim(out.String(), "-")
	if s == "" {
		return "project"
	}
	return s
}

// uniqueSlugFrozen generates a unique slug, appending a numeric suffix on collision.
func uniqueSlugFrozen(name string, used map[string]int) string {
	base := slugifyFrozen(name)
	s := base
	for {
		if used[s] == 0 {
			break
		}
		used[base]++
		s = fmt.Sprintf("%s-%d", base, used[base])
	}
	used[s] = 1
	return s
}
