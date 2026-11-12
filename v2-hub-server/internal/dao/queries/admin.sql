-- name: GetAdminByUsername :one
SELECT id, username, password_hash, is_disabled
FROM admin
WHERE username = $1;

-- name: GetAdminByID :one
SELECT id, username, is_disabled, last_login_at, created_at
FROM admin
WHERE id = $1;

-- name: CreateAdmin :one
INSERT INTO admin (username, password_hash)
VALUES ($1, $2)
RETURNING id, created_at;

-- name: UpdateAdminDisabled :exec
UPDATE admin SET is_disabled = $2, updated_at = now() WHERE id = $1;

-- name: UpdateAdminPasswordHash :execrows
UPDATE admin SET password_hash = $2, updated_at = now() WHERE id = $1;

-- name: UpdateAdminLastLogin :exec
UPDATE admin SET last_login_at = now() WHERE id = $1;

-- name: ListAdmins :many
SELECT id, username, is_disabled, last_login_at, created_at
FROM admin
ORDER BY id
LIMIT $1 OFFSET $2;

-- name: CountAdmins :one
SELECT COUNT(*) FROM admin;

-- name: ListRoleCodesByAdminIDs :many
SELECT ar.admin_id, r.code
FROM admin_role ar
JOIN role r ON r.id = ar.role_id
WHERE ar.admin_id = ANY(sqlc.arg(admin_ids)::bigint[])
ORDER BY ar.admin_id, r.code;

-- name: DeleteAdminRoles :exec
DELETE FROM admin_role WHERE admin_id = $1;

-- name: InsertAdminRoles :exec
INSERT INTO admin_role (admin_id, role_id)
SELECT sqlc.arg(admin_id)::bigint, unnest(sqlc.arg(role_ids)::bigint[]);

-- name: CountEnabledAdminsWithRoleExcluding :one
-- 用于 R5：除 excluded_admin_id 外，仍持有该角色且启用的管理员数。
SELECT COUNT(DISTINCT a.id)
FROM admin a
JOIN admin_role ar ON ar.admin_id = a.id
WHERE ar.role_id = sqlc.arg(role_id)
  AND a.is_disabled = FALSE
  AND a.id <> sqlc.arg(excluded_admin_id);

-- name: LockSuperAdminRole :exec
-- R5 的并发保护：改动可能影响 super_admin 数量的事务先锁住该角色行，串行化判断。
SELECT id FROM role WHERE code = 'super_admin' FOR UPDATE;
