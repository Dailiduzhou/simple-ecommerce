-- name: CreateMediaAsset :one
INSERT INTO media_assets(owner_id,provider,bucket_name,object_key,staging_key,content_type,size_bytes,expires_at,upload_expires_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING *;

-- name: GetMediaAsset :one
SELECT * FROM media_assets WHERE id=$1;

-- name: LockMediaAssets :many
SELECT * FROM media_assets WHERE id=ANY(sqlc.arg(ids)::bigint[]) ORDER BY id FOR UPDATE;

-- name: ReadyMediaAsset :one
UPDATE media_assets SET status='ready',content_type=$2,size_bytes=$3,width=$4,height=$5,updated_at=clock_timestamp()
WHERE id=$1 AND status='pending' RETURNING *;

-- name: MarkMediaDeleting :execrows
UPDATE media_assets SET status='deleting',updated_at=clock_timestamp() WHERE id=$1 AND status IN ('pending','ready')
AND NOT EXISTS(SELECT 1 FROM post_images WHERE media_id=$1);

-- name: MarkMediaDeleted :exec
UPDATE media_assets SET status='deleted',updated_at=clock_timestamp() WHERE id=$1 AND status='deleting';

-- name: LockUserMedia :many
SELECT * FROM media_assets WHERE owner_id=$1 AND status <> 'deleted' ORDER BY id FOR UPDATE;

-- name: LockExpiredMedia :many
-- Each class gets its own bounded budget: arbitrarily many failed deletions
-- cannot consume the slots needed to transition fresh expirations.
WITH fresh AS (
 SELECT m.* FROM media_assets m WHERE m.status IN ('pending','ready') AND m.expires_at<=clock_timestamp()
 AND NOT EXISTS(SELECT 1 FROM post_images i WHERE i.media_id=m.id)
 ORDER BY m.expires_at,m.id LIMIT $1 FOR UPDATE SKIP LOCKED
), stale AS (
 SELECT m.* FROM media_assets m WHERE m.status='deleting' AND m.updated_at<=clock_timestamp()-interval '10 minutes'
 AND NOT EXISTS(SELECT 1 FROM post_images i WHERE i.media_id=m.id)
 ORDER BY m.updated_at,m.id LIMIT $1 FOR UPDATE SKIP LOCKED
)
SELECT * FROM fresh UNION ALL SELECT * FROM stale;
-- name: CanReadMedia :one
SELECT EXISTS(SELECT 1 FROM media_assets m WHERE m.id=sqlc.arg(id) AND m.status='ready' AND
 (EXISTS(SELECT 1 FROM post_images i JOIN posts p ON p.id=i.post_id WHERE i.media_id=m.id AND p.deleted_at IS NULL)
 OR (m.owner_id=sqlc.arg(viewer_id) AND m.expires_at>clock_timestamp() AND NOT EXISTS(SELECT 1 FROM post_images i WHERE i.media_id=m.id))))::boolean;

-- name: AcquireMediaIOLock :exec
-- 1279476052 is this application's advisory-lock namespace for media object I/O.
-- It is an arbitrary but fixed int4; keep it unique across every advisory-lock
-- user in this database so unrelated features never contend by accident. The
-- second key is hashint8(media id), giving one session-level lock per media row
-- that survives COMMIT/ROLLBACK and is released on unlock or session end.
SELECT pg_advisory_lock(1279476052,hashint8($1::bigint));

-- name: ReleaseMediaIOLock :one
-- Must run on the same session that acquired the lock; false means the session
-- did not hold it, so the caller must discard the connection instead of
-- returning a possibly locked session to the pool.
SELECT pg_advisory_unlock(1279476052,hashint8($1::bigint))::boolean AS unlocked;

-- name: TouchDeletingMedia :exec
UPDATE media_assets SET updated_at=clock_timestamp() WHERE id=$1 AND status='deleting';
