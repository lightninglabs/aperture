-- name: UpsertService :exec
INSERT INTO services (
    name, address, protocol, host_regexp, path_regexp, price, auth,
    auth_scheme, payment_lndhost, payment_tlspath, payment_macpath,
    created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
)
ON CONFLICT(name) DO UPDATE SET
    address = excluded.address,
    protocol = excluded.protocol,
    host_regexp = excluded.host_regexp,
    path_regexp = excluded.path_regexp,
    price = excluded.price,
    auth = excluded.auth,
    auth_scheme = excluded.auth_scheme,
    payment_lndhost = excluded.payment_lndhost,
    payment_tlspath = excluded.payment_tlspath,
    payment_macpath = excluded.payment_macpath,
    updated_at = excluded.updated_at;

-- name: DeleteService :execrows
DELETE FROM services
WHERE name = $1;

-- name: ListServices :many
SELECT *
FROM services
ORDER BY name;
