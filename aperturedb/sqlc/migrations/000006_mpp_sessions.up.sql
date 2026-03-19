CREATE TABLE IF NOT EXISTS mpp_sessions (
    id INTEGER PRIMARY KEY,
    session_id TEXT UNIQUE NOT NULL,
    payment_hash BLOB NOT NULL,
    deposit_sats BIGINT NOT NULL DEFAULT 0,
    spent_sats BIGINT NOT NULL DEFAULT 0,
    return_invoice TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'open',
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS mpp_sessions_session_id_idx ON mpp_sessions (session_id);
