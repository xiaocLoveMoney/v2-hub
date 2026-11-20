-- 服务器（docs/03 §2、§3 T1–T4、§5.1）---------------------------------------

-- name: ListServers :many
SELECT s.id, s.name, s.provider_type, s.base_url, s.tls_mode, s.cert_pin_sha256, s.region, s.remark,
       s.is_disabled, s.status, s.last_error, s.last_seen_at, s.panel_version, s.status_snapshot,
       s.last_synced_at, s.created_at, s.client_flow,
       s.public_ipv4, s.public_ipv6, s.online_clients,
       s.price_cents, s.price_currency, s.billing_cycle, s.expires_on,
       s.traffic_quota_bytes, s.traffic_reset_day, s.traffic_mode, s.traffic_adjust_bytes, s.traffic_adjust_cycle,
       (SELECT COUNT(*) FROM inbound i WHERE i.server_id = s.id AND NOT i.is_missing)::int AS inbound_count,
       coalesce(h.id, 0)::bigint AS host_id, coalesce(h.os_id, '')::varchar AS host_os_id,
       coalesce(h.arch, '')::varchar AS host_arch, h.cert_not_after AS host_cert_not_after
FROM server s
LEFT JOIN server_host h ON h.server_id = s.id
ORDER BY s.id;

-- name: GetServer :one
SELECT s.id, s.name, s.provider_type, s.base_url, s.tls_mode, s.cert_pin_sha256, s.region, s.remark,
       s.is_disabled, s.status, s.last_error, s.last_seen_at, s.panel_version, s.status_snapshot,
       s.last_synced_at, s.created_at, s.client_flow,
       s.public_ipv4, s.public_ipv6, s.online_clients,
       s.price_cents, s.price_currency, s.billing_cycle, s.expires_on,
       s.traffic_quota_bytes, s.traffic_reset_day, s.traffic_mode, s.traffic_adjust_bytes, s.traffic_adjust_cycle,
       (SELECT COUNT(*) FROM inbound i WHERE i.server_id = s.id AND NOT i.is_missing)::int AS inbound_count,
       coalesce(h.id, 0)::bigint AS host_id, coalesce(h.os_id, '')::varchar AS host_os_id,
       coalesce(h.arch, '')::varchar AS host_arch, h.cert_not_after AS host_cert_not_after
FROM server s
LEFT JOIN server_host h ON h.server_id = s.id
WHERE s.id = $1;

-- name: GetServerConn :one
SELECT id, name, provider_type, base_url, api_token_enc, tls_mode, cert_pin_sha256, is_disabled, client_flow
FROM server
WHERE id = $1;

-- name: SetServerClientFlow :execrows
-- 服务器的 VLESS 流控（Ruling-77）：下发对账据此给 client 带上或去掉 flow
UPDATE server SET client_flow = $2, updated_at = now() WHERE id = $1;

-- name: ListEnabledServerIDs :many
SELECT id FROM server WHERE NOT is_disabled ORDER BY id;

-- name: CreateServer :one
-- token 密文依赖 id 作关联数据，先写占位，同一事务内再 SetServerToken
INSERT INTO server (name, provider_type, base_url, api_token_enc, tls_mode, cert_pin_sha256, region, remark, is_disabled)
VALUES ($1, $2, $3, '\x00', $4, $5, $6, $7, $8)
RETURNING id;

-- name: SetServerToken :exec
UPDATE server SET api_token_enc = $2, updated_at = now() WHERE id = $1;

-- name: UpdateServer :execrows
UPDATE server
SET name = $2, provider_type = $3, base_url = $4, tls_mode = $5, cert_pin_sha256 = $6,
    region = $7, remark = $8, is_disabled = $9, updated_at = now()
WHERE id = $1;

-- name: DeleteServer :execrows
DELETE FROM server WHERE id = $1;

