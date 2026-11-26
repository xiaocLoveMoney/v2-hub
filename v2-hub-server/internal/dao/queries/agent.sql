-- 二级代理（docs/03 §5.23，Ruling-101 – Ruling-104）-------------------------------------
-- 代理侧（Agent 前缀）的每个查询都必须带 agent_id = 当前代理 的条件，agent_id 只取自会话，不接受请求里的值。

-- ---- 超管侧：代理账号与配额 ----

-- name: CreateAgentProfile :exec
INSERT INTO agent_profile (admin_id, max_users, traffic_pool_total, remark)
VALUES ($1, $2, $3, $4);

-- name: UpdateAgentProfile :exec
UPDATE agent_profile SET max_users = $2, traffic_pool_total = $3, remark = $4, updated_at = now()
WHERE admin_id = $1;

-- name: LockAgentProfile :one
-- 配额判断前锁住代理的配额行（Ruling-103）：开通、续期、新建与注册都先锁它再锁用户行，顺序固定防死锁
SELECT p.admin_id, p.max_users, p.traffic_pool_total, p.traffic_pool_used, p.remark, a.is_disabled
FROM agent_profile p
JOIN admin a ON a.id = p.admin_id AND a.kind = 'agent'
WHERE p.admin_id = $1
FOR UPDATE OF p;

-- name: AddAgentPoolUsed :execrows
-- 扣流量池；超过总量时不更新（调用方已在同一事务里锁行并判断过，这里再兜一层）
UPDATE agent_profile
SET traffic_pool_used = traffic_pool_used + sqlc.arg(amount)::bigint, updated_at = now()
WHERE admin_id = sqlc.arg(admin_id) AND traffic_pool_used <= traffic_pool_total - sqlc.arg(amount)::bigint;

-- name: CountAgentUsers :one
-- 名下未删除用户数（用户数上限只计这些，docs/03 §5.23）
SELECT COUNT(*) FROM end_user WHERE agent_id = $1 AND deleted_at IS NULL;

-- name: ListAgents :many
SELECT a.id, a.username, a.is_disabled, (a.totp_secret_enc IS NOT NULL)::boolean AS totp_enabled, a.last_login_at, a.created_at,
       p.remark, p.max_users, p.traffic_pool_total, p.traffic_pool_used,
       (SELECT COUNT(*) FROM end_user u WHERE u.agent_id = a.id AND u.deleted_at IS NULL)::int AS user_count
FROM admin a
JOIN agent_profile p ON p.admin_id = a.id
WHERE a.kind = 'agent'
  AND (sqlc.narg(agent_id)::bigint IS NULL OR a.id = sqlc.narg(agent_id))
  AND (sqlc.narg(keyword)::text IS NULL OR strpos(lower(a.username), lower(sqlc.narg(keyword))) > 0)
ORDER BY a.created_at DESC, a.id DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: CountAgents :one
SELECT COUNT(*) FROM admin a
JOIN agent_profile p ON p.admin_id = a.id
WHERE a.kind = 'agent'
  AND (sqlc.narg(keyword)::text IS NULL OR strpos(lower(a.username), lower(sqlc.narg(keyword))) > 0);

-- name: ExistsAgent :one
SELECT EXISTS (SELECT 1 FROM admin WHERE id = $1 AND kind = 'agent')::boolean;

-- name: ListAgentPlanIDs :many
SELECT admin_id, plan_id FROM agent_plan WHERE admin_id = ANY(sqlc.arg(admin_ids)::bigint[]) ORDER BY admin_id, plan_id;

-- name: DeleteAgentPlans :exec
DELETE FROM agent_plan WHERE admin_id = $1;

-- name: InsertAgentPlans :exec
INSERT INTO agent_plan (admin_id, plan_id)
SELECT sqlc.arg(admin_id)::bigint, unnest(sqlc.arg(plan_ids)::bigint[]);

