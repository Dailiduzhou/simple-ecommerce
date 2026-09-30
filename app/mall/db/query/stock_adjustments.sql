-- name: GetStockAdjustment :one
SELECT * FROM stock_adjustments WHERE product_id=$1 AND idempotency_key=$2;

-- name: CreateStockAdjustment :one
INSERT INTO stock_adjustments (product_id,actor_id,delta,reason,idempotency_key,resulting_stock)
VALUES ($1,$2,$3,$4,$5,$6)
RETURNING *;
