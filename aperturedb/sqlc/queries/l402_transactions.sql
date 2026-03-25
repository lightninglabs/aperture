-- name: InsertL402Transaction :one
INSERT INTO l402_transactions (
    token_id, payment_hash, service_name, price_sats, state, created_at,
    identifier_hash
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
) RETURNING id;

-- name: UpdateL402TransactionState :execrows
UPDATE l402_transactions
SET state = $1, settled_at = $2
WHERE payment_hash = $3 AND state = 'pending';

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
WHERE state = 'settled' AND settled_at >= $1 AND settled_at <= $2
ORDER BY settled_at DESC
LIMIT $3 OFFSET $4;

-- name: CountL402Transactions :one
SELECT count(*)
FROM l402_transactions
WHERE state = 'settled';

-- name: CountL402TransactionsByService :one
SELECT count(*)
FROM l402_transactions
WHERE service_name = $1;

-- name: CountL402TransactionsByDateRange :one
SELECT count(*)
FROM l402_transactions
WHERE state = 'settled' AND settled_at >= $1 AND settled_at <= $2;

-- name: GetL402RevenueByService :many
SELECT service_name, CAST(COALESCE(SUM(price_sats), 0) AS BIGINT) AS total_revenue
FROM l402_transactions
WHERE state = 'settled'
GROUP BY service_name;

-- name: GetL402RevenueByServiceAndDateRange :many
SELECT service_name, CAST(COALESCE(SUM(price_sats), 0) AS BIGINT) AS total_revenue
FROM l402_transactions
WHERE state = 'settled' AND settled_at >= $1 AND settled_at <= $2
GROUP BY service_name;

-- name: GetL402TotalRevenue :one
SELECT CAST(COALESCE(SUM(price_sats), 0) AS BIGINT) AS total_revenue
FROM l402_transactions
WHERE state = 'settled';

-- name: GetL402TotalRevenueByDateRange :one
SELECT CAST(COALESCE(SUM(price_sats), 0) AS BIGINT) AS total_revenue
FROM l402_transactions
WHERE state = 'settled' AND settled_at >= $1 AND settled_at <= $2;

-- name: GetL402TransactionByIdentifierHash :one
SELECT *
FROM l402_transactions
WHERE identifier_hash = $1;

-- name: GetL402SettledTransactionByTokenID :one
SELECT *
FROM l402_transactions
WHERE token_id = $1 AND state = 'settled';

-- name: DeleteL402TransactionByTokenID :execrows
DELETE FROM l402_transactions
WHERE token_id = $1;

-- name: ListL402TransactionsFiltered :many
SELECT *
FROM l402_transactions
WHERE (sqlc.arg(filter_service) = '' OR service_name = sqlc.arg(filter_service))
  AND (sqlc.arg(filter_state) = '' OR state = sqlc.arg(filter_state))
  AND (sqlc.arg(has_date_range) = 0 OR settled_at >= sqlc.arg(date_from))
  AND (sqlc.arg(has_date_range) = 0 OR settled_at <= sqlc.arg(date_to))
ORDER BY created_at DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: CountL402TransactionsFiltered :one
SELECT count(*)
FROM l402_transactions
WHERE (sqlc.arg(filter_service) = '' OR service_name = sqlc.arg(filter_service))
  AND (sqlc.arg(filter_state) = '' OR state = sqlc.arg(filter_state))
  AND (sqlc.arg(has_date_range) = 0 OR settled_at >= sqlc.arg(date_from))
  AND (sqlc.arg(has_date_range) = 0 OR settled_at <= sqlc.arg(date_to));