-- name: ListPlanLimitsByIDs :many
-- 校验白名单：只收流量与时长都有限的套餐（Ruling-103）
SELECT id, traffic_limit, duration_days FROM plan WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: InsertAgentAllocation :exec
-- 开通记录只追加（Ruling-103）
INSERT INTO agent_allocation (agent_id, user_id, username, plan_id, plan_name, traffic_bytes, duration_days, kind)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: ListAgentAllocations :many
SELECT id, agent_id, user_id, username, plan_id, plan_name, traffic_bytes, duration_days, kind, created_at
FROM agent_allocation
WHERE agent_id = sqlc.arg(agent_id)
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR created_at >= sqlc.narg(from_time)::timestamptz)
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR created_at < sqlc.narg(to_time)::timestamptz)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: CountAgentAllocations :one
SELECT COUNT(*) FROM agent_allocation
WHERE agent_id = sqlc.arg(agent_id)
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR created_at >= sqlc.narg(from_time)::timestamptz)
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR created_at < sqlc.narg(to_time)::timestamptz);

-- ---- 代理侧：配额与套餐 ----

-- name: AgentGetSummary :one
SELECT p.max_users, p.traffic_pool_total, p.traffic_pool_used,
       (SELECT COUNT(*) FROM end_user u WHERE u.agent_id = p.admin_id AND u.deleted_at IS NULL)::int AS user_count
FROM agent_profile p
WHERE p.admin_id = $1;

-- name: AgentListPlans :many
SELECT pl.id, pl.name, pl.traffic_limit, pl.duration_days, pl.traffic_reset, pl.limit_ip
FROM agent_plan ap
JOIN plan pl ON pl.id = ap.plan_id
WHERE ap.admin_id = $1
ORDER BY pl.sort_order, pl.id;

-- name: AgentLockPlan :one
-- 白名单里的套餐；FOR SHARE 让本事务结束前套餐不能被改成不限。行结构须与 user.sql 的 GetPlan 逐字段相同
SELECT pl.id, pl.name, pl.traffic_limit, pl.duration_days, pl.start_on_first_use, pl.limit_ip, pl.traffic_reset, pl.traffic_reset_day,
       pl.is_on_sale, pl.is_trial
FROM plan pl
JOIN agent_plan ap ON ap.plan_id = pl.id AND ap.admin_id = sqlc.arg(agent_id)
WHERE pl.id = sqlc.arg(plan_id)
FOR SHARE OF pl;

-- ---- 代理侧：名下用户 ----

-- name: AgentListUsers :many
SELECT u.id, u.username, u.email, u.remark, u.status, u.is_disabled, u.plan_id, COALESCE(p.name, '')::text AS plan_name,
       u.traffic_limit, u.traffic_used, u.expires_at, u.pending_days, u.balance,
       (u.totp_secret_enc IS NOT NULL)::boolean AS totp_enabled, u.created_at, u.last_used_at
FROM end_user u
LEFT JOIN plan p ON p.id = u.plan_id
WHERE u.agent_id = sqlc.arg(agent_id) AND u.deleted_at IS NULL
  AND (sqlc.narg(user_id)::bigint IS NULL OR u.id = sqlc.narg(user_id))
  AND (sqlc.narg(keyword)::text IS NULL
       OR strpos(lower(u.username), lower(sqlc.narg(keyword))) > 0
       OR strpos(lower(u.remark), lower(sqlc.narg(keyword))) > 0
       OR strpos(lower(COALESCE(u.email, '')), lower(sqlc.narg(keyword))) > 0)
  AND (sqlc.narg(status)::text IS NULL OR u.status = sqlc.narg(status))
  AND (sqlc.narg(plan_id)::bigint IS NULL OR u.plan_id = sqlc.narg(plan_id))
