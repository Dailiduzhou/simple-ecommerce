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
-- Serialize default-address creation, switching and address deletion, even
-- when the user has no default yet. Acquire this before locking address rows.
-- Allow checkout's user FK KEY SHARE lock while it holds an address FOR SHARE.
SELECT id FROM users WHERE id=$1 FOR NO KEY UPDATE;

-- name: LockUserForReference :one
-- Take the user FK lock before dependent rows, matching account deletion's
-- lock order while allowing non-key user updates and default-address switches.
SELECT id FROM users WHERE id=$1 FOR KEY SHARE;

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

-- name: LockUserRoleChanges :exec
-- Serialize role assignment and account deletion before taking any user/content
-- row locks. 1279476053 is reserved for account-role mutations (media I/O uses
-- 1279476052); the lock is held until the surrounding transaction ends.
SELECT pg_advisory_xact_lock(1279476053, 0);

-- name: CountAdmins :one
SELECT count(*) FROM users WHERE role = 'admin';

-- name: DeleteUser :exec
DELETE FROM users
WHERE id = $1;
