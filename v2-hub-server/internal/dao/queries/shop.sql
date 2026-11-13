-- 商店：流量包、优惠码、下单与订单（docs/03 §5.12、§5.13、§5.15）-----------------------------

-- name: ListTrafficPacks :many
SELECT id, name, traffic_bytes, price, is_on_sale, sort_order, created_at
FROM traffic_pack
WHERE (NOT sqlc.arg(only_on_sale)::boolean OR is_on_sale)
ORDER BY sort_order, id;

-- name: GetTrafficPack :one
SELECT id, name, traffic_bytes, price, is_on_sale, sort_order, created_at FROM traffic_pack WHERE id = $1;

-- name: CreateTrafficPack :one
INSERT INTO traffic_pack (name, traffic_bytes, price, is_on_sale, sort_order) VALUES ($1, $2, $3, $4, $5) RETURNING id;

-- name: UpdateTrafficPack :execrows
UPDATE traffic_pack SET name = $2, traffic_bytes = $3, price = $4, is_on_sale = $5, sort_order = $6, updated_at = now() WHERE id = $1;

-- name: DeleteTrafficPack :execrows
DELETE FROM traffic_pack WHERE id = $1;

-- name: ListCoupons :many
SELECT c.id, c.code, c.kind, c.value, c.all_plans, c.for_traffic_pack, c.min_amount, c.max_uses, c.max_uses_per_user, c.used_count,
       c.starts_at, c.ends_at, c.is_disabled, c.remark, c.created_at,
       COALESCE((SELECT array_agg(cp.plan_id ORDER BY cp.plan_id) FROM coupon_plan cp WHERE cp.coupon_id = c.id), '{}')::bigint[] AS plan_ids
FROM coupon c
WHERE (sqlc.arg(id)::bigint = 0 OR c.id = sqlc.arg(id)::bigint)
ORDER BY c.id DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: CountCoupons :one
SELECT count(*) FROM coupon;

-- name: CreateCoupon :one
INSERT INTO coupon (code, kind, value, all_plans, for_traffic_pack, min_amount, max_uses, max_uses_per_user, starts_at, ends_at, is_disabled, remark)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING id;

-- name: UpdateCoupon :execrows
UPDATE coupon SET code = $2, kind = $3, value = $4, all_plans = $5, for_traffic_pack = $6, min_amount = $7, max_uses = $8,
    max_uses_per_user = $9, starts_at = $10, ends_at = $11, is_disabled = $12, remark = $13, updated_at = now()
WHERE id = $1;

-- name: DeleteUnusedCoupon :execrows
-- 已被使用过的不可删除，只能停用（docs/03 §5.15）
DELETE FROM coupon WHERE id = $1 AND used_count = 0;

-- name: CouponExists :one
SELECT EXISTS (SELECT 1 FROM coupon WHERE id = $1);

-- name: ClearCouponPlans :exec
DELETE FROM coupon_plan WHERE coupon_id = $1;

-- name: InsertCouponPlans :exec
INSERT INTO coupon_plan (coupon_id, plan_id)
SELECT sqlc.arg(coupon_id)::bigint, unnest(sqlc.arg(plan_ids)::bigint[]);

-- name: GetCouponByCodeForUpdate :one
-- 下单时锁住优惠码行，次数判断与占用在同一事务内完成（并发下单不超发）
SELECT id, code, kind, value, all_plans, for_traffic_pack, min_amount, max_uses, max_uses_per_user, used_count, starts_at, ends_at, is_disabled
FROM coupon WHERE code = $1 FOR UPDATE;

-- name: CouponCoversPlan :one
SELECT EXISTS (SELECT 1 FROM coupon_plan WHERE coupon_id = $1 AND plan_id = $2);

-- name: CountCouponUsesByUser :one
SELECT count(*) FROM coupon_usage WHERE coupon_id = $1 AND user_id = $2;

-- name: InsertCouponUsage :exec
INSERT INTO coupon_usage (coupon_id, user_id, order_id, created_at) VALUES ($1, $2, $3, $4);

-- name: BumpCouponUse :exec
UPDATE coupon SET used_count = used_count + 1 WHERE id = $1;

-- name: GetPlanPriceForSale :one
-- 一个时长选项连同它的套餐；是否上架由调用方判断（自动续费也用这条）
SELECT pp.id, pp.plan_id, pp.duration_days, pp.price, p.name, p.traffic_limit, p.start_on_first_use, p.limit_ip,
       p.traffic_reset, p.traffic_reset_day, p.is_on_sale, p.is_trial
