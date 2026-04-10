-- +goose Up
CREATE TABLE push_queue (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id         TEXT NOT NULL REFERENCES devices(id),
    event_seq         INTEGER NOT NULL REFERENCES events(seq),
    created_at        TEXT NOT NULL,
    attempts          INTEGER NOT NULL DEFAULT 0,
    last_error        TEXT,
    next_attempt_at   TEXT NOT NULL,
    delivered_at      TEXT,
    dropped_at        TEXT,
    dropped_reason    TEXT
);

CREATE INDEX idx_push_queue_pending
    ON push_queue(next_attempt_at)
    WHERE delivered_at IS NULL AND dropped_at IS NULL;
