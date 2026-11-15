-- 总览分析（docs/03 §5.20 总览分析，Ruling-66）。时间范围由 service 校验（7 / 30 / 90 天），自然日按传入的时区。

-- name: AnalyticsUserGrowth :many
SELECT (u.created_at AT TIME ZONE sqlc.arg(tz)::text)::date AS day, COUNT(*)::int AS new_users
FROM end_user u
WHERE u.deleted_at IS NULL AND u.created_at >= sqlc.arg(since)
GROUP BY 1
ORDER BY 1;

-- name: CountUsersCreatedBefore :one
SELECT COUNT(*)::int FROM end_user WHERE deleted_at IS NULL AND created_at < sqlc.arg(before);

-- name: AnalyticsActiveDays :many
-- 每个自然日有流量的去重用户数
SELECT (h.hour AT TIME ZONE sqlc.arg(tz)::text)::date AS day, COUNT(DISTINCT h.user_id)::int AS users
FROM user_traffic_hourly h
WHERE h.hour >= sqlc.arg(since) AND h.up + h.down > 0
GROUP BY 1
ORDER BY 1;

-- name: CountActiveUsersBetween :one
SELECT COUNT(DISTINCT user_id)::int FROM user_traffic_hourly
WHERE hour >= sqlc.arg(from_at) AND hour < sqlc.arg(to_at) AND up + down > 0;

-- name: AnalyticsUserBreakdown :one
-- 状态分布、到期分布与沉默用户，一次扫描
SELECT COUNT(*) FILTER (WHERE status = 'active')::int AS active,
       COUNT(*) FILTER (WHERE status = 'limited')::int AS limited,
       COUNT(*) FILTER (WHERE status = 'expired')::int AS expired,
       COUNT(*) FILTER (WHERE status = 'disabled')::int AS disabled,
       COUNT(*) FILTER (WHERE expires_at < sqlc.arg(now_at)::timestamptz)::int AS expiry_expired,
       COUNT(*) FILTER (WHERE expires_at >= sqlc.arg(now_at)::timestamptz AND expires_at < sqlc.arg(week_end)::timestamptz)::int AS expiry_week,
       COUNT(*) FILTER (WHERE expires_at >= sqlc.arg(week_end)::timestamptz AND expires_at < sqlc.arg(month_end)::timestamptz)::int AS expiry_month,
       COUNT(*) FILTER (WHERE expires_at >= sqlc.arg(month_end)::timestamptz)::int AS expiry_later,
       COUNT(*) FILTER (WHERE expires_at IS NULL)::int AS expiry_never,
       COUNT(*) FILTER (WHERE expires_at >= sqlc.arg(now_at)::timestamptz AND expires_at < sqlc.arg(week_end)::timestamptz
                              AND NOT auto_renew)::int AS expiry_week_manual,
       COUNT(*) FILTER (WHERE status = 'active'
                              AND (last_used_at IS NULL OR last_used_at < sqlc.arg(silent_before)::timestamptz))::int AS silent
FROM end_user
WHERE deleted_at IS NULL;

-- name: AnalyticsPlanUsers :many
SELECT u.plan_id, COALESCE(p.name, '')::text AS name, COUNT(*)::int AS users,
       COUNT(*) FILTER (WHERE u.status = 'active')::int AS active_users
FROM end_user u
LEFT JOIN plan p ON p.id = u.plan_id
WHERE u.deleted_at IS NULL
GROUP BY u.plan_id, p.name
ORDER BY 3 DESC, u.plan_id NULLS LAST
LIMIT sqlc.arg(row_limit);

-- name: AnalyticsTopUsers :many
SELECT t.user_id, u.username, COALESCE(p.name, '')::text AS plan_name, t.bytes
FROM (SELECT h.user_id, SUM(h.up + h.down)::bigint AS bytes
      FROM user_traffic_hourly h
      WHERE h.hour >= sqlc.arg(since)
      GROUP BY h.user_id
      HAVING SUM(h.up + h.down) > 0
      ORDER BY 2 DESC, h.user_id
      LIMIT sqlc.arg(row_limit)) t
