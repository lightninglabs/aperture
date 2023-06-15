CREATE TABLE IF NOT EXISTS secrets (
    id INTEGER PRIMARY KEY,
    hash BLOB UNIQUE NOT NULL,
    secret BLOB UNIQUE NOT NULL,
    created_at TIMESTAMP NOT NULL 
);

CREATE INDEX IF NOT EXISTS secrets_hash_idx ON secrets (hash);
