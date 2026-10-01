CREATE TABLE IF NOT EXISTS mpp_session_credits (
    id INTEGER PRIMARY KEY,

    -- payment_hash is the hash of the Lightning payment that funded this
    -- credit, either the deposit that opened the session or a later top-up.
    -- It is unique across every session, which is what makes a credit
    -- once-only: a settled invoice stays settled forever, so without this the
    -- same preimage could be presented again and again.
    payment_hash BLOB NOT NULL UNIQUE,

    -- session_id is the session the payment was credited to. Recording it
    -- means a replay can be told apart from an honest client retrying a
    -- top-up whose response was lost.
    session_id TEXT NOT NULL,

    -- amount_sats is the number of satoshis the payment added to the
    -- session's deposit.
    amount_sats BIGINT NOT NULL,

    -- created_at is the time the credit was applied.
    created_at TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS mpp_session_credits_session_id_idx
    ON mpp_session_credits (session_id);