JOIN end_user u ON u.id = t.user_id AND u.deleted_at IS NULL
LEFT JOIN plan p ON p.id = u.plan_id
ORDER BY t.bytes DESC, t.user_id;

-- name: AnalyticsSubscribeDays :many
SELECT (l.created_at AT TIME ZONE sqlc.arg(tz)::text)::date AS day, COUNT(*)::int AS pulls,
       COUNT(DISTINCT l.user_id)::int AS users
FROM subscribe_log l
WHERE l.created_at >= sqlc.arg(since)
GROUP BY 1
ORDER BY 1;

-- name: AnalyticsSubscribeClients :many
-- 客户端按 User-Agent 里出现的名字归类（strpos：只是分类用的包含判断，过滤走 created_at 索引）；
-- 顺序有讲究：v2rayNG 含 v2rayn，Hiddify / Stash 的 UA 里带 clash 字样，所以排在前面
SELECT c.client::text AS client, COUNT(*)::int AS pulls, COUNT(DISTINCT c.user_id)::int AS users
FROM (SELECT a.user_id,
             CASE
                 WHEN strpos(a.ua, 'v2rayng') > 0 THEN 'v2rayng'
                 WHEN strpos(a.ua, 'v2rayn') > 0 THEN 'v2rayn'
                 WHEN strpos(a.ua, 'hiddify') > 0 THEN 'hiddify'
                 WHEN strpos(a.ua, 'stash') > 0 THEN 'stash'
                 WHEN strpos(a.ua, 'shadowrocket') > 0 THEN 'shadowrocket'
                 WHEN strpos(a.ua, 'quantumult') > 0 THEN 'quantumult'
                 WHEN strpos(a.ua, 'surge') > 0 THEN 'surge'
                 WHEN strpos(a.ua, 'loon') > 0 THEN 'loon'
                 WHEN strpos(a.ua, 'nekobox') > 0 OR strpos(a.ua, 'nekoray') > 0 THEN 'nekobox'
                 WHEN strpos(a.ua, 'sing-box') > 0 OR strpos(a.ua, 'singbox') > 0
                      OR starts_with(a.ua, 'sfa/') OR starts_with(a.ua, 'sfi/') OR starts_with(a.ua, 'sfm/') THEN 'singbox'
                 WHEN strpos(a.ua, 'clash') > 0 OR strpos(a.ua, 'mihomo') > 0 THEN 'clash'
                 ELSE 'other'
             END AS client
      FROM (SELECT l.user_id, lower(l.user_agent) AS ua
            FROM subscribe_log l
            WHERE l.created_at >= sqlc.arg(since)) a) c
GROUP BY c.client
ORDER BY 3 DESC, 2 DESC, 1;

-- name: AnalyticsSubscribeFormats :many
SELECT l.format, COUNT(*)::int AS pulls
FROM subscribe_log l
WHERE l.created_at >= sqlc.arg(since)
GROUP BY l.format
ORDER BY 2 DESC, 1;

-- name: AnalyticsTrafficDays :many
SELECT (h.hour AT TIME ZONE sqlc.arg(tz)::text)::date AS day,
       COALESCE(SUM(h.up), 0)::bigint AS up, COALESCE(SUM(h.down), 0)::bigint AS down
FROM user_traffic_hourly h
WHERE h.hour >= sqlc.arg(since)
GROUP BY 1
ORDER BY 1;

-- name: AnalyticsTrafficHours :many
-- 一天中每个小时（平台时区）在窗口内的流量合计；日均由 service 除以天数
SELECT EXTRACT(HOUR FROM (h.hour AT TIME ZONE sqlc.arg(tz)::text))::int AS hour_of_day,
       COALESCE(SUM(h.up + h.down), 0)::bigint AS bytes
FROM user_traffic_hourly h
WHERE h.hour >= sqlc.arg(since)
GROUP BY 1
ORDER BY 1;

