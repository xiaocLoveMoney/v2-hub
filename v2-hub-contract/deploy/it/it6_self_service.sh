#!/usr/bin/env bash
# IT-6 自助运营（docs/08，M4 DoD）：系统设置 → 管理员邀请码 → 邀请注册（送套餐）→ 个人邀请码再邀请一人
# → 签到（每日一次）→ 提工单 → 管理员回复 → 用户再回复 / 关闭 → 关闭工单与注册开关后相应接口 403。
# 黑盒，只用 curl / jq；不依赖面板（套餐不绑节点组）。IT 环境不配 SMTP，所以邮件类接口应为 403 feature_disabled、注册不要邮箱验证码。
source "$(dirname "$0")/common.sh"
set -e

BASE=http://127.0.0.1:8080
ROOT_USER=it6-root  # 与前面用例的超管区分（run_all 只在开头重置一次库）
FIRST_USER=it6-alice
SECOND_USER=it6-bob
THIRD_USER=it6-carol
MAIL_DOMAIN=it6.example.com
CRED="it-$(openssl rand -hex 12)"
USER_CRED="it-$(openssl rand -hex 12)"
GIB=1073741824
MIB=1048576
CHECKIN_MB=100  # 上下限相同，奖励值可断言
ADMIN_CODE_MAX_USES=1
SITE_NAME="IT6 站点"
WORK="$(mktemp -d /tmp/it6.XXXXXX)"
trap 'rm -rf "$WORK"; stop_all' EXIT

fail() { echo "IT-6: $*"; exit 1; }

# ---- 1. hub-server 与超管 ----
printf '%s\n' "$CRED" | (cd "$REPO_ROOT/v2-hub-server" && make -s admin-create USERNAME="$ROOT_USER") >/tmp/it-admin-create.log 2>&1 || fail "admin-create 失败"
start_module v2-hub-server 8080
HUB_JAR="$WORK/hub.jar"
# login_body_of <用户名> <口令>
login_body_of() { jq -nc --arg u "$1" --arg p "$2" '{username:$u,password:$p}'; }
# register_body_of <用户名> [邀请码]：邮箱取 <用户名>@$MAIL_DOMAIN，口令统一用 $USER_CRED
register_body_of() {
  login_body_of "$1" "$USER_CRED" | jq -c --arg e "$1@$MAIL_DOMAIN" --arg c "${2:-}" '. + {email:$e} + (if $c == "" then {} else {inviteCode:$c} end)'
}
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
CSRF="$(call "$HUB_JAR" "" POST /api/admin/auth/login "$(login_body_of "$ROOT_USER" "$CRED")" | jq -r '.data.csrfToken // empty')"
[ -n "$CSRF" ] || fail "hub 登录失败"
hub() { call "$HUB_JAR" "$CSRF" "$@"; }

# ---- 2. 默认设置与站点信息（docs/03 §5.6、§5.7）：默认仅邀请注册；没配 SMTP 时邮件类接口 403 ----
ANON_JAR="$WORK/anon.jar"
site="$(call "$ANON_JAR" "" GET /api/user/site)"; expect_code 200 "站点信息"
[ "$(printf '%s' "$site" | jq -r '[.data.registerMode, .data.mailEnabled, .data.emailVerifyRequired, .data.checkinEnabled] | map(tostring) | join(",")')" = "invite,false,false,false" ] \
  || fail "默认站点信息不符：$site"
call "$ANON_JAR" "" POST /api/user/auth/email-code "$(jq -nc --arg e "$FIRST_USER@$MAIL_DOMAIN" '{email:$e,purpose:"register"}')" >/dev/null
expect_error 403 feature_disabled "未配置邮件时发验证码"

