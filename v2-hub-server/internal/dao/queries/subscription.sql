-- 订阅与客户后台数据（docs/03 §5.4、§5.5）。

-- name: GetUserBySubTokenHash :one
SELECT u.id, u.username, u.status, u.traffic_limit, u.traffic_used, u.expires_at, u.pending_days
FROM end_user u
WHERE u.sub_token_hash = $1 AND u.deleted_at IS NULL;

-- name: GetUserSubToken :one
SELECT sub_token_enc FROM end_user WHERE id = $1 AND deleted_at IS NULL;

-- name: SetUserSubToken :execrows
-- 只在还没有 token 时写入，避免并发请求互相覆盖
UPDATE end_user SET sub_token_hash = $2, sub_token_enc = $3, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL AND sub_token_hash IS NULL;

-- name: RotateUserSubscription :execrows
-- 重置订阅：换 token 并轮换连接凭据（docs/03 §5.4 重置）
UPDATE end_user
SET sub_token_hash = $2, sub_token_enc = $3, client_cred_enc = $4, cred_version = cred_version + 1, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL;

-- name: ListUserSubNodes :many
-- 用户被授权且可输出的节点：节点启用、入站启用且未缺失、服务器未停用、对外地址未缺失未停用
SELECT DISTINCT n.id, n.display_name, n.region, n.sort_order, i.id AS inbound_id, i.server_id, i.port AS inbound_port, i.protocol,
       (n.endpoint_id IS NULL)::boolean AS is_direct,
       coalesce(e.address, '')::text AS endpoint_address, coalesce(e.port, 0)::int AS endpoint_port, s.base_url
FROM end_user u
JOIN plan_node_group pg ON pg.plan_id = u.plan_id
JOIN node_group_item gi ON gi.node_group_id = pg.node_group_id
JOIN node n ON n.id = gi.node_id AND n.is_enabled
JOIN inbound i ON i.id = n.inbound_id AND NOT i.is_missing AND i.is_enabled
JOIN server s ON s.id = i.server_id AND NOT s.is_disabled
LEFT JOIN endpoint e ON e.id = n.endpoint_id
WHERE u.id = $1 AND u.deleted_at IS NULL AND (e.id IS NULL OR (NOT e.is_missing AND NOT e.is_disabled))
ORDER BY n.sort_order, n.display_name, n.id;

-- name: ListServerEndpointAddrs :many
-- 这些服务器上全部对外地址（含未授权的），用来判断一条链接是不是某个对外地址的
SELECT e.inbound_id, e.address, e.port
FROM endpoint e JOIN inbound i ON i.id = e.inbound_id
WHERE i.server_id = ANY(sqlc.arg(server_ids)::bigint[]) AND NOT e.is_missing;

-- name: ListUserProvisionLinks :many
SELECT id, server_id, links_enc FROM provision WHERE user_id = $1 AND links_enc IS NOT NULL ORDER BY server_id;

-- name: InsertSubscribeLog :exec
INSERT INTO subscribe_log (user_id, format, client_ip, user_agent) VALUES ($1, $2, $3, $4);

-- name: ListSubscribeLogs :many
SELECT l.id, l.user_id, u.username, l.format, l.client_ip, l.user_agent, l.created_at
FROM subscribe_log l JOIN end_user u ON u.id = l.user_id
WHERE (sqlc.narg(user_id)::bigint IS NULL OR l.user_id = sqlc.narg(user_id))
ORDER BY l.created_at DESC, l.id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountSubscribeLogs :one
SELECT COUNT(*) FROM subscribe_log l
WHERE (sqlc.narg(user_id)::bigint IS NULL OR l.user_id = sqlc.narg(user_id));

-- name: DeleteOldSubscribeLogs :execrows
DELETE FROM subscribe_log WHERE created_at < $1;

-- name: GetUserDashboard :one
SELECT u.status, coalesce(p.name, '')::text AS plan_name, u.traffic_limit, u.traffic_used, u.expires_at, u.pending_days,
       u.limit_ip, u.last_used_at,
       (SELECT COUNT(*) FROM provision v WHERE v.user_id = u.id AND v.is_online)::int AS online_devices,
       (SELECT coalesce(sum(t.up + t.down), 0) FROM user_traffic_hourly t
         WHERE t.user_id = u.id AND t.hour >= sqlc.arg(day_start)::timestamptz)::bigint AS traffic_today
FROM end_user u LEFT JOIN plan p ON p.id = u.plan_id
WHERE u.id = sqlc.arg(id) AND u.deleted_at IS NULL;

-- name: ListUserTrafficHourly :many
SELECT hour, up, down FROM user_traffic_hourly WHERE user_id = $1 AND hour >= $2 ORDER BY hour;

-- name: ListUserNodeGroups :many
-- 客户后台节点列表：按节点组分段，状态取平台缓存（docs/03 §5.5）
SELECT g.id AS group_id, g.name AS group_name, n.id, n.display_name, n.region, n.sort_order, i.protocol,
       (s.status = 'online')::boolean AS is_server_online, s.online_clients, s.status_snapshot
FROM end_user u
JOIN plan_node_group pg ON pg.plan_id = u.plan_id
JOIN node_group g ON g.id = pg.node_group_id
JOIN node_group_item gi ON gi.node_group_id = g.id
JOIN node n ON n.id = gi.node_id AND n.is_enabled
JOIN inbound i ON i.id = n.inbound_id AND NOT i.is_missing AND i.is_enabled
JOIN server s ON s.id = i.server_id AND NOT s.is_disabled
LEFT JOIN endpoint e ON e.id = n.endpoint_id
WHERE u.id = $1 AND u.deleted_at IS NULL AND (e.id IS NULL OR (NOT e.is_missing AND NOT e.is_disabled))
ORDER BY g.name, g.id, n.sort_order, n.display_name, n.id;
