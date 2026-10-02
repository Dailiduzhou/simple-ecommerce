-- name: CreateUser :one
INSERT INTO users (nickname, real_name, phone_hash, phone_encrypt, password_hash, role)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetUserByID :one
SELECT *
FROM users
WHERE id = $1;

-- name: GetUserByPhoneHash :one
SELECT *
FROM users
WHERE phone_hash = $1;

-- name: LockUserForAddress :one
-- Serialize default-address switches even when the user has no default yet.
-- Allow checkout's user FK KEY SHARE lock while it holds an address FOR SHARE.
SELECT id FROM users WHERE id=$1 FOR NO KEY UPDATE;

-- name: UpdateUser :one
UPDATE users
SET nickname = $2,
    real_name = $3,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
RETURNING *;

-- name: UpdateUserPassword :execrows
UPDATE users
SET password_hash = $2,
    auth_version = auth_version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND auth_version = sqlc.arg(expected_version)::bigint;

-- name: UpdateUserRole :exec
UPDATE users
SET role = $2,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: DeleteUser :exec
DELETE FROM users
WHERE id = $1;
