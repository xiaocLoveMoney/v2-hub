-- 链式规则与出口（docs/03 §5.18、§5.19、§5.21）-------------------------------------------

-- name: GetUserChainFlags :one
-- 当前套餐的链式开关；没有套餐时都为假
SELECT coalesce(p.allow_client_chain, FALSE)::boolean AS allow_client_chain,
       coalesce(p.allow_custom_node, FALSE)::boolean AS allow_custom_node,
       coalesce(p.allow_server_chain, FALSE)::boolean AS allow_server_chain
FROM end_user u LEFT JOIN plan p ON p.id = u.plan_id
WHERE u.id = $1 AND u.deleted_at IS NULL;

-- name: ListChainRoutes :many
-- 这些用户的全部规则（两种类型），带落地的显示信息与出口的开关；同一用户同一类型内按 position 排
SELECT r.id, r.user_id, r.kind, r.position, r.name, r.is_enabled, r.quarantine_hash, r.created_at, r.updated_at,
       coalesce(r.exit_node_id, 0)::bigint AS exit_node_id, coalesce(xn.display_name, '')::text AS exit_node_name,
       coalesce(xi.protocol, '')::text AS exit_node_protocol,
       coalesce(r.exit_id, 0)::bigint AS exit_id, coalesce(x.name, '')::text AS exit_name, coalesce(x.protocol, '')::text AS exit_protocol,
       coalesce(x.user_id, 0)::bigint AS exit_owner_id, coalesce(x.is_enabled, FALSE)::boolean AS exit_enabled,
       coalesce(x.allow_server, FALSE)::boolean AS exit_allow_server, coalesce(x.allow_client, FALSE)::boolean AS exit_allow_client,
       coalesce(x.updated_at, r.updated_at)::timestamptz AS exit_updated_at
FROM chain_route r
LEFT JOIN node xn ON xn.id = r.exit_node_id
LEFT JOIN inbound xi ON xi.id = xn.inbound_id
LEFT JOIN chain_exit x ON x.id = r.exit_id
WHERE r.user_id = ANY(sqlc.arg(user_ids)::bigint[])
ORDER BY r.user_id, r.kind, r.position, r.id;

-- name: ListChainRouteEntries :many
-- 这些用户全部规则的起飞节点，带入站与服务器；规则内按节点目录顺序
SELECT e.route_id, e.entry_node_id, n.display_name, i.id AS inbound_id, i.tag AS inbound_tag, i.server_id, s.name AS server_name
FROM chain_route_entry e
JOIN chain_route r ON r.id = e.route_id
JOIN node n ON n.id = e.entry_node_id
JOIN inbound i ON i.id = n.inbound_id
JOIN server s ON s.id = i.server_id
WHERE r.user_id = ANY(sqlc.arg(user_ids)::bigint[])
ORDER BY e.route_id, n.sort_order, n.display_name, n.id;

-- name: CountChainRoutes :one
SELECT COUNT(*) FROM chain_route WHERE user_id = sqlc.arg(user_id) AND kind = sqlc.arg(kind);

-- name: InsertChainRoute :one
-- 新规则排在同类型的最后
INSERT INTO chain_route (user_id, kind, position, name, exit_node_id, exit_id, is_enabled)
VALUES (sqlc.arg(user_id)::bigint, sqlc.arg(kind)::varchar,
        (SELECT coalesce(max(c.position), 0) + 1 FROM chain_route c WHERE c.user_id = sqlc.arg(user_id)::bigint AND c.kind = sqlc.arg(kind)::varchar),
        sqlc.arg(name), sqlc.narg(exit_node_id), sqlc.narg(exit_id), sqlc.arg(is_enabled))
RETURNING id;

-- name: UpdateChainRoute :execrows
UPDATE chain_route
SET name = sqlc.arg(name), exit_node_id = sqlc.narg(exit_node_id), exit_id = sqlc.narg(exit_id), is_enabled = sqlc.arg(is_enabled), updated_at = now()
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id);

