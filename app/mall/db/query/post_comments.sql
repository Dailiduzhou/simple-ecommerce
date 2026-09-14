-- name: GetComment :one
SELECT *
FROM post_comments
WHERE post_id = $1
  AND id = $2;

-- name: CreateComment :one
INSERT INTO post_comments (post_id, author_id, root_comment_id, reply_to_comment_id, content)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: DeleteComment :execrows
UPDATE post_comments
SET content = '',
    deleted_at = clock_timestamp()
WHERE post_id = sqlc.arg(post_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL
  AND (author_id = sqlc.arg(actor_id) OR sqlc.arg(is_admin)::boolean);

-- name: DeleteUserComments :exec
UPDATE post_comments
SET content = '',
    deleted_at = clock_timestamp()
WHERE author_id = $1
  AND deleted_at IS NULL;

-- name: ListRootComments :many
SELECT
  c.*,
  CASE WHEN c.deleted_at IS NULL THEN COALESCE(u.nickname, '已注销') ELSE '' END::text AS nickname,
  (
    SELECT count(*)
    FROM post_comments r
    WHERE r.post_id = c.post_id
      AND r.root_comment_id = c.id
      AND r.deleted_at IS NULL
  )::bigint AS reply_count
FROM post_comments c
LEFT JOIN users u ON u.id = c.author_id
JOIN posts p ON p.id = c.post_id
WHERE c.post_id = sqlc.arg(post_id)
  AND p.deleted_at IS NULL
  AND c.root_comment_id IS NULL
  AND (
    c.deleted_at IS NULL
    OR EXISTS (
      SELECT 1
      FROM post_comments r
      WHERE r.post_id = c.post_id
        AND r.root_comment_id = c.id
        AND r.deleted_at IS NULL
    )
  )
  AND (
    NOT sqlc.arg(has_cursor)::boolean
    OR (c.created_at, c.id) < (sqlc.arg(cursor_time)::timestamptz, sqlc.arg(cursor_id)::bigint)
  )
ORDER BY c.created_at DESC, c.id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListCommentReplies :many
SELECT
  c.*,
  COALESCE(u.nickname, '已注销')::text AS nickname,
  (target.deleted_at IS NOT NULL)::boolean AS target_deleted,
  CASE
    WHEN target.deleted_at IS NULL THEN COALESCE(target.author_id, 0)
    ELSE 0
  END::bigint AS target_author_id,
  CASE
    WHEN target.deleted_at IS NULL THEN COALESCE(tu.nickname, '已注销')
    ELSE ''
  END::text AS target_nickname
FROM post_comments c
JOIN posts p ON p.id = c.post_id
LEFT JOIN users u ON u.id = c.author_id
JOIN post_comments target
  ON target.post_id = c.post_id
 AND target.id = c.reply_to_comment_id
LEFT JOIN users tu ON tu.id = target.author_id
WHERE c.post_id = sqlc.arg(post_id)
  AND p.deleted_at IS NULL
  AND c.root_comment_id = sqlc.arg(root_id)
  AND c.deleted_at IS NULL
  AND (
    NOT sqlc.arg(has_cursor)::boolean
    OR (c.created_at, c.id) > (sqlc.arg(cursor_time)::timestamptz, sqlc.arg(cursor_id)::bigint)
  )
ORDER BY c.created_at, c.id
LIMIT sqlc.arg(page_limit);
