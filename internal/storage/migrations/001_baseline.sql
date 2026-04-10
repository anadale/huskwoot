-- +goose Up
CREATE TABLE IF NOT EXISTS cursors (
    channel_id TEXT    PRIMARY KEY,
    message_id TEXT    NOT NULL,
    folder_id  TEXT    NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS channel_projects (
    channel_id TEXT    PRIMARY KEY,
    project_id INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS messages (
    id          INTEGER PRIMARY KEY,
    source_id   TEXT    NOT NULL,
    author_name TEXT    NOT NULL,
    text        TEXT    NOT NULL,
    timestamp   INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_messages_source_time
    ON messages(source_id, timestamp DESC);

CREATE TABLE IF NOT EXISTS projects (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tasks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  INTEGER NOT NULL REFERENCES projects(id),
    summary     TEXT NOT NULL,
    details     TEXT NOT NULL DEFAULT '',
    topic       TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'open',
    deadline    TEXT,
    closed_at   TEXT,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    source_kind TEXT NOT NULL DEFAULT '',
    source_id   TEXT NOT NULL DEFAULT ''
);
