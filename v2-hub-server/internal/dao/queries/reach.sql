-- 大陆连通检测（docs/03 §5.20 大陆连通检测，Ruling-67）。

-- name: ListReachCandidates :many
-- 检测范围内的节点：启用中的服务器上启用且未缺失的节点；目标的地址与端口由 service 推导（直连节点取面板主机名）
SELECT n.id AS node_id, n.display_name, s.id AS server_id, s.name AS server_name, s.base_url, s.status AS server_status,
       i.protocol, i.network, i.port AS inbound_port,
       COALESCE(e.address, '')::text AS endpoint_address, COALESCE(e.port, 0)::int AS endpoint_port
FROM node n
JOIN inbound i ON i.id = n.inbound_id AND NOT i.is_missing AND i.is_enabled
JOIN server s ON s.id = i.server_id AND NOT s.is_disabled
LEFT JOIN endpoint e ON e.id = n.endpoint_id
WHERE n.is_enabled AND (e.id IS NULL OR (NOT e.is_missing AND NOT e.is_disabled))
ORDER BY n.id;

-- name: ListReachTargets :many
SELECT id, address, port, transport, verdict, cn_ok, cn_total, control_ok, control_total, ip_reachable, probes,
       last_error, blocked_streak, blocked_since, alert_sent_at, checked_at
FROM node_reach
ORDER BY checked_at NULLS FIRST, id;

-- name: GetReachTarget :one
SELECT id, address, port, transport, verdict, cn_ok, cn_total, control_ok, control_total, ip_reachable, probes,
       last_error, blocked_streak, blocked_since, alert_sent_at, checked_at
FROM node_reach
WHERE address = $1 AND port = $2 AND transport = $3;

-- name: InsertReachTarget :exec
INSERT INTO node_reach (address, port, transport) VALUES ($1, $2, $3)
ON CONFLICT (address, port, transport) DO NOTHING;

-- name: DeleteReachTargets :exec
DELETE FROM node_reach WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: SaveReachResult :exec
UPDATE node_reach
SET verdict = $2, cn_ok = $3, cn_total = $4, control_ok = $5, control_total = $6, ip_reachable = $7, probes = $8,
    last_error = $9, blocked_streak = $10, blocked_since = $11, checked_at = $12, updated_at = now()
WHERE id = $1;

-- name: MarkReachAlerted :exec
UPDATE node_reach SET alert_sent_at = $2, updated_at = now() WHERE id = $1;

-- name: ClearReachAlert :exec
UPDATE node_reach SET alert_sent_at = NULL, updated_at = now() WHERE id = $1;

-- name: CountBlockedReachTargets :one
SELECT COUNT(*)::int FROM node_reach WHERE verdict = 'blocked';

-- name: NodeExists :one
SELECT EXISTS (SELECT 1 FROM node WHERE id = $1);
