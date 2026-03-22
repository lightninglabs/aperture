-- name: InsertMPPSession :one
INSERT INTO mpp_sessions (
    session_id, payment_hash, deposit_sats, spent_sats,
    return_invoice, status, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
) RETURNING id;

-- name: GetMPPSessionByID :one
SELECT *
FROM mpp_sessions
WHERE session_id = $1;

-- name: UpdateMPPSessionDeposit :execresult
UPDATE mpp_sessions
SET deposit_sats = deposit_sats + $1, updated_at = $2
WHERE session_id = $3 AND status = 'open';

-- name: UpdateMPPSessionSpent :execresult
UPDATE mpp_sessions
SET spent_sats = spent_sats + $1, updated_at = $2
WHERE session_id = $3
  AND status = 'open'
  AND deposit_sats - spent_sats >= $1;

-- name: CloseMPPSession :execresult
UPDATE mpp_sessions
SET status = 'closed', updated_at = $1
WHERE session_id = $2 AND status = 'open';
