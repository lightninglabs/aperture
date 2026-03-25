CREATE TABLE IF NOT EXISTS l402_transactions (
    id               INTEGER PRIMARY KEY,
    token_id         BLOB NOT NULL,
    payment_hash     BLOB NOT NULL,
    identifier_hash  BLOB,
    service_name     TEXT NOT NULL,
    price_sats       BIGINT NOT NULL,
    state            TEXT NOT NULL DEFAULT 'pending',
    created_at       TIMESTAMP NOT NULL,
    settled_at       TIMESTAMP
);

CREATE INDEX IF NOT EXISTS l402_transactions_payment_hash_idx ON l402_transactions(payment_hash);
CREATE INDEX IF NOT EXISTS l402_transactions_service_name_idx ON l402_transactions(service_name);
CREATE INDEX IF NOT EXISTS l402_transactions_state_idx ON l402_transactions(state);
CREATE INDEX IF NOT EXISTS l402_transactions_created_at_idx ON l402_transactions(created_at);

CREATE UNIQUE INDEX IF NOT EXISTS l402_transactions_identifier_hash_idx
    ON l402_transactions(identifier_hash)
    WHERE identifier_hash IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS l402_transactions_token_id_idx
    ON l402_transactions(token_id);

CREATE INDEX IF NOT EXISTS l402_transactions_settled_at_idx
    ON l402_transactions(settled_at)
    WHERE state = 'settled';
