-- name: CreateEvent :one
INSERT INTO events (name, status, start_at, end_at, cover_image, media_assets, description)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetEvent :one
SELECT * FROM events WHERE id = $1 AND deleted_at IS NULL;

-- name: ListActiveEvents :many
SELECT * FROM events
WHERE status = 1 AND deleted_at IS NULL
ORDER BY start_at ASC
LIMIT $1 OFFSET $2;

-- name: ListUpcomingEvents :many
SELECT * FROM events
WHERE status = 0 AND start_at > CURRENT_TIMESTAMP AND deleted_at IS NULL
ORDER BY start_at ASC;

-- name: UpdateEvent :one
UPDATE events
SET name = $2, start_at = $3, end_at = $4,
    cover_image = $5, media_assets = $6, description = $7, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: UpdateEventStatus :exec
UPDATE events SET status = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $1 AND deleted_at IS NULL;

-- name: SoftDeleteEvent :exec
UPDATE events SET deleted_at = CURRENT_TIMESTAMP WHERE id = $1;