-- name: SetServerProbeResult :exec
-- T1–T3 的落库：status、last_error 由调用方按转移表算好；is_online 时才刷新版本、GUID、快照、公网 IP 与最近在线时间。
-- 不在线起始时间（docs/03 §5.20 掉线告警）：回到 online 清空，离开 online 时记下第一次的时刻
UPDATE server
SET status = sqlc.arg(status)::varchar,
    offline_since = CASE WHEN sqlc.arg(is_online)::bool THEN NULL ELSE coalesce(offline_since, sqlc.arg(probed_at)::timestamptz) END,
    last_error = sqlc.arg(last_error)::varchar,
    last_seen_at = CASE WHEN sqlc.arg(is_online)::bool THEN now() ELSE last_seen_at END,
    panel_version = CASE WHEN sqlc.arg(is_online)::bool THEN sqlc.arg(panel_version)::varchar ELSE panel_version END,
    panel_guid = CASE WHEN sqlc.arg(is_online)::bool THEN sqlc.arg(panel_guid)::varchar ELSE panel_guid END,
    status_snapshot = CASE WHEN sqlc.arg(is_online)::bool THEN sqlc.narg(status_snapshot)::jsonb ELSE status_snapshot END,
    public_ipv4 = CASE WHEN sqlc.arg(is_online)::bool THEN sqlc.arg(public_ipv4)::varchar ELSE public_ipv4 END,
    public_ipv6 = CASE WHEN sqlc.arg(is_online)::bool THEN sqlc.arg(public_ipv6)::varchar ELSE public_ipv6 END
WHERE id = sqlc.arg(id);

-- name: SetServerSynced :exec
UPDATE server SET last_synced_at = now() WHERE id = $1;

-- name: ListUnlocatedServerIDs :many
-- 位置未知的启用服务器，启动时补扫（Ruling-65）
SELECT id FROM server WHERE NOT is_disabled AND region = '' ORDER BY id;

-- name: FillServerRegion :execrows
-- 扫描结果只填进仍为空的位置，不覆盖管理员这期间的选择（Ruling-65）
UPDATE server SET region = sqlc.arg(region)::varchar, updated_at = now() WHERE id = sqlc.arg(id) AND region = '';

-- name: RecordServerMetric :exec
-- 健康探测成功顺带记一条采样：对齐 5 分钟，同一时段覆盖为最新值，在线数取服务器缓存（Ruling-65、Ruling-83）
INSERT INTO server_metric (server_id, bucket_at, cpu, online_clients, mem_used, mem_total, disk_used, disk_total,
                           load1_x100, net_up_rate, net_down_rate, tcp_count, udp_count)
SELECT s.id, date_bin('5 minutes', sqlc.arg(sampled_at)::timestamptz, TIMESTAMPTZ '2000-01-01 00:00:00+00'),
       sqlc.narg(cpu)::double precision, s.online_clients,
       sqlc.narg(mem_used)::bigint, sqlc.narg(mem_total)::bigint, sqlc.narg(disk_used)::bigint, sqlc.narg(disk_total)::bigint,
       sqlc.narg(load1_x100)::int, sqlc.narg(net_up_rate)::bigint, sqlc.narg(net_down_rate)::bigint,
       sqlc.narg(tcp_count)::int, sqlc.narg(udp_count)::int
FROM server s WHERE s.id = sqlc.arg(server_id)
ON CONFLICT (server_id, bucket_at) DO UPDATE SET
    cpu = EXCLUDED.cpu, online_clients = EXCLUDED.online_clients,
    mem_used = EXCLUDED.mem_used, mem_total = EXCLUDED.mem_total, disk_used = EXCLUDED.disk_used, disk_total = EXCLUDED.disk_total,
    load1_x100 = EXCLUDED.load1_x100, net_up_rate = EXCLUDED.net_up_rate, net_down_rate = EXCLUDED.net_down_rate,
    tcp_count = EXCLUDED.tcp_count, udp_count = EXCLUDED.udp_count;

-- name: PurgeServerMetrics :execrows
DELETE FROM server_metric WHERE bucket_at < $1;

-- name: ListServerMetrics :many
-- 5 分钟采样原值（近 24 小时趋势）
SELECT bucket_at, cpu, online_clients FROM server_metric
WHERE server_id = $1 AND bucket_at >= $2 ORDER BY bucket_at;