-- name: AddChainRouteEntries :exec
-- 先加后删（见 RemoveChainRouteEntriesExcept）：起飞节点被删光时触发器会连规则一起删
INSERT INTO chain_route_entry (route_id, entry_node_id)
SELECT sqlc.arg(route_id), unnest(sqlc.arg(node_ids)::bigint[])
ON CONFLICT DO NOTHING;

-- name: RemoveChainRouteEntriesExcept :exec
DELETE FROM chain_route_entry WHERE route_id = sqlc.arg(route_id) AND entry_node_id <> ALL(sqlc.arg(node_ids)::bigint[]);

-- name: SetChainRoutePosition :execrows
-- 调顺序：唯一约束是延迟到提交时检查的，事务内可以逐条改；同时刷新 updated_at，让下发状态按新顺序重新判断
UPDATE chain_route SET position = sqlc.arg(position), updated_at = now()
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) AND kind = sqlc.arg(kind);

-- name: DeleteChainRoute :one
DELETE FROM chain_route WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) RETURNING kind;

-- name: DeleteChainRoutesByUser :exec
DELETE FROM chain_route WHERE user_id = $1;

-- name: SetChainRouteQuarantine :exec
-- 失败隔离（docs/03 §5.19）：记下当时的内容指纹；内容变化后指纹对不上即视为解除
UPDATE chain_route SET quarantine_hash = sqlc.arg(quarantine_hash) WHERE id = sqlc.arg(id);

-- name: ListServerRouteNodeRoles :many
-- 某用户服务端规则里用到的节点：is_entry 为真 = 作起飞，为假 = 作落地；防环校验用（Ruling-49）
SELECT r.id AS route_id, e.entry_node_id AS node_id, TRUE::boolean AS is_entry
FROM chain_route r JOIN chain_route_entry e ON e.route_id = r.id
WHERE r.user_id = $1 AND r.kind = 'server'
UNION ALL
SELECT r.id AS route_id, r.exit_node_id::bigint AS node_id, FALSE::boolean AS is_entry
FROM chain_route r
WHERE r.user_id = $1 AND r.kind = 'server' AND r.exit_node_id IS NOT NULL;

-- 出口 ------------------------------------------------------------------------------------

-- name: ListChainExits :many
-- 某用户自己的出口及各自被多少条规则引用；exit_id 非空时只取这一个（归属按 user_id 约束，R8）
SELECT x.id, x.name, x.protocol, x.address, x.port, x.is_enabled, x.created_at,
       (SELECT COUNT(*) FROM chain_route r WHERE r.exit_id = x.id)::int AS route_count
FROM chain_exit x
WHERE x.user_id = sqlc.arg(user_id)::bigint AND (sqlc.narg(exit_id)::bigint IS NULL OR x.id = sqlc.narg(exit_id))
ORDER BY x.id;

-- name: CountChainExits :one
SELECT COUNT(*) FROM chain_exit WHERE user_id = sqlc.arg(user_id)::bigint;

-- name: InsertChainExit :one
-- 链接密文的关联数据含 id，所以先插入空值，拿到 id 后由 SetChainExitLink 回填。user_id 为空 = 平台出口
INSERT INTO chain_exit (user_id, name, protocol, address, port, link_enc, remark, is_enabled, allow_server, allow_client)
VALUES (sqlc.narg(user_id), sqlc.arg(name), sqlc.arg(protocol), sqlc.arg(address), sqlc.arg(port), ''::bytea,
        sqlc.arg(remark), sqlc.arg(is_enabled), sqlc.arg(allow_server), sqlc.arg(allow_client))
RETURNING id;

-- name: SetChainExitLink :exec
-- 换链接：展示字段一并更新，固定的 IP 作废（由调用方按需重新固定）
UPDATE chain_exit
SET link_enc = sqlc.arg(link_enc), protocol = sqlc.arg(protocol), address = sqlc.arg(address), port = sqlc.arg(port),
    resolved_ip = NULL, resolved_at = NULL, updated_at = now()
WHERE id = sqlc.arg(id);

-- name: SetChainExitResolved :exec
-- 固定用于服务端链式的地址（Ruling-48）；resolved_ip 为空而 resolved_at 非空 = 复查发现不安全
UPDATE chain_exit SET resolved_ip = sqlc.narg(resolved_ip), resolved_at = sqlc.arg(resolved_at) WHERE id = sqlc.arg(id);