# ---- 3. 套餐与系统设置：整体校验（一项不合法则全部不保存），成功后站点信息随之变化 ----
plan="$(hub POST /api/admin/plans "$(jq -nc --argjson l "$GIB" '{name:"it6-plan",trafficLimit:$l,durationDays:30,nodeGroupIds:[]}')")"; expect_code 200 "新建套餐"
PLAN_ID="$(printf '%s' "$plan" | jq -r .data.id)"
hub PUT /api/admin/settings "$(jq -nc --arg n "$SITE_NAME" '{site:{name:$n},checkin:{minMb:0}}')" >/dev/null; expect_code 400 "含非法项的设置"
[ "$(hub GET /api/admin/settings | jq -r .data.site.name)" != "$SITE_NAME" ] || fail "设置有一项不合法时不应保存其余项"
hub PUT /api/admin/settings "$(jq -nc --arg n "$SITE_NAME" --argjson p "$PLAN_ID" --argjson mb "$CHECKIN_MB" \
  '{site:{name:$n},register:{planId:$p},checkin:{enabled:true,minMb:$mb,maxMb:$mb}}')" >/dev/null
expect_code 200 "修改设置"
site="$(call "$ANON_JAR" "" GET /api/user/site)"
[ "$(printf '%s' "$site" | jq -r '.data.siteName + "," + (.data.checkinEnabled | tostring)')" = "$SITE_NAME,true" ] || fail "站点信息应反映新设置：$site"

# ---- 4. 管理员邀请码 → 邀请注册（docs/03 §5.8）----
code="$(hub POST /api/admin/invite-codes "$(jq -nc --argjson m "$ADMIN_CODE_MAX_USES" '{maxUses:$m,isDisabled:false,remark:"it6"}')")"; expect_code 200 "新建邀请码"
ADMIN_CODE="$(printf '%s' "$code" | jq -r .data.code)"
ADMIN_CODE_ID="$(printf '%s' "$code" | jq -r .data.id)"
FIRST_JAR="$WORK/first.jar"
call "$FIRST_JAR" "" POST /api/user/auth/register "$(register_body_of "$FIRST_USER")" >/dev/null; expect_code 400 "不带邀请码注册"
call "$FIRST_JAR" "" POST /api/user/auth/register "$(register_body_of "$FIRST_USER" "ZZZZZZZZZZ")" >/dev/null; expect_code 400 "邀请码不存在"
call "$FIRST_JAR" "" POST /api/user/auth/register "$(register_body_of "bad@name" "$ADMIN_CODE")" >/dev/null; expect_code 400 "用户名含 @"
session="$(call "$FIRST_JAR" "" POST /api/user/auth/register "$(register_body_of "$FIRST_USER" "$ADMIN_CODE")")"; expect_code 200 "邀请注册"
FIRST_CSRF="$(printf '%s' "$session" | jq -r '.data.csrfToken // empty')"
FIRST_ID="$(printf '%s' "$session" | jq -r .data.user.id)"
[ -n "$FIRST_CSRF" ] || fail "注册成功后应直接登录：$session"
[ "$(printf '%s' "$session" | jq -r '.data.user.email + "," + (.data.user.isEmailVerified | tostring)')" = "$FIRST_USER@$MAIL_DOMAIN,false" ] \
  || fail "未验证邮箱注册后应记下邮箱且未验证：$session"
first() { call "$FIRST_JAR" "$FIRST_CSRF" "$@"; }
[ "$(first GET /api/user/dashboard | jq -r .data.trafficLimit)" = "$GIB" ] || fail "注册应送设置里的套餐：$(cat "$WORK/body")"
# 管理员码只能用一次；失败的注册不占次数
SECOND_JAR="$WORK/second.jar"
call "$SECOND_JAR" "" POST /api/user/auth/register "$(register_body_of "$SECOND_USER" "$ADMIN_CODE")" >/dev/null; expect_code 400 "邀请码次数用尽"
[ "$(hub GET "/api/admin/invite-codes?kind=admin" | jq --argjson id "$ADMIN_CODE_ID" '.data.items[] | select(.id == $id) | .usedCount')" = "$ADMIN_CODE_MAX_USES" ] \
  || fail "管理员邀请码已用次数应为 $ADMIN_CODE_MAX_USES：$(cat "$WORK/body")"
# 邮箱登录：先按用户名、再按邮箱
call "$WORK/mail-login.jar" "" POST /api/user/auth/login "$(login_body_of "$FIRST_USER@$MAIL_DOMAIN" "$USER_CRED")" >/dev/null; expect_code 200 "用邮箱登录"

