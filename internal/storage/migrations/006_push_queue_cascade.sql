-- +goose Up
-- Пересоздаём push_queue с ON DELETE CASCADE для event_seq: retention удаляет
-- старые события, и любые pending push-задания, ссылающиеся на них, должны
-- удаляться каскадом. Иначе FK-ограничение блокирует events.DeleteOlderThan,
-- и события копятся на диске бессрочно.

CREATE TABLE push_queue_new (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id         TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    event_seq         INTEGER NOT NULL REFERENCES events(seq) ON DELETE CASCADE,
    created_at        TEXT NOT NULL,
    attempts          INTEGER NOT NULL DEFAULT 0,
    last_error        TEXT,
    next_attempt_at   TEXT NOT NULL,
    delivered_at      TEXT,
    dropped_at        TEXT,
    dropped_reason    TEXT
);

INSERT INTO push_queue_new
    (id, device_id, event_seq, created_at, attempts, last_error,
     next_attempt_at, delivered_at, dropped_at, dropped_reason)
SELECT id, device_id, event_seq, created_at, attempts, last_error,
       next_attempt_at, delivered_at, dropped_at, dropped_reason
FROM push_queue;

DROP TABLE push_queue;
ALTER TABLE push_queue_new RENAME TO push_queue;

CREATE INDEX idx_push_queue_pending
    ON push_queue(next_attempt_at)
    WHERE delivered_at IS NULL AND dropped_at IS NULL;
