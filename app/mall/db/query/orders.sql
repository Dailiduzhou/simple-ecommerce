-- name: CreateOrder :one
INSERT INTO orders (
  user_id,
  address_id,
  total_amount_minor,
  currency,
  status,
  out_trade_no,
  idempotency_key,
  request_hash,
  expires_at
)
VALUES ($1, $2, $3, $4, 'pending_payment', $5, $6, $7, $8)
RETURNING *;

-- name: GetOrder :one
SELECT *
FROM orders
WHERE id = $1;

-- name: GetOrderForUpdate :one
SELECT *
FROM orders
WHERE id = $1
FOR UPDATE;

-- name: GetOrderByOrderNo :one
-- 通过商户订单号(orders.out_trade_no)查询订单。
-- 统一支付 API 的入口:order_no -> order。
SELECT *
FROM orders
WHERE out_trade_no = $1;

-- name: GetOrderByUser :one
SELECT *
FROM orders
WHERE id = $1
  AND user_id = $2;

-- name: GetOrderByUserIdempotency :one
SELECT *
FROM orders
WHERE user_id = $1
  AND idempotency_key = $2;

-- name: GetOrderByUserForUpdate :one
SELECT *
FROM orders
WHERE id = $1
  AND user_id = $2
FOR UPDATE;

-- name: ListOrdersByUser :many
SELECT *
FROM orders
WHERE user_id = $1
ORDER BY id DESC
LIMIT $2 OFFSET $3;

-- name: ListOngoingOrdersByUser :many
SELECT *
FROM orders
WHERE user_id = $1
  AND is_completed = FALSE
ORDER BY id DESC;

-- name: MarkOrderPaid :one
UPDATE orders
SET status = 'paid',
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
  AND status = 'pending_payment'
RETURNING *;

-- name: MarkOrderRefunded :one
-- A fully refunded paid order reaches its terminal state. The CAS guard keeps
-- non-paid orders out (e.g. already cancelled), so a refund can never silently
-- rewrite an order that is not in the refundable state.
UPDATE orders
SET is_completed = TRUE,
    status = 'refunded',
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
  AND status = 'paid'
RETURNING *;

-- name: MarkOrderCancelling :one
UPDATE orders
SET status = 'cancelling',
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
  AND status = 'pending_payment'
RETURNING *;

-- name: MarkOrderCancelled :one
UPDATE orders
SET is_completed = TRUE,
    status = 'cancelled',
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
  AND status = 'cancelling'
RETURNING *;

-- name: HasOngoingOrders :one
SELECT EXISTS (
  SELECT 1
  FROM orders
  WHERE user_id = $1
    AND is_completed = FALSE
) AS has_ongoing;

-- name: CountOrdersByUser :one
SELECT count(*)
FROM orders
WHERE user_id = $1;

-- name: OrderIsExpired :one
-- Expiry decisions must use the database clock, not the application server's,
-- so instances with skewed clocks cannot extend or shrink the payment window.
SELECT COALESCE(expires_at <= now(), TRUE)::boolean AS expired
FROM orders
WHERE id = $1;

-- name: ListOverduePendingOrders :many
-- Backstop for expire_order jobs that were discarded after exhausting retries;
-- the partial index idx_orders_pending_expiry keeps this scan cheap.
SELECT id
FROM orders
WHERE id > sqlc.arg(after_id)::bigint
  AND status = 'pending_payment'
  AND expires_at <= now() - make_interval(secs => sqlc.arg(grace_seconds)::double precision)
ORDER BY id
LIMIT sqlc.arg(limit_rows);

-- name: LockOrderIdempotency :exec
-- Lock the request identity BEFORE reading stock or checking for a replay.
SELECT pg_advisory_xact_lock(hashtextextended(
  'order-idempotency:' || sqlc.arg(user_id)::bigint::text || ':' || sqlc.arg(idempotency_key)::text, 0));

-- name: GetOrderForUpdateByPaymentID :one
-- Refund settlement locks the order row before the payment row, matching
-- every other order-payment transaction (ApplyPayQuery, ExpireOrder,
-- CancelOrderByUser); the inverted order would deadlock against them.
SELECT o.*
FROM orders o
JOIN payments p ON p.id = $1 AND o.id = p.order_id
FOR UPDATE OF o;
