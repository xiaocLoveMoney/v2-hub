-- name: ListPermissions :many
SELECT code, name FROM permission ORDER BY id;

-- name: ListRoles :many
SELECT id, code, name, is_builtin FROM role ORDER BY id;

-- name: GetRoleByID :one
SELECT id, code, name, is_builtin FROM role WHERE id = $1;

-- name: GetRoleByCode :one
SELECT id, code, name, is_builtin FROM role WHERE code = $1;

-- name: CreateRole :one
INSERT INTO role (code, name) VALUES ($1, $2) RETURNING id;

-- name: UpdateRoleName :exec
UPDATE role SET name = $2, updated_at = now() WHERE id = $1;

-- name: DeleteRole :exec
DELETE FROM role WHERE id = $1;

-- name: CountAdminsByRole :one
SELECT COUNT(*) FROM admin_role WHERE role_id = $1;

-- name: ListRolePermissionCodes :many
SELECT rp.role_id, p.code
FROM role_permission rp
JOIN permission p ON p.id = rp.permission_id
ORDER BY rp.role_id, p.id;

-- name: DeleteRolePermissions :exec
DELETE FROM role_permission WHERE role_id = $1;

-- name: InsertRolePermissions :exec
INSERT INTO role_permission (role_id, permission_id)
SELECT sqlc.arg(role_id)::bigint, p.id
FROM permission p
WHERE p.code = ANY(sqlc.arg(codes)::text[]);

-- name: ListRoleCodesForAdmin :many
SELECT r.code
FROM admin_role ar
JOIN role r ON r.id = ar.role_id
WHERE ar.admin_id = $1;

-- name: ListRolesForAdmin :many
-- 鉴权用：内置角色只在 is_builtin 为真时按编码取代码定义的权限（Ruling-95）。
SELECT r.code, r.is_builtin
FROM admin_role ar
JOIN role r ON r.id = ar.role_id
WHERE ar.admin_id = $1;

-- name: ListPermissionCodesForAdmin :many
-- 只取自定义角色的 role_permission 行；内置角色的权限由代码定义，库里即使有行也不算（Ruling-95）。
SELECT DISTINCT p.code
FROM admin_role ar
JOIN role r ON r.id = ar.role_id AND r.is_builtin = FALSE
JOIN role_permission rp ON rp.role_id = ar.role_id
JOIN permission p ON p.id = rp.permission_id
WHERE ar.admin_id = $1;
