-- name: InsertL402Transaction :one
INSERT INTO l402_transactions (
    token_id, payment_hash, service_name, price_sats, state, created_at,
    identifier_hash
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
) RETURNING id;

-- name: UpdateL402TransactionState :exec
UPDATE l402_transactions
SET state = $1, settled_at = $2
WHERE payment_hash = $3;

-- name: GetL402TransactionsByPaymentHash :many
SELECT *
FROM l402_transactions
WHERE payment_hash = $1;

-- name: ListL402Transactions :many
SELECT *
FROM l402_transactions
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- name: ListL402TransactionsByService :many
SELECT *
FROM l402_transactions
WHERE service_name = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: ListL402TransactionsByState :many
SELECT *
FROM l402_transactions
WHERE state = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: ListL402TransactionsByDateRange :many
SELECT *
FROM l402_transactions
WHERE created_at >= $1 AND created_at <= $2
ORDER BY created_at DESC
LIMIT $3 OFFSET $4;

-- name: CountL402Transactions :one
SELECT count(*) FROM l402_transactions;

-- name: CountL402TransactionsByService :one
SELECT count(*)
FROM l402_transactions
WHERE service_name = $1;

-- name: GetL402RevenueByService :many
SELECT service_name, COALESCE(SUM(price_sats), 0) AS total_revenue
FROM l402_transactions
WHERE state = 'settled'
GROUP BY service_name;

-- name: GetL402RevenueByServiceAndDateRange :many
SELECT service_name, COALESCE(SUM(price_sats), 0) AS total_revenue
FROM l402_transactions
WHERE state = 'settled' AND created_at >= $1 AND created_at <= $2
GROUP BY service_name;

-- name: GetL402TotalRevenue :one
SELECT COALESCE(SUM(price_sats), 0) AS total_revenue
FROM l402_transactions
WHERE state = 'settled';

-- name: GetL402TransactionByIdentifierHash :one
SELECT *
FROM l402_transactions
WHERE identifier_hash = $1;

-- name: DeleteL402TransactionByTokenID :execrows
DELETE FROM l402_transactions
WHERE token_id = $1;
