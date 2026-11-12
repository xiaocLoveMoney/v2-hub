-- 入站与对外地址缓存（docs/03 §5.1）------------------------------------------

-- name: ListInboundsByServer :many
SELECT id, server_id, remote_ref, protocol, port, listen, tag, remark, is_enabled, network, security,
       core_type, config, client_count, synced_hash, drift_detected_at, is_missing, created_at
FROM inbound
WHERE server_id = $1
ORDER BY port, id;

-- name: GetInbound :one
SELECT id, server_id, remote_ref, protocol, port, listen, tag, remark, is_enabled, network, security,
       core_type, config, client_count, synced_hash, drift_detected_at, is_missing, created_at
FROM inbound
WHERE id = $1;

-- name: ListInboundsByIDs :many
SELECT id, server_id, remote_ref, is_missing FROM inbound WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: UpsertInboundFromPlatform :one
-- 平台写入面板成功后的落库：摘要即“已确认”状态，清除漂移与缺失标记
INSERT INTO inbound (server_id, remote_ref, protocol, port, listen, tag, remark, is_enabled, network, security,
                     core_type, config, client_count, synced_hash, drift_detected_at, is_missing)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, NULL, FALSE)
ON CONFLICT (server_id, remote_ref) WHERE NOT is_missing DO UPDATE
SET protocol = EXCLUDED.protocol, port = EXCLUDED.port, listen = EXCLUDED.listen, tag = EXCLUDED.tag,
    remark = EXCLUDED.remark, is_enabled = EXCLUDED.is_enabled, network = EXCLUDED.network,
    security = EXCLUDED.security, core_type = EXCLUDED.core_type, config = EXCLUDED.config,
    client_count = EXCLUDED.client_count, synced_hash = EXCLUDED.synced_hash,
    drift_detected_at = NULL, updated_at = now()
RETURNING id;

-- name: RetireInboundByRef :many
-- 面板把该标识分给了新入站：仍挂着它的未缺失记录对应的旧入站已被删，标记缺失（docs/03 §5.1 面板标识复用）
UPDATE inbound SET is_missing = TRUE, drift_detected_at = NULL, updated_at = now()
WHERE server_id = $1 AND remote_ref = $2 AND NOT is_missing
RETURNING id;

-- name: UpdateInboundFromSync :exec
-- 同步时以面板为准刷新缓存；drift_detected_at 由调用方按漂移规则给出（保留已有标记）。缺失是终态，不刷新
UPDATE inbound
SET protocol = $2, port = $3, listen = $4, tag = $5, remark = $6, is_enabled = $7, network = $8,
    security = $9, core_type = $10, config = $11, client_count = $12, drift_detected_at = $13, updated_at = now()
WHERE id = $1 AND NOT is_missing;

-- name: MarkInboundsMissing :execrows
UPDATE inbound SET is_missing = TRUE, updated_at = now()
WHERE server_id = sqlc.arg(server_id) AND NOT is_missing AND NOT (remote_ref = ANY(sqlc.arg(present_refs)::text[]));

-- name: SetInboundEnabledLocal :exec
UPDATE inbound SET is_enabled = $2, updated_at = now() WHERE id = $1;

-- name: DeleteInbound :exec
DELETE FROM inbound WHERE id = $1;

-- name: ListEndpointsByServer :many
SELECT e.id, e.inbound_id, e.remote_group_ref, e.address, e.port, e.remark, e.is_disabled, e.is_missing
FROM endpoint e
JOIN inbound i ON i.id = e.inbound_id
WHERE i.server_id = $1
ORDER BY e.id;

-- name: GetEndpoint :one
SELECT e.id, e.inbound_id, e.remote_group_ref, e.address, e.is_missing, i.server_id,
       i.remote_ref AS inbound_ref, i.is_missing AS inbound_missing
FROM endpoint e
JOIN inbound i ON i.id = e.inbound_id
WHERE e.id = $1;

-- name: UpsertEndpoint :one
INSERT INTO endpoint (inbound_id, remote_group_ref, address, port, remark, is_disabled, is_missing)
VALUES ($1, $2, $3, $4, $5, $6, FALSE)
ON CONFLICT (inbound_id, remote_group_ref, address) DO UPDATE
SET port = EXCLUDED.port, remark = EXCLUDED.remark, is_disabled = EXCLUDED.is_disabled,
    is_missing = FALSE, updated_at = now()
RETURNING id;

-- name: MarkEndpointsMissing :execrows
UPDATE endpoint e SET is_missing = TRUE, updated_at = now()
FROM inbound i
WHERE i.id = e.inbound_id AND i.server_id = sqlc.arg(server_id) AND NOT e.is_missing
  AND NOT (e.id = ANY(sqlc.arg(present_ids)::bigint[]));

-- name: DeleteEndpoint :exec
DELETE FROM endpoint WHERE id = $1;

-- name: RepointEndpoint :exec
-- 删除对外地址拆分了面板上的组：被挪到新组的地址改写组标识，保留原记录与节点（docs/03 §5.1）
UPDATE endpoint e SET remote_group_ref = sqlc.arg(new_group_ref), updated_at = now()
FROM inbound i
WHERE i.id = e.inbound_id AND i.server_id = sqlc.arg(server_id) AND i.remote_ref = sqlc.arg(inbound_ref) AND NOT i.is_missing
  AND e.remote_group_ref = sqlc.arg(old_group_ref) AND e.address = sqlc.arg(address);
