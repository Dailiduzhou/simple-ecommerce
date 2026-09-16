-- name: LockCommunityUser :one
SELECT *
FROM users
WHERE id = $1
FOR UPDATE;

-- name: RecordProductView :one
INSERT INTO product_browsing_history (user_id, product_id, first_viewed_at, last_viewed_at)
SELECT
  sqlc.arg(user_id),
  p.id,
  statement_timestamp(),
  statement_timestamp()
FROM products p
WHERE p.id = sqlc.arg(product_id)
  AND p.status = 1
  AND p.deleted_at IS NULL
ON CONFLICT (user_id, product_id) DO UPDATE
SET last_viewed_at = GREATEST(product_browsing_history.last_viewed_at, clock_timestamp())
RETURNING *;

-- name: ListBrowsingHistory :many
SELECT
  h.*,
  p.name,
  p.price_minor,
  p.discount,
  p.cover_image,
  (p.status = 1 AND p.deleted_at IS NULL)::boolean AS available
FROM product_browsing_history h
JOIN products p ON p.id = h.product_id
WHERE h.user_id = sqlc.arg(user_id)
  -- 过期判定使用数据库时钟，避免多实例应用时钟漂移导致边界不一致。
  AND h.last_viewed_at > NOW() - make_interval(secs => sqlc.arg(retention_seconds)::double precision)
  AND (NOT sqlc.arg(has_start)::boolean OR h.last_viewed_at >= sqlc.arg(start_time)::timestamptz)
  AND (NOT sqlc.arg(has_end)::boolean OR h.last_viewed_at < sqlc.arg(end_time)::timestamptz)
  AND (
    NOT sqlc.arg(has_cursor)::boolean
    OR (h.last_viewed_at, h.product_id)
      < (sqlc.arg(cursor_time)::timestamptz, sqlc.arg(cursor_id)::bigint)
  )
ORDER BY h.last_viewed_at DESC, h.product_id DESC
LIMIT sqlc.arg(page_limit);

-- name: DeleteBrowsingHistoryItem :exec
DELETE FROM product_browsing_history
WHERE user_id = $1
  AND product_id = $2;

-- name: ClearBrowsingHistory :exec
DELETE FROM product_browsing_history
WHERE user_id = $1;

-- name: CleanupBrowsingHistory :execrows
WITH expired AS (
  SELECT user_id, product_id
  FROM product_browsing_history
  -- 与列表页共用数据库时钟计算过期边界。
  WHERE last_viewed_at <= NOW() - make_interval(secs => sqlc.arg(retention_seconds)::double precision)
  ORDER BY last_viewed_at, user_id, product_id
  LIMIT sqlc.arg(batch_size)
  FOR UPDATE SKIP LOCKED
)
DELETE FROM product_browsing_history h
USING expired e
WHERE h.user_id = e.user_id
  AND h.product_id = e.product_id
  AND h.last_viewed_at <= NOW() - make_interval(secs => sqlc.arg(retention_seconds)::double precision);
