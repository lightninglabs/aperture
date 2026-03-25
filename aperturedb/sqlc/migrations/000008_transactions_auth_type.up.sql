-- Add auth_type column and make token_id nullable for MPP transactions.
-- SQLite does not support ALTER COLUMN, so we recreate the table.

ALTER TABLE l402_transactions RENAME TO l402_transactions_old;

CREATE TABLE IF NOT EXISTS l402_transactions (
    id               INTEGER PRIMARY KEY,
    token_id         BLOB,
    payment_hash     BLOB NOT NULL,
    identifier_hash  BLOB,
    service_name     TEXT NOT NULL,
    price_sats       BIGINT NOT NULL,
    state            TEXT NOT NULL DEFAULT 'pending',
    auth_type        TEXT NOT NULL DEFAULT 'l402',
    created_at       TIMESTAMP NOT NULL,
    settled_at       TIMESTAMP
);

INSERT INTO l402_transactions (
    id, token_id, payment_hash, identifier_hash, service_name,
    price_sats, state, auth_type, created_at, settled_at
)
SELECT
    id, token_id, payment_hash, identifier_hash, service_name,
    price_sats, state, 'l402', created_at, settled_at
FROM l402_transactions_old;

DROP TABLE l402_transactions_old;

CREATE INDEX IF NOT EXISTS l402_transactions_payment_hash_idx ON l402_transactions(payment_hash);
CREATE INDEX IF NOT EXISTS l402_transactions_service_name_idx ON l402_transactions(service_name);
CREATE INDEX IF NOT EXISTS l402_transactions_state_idx ON l402_transactions(state);
CREATE INDEX IF NOT EXISTS l402_transactions_created_at_idx ON l402_transactions(created_at);
CREATE INDEX IF NOT EXISTS l402_transactions_auth_type_idx ON l402_transactions(auth_type);

CREATE UNIQUE INDEX IF NOT EXISTS l402_transactions_identifier_hash_idx
    ON l402_transactions(identifier_hash)
    WHERE identifier_hash IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS l402_transactions_token_id_idx
    ON l402_transactions(token_id)
    WHERE token_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS l402_transactions_settled_at_idx
    ON l402_transactions(settled_at)
    WHERE state = 'settled';
