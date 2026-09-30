-- name: CreateOrderItem :one
INSERT INTO order_items (
  order_id,
  product_id,
  quantity,
  unit_price_minor,
  product_name_snapshot,
  cover_image_snapshot
)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListOrderItems :many
SELECT *
FROM order_items
WHERE order_id = $1;

-- name: RestoreOrderItemStock :exec
UPDATE products p
SET stock = p.stock + oi.quantity,
    updated_at = CURRENT_TIMESTAMP
FROM order_items oi
WHERE oi.order_id = $1
  AND oi.product_id = p.id;

-- name: ListOrderItemsByOrderIDs :many
SELECT * FROM order_items WHERE order_id=ANY($1::bigint[])
ORDER BY order_id, id;

-- name: ListOrderProductCacheTargets :many
SELECT DISTINCT p.id, p.category_id FROM products p
JOIN order_items oi ON oi.product_id = p.id WHERE oi.order_id = $1;
