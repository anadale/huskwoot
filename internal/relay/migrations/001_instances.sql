-- +goose Up
CREATE TABLE instances (
    id            TEXT PRIMARY KEY,
    owner_contact TEXT NOT NULL,
    secret_hash   TEXT NOT NULL,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    disabled_at   DATETIME
);

-- +goose Down
DROP TABLE instances;