# ---- 5. 个人邀请码：第一人邀请第二人，重复的用户名 409 且不占次数 ----
invite="$(first GET /api/user/invite)"; expect_code 200 "取个人邀请码"
USER_CODE="$(printf '%s' "$invite" | jq -r .data.code)"
[ -n "$USER_CODE" ] && [ "$(printf '%s' "$invite" | jq -r .data.usedCount)" = "0" ] || fail "个人邀请码应已生成且未使用：$invite"
call "$SECOND_JAR" "" POST /api/user/auth/register \
  "$(register_body_of "$FIRST_USER" "$USER_CODE" | jq -c --arg e "$SECOND_USER@$MAIL_DOMAIN" '. + {email:$e}')" >/dev/null
expect_code 409 "用户名已占用"
session="$(call "$SECOND_JAR" "" POST /api/user/auth/register "$(register_body_of "$SECOND_USER" "$USER_CODE")")"; expect_code 200 "个人邀请码注册"
SECOND_CSRF="$(printf '%s' "$session" | jq -r '.data.csrfToken // empty')"
second() { call "$SECOND_JAR" "$SECOND_CSRF" "$@"; }
invite="$(first GET /api/user/invite)"
[ "$(printf '%s' "$invite" | jq -r '(.data.usedCount | tostring) + "," + (.data.invitees | length | tostring)')" = "1,1" ] || fail "邀请人应看到 1 次使用、1 个被邀请人：$invite"
[ "$(printf '%s' "$invite" | jq -r '.data.invitees[0].username')" != "$SECOND_USER" ] || fail "被邀请人用户名应打码：$invite"
[ "$(hub GET "/api/admin/users?keyword=$SECOND_USER" | jq -r '.data.items[0].invitedByUsername')" = "$FIRST_USER" ] || fail "管理端应显示邀请人：$(cat "$WORK/body")"
OLD_CODE="$USER_CODE"
USER_CODE="$(first POST /api/user/invite/reset | jq -r '.data.code // empty')"; expect_code 200 "重置个人邀请码"
[ -n "$USER_CODE" ] && [ "$USER_CODE" != "$OLD_CODE" ] || fail "重置后个人邀请码应变化"
call "$WORK/third.jar" "" POST /api/user/auth/register "$(register_body_of "$THIRD_USER" "$OLD_CODE")" >/dev/null; expect_code 400 "重置前的个人邀请码"

# ---- 6. 签到（docs/03 §5.11）：每天一次，奖励加到流量额度 ----
info="$(first GET /api/user/checkin)"; expect_code 200 "签到状态"
[ "$(printf '%s' "$info" | jq -r '[.data.enabled, .data.canCheckin, .data.checkedToday] | map(tostring) | join(",")')" = "true,true,false" ] || fail "签到前状态不符：$info"
call "$FIRST_JAR" "" POST /api/user/checkin >/dev/null; expect_code 403 "不带 CSRF 头签到"
reward="$(first POST /api/user/checkin)"; expect_code 200 "签到"
REWARD_BYTES=$((CHECKIN_MB * MIB))
[ "$(printf '%s' "$reward" | jq -r '(.data.rewardBytes | tostring) + "," + (.data.trafficLimit | tostring)')" = "$REWARD_BYTES,$((GIB + REWARD_BYTES))" ] \
  || fail "签到奖励应为 ${CHECKIN_MB}MB 并加到额度：$reward"
first POST /api/user/checkin >/dev/null; expect_error 409 conflict "同一天第二次签到"
info="$(first GET /api/user/checkin)"
[ "$(printf '%s' "$info" | jq -r '[.data.canCheckin, .data.checkedToday, (.data.logs | length)] | map(tostring) | join(",")')" = "false,true,1" ] || fail "签到后状态不符：$info"
[ "$(first GET /api/user/dashboard | jq -r .data.trafficLimit)" = "$((GIB + REWARD_BYTES))" ] || fail "仪表盘额度应含签到奖励：$(cat "$WORK/body")"

