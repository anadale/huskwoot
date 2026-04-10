-- +goose Up
CREATE TABLE devices (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    platform      TEXT NOT NULL,
    token_hash    TEXT NOT NULL,
    apns_token    TEXT,
    fcm_token     TEXT,
    created_at    TEXT NOT NULL,
    last_seen_at  TEXT,
    revoked_at    TEXT
);

CREATE UNIQUE INDEX idx_devices_token_hash_active
    ON devices(token_hash) WHERE revoked_at IS NULL;
