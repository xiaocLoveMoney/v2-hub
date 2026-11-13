-- 套餐、用户、下发与用量（docs/03 §5.2）-------------------------------------------

-- name: ListPlans :many
SELECT p.id, p.name, p.traffic_limit, p.duration_days, p.start_on_first_use, p.limit_ip, p.renew_every_days,
       p.renew_month_day, p.renew_weekday, p.renew_max, p.traffic_reset, p.traffic_reset_day,
       p.allow_client_chain, p.allow_custom_node, p.remark, p.created_at,
       COALESCE((SELECT array_agg(g.node_group_id ORDER BY g.node_group_id) FROM plan_node_group g WHERE g.plan_id = p.id), '{}')::bigint[] AS node_group_ids,
       (SELECT COUNT(*) FROM end_user u WHERE u.plan_id = p.id AND u.deleted_at IS NULL)::int AS user_count
FROM plan p
ORDER BY p.id;

-- name: GetPlan :one
SELECT id, name, traffic_limit, duration_days, start_on_first_use, limit_ip, renew_every_days, renew_month_day,
       renew_weekday, renew_max, traffic_reset, traffic_reset_day
FROM plan WHERE id = $1;

-- name: CreatePlan :one
INSERT INTO plan (name, traffic_limit, duration_days, start_on_first_use, limit_ip, renew_every_days, renew_month_day,
                  renew_weekday, renew_max, traffic_reset, traffic_reset_day, allow_client_chain, allow_custom_node, remark)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
RETURNING id;

-- name: UpdatePlan :execrows
UPDATE plan SET name = $2, traffic_limit = $3, duration_days = $4, start_on_first_use = $5, limit_ip = $6,
    renew_every_days = $7, renew_month_day = $8, renew_weekday = $9, renew_max = $10, traffic_reset = $11,
    traffic_reset_day = $12, allow_client_chain = $13, allow_custom_node = $14, remark = $15, updated_at = now()
WHERE id = $1;

-- name: DeletePlan :execrows
DELETE FROM plan WHERE id = $1;

-- name: ClearPlanNodeGroups :exec
DELETE FROM plan_node_group WHERE plan_id = $1;

-- name: InsertPlanNodeGroups :exec
INSERT INTO plan_node_group (plan_id, node_group_id)
SELECT sqlc.arg(plan_id)::bigint, unnest(sqlc.arg(group_ids)::bigint[]);

-- name: CountNodeGroupsByIDs :one
SELECT COUNT(*) FROM node_group WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: ListUsers :many
SELECT u.id, u.username, u.remark, u.plan_id, COALESCE(p.name, '')::text AS plan_name, u.traffic_limit, u.traffic_used,
       u.expires_at, u.pending_days, u.limit_ip, u.renew_every_days, u.renew_month_day, u.renew_weekday, u.renew_max,
       u.renew_count, u.traffic_reset, u.traffic_reset_day, u.is_disabled, u.status, u.created_at,
       (SELECT COUNT(*) FROM provision v WHERE v.user_id = u.id AND v.status = 'synced')::int AS synced_count,
       (SELECT COUNT(*) FROM provision v WHERE v.user_id = u.id AND v.status = 'pending')::int AS pending_count,
       (SELECT COUNT(*) FROM provision v WHERE v.user_id = u.id AND v.status = 'failed')::int AS failed_count
FROM end_user u
LEFT JOIN plan p ON p.id = u.plan_id
WHERE u.deleted_at IS NULL
  AND (sqlc.narg(user_id)::bigint IS NULL OR u.id = sqlc.narg(user_id))
  AND (sqlc.narg(keyword)::text IS NULL OR u.username ILIKE '%' || sqlc.narg(keyword) || '%' OR u.remark ILIKE '%' || sqlc.narg(keyword) || '%') -- rule-ignore: 管理端按用户名 / 备注模糊搜索，只给管理员用且有分页上限；本项目是 PostgreSQL，mysql.md 不适用
  AND (sqlc.narg(status)::text IS NULL OR u.status = sqlc.narg(status))
  AND (sqlc.narg(plan_id)::bigint IS NULL OR u.plan_id = sqlc.narg(plan_id))
ORDER BY u.id DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: CountUsers :one
SELECT COUNT(*) FROM end_user u
WHERE u.deleted_at IS NULL
  AND (sqlc.narg(keyword)::text IS NULL OR u.username ILIKE '%' || sqlc.narg(keyword) || '%' OR u.remark ILIKE '%' || sqlc.narg(keyword) || '%') -- rule-ignore: 管理端按用户名 / 备注模糊搜索，只给管理员用且有分页上限；本项目是 PostgreSQL，mysql.md 不适用
  AND (sqlc.narg(status)::text IS NULL OR u.status = sqlc.narg(status))
  AND (sqlc.narg(plan_id)::bigint IS NULL OR u.plan_id = sqlc.narg(plan_id));

-- name: GetUserQuota :one
-- 读写配额前加行锁，避免流量任务与管理员操作互相覆盖
SELECT id, plan_id, traffic_limit, traffic_used, expires_at, pending_days, limit_ip, renew_every_days, renew_month_day,
       renew_weekday, renew_max, renew_count, traffic_reset, traffic_reset_day, last_traffic_reset_at, is_disabled, status
