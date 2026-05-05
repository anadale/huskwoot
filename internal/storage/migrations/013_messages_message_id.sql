-- +goose Up

-- Add message_id column for a stable UUID per message.
-- DEFAULT '' is required for NOT NULL during ALTER TABLE; immediately after,
-- backfill via randomblob assigns a unique hex-32 to each existing row.
ALTER TABLE messages ADD COLUMN message_id TEXT NOT NULL DEFAULT '';
UPDATE messages SET message_id = lower(hex(randomblob(16)));
CREATE UNIQUE INDEX idx_messages_message_id ON messages(message_id);

-- +goose Down
DROP INDEX IF EXISTS idx_messages_message_id;
ALTER TABLE messages DROP COLUMN message_id;