-- name: RenameChainExit :execrows
UPDATE chain_exit SET name = sqlc.arg(name), updated_at = now()
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id)::bigint;

-- name: DeleteChainExit :execrows
DELETE FROM chain_exit WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id)::bigint;

-- name: DeleteChainExitsByUser :exec
DELETE FROM chain_exit WHERE user_id = sqlc.arg(user_id)::bigint;

-- name: GetChainExitLink :one
SELECT link_enc FROM chain_exit WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id)::bigint;

-- name: ListChainExitSecrets :many
-- 渲染订阅与下发用：这些出口的名称、链接密文与固定的地址
SELECT id, coalesce(user_id, 0)::bigint AS owner_id, name, link_enc, resolved_ip, resolved_at, is_enabled
FROM chain_exit WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: CountServerRoutesByExit :one
SELECT COUNT(*) FROM chain_route WHERE exit_id = $1 AND kind = 'server';

-- name: ListUserPlatformExits :many
-- 用户被授权的平台出口：启用，且分配到本人——按节点组时属于套餐绑定的某个节点组（Ruling-47），指定用户时本人在名单里（Ruling-64）
SELECT x.id, x.name, x.protocol, x.allow_server, x.allow_client
FROM chain_exit x
JOIN end_user u ON u.id = sqlc.arg(user_id)::bigint AND u.deleted_at IS NULL
WHERE x.user_id IS NULL AND x.is_enabled
  AND ((x.assign_mode = 'group' AND EXISTS (
          SELECT 1 FROM plan_node_group pg JOIN node_group_exit ge ON ge.node_group_id = pg.node_group_id
          WHERE pg.plan_id = u.plan_id AND ge.exit_id = x.id))
    OR (x.assign_mode = 'user' AND EXISTS (
          SELECT 1 FROM chain_exit_user eu WHERE eu.exit_id = x.id AND eu.user_id = u.id)))
ORDER BY x.name, x.id;

-- name: ListPlatformHosts :many
-- 平台自己的地址：面板地址与节点对外地址；用户出口不得指向它们（防环，Ruling-49）
SELECT base_url AS host FROM server
UNION
SELECT address AS host FROM endpoint WHERE NOT is_missing;

-- 管理端：出口 ------------------------------------------------------------------------------

-- name: CountAdminChainExits :one
-- 关键字按子串匹配名称或地址（strpos，不当通配符解释；出口表很小，不需要索引）
SELECT COUNT(*) FROM chain_exit x
WHERE (sqlc.narg(is_platform)::boolean IS NULL OR (x.user_id IS NULL) = sqlc.narg(is_platform))
  AND (sqlc.narg(user_id)::bigint IS NULL OR x.user_id = sqlc.narg(user_id))
  AND (sqlc.narg(keyword)::text IS NULL OR strpos(lower(x.name), lower(sqlc.narg(keyword))) > 0 OR strpos(lower(x.address), lower(sqlc.narg(keyword))) > 0);

-- name: ListAdminChainExits :many
-- 全部出口（Ruling-50），按 id 倒序分页；exit_id 非空时只取这一个
SELECT x.id, coalesce(x.user_id, 0)::bigint AS owner_id, coalesce(u.username, '')::text AS username,
       x.name, x.protocol, x.address, x.port, x.remark, x.is_enabled, x.allow_server, x.allow_client, x.assign_mode, x.created_at, x.updated_at,
       (SELECT COUNT(*) FROM chain_route r WHERE r.exit_id = x.id)::int AS route_count
FROM chain_exit x
LEFT JOIN end_user u ON u.id = x.user_id
WHERE (sqlc.narg(exit_id)::bigint IS NULL OR x.id = sqlc.narg(exit_id))
  AND (sqlc.narg(is_platform)::boolean IS NULL OR (x.user_id IS NULL) = sqlc.narg(is_platform))
  AND (sqlc.narg(user_id)::bigint IS NULL OR x.user_id = sqlc.narg(user_id))
  AND (sqlc.narg(keyword)::text IS NULL OR strpos(lower(x.name), lower(sqlc.narg(keyword))) > 0 OR strpos(lower(x.address), lower(sqlc.narg(keyword))) > 0)
