-- name: CreateOrderItem :one
INSERT INTO order_items (order_id, product_id, quantity, unit_price_minor, product_name_snapshot, cover_image_snapshot)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListOrderItems :many
SELECT * FROM order_items WHERE order_id = $1;

-- name: RestoreOrderItemStock :exec
UPDATE products p
SET stock = p.stock + oi.quantity, updated_at = CURRENT_TIMESTAMP
FROM order_items oi
WHERE oi.order_id = $1 AND oi.product_id = p.id;
