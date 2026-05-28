-- name: CreateOrderItem :one
INSERT INTO order_items (order_id, product_id, quantity, unit_price)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ListOrderItems :many
SELECT * FROM order_items WHERE order_id = $1;

-- name: ListOrderItemsWithProduct :many
SELECT oi.*, p.name AS product_name, p.cover_image
FROM order_items oi
JOIN products p ON p.id = oi.product_id
WHERE oi.order_id = $1;
