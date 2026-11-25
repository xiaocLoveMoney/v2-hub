#!/usr/bin/env bash
# IT-7 商店与钱包（docs/08，M5 DoD）：商店开关 → 充值码入账 → 余额不足不成单 → 购买 → 开自动续费
# → 到期自动续费（扣余额、顺延、发邮件）→ 再到期余额不足（关闭自动续费、标到期、发邮件）→ 充值后不补扣，手动重新购买。
# 黑盒，只用 curl / jq；不依赖面板（套餐不绑节点组）。SMTP 指向本机一个没有监听的端口：邮件功能视为已配置、
# 通知会进发件箱（投递必然失败），断言只看发件箱里有没有这封信。到期由管理员把到期时间改到过去来构造，
# 自动续费由用量任务驱动（默认每分钟一轮），所以两次到期各要等最多一轮。
source "$(dirname "$0")/common.sh"
set -e

BASE=http://127.0.0.1:8080
ROOT_USER=it7-root  # 与前面用例的超管区分（run_all 只在开头重置一次库）
BUYER=it7-buyer
MAIL_DOMAIN=it7.example.com
BUYER_MAIL="$BUYER@$MAIL_DOMAIN"
CRED="it-$(openssl rand -hex 12)"
USER_CRED="it-$(openssl rand -hex 12)"
SITE_NAME="IT7 站点"
SUBJECT_RENEW_OK="【${SITE_NAME}】已自动续费"
SUBJECT_RENEW_FAILED="【${SITE_NAME}】自动续费失败"
SMTP_HOST=127.0.0.1
SMTP_DEAD_PORT=1  # 没有服务监听，连接立即被拒
GIB=1073741824
PLAN_DAYS=30
PLAN_PRICE=2000   # 分
TOPUP_AMOUNT=5000 # 分；买一次 + 自动续费一次后剩 1000，不够再续
TOPUP_COUNT=2
SECONDS_PER_DAY=86400
EXPIRED_AGO_SECONDS=60
# 用量任务默认每分钟一轮，留出一轮半
RENEW_WAIT_SECONDS=90
POLL_SECONDS=2
OUTBOX_PAGE_SIZE=100
# jq 函数 epoch：把带时区偏移（或 Z）的 RFC 3339 时间换成 Unix 秒；jq 自带的 fromdate 只认 Z 结尾
JQ_EPOCH='def epoch: capture("^(?<d>[0-9-]+T[0-9:]+)(\\.[0-9]+)?(?<z>Z|[+-][0-9]{2}:[0-9]{2})$")
  | ((.d + "Z") | fromdate) - (if .z == "Z" then 0 else ((.z[0:1] + "1") | tonumber) * ((.z[1:3] | tonumber) * 3600 + (.z[4:6] | tonumber) * 60) end); '
WORK="$(mktemp -d /tmp/it7.XXXXXX)"
trap 'rm -rf "$WORK"; stop_all' EXIT

fail() { echo "IT-7: $*"; exit 1; }

# ---- 1. hub-server（带 SMTP 环境变量）与超管 ----
printf '%s\n' "$CRED" | (cd "$REPO_ROOT/v2-hub-server" && make -s admin-create USERNAME="$ROOT_USER") >/tmp/it-admin-create.log 2>&1 || fail "admin-create 失败"
it_hub_config "$WORK/hub-server.yaml" || fail "派生配置失败"
start_module v2-hub-server 8080 CONFIG="$WORK/hub-server.yaml" HUB_SMTP_HOST="$SMTP_HOST" HUB_SMTP_PORT="$SMTP_DEAD_PORT" HUB_SMTP_FROM="noreply@$MAIL_DOMAIN" HUB_SMTP_TLS=none
HUB_JAR="$WORK/hub.jar"
login_body_of() { jq -nc --arg u "$1" --arg p "$2" '{username:$u,password:$p}'; }
# call <cookie jar> <csrf> <method> <path> [json] → 打印响应体；状态码写入 $WORK/code
call() {
  local args=(-s -o "$WORK/body" -w '%{http_code}' -b "$1" -c "$1" -X "$3" -H 'Content-Type: application/json')
  [ -n "$2" ] && args+=(-H "X-CSRF-Token: $2")
  [ -n "${5:-}" ] && args+=(--data "$5")
  curl "${args[@]}" "$BASE$4" > "$WORK/code"
  cat "$WORK/body"
}
expect_code() { [ "$(cat "$WORK/code")" = "$1" ] || fail "$2：期望 HTTP $1，实际 $(cat "$WORK/code") $(cat "$WORK/body")"; }
# expect_error <状态码> <错误码> <说明>
expect_error() {
  expect_code "$1" "$3"
  [ "$(jq -r .code "$WORK/body")" = "$2" ] || fail "$3：期望错误码 $2，实际 $(cat "$WORK/body")"
}
# 超管登录：首次登录当场绑定两步验证（R10）；登录即视为刚做过二次验证，联调配置把有效期拉长（common.sh it_admin_login）
CSRF="$(it_admin_login "$HUB_JAR" "$ROOT_USER" "$CRED")" || fail "hub 登录失败"
hub() { call "$HUB_JAR" "$CSRF" "$@"; }

