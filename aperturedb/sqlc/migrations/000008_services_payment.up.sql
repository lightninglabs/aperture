-- Optional per-service lnd override for multi-merchant deployments.
-- When any of these columns are non-empty, invoices for this service are
-- routed through the merchant's own lnd instead of the global gateway
-- lnd, so payments land directly in the merchant's wallet.
--
-- All three columns are expected to be set together (lndhost + tlspath +
-- macpath); enforced at the admin-server layer rather than here so we
-- can give a friendlier error than a constraint violation.
ALTER TABLE services ADD COLUMN payment_lndhost TEXT NOT NULL DEFAULT '';
ALTER TABLE services ADD COLUMN payment_tlspath TEXT NOT NULL DEFAULT '';
ALTER TABLE services ADD COLUMN payment_macpath TEXT NOT NULL DEFAULT '';
