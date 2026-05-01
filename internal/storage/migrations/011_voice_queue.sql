-- +goose Up

CREATE TABLE voice_queue (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id         INTEGER NOT NULL,
    message_id      INTEGER NOT NULL,
    file_id         TEXT    NOT NULL,
    mime_type       TEXT    NOT NULL,
    duration_ms     INTEGER NOT NULL,
    received_at     INTEGER NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL,
    last_error      TEXT,
    UNIQUE(chat_id, message_id)
);
CREATE INDEX idx_voice_queue_pending ON voice_queue(next_attempt_at);

-- +goose Down
DROP INDEX IF EXISTS idx_voice_queue_pending;
DROP TABLE IF EXISTS voice_queue;
