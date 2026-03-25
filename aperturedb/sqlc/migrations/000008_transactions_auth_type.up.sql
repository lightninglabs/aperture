ALTER TABLE l402_transactions ADD COLUMN auth_type TEXT NOT NULL DEFAULT 'l402';
CREATE INDEX IF NOT EXISTS l402_transactions_auth_type_idx ON l402_transactions(auth_type);