# ---- 2. 套餐与时长选项、用户（管理员建的用户，邮箱视为已验证）----
hub PUT /api/admin/settings "$(jq -nc --arg n "$SITE_NAME" '{site:{name:$n},shop:{enabled:false}}')" >/dev/null; expect_code 200 "设置站点名并关闭商店"
plan="$(hub POST /api/admin/plans "$(jq -nc --argjson l "$GIB" --argjson d "$PLAN_DAYS" --argjson p "$PLAN_PRICE" \
  '{name:"it7-plan",trafficLimit:$l,durationDays:$d,nodeGroupIds:[],isOnSale:true,prices:[{durationDays:$d,price:$p}]}')")"
expect_code 200 "新建在售套餐"
PRICE_ID="$(printf '%s' "$plan" | jq -r '.data.prices[0].id')"
[ "$PRICE_ID" != "null" ] && [ -n "$PRICE_ID" ] || fail "套餐应带回时长选项：$plan"
user="$(hub POST /api/admin/users "$(login_body_of "$BUYER" "$USER_CRED" | jq -c --arg e "$BUYER_MAIL" '. + {email:$e}')")"; expect_code 200 "新建用户"
BUYER_ID="$(printf '%s' "$user" | jq -r .data.id)"
[ "$(printf '%s' "$user" | jq -r '.data.isEmailVerified')" = "true" ] || fail "管理员填的邮箱应视为已验证：$user"
BUYER_JAR="$WORK/buyer.jar"
BUYER_CSRF="$(call "$BUYER_JAR" "" POST /api/user/auth/login "$(login_body_of "$BUYER" "$USER_CRED")" | jq -r '.data.csrfToken // empty')"
[ -n "$BUYER_CSRF" ] || fail "用户登录失败：$(cat "$WORK/body")"
buyer() { call "$BUYER_JAR" "$BUYER_CSRF" "$@"; }
order_body="$(jq -nc --argjson p "$PRICE_ID" '{kind:"plan",planPriceId:$p}')"
admin_user() { hub GET "/api/admin/users/$BUYER_ID"; }
renew_order_count() { hub GET "/api/admin/orders?userId=$BUYER_ID&kind=renew" | jq '.data.items | length'; }
# outbox_has <主题>：发件箱里有没有发给买家的这封信
outbox_has() {
  hub GET "/api/admin/mail-outbox?pageSize=$OUTBOX_PAGE_SIZE" | jq -e --arg r "$BUYER_MAIL" --arg s "$1" '[.data.items[] | select(.recipient == $r and .subject == $s)] | length > 0' >/dev/null
}

# ---- 3. 商店开关（docs/03 §5.12）：关闭时下单、兑换都 403；打开后站点信息随之变化 ----
buyer POST /api/user/orders "$order_body" >/dev/null; expect_error 403 feature_disabled "商店关闭时下单"
hub PUT /api/admin/settings '{"shop":{"enabled":true}}' >/dev/null; expect_code 200 "打开商店"
[ "$(call "$WORK/anon.jar" "" GET /api/user/site | jq -r .data.shopEnabled)" = "true" ] || fail "站点信息应显示商店已开：$(cat "$WORK/body")"
shop="$(buyer GET /api/user/shop)"; expect_code 200 "商店"
[ "$(printf '%s' "$shop" | jq -r --argjson p "$PRICE_ID" '[.data.balance, (.data.plans[] | select(.prices[0].id == $p) | .canBuy)] | map(tostring) | join(",")')" = "0,true" ] \
  || fail "商店应列出在售套餐且余额为 0：$shop"

# ---- 4. 余额不足不成单（Ruling-28）：409 insufficient_balance 并带差额，不留订单 ----
quote="$(buyer POST /api/user/shop/quote "$order_body")"; expect_code 200 "询价"
[ "$(printf '%s' "$quote" | jq -r '[.data.amountPayable, .data.shortfall] | map(tostring) | join(",")')" = "$PLAN_PRICE,$PLAN_PRICE" ] || fail "询价应给出应付与差额：$quote"
buyer POST /api/user/orders "$order_body" >/dev/null; expect_error 409 insufficient_balance "余额不足下单"
[ "$(jq -r .data.shortfall "$WORK/body")" = "$PLAN_PRICE" ] || fail "余额不足应带差额：$(cat "$WORK/body")"
[ "$(buyer GET /api/user/orders | jq '.data.items | length')" = "0" ] || fail "余额不足不应留下订单：$(cat "$WORK/body")"

