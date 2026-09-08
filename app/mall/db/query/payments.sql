-- name: CreatePayment :one
INSERT INTO payments (
  order_id, user_id, merchant_id, amount_minor, currency, status, pay_channel, out_trade_no
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
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

-- name: MarkPaymentClosePending :one
UPDATE payments SET status = 'close_pending', updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND status IN ('creating', 'pending')
RETURNING *;

-- name: MarkPaymentClosed :one
UPDATE payments
SET status = 'closed',
    prepay_lease_token = NULL,
    prepay_lease_until = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND status IN ('creating', 'pending', 'close_pending')
RETURNING *;

-- name: RequirePaymentReconciliation :one
UPDATE payments
SET reconciliation_status = 'required',
    reconciliation_reason = $2,
    reconciliation_detail = $3,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND reconciliation_status NOT IN ('processing', 'resolved')
RETURNING *;

-- name: UpdatePaymentRefunded :execrows
UPDATE payments SET status = 'refunded', updated_at = CURRENT_TIMESTAMP WHERE id = $1 AND status = 'success';

-- name: CreatePaymentWithOutTradeNo :one
INSERT INTO payments (
  order_id, user_id, merchant_id, amount_minor, currency, status, pay_channel, out_trade_no
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8
)
RETURNING *;

-- name: GetActivePaymentByOrderChannel :one
SELECT * FROM payments
WHERE order_id = $1 AND pay_channel = $2 AND status IN ('creating', 'pending', 'close_pending')
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: GetActivePaymentByOrder :one
SELECT * FROM payments
WHERE order_id = $1 AND status IN ('creating', 'pending', 'close_pending')
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: GetPaymentByOutTradeNo :one
SELECT * FROM payments WHERE out_trade_no = $1;

-- name: ClaimPaymentPrepay :one
UPDATE payments
SET prepay_lease_token = $2,
    prepay_lease_until = CURRENT_TIMESTAMP + make_interval(secs => sqlc.arg(lease_seconds)::double precision),
    prepay_attempts = prepay_attempts + 1,
    last_error = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
  AND status = 'creating'
  AND (prepay_lease_until IS NULL OR prepay_lease_until < CURRENT_TIMESTAMP)
RETURNING *;

-- name: FinalizePaymentPrepay :one
UPDATE payments
SET status = 'pending',
    action_type = $3,
    action_payload = $4,
    prepay_lease_token = NULL,
    prepay_lease_until = NULL,
    last_error = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
  AND status = 'creating'
  AND prepay_lease_token = $2
RETURNING *;

-- name: FailPaymentPrepay :one
UPDATE payments
SET status = 'failed',
    prepay_lease_token = NULL,
    prepay_lease_until = NULL,
    last_error = $3,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
  AND status = 'creating'
  AND prepay_lease_token = $2
RETURNING *;

-- name: RecordPaymentPrepayError :execrows
UPDATE payments
SET last_error = $3,
    prepay_lease_token = NULL,
    prepay_lease_until = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
  AND status = 'creating'
  AND prepay_lease_token = $2;

-- name: MarkPaymentFailed :one
UPDATE payments
SET status = 'failed',
    prepay_lease_token = NULL,
    prepay_lease_until = NULL,
    last_error = $2,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND status IN ('creating', 'pending', 'close_pending')
RETURNING *;

-- name: RecordPaymentSuccess :one
UPDATE payments
SET status = 'success',
    third_party_tx_id = $2,
    paid_at = COALESCE(paid_at, CURRENT_TIMESTAMP),
    prepay_lease_token = NULL,
    prepay_lease_until = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND status NOT IN ('success', 'refunded')
RETURNING *;
