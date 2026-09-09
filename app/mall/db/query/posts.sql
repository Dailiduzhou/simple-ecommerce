-- name: CreatePost :one
INSERT INTO posts(author_id,title,content) VALUES($1,$2,$3) RETURNING *;
-- name: LockPost :one
SELECT * FROM posts WHERE id=$1 FOR UPDATE;
-- name: GetVisiblePost :one
SELECT p.*, COALESCE(u.nickname,'已注销')::text AS nickname FROM posts p LEFT JOIN users u ON u.id=p.author_id WHERE p.id=$1 AND p.deleted_at IS NULL;
-- name: ListPosts :many
SELECT p.*, COALESCE(u.nickname,'已注销')::text AS nickname FROM posts p LEFT JOIN users u ON u.id=p.author_id
WHERE p.deleted_at IS NULL AND (sqlc.arg(author_id)::bigint = 0 OR p.author_id=sqlc.arg(author_id))
AND (NOT sqlc.arg(has_cursor)::boolean OR (p.created_at,p.id)<(sqlc.arg(cursor_time)::timestamptz,sqlc.arg(cursor_id)::bigint))
ORDER BY p.created_at DESC,p.id DESC LIMIT sqlc.arg(page_limit);
-- name: UpdatePost :one
UPDATE posts SET title=sqlc.arg(title),content=sqlc.arg(content),version=version+1,updated_at=clock_timestamp()
WHERE id=sqlc.arg(id) AND author_id=sqlc.arg(author_id) AND deleted_at IS NULL AND version=sqlc.arg(expected_version) RETURNING *;
-- name: SoftDeletePost :execrows
UPDATE posts SET deleted_at=clock_timestamp(),updated_at=clock_timestamp(),version=version+1
WHERE id=sqlc.arg(id) AND deleted_at IS NULL AND (author_id=sqlc.arg(actor_id) OR sqlc.arg(is_admin)::boolean);
-- name: LockUserCommunityPosts :many
SELECT p.* FROM posts p WHERE p.author_id=$1 OR EXISTS(SELECT 1 FROM post_comments c WHERE c.post_id=p.id AND c.author_id=$1)
ORDER BY p.id FOR UPDATE;
-- name: HideUserPosts :exec
UPDATE posts SET deleted_at=clock_timestamp(),updated_at=clock_timestamp(),version=version+1 WHERE author_id=$1 AND deleted_at IS NULL;