ORDER BY x.id DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: ListExitNodeGroups :many
SELECT ge.exit_id, g.id, g.name
FROM node_group_exit ge JOIN node_group g ON g.id = ge.node_group_id
WHERE ge.exit_id = ANY(sqlc.arg(exit_ids)::bigint[])
ORDER BY ge.exit_id, g.name, g.id;

-- name: DeleteExitNodeGroups :exec
DELETE FROM node_group_exit WHERE exit_id = $1;

-- name: AddExitNodeGroups :exec
INSERT INTO node_group_exit (node_group_id, exit_id)
SELECT unnest(sqlc.arg(node_group_ids)::bigint[]), sqlc.arg(exit_id)
ON CONFLICT DO NOTHING;

-- name: ListExitUsers :many
-- 平台出口指定的用户（Ruling-64）；已注销的不列出
SELECT eu.exit_id, u.id, u.username
FROM chain_exit_user eu JOIN end_user u ON u.id = eu.user_id AND u.deleted_at IS NULL
WHERE eu.exit_id = ANY(sqlc.arg(exit_ids)::bigint[])
ORDER BY eu.exit_id, u.username, u.id;

-- name: DeleteExitUsers :exec
DELETE FROM chain_exit_user WHERE exit_id = $1;

-- name: AddExitUsers :exec
INSERT INTO chain_exit_user (exit_id, user_id)
SELECT sqlc.arg(exit_id), unnest(sqlc.arg(user_ids)::bigint[])
ON CONFLICT DO NOTHING;

-- name: CountLiveUsersByIDs :one
SELECT COUNT(*) FROM end_user WHERE id = ANY(sqlc.arg(ids)::bigint[]) AND deleted_at IS NULL;

-- name: SetChainExitAssignMode :exec
UPDATE chain_exit SET assign_mode = sqlc.arg(assign_mode) WHERE id = sqlc.arg(id) AND user_id IS NULL;

-- name: UpdatePlatformChainExit :execrows
UPDATE chain_exit
SET name = sqlc.arg(name), remark = sqlc.arg(remark), is_enabled = sqlc.arg(is_enabled),
    allow_server = sqlc.arg(allow_server), allow_client = sqlc.arg(allow_client), updated_at = now()
WHERE id = sqlc.arg(id) AND user_id IS NULL;

-- name: SetChainExitEnabled :execrows
UPDATE chain_exit SET is_enabled = sqlc.arg(is_enabled), updated_at = now() WHERE id = sqlc.arg(id);

-- name: DeleteChainExitByID :execrows
DELETE FROM chain_exit WHERE id = $1;

-- name: GetPlatformChainExitLink :one
SELECT link_enc FROM chain_exit WHERE id = $1 AND user_id IS NULL;

-- 管理端：规则 ------------------------------------------------------------------------------

-- name: CountAdminChainRoutes :one
SELECT COUNT(*) FROM chain_route r
WHERE (sqlc.narg(kind)::text IS NULL OR r.kind = sqlc.narg(kind))
  AND (sqlc.narg(user_id)::bigint IS NULL OR r.user_id = sqlc.narg(user_id))
  AND (sqlc.narg(server_id)::bigint IS NULL OR EXISTS (
        SELECT 1 FROM chain_route_entry e JOIN node n ON n.id = e.entry_node_id JOIN inbound i ON i.id = n.inbound_id
        WHERE e.route_id = r.id AND i.server_id = sqlc.narg(server_id)));

-- name: ListAdminChainRoutes :many
-- 全站规则（管理端只读，Ruling-41），按 id 倒序分页；只取 id 与归属，内容由调用方按用户整体装配
SELECT r.id, r.user_id, u.username
FROM chain_route r
JOIN end_user u ON u.id = r.user_id
WHERE (sqlc.narg(kind)::text IS NULL OR r.kind = sqlc.narg(kind))
  AND (sqlc.narg(user_id)::bigint IS NULL OR r.user_id = sqlc.narg(user_id))
  AND (sqlc.narg(server_id)::bigint IS NULL OR EXISTS (
        SELECT 1 FROM chain_route_entry e JOIN node n ON n.id = e.entry_node_id JOIN inbound i ON i.id = n.inbound_id
        WHERE e.route_id = r.id AND i.server_id = sqlc.narg(server_id)))
