-- name: InsertMPPConsumedCharge :execresult
INSERT INTO mpp_consumed_charges (
    payment_hash, challenge_id, expires_at, consumed_at
) VALUES (
    $1, $2, $3, $4
) ON CONFLICT (payment_hash) DO NOTHING;

-- name: GetMPPConsumedCharge :one
SELECT *
FROM mpp_consumed_charges
WHERE payment_hash = $1;

-- name: DeleteExpiredMPPConsumedCharges :execresult
DELETE FROM mpp_consumed_charges
WHERE expires_at < $1;
