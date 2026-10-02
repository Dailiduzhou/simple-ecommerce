-- name: GetReconciliationAction :one
SELECT * FROM payment_reconciliation_actions WHERE payment_id=$1 AND idempotency_key=$2;

-- name: CreateReconciliationAction :one
INSERT INTO payment_reconciliation_actions(payment_id,actor_id,action,from_status,to_status,from_version,to_version,idempotency_key,reason,evidence,river_job_id)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING *;

-- name: RetryPaymentReconciliation :one
UPDATE payments SET reconciliation_status='processing', reconciliation_version=reconciliation_version+1, updated_at=clock_timestamp()
WHERE id=$1 AND reconciliation_version=$2 AND reconciliation_status='required' RETURNING *;

-- name: ResolvePaymentReconciliation :one
WITH resolved AS (
  UPDATE payments SET reconciliation_status='resolved', reconciliation_version=payments.reconciliation_version+1, updated_at=clock_timestamp()
  WHERE payments.id=$1 AND payments.reconciliation_version=$2 AND payments.reconciliation_status IN ('required','processing') RETURNING payments.*
), cleared AS (
  UPDATE payment_reconciliation_failures f SET resolved_at=clock_timestamp()
  FROM resolved p WHERE f.payment_id=p.id AND f.resolved_at IS NULL
)
SELECT * FROM resolved;

-- name: ListReconciliationCases :many
SELECT * FROM payments WHERE id > $1 AND reconciliation_status IN ('required','processing') ORDER BY id LIMIT $2;

-- name: ListReconciliationActions :many
SELECT * FROM payment_reconciliation_actions WHERE payment_id=$1 AND id>$2 ORDER BY id LIMIT $3;
