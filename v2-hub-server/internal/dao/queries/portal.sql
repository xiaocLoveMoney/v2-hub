-- 客户后台：终端用户会话、两步验证、登录记录（docs/03 §5.3）。

-- name: GetEndUserForLogin :one
-- 待删除的用户不可登录；停用的用户可以登录（docs/03 §5.3）。
SELECT id, username, password_hash, status, is_disabled, totp_secret_enc, totp_last_step
FROM end_user
WHERE username = $1 AND deleted_at IS NULL;

-- name: GetEndUserLoginSecretHash :one
SELECT password_hash FROM end_user WHERE id = $1 AND deleted_at IS NULL;

-- name: CreateUserSession :one
INSERT INTO user_session (token_hash, user_id, csrf_token, client_ip, user_agent, expires_at, idle_until)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id;

-- name: GetUserSessionByTokenHash :one
SELECT s.id, s.user_id, s.csrf_token, s.expires_at, s.idle_until, u.username, u.status, u.is_disabled,
       (u.totp_secret_enc IS NOT NULL)::boolean AS totp_enabled,
       (u.deleted_at IS NOT NULL)::boolean AS is_deleted
FROM user_session s
JOIN end_user u ON u.id = s.user_id
WHERE s.token_hash = $1;

-- name: TouchUserSession :exec
UPDATE user_session SET idle_until = $2 WHERE id = $1;

-- name: DeleteUserSessionByID :exec
DELETE FROM user_session WHERE id = $1;

-- name: DeleteUserSessionsByUser :exec
DELETE FROM user_session WHERE user_id = $1;

-- name: DeleteOtherUserSessions :exec
DELETE FROM user_session WHERE user_id = sqlc.arg(user_id) AND id <> sqlc.arg(keep_id);

-- name: DeleteExpiredUserSessions :execrows
DELETE FROM user_session WHERE expires_at <= $1 OR idle_until <= $1;

-- name: GetUserTOTP :one
SELECT totp_secret_enc, totp_pending_enc, totp_last_step FROM end_user WHERE id = $1 AND deleted_at IS NULL;

-- name: SetUserTOTPPending :exec
UPDATE end_user SET totp_pending_enc = $2, updated_at = now() WHERE id = $1;

-- name: EnableUserTOTP :exec
-- 待定密钥已由调用方校验并重新加密为正式密钥后传入
UPDATE end_user
SET totp_secret_enc = $2, totp_pending_enc = NULL, totp_last_step = $3, updated_at = now()
WHERE id = $1;

-- name: ClearUserTOTP :execrows
UPDATE end_user
SET totp_secret_enc = NULL, totp_pending_enc = NULL, totp_last_step = 0, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL;

-- name: AdvanceUserTOTPStep :execrows
-- 原子地推进已用步长；返回 0 行表示该步长已被用过（并发重放）
UPDATE end_user SET totp_last_step = sqlc.arg(step) WHERE id = sqlc.arg(id) AND totp_last_step < sqlc.arg(step);

-- name: InsertUserLoginLog :exec
INSERT INTO user_login_log (user_id, client_ip, is_success) VALUES ($1, $2, $3);

-- name: ListUserLoginLogs :many
SELECT client_ip, is_success, created_at
FROM user_login_log
WHERE user_id = $1
ORDER BY created_at DESC, id DESC
LIMIT $2;

-- name: DeleteOldUserLoginLogs :execrows
DELETE FROM user_login_log WHERE created_at < $1;
