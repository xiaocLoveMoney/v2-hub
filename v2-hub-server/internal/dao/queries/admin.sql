-- name: GetAdminByUsername :one
SELECT id, username, password_hash, is_disabled, totp_secret_enc, totp_last_step, is_owner, kind
FROM admin
WHERE username = $1;

-- name: GetAdminByID :one
-- 行结构须与 ListAdmins 逐字段相同（service 层直接做类型转换）。含代理账号：重置密码、清两步验证对代理照常可用（docs/03 §5.23）。
SELECT id, username, is_disabled, (totp_secret_enc IS NOT NULL)::boolean AS totp_enabled, last_login_at, created_at, is_owner, kind
FROM admin
WHERE id = $1;

-- name: CreateAdmin :one
-- kind 为 staff（员工）或 agent（代理，不持角色，Ruling-101），创建后不可改
INSERT INTO admin (username, password_hash, kind)
VALUES (sqlc.arg(username), sqlc.arg(password_hash), sqlc.arg(kind))
RETURNING id, created_at;

-- name: UpdateAdminDisabled :exec
UPDATE admin SET is_disabled = $2, updated_at = now() WHERE id = $1;

-- name: UpdateAdminPasswordHash :execrows
UPDATE admin SET password_hash = $2, updated_at = now() WHERE id = $1;

-- name: GetAdminSecrets :one
-- 本人改密码与二次验证用（Ruling-96、Ruling-100）。
SELECT password_hash, totp_secret_enc, totp_last_step FROM admin WHERE id = $1;

-- name: UpdateAdminLastLogin :exec
UPDATE admin SET last_login_at = now() WHERE id = $1;

-- name: ListAdmins :many
-- 只列员工，代理见 agent.sql（Ruling-101）
SELECT id, username, is_disabled, (totp_secret_enc IS NOT NULL)::boolean AS totp_enabled, last_login_at, created_at, is_owner, kind
FROM admin
WHERE kind = 'staff'
ORDER BY id
LIMIT $1 OFFSET $2;

-- name: CountAdmins :one
SELECT COUNT(*) FROM admin WHERE kind = 'staff';

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
-- 用于 R5：除 excluded_admin_id 外，仍持有该角色且启用的员工数（代理即使库里有角色行也不算，Ruling-101）。
SELECT COUNT(DISTINCT a.id)
FROM admin a
JOIN admin_role ar ON ar.admin_id = a.id
WHERE ar.role_id = sqlc.arg(role_id)
  AND a.kind = 'staff'
  AND a.is_disabled = FALSE
  AND a.id <> sqlc.arg(excluded_admin_id);

-- name: LockSuperAdminRole :exec
-- R5 的并发保护：改动可能影响 super_admin 数量的事务先锁住该角色行，串行化判断。
SELECT id FROM role WHERE code = 'super_admin' FOR UPDATE;

-- 所有者（Ruling-99）--------------------------------------------------------

-- name: GetOwnerAdminID :one
SELECT id FROM admin WHERE is_owner;

-- name: ClearAdminOwner :exec
UPDATE admin SET is_owner = FALSE, updated_at = now() WHERE is_owner;

-- name: SetAdminOwner :execrows
-- 只能设给启用状态的员工（代理不能成为所有者，Ruling-101）；是否持有 super_admin 由调用方在同一事务里先查
UPDATE admin SET is_owner = TRUE, updated_at = now() WHERE id = $1 AND is_disabled = FALSE AND kind = 'staff';

-- 两步验证（docs/03 §4.3）---------------------------------------------------

-- name: GetAdminTOTP :one
SELECT totp_secret_enc, totp_pending_enc, totp_last_step FROM admin WHERE id = $1;

-- name: SetAdminTOTPPending :exec
UPDATE admin SET totp_pending_enc = $2, updated_at = now() WHERE id = $1;

-- name: EnableAdminTOTP :exec
-- 待定密钥已由调用方校验并重新加密为正式密钥后传入
UPDATE admin
SET totp_secret_enc = $2, totp_pending_enc = NULL, totp_last_step = $3, updated_at = now()
WHERE id = $1;

-- name: ClearAdminTOTP :execrows
UPDATE admin
SET totp_secret_enc = NULL, totp_pending_enc = NULL, totp_last_step = 0, updated_at = now()
WHERE id = $1;

-- name: AdvanceAdminTOTPStep :execrows
-- 原子地推进已用步长；返回 0 行表示该步长已被用过（并发重放）
UPDATE admin SET totp_last_step = sqlc.arg(step) WHERE id = sqlc.arg(id) AND totp_last_step < sqlc.arg(step);
