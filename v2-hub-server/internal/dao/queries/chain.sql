-- 客户端链式：链式规则与自有落地节点（docs/03 §5.18）------------------------------------

-- name: GetUserChainFlags :one
-- 当前套餐的链式开关；没有套餐时都为假
SELECT coalesce(p.allow_client_chain, FALSE)::boolean AS allow_client_chain,
       coalesce(p.allow_custom_node, FALSE)::boolean AS allow_custom_node,
       coalesce(p.allow_server_chain, FALSE)::boolean AS allow_server_chain
FROM end_user u LEFT JOIN plan p ON p.id = u.plan_id
WHERE u.id = $1 AND u.deleted_at IS NULL;

-- name: ListChainRules :many
-- 某用户的客户端链式规则，带入口与落地的显示信息；rule_id 非空时只取这一条（归属仍按 user_id 约束，R8）
SELECT r.id, r.name, r.is_enabled, r.created_at,
       r.entry_node_id, en.display_name AS entry_name,
       coalesce(r.exit_node_id, 0)::bigint AS exit_node_id, coalesce(xn.display_name, '')::text AS exit_node_name,
       coalesce(xi.protocol, '')::text AS exit_node_protocol,
       coalesce(r.exit_custom_id, 0)::bigint AS exit_custom_id, coalesce(c.name, '')::text AS exit_custom_name,
       coalesce(c.protocol, '')::text AS exit_custom_protocol
FROM chain_rule r
JOIN node en ON en.id = r.entry_node_id
LEFT JOIN node xn ON xn.id = r.exit_node_id
LEFT JOIN inbound xi ON xi.id = xn.inbound_id
LEFT JOIN custom_node c ON c.id = r.exit_custom_id
WHERE r.user_id = sqlc.arg(user_id) AND r.kind = 'client'
  AND (sqlc.narg(rule_id)::bigint IS NULL OR r.id = sqlc.narg(rule_id))
ORDER BY r.id;

-- name: CountChainRules :one
SELECT COUNT(*) FROM chain_rule WHERE user_id = $1 AND kind = 'client';

-- name: InsertChainRule :one
INSERT INTO chain_rule (user_id, kind, name, entry_node_id, exit_node_id, exit_custom_id, is_enabled)
VALUES (sqlc.arg(user_id), 'client', sqlc.arg(name), sqlc.arg(entry_node_id), sqlc.narg(exit_node_id), sqlc.narg(exit_custom_id), sqlc.arg(is_enabled))
RETURNING id;

-- name: UpdateChainRule :execrows
UPDATE chain_rule SET name = sqlc.arg(name), is_enabled = sqlc.arg(is_enabled), updated_at = now()
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) AND kind = 'client';

-- name: DeleteChainRule :execrows
DELETE FROM chain_rule WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) AND kind = 'client';

-- name: DeleteChainRulesByUser :exec
DELETE FROM chain_rule WHERE user_id = $1;

-- name: ListCustomNodes :many
-- 某用户的自有落地节点及各自被多少条规则引用；node_id 非空时只取这一个
SELECT c.id, c.name, c.protocol, c.address, c.port, c.created_at,
       (SELECT COUNT(*) FROM chain_rule r WHERE r.exit_custom_id = c.id)::int AS rule_count
FROM custom_node c
WHERE c.user_id = sqlc.arg(user_id) AND (sqlc.narg(node_id)::bigint IS NULL OR c.id = sqlc.narg(node_id))
ORDER BY c.id;

-- name: CountCustomNodes :one
SELECT COUNT(*) FROM custom_node WHERE user_id = $1;

-- name: InsertCustomNode :one
-- 链接密文的关联数据含 id，所以先插入空值，拿到 id 后由 SetCustomNodeLink 回填
INSERT INTO custom_node (user_id, name, protocol, address, port, link_enc)
VALUES ($1, $2, $3, $4, $5, ''::bytea)
RETURNING id;

-- name: SetCustomNodeLink :exec
UPDATE custom_node SET link_enc = sqlc.arg(link_enc), protocol = sqlc.arg(protocol), address = sqlc.arg(address), port = sqlc.arg(port), updated_at = now()
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id);

-- name: RenameCustomNode :execrows
UPDATE custom_node SET name = sqlc.arg(name), updated_at = now()
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id);

-- name: DeleteCustomNode :execrows
DELETE FROM custom_node WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id);

-- name: DeleteCustomNodesByUser :exec
DELETE FROM custom_node WHERE user_id = $1;

-- name: GetCustomNodeLink :one
SELECT link_enc FROM custom_node WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id);

-- name: ListCustomNodeLinks :many
-- 渲染订阅用：该用户全部自有落地节点的名称与链接密文
SELECT id, name, link_enc FROM custom_node WHERE user_id = $1 ORDER BY id;

-- 服务端链式：规则与下发记录（docs/03 §5.19）------------------------------------------------

