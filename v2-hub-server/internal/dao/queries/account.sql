-- 注册、邀请码与账号自助（docs/03 §5.8）。

-- name: GetInviteCodeForUpdate :one
-- 注册时锁住邀请码行，计数与上限的判断在同一事务内完成（并发注册不超发）
SELECT c.id, c.owner_user_id, c.max_uses, c.used_count, c.expires_at, c.is_disabled,
       (o.id IS NOT NULL AND (o.deleted_at IS NOT NULL OR o.is_disabled))::boolean AS is_owner_inactive
FROM invite_code c
LEFT JOIN end_user o ON o.id = c.owner_user_id
WHERE c.code = $1
FOR UPDATE OF c;

-- name: BumpInviteCodeUse :exec
UPDATE invite_code SET used_count = used_count + 1 WHERE id = $1;

-- name: EnsureUserInviteCode :exec
-- 首次查看时生成个人码；已有（或随机码撞了别人的）时什么都不做，由调用方重读
INSERT INTO invite_code (code, owner_user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: GetUserInviteCode :one
SELECT id, code, used_count, is_disabled FROM invite_code WHERE owner_user_id = $1;

-- name: ResetUserInviteCode :execrows
-- 原地换码：旧码作废，已用次数不清零（防止靠重置绕过次数上限）
UPDATE invite_code SET code = $2 WHERE owner_user_id = $1;

-- name: ListInvitees :many
SELECT username, created_at FROM end_user WHERE invited_by = $1 AND deleted_at IS NULL ORDER BY id DESC LIMIT $2;

-- name: ListInviteCodes :many
SELECT c.id, c.code, c.owner_user_id, COALESCE(o.username, '')::text AS owner_username, c.max_uses, c.used_count, c.expires_at,
       c.is_disabled, c.remark, c.created_at
FROM invite_code c
LEFT JOIN end_user o ON o.id = c.owner_user_id
WHERE (sqlc.arg(kind)::text = ''
       OR (sqlc.arg(kind)::text = 'admin' AND c.owner_user_id IS NULL)
       OR (sqlc.arg(kind)::text = 'user' AND c.owner_user_id IS NOT NULL))
  AND (sqlc.arg(id)::bigint = 0 OR c.id = sqlc.arg(id)::bigint)
ORDER BY c.id DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: CountInviteCodes :one
SELECT count(*) FROM invite_code c
WHERE (sqlc.arg(kind)::text = ''
       OR (sqlc.arg(kind)::text = 'admin' AND c.owner_user_id IS NULL)
       OR (sqlc.arg(kind)::text = 'user' AND c.owner_user_id IS NOT NULL));

-- name: CreateAdminInviteCode :one
INSERT INTO invite_code (code, max_uses, expires_at, is_disabled, remark, created_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id;

-- name: GetInviteCodeOwner :one
SELECT owner_user_id FROM invite_code WHERE id = $1 FOR UPDATE;

-- name: UpdateAdminInviteCode :exec
UPDATE invite_code SET max_uses = $2, expires_at = $3, is_disabled = $4, remark = $5 WHERE id = $1;

-- name: SetInviteCodeDisabled :exec
UPDATE invite_code SET is_disabled = $2 WHERE id = $1;

-- name: DeleteInviteCode :exec
DELETE FROM invite_code WHERE id = $1;

-- name: GetEndUserEmail :one
SELECT email FROM end_user WHERE id = $1 AND deleted_at IS NULL;
