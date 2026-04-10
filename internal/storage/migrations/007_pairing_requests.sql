-- +goose Up

CREATE TABLE pairing_requests (
    id                 TEXT PRIMARY KEY,
    device_name        TEXT NOT NULL,
    platform           TEXT NOT NULL,
    apns_token         TEXT,
    fcm_token          TEXT,
    client_nonce_hash  TEXT NOT NULL,
    csrf_token_hash    TEXT NOT NULL DEFAULT '',
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at         DATETIME NOT NULL,
    confirmed_at       DATETIME,
    issued_device_id   TEXT REFERENCES devices(id)
);

CREATE INDEX idx_pairing_requests_expires ON pairing_requests(expires_at);