-- name: ListServerChainRules :many
-- 某用户的服务端规则；rule_id 非空时只取这一条（归属按 user_id 约束，R8）
SELECT r.id, r.is_enabled, r.created_at,
       r.entry_node_id, en.display_name AS entry_name, ei.server_id AS entry_server_id,
       r.exit_node_id::bigint AS exit_node_id, xn.display_name AS exit_name, xi.protocol AS exit_protocol
FROM chain_rule r
JOIN node en ON en.id = r.entry_node_id
JOIN inbound ei ON ei.id = en.inbound_id
JOIN node xn ON xn.id = r.exit_node_id
JOIN inbound xi ON xi.id = xn.inbound_id
WHERE r.user_id = sqlc.arg(user_id) AND r.kind = 'server'
  AND (sqlc.narg(rule_id)::bigint IS NULL OR r.id = sqlc.narg(rule_id))
ORDER BY r.id;

-- name: CountServerChainRules :one
SELECT COUNT(*) FROM chain_rule WHERE user_id = $1 AND kind = 'server';

-- name: InsertServerChainRule :one
INSERT INTO chain_rule (user_id, kind, entry_node_id, exit_node_id, is_enabled)
VALUES (sqlc.arg(user_id), 'server', sqlc.arg(entry_node_id), sqlc.arg(exit_node_id), sqlc.arg(is_enabled))
RETURNING id;

-- name: SetServerChainRuleEnabled :execrows
UPDATE chain_rule SET is_enabled = sqlc.arg(is_enabled), updated_at = now()
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) AND kind = 'server';

-- name: DeleteServerChainRule :execrows
DELETE FROM chain_rule WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) AND kind = 'server';

-- name: ListServerProviderTypes :many
-- 各服务器的面板类型，用来判断入口能否改内核配置
SELECT id, provider_type FROM server ORDER BY id;

-- name: ListChainApplyServerIDs :many
-- 需要检查下发的服务器：启用中，且有以它为入口的服务端规则，或面板上还留着平台条目
SELECT s.id FROM server s
WHERE NOT s.is_disabled
  AND (EXISTS (SELECT 1 FROM chain_rule r JOIN node n ON n.id = r.entry_node_id JOIN inbound i ON i.id = n.inbound_id
               WHERE r.kind = 'server' AND i.server_id = s.id)
       OR EXISTS (SELECT 1 FROM chain_apply a WHERE a.server_id = s.id AND cardinality(a.applied_rule_ids) > 0))
ORDER BY s.id;

-- name: ListServerChainRulesOnServer :many
-- 以某台服务器为入口的全部服务端规则，带入口入站的 tag
SELECT r.id, r.user_id, r.is_enabled, r.entry_node_id, r.exit_node_id::bigint AS exit_node_id, i.tag AS inbound_tag
FROM chain_rule r
JOIN node n ON n.id = r.entry_node_id
JOIN inbound i ON i.id = n.inbound_id
JOIN end_user u ON u.id = r.user_id AND u.deleted_at IS NULL
WHERE r.kind = 'server' AND i.server_id = $1
ORDER BY r.id;

-- name: GetProvisionEmail :one
SELECT email FROM provision WHERE user_id = sqlc.arg(user_id) AND server_id = sqlc.arg(server_id);

-- name: GetChainApply :one
SELECT server_id, status, desired_hash, applied_rule_ids, fail_count, last_error, last_attempt_at, next_attempt_at, applied_at, backup_enc, updated_at
FROM chain_apply WHERE server_id = $1;

-- name: ListChainApplies :many
SELECT server_id, status, applied_rule_ids FROM chain_apply WHERE server_id = ANY(sqlc.arg(server_ids)::bigint[]);

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

-- name: CountAdminServerChainRules :one
SELECT COUNT(*) FROM chain_rule r JOIN node n ON n.id = r.entry_node_id JOIN inbound i ON i.id = n.inbound_id
WHERE r.kind = 'server'
  AND (sqlc.narg(user_id)::bigint IS NULL OR r.user_id = sqlc.narg(user_id))
  AND (sqlc.narg(server_id)::bigint IS NULL OR i.server_id = sqlc.narg(server_id));

-- name: ListAdminServerChainRules :many
-- 全站服务端规则（管理端只读，Ruling-41），按 id 倒序分页
SELECT r.id, r.user_id, u.username, r.is_enabled, r.created_at,
       r.entry_node_id, en.display_name AS entry_name, ei.server_id AS entry_server_id, s.name AS entry_server_name,
       r.exit_node_id::bigint AS exit_node_id, xn.display_name AS exit_name
FROM chain_rule r
JOIN end_user u ON u.id = r.user_id
JOIN node en ON en.id = r.entry_node_id
JOIN inbound ei ON ei.id = en.inbound_id
JOIN server s ON s.id = ei.server_id
JOIN node xn ON xn.id = r.exit_node_id
WHERE r.kind = 'server'
  AND (sqlc.narg(user_id)::bigint IS NULL OR r.user_id = sqlc.narg(user_id))
  AND (sqlc.narg(server_id)::bigint IS NULL OR ei.server_id = sqlc.narg(server_id))
ORDER BY r.id DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);