# ---- 5. 充值码入账（docs/03 §5.14）：明文只在生成时返回；错码 400；一码一次 ----
batch="$(hub POST /api/admin/topup-codes "$(jq -nc --argjson a "$TOPUP_AMOUNT" --argjson c "$TOPUP_COUNT" '{amount:$a,count:$c,batch:"it7"}')")"; expect_code 200 "生成充值码"
FIRST_CODE="$(printf '%s' "$batch" | jq -r '.data.codes[0]')"
SECOND_CODE="$(printf '%s' "$batch" | jq -r '.data.codes[1]')"
[ "$(printf '%s' "$batch" | jq '.data.codes | length')" = "$TOPUP_COUNT" ] || fail "应生成 $TOPUP_COUNT 个充值码：$(printf '%s' "$batch" | jq -c 'del(.data.codes)')"
redeem_body_of() { jq -nc --arg c "$1" '{code:$c}'; }
call "$BUYER_JAR" "" POST /api/user/wallet/redeem "$(redeem_body_of "$FIRST_CODE")" >/dev/null; expect_code 403 "不带 CSRF 头兑换"
buyer POST /api/user/wallet/redeem "$(redeem_body_of "AAAAAAAAAAAAAAAA")" >/dev/null; expect_code 400 "不存在的充值码"
buyer POST /api/user/wallet/redeem "$(redeem_body_of "$FIRST_CODE")" >/dev/null; expect_code 200 "兑换充值码"
buyer POST /api/user/wallet/redeem "$(redeem_body_of "$FIRST_CODE")" >/dev/null; expect_code 400 "同一个充值码兑换第二次"
[ "$(buyer GET /api/user/wallet | jq -r .data.balance)" = "$TOPUP_AMOUNT" ] || fail "兑换后余额应为 ${TOPUP_AMOUNT}：$(cat "$WORK/body")"
[ "$(buyer GET /api/user/wallet/ledger | jq -r '.data.items[0] | .kind + "," + (.amount | tostring) + "," + (.balanceAfter | tostring)')" = "topup_code,$TOPUP_AMOUNT,$TOPUP_AMOUNT" ] \
  || fail "流水应记一笔充值码入账：$(cat "$WORK/body")"

# ---- 6. 购买（docs/03 §5.13）：扣余额、立即生效、记流水 ----
order="$(buyer POST /api/user/orders "$order_body")"; expect_code 200 "购买"
[ "$(printf '%s' "$order" | jq -r '.data.kind + "," + (.data.amountPaid | tostring)')" = "plan,$PLAN_PRICE" ] || fail "订单应为套餐、实付 ${PLAN_PRICE}：$order"
AFTER_BUY=$((TOPUP_AMOUNT - PLAN_PRICE))
dash="$(buyer GET /api/user/dashboard)"
[ "$(printf '%s' "$dash" | jq -r '[.data.status, .data.planName, .data.balance, .data.trafficLimit] | map(tostring) | join(",")')" = "active,it7-plan,$AFTER_BUY,$GIB" ] \
  || fail "购买后应生效并扣余额：$dash"
[ "$(buyer GET /api/user/wallet/ledger | jq -r '.data.items[0] | .kind + "," + (.amount | tostring)')" = "purchase,-$PLAN_PRICE" ] || fail "流水应记一笔购买：$(cat "$WORK/body")"

# ---- 7. 开自动续费（docs/03 §5.16）----
wallet="$(buyer PUT /api/user/wallet/auto-renew '{"enabled":true}')"; expect_code 200 "开自动续费"
[ "$(printf '%s' "$wallet" | jq -r '[.data.autoRenew.enabled, .data.autoRenew.durationDays, .data.autoRenew.price] | map(tostring) | join(",")')" = "true,$PLAN_DAYS,$PLAN_PRICE" ] \
  || fail "自动续费应开启并指向所买的时长选项：$wallet"

# expire_now：管理员把到期时间改到一分钟前，其余配额字段原样带回
expire_now() {
  local body
  body="$(admin_user | jq -c --argjson ago "$EXPIRED_AGO_SECONDS" \
    '.data | {trafficLimit, trafficUsed, expiresAt: (now - $ago | todate), pendingDays, limitIp, renewEveryDays, renewMonthDay, renewWeekday, renewMax, trafficReset, trafficResetDay}')"
  hub PUT "/api/admin/users/$BUYER_ID/quota" "$body" >/dev/null; expect_code 200 "把到期时间改到过去"
}
# wait_until <说明> <命令...>：轮询到命令成功，超过一轮用量任务的时间仍不成立即失败
wait_until() {
  local what="$1" waited=0; shift
  until "$@"; do
    [ "$waited" -lt "$RENEW_WAIT_SECONDS" ] || fail "${RENEW_WAIT_SECONDS}s 内没有等到：$what"
    sleep "$POLL_SECONDS"
    waited=$((waited + POLL_SECONDS))
  done
}

