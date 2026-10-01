CREATE TABLE IF NOT EXISTS mpp_consumed_charges (
    id INTEGER PRIMARY KEY,

    -- payment_hash is the hash of the Lightning payment that bought the one
    -- request this row records. It is the consumption key rather than the
    -- challenge ID because it names the money rather than the offer: a
    -- settled invoice stays settled forever, so the hash is the thing that
    -- must be spendable only once.
    payment_hash BLOB NOT NULL UNIQUE,

    -- challenge_id is the HMAC-bound ID of the challenge the credential
    -- echoed. It is recorded so an operator can tie a refusal back to the
    -- 402 that offered the invoice, and is deliberately not the key.
    challenge_id TEXT NOT NULL,

    -- expires_at is the expiry the challenge carried. A credential whose
    -- challenge has expired is refused whether or not this row exists, so
    -- the row is dead weight past this instant and pruning may drop it.
    -- This is what keeps the table bounded on a public proxy.
    expires_at TIMESTAMP NOT NULL,

    -- consumed_at is the time the request was authorized.
    consumed_at TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS mpp_consumed_charges_expires_at_idx
    ON mpp_consumed_charges (expires_at);
