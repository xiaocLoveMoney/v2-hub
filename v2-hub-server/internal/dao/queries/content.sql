-- 公告、教程、共享账号、工单与签到（docs/03 §5.9–§5.11）。

-- ---- 公告 ----

-- name: ListAnnouncements :many
-- 管理端：含草稿，新的在前
SELECT id, title, content, is_pinned, is_popup, published_at, mailed_at, created_at
FROM announcement
WHERE (sqlc.arg(id)::bigint = 0 OR id = sqlc.arg(id)::bigint)
ORDER BY id DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: CountAnnouncements :one
SELECT count(*) FROM announcement;

-- name: ListPublishedAnnouncements :many
-- 用户端：只有已发布的，置顶在前、新的在前；only_popup 为真时只取标了弹窗的
SELECT id, title, content, is_pinned, is_popup, published_at
FROM announcement
WHERE published_at IS NOT NULL AND (NOT sqlc.arg(only_popup)::boolean OR is_popup)
ORDER BY is_pinned DESC, published_at DESC, id DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: CountPublishedAnnouncements :one
SELECT count(*) FROM announcement WHERE published_at IS NOT NULL;

-- name: CreateAnnouncement :one
INSERT INTO announcement (title, content, is_pinned, is_popup, published_at, created_by, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: GetAnnouncementForUpdate :one
SELECT id, title, content, published_at, mailed_at FROM announcement WHERE id = $1 FOR UPDATE;

-- name: UpdateAnnouncement :exec
UPDATE announcement
SET title = $2, content = $3, is_pinned = $4, is_popup = $5, published_at = $6, updated_at = sqlc.arg(now)
WHERE id = $1;

-- name: DeleteAnnouncement :execrows
DELETE FROM announcement WHERE id = $1;

-- name: MarkAnnouncementMailed :exec
UPDATE announcement SET mailed_at = $2 WHERE id = $1;

-- name: ListMailableUserEmails :many
-- 群发对象：有已验证邮箱且未停用的用户（docs/03 §5.9 公告群发）
SELECT email::text AS email FROM end_user
WHERE email IS NOT NULL AND email_verified_at IS NOT NULL AND NOT is_disabled AND deleted_at IS NULL
ORDER BY id;

-- ---- 教程 ----

-- name: ListTutorials :many
-- only_published 为真时只取已发布的（用户端）
SELECT id, platform, client_name, summary, links, content, sort_order, is_published
FROM tutorial
WHERE (NOT sqlc.arg(only_published)::boolean OR is_published)
  AND (sqlc.arg(id)::bigint = 0 OR id = sqlc.arg(id)::bigint)
ORDER BY platform, sort_order, id;

-- name: CreateTutorial :one
INSERT INTO tutorial (platform, client_name, summary, links, content, sort_order, is_published, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: UpdateTutorial :execrows
UPDATE tutorial
SET platform = $2, client_name = $3, summary = $4, links = $5, content = $6, sort_order = $7, is_published = $8, updated_at = sqlc.arg(now)
WHERE id = $1;

-- name: DeleteTutorial :execrows
DELETE FROM tutorial WHERE id = $1;

-- ---- 共享账号 ----

-- name: ListSharedAccounts :many
-- 管理端：全部条目与各自限定的套餐，不含密码
SELECT a.id, a.name, a.category, a.account, a.remark, a.sort_order, a.is_published,
       COALESCE((SELECT array_agg(p.plan_id ORDER BY p.plan_id) FROM shared_account_plan p WHERE p.shared_account_id = a.id), '{}')::bigint[] AS plan_ids
FROM shared_account a
WHERE (sqlc.arg(id)::bigint = 0 OR a.id = sqlc.arg(id)::bigint)
ORDER BY a.category, a.sort_order, a.id;

-- name: ListVisibleSharedAccounts :many
-- 用户端：已发布，且没有限定套餐或限定里包含该用户的套餐
SELECT a.id, a.name, a.category, a.account, a.remark, a.sort_order
FROM shared_account a
WHERE a.is_published
  AND (NOT EXISTS (SELECT 1 FROM shared_account_plan p WHERE p.shared_account_id = a.id)
       OR EXISTS (SELECT 1 FROM shared_account_plan p WHERE p.shared_account_id = a.id AND p.plan_id = sqlc.arg(plan_id)::bigint))
ORDER BY a.category, a.sort_order, a.id;

-- name: GetVisibleSharedAccountCredential :one
-- 与 ListVisibleSharedAccounts 同样的可见性条件，防止凭 id 读到不可见的条目
SELECT a.credential_enc
FROM shared_account a
WHERE a.id = sqlc.arg(id) AND a.is_published
  AND (NOT EXISTS (SELECT 1 FROM shared_account_plan p WHERE p.shared_account_id = a.id)
       OR EXISTS (SELECT 1 FROM shared_account_plan p WHERE p.shared_account_id = a.id AND p.plan_id = sqlc.arg(plan_id)::bigint));

-- name: GetSharedAccountCredential :one
SELECT credential_enc FROM shared_account WHERE id = $1;

-- name: CreateSharedAccount :one
INSERT INTO shared_account (name, category, account, remark, sort_order, is_published, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: SetSharedAccountCredential :exec
UPDATE shared_account SET credential_enc = $2 WHERE id = $1;

-- name: UpdateSharedAccount :execrows
UPDATE shared_account
SET name = $2, category = $3, account = $4, remark = $5, sort_order = $6, is_published = $7, updated_at = sqlc.arg(now)
WHERE id = $1;

-- name: DeleteSharedAccount :execrows
DELETE FROM shared_account WHERE id = $1;

-- name: ClearSharedAccountPlans :exec
DELETE FROM shared_account_plan WHERE shared_account_id = $1;

-- name: InsertSharedAccountPlans :exec
INSERT INTO shared_account_plan (shared_account_id, plan_id)
SELECT sqlc.arg(shared_account_id)::bigint, unnest(sqlc.arg(plan_ids)::bigint[]);

-- name: CountPlansByIDs :one
SELECT count(*) FROM plan WHERE id = ANY (sqlc.arg(ids)::bigint[]);

-- name: GetUserPlanStatus :one
SELECT plan_id, status FROM end_user WHERE id = $1 AND deleted_at IS NULL;

-- ---- 工单 ----

-- name: CreateTicket :one
INSERT INTO ticket (user_id, subject, status, last_reply_at, created_at, updated_at)
VALUES ($1, $2, 'open', sqlc.arg(now), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: InsertTicketMessage :exec
INSERT INTO ticket_message (ticket_id, author_kind, admin_id, content, created_at) VALUES ($1, $2, $3, $4, $5);

-- name: DeleteUnclosedTicketsByUser :exec
-- 删除用户时当场删掉未关闭的工单（docs/03 §5.2 删除用户）；已关闭的随用户记录级联删除
DELETE FROM ticket WHERE user_id = $1 AND status <> 'closed';

-- name: CountOpenTicketsByUser :one
SELECT count(*) FROM ticket WHERE user_id = $1 AND status <> 'closed';

-- name: CountTicketsCreatedSince :one
SELECT count(*) FROM ticket WHERE user_id = $1 AND created_at >= $2;

-- name: CountUserTicketMessagesSince :one
SELECT count(*) FROM ticket_message m JOIN ticket t ON t.id = m.ticket_id
WHERE t.user_id = $1 AND m.author_kind = 'user' AND m.created_at >= $2;

-- name: GetTicketForUpdate :one
SELECT id, user_id, subject, status FROM ticket WHERE id = $1 FOR UPDATE;

-- name: SetTicketStatus :exec
-- touch_reply 为真时把最近回复时间推到 now（有新消息）；关闭时记关闭时间，重新打开时清掉
UPDATE ticket
SET status = sqlc.arg(status)::text,
    last_reply_at = CASE WHEN sqlc.arg(touch_reply)::boolean THEN sqlc.arg(now)::timestamptz ELSE last_reply_at END,
    closed_at = CASE WHEN sqlc.arg(status)::text = 'closed' THEN sqlc.arg(now)::timestamptz ELSE NULL END,
    updated_at = sqlc.arg(now)::timestamptz
WHERE id = sqlc.arg(id);

-- name: ListTickets :many
SELECT t.id, t.user_id, u.username, t.subject, t.status, t.last_reply_at, t.created_at
FROM ticket t
JOIN end_user u ON u.id = t.user_id
WHERE (sqlc.arg(id)::bigint = 0 OR t.id = sqlc.arg(id)::bigint)
  AND (sqlc.arg(user_id)::bigint = 0 OR t.user_id = sqlc.arg(user_id)::bigint)
  AND (sqlc.arg(status)::text = '' OR t.status = sqlc.arg(status)::text)
ORDER BY t.last_reply_at DESC, t.id DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: CountTickets :one
SELECT count(*) FROM ticket t
WHERE (sqlc.arg(user_id)::bigint = 0 OR t.user_id = sqlc.arg(user_id)::bigint)
  AND (sqlc.arg(status)::text = '' OR t.status = sqlc.arg(status)::text);

-- name: ListTicketMessages :many
SELECT m.id, m.author_kind, COALESCE(a.username, '')::text AS admin_username, m.content, m.created_at
FROM ticket_message m
LEFT JOIN admin a ON a.id = m.admin_id
WHERE m.ticket_id = $1
ORDER BY m.id;

-- name: GetUserNotifyEmail :one
-- 只给已验证邮箱且未停用的用户发提醒；没有时返回空串
SELECT COALESCE((SELECT email FROM end_user WHERE id = $1 AND email_verified_at IS NOT NULL AND NOT is_disabled AND deleted_at IS NULL), '')::text AS email;

-- ---- 签到 ----

-- name: InsertCheckin :execrows
-- 唯一约束 (user_id, checkin_date) 保证每人每天一次（Ruling-26）；并发提交只有一条插入成功
INSERT INTO checkin_log (user_id, checkin_date, reward_bytes, created_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, checkin_date) DO NOTHING;

-- name: ExistsCheckin :one
SELECT EXISTS (SELECT 1 FROM checkin_log WHERE user_id = $1 AND checkin_date = $2);

-- name: ListCheckins :many
SELECT checkin_date, reward_bytes FROM checkin_log WHERE user_id = $1 ORDER BY checkin_date DESC LIMIT $2;

-- name: GetUserCheckinState :one
-- 判断能否签到要用的字段；状态由调用方按当前时间现算，不取库里可能滞后的 status 列
SELECT plan_id, traffic_limit, traffic_used, expires_at, is_disabled FROM end_user WHERE id = $1 AND deleted_at IS NULL;
