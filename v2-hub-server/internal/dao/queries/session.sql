-- name: CreateAdminSession :one
-- reauth_until：登录成功视为刚做过二次验证（Ruling-100）
INSERT INTO admin_session (token_hash, admin_id, csrf_token, client_ip, user_agent, expires_at, idle_until, reauth_until)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id;

-- name: GetAdminSessionByTokenHash :one
-- 同时取管理员状态：被禁用的管理员会话视为无效（R7）。
SELECT s.id, s.admin_id, s.csrf_token, s.expires_at, s.idle_until, a.username, a.is_disabled,
       (a.totp_secret_enc IS NOT NULL)::boolean AS totp_enabled, s.reauth_until, a.is_owner
FROM admin_session s
JOIN admin a ON a.id = s.admin_id
WHERE s.token_hash = $1;

-- name: TouchAdminSession :exec
UPDATE admin_session SET idle_until = $2 WHERE id = $1;

-- name: SetAdminSessionReauth :exec
UPDATE admin_session SET reauth_until = $2 WHERE id = $1;

-- name: DeleteAdminSessionByID :exec
DELETE FROM admin_session WHERE id = $1;

-- name: DeleteAdminSessionsByAdmin :exec
DELETE FROM admin_session WHERE admin_id = $1;

-- name: DeleteOtherAdminSessions :exec
-- 本人改密码：保留当前会话，其余失效（R7）
DELETE FROM admin_session WHERE admin_id = sqlc.arg(admin_id) AND id <> sqlc.arg(keep_id);

-- name: DeleteExpiredAdminSessions :execrows
DELETE FROM admin_session WHERE expires_at <= $1 OR idle_until <= $1;
