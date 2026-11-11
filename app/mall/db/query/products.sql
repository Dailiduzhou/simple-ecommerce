-- name: CreateProduct :one
INSERT INTO products (
  merchant_id,
  category_id,
  name,
  price_minor,
  discount,
  stock,
  status,
  cover_image,
  media_assets,
  description
)
VALUES (sqlc.arg(merchant_id), sqlc.arg(category_id), sqlc.arg(name), sqlc.arg(price_minor),
  sqlc.arg(discount), sqlc.arg(stock), sqlc.arg(status), sqlc.arg(cover_image),
  sqlc.arg(media_assets), sqlc.arg(description))
RETURNING *;

-- name: GetProduct :one
SELECT *
FROM products
WHERE id = $1
  AND deleted_at IS NULL;

-- name: GetProductForOrder :one
SELECT *
FROM products
WHERE id = $1
  AND deleted_at IS NULL
FOR UPDATE;

-- 商品状态：0=下架，1=上架；分类商品列表仅展示上架商品。
-- name: ListProductsByCategory :many
SELECT *
FROM products
WHERE category_id = sqlc.arg(category_id)
  AND status = 1
  AND deleted_at IS NULL
  AND (sqlc.narg(merchant_id)::bigint IS NULL OR merchant_id = sqlc.narg(merchant_id))
ORDER BY id DESC
LIMIT sqlc.arg(limit_rows) OFFSET sqlc.arg(offset_rows);

-- name: ListProducts :many
SELECT *
FROM products
WHERE deleted_at IS NULL
  AND (sqlc.narg(merchant_id)::bigint IS NULL OR merchant_id = sqlc.narg(merchant_id))
ORDER BY id DESC
LIMIT sqlc.arg(limit_rows) OFFSET sqlc.arg(offset_rows);

-- name: UpdateProduct :one
UPDATE products
SET category_id = $2,
    name = $3,
    price_minor = $4,
    discount = $5,
    cover_image = $6,
    media_assets = $7,
    description = $8,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
  AND deleted_at IS NULL
RETURNING *;

-- name: UpdateProductStatus :one
UPDATE products
SET status = $2,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
  AND deleted_at IS NULL
RETURNING *;

-- name: AdjustProductStock :one
UPDATE products
SET stock = stock + sqlc.arg(delta)::integer, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: DecrProductStock :one
UPDATE products
SET stock = stock - $2,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
  AND stock >= $2
  AND deleted_at IS NULL
RETURNING stock;

-- name: IncrementProductStock :exec
UPDATE products
SET stock = stock + $2,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
  AND deleted_at IS NULL;

-- name: SoftDeleteProduct :exec
UPDATE products
SET deleted_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: CountProducts :one
SELECT count(*)
FROM products
WHERE deleted_at IS NULL
  AND (sqlc.narg(merchant_id)::bigint IS NULL OR merchant_id = sqlc.narg(merchant_id));

-- name: CountProductsByCategory :one
SELECT count(*)
FROM products
WHERE category_id = sqlc.arg(category_id)
  AND status = 1
  AND deleted_at IS NULL
  AND (sqlc.narg(merchant_id)::bigint IS NULL OR merchant_id = sqlc.narg(merchant_id));
