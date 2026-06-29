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

-- name: CloseMPPSessionReturningBalance :one
UPDATE mpp_sessions
SET status = 'closed', updated_at = $1
WHERE session_id = $2 AND status = 'open'
RETURNING CAST(deposit_sats - spent_sats AS BIGINT);

-- name: ListMPPSessions :many
SELECT *
FROM mpp_sessions
WHERE (sqlc.arg(filter_status) = '' OR status = sqlc.arg(filter_status))
ORDER BY created_at DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: CountMPPSessions :one
SELECT count(*)
FROM mpp_sessions
WHERE (sqlc.arg(filter_status) = '' OR status = sqlc.arg(filter_status));

-- name: GetMPPSessionAggregateStats :one
-- Uses only portable SQL (no COUNT FILTER) so the same query runs against
-- both postgres and sqlite. Casts to BIGINT to give sqlc a consistent
-- int64 type on both drivers.
SELECT
    CAST(COUNT(*) AS BIGINT) AS total_sessions,
    CAST(COALESCE(SUM(
        CASE WHEN status = 'open' THEN 1 ELSE 0 END
    ), 0) AS BIGINT) AS open_sessions,
    CAST(COALESCE(SUM(
        CASE WHEN status = 'closed' THEN 1 ELSE 0 END
    ), 0) AS BIGINT) AS closed_sessions,
    CAST(COALESCE(SUM(deposit_sats), 0) AS BIGINT) AS total_deposit_sats,
    CAST(COALESCE(SUM(spent_sats), 0) AS BIGINT) AS total_spent_sats,
    CAST(COALESCE(SUM(
        CASE WHEN status = 'open' THEN deposit_sats - spent_sats ELSE 0 END
    ), 0) AS BIGINT) AS open_balance_sats
FROM mpp_sessions;
