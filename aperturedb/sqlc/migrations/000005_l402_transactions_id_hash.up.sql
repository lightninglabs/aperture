ALTER TABLE l402_transactions ADD COLUMN identifier_hash BLOB;

-- This column is left nullable for backward compatibility with any rows that
-- may predate identifier_hash tracking.
CREATE UNIQUE INDEX IF NOT EXISTS l402_transactions_identifier_hash_idx
    ON l402_transactions(identifier_hash)
    WHERE identifier_hash IS NOT NULL;
