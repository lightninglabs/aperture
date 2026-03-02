ALTER TABLE l402_transactions ADD COLUMN identifier_hash BLOB;

CREATE INDEX IF NOT EXISTS l402_transactions_identifier_hash_idx
    ON l402_transactions(identifier_hash);
