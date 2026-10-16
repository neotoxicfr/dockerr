-- +goose Up
CREATE TABLE versions (
    id         INTEGER PRIMARY KEY,
    stack      TEXT    NOT NULL,
    file       TEXT    NOT NULL, -- "compose" or "env"
    content    TEXT    NOT NULL,
    author     TEXT    NOT NULL,
    note       TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
CREATE INDEX versions_stack ON versions (stack, file, id DESC);

CREATE TABLE prefs (
    user  TEXT NOT NULL,
    key   TEXT NOT NULL,
    value TEXT NOT NULL,
    PRIMARY KEY (user, key)
);

CREATE TABLE exec_log (
    id         INTEGER PRIMARY KEY,
    user       TEXT    NOT NULL,
    container  TEXT    NOT NULL,
    cmd        TEXT    NOT NULL,
    started_at INTEGER NOT NULL,
    ended_at   INTEGER
);

-- +goose Down
DROP TABLE exec_log;
DROP TABLE prefs;
DROP TABLE versions;
