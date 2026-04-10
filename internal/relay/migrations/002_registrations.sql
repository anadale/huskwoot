-- +goose Up
CREATE TABLE registrations (
    instance_id  TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    device_id    TEXT NOT NULL,
    apns_token   TEXT,
    fcm_token    TEXT,
    platform     TEXT NOT NULL,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME,
    PRIMARY KEY (instance_id, device_id)
);
CREATE INDEX idx_registrations_last_used ON registrations(last_used_at);

-- +goose Down
DROP INDEX IF EXISTS idx_registrations_last_used;
DROP TABLE registrations;