# ---- 7. 工单（docs/03 §5.10）：提交 → 管理员回复 → 用户回复 → 关闭 → 回复即重开；别人的工单 404 ----
first POST /api/user/tickets '{"subject":"","content":"x"}' >/dev/null; expect_code 400 "空主题工单"
ticket="$(first POST /api/user/tickets '{"subject":"IT-6 连不上","content":"第一条"}')"; expect_code 200 "新建工单"
TICKET_ID="$(printf '%s' "$ticket" | jq -r .data.id)"
[ "$(printf '%s' "$ticket" | jq -r '.data.status + "," + (.data.messages | length | tostring)')" = "open,1" ] || fail "新工单应为 open 且有 1 条消息：$ticket"
second GET "/api/user/tickets/$TICKET_ID" >/dev/null; expect_code 404 "查看别人的工单"
second POST "/api/user/tickets/$TICKET_ID/messages" '{"content":"越权"}' >/dev/null; expect_code 404 "回复别人的工单"
second POST "/api/user/tickets/$TICKET_ID/close" >/dev/null; expect_code 404 "关闭别人的工单"
[ "$(second GET /api/user/tickets | jq '.data.items | length')" = "0" ] || fail "第二人的工单列表应为空：$(cat "$WORK/body")"
[ "$(hub GET "/api/admin/tickets?status=open&userId=$FIRST_ID" | jq -r '.data.items[0] | (.id | tostring) + "," + .username')" = "$TICKET_ID,$FIRST_USER" ] \
  || fail "管理端应按状态与用户筛到该工单：$(cat "$WORK/body")"
reply="$(hub POST "/api/admin/tickets/$TICKET_ID/messages" '{"content":"请重置订阅后再试"}')"; expect_code 200 "管理员回复"
[ "$(printf '%s' "$reply" | jq -r '.data.status + "," + .data.messages[-1].authorName')" = "answered,$ROOT_USER" ] || fail "管理员回复后应为 answered 并带回复人：$reply"
detail="$(first GET "/api/user/tickets/$TICKET_ID")"; expect_code 200 "用户查看工单"
[ "$(printf '%s' "$detail" | jq -r '.data.status + "," + (.data.messages | length | tostring) + "," + .data.messages[-1].authorKind + "," + (.data.messages[-1].authorName // "")')" = "answered,2,admin," ] \
  || fail "用户应看到管理员回复且不带管理员用户名：$detail"
[ "$(first POST "/api/user/tickets/$TICKET_ID/messages" '{"content":"好了，谢谢"}' | jq -r .data.status)" = "open" ] || fail "用户回复后应回到 open：$(cat "$WORK/body")"
[ "$(hub POST "/api/admin/tickets/$TICKET_ID/close" | jq -r .data.status)" = "closed" ] || fail "管理员关闭后应为 closed：$(cat "$WORK/body")"
[ "$(first POST "/api/user/tickets/$TICKET_ID/messages" '{"content":"还有个问题"}' | jq -r .data.status)" = "open" ] || fail "回复已关闭的工单应重新打开：$(cat "$WORK/body")"
[ "$(first POST "/api/user/tickets/$TICKET_ID/close" | jq -r .data.status)" = "closed" ] || fail "用户关闭后应为 closed：$(cat "$WORK/body")"

# ---- 8. 关掉开关：工单不能新建与回复但仍可查看；注册关闭后 403 ----
hub PUT /api/admin/settings '{"ticket":{"enabled":false},"register":{"mode":"closed"}}' >/dev/null; expect_code 200 "关闭工单与注册"
first POST /api/user/tickets '{"subject":"关闭后","content":"x"}' >/dev/null; expect_error 403 feature_disabled "工单关闭后新建"
first POST "/api/user/tickets/$TICKET_ID/messages" '{"content":"x"}' >/dev/null; expect_error 403 feature_disabled "工单关闭后回复"
first GET "/api/user/tickets/$TICKET_ID" >/dev/null; expect_code 200 "工单关闭后仍可查看"
call "$WORK/third.jar" "" POST /api/user/auth/register "$(register_body_of "$THIRD_USER" "$USER_CODE")" >/dev/null; expect_error 403 feature_disabled "注册关闭后注册"
echo "IT-6 PASS"
