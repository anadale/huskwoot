-- +goose Up

CREATE TABLE forward_buffer (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id          TEXT    NOT NULL,
    message_id       TEXT    NOT NULL,
    forward_from_id  TEXT,
    forward_from_name TEXT,
    is_self_authored INTEGER NOT NULL,
    original_date    INTEGER NOT NULL,
    text             TEXT    NOT NULL DEFAULT '',
    received_at      INTEGER NOT NULL,
    UNIQUE(chat_id, message_id)
);

CREATE INDEX idx_forward_buffer_chat_received
    ON forward_buffer(chat_id, received_at);

-- +goose Down

DROP INDEX IF EXISTS idx_forward_buffer_chat_received;
DROP TABLE IF EXISTS forward_buffer;
