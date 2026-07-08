-- name: UpsertService :exec
INSERT INTO services (
    name, address, protocol, host_regexp, path_regexp, price, auth,
    auth_scheme, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
)
ON CONFLICT(name) DO UPDATE SET
    address = excluded.address,
    protocol = excluded.protocol,
    host_regexp = excluded.host_regexp,
    path_regexp = excluded.path_regexp,
    price = excluded.price,
    auth = excluded.auth,
    auth_scheme = excluded.auth_scheme,
    updated_at = excluded.updated_at;

-- name: DeleteService :execrows
DELETE FROM services
WHERE name = $1;

-- name: ListServices :many
SELECT *
FROM services
ORDER BY name;
