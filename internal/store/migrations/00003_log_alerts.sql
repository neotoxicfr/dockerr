-- +goose Up
CREATE TABLE log_alerts (
    id         INTEGER PRIMARY KEY,
    pattern    TEXT    NOT NULL, -- text, or /regexp/
    stack      TEXT    NOT NULL DEFAULT '', -- '' = every stack
    created_by TEXT    NOT NULL,
    created_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE log_alerts;
