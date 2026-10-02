-- name: MarkOrderShipped :one
UPDATE orders SET status='shipped', updated_at=clock_timestamp()
WHERE id=$1 AND status='paid' AND paid_payment_id IS NOT NULL
RETURNING *;

-- name: MarkOrderCompleted :one
UPDATE orders SET status='completed', is_completed=TRUE, updated_at=clock_timestamp()
WHERE id=$1 AND status='shipped'
RETURNING *;

-- name: GetOrderFulfillmentAction :one
SELECT * FROM order_fulfillment_actions WHERE order_id=$1 AND idempotency_key=$2;

-- name: CreateOrderFulfillmentAction :one
INSERT INTO order_fulfillment_actions
 (order_id, actor_id, action, from_status, to_status, idempotency_key, reason, carrier, tracking_number)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING *;

-- name: ListOrderFulfillmentActions :many
-- The unique order/action constraint and action CHECK bound each order to two records.
SELECT * FROM order_fulfillment_actions WHERE order_id=$1 ORDER BY id LIMIT 2;
