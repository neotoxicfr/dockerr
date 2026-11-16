-- +goose Up
CREATE TABLE auto_update (
    stack      TEXT    NOT NULL,
    host       TEXT    NOT NULL DEFAULT '', -- '' = local
    created_by TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (stack, host)
);

-- +goose Down
DROP TABLE auto_update;
