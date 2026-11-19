-- 服务器 IP 质量检测（docs/03 §5.23，Ruling-81、Ruling-82）

-- name: CreateIPCheck :one
-- 同一服务器已有 queued / running 的检测时违反 uk_server_ip_check_active
INSERT INTO server_ip_check (server_id, origin, scope, host_probe, created_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING id;

-- name: GetIPCheck :one
-- 不带完整报告，列与 ListIPChecks 相同；报告另取 GetIPCheckResult
SELECT c.id, c.server_id, c.status, c.origin, c.scope, c.host_probe, c.ip, c.ip_source, c.country, c.reg_country,
       c.ip_kind, c.asn, c.as_org, c.usage_type, c.risk_level, c.dnsbl_listed, c.dnsbl_total, c.media_ok, c.media_total,
       c.port25, c.error_code, c.last_error, c.created_by, c.created_at, c.started_at, c.finished_at,
       coalesce(a.username, '')::varchar AS created_by_name
FROM server_ip_check c
LEFT JOIN admin a ON a.id = c.created_by
WHERE c.id = $1;

-- name: GetIPCheckResult :one
SELECT result FROM server_ip_check WHERE id = $1;

-- name: ListIPChecks :many
-- 新的在前
SELECT c.id, c.server_id, c.status, c.origin, c.scope, c.host_probe, c.ip, c.ip_source, c.country, c.reg_country,
       c.ip_kind, c.asn, c.as_org, c.usage_type, c.risk_level, c.dnsbl_listed, c.dnsbl_total, c.media_ok, c.media_total,
       c.port25, c.error_code, c.last_error, c.created_by, c.created_at, c.started_at, c.finished_at,
       coalesce(a.username, '')::varchar AS created_by_name
FROM server_ip_check c
LEFT JOIN admin a ON a.id = c.created_by
WHERE c.server_id = $1
ORDER BY c.id DESC
LIMIT $2 OFFSET $3;

-- name: CountIPChecks :one
SELECT COUNT(*) FROM server_ip_check WHERE server_id = $1;

-- name: ClaimQueuedIPChecks :many
-- 领取排队的检测：按先后取至多 N 条置为进行中；SKIP LOCKED 让并发领取互不等待
UPDATE server_ip_check
SET status = 'running', started_at = now()
WHERE id IN (
    SELECT q.id FROM server_ip_check q
    WHERE q.status = 'queued'
    ORDER BY q.id
    LIMIT $1
    FOR UPDATE SKIP LOCKED
)
RETURNING id;

-- name: GetIPCheckRun :one
-- 执行一条检测需要的参数
SELECT id, server_id, scope, host_probe FROM server_ip_check WHERE id = $1;

-- name: FinishIPCheck :execrows
UPDATE server_ip_check
SET status = sqlc.arg(status), host_probe = sqlc.arg(host_probe), ip = sqlc.arg(ip), ip_source = sqlc.arg(ip_source),
    country = sqlc.arg(country), reg_country = sqlc.arg(reg_country), ip_kind = sqlc.arg(ip_kind), asn = sqlc.narg(asn),
    as_org = sqlc.arg(as_org), usage_type = sqlc.arg(usage_type), risk_level = sqlc.arg(risk_level),
    dnsbl_listed = sqlc.arg(dnsbl_listed), dnsbl_total = sqlc.arg(dnsbl_total), media_ok = sqlc.arg(media_ok),
    media_total = sqlc.arg(media_total), port25 = sqlc.arg(port25), result = sqlc.narg(result),
    error_code = sqlc.arg(error_code), last_error = sqlc.arg(last_error), finished_at = now()
WHERE id = sqlc.arg(id) AND status = 'running';

-- name: FailRunningIPChecks :execrows
-- 进程重启后，上次没跑完的检测一律记为失败（docs/03 §5.23 记录）
UPDATE server_ip_check
SET status = 'failed', error_code = sqlc.arg(error_code), last_error = sqlc.arg(last_error), finished_at = now()
WHERE status = 'running';

-- name: ListServersDueForIPCheck :many
-- 定期复检挑选：启用且在线、没有进行中的检测、绑定的主机没有未结束的主机任务、最近一次检测早于截止时刻；
-- 从没测过的最优先，其次最久没测的
SELECT s.id
FROM server s
LEFT JOIN LATERAL (
    SELECT max(c.created_at) AS last_at FROM server_ip_check c WHERE c.server_id = s.id
) lc ON TRUE
WHERE NOT s.is_disabled
  AND s.status = 'online'
  AND (lc.last_at IS NULL OR lc.last_at < sqlc.arg(cutoff)::timestamptz)
  AND NOT EXISTS (
      SELECT 1 FROM server_ip_check a WHERE a.server_id = s.id AND a.status IN ('queued', 'running')
  )
  AND NOT EXISTS (
      SELECT 1 FROM server_host h JOIN host_task t ON t.host_id = h.id
      WHERE h.server_id = s.id AND t.status IN ('queued', 'running')
  )
ORDER BY lc.last_at ASC NULLS FIRST, s.id
LIMIT sqlc.arg(max_count);

-- name: PurgeIPChecks :execrows
-- 清理：早于截止时刻的，以及每台服务器超出保留条数的（进行中的不删）
DELETE FROM server_ip_check
WHERE status NOT IN ('queued', 'running')
  AND (
      created_at < sqlc.arg(cutoff)::timestamptz
      OR id IN (
          SELECT r.id FROM (
              SELECT c.id, row_number() OVER (PARTITION BY c.server_id ORDER BY c.id DESC) AS rn
              FROM server_ip_check c
          ) r
          WHERE r.rn > sqlc.arg(keep_per_server)::bigint
      )
  );