ORDER BY u.created_at DESC, u.id DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: AgentCountUsers :one
SELECT COUNT(*) FROM end_user u
WHERE u.agent_id = sqlc.arg(agent_id) AND u.deleted_at IS NULL
  AND (sqlc.narg(keyword)::text IS NULL
       OR strpos(lower(u.username), lower(sqlc.narg(keyword))) > 0
       OR strpos(lower(u.remark), lower(sqlc.narg(keyword))) > 0
       OR strpos(lower(COALESCE(u.email, '')), lower(sqlc.narg(keyword))) > 0)
  AND (sqlc.narg(status)::text IS NULL OR u.status = sqlc.narg(status))
  AND (sqlc.narg(plan_id)::bigint IS NULL OR u.plan_id = sqlc.narg(plan_id));

-- name: AgentLockUser :one
-- 代理侧按 id 的写操作唯一的归属入口：不在名下或待删除一律查不到（404）。行结构须与 user.sql 的 GetUserQuota 逐字段相同
SELECT id, plan_id, traffic_limit, traffic_used, expires_at, pending_days, limit_ip, renew_every_days, renew_month_day,
       renew_weekday, renew_max, renew_count, traffic_reset, traffic_reset_day, last_traffic_reset_at, is_disabled, status, username
FROM end_user
WHERE id = sqlc.arg(id) AND agent_id = sqlc.arg(agent_id) AND deleted_at IS NULL
FOR UPDATE;

-- ---- 代理侧：邀请码 ----

-- name: AgentListInviteCodes :many
SELECT c.id, c.code, c.max_uses, c.used_count, c.expires_at, c.is_disabled, c.remark, c.created_at, a.username AS agent_username
FROM invite_code c
JOIN admin a ON a.id = c.agent_id
WHERE c.agent_id = sqlc.arg(agent_id)
  AND (sqlc.narg(id)::bigint IS NULL OR c.id = sqlc.narg(id))
ORDER BY c.id DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: AgentCountInviteCodes :one
SELECT COUNT(*) FROM invite_code WHERE agent_id = $1;

-- name: AgentCreateInviteCode :one
INSERT INTO invite_code (code, max_uses, expires_at, is_disabled, remark, created_by, agent_id)
VALUES (sqlc.arg(code), sqlc.arg(max_uses), sqlc.narg(expires_at), sqlc.arg(is_disabled), sqlc.arg(remark), sqlc.arg(agent_id), sqlc.arg(agent_id))
RETURNING id;

-- name: AgentLockInviteCode :one
SELECT id FROM invite_code WHERE id = sqlc.arg(id) AND agent_id = sqlc.arg(agent_id) FOR UPDATE;

-- name: AgentUpdateInviteCode :execrows
UPDATE invite_code SET max_uses = sqlc.arg(max_uses), expires_at = sqlc.narg(expires_at), is_disabled = sqlc.arg(is_disabled), remark = sqlc.arg(remark)
WHERE id = sqlc.arg(id) AND agent_id = sqlc.arg(agent_id);

-- name: AgentDeleteInviteCode :execrows
DELETE FROM invite_code WHERE id = sqlc.arg(id) AND agent_id = sqlc.arg(agent_id);

-- ---- 代理侧：工单 ----

-- name: AgentListTickets :many
SELECT t.id, t.user_id, u.username, t.subject, t.status, t.last_reply_at, t.created_at
FROM ticket t
JOIN end_user u ON u.id = t.user_id AND u.agent_id = sqlc.arg(agent_id) AND u.deleted_at IS NULL
WHERE (sqlc.narg(id)::bigint IS NULL OR t.id = sqlc.narg(id))
  AND (sqlc.narg(user_id)::bigint IS NULL OR t.user_id = sqlc.narg(user_id))
  AND (sqlc.narg(status)::text IS NULL OR t.status = sqlc.narg(status))
ORDER BY t.last_reply_at DESC, t.id DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: AgentCountTickets :one
SELECT COUNT(*) FROM ticket t
JOIN end_user u ON u.id = t.user_id AND u.agent_id = sqlc.arg(agent_id) AND u.deleted_at IS NULL
WHERE (sqlc.narg(user_id)::bigint IS NULL OR t.user_id = sqlc.narg(user_id))
  AND (sqlc.narg(status)::text IS NULL OR t.status = sqlc.narg(status));