-- name: ListServerMetricsHourly :many
-- 按小时平均（近 7 天趋势）；cpu_samples 为 0 表示该小时 CPU 全为空（此时 cpu 无意义）
SELECT date_bin('1 hour', bucket_at, TIMESTAMPTZ '2000-01-01 00:00:00+00')::timestamptz AS bucket_at,
       coalesce(avg(cpu), 0)::double precision AS cpu, count(cpu) AS cpu_samples,
       round(avg(online_clients))::integer AS online_clients
FROM server_metric
WHERE server_id = $1 AND bucket_at >= $2
GROUP BY 1 ORDER BY 1;

-- name: ListServerResourceMetrics :many
-- 管理端资源趋势的 5 分钟原值（Ruling-83）；Ruling-83 之前的行除 cpu 与 online_clients 外为空
SELECT bucket_at, cpu, online_clients, mem_used, mem_total, disk_used, disk_total, load1_x100,
       net_up_rate, net_down_rate, tcp_count, udp_count
FROM server_metric
WHERE server_id = $1 AND bucket_at >= $2 ORDER BY bucket_at;

-- name: ListServerResourceMetricsHourly :many
-- 管理端资源趋势按小时平均；cpu_samples / resource_samples 为 0 表示该小时对应的列全为空（此时数值无意义）
SELECT date_bin('1 hour', bucket_at, TIMESTAMPTZ '2000-01-01 00:00:00+00')::timestamptz AS bucket_at,
       coalesce(avg(cpu), 0)::double precision AS cpu, count(cpu) AS cpu_samples,
       round(avg(online_clients))::integer AS online_clients,
       count(mem_total) AS resource_samples,
       coalesce(round(avg(mem_used)), 0)::bigint AS mem_used, coalesce(round(avg(mem_total)), 0)::bigint AS mem_total,
       coalesce(round(avg(disk_used)), 0)::bigint AS disk_used, coalesce(round(avg(disk_total)), 0)::bigint AS disk_total,
       coalesce(round(avg(load1_x100)), 0)::integer AS load1_x100,
       coalesce(round(avg(net_up_rate)), 0)::bigint AS net_up_rate, coalesce(round(avg(net_down_rate)), 0)::bigint AS net_down_rate,
       coalesce(round(avg(tcp_count)), 0)::integer AS tcp_count, coalesce(round(avg(udp_count)), 0)::integer AS udp_count
FROM server_metric
WHERE server_id = $1 AND bucket_at >= $2
GROUP BY 1 ORDER BY 1;

-- name: ListRecentServerNet :many
-- 各服务器近一段时间的上下行速率（列表卡片的小图，Ruling-83）
SELECT server_id, bucket_at, net_up_rate, net_down_rate
FROM server_metric
WHERE bucket_at >= $1 AND net_up_rate IS NOT NULL AND net_down_rate IS NOT NULL
ORDER BY server_id, bucket_at;

-- 账单与流量额度（docs/03 §5.1，Ruling-82）-------------------------------------

-- name: SetServerBilling :execrows
UPDATE server
SET price_cents = sqlc.narg(price_cents), price_currency = sqlc.arg(price_currency)::varchar,
    billing_cycle = sqlc.arg(billing_cycle)::varchar, expires_on = sqlc.narg(expires_on),
    traffic_quota_bytes = sqlc.narg(traffic_quota_bytes), traffic_reset_day = sqlc.arg(traffic_reset_day)::smallint,
    traffic_mode = sqlc.arg(traffic_mode)::varchar,
    traffic_adjust_bytes = sqlc.arg(traffic_adjust_bytes)::bigint, traffic_adjust_cycle = sqlc.narg(traffic_adjust_cycle),
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: GetServerNicCountersForUpdate :one
-- 上次探测时的网卡累计计数；行锁让并发的探测不会把同一段增量记两次
SELECT nic_last_sent, nic_last_recv FROM server WHERE id = $1 FOR UPDATE;

