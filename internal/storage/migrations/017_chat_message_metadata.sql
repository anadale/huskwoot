-- +goose Up

-- Chat metadata for client conversations. Channel history rows keep the defaults.
ALTER TABLE messages ADD COLUMN role TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN kind TEXT NOT NULL DEFAULT 'text';
ALTER TABLE messages ADD COLUMN audio_job_id TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN audio_duration_ms INTEGER NOT NULL DEFAULT 0;
-- JSON array of task UUIDs touched by an agent reply; '' means none.
ALTER TABLE messages ADD COLUMN task_ids TEXT NOT NULL DEFAULT '';

-- Existing client chat rows: author_name is "agent" for agent replies and the device id otherwise.
UPDATE messages
SET role = CASE WHEN author_name = 'agent' THEN 'agent' ELSE 'user' END
WHERE source_id LIKE 'client:%';

-- +goose Down
ALTER TABLE messages DROP COLUMN task_ids;
ALTER TABLE messages DROP COLUMN audio_duration_ms;
ALTER TABLE messages DROP COLUMN audio_job_id;
ALTER TABLE messages DROP COLUMN kind;
ALTER TABLE messages DROP COLUMN role;
