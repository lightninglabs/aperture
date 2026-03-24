CREATE TABLE IF NOT EXISTS services (
    id           INTEGER PRIMARY KEY,
    name         TEXT NOT NULL UNIQUE,
    address      TEXT NOT NULL,
    protocol     TEXT NOT NULL DEFAULT 'http',
    host_regexp  TEXT NOT NULL DEFAULT '.*',
    path_regexp  TEXT NOT NULL DEFAULT '',
    price        BIGINT NOT NULL DEFAULT 0,
    auth         TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMP NOT NULL,
    updated_at   TIMESTAMP NOT NULL
);
