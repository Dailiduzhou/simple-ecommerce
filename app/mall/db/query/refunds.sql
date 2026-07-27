-- name: CreateOrderRefund :one
INSERT INTO order_refunds (
  payment_id,
  order_id,
  user_id,
  out_refund_no,
  total_amount_minor,
  refund_amount_minor,
  currency,
  reason,
  status
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, 'pending'
)
RETURNING *;

-- name: GetOrderRefundByPaymentID :one
SELECT *
FROM order_refunds
WHERE payment_id = $1;

-- name: RecordOrderRefundError :one
UPDATE order_refunds
SET status = CASE
      WHEN sqlc.arg(definitive)::boolean THEN 'failed'
      ELSE status
    END,
    last_error = sqlc.arg(last_error),
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id)
  AND status <> 'success'
RETURNING *;

-- name: MarkOrderRefundSuccess :one
UPDATE order_refunds
SET status = 'success',
    last_error = '',
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
  AND payment_id = $2
RETURNING *;

-- name: ConfirmPaymentRefunded :one
UPDATE payments
SET status = 'refunded',
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
  AND status IN ('success', 'refunded')
RETURNING *;
