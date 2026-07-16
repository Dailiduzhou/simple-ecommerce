-- name: CreatePayment :one
INSERT INTO payments (order_id, user_id, merchant_id, amount_minor, status, pay_channel)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetPayment :one
SELECT * FROM payments WHERE id = $1;

-- name: GetPaymentForUpdate :one
SELECT * FROM payments WHERE id = $1 FOR UPDATE;

-- name: GetLatestPaymentByOrder :one
SELECT * FROM payments WHERE order_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: ListPaymentsByOrderForUpdate :many
SELECT * FROM payments
WHERE order_id = $1
ORDER BY created_at DESC, id DESC
FOR UPDATE;

-- name: GetPaymentByThirdPartyTxID :one
SELECT * FROM payments WHERE third_party_tx_id = $1;

-- name: HasSuccessfulPaymentByOrder :one
SELECT EXISTS (
  SELECT 1 FROM payments WHERE order_id = $1 AND status IN ('success', 'refunded', 'reconcile_required')
) AS has_successful;

-- name: MarkPaymentSuccess :one
UPDATE payments
SET status = 'success', third_party_tx_id = $2, paid_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: MarkPaymentClosePending :one
UPDATE payments SET status = 'close_pending', updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: MarkPaymentClosed :one
UPDATE payments SET status = 'closed', updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND status IN ('pending', 'close_pending')
RETURNING *;

-- name: MarkPaymentReconcileRequired :one
UPDATE payments SET status = 'reconcile_required', updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND status <> 'reconcile_required'
RETURNING *;

-- name: UpdatePaymentRefunded :exec
UPDATE payments SET status = 'refunded', updated_at = CURRENT_TIMESTAMP WHERE id = $1 AND status = 'success';

-- name: HasOngoingPayments :one
SELECT EXISTS (SELECT 1 FROM payments WHERE user_id = $1 AND status IN ('creating', 'pending', 'close_pending', 'reconcile_required')) AS has_ongoing;

-- name: CreatePaymentWithOutTradeNo :one
INSERT INTO payments (
  order_id, user_id, merchant_id, amount_minor, currency, status, pay_channel, out_trade_no
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8
)
RETURNING *;

-- name: GetActivePaymentByOrderChannel :one
SELECT * FROM payments
WHERE order_id = $1 AND pay_channel = $2 AND status IN ('creating', 'pending', 'success', 'close_pending')
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: GetPaymentByOutTradeNo :one
SELECT * FROM payments WHERE out_trade_no = $1;

-- name: MarkPaymentPending :one
UPDATE payments
SET status = 'pending', action_type = $2, action_payload = $3, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND status = 'creating'
RETURNING *;