-- name: AgentLockTicket :one
SELECT t.id, t.user_id, t.subject, t.status
FROM ticket t
JOIN end_user u ON u.id = t.user_id AND u.agent_id = sqlc.arg(agent_id) AND u.deleted_at IS NULL
WHERE t.id = sqlc.arg(id)
FOR UPDATE OF t;

-- name: AgentListTicketMessages :many
-- 员工的回复不带账号名，只有代理本人的回复带他自己的用户名（docs/03 §5.23 不暴露员工）
SELECT m.id, m.author_kind,
       (CASE WHEN m.admin_id = sqlc.arg(agent_id)::bigint THEN COALESCE(a.username, '') ELSE '' END)::text AS admin_username,
       m.content, m.created_at
FROM ticket_message m
JOIN ticket t ON t.id = m.ticket_id
JOIN end_user u ON u.id = t.user_id AND u.agent_id = sqlc.arg(agent_id)::bigint AND u.deleted_at IS NULL
LEFT JOIN admin a ON a.id = m.admin_id
WHERE m.ticket_id = sqlc.arg(ticket_id)
ORDER BY m.id;

-- ---- 代理侧：订单与余额流水（只含现存名下用户，Ruling-102） ----

-- name: AgentListOrders :many
-- 行结构须与 shop.sql 的 ListOrders 逐字段相同
SELECT o.id, o.order_no, o.user_id, o.username, o.kind, o.item, o.is_trial, o.amount_original, o.amount_discount, o.amount_paid,
       o.coupon_code, o.created_at
FROM shop_order o
JOIN end_user u ON u.id = o.user_id AND u.agent_id = sqlc.arg(agent_id) AND u.deleted_at IS NULL
WHERE (sqlc.narg(user_id)::bigint IS NULL OR o.user_id = sqlc.narg(user_id))
  AND (sqlc.narg(kind)::text IS NULL OR o.kind = sqlc.narg(kind))
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR o.created_at >= sqlc.narg(from_time)::timestamptz)
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR o.created_at < sqlc.narg(to_time)::timestamptz)
ORDER BY o.id DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: AgentCountOrders :one
SELECT COUNT(*) FROM shop_order o
JOIN end_user u ON u.id = o.user_id AND u.agent_id = sqlc.arg(agent_id) AND u.deleted_at IS NULL
WHERE (sqlc.narg(user_id)::bigint IS NULL OR o.user_id = sqlc.narg(user_id))
  AND (sqlc.narg(kind)::text IS NULL OR o.kind = sqlc.narg(kind))
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR o.created_at >= sqlc.narg(from_time)::timestamptz)
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR o.created_at < sqlc.narg(to_time)::timestamptz);

-- name: AgentListLedger :many
-- 不带操作人（员工账号名不向代理暴露）
SELECT l.id, l.user_id, l.username, l.kind, l.amount, l.balance_after, l.remark, l.created_at
FROM wallet_ledger l
JOIN end_user u ON u.id = l.user_id AND u.agent_id = sqlc.arg(agent_id) AND u.deleted_at IS NULL
WHERE (sqlc.narg(user_id)::bigint IS NULL OR l.user_id = sqlc.narg(user_id))
  AND (sqlc.narg(kind)::text IS NULL OR l.kind = sqlc.narg(kind))
ORDER BY l.id DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: AgentCountLedger :one
SELECT COUNT(*) FROM wallet_ledger l
JOIN end_user u ON u.id = l.user_id AND u.agent_id = sqlc.arg(agent_id) AND u.deleted_at IS NULL
WHERE (sqlc.narg(user_id)::bigint IS NULL OR l.user_id = sqlc.narg(user_id))
  AND (sqlc.narg(kind)::text IS NULL OR l.kind = sqlc.narg(kind));
