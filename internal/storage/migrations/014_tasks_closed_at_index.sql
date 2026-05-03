-- +goose Up
CREATE INDEX IF NOT EXISTS idx_tasks_status_closed_at
    ON tasks(status, closed_at);

-- +goose Down
DROP INDEX IF EXISTS idx_tasks_status_closed_at;
