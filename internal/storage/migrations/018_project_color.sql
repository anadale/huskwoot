-- +goose Up
ALTER TABLE projects ADD COLUMN color TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE projects DROP COLUMN color;
