-- name: InsertSession :exec
INSERT INTO lnc_sessions (
    passphrase_words, passphrase_entropy, local_static_priv_key, mailbox_addr,
    created_at, expiry, dev_server
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
);

-- name: GetSession :one
SELECT *
FROM lnc_sessions
WHERE passphrase_entropy = $1;

-- name: SetRemotePubKey :exec
UPDATE lnc_sessions
SET remote_static_pub_key=$1
WHERE passphrase_entropy=$2;

-- name: SetExpiry :exec
UPDATE lnc_sessions
SET expiry=$1
WHERE passphrase_entropy=$2;
