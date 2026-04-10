-- +goose Up

CREATE TABLE guard_pending (
    chat_id        INTEGER PRIMARY KEY,
    welcome_msg_id INTEGER NOT NULL,
    deadline       INTEGER NOT NULL  -- Unix timestamp
);

-- +goose Down

DROP TABLE IF EXISTS guard_pending;