FROM plan_price pp
JOIN plan p ON p.id = pp.plan_id
WHERE pp.id = $1;

-- name: ListShopPlans :many
-- 商店里的套餐：已上架且至少有一个时长选项；node_count 是该套餐当前可输出的节点数（条件同 ListUserSubNodes）
SELECT p.id, p.name, p.description, p.traffic_limit, p.traffic_reset, p.limit_ip, p.is_trial,
       (SELECT count(DISTINCT n.id)
          FROM plan_node_group pg
          JOIN node_group_item gi ON gi.node_group_id = pg.node_group_id
          JOIN node n ON n.id = gi.node_id AND n.is_enabled
          JOIN inbound i ON i.id = n.inbound_id AND NOT i.is_missing AND i.is_enabled
          JOIN server s ON s.id = i.server_id AND NOT s.is_disabled
          LEFT JOIN endpoint e ON e.id = n.endpoint_id
         WHERE pg.plan_id = p.id AND (e.id IS NULL OR (NOT e.is_missing AND NOT e.is_disabled)))::int AS node_count
FROM plan p
WHERE p.is_on_sale AND EXISTS (SELECT 1 FROM plan_price pp WHERE pp.plan_id = p.id)
ORDER BY p.sort_order, p.id;

-- name: ListPlanPricesOnSale :many
SELECT pp.id, pp.plan_id, pp.duration_days, pp.price
FROM plan_price pp
JOIN plan p ON p.id = pp.plan_id AND p.is_on_sale
ORDER BY pp.plan_id, pp.duration_days;

-- name: GetUserShopState :one
-- 用户与购买有关的状态；不加锁，只用于展示
SELECT u.plan_id, u.plan_price_id, u.auto_renew, u.balance, u.traffic_limit, u.traffic_used, u.expires_at, u.pending_days, u.is_disabled,
       EXISTS (SELECT 1 FROM shop_order o WHERE o.user_id = u.id AND o.is_trial)::boolean AS is_trial_used
FROM end_user u WHERE u.id = $1 AND u.deleted_at IS NULL;

-- name: HasTrialOrder :one
SELECT EXISTS (SELECT 1 FROM shop_order WHERE user_id = $1 AND is_trial);

-- name: SetUserPurchase :exec
-- 记下最近一次购买的时长选项与自动续费开关；提醒去重标记随新一轮额度清掉
UPDATE end_user SET plan_price_id = $2, auto_renew = $3, expire_notified_for = NULL, traffic_notified = FALSE, updated_at = now() WHERE id = $1;

-- name: SetUserAutoRenew :exec
UPDATE end_user SET auto_renew = $2, updated_at = now() WHERE id = $1;

-- name: ClearUserTrafficNotified :exec
UPDATE end_user SET traffic_notified = FALSE, updated_at = now() WHERE id = $1;

-- name: InsertOrder :one
INSERT INTO shop_order (order_no, user_id, username, kind, plan_id, traffic_pack_id, item, is_trial, amount_original, amount_discount, amount_paid,
                        coupon_id, coupon_code, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
RETURNING id;

-- name: ListOrders :many
SELECT o.id, o.order_no, o.user_id, o.username, o.kind, o.item, o.is_trial, o.amount_original, o.amount_discount, o.amount_paid,
       o.coupon_code, o.created_at
FROM shop_order o
WHERE (sqlc.arg(user_id)::bigint = 0 OR o.user_id = sqlc.arg(user_id)::bigint)
  AND (sqlc.arg(kind)::text = '' OR o.kind = sqlc.arg(kind)::text)
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR o.created_at >= sqlc.narg(from_time)::timestamptz)
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR o.created_at < sqlc.narg(to_time)::timestamptz)
ORDER BY o.id DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: CountOrders :one
SELECT count(*) FROM shop_order o
WHERE (sqlc.arg(user_id)::bigint = 0 OR o.user_id = sqlc.arg(user_id)::bigint)
  AND (sqlc.arg(kind)::text = '' OR o.kind = sqlc.arg(kind)::text)
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR o.created_at >= sqlc.narg(from_time)::timestamptz)
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR o.created_at < sqlc.narg(to_time)::timestamptz);

-- name: SumRebateByInviter :one
SELECT COALESCE(sum(amount), 0)::bigint FROM rebate WHERE inviter_id = $1;
