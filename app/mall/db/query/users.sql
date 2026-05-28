-- name: CreateUser :one
INSERT INTO users (nickname, real_name, phone_hash, phone_encrypt, password_hash, role)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByPhoneHash :one
SELECT * FROM users WHERE phone_hash = $1;

-- name: UpdateUser :one
UPDATE users
SET nickname = $2, real_name = $3, updated_at = CURRENT_TIMESTAMP
WHERE id = $1
RETURNING *;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $1;

-- name: UpdateUserRole :exec
UPDATE users SET role = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $1;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = $1;
