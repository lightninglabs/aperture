CREATE TABLE IF NOT EXISTS l402_transactions (
    id            INTEGER PRIMARY KEY,
    token_id      BLOB NOT NULL,
    payment_hash  BLOB NOT NULL,
    service_name  TEXT NOT NULL,
    price_sats    INTEGER NOT NULL,
    state         TEXT NOT NULL DEFAULT 'pending',
    created_at    TIMESTAMP NOT NULL,
    settled_at    TIMESTAMP
);

CREATE INDEX IF NOT EXISTS l402_transactions_payment_hash_idx ON l402_transactions(payment_hash);
CREATE INDEX IF NOT EXISTS l402_transactions_service_name_idx ON l402_transactions(service_name);
CREATE INDEX IF NOT EXISTS l402_transactions_state_idx ON l402_transactions(state);
CREATE INDEX IF NOT EXISTS l402_transactions_created_at_idx ON l402_transactions(created_at);
