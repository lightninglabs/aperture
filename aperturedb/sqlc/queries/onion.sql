-- name: UpsertOnion :exec
INSERT INTO onion (
    private_key, created_at
) VALUES (
    $1, $2
) ON CONFLICT (
    private_key
) DO NOTHING;

-- name: SelectOnionPrivateKey :one
SELECT private_key 
FROM onion 
LIMIT 1;

-- name: DeleteOnionPrivateKey :exec
DELETE FROM onion;
