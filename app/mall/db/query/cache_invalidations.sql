-- name: LockCacheInvalidations :many
SELECT * FROM cache_invalidations ORDER BY id
LIMIT $1 FOR UPDATE SKIP LOCKED;

-- name: DeleteCacheInvalidations :exec
DELETE FROM cache_invalidations WHERE id = ANY($1::bigint[]);