-- name: AddServerTraffic :exec
INSERT INTO server_traffic_hourly (server_id, hour, up, down) VALUES ($1, $2, $3, $4)
ON CONFLICT (server_id, hour) DO UPDATE SET up = server_traffic_hourly.up + EXCLUDED.up, down = server_traffic_hourly.down + EXCLUDED.down;

-- name: AnalyticsTrafficServers :many
SELECT t.server_id, s.name, t.up, t.down
FROM (SELECT h.server_id, SUM(h.up)::bigint AS up, SUM(h.down)::bigint AS down
      FROM server_traffic_hourly h
      WHERE h.hour >= sqlc.arg(since)
      GROUP BY h.server_id
      ORDER BY SUM(h.up + h.down) DESC, h.server_id
      LIMIT sqlc.arg(row_limit)) t
JOIN server s ON s.id = t.server_id
ORDER BY t.up + t.down DESC, t.server_id;

-- name: AnalyticsServerSamples :many
-- 启用中的服务器在窗口内的负载采样数（可用率的分子）
SELECT s.id, s.name, s.created_at,
       (SELECT COUNT(*) FROM server_metric m WHERE m.server_id = s.id AND m.bucket_at >= sqlc.arg(since))::int AS samples
FROM server s
WHERE NOT s.is_disabled
ORDER BY s.id
LIMIT sqlc.arg(row_limit);

-- name: AnalyticsOnlineSeries :many
-- 各服务器同一 5 分钟时段的在线 client 数之和
SELECT m.bucket_at, SUM(m.online_clients)::int AS connections
FROM server_metric m
WHERE m.bucket_at >= sqlc.arg(since)
GROUP BY m.bucket_at
ORDER BY m.bucket_at;

-- name: AnalyticsOnlineSeriesHourly :many
SELECT date_bin('1 hour', t.bucket_at, TIMESTAMPTZ '2000-01-01 00:00:00+00')::timestamptz AS bucket_at,
       round(avg(t.connections))::int AS connections
FROM (SELECT m.bucket_at, SUM(m.online_clients) AS connections
      FROM server_metric m
      WHERE m.bucket_at >= sqlc.arg(since)
      GROUP BY m.bucket_at) t
GROUP BY 1
ORDER BY 1;

-- name: AnalyticsLatestLoads :many
-- 每台启用中的服务器在窗口内最新的一条采样
SELECT DISTINCT ON (m.server_id) m.server_id, s.name, m.cpu, m.online_clients
FROM server_metric m
JOIN server s ON s.id = m.server_id AND NOT s.is_disabled
WHERE m.bucket_at >= sqlc.arg(since)
ORDER BY m.server_id, m.bucket_at DESC;

-- name: AnalyticsOrderKinds :many
SELECT o.kind, COUNT(*)::int AS orders, COALESCE(SUM(o.amount_paid), 0)::bigint AS paid
FROM shop_order o
WHERE o.created_at >= sqlc.arg(since)
GROUP BY o.kind;

-- name: AnalyticsOrderSummary :one
-- 试用单数、付费用户数（按下单时的用户名去重）与实付合计
SELECT COUNT(*) FILTER (WHERE o.is_trial)::int AS trial_count,
       COUNT(DISTINCT o.username) FILTER (WHERE o.amount_paid > 0)::int AS payers,
       COALESCE(SUM(o.amount_paid), 0)::bigint AS paid
FROM shop_order o
WHERE o.created_at >= sqlc.arg(since);

-- name: AnalyticsPlanSales :many
SELECT o.plan_id, COALESCE(p.name, '')::text AS name, COUNT(*)::int AS orders,
       COALESCE(SUM(o.amount_paid), 0)::bigint AS paid
FROM shop_order o
LEFT JOIN plan p ON p.id = o.plan_id
WHERE o.created_at >= sqlc.arg(since) AND o.kind IN ('plan', 'renew')
GROUP BY o.plan_id, p.name
ORDER BY 4 DESC, 3 DESC, o.plan_id NULLS LAST
LIMIT sqlc.arg(row_limit);

