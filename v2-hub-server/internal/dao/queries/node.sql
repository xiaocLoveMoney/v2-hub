-- 节点与节点组（docs/03 §5.1）-----------------------------------------------

-- name: EnsureNode :exec
-- 同一来源（入站 × 对外地址）只生成一个节点；已存在时保留管理员改过的展示属性
INSERT INTO node (inbound_id, endpoint_id, display_name)
VALUES ($1, $2, $3)
ON CONFLICT (inbound_id, endpoint_id) DO NOTHING;

-- name: RenameLegacyDirectNodes :execrows
-- 旧默认名“服务器名 · 入站称呼”去掉服务器名前缀，只改直连节点（Ruling-63）
UPDATE node n
SET display_name = substr(n.display_name, char_length(s.name) + char_length(sqlc.arg(separator)::text) + 1),
    updated_at   = now()
FROM inbound i
JOIN server s ON s.id = i.server_id
WHERE n.inbound_id = i.id
  AND n.endpoint_id IS NULL
  AND char_length(n.display_name) > char_length(s.name) + char_length(sqlc.arg(separator)::text)
  AND left(n.display_name, char_length(s.name) + char_length(sqlc.arg(separator)::text)) = s.name || sqlc.arg(separator)::text;

-- name: CountNodes :one
SELECT COUNT(*)
FROM node n
JOIN inbound i ON i.id = n.inbound_id
WHERE (sqlc.narg(server_id)::bigint IS NULL OR i.server_id = sqlc.narg(server_id))
  AND (sqlc.narg(group_id)::bigint IS NULL OR EXISTS (
        SELECT 1 FROM node_group_item gi WHERE gi.node_id = n.id AND gi.node_group_id = sqlc.narg(group_id)));

-- name: ListNodes :many
SELECT n.id, n.inbound_id, n.endpoint_id, n.display_name, s.region, n.tags, n.sort_order, n.is_enabled,
       i.server_id, s.name AS server_name, s.base_url, i.protocol, i.port AS inbound_port,
       COALESCE(e.address, '')::text AS endpoint_address, COALESCE(e.port, 0)::int AS endpoint_port,
       (i.is_missing OR COALESCE(e.is_missing, FALSE))::boolean AS is_missing,
       COALESCE((SELECT array_agg(gi.node_group_id ORDER BY gi.node_group_id)
                 FROM node_group_item gi WHERE gi.node_id = n.id), '{}')::bigint[] AS group_ids
FROM node n
JOIN inbound i ON i.id = n.inbound_id
JOIN server s ON s.id = i.server_id
LEFT JOIN endpoint e ON e.id = n.endpoint_id
WHERE (sqlc.narg(server_id)::bigint IS NULL OR i.server_id = sqlc.narg(server_id))
  AND (sqlc.narg(group_id)::bigint IS NULL OR EXISTS (
        SELECT 1 FROM node_group_item gi WHERE gi.node_id = n.id AND gi.node_group_id = sqlc.narg(group_id)))
ORDER BY n.sort_order, n.id
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: UpdateNode :execrows
UPDATE node SET display_name = $2, tags = $3, sort_order = $4, is_enabled = $5, updated_at = now()
WHERE id = $1;

-- name: CountNodesByIDs :one
SELECT COUNT(*) FROM node WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: ListNodeGroups :many
SELECT g.id, g.name, g.remark,
       COALESCE((SELECT array_agg(gi.node_id ORDER BY gi.node_id)
                 FROM node_group_item gi WHERE gi.node_group_id = g.id), '{}')::bigint[] AS node_ids
FROM node_group g
ORDER BY g.id;

-- name: CreateNodeGroup :one
INSERT INTO node_group (name, remark) VALUES ($1, $2) RETURNING id;

-- name: UpdateNodeGroup :execrows
UPDATE node_group SET name = $2, remark = $3, updated_at = now() WHERE id = $1;

-- name: DeleteNodeGroup :execrows
DELETE FROM node_group WHERE id = $1;

-- name: ClearNodeGroupItems :exec
DELETE FROM node_group_item WHERE node_group_id = $1;

-- name: InsertNodeGroupItems :exec
INSERT INTO node_group_item (node_group_id, node_id)
SELECT sqlc.arg(group_id)::bigint, unnest(sqlc.arg(node_ids)::bigint[]);

-- name: GetDirectNodeID :one
-- 入站的直连节点（一键部署把它加进节点组，Ruling-76）
SELECT id FROM node WHERE inbound_id = $1 AND endpoint_id IS NULL;
