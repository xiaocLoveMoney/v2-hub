-- 系统设置、发件箱与邮箱验证码（docs/03 §5.6、§5.7）。

-- name: ListSettings :many
SELECT key, value FROM setting;

-- name: UpsertSetting :exec
INSERT INTO setting (key, value, updated_by, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_by = excluded.updated_by, updated_at = now();

-- name: InsertMailOutbox :one
-- 时间由服务时钟给出（不用库的 now()），与领取时的比较基准一致
INSERT INTO mail_outbox (aad_key, recipient, subject, body_enc, priority, next_attempt_at, created_at)
VALUES ($1, $2, $3, $4, $5, sqlc.arg(queued_at), sqlc.arg(queued_at))
RETURNING id;

-- name: ClaimDueMail :many
-- 领取到期的待发邮件并顺延下次尝试时间（租约），发送在事务外进行；并发的任务实例不会领到同一封
UPDATE mail_outbox m
SET attempts = m.attempts + 1, next_attempt_at = sqlc.arg(lease_until)
WHERE m.id IN (
    SELECT c.id FROM mail_outbox c
    WHERE c.status = 'pending' AND c.next_attempt_at <= sqlc.arg(now)
    ORDER BY c.priority, c.next_attempt_at
    LIMIT sqlc.arg(batch)
    FOR UPDATE SKIP LOCKED
)
RETURNING m.id, m.aad_key, m.recipient, m.subject, m.body_enc, m.attempts, m.priority;

-- name: MarkMailSent :exec
-- 发送成功后清空正文（docs/03 §5.7 发件箱）
UPDATE mail_outbox SET status = 'sent', body_enc = NULL, sent_at = now(), last_error = '' WHERE id = $1;

-- name: MarkMailFailed :exec
-- 放弃重试：清空正文，只留收件人、主题与最后一次错误
UPDATE mail_outbox SET status = 'failed', body_enc = NULL, last_error = $2 WHERE id = $1;

-- name: SetMailRetry :exec
UPDATE mail_outbox SET next_attempt_at = $2, last_error = $3 WHERE id = $1;

-- name: ListMailOutbox :many
SELECT id, recipient, subject, status, attempts, last_error, created_at, sent_at
FROM mail_outbox
WHERE (sqlc.arg(status)::text = '' OR status = sqlc.arg(status)::text)
ORDER BY id DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: CountMailOutbox :one
SELECT count(*) FROM mail_outbox WHERE (sqlc.arg(status)::text = '' OR status = sqlc.arg(status)::text);

-- name: PurgeMailOutbox :execrows
DELETE FROM mail_outbox WHERE created_at < $1;

-- name: CountEmailCodesByEmailSince :one
SELECT count(*) FROM email_code WHERE email = $1 AND created_at >= $2;

-- name: CountEmailCodesByIPSince :one
SELECT count(*) FROM email_code WHERE client_ip = $1 AND created_at >= $2;

-- name: SupersedeEmailCodes :exec
-- 同一邮箱同一用途只有最新一条有效
UPDATE email_code SET consumed_at = now() WHERE email = $1 AND purpose = $2 AND consumed_at IS NULL;

-- name: InsertEmailCode :exec
INSERT INTO email_code (email, purpose, code_hash, expires_at, client_ip, created_at) VALUES ($1, $2, $3, $4, $5, $6);

-- name: GetActiveEmailCodeForUpdate :one
SELECT id, code_hash, attempts, expires_at
FROM email_code
WHERE email = $1 AND purpose = $2 AND consumed_at IS NULL
ORDER BY id DESC
LIMIT 1
FOR UPDATE;

-- name: BumpEmailCodeAttempts :exec
-- 校验失败计一次；达到上限即作废
UPDATE email_code
SET attempts = attempts + 1,
    consumed_at = CASE WHEN attempts + 1 >= sqlc.arg(max_attempts)::int THEN now() ELSE consumed_at END
WHERE id = sqlc.arg(id);

-- name: ConsumeEmailCode :execrows
UPDATE email_code SET consumed_at = now() WHERE id = $1 AND consumed_at IS NULL AND expires_at > $2;

-- name: PurgeEmailCodes :execrows
DELETE FROM email_code WHERE created_at < $1;

-- name: GetEndUserIDByEmail :one
SELECT id FROM end_user WHERE email = sqlc.arg(email)::text AND deleted_at IS NULL;

-- name: ExistsEndUserIdentity :one
-- 邮箱是否已被占用：等于某个用户的邮箱，或等于某个（存量、含 @ 的）用户名（docs/03 §5.8 冲突）
SELECT EXISTS (SELECT 1 FROM end_user WHERE email = sqlc.arg(email)::text OR lower(username) = sqlc.arg(email)::text);

-- name: ExistsPlan :one
SELECT EXISTS (SELECT 1 FROM plan WHERE id = $1);
