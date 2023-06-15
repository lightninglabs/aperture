-- name: InsertSecret :one
INSERT INTO secrets (
    hash, secret, created_at
) VALUES (
    $1, $2, $3
) RETURNING id;

-- name: GetSecretByHash :one
SELECT secret 
FROM secrets
WHERE hash = $1;

-- name: DeleteSecretByHash :execrows
DELETE FROM secrets
WHERE hash = $1;
