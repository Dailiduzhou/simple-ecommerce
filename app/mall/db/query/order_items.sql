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

-- name: GetRestorableProductStock :one
-- Call AFTER locking the product, in a separate READ COMMITTED statement.
-- Checkout and stock restoration both hold that product lock until commit.
-- Read orders without row locks to avoid reversing their order -> product order.
-- Only these states can still return stock; shipped/completed orders cannot be
-- cancelled/refunded, and duplicate/late payment refunds do not restore stock.
SELECT COALESCE(SUM(oi.quantity), 0)::bigint AS reserved
FROM order_items oi
JOIN orders o ON o.id = oi.order_id
WHERE oi.product_id = $1
  AND o.status IN ('pending_payment', 'cancelling', 'paid');
