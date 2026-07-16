-- name: CreateOrder :one
INSERT INTO orders (user_id, address_id, total_amount_minor, currency, status, out_trade_no)
VALUES ($1, $2, $3, $4, 'pending_payment', $5)
RETURNING *;

-- name: GetOrder :one
SELECT * FROM orders WHERE id = $1;

-- name: GetOrderForUpdate :one
SELECT * FROM orders WHERE id = $1 FOR UPDATE;

-- name: GetOrderByOrderNo :one
-- 通过商户订单号(orders.out_trade_no)查询订单。
-- 统一支付 API 的入口:order_no -> order。
SELECT * FROM orders WHERE out_trade_no = $1;

-- name: GetOrderByUser :one
SELECT * FROM orders WHERE id = $1 AND user_id = $2;

-- name: GetOrderByUserForUpdate :one
SELECT * FROM orders WHERE id = $1 AND user_id = $2 FOR UPDATE;

-- name: ListOrdersByUser :many
SELECT * FROM orders WHERE user_id = $1 ORDER BY id DESC LIMIT $2 OFFSET $3;

-- name: ListOngoingOrdersByUser :many
SELECT * FROM orders WHERE user_id = $1 AND is_completed = FALSE ORDER BY id DESC;

-- name: MarkOrderPaid :one
UPDATE orders SET status = 'paid', updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND status = 'pending_payment'
RETURNING *;

UPDATE orders SET is_completed = TRUE, status = 'completed', updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND status IN ('paid', 'shipped');

UPDATE orders SET is_completed = TRUE, status = 'cancelled', updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND status IN ('pending_payment', 'cancelling');

-- name: MarkOrderCancelling :one
UPDATE orders SET status = 'cancelling', updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND status = 'pending_payment'
RETURNING *;

-- name: MarkOrderCancelled :one
UPDATE orders SET is_completed = TRUE, status = 'cancelled', updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND status = 'cancelling'
RETURNING *;

-- name: HasOngoingOrders :one
SELECT EXISTS (SELECT 1 FROM orders WHERE user_id = $1 AND is_completed = FALSE) AS has_ongoing;

-- name: CountOrdersByUser :one
SELECT COUNT(*) FROM orders WHERE user_id = $1;
