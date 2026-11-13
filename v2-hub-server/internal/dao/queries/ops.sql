-- 运营：Telegram 发件箱、后台任务状态、掉线告警、仪表盘与公开套餐（docs/03 §5.20）-----------

-- name: InsertTelegramOutbox :one
INSERT INTO telegram_outbox (chat_id, text, next_attempt_at, created_at)
VALUES (sqlc.arg(chat_id), sqlc.arg(text), sqlc.arg(queued_at), sqlc.arg(queued_at))
RETURNING id;

-- name: ClaimDueTelegram :many
-- 领取到期的待发消息并顺延下次尝试时间（租约），发送在事务外进行
UPDATE telegram_outbox m
SET attempts = m.attempts + 1, next_attempt_at = sqlc.arg(lease_until)
WHERE m.id IN (
    SELECT c.id FROM telegram_outbox c
    WHERE c.status = 'pending' AND c.next_attempt_at <= sqlc.arg(now) AND c.text IS NOT NULL
    ORDER BY c.next_attempt_at, c.id
    LIMIT sqlc.arg(batch)
    FOR UPDATE SKIP LOCKED
)
RETURNING m.id, m.chat_id, m.text::text AS text, m.attempts;

-- name: MarkTelegramSent :exec
UPDATE telegram_outbox SET status = 'sent', text = NULL, sent_at = sqlc.arg(sent_at), last_error = '' WHERE id = sqlc.arg(id);

-- name: MarkTelegramFailed :exec
UPDATE telegram_outbox SET status = 'failed', text = NULL, last_error = $2 WHERE id = $1;

-- name: SetTelegramRetry :exec
UPDATE telegram_outbox SET next_attempt_at = $2, last_error = $3 WHERE id = $1;

-- name: PurgeTelegramOutbox :execrows
DELETE FROM telegram_outbox WHERE created_at < $1;

-- name: GetJobState :one
SELECT value FROM job_state WHERE key = $1;

-- name: UpsertJobState :exec
INSERT INTO job_state (key, value, updated_at) VALUES ($1, $2, now())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();

-- name: ListAlertDueServers :many
-- 启用中、不在线达到阈值且这次掉线还没告警的服务器
SELECT id, name, status, last_error, offline_since::timestamptz AS offline_since
FROM server
WHERE NOT is_disabled AND status <> 'online' AND alert_sent_at IS NULL
  AND offline_since IS NOT NULL AND offline_since <= sqlc.arg(before)
ORDER BY id;

-- name: MarkServerAlerted :exec
UPDATE server SET alert_sent_at = sqlc.arg(alerted_at) WHERE id = sqlc.arg(id);

-- name: ListRecoveredServers :many
-- 发过告警、现在已恢复在线或已被停用的服务器
SELECT id, name, is_disabled, alert_sent_at::timestamptz AS alert_sent_at
FROM server
WHERE alert_sent_at IS NOT NULL AND (status = 'online' OR is_disabled)
ORDER BY id;

-- name: ClearServerAlert :exec
UPDATE server SET alert_sent_at = NULL, offline_since = CASE WHEN is_disabled THEN NULL ELSE offline_since END WHERE id = $1;

-- name: DashboardUsers :one
SELECT COUNT(*)::int AS total,
       COUNT(*) FILTER (WHERE u.status = 'active')::int AS active,
       COUNT(*) FILTER (WHERE u.created_at >= sqlc.arg(today_start)::timestamptz)::int AS new_today,
       COUNT(*) FILTER (WHERE u.created_at >= sqlc.arg(week_start)::timestamptz)::int AS new_7d,
       (SELECT COUNT(DISTINCT v.user_id) FROM provision v JOIN end_user e ON e.id = v.user_id AND e.deleted_at IS NULL WHERE v.is_online)::int AS online
FROM end_user u WHERE u.deleted_at IS NULL;

-- name: DailyTraffic :many
-- 平台时区下每个自然日的流量合计（上行 + 下行）
SELECT (h.hour AT TIME ZONE sqlc.arg(tz)::text)::date AS day, COALESCE(SUM(h.up + h.down), 0)::bigint AS bytes
FROM user_traffic_hourly h
WHERE h.hour >= sqlc.arg(since)
GROUP BY 1
ORDER BY 1;

-- name: DashboardServers :one
SELECT COUNT(*)::int AS total,
       COUNT(*) FILTER (WHERE status = 'online')::int AS online,
       (SELECT COUNT(*) FROM node n JOIN inbound i ON i.id = n.inbound_id WHERE NOT i.is_missing)::int AS node_count
FROM server;

-- name: ListOfflineServers :many
SELECT id, name, status, offline_since FROM server
WHERE NOT is_disabled AND status <> 'online'
ORDER BY offline_since NULLS LAST, id
LIMIT sqlc.arg(row_limit);

-- name: CountOpenTickets :one
SELECT COUNT(*)::int FROM ticket WHERE status = 'open';

-- name: CountUsersCreatedBetween :one
SELECT COUNT(*)::int FROM end_user WHERE deleted_at IS NULL AND created_at >= sqlc.arg(from_at) AND created_at < sqlc.arg(to_at);

-- name: CountTicketsCreatedBetween :one
SELECT COUNT(*)::int FROM ticket WHERE created_at >= sqlc.arg(from_at) AND created_at < sqlc.arg(to_at);

-- name: SumTrafficBetween :one
SELECT COALESCE(SUM(up + down), 0)::bigint FROM user_traffic_hourly WHERE hour >= sqlc.arg(from_at) AND hour < sqlc.arg(to_at);

-- name: SumTopupsBetween :one
SELECT COALESCE(SUM(amount), 0)::bigint FROM wallet_ledger
WHERE kind IN ('topup_code', 'topup_online') AND created_at >= sqlc.arg(from_at) AND created_at < sqlc.arg(to_at);

-- name: SumOrdersBetween :one
SELECT COALESCE(SUM(amount_paid), 0)::bigint FROM shop_order WHERE created_at >= sqlc.arg(from_at) AND created_at < sqlc.arg(to_at);

-- name: CountOfflineServers :one
SELECT COUNT(*)::int FROM server WHERE NOT is_disabled AND status <> 'online';
