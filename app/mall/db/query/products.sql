-- name: CreateProduct :one
INSERT INTO products (
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
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- 公开读取仅返回上架商品；管理端传入 include_unpublished=true 查看全部状态。
-- name: GetProduct :one
SELECT *
FROM products
WHERE id = sqlc.arg(id)
  AND deleted_at IS NULL
  AND (sqlc.arg(include_unpublished)::boolean OR status = 1);

-- name: GetProductForOrder :one
SELECT *
FROM products
WHERE id = $1
  AND deleted_at IS NULL
FOR UPDATE;

-- 商品状态：0=下架，1=上架；公开列表仅展示上架商品，管理端可传
-- include_unpublished=true 查看全部状态。
-- name: ListProductsByCategory :many
SELECT *
FROM products
WHERE category_id = sqlc.arg(category_id)
  AND deleted_at IS NULL
  AND (sqlc.arg(include_unpublished)::boolean OR status = 1)
ORDER BY id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: ListProducts :many
SELECT *
FROM products
WHERE deleted_at IS NULL
  AND (sqlc.arg(include_unpublished)::boolean OR status = 1)
ORDER BY id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

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
  AND (sqlc.arg(include_unpublished)::boolean OR status = 1);

-- name: CountProductsByCategory :one
SELECT count(*)
FROM products
WHERE category_id = sqlc.arg(category_id)
  AND deleted_at IS NULL
  AND (sqlc.arg(include_unpublished)::boolean OR status = 1);
