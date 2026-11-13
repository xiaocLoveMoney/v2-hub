-- 客户端链式：链式规则与自有落地节点（docs/03 §5.18）------------------------------------

-- name: GetUserChainFlags :one
-- 当前套餐的两个开关；没有套餐时都为假
SELECT coalesce(p.allow_client_chain, FALSE)::boolean AS allow_client_chain,
       coalesce(p.allow_custom_node, FALSE)::boolean AS allow_custom_node
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
