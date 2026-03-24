-- Add a partial index on settled_at for revenue and date-range stats queries
-- that filter on state = 'settled'. This avoids full table scans as the
-- transactions table grows.
CREATE INDEX IF NOT EXISTS l402_transactions_settled_at_idx
    ON l402_transactions(settled_at)
    WHERE state = 'settled';

-- Add a unique index on token_id to enforce uniqueness and improve lookup
-- performance for RevokeToken and DeleteByTokenID operations.
CREATE UNIQUE INDEX IF NOT EXISTS l402_transactions_token_id_idx
    ON l402_transactions(token_id);
