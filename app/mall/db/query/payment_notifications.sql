-- name: CreatePaymentNotification :one
INSERT INTO payment_notifications (
  provider, provider_event_id, out_trade_no, payload_hash, verified_at
) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetPaymentNotification :one
SELECT * FROM payment_notifications WHERE id = $1;

-- name: GetPaymentNotificationByEvent :one
SELECT * FROM payment_notifications
WHERE provider = $1 AND provider_event_id = $2;

-- name: GetPaymentNotificationByPayload :one
SELECT * FROM payment_notifications
WHERE provider = $1 AND out_trade_no = $2 AND payload_hash = $3;

-- name: BeginPaymentNotificationProcessing :one
UPDATE payment_notifications
SET status = 'processing', last_error = NULL, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND status IN ('received', 'processing', 'failed')
RETURNING *;

-- name: RecordPaymentNotificationError :execrows
UPDATE payment_notifications
SET last_error = $2, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND status = 'processing';

-- name: SetPaymentNotificationRiverJob :exec
UPDATE payment_notifications
SET river_job_id = $2, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND status <> 'processed';

-- name: MarkPaymentNotificationProcessed :execrows
UPDATE payment_notifications
SET status = 'processed', processed_at = CURRENT_TIMESTAMP,
    last_error = NULL, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND status IN ('received', 'processing');

-- name: MarkPaymentNotificationFailed :execrows
UPDATE payment_notifications
SET status = 'failed', last_error = $2, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND status IN ('received', 'processing', 'failed');

-- name: CreatePaymentReconciliationFailure :one
INSERT INTO payment_reconciliation_failures (
  payment_id, provider, reason, river_job_id, attempt, last_error
) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (river_job_id) WHERE river_job_id IS NOT NULL
DO UPDATE SET reason = EXCLUDED.reason, attempt = EXCLUDED.attempt, last_error = EXCLUDED.last_error
RETURNING *;
