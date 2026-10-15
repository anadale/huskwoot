-- +goose Up
CREATE TABLE auto_task_inputs (
    input_key TEXT PRIMARY KEY,
    created_at TEXT NOT NULL
);

CREATE TABLE auto_task_candidates (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    status TEXT NOT NULL CHECK (status IN ('pending', 'created', 'rejected', 'duplicate')),
    payload TEXT NOT NULL,
    created_at TEXT NOT NULL,
    question_chat_id INTEGER NOT NULL DEFAULT 0,
    question_message_id INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_auto_task_candidates_project_status ON auto_task_candidates(project_id, status);
CREATE INDEX idx_auto_task_candidates_unsent ON auto_task_candidates(created_at, id)
    WHERE status = 'pending' AND question_message_id = 0;

-- +goose Down
DROP TABLE auto_task_candidates;
DROP TABLE auto_task_inputs;