-- name: AnalyticsFunnel :one
-- 窗口内注册的用户 → 其中下过试用单的 → 其中有过实付 > 0 订单的
SELECT COUNT(*)::int AS registered,
       COUNT(*) FILTER (WHERE EXISTS (SELECT 1 FROM shop_order o WHERE o.user_id = u.id AND o.is_trial))::int AS trial,
       COUNT(*) FILTER (WHERE EXISTS (SELECT 1 FROM shop_order o WHERE o.user_id = u.id AND o.amount_paid > 0))::int AS paid
FROM end_user u
WHERE u.deleted_at IS NULL AND u.created_at >= sqlc.arg(since);

-- name: AnalyticsPayments :one
SELECT COUNT(*) FILTER (WHERE status = 'paid')::int AS paid,
       COUNT(*) FILTER (WHERE status = 'expired')::int AS expired,
       COUNT(*) FILTER (WHERE status = 'pending')::int AS pending
FROM payment
WHERE created_at >= sqlc.arg(since);

-- name: AnalyticsCoupons :many
SELECT o.coupon_code, COUNT(*)::int AS orders, COALESCE(SUM(o.amount_discount), 0)::bigint AS discount,
       COALESCE(SUM(o.amount_paid), 0)::bigint AS paid
FROM shop_order o
WHERE o.created_at >= sqlc.arg(since) AND o.coupon_code <> ''
GROUP BY o.coupon_code
ORDER BY 2 DESC, o.coupon_code
LIMIT sqlc.arg(row_limit);

-- name: AnalyticsTicketBacklog :one
SELECT COUNT(*)::int AS open, MIN(last_reply_at)::timestamptz AS oldest_at
FROM ticket
WHERE status = 'open';

-- name: AnalyticsFirstReply :one
-- 窗口内新建且已有管理员回复的工单：数量与首次回复时长合计（整秒）
SELECT COUNT(*)::int AS replied,
       COALESCE(SUM(EXTRACT(EPOCH FROM (f.first_at - t.created_at))), 0)::bigint AS total_seconds
FROM ticket t
JOIN LATERAL (SELECT MIN(m.created_at) AS first_at
              FROM ticket_message m
              WHERE m.ticket_id = t.id AND m.author_kind = 'admin') f ON f.first_at IS NOT NULL
WHERE t.created_at >= sqlc.arg(since);

-- name: AnalyticsFailedProvisions :many
SELECT v.server_id, s.name, COUNT(*)::int AS failed
FROM provision v
JOIN server s ON s.id = v.server_id
WHERE v.status = 'failed'
GROUP BY v.server_id, s.name
ORDER BY 3 DESC, v.server_id;

-- name: AnalyticsChainFailed :many
SELECT a.server_id, s.name, a.fail_count, a.last_attempt_at
FROM chain_apply a
JOIN server s ON s.id = a.server_id
WHERE a.status = 'failed'
ORDER BY a.fail_count DESC, a.server_id
LIMIT sqlc.arg(row_limit);

-- name: AnalyticsInboundIssues :one
SELECT COUNT(*) FILTER (WHERE drift_detected_at IS NOT NULL AND NOT is_missing)::int AS drift,
       COUNT(*) FILTER (WHERE is_missing)::int AS missing
FROM inbound;

-- name: AnalyticsOutbox :one
SELECT (SELECT COUNT(*) FROM mail_outbox mo WHERE mo.status = 'pending')::int AS mail_pending,
       (SELECT COUNT(*) FROM mail_outbox mf WHERE mf.status = 'failed' AND mf.created_at >= sqlc.arg(since))::int AS mail_failed,
       (SELECT COUNT(*) FROM telegram_outbox tp WHERE tp.status = 'pending')::int AS telegram_pending,
       (SELECT COUNT(*) FROM telegram_outbox tf WHERE tf.status = 'failed' AND tf.created_at >= sqlc.arg(since))::int AS telegram_failed;
