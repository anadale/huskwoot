-- +goose Up

-- Task priority: 0 none, 1 low, 2 medium, 3 high.
ALTER TABLE tasks ADD COLUMN priority INTEGER NOT NULL DEFAULT 0;
-- JSON snapshot of the message a task was created from; '' means no details.
ALTER TABLE tasks ADD COLUMN source_details TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE tasks DROP COLUMN source_details;
ALTER TABLE tasks DROP COLUMN priority;