ORDER BY r.id DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- 服务端链式的下发（docs/03 §5.19）-----------------------------------------------------------

-- name: ListServerProviderTypes :many
-- 各服务器的面板类型，用来判断能否改内核配置
SELECT id, provider_type FROM server ORDER BY id;

-- name: ListChainApplyServerIDs :many
-- 需要检查下发的服务器：启用中，且开着平台拦截规则（include_all，Ruling-55）、有起飞节点在它上面的服务端规则，
-- 或有过下发记录（面板上可能还留着平台条目，如刚关掉拦截规则）
SELECT s.id FROM server s
WHERE NOT s.is_disabled
  AND (sqlc.arg(include_all)::boolean
       OR EXISTS (SELECT 1 FROM chain_route r JOIN chain_route_entry e ON e.route_id = r.id
               JOIN node n ON n.id = e.entry_node_id JOIN inbound i ON i.id = n.inbound_id
               WHERE r.kind = 'server' AND i.server_id = s.id)
       OR EXISTS (SELECT 1 FROM chain_apply a WHERE a.server_id = s.id))
ORDER BY s.id;

-- name: ListServerRouteUsersOnServer :many
-- 有服务端规则的起飞节点在这台服务器上的用户
SELECT DISTINCT r.user_id
FROM chain_route r
JOIN chain_route_entry e ON e.route_id = r.id
JOIN node n ON n.id = e.entry_node_id
JOIN inbound i ON i.id = n.inbound_id
JOIN end_user u ON u.id = r.user_id AND u.deleted_at IS NULL
WHERE r.kind = 'server' AND i.server_id = $1
ORDER BY r.user_id;

-- name: GetDBNow :one
-- 数据库的当前时间：下发记录的 applied_at 用它，才能与规则的 updated_at 直接比较
SELECT now()::timestamptz AS db_now;

-- name: GetProvisionEmail :one
SELECT email FROM provision WHERE user_id = sqlc.arg(user_id) AND server_id = sqlc.arg(server_id);

-- name: GetChainApply :one
SELECT server_id, status, desired_hash, applied_rule_ids, fail_count, last_error, last_attempt_at, next_attempt_at, applied_at, backup_enc, updated_at
FROM chain_apply WHERE server_id = $1;

-- name: ListChainApplies :many
SELECT server_id, status, applied_rule_ids, applied_at FROM chain_apply WHERE server_id = ANY(sqlc.arg(server_ids)::bigint[]);

-- name: UpsertChainApply :exec
INSERT INTO chain_apply (server_id, status, desired_hash, applied_rule_ids, fail_count, last_error, last_attempt_at, next_attempt_at, applied_at, backup_enc, updated_at)
VALUES (sqlc.arg(server_id), sqlc.arg(status), sqlc.arg(desired_hash), sqlc.arg(applied_rule_ids), sqlc.arg(fail_count), sqlc.arg(last_error),
        sqlc.narg(last_attempt_at), sqlc.narg(next_attempt_at), sqlc.narg(applied_at), sqlc.narg(backup_enc), now())
ON CONFLICT (server_id) DO UPDATE SET status = EXCLUDED.status, desired_hash = EXCLUDED.desired_hash, applied_rule_ids = EXCLUDED.applied_rule_ids,
    fail_count = EXCLUDED.fail_count, last_error = EXCLUDED.last_error, last_attempt_at = EXCLUDED.last_attempt_at,
    next_attempt_at = EXCLUDED.next_attempt_at, applied_at = EXCLUDED.applied_at, backup_enc = EXCLUDED.backup_enc, updated_at = now();

-- name: ResetChainApplyFailures :execrows
-- 管理员重试（docs/03 §3 C4）：清零失败次数与退避
UPDATE chain_apply SET fail_count = 0, next_attempt_at = NULL, updated_at = now() WHERE server_id = $1;
