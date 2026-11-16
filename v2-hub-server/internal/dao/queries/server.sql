-- 服务器（docs/03 §2、§3 T1–T4、§5.1）---------------------------------------

-- name: ListServers :many
SELECT s.id, s.name, s.provider_type, s.base_url, s.tls_mode, s.cert_pin_sha256, s.region, s.remark,
       s.is_disabled, s.status, s.last_error, s.last_seen_at, s.panel_version, s.status_snapshot,
       s.last_synced_at, s.created_at,
       (SELECT COUNT(*) FROM inbound i WHERE i.server_id = s.id AND NOT i.is_missing)::int AS inbound_count,
       coalesce((SELECT h.id FROM server_host h WHERE h.server_id = s.id), 0)::bigint AS host_id
FROM server s
ORDER BY s.id;

-- name: GetServer :one
SELECT s.id, s.name, s.provider_type, s.base_url, s.tls_mode, s.cert_pin_sha256, s.region, s.remark,
       s.is_disabled, s.status, s.last_error, s.last_seen_at, s.panel_version, s.status_snapshot,
       s.last_synced_at, s.created_at,
       (SELECT COUNT(*) FROM inbound i WHERE i.server_id = s.id AND NOT i.is_missing)::int AS inbound_count,
       coalesce((SELECT h.id FROM server_host h WHERE h.server_id = s.id), 0)::bigint AS host_id
FROM server s
WHERE s.id = $1;

-- name: GetServerConn :one
SELECT id, name, provider_type, base_url, api_token_enc, tls_mode, cert_pin_sha256, is_disabled
FROM server
WHERE id = $1;

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
-- T1–T3 的落库：status、last_error 由调用方按转移表算好；is_online 时才刷新版本、GUID、快照与最近在线时间。
-- 不在线起始时间（docs/03 §5.20 掉线告警）：回到 online 清空，离开 online 时记下第一次的时刻
UPDATE server
SET status = sqlc.arg(status)::varchar,
    offline_since = CASE WHEN sqlc.arg(is_online)::bool THEN NULL ELSE coalesce(offline_since, sqlc.arg(probed_at)::timestamptz) END,
    last_error = sqlc.arg(last_error)::varchar,
    last_seen_at = CASE WHEN sqlc.arg(is_online)::bool THEN now() ELSE last_seen_at END,
    panel_version = CASE WHEN sqlc.arg(is_online)::bool THEN sqlc.arg(panel_version)::varchar ELSE panel_version END,
    panel_guid = CASE WHEN sqlc.arg(is_online)::bool THEN sqlc.arg(panel_guid)::varchar ELSE panel_guid END,
    status_snapshot = CASE WHEN sqlc.arg(is_online)::bool THEN sqlc.narg(status_snapshot)::jsonb ELSE status_snapshot END
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
-- 健康探测成功顺带记一条负载采样：对齐 5 分钟，同一时段覆盖为最新值，在线数取服务器缓存（Ruling-65）
INSERT INTO server_metric (server_id, bucket_at, cpu, online_clients)
SELECT s.id, date_bin('5 minutes', sqlc.arg(sampled_at)::timestamptz, TIMESTAMPTZ '2000-01-01 00:00:00+00'),
       sqlc.narg(cpu)::double precision, s.online_clients
FROM server s WHERE s.id = sqlc.arg(server_id)
ON CONFLICT (server_id, bucket_at) DO UPDATE SET cpu = EXCLUDED.cpu, online_clients = EXCLUDED.online_clients;

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
