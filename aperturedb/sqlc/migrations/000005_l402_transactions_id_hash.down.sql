DROP INDEX IF EXISTS l402_transactions_identifier_hash_idx;

-- SQLite does not support DROP COLUMN directly.
-- For a proper down migration, the table would need to be recreated.
-- This migration only drops the index; the column remains but is unused.
