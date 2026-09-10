-- name: LikePost :exec
INSERT INTO post_likes(post_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING;
-- name: UnlikePost :exec
DELETE FROM post_likes WHERE post_id=$1 AND user_id=$2;
-- name: GetPostStats :many
SELECT p.id,
 (SELECT count(*) FROM post_likes l WHERE l.post_id=p.id)::bigint AS like_count,
 (SELECT count(*) FROM post_comments c WHERE c.post_id=p.id AND c.deleted_at IS NULL)::bigint AS comment_count,
 EXISTS(SELECT 1 FROM post_likes l WHERE l.post_id=p.id AND l.user_id=sqlc.arg(viewer_id))::boolean AS liked_by_me
FROM posts p WHERE p.id = ANY(sqlc.arg(post_ids)::bigint[]) AND p.deleted_at IS NULL;