FROM end_user WHERE id = $1 AND deleted_at IS NULL
FOR UPDATE;

-- name: CreateUser :one
INSERT INTO end_user (username, password_hash, remark, email_code, client_cred_enc)
VALUES ($1, $2, $3, $4, ''::bytea)
RETURNING id;

-- name: SetUserCredential :exec
UPDATE end_user SET client_cred_enc = $2 WHERE id = $1;

-- name: SaveUserQuota :exec
UPDATE end_user SET plan_id = $2, traffic_limit = $3, traffic_used = $4, expires_at = $5, pending_days = $6, limit_ip = $7,
    renew_every_days = $8, renew_month_day = $9, renew_weekday = $10, renew_max = $11, renew_count = $12,
    traffic_reset = $13, traffic_reset_day = $14, last_traffic_reset_at = $15, is_disabled = $16, status = $17, updated_at = now()
WHERE id = $1;

-- name: UpdateUserRemark :execrows
UPDATE end_user SET remark = $2, updated_at = now() WHERE id = $1 AND deleted_at IS NULL;

-- name: SetUserPasswordHash :execrows
UPDATE end_user SET password_hash = $2, updated_at = now() WHERE id = $1 AND deleted_at IS NULL;

-- name: MarkUserDeleted :execrows
UPDATE end_user SET deleted_at = now(), status = 'disabled', updated_at = now() WHERE id = $1 AND deleted_at IS NULL;

-- name: PurgeDeletedUsers :execrows
-- 待删除且各面板 client 已删完的用户，删除记录（docs/03 §5.2 删除用户）
DELETE FROM end_user u WHERE u.deleted_at IS NOT NULL AND NOT EXISTS (SELECT 1 FROM provision v WHERE v.user_id = u.id);

-- name: ListActiveUserIDs :many
SELECT id FROM end_user WHERE deleted_at IS NULL ORDER BY id;

-- name: ListDesiredClients :many
-- 期望状态：每个（用户, 服务器）上应挂的入站。来源 = 套餐节点组里启用的节点，入站未缺失、服务器未停用（docs/03 §5.2 授权）
SELECT DISTINCT u.id AS user_id, i.remote_ref
FROM end_user u
JOIN plan_node_group pg ON pg.plan_id = u.plan_id
JOIN node_group_item gi ON gi.node_group_id = pg.node_group_id
JOIN node n ON n.id = gi.node_id AND n.is_enabled
JOIN inbound i ON i.id = n.inbound_id AND NOT i.is_missing
JOIN server s ON s.id = i.server_id AND NOT s.is_disabled
WHERE u.deleted_at IS NULL AND i.server_id = $1;

-- name: ListUsersByIDs :many
-- 对账需要的用户信息：已有下发或期望下发到该服务器的用户（含待删除的）
SELECT u.id, u.status, u.limit_ip, u.email_code, u.client_cred_enc, (u.deleted_at IS NOT NULL)::boolean AS is_deleted
FROM end_user u
WHERE u.id = ANY(sqlc.arg(ids)::bigint[]);

-- name: ListServerProvisions :many
SELECT id, user_id, email, status, retry_count, last_up, last_down FROM provision WHERE server_id = $1;

-- name: UpsertProvision :exec
INSERT INTO provision (user_id, server_id, email, status, last_error, retry_count, synced_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (user_id, server_id) DO UPDATE
SET email = EXCLUDED.email, status = EXCLUDED.status, last_error = EXCLUDED.last_error,
    retry_count = EXCLUDED.retry_count, synced_at = COALESCE(EXCLUDED.synced_at, provision.synced_at), updated_at = now();

-- name: DeleteProvision :exec
DELETE FROM provision WHERE user_id = $1 AND server_id = $2;

-- name: SetProvisionCounters :exec
UPDATE provision SET last_up = $2, last_down = $3 WHERE id = $1;

-- name: MarkUserProvisionsPending :exec
UPDATE provision SET status = 'pending', updated_at = now() WHERE user_id = $1 AND status = 'synced';

-- name: ListUserProvisions :many
SELECT v.server_id, s.name AS server_name, v.email, v.status, v.last_error, v.retry_count, v.synced_at, v.updated_at
FROM provision v JOIN server s ON s.id = v.server_id
WHERE v.user_id = $1
ORDER BY v.server_id;

-- name: ListUserServerIDs :many
-- 用户涉及的服务器：已有下发的，加上期望下发到的
SELECT DISTINCT server_id FROM (
  SELECT v.server_id FROM provision v WHERE v.user_id = sqlc.arg(user_id)
  UNION
  SELECT i.server_id
  FROM end_user u
  JOIN plan_node_group pg ON pg.plan_id = u.plan_id
  JOIN node_group_item gi ON gi.node_group_id = pg.node_group_id
  JOIN node n ON n.id = gi.node_id
  JOIN inbound i ON i.id = n.inbound_id
  WHERE u.id = sqlc.arg(user_id)
) t;

-- name: AddUserTraffic :exec
INSERT INTO user_traffic_hourly (user_id, hour, up, down) VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, hour) DO UPDATE SET up = user_traffic_hourly.up + EXCLUDED.up, down = user_traffic_hourly.down + EXCLUDED.down;
