-- name: CreatePayment :one
INSERT INTO payments (order_id, user_id, merchant_id, amount, status, pay_channel)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetPayment :one
SELECT * FROM payments WHERE id = $1;

-- name: GetPaymentByOrder :one
SELECT * FROM payments WHERE order_id = $1;

-- name: GetPaymentByThirdPartyTxID :one
SELECT * FROM payments WHERE third_party_tx_id = $1;

-- name: UpdatePaymentSuccess :one
UPDATE payments
SET status = 'success', third_party_tx_id = $2, paid_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE id = $1
RETURNING *;

-- name: UpdatePaymentFailed :exec
UPDATE payments SET status = 'failed', updated_at = CURRENT_TIMESTAMP WHERE id = $1;

-- name: UpdatePaymentRefunded :exec
UPDATE payments SET status = 'refunded', updated_at = CURRENT_TIMESTAMP WHERE id = $1;

-- name: HasOngoingPayments :one
SELECT EXISTS (SELECT 1 FROM payments WHERE user_id = $1 AND status = 'pending') AS has_ongoing;

-- name: CreatePaymentWithOutTradeNo :one
INSERT INTO payments (
  order_id, user_id, merchant_id, amount, status, pay_channel, out_trade_no
) VALUES (
  $1, $2, $3, $4, $5, $6, $7
)
RETURNING *;

-- name: GetActivePaymentByOrderChannel :one
SELECT * FROM payments
WHERE order_id = $1 AND pay_channel = $2 AND status IN ('pending','success')
LIMIT 1;

-- name: GetPaymentByOutTradeNo :one
SELECT * FROM payments WHERE out_trade_no = $1 LIMIT 1;
