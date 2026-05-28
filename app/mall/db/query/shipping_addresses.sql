-- name: CreateShippingAddress :one
INSERT INTO shipping_addresses (user_id, receiver_name, receiver_phone_hash, receiver_phone_encrypt, province, city, district, detail_address, address_tag, is_default)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: GetShippingAddress :one
SELECT * FROM shipping_addresses WHERE id = $1 AND user_id = $2;

-- name: ListShippingAddressesByUser :many
SELECT * FROM shipping_addresses WHERE user_id = $1 ORDER BY is_default DESC, id DESC;

-- name: GetDefaultShippingAddress :one
SELECT * FROM shipping_addresses WHERE user_id = $1 AND is_default = TRUE LIMIT 1;

-- name: UpdateShippingAddress :one
UPDATE shipping_addresses
SET receiver_name = $3, receiver_phone_hash = $4, receiver_phone_encrypt = $5,
    province = $6, city = $7, district = $8, detail_address = $9, address_tag = $10,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND user_id = $2
RETURNING *;

-- name: ClearDefaultShippingAddress :exec
UPDATE shipping_addresses SET is_default = FALSE, updated_at = CURRENT_TIMESTAMP WHERE user_id = $1;

-- name: SetDefaultShippingAddress :exec
UPDATE shipping_addresses SET is_default = TRUE, updated_at = CURRENT_TIMESTAMP WHERE id = $1 AND user_id = $2;

-- name: DeleteShippingAddress :exec
DELETE FROM shipping_addresses WHERE id = $1 AND user_id = $2;
