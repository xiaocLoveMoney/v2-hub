-- 在线充值单、返利列表与收支统计（docs/03 §5.13–§5.16）--------------------------------

-- name: InsertPayment :one
INSERT INTO payment (trade_no, user_id, username, gateway, amount, expires_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id;

-- name: GetPaymentByTradeNoForUpdate :one
-- 入账前锁住充值单，重复通知只入账一次
SELECT id, user_id, gateway, amount, status, gateway_trade_no FROM payment WHERE trade_no = $1 FOR UPDATE;

-- name: MarkPaymentPaid :exec
UPDATE payment SET status = 'paid', gateway_trade_no = $2, paid_at = $3 WHERE id = $1;

-- name: ExpirePayments :execrows
UPDATE payment SET status = 'expired' WHERE status = 'pending' AND expires_at <= $1;

-- name: ListPayments :many
SELECT id, trade_no, user_id, username, gateway, amount, status, paid_at, created_at
FROM payment p
WHERE (sqlc.arg(user_id)::bigint = 0 OR p.user_id = sqlc.arg(user_id)::bigint)
  AND (sqlc.arg(status)::text = '' OR p.status = sqlc.arg(status)::text)
ORDER BY p.id DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: CountPayments :one
SELECT count(*) FROM payment p
WHERE (sqlc.arg(user_id)::bigint = 0 OR p.user_id = sqlc.arg(user_id)::bigint)
  AND (sqlc.arg(status)::text = '' OR p.status = sqlc.arg(status)::text);

-- name: ListRebates :many
SELECT r.id, r.inviter_username, r.invitee_username, o.order_no, r.order_amount, r.percent, r.amount, r.created_at
FROM rebate r
JOIN shop_order o ON o.id = r.order_id
WHERE (sqlc.arg(inviter_id)::bigint = 0 OR r.inviter_id = sqlc.arg(inviter_id)::bigint)
ORDER BY r.id DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: CountRebates :one
SELECT count(*) FROM rebate r WHERE (sqlc.arg(inviter_id)::bigint = 0 OR r.inviter_id = sqlc.arg(inviter_id)::bigint);

-- name: DailyTopups :many
-- 按平台时区的自然日汇总充值入账（充值码 + 在线充值）
SELECT (l.created_at AT TIME ZONE sqlc.arg(tz)::text)::date AS day, COALESCE(sum(l.amount), 0)::bigint AS total
FROM wallet_ledger l
WHERE l.kind IN ('topup_code', 'topup_online') AND l.created_at >= sqlc.arg(since)::timestamptz
GROUP BY 1;

-- name: DailyOrders :many
SELECT (o.created_at AT TIME ZONE sqlc.arg(tz)::text)::date AS day, COALESCE(sum(o.amount_paid), 0)::bigint AS total, count(*)::int AS order_count
FROM shop_order o
WHERE o.created_at >= sqlc.arg(since)::timestamptz
GROUP BY 1;

-- name: DailyRebates :many
SELECT (r.created_at AT TIME ZONE sqlc.arg(tz)::text)::date AS day, COALESCE(sum(r.amount), 0)::bigint AS total
FROM rebate r
WHERE r.created_at >= sqlc.arg(since)::timestamptz
GROUP BY 1;

-- name: SumUserBalances :one
SELECT COALESCE(sum(balance), 0)::bigint FROM end_user WHERE deleted_at IS NULL;
