-- +goose Up
CREATE TABLE operations (
    id         TEXT    PRIMARY KEY,
    host       TEXT    NOT NULL DEFAULT '', -- Docker host, '' = the local one
    stack      TEXT    NOT NULL,
    service    TEXT    NOT NULL DEFAULT '',
    kind       TEXT    NOT NULL,
    user       TEXT    NOT NULL,
    started_at INTEGER NOT NULL,
    ended_at   INTEGER NOT NULL,
    err        TEXT    NOT NULL DEFAULT '',
    tasks      TEXT    NOT NULL DEFAULT '[]' -- JSON: compose progress, in order
);
CREATE INDEX operations_started ON operations (started_at DESC);
CREATE INDEX operations_stack ON operations (stack, started_at DESC);

-- +goose Down
DROP TABLE operations;
