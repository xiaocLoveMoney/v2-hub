-- name: CreateAdminSession :exec
INSERT INTO admin_session (token_hash, admin_id, csrf_token, client_ip, user_agent, expires_at, idle_until)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetAdminSessionByTokenHash :one
-- 同时取管理员状态：被禁用的管理员会话视为无效（R7）。
SELECT s.id, s.admin_id, s.csrf_token, s.expires_at, s.idle_until, a.username, a.is_disabled
FROM admin_session s
JOIN admin a ON a.id = s.admin_id
WHERE s.token_hash = $1;

-- name: TouchAdminSession :exec
UPDATE admin_session SET idle_until = $2 WHERE id = $1;

-- name: DeleteAdminSessionByTokenHash :exec
DELETE FROM admin_session WHERE token_hash = $1;

-- name: DeleteAdminSessionByID :exec
DELETE FROM admin_session WHERE id = $1;

-- name: DeleteAdminSessionsByAdmin :exec
DELETE FROM admin_session WHERE admin_id = $1;

-- name: DeleteExpiredAdminSessions :execrows
DELETE FROM admin_session WHERE expires_at <= $1 OR idle_until <= $1;