# ---- 8. 到期自动续费（U10）：扣余额、生成 renew 订单、从原到期日顺延、开关保持、发邮件 ----
expire_now
[ "$(admin_user | jq -r .data.status)" = "expired" ] || fail "到期时间改到过去后应为 expired：$(cat "$WORK/body")"
has_one_renew_order() { [ "$(renew_order_count)" = "1" ]; }
wait_until "自动续费订单" has_one_renew_order
AFTER_RENEW=$((AFTER_BUY - PLAN_PRICE))
info="$(admin_user)"
[ "$(printf '%s' "$info" | jq -r '[.data.status, .data.balance, .data.autoRenew] | map(tostring) | join(",")')" = "active,$AFTER_RENEW,true" ] \
  || fail "续费后应恢复 active、扣一次钱、开关保持：$info"
# 从原到期日（一分钟前）顺延 30 天：剩余应在 29–30 天之间
left="$(printf '%s' "$info" | jq -r "$JQ_EPOCH"'.data.expiresAt | epoch - now | floor')"
[ "$left" -gt $(((PLAN_DAYS - 1) * SECONDS_PER_DAY)) ] && [ "$left" -le $((PLAN_DAYS * SECONDS_PER_DAY)) ] || fail "续费应从原到期日顺延 $PLAN_DAYS 天，实际还剩 ${left}s"
[ "$(buyer GET /api/user/wallet/ledger | jq -r '.data.items[0] | .kind + "," + (.amount | tostring) + "," + (.balanceAfter | tostring)')" = "renew,-$PLAN_PRICE,$AFTER_RENEW" ] \
  || fail "流水应记一笔自动续费：$(cat "$WORK/body")"
outbox_has "$SUBJECT_RENEW_OK" || fail "发件箱应有续费成功通知：$(cat "$WORK/body")"

# ---- 9. 再到期而余额不足（Ruling-35）：不扣钱、不成单、关闭自动续费、标到期、发邮件 ----
[ "$AFTER_RENEW" -lt "$PLAN_PRICE" ] || fail "用例数值有误：续费一次后的余额应不够再续"
expire_now
auto_renew_is_off() { [ "$(admin_user | jq -r .data.autoRenew)" = "false" ]; }
wait_until "自动续费因余额不足关闭" auto_renew_is_off
[ "$(admin_user | jq -r '[.data.status, .data.balance] | map(tostring) | join(",")')" = "expired,$AFTER_RENEW" ] || fail "续费失败后应为 expired 且余额不变：$(cat "$WORK/body")"
[ "$(renew_order_count)" = "1" ] || fail "续费失败不应生成订单：$(cat "$WORK/body")"
outbox_has "$SUBJECT_RENEW_FAILED" || fail "发件箱应有续费失败通知：$(cat "$WORK/body")"
[ "$(buyer GET /api/user/wallet | jq -r .data.autoRenew.enabled)" = "false" ] || fail "用户端应看到自动续费已关闭：$(cat "$WORK/body")"

# ---- 10. 充值后不补扣，手动重新购买即恢复 ----
buyer POST /api/user/wallet/redeem "$(redeem_body_of "$SECOND_CODE")" >/dev/null; expect_code 200 "兑换第二个充值码"
AFTER_SECOND_TOPUP=$((AFTER_RENEW + TOPUP_AMOUNT))
[ "$(admin_user | jq -r '[.data.status, .data.balance, .data.autoRenew] | map(tostring) | join(",")')" = "expired,$AFTER_SECOND_TOPUP,false" ] \
  || fail "充值后不应补扣续费：$(cat "$WORK/body")"
buyer POST /api/user/orders "$order_body" >/dev/null; expect_code 200 "到期后重新购买"
[ "$(admin_user | jq -r '[.data.status, .data.balance] | map(tostring) | join(",")')" = "active,$((AFTER_SECOND_TOPUP - PLAN_PRICE))" ] || fail "重新购买后应恢复 active：$(cat "$WORK/body")"
[ "$(hub GET "/api/admin/orders?userId=$BUYER_ID" | jq '.data.items | length')" = "3" ] || fail "管理端应看到 3 笔订单（购买、自动续费、重新购买）：$(cat "$WORK/body")"
echo "IT-7 PASS"
