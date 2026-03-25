CREATE TABLE l402_transactions (
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

CREATE INDEX l402_transactions_auth_type_idx ON l402_transactions(auth_type);

CREATE INDEX l402_transactions_created_at_idx ON l402_transactions(created_at);

CREATE UNIQUE INDEX l402_transactions_identifier_hash_idx
    ON l402_transactions(identifier_hash)
    WHERE identifier_hash IS NOT NULL;

CREATE INDEX l402_transactions_payment_hash_idx ON l402_transactions(payment_hash);

CREATE INDEX l402_transactions_service_name_idx ON l402_transactions(service_name);

CREATE INDEX l402_transactions_settled_at_idx
    ON l402_transactions(settled_at)
    WHERE state = 'settled';

CREATE INDEX l402_transactions_state_idx ON l402_transactions(state);

CREATE UNIQUE INDEX l402_transactions_token_id_idx
    ON l402_transactions(token_id)
    WHERE token_id IS NOT NULL;

CREATE TABLE lnc_sessions (
    id INTEGER PRIMARY KEY,

    -- The passphrase words used to derive the passphrase entropy.
    passphrase_words TEXT NOT NULL UNIQUE,

    -- The entropy bytes to be used for mask the local ephemeral key during the 
    -- first step of the Noise XX handshake.
    passphrase_entropy BLOB NOT NULL UNIQUE,

    -- The remote static key being used for the connection.
    remote_static_pub_key BLOB UNIQUE,

    -- The local static key being used for the connection.
    local_static_priv_key BLOB NOT NULL,

    -- mailbox_addr is the address of the mailbox used for the session.
    mailbox_addr TEXT NOT NULL,

    -- created_at is the time the session was created.
    created_at TIMESTAMP NOT NULL,

    -- expiry is the time the session will expire.
    expiry TIMESTAMP,

    -- dev_server signals if we need to skip the verification of the server's 
    -- tls certificate.
    dev_server BOOL NOT NULL
);

CREATE INDEX lnc_sessions_passphrase_entropy_idx ON lnc_sessions(passphrase_entropy);

CREATE TABLE mpp_sessions (
    id INTEGER PRIMARY KEY,
    session_id TEXT UNIQUE NOT NULL,
    payment_hash BLOB NOT NULL,
    deposit_sats BIGINT NOT NULL DEFAULT 0,
    spent_sats BIGINT NOT NULL DEFAULT 0,
    return_invoice TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'open',
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

CREATE INDEX mpp_sessions_session_id_idx ON mpp_sessions (session_id);

CREATE TABLE onion (
    private_key BLOB UNIQUE NOT NULL,
    created_at TIMESTAMP NOT NULL 
);

CREATE TABLE secrets (
    id INTEGER PRIMARY KEY,
    hash BLOB UNIQUE NOT NULL,
    secret BLOB UNIQUE NOT NULL,
    created_at TIMESTAMP NOT NULL 
);

CREATE INDEX secrets_hash_idx ON secrets (hash);

CREATE TABLE services (
    id           INTEGER PRIMARY KEY,
    name         TEXT NOT NULL UNIQUE,
    address      TEXT NOT NULL,
    protocol     TEXT NOT NULL DEFAULT 'http',
    host_regexp  TEXT NOT NULL DEFAULT '.*',
    path_regexp  TEXT NOT NULL DEFAULT '',
    price        BIGINT NOT NULL DEFAULT 0,
    auth         TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMP NOT NULL,
    updated_at   TIMESTAMP NOT NULL
, auth_scheme TEXT NOT NULL DEFAULT 'l402');

