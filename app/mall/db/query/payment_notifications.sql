-- name: CreatePaymentNotification :one
INSERT INTO payment_notifications (
  provider, provider_event_id, out_trade_no, payload_hash, verified_at
) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetPaymentNotification :one
SELECT * FROM payment_notifications WHERE id = $1;

-- name: MarkPaymentNotificationProcessed :exec
UPDATE payment_notifications
SET status = 'processed', processed_at = CURRENT_TIMESTAMP, last_error = NULL
WHERE id = $1 AND status IN ('received', 'processing');

-- name: MarkPaymentNotificationFailed :exec
UPDATE payment_notifications
SET status = 'failed', last_error = $2
WHERE id = $1 AND status <> 'processed';

-- name: CreatePaymentReconciliationFailure :one
INSERT INTO payment_reconciliation_failures (
  payment_id, provider, river_job_id, attempt, last_error
) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (river_job_id) WHERE river_job_id IS NOT NULL
DO UPDATE SET attempt = EXCLUDED.attempt, last_error = EXCLUDED.last_error
RETURNING *;