-- name: SetServerNicCounters :exec
UPDATE server SET nic_last_sent = sqlc.arg(sent)::bigint, nic_last_recv = sqlc.arg(recv)::bigint WHERE id = sqlc.arg(id);

-- name: AddServerNicTraffic :exec
INSERT INTO server_nic_traffic_daily (server_id, day, sent, recv)
VALUES (sqlc.arg(server_id), sqlc.arg(day), sqlc.arg(sent), sqlc.arg(recv))
ON CONFLICT (server_id, day) DO UPDATE SET
    sent = server_nic_traffic_daily.sent + EXCLUDED.sent, recv = server_nic_traffic_daily.recv + EXCLUDED.recv;

-- name: ListServerNicTraffic :many
-- 全部服务器自某天起的按天流量（各台的周期起始日不同，由调用方按周期求和）
SELECT server_id, day, sent, recv FROM server_nic_traffic_daily WHERE day >= $1 ORDER BY server_id, day;

-- name: ListServerNicTrafficOf :many
SELECT server_id, day, sent, recv FROM server_nic_traffic_daily WHERE server_id = $1 AND day >= $2 ORDER BY day;

-- name: PurgeServerNicTraffic :execrows
DELETE FROM server_nic_traffic_daily WHERE day < $1;

-- 三网延迟（docs/03 §5.20，Ruling-84）-----------------------------------------

-- name: ListLatencyDueServers :many
-- 到期该测延迟的服务器：启用、在线、有公网 IPv4；最久没测的在前
SELECT id, public_ipv4 FROM server
WHERE NOT is_disabled AND status = 'online' AND public_ipv4 <> ''
  AND (latency_checked_at IS NULL OR latency_checked_at < sqlc.arg(due_before)::timestamptz)
ORDER BY latency_checked_at NULLS FIRST, id
LIMIT sqlc.arg(row_limit);

-- name: SetServerLatencyChecked :exec
UPDATE server SET latency_checked_at = sqlc.arg(checked_at)::timestamptz WHERE id = sqlc.arg(id);

-- name: InsertServerLatency :exec
INSERT INTO server_latency (server_id, isp, sampled_at, avg_ms, loss_percent, probe_city)
VALUES (sqlc.arg(server_id), sqlc.arg(isp), sqlc.arg(sampled_at), sqlc.narg(avg_ms), sqlc.arg(loss_percent), sqlc.arg(probe_city))
ON CONFLICT (server_id, isp, sampled_at) DO NOTHING;

-- name: ListServerLatency :many
SELECT isp, sampled_at, avg_ms, loss_percent FROM server_latency
WHERE server_id = $1 AND sampled_at >= $2 ORDER BY sampled_at;

-- name: ListServerLatencyHourly :many
-- 按小时平均；ok_samples 为 0 表示该小时全丢（此时 avg_ms 无意义）
SELECT isp, date_bin('1 hour', sampled_at, TIMESTAMPTZ '2000-01-01 00:00:00+00')::timestamptz AS sampled_at,
       coalesce(round(avg(avg_ms)), 0)::integer AS avg_ms, count(avg_ms) AS ok_samples,
       round(avg(loss_percent))::integer AS loss_percent
FROM server_latency
WHERE server_id = $1 AND sampled_at >= $2
GROUP BY 1, 2 ORDER BY 2;

-- name: ListRecentServerLatency :many
-- 各服务器每家运营商最近的若干次检测（列表卡片用），按时间升序
SELECT t.server_id, t.isp, t.sampled_at, t.avg_ms, t.loss_percent
FROM (SELECT l.server_id, l.isp, l.sampled_at, l.avg_ms, l.loss_percent,
             row_number() OVER (PARTITION BY l.server_id, l.isp ORDER BY l.sampled_at DESC) AS recent_rank
      FROM server_latency l
      WHERE l.sampled_at >= sqlc.arg(since)::timestamptz) t
WHERE t.recent_rank <= sqlc.arg(per_series)::int
ORDER BY t.server_id, t.isp, t.sampled_at;

-- name: PurgeServerLatency :execrows
DELETE FROM server_latency WHERE sampled_at < $1;
