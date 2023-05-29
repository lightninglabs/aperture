-- lnc_sessions is table used to store data about LNC sesssions.
CREATE TABLE IF NOT EXISTS lnc_sessions (
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

CREATE INDEX IF NOT EXISTS lnc_sessions_passphrase_entropy_idx ON lnc_sessions(passphrase_entropy);
