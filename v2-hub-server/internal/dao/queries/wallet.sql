-- 余额、流水与充值码（docs/03 §5.14、§5.15）-------------------------------------------

-- name: LockUserBalance :one
-- 改余额前先锁用户行；同一事务里其它查询再锁同一行不会阻塞
SELECT id, username, balance, is_disabled FROM end_user WHERE id = $1 AND deleted_at IS NULL FOR UPDATE;

-- name: SetUserBalance :exec
UPDATE end_user SET balance = $2, updated_at = now() WHERE id = $1;

-- name: InsertLedger :one
INSERT INTO wallet_ledger (user_id, username, kind, amount, balance_after, ref_type, ref_id, remark, admin_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id;

-- name: ListLedger :many
SELECT l.id, l.user_id, l.username, l.kind, l.amount, l.balance_after, l.ref_type, l.ref_id, l.remark, l.created_at,
       COALESCE(a.username, '')::text AS admin_username
FROM wallet_ledger l
LEFT JOIN admin a ON a.id = l.admin_id
WHERE (sqlc.arg(user_id)::bigint = 0 OR l.user_id = sqlc.arg(user_id)::bigint)
  AND (sqlc.arg(kind)::text = '' OR l.kind = sqlc.arg(kind)::text)
ORDER BY l.id DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: CountLedger :one
SELECT count(*) FROM wallet_ledger l
WHERE (sqlc.arg(user_id)::bigint = 0 OR l.user_id = sqlc.arg(user_id)::bigint)
  AND (sqlc.arg(kind)::text = '' OR l.kind = sqlc.arg(kind)::text);

-- name: InsertTopupCode :exec
INSERT INTO topup_code (code_hash, code_enc, aad_key, code_tail, amount, batch, expires_at, created_by, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: GetTopupCodeByHashForUpdate :one
-- 兑换时按 HMAC 查找并锁行，一码只能用一次（Ruling-31）
SELECT id, amount, expires_at, is_disabled, used_at FROM topup_code WHERE code_hash = $1 FOR UPDATE;

-- name: MarkTopupCodeUsed :exec
UPDATE topup_code SET used_by = $2, used_by_username = $3, used_at = $4 WHERE id = $1;

-- name: ListTopupCodes :many
-- 状态由 used_at / is_disabled / expires_at 推出：unused / used / disabled / expired；now 由服务端时钟传入
SELECT id, code_tail, amount, batch, expires_at, is_disabled, used_by, used_by_username, used_at, created_at
FROM topup_code c
WHERE (sqlc.arg(batch)::text = '' OR c.batch = sqlc.arg(batch)::text)
  AND (sqlc.arg(status)::text = ''
       OR (sqlc.arg(status)::text = 'used' AND c.used_at IS NOT NULL)
       OR (sqlc.arg(status)::text = 'disabled' AND c.used_at IS NULL AND c.is_disabled)
       OR (sqlc.arg(status)::text = 'expired' AND c.used_at IS NULL AND NOT c.is_disabled AND c.expires_at IS NOT NULL AND c.expires_at <= sqlc.arg(now)::timestamptz)
       OR (sqlc.arg(status)::text = 'unused' AND c.used_at IS NULL AND NOT c.is_disabled AND (c.expires_at IS NULL OR c.expires_at > sqlc.arg(now)::timestamptz)))
ORDER BY c.id DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: CountTopupCodes :one
SELECT count(*) FROM topup_code c
WHERE (sqlc.arg(batch)::text = '' OR c.batch = sqlc.arg(batch)::text)
  AND (sqlc.arg(status)::text = ''
       OR (sqlc.arg(status)::text = 'used' AND c.used_at IS NOT NULL)
       OR (sqlc.arg(status)::text = 'disabled' AND c.used_at IS NULL AND c.is_disabled)
       OR (sqlc.arg(status)::text = 'expired' AND c.used_at IS NULL AND NOT c.is_disabled AND c.expires_at IS NOT NULL AND c.expires_at <= sqlc.arg(now)::timestamptz)
       OR (sqlc.arg(status)::text = 'unused' AND c.used_at IS NULL AND NOT c.is_disabled AND (c.expires_at IS NULL OR c.expires_at > sqlc.arg(now)::timestamptz)));

-- name: GetTopupCodeForUpdate :one
SELECT id, code_enc, aad_key, code_tail, amount, batch, expires_at, is_disabled, used_by, used_by_username, used_at, created_at
FROM topup_code WHERE id = $1 FOR UPDATE;

-- name: SetTopupCodeDisabled :exec
UPDATE topup_code SET is_disabled = $2 WHERE id = $1;

-- name: DeleteTopupCode :exec
DELETE FROM topup_code WHERE id = $1;
