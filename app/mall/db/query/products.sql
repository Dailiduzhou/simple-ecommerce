-- name: CreateProduct :one
INSERT INTO products (category_id, name, price_minor, discount, stock, status, cover_image, media_assets, description)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetProduct :one
SELECT * FROM products WHERE id = $1 AND deleted_at IS NULL;

-- name: GetProductForOrder :one
SELECT * FROM products WHERE id = $1 AND deleted_at IS NULL FOR UPDATE;

-- name: ListProductsByCategory :many
SELECT * FROM products
WHERE category_id = $1 AND status = 1 AND deleted_at IS NULL
ORDER BY id DESC
LIMIT $2 OFFSET $3;

-- name: ListProducts :many
SELECT * FROM products
WHERE deleted_at IS NULL
ORDER BY id DESC
LIMIT $1 OFFSET $2;

-- name: UpdateProduct :one
UPDATE products
SET category_id = $2, name = $3, price_minor = $4, discount = $5, stock = $6,
    cover_image = $7, media_assets = $8, description = $9, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: UpdateProductStatus :exec
UPDATE products SET status = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $1 AND deleted_at IS NULL;

-- name: DecrProductStock :one
UPDATE products SET stock = stock - $2, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND stock >= $2 AND deleted_at IS NULL
RETURNING stock;

-- name: IncrementProductStock :exec
UPDATE products SET stock = stock + $2, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND deleted_at IS NULL;

-- name: SoftDeleteProduct :exec
UPDATE products SET deleted_at = CURRENT_TIMESTAMP WHERE id = $1;
