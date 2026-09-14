-- name: GetPostImages :many
SELECT
  i.post_id,
  i.sort_order,
  m.*
FROM post_images i
JOIN media_assets m ON m.id = i.media_id
JOIN posts p ON p.id = i.post_id
WHERE i.post_id = ANY(sqlc.arg(post_ids)::bigint[])
  AND p.deleted_at IS NULL
  AND m.status = 'ready'
ORDER BY i.post_id, i.sort_order;

-- name: LockPostImageAssets :many
SELECT m.*
FROM media_assets m
JOIN post_images i ON i.media_id = m.id
WHERE i.post_id = $1
ORDER BY m.id
FOR UPDATE OF m;

-- name: GetMediaBinding :one
SELECT post_id
FROM post_images
WHERE media_id = $1;

-- name: BindPostImage :exec
INSERT INTO post_images (post_id, media_id, sort_order)
VALUES ($1, $2, $3);

-- name: UnbindPostImages :exec
DELETE FROM post_images
WHERE post_id = $1;

-- name: UnbindUserImages :exec
DELETE FROM post_images
WHERE media_id IN (
  SELECT id
  FROM media_assets
  WHERE owner_id = $1
);
