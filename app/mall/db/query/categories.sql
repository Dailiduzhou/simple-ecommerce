-- name: CreateCategory :one
INSERT INTO categories (parent_id, name, sort_order)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetCategory :one
SELECT *
FROM categories
WHERE id = $1;

-- name: ListTopCategories :many
SELECT *
FROM categories
WHERE parent_id IS NULL
ORDER BY sort_order, id;

-- name: ListSubCategories :many
SELECT *
FROM categories
WHERE parent_id = $1
ORDER BY sort_order, id;

-- name: UpdateCategory :one
UPDATE categories
SET name = $2,
    sort_order = $3,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
RETURNING *;

-- name: CountSubCategories :one
SELECT COUNT(*)
FROM categories
WHERE parent_id = $1;

-- name: CountCategoryProductReferences :one
-- Any product row still owns a foreign key to the category; soft-deleted rows
-- count too, because the FK is not conditional on deleted_at.
SELECT COUNT(*)
FROM products
WHERE category_id = $1;

-- name: DeleteCategoryIfUnused :execrows
-- 单条语句完成"没有子类、没有被商品引用"检查与删除：避免应用层
-- check-then-delete 的 TOCTOU 竞争。返回 0 行表示未删除。
DELETE FROM categories
WHERE categories.id = $1
  AND NOT EXISTS (SELECT 1 FROM categories child WHERE child.parent_id = categories.id)
  AND NOT EXISTS (SELECT 1 FROM products p WHERE p.category_id = categories.id);
