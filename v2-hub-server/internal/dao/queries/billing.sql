-- 自动续费、提醒与返利（docs/03 §5.16）------------------------------------------------

-- name: ListAutoRenewDueIDs :many
-- 到期且开了扣余额续费的用户；一轮最多处理一批，剩下的下一轮再来
SELECT id FROM end_user
WHERE auto_renew AND deleted_at IS NULL AND NOT is_disabled AND expires_at IS NOT NULL AND expires_at <= sqlc.arg(now)::timestamptz
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- name: GetUserNotifyTarget :one
-- 提醒邮件只发给有已验证邮箱且未停用的用户
SELECT username, COALESCE(email, '')::text AS email, (email_verified_at IS NOT NULL)::boolean AS is_email_verified, is_disabled
FROM end_user WHERE id = $1 AND deleted_at IS NULL;

-- name: GetUserInviter :one
-- 被邀请人的用户名与邀请人；邀请人已删除时没有行
SELECT u.username, i.id AS inviter_id, i.username AS inviter_username, i.is_disabled AS is_inviter_disabled
FROM end_user u
JOIN end_user i ON i.id = u.invited_by AND i.deleted_at IS NULL
WHERE u.id = $1;

-- name: CountOtherPaidOrders :one
-- 这个用户除本单以外实付 > 0 的订单数，用来判断是不是“首笔消费”
SELECT count(*) FROM shop_order WHERE user_id = $1 AND amount_paid > 0 AND id <> $2;

-- name: InsertRebate :one
INSERT INTO rebate (inviter_id, inviter_username, invitee_id, invitee_username, order_id, order_amount, percent, amount, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id;

-- name: ListExpiringUsers :many
-- 临近到期、还没为这个到期时间提醒过、没有设免费续期的用户；price 是自动续费对象的现价（没有则为 NULL）
SELECT u.id, u.username, u.email, u.expires_at, u.auto_renew, u.balance, COALESCE(p.name, '')::text AS plan_name, pp.price
FROM end_user u
LEFT JOIN plan p ON p.id = u.plan_id
LEFT JOIN plan_price pp ON pp.id = u.plan_price_id
WHERE u.deleted_at IS NULL AND NOT u.is_disabled AND u.email IS NOT NULL AND u.email_verified_at IS NOT NULL
  AND u.expires_at > sqlc.arg(now)::timestamptz AND u.expires_at <= sqlc.arg(until)::timestamptz
  AND (u.expire_notified_for IS NULL OR u.expire_notified_for <> u.expires_at)
  AND u.renew_every_days = 0 AND u.renew_month_day = 0 AND u.renew_weekday = 0
ORDER BY u.id
LIMIT sqlc.arg(row_limit);

-- name: SetExpireNotified :exec
UPDATE end_user SET expire_notified_for = $2 WHERE id = $1;

-- name: ResetTrafficNotified :exec
-- 剩余流量回到阈值以上（清零、续费、加了流量包）的用户，下次再低于阈值时可以再提醒一次
UPDATE end_user SET traffic_notified = FALSE
WHERE traffic_notified AND (traffic_limit = 0 OR (traffic_limit - traffic_used) * 100 >= traffic_limit * sqlc.arg(percent)::bigint);

-- name: ListLowTrafficUsers :many
SELECT u.id, u.username, u.email, u.traffic_limit, u.traffic_used
FROM end_user u
WHERE u.deleted_at IS NULL AND NOT u.is_disabled AND u.email IS NOT NULL AND u.email_verified_at IS NOT NULL
  AND u.status = 'active' AND NOT u.traffic_notified AND u.traffic_limit > 0
  AND (u.traffic_limit - u.traffic_used) * 100 < u.traffic_limit * sqlc.arg(percent)::bigint
ORDER BY u.id
LIMIT sqlc.arg(row_limit);

-- name: SetTrafficNotified :exec
UPDATE end_user SET traffic_notified = TRUE WHERE id = $1;

-- name: ListRebatesByInviter :many
-- 本人最近的返利记录（docs/03 §5.16 返利去向）；被邀请人的用户名由调用方打码
SELECT invitee_username, amount, created_at FROM rebate WHERE inviter_id = $1 ORDER BY id DESC LIMIT $2;
