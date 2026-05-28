-- name: CreateCategory :one
INSERT INTO categories (parent_id, name, sort_order)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetCategory :one
SELECT * FROM categories WHERE id = $1;

-- name: ListTopCategories :many
SELECT * FROM categories WHERE parent_id IS NULL ORDER BY sort_order, id;

-- name: ListSubCategories :many
SELECT * FROM categories WHERE parent_id = $1 ORDER BY sort_order, id;

-- name: UpdateCategory :one
UPDATE categories
SET name = $2, sort_order = $3, updated_at = CURRENT_TIMESTAMP
WHERE id = $1
RETURNING *;

-- name: DeleteCategory :exec
DELETE FROM categories WHERE id = $1;
