-- +goose Up

CREATE TABLE voice_queue_new (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    source_kind     TEXT    NOT NULL,                  -- 'telegram' | 'client'
    payload         TEXT    NOT NULL,                  -- file_id (telegram) | rel-path (client)
    mime_type       TEXT    NOT NULL,
    duration_ms     INTEGER NOT NULL,
    received_at     INTEGER NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL,
    last_error      TEXT,
    chat_id         INTEGER,                            -- nullable, telegram-only
    message_id      INTEGER,                            -- nullable, telegram-only
    device_id       TEXT,                               -- nullable, client-only
    client_job_id   TEXT,                               -- nullable; UUID for client jobs
    project_id      TEXT                                -- nullable; project context hint (client-only)
);

INSERT INTO voice_queue_new
    (id, source_kind, payload, mime_type, duration_ms, received_at,
     attempts, next_attempt_at, last_error, chat_id, message_id, device_id,
     client_job_id, project_id)
SELECT
    id, 'telegram', file_id, mime_type, duration_ms, received_at,
    attempts, next_attempt_at, last_error, chat_id, message_id, NULL, NULL, NULL
FROM voice_queue;

DROP INDEX IF EXISTS idx_voice_queue_pending;
DROP TABLE voice_queue;
ALTER TABLE voice_queue_new RENAME TO voice_queue;

CREATE INDEX idx_voice_queue_pending ON voice_queue(next_attempt_at);

-- Partial UNIQUE to deduplicate Telegram voice messages (replaces the old UNIQUE(chat_id, message_id)).
-- Client-side dedup is not needed: payload contains a fresh UUID so collisions are impossible;
-- duplicates are caught at the Idempotency-Key middleware level.
CREATE UNIQUE INDEX idx_voice_queue_telegram_uniq
    ON voice_queue(chat_id, message_id) WHERE source_kind = 'telegram';

-- UUID used by the client to locate its job in SSE events.
CREATE UNIQUE INDEX idx_voice_queue_client_job_id
    ON voice_queue(client_job_id) WHERE client_job_id IS NOT NULL;

-- +goose Down

-- DESTRUCTIVE: client rows are silently dropped on rollback because the old schema
-- requires NOT NULL chat_id/message_id/file_id which client jobs don't have.
DROP INDEX IF EXISTS idx_voice_queue_client_job_id;
DROP INDEX IF EXISTS idx_voice_queue_telegram_uniq;
DROP INDEX IF EXISTS idx_voice_queue_pending;

CREATE TABLE voice_queue_old (
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

INSERT INTO voice_queue_old
    (id, chat_id, message_id, file_id, mime_type, duration_ms, received_at,
     attempts, next_attempt_at, last_error)
SELECT id, chat_id, message_id, payload, mime_type, duration_ms, received_at,
       attempts, next_attempt_at, last_error
FROM voice_queue
WHERE source_kind = 'telegram';

DROP TABLE voice_queue;
ALTER TABLE voice_queue_old RENAME TO voice_queue;
CREATE INDEX idx_voice_queue_pending ON voice_queue(next_attempt_at);
