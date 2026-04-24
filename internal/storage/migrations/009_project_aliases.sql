-- +goose Up

CREATE TABLE project_aliases (
    project_id TEXT NOT NULL,
    alias      TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (alias),
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE INDEX idx_project_aliases_project ON project_aliases(project_id);

-- +goose Down

DROP INDEX IF EXISTS idx_project_aliases_project;
DROP TABLE IF EXISTS project_aliases;
