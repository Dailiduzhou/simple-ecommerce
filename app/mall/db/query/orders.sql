-- name: CreateOrder :one
INSERT INTO orders (user_id, address_id, total_amount, status)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetOrder :one
SELECT * FROM orders WHERE id = $1;

-- name: GetOrderByUser :one
SELECT * FROM orders WHERE id = $1 AND user_id = $2;

-- name: ListOrdersByUser :many
SELECT * FROM orders WHERE user_id = $1 ORDER BY id DESC LIMIT $2 OFFSET $3;

-- name: ListOngoingOrdersByUser :many
SELECT * FROM orders WHERE user_id = $1 AND is_completed = FALSE ORDER BY id DESC;

-- name: UpdateOrderStatus :one
UPDATE orders SET status = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $1 RETURNING *;

-- name: CompleteOrder :exec
UPDATE orders SET is_completed = TRUE, status = 'completed', updated_at = CURRENT_TIMESTAMP WHERE id = $1;

-- name: CancelOrder :exec
UPDATE orders SET is_completed = TRUE, status = 'cancelled', updated_at = CURRENT_TIMESTAMP WHERE id = $1;

-- name: HasOngoingOrders :one
SELECT EXISTS (SELECT 1 FROM orders WHERE user_id = $1 AND is_completed = FALSE) AS has_ongoing;
