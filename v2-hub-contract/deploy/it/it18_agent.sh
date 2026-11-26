#!/usr/bin/env bash
# IT-18 二级代理（docs/08，docs/03 §5.23，Ruling-101 – Ruling-104、Ruling-108）：
# → 超管建套餐（有限 / 按周重置 / 不限流量 / 注册赠送）与代理 A、B：白名单含不限套餐 400；代理不进管理员列表、PUT /admins/{id} 409、不能设为所有者
# → 隔离：代理的会话 kind=agent、无权限码；员工接口一律 403；超管调代理接口 403；未登录 401
# → 配额：白名单与 poolCost（按时长内最多能用到的周期数，Ruling-108）；新建用户（不带套餐 / 白名单外 400）、分配与续期各扣一次；
#   同一套餐续期顺延、换套餐从当时起算；池不足 409 agent_pool_insufficient 带 poolCost / poolRemaining 且用户与池都不变；
#   达到用户数上限 409 agent_user_limit；调额只能收紧；改备注、停用 / 启用（两项缺一 400）、重置订阅；删除用户后池不退回
# → 邀请码与注册：代理码注册的用户归 A、无邀请人、不送注册套餐；名下用户的个人码拉来的也归 A；达到上限时两种码都不能注册
# → 用户自助下单不动池；订单、流水、工单列表只有名下的；工单的员工回复不带账号名；代理回复与关闭
# → 水平越权：B 名下、平台用户的 id 调每个按 id 的接口 404 且超管侧看不出任何变化；改 / 删 B 的邀请码 404
# → 超管侧：划转与收回、流量池低于已用量 409、改白名单、开通记录、用户列表的 agent 与 agentId 筛选、审计
# → 高危（reauthWindow=5s 重启）：过期后删除用户 / 重置密码 / 清两步验证 403 reauth_required，密码二次验证后成功
# → 限速（agent.writeRatePerHour=3 重启）：超出后写操作 429（参数不合法的也计数）、读操作不受影响
# → 停用 A：会话立即 401、A 的码不能注册，名下用户的个人码照常可用且新用户仍归 A
# 黑盒，只用 curl / jq / python3（TOTP 与时间计算）。不依赖面板：套餐不绑节点组。hub-server 中途换配置重启两次。
source "$(dirname "$0")/common.sh"
set -e

BASE=http://127.0.0.1:8080
ROOT_USER=it18-root  # 与前面用例的超管区分（run_all 只在开头重置一次库）
AGENT_A=it18-agent-a
AGENT_B=it18-agent-b
CRED="it-$(openssl rand -hex 12)"
AGENT_CRED="it-$(openssl rand -hex 12)"
USER_CRED="it-$(openssl rand -hex 12)"
USER_CRED_NEW="it-$(openssl rand -hex 12)"
MAIL_DOMAIN=it18.example.com
GIB=1073741824
MIB=1048576
DAY_SECONDS=86400
PLAN_DAYS=30
# poolCost 的周期数（Ruling-108：时长内最多能用到的周期数）
WEEKLY_PERIODS=6           # 30 天按周重置：ceil(30 / 7) + 1 = 6 期
HOURLY_DAYS=1
HOURLY_PERIODS=25          # 1 天按小时重置：1 × 24 + 1 = 25 期
DAILY_PERIODS=31           # 30 天按天重置：30 + 1 = 31 期
MONTHLY_PERIODS=3          # 30 天按月重置：ceil(30 / 28) + 1 = 3 期
CLOCK_SLACK_SECONDS=2      # 比对“从当时起算”的到期时间时容许的秒级误差
A_MAX_USERS=3
A_POOL=$((9 * GIB))        # 1（新建）+ 1（续期）+ 6（换按周套餐）后剩 1 GiB，正好够再建一个有限套餐的用户
A_POOL_RAISED=$((20 * GIB))
A_MAX_USERS_RAISED=4
B_MAX_USERS=5
B_POOL=$((10 * GIB))
GIFT_PRICE=100
BALANCE_TOPUP=1000
SHORT_WINDOW=5s
SHORT_WINDOW_WAIT=7
WRITE_RATE=3
AUDIT_PAGE_SIZE=100
WORK="$(mktemp -d /tmp/it18.XXXXXX)"
trap 'rm -rf "$WORK"; stop_all' EXIT

fail() { echo "IT-18: $*"; exit 1; }

# ---- 1. hub-server 与超管；另派生两份配置备用：短二次验证有效期、写操作限速 ----
it_hub_config "$WORK/hub-server.yaml" || fail "派生配置失败"
IT_REAUTH_WINDOW="$SHORT_WINDOW" it_hub_config "$WORK/hub-server-short.yaml" || fail "派生短有效期配置失败"
it_hub_config "$WORK/hub-server-rate.yaml" -e "s/^  writeRatePerHour: .*/  writeRatePerHour: $WRITE_RATE/" || fail "派生限速配置失败"
grep -q "^  writeRatePerHour: $WRITE_RATE\$" "$WORK/hub-server-rate.yaml" || fail "限速配置没有改到 agent.writeRatePerHour"
it_admin_cli admin-create "$ROOT_USER" "$CRED" || fail "admin-create 失败"
start_module v2-hub-server 8080 CONFIG="$WORK/hub-server.yaml"

# call <cookie jar> <csrf> <method> <path> [json] → 打印响应体；状态码写入 $WORK/code
call() {
  local args=(-s -o "$WORK/body" -w '%{http_code}' -b "$1" -c "$1" -X "$3" -H 'Content-Type: application/json')
  [ -n "$2" ] && args+=(-H "X-CSRF-Token: $2")
  [ -n "${5:-}" ] && args+=(--data "$5")
  curl "${args[@]}" "$BASE$4" > "$WORK/code"
  cat "$WORK/body"
}
expect_code() { [ "$(cat "$WORK/code")" = "$1" ] || fail "$2：期望 HTTP $1，实际 $(cat "$WORK/code") $(cat "$WORK/body")"; }
expect_error() { expect_code "$1" "$3"; [ "$(jq -r .code "$WORK/body")" = "$2" ] || fail "$3：期望错误码 $2，实际 $(cat "$WORK/body")"; }
login_body_of() { jq -nc --arg u "$1" --arg p "$2" '{username:$u,password:$p}'; }
# iso_epoch <ISO 时间>：转成 Unix 秒（容忍小数秒与时区）；iso_shift <ISO 时间> <秒>：平移后输出 UTC 的 ISO 时间
iso_epoch() { python3 -I -c 'import sys, datetime; print(int(datetime.datetime.fromisoformat(sys.argv[1].replace("Z", "+00:00")).timestamp()))' "$1"; }
iso_shift() {
  python3 -I -c '
import sys, datetime
t = datetime.datetime.fromisoformat(sys.argv[1].replace("Z", "+00:00")) + datetime.timedelta(seconds=int(sys.argv[2]))
print(t.astimezone(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"))' "$1" "$2"
}

HUB_JAR="$WORK/hub.jar"
CSRF="$(it_admin_login "$HUB_JAR" "$ROOT_USER" "$CRED")" || fail "hub 登录失败"
hub() { call "$HUB_JAR" "$CSRF" "$@"; }

# ---- 2. 套餐与系统设置：前面用例可能关掉了注册与工单（run_all 只重置一次库），这里显式打开 ----
new_plan() {  # new_plan <名称> <流量> <天数> <重置周期> [附加 JSON] → 新建套餐，响应体留在 $WORK/body，打印套餐 id
  local extra="${5:-}"
  [ -n "$extra" ] || extra='{}'
  hub POST /api/admin/plans "$(jq -nc --arg n "$1" --argjson l "$2" --argjson d "$3" --arg r "$4" --argjson x "$extra" \
    '{name:$n,trafficLimit:$l,durationDays:$d,trafficReset:$r,nodeGroupIds:[]} + $x')" | jq -r '.data.id // empty'
}
PLAN_ONCE="$(new_plan it18-once "$GIB" "$PLAN_DAYS" never)"; expect_code 200 "新建有限套餐"
PLAN_WEEKLY="$(new_plan it18-weekly "$GIB" "$PLAN_DAYS" weekly)"; expect_code 200 "新建按周重置套餐"
PLAN_UNLIMITED="$(new_plan it18-unlimited 0 "$PLAN_DAYS" never)"; expect_code 200 "新建不限流量套餐"
# 下面三个只放进 B 的白名单，用来核对 poolCost 的公式（Ruling-108）
PLAN_HOURLY="$(new_plan it18-hourly "$GIB" "$HOURLY_DAYS" hourly)"; expect_code 200 "新建按小时重置套餐"
PLAN_DAILY="$(new_plan it18-daily "$GIB" "$PLAN_DAYS" daily)"; expect_code 200 "新建按天重置套餐"
PLAN_MONTHLY="$(new_plan it18-monthly "$GIB" "$PLAN_DAYS" monthly)"; expect_code 200 "新建按月重置套餐"
PLAN_GIFT="$(new_plan it18-gift $((2 * GIB)) "$PLAN_DAYS" never \
  "$(jq -nc --argjson d "$PLAN_DAYS" --argjson p "$GIFT_PRICE" '{isOnSale:true,prices:[{durationDays:$d,price:$p}]}')")"
expect_code 200 "新建注册赠送与在售套餐"
GIFT_PRICE_ID="$(jq -r '.data.prices[0].id // empty' "$WORK/body")"
[ -n "$GIFT_PRICE_ID" ] || fail "在售套餐应带时长选项：$(cat "$WORK/body")"
hub PUT /api/admin/settings "$(jq -nc --argjson p "$PLAN_GIFT" \
  '{register:{mode:"invite",emailVerify:false,emailAllow:[],emailBlock:[],planId:$p},invite:{userEnabled:true,userMaxUses:0},ticket:{enabled:true},shop:{enabled:true}}')" >/dev/null
expect_code 200 "打开邀请注册（送套餐）、个人邀请码、工单与商店"

# ---- 3. 超管新建代理（Ruling-103）：白名单含不限套餐 400；重名 409；代理不进管理员列表、不能改角色、不能成为所有者 ----
agent_body() {  # agent_body <最大用户数> <流量池> <套餐 id 数组> <是否停用>
  jq -nc --argjson m "$1" --argjson t "$2" --argjson p "$3" --argjson d "$4" '{remark:"it18",maxUsers:$m,trafficPoolTotal:$t,planIds:$p,isDisabled:$d}'
}
agent_create_body() {  # agent_create_body <用户名> <最大用户数> <流量池> <套餐 id 数组>
  agent_body "$2" "$3" "$4" false | jq -c --arg u "$1" --arg p "$AGENT_CRED" '{username:$u,password:$p} + (del(.isDisabled))'
}
hub POST /api/admin/agents "$(agent_create_body "$AGENT_A" "$A_MAX_USERS" "$A_POOL" "[$PLAN_ONCE,$PLAN_UNLIMITED]")" >/dev/null
expect_error 400 invalid_argument "白名单含不限流量的套餐"
A_ID="$(hub POST /api/admin/agents "$(agent_create_body "$AGENT_A" "$A_MAX_USERS" "$A_POOL" "[$PLAN_ONCE,$PLAN_WEEKLY]")" | jq -r '.data.id // empty')"
expect_code 200 "新建代理 A"
jq -e --argjson m "$A_MAX_USERS" --argjson t "$A_POOL" '.data | .maxUsers == $m and .trafficPoolTotal == $t and .trafficPoolUsed == 0 and .userCount == 0 and (.isDisabled | not)' \
  "$WORK/body" >/dev/null || fail "新建的代理配额不对：$(cat "$WORK/body")"
hub POST /api/admin/agents "$(agent_create_body "$AGENT_A" 1 "$GIB" "[$PLAN_ONCE]")" >/dev/null; expect_error 409 conflict "代理重名"
hub POST /api/admin/agents "$(agent_create_body "$ROOT_USER" 1 "$GIB" "[$PLAN_ONCE]")" >/dev/null; expect_error 409 conflict "代理与管理员重名"
B_ID="$(hub POST /api/admin/agents "$(agent_create_body "$AGENT_B" "$B_MAX_USERS" "$B_POOL" "[$PLAN_ONCE,$PLAN_HOURLY,$PLAN_DAILY,$PLAN_MONTHLY]")" | jq -r '.data.id // empty')"
expect_code 200 "新建代理 B"
hub GET "/api/admin/admins?pageSize=100" | jq -e --arg a "$AGENT_A" --arg b "$AGENT_B" '[.data.items[] | select(.username == $a or .username == $b)] | length == 0' >/dev/null \
  || fail "管理员列表不应含代理：$(cat "$WORK/body")"
[ "$(hub GET "/api/admin/agents?keyword=it18-agent" | jq -c '[.data.items[].username] | sort')" = "[\"$AGENT_A\",\"$AGENT_B\"]" ] || fail "代理列表应有 A、B：$(cat "$WORK/body")"
hub PUT "/api/admin/admins/$A_ID" '{"roles":["finance"],"isDisabled":false}' >/dev/null; expect_error 409 conflict "经管理员接口改代理"
hub GET "/api/admin/agents/$A_ID" >/dev/null; expect_code 200 "改角色被拒后代理详情"
! it_admin_cli admin-owner-set "$AGENT_A" 2>/dev/null || fail "代理不应能被设为所有者"
grep -q '所有者必须是启用状态的超级管理员' /tmp/it-admin-cli.log || fail "admin-owner-set 应因“不是超管”被拒：$(cat /tmp/it-admin-cli.log)"
hub GET "/api/admin/agents/999999" >/dev/null; expect_error 404 not_found "不存在的代理"

# 平台用户（没有归属）作越权目标
P_USER=it18-platform
P_ID="$(hub POST /api/admin/users "$(login_body_of "$P_USER" "$USER_CRED" | jq -c --argjson p "$PLAN_ONCE" --arg e "$P_USER@$MAIL_DOMAIN" '. + {planId:$p,email:$e}')" | jq -r '.data.id // empty')"
expect_code 200 "新建平台用户"
[ "$(jq -c .data.agent "$WORK/body")" = "null" ] || fail "管理员建的用户应没有归属：$(cat "$WORK/body")"

# ---- 4. 代理登录与隔离（Ruling-101）----
A_JAR="$WORK/agent-a.jar"
B_JAR="$WORK/agent-b.jar"
A_CSRF="$(it_admin_login "$A_JAR" "$AGENT_A" "$AGENT_CRED")" || fail "代理 A 登录失败"
B_CSRF="$(it_admin_login "$B_JAR" "$AGENT_B" "$AGENT_CRED")" || fail "代理 B 登录失败"
agent_a() { call "$A_JAR" "$A_CSRF" "$@"; }
agent_b() { call "$B_JAR" "$B_CSRF" "$@"; }
agent_a GET /api/admin/auth/me >/dev/null; expect_code 200 "代理 me"
jq -e '.data | .kind == "agent" and .permissions == [] and (.isSuperAdmin | not) and (.isOwner | not) and (.totpSetupRequired | not) and .menuOrder == []' "$WORK/body" >/dev/null \
  || fail "代理的会话信息不对：$(cat "$WORK/body")"
[ "$(hub GET /api/admin/auth/me | jq -r .data.kind)" = "staff" ] || fail "超管的会话 kind 应为 staff：$(cat "$WORK/body")"
for path in /api/admin/dashboard /api/admin/analytics/backlog /api/admin/users "/api/admin/users/$P_ID" /api/admin/servers /api/admin/nodes \
  /api/admin/settings /api/admin/audit-logs /api/admin/admins /api/admin/roles /api/admin/plans /api/admin/orders /api/admin/tickets \
  /api/admin/invite-codes /api/admin/agents /api/admin/ssh-credentials; do
  agent_a GET "$path" >/dev/null; expect_error 403 permission_denied "代理访问员工接口 GET $path"
done
agent_a POST /api/admin/users "$(login_body_of it18-sneak "$USER_CRED")" >/dev/null; expect_error 403 permission_denied "代理调员工的新建用户"
agent_a PUT /api/admin/settings '{"ticket":{"enabled":false}}' >/dev/null; expect_error 403 permission_denied "代理改系统设置"
agent_a PUT "/api/admin/users/$P_ID/agent" "$(jq -nc --argjson a "$A_ID" '{agentId:$a}')" >/dev/null; expect_error 403 permission_denied "代理调划转接口"
hub GET /api/admin/agent/summary >/dev/null; expect_error 403 permission_denied "超管调代理接口"
hub POST /api/admin/agent/users "$(login_body_of it18-sneak "$USER_CRED" | jq -c --argjson p "$PLAN_ONCE" '. + {planId:$p}')" >/dev/null
expect_error 403 permission_denied "超管调代理的新建用户"
call "$WORK/anon.jar" "" GET /api/admin/agent/summary >/dev/null; expect_error 401 unauthenticated "未登录调代理接口"
call "$WORK/anon.jar" "" GET /api/admin/agents >/dev/null; expect_error 401 unauthenticated "未登录调代理管理"

# ---- 5. 配额概览与白名单（poolCost = 流量 × 时长内最多能用到的重置周期数，Ruling-108）----
summary_a() { agent_a GET /api/admin/agent/summary | jq -c '.data | [.userCount, .trafficPoolUsed, .trafficPoolTotal, .maxUsers]'; }
[ "$(summary_a)" = "[0,0,$A_POOL,$A_MAX_USERS]" ] || fail "初始配额概览不对：$(cat "$WORK/body")"
[ "$(agent_a GET /api/admin/agent/plans | jq -c '[.data[] | [.id, .poolCost, .trafficReset]] | sort')" \
  = "$(jq -nc --argjson o "$PLAN_ONCE" --argjson w "$PLAN_WEEKLY" --argjson g "$GIB" --argjson n "$WEEKLY_PERIODS" '[[$o, $g, "never"], [$w, $g * $n, "weekly"]] | sort')" ] \
  || fail "白名单与 poolCost 不对：$(cat "$WORK/body")"
jq -e '.data | all(has("price") | not) and all(has("nodeGroupIds") | not)' "$WORK/body" >/dev/null || fail "代理看到的套餐不应带价格与节点组：$(cat "$WORK/body")"
[ "$(agent_b GET /api/admin/agent/plans | jq -c '[.data[] | [.id, .poolCost, .trafficReset]] | sort')" \
  = "$(jq -nc --argjson o "$PLAN_ONCE" --argjson h "$PLAN_HOURLY" --argjson d "$PLAN_DAILY" --argjson m "$PLAN_MONTHLY" --argjson g "$GIB" \
    --argjson hp "$HOURLY_PERIODS" --argjson dp "$DAILY_PERIODS" --argjson mp "$MONTHLY_PERIODS" \
    '[[$o, $g, "never"], [$h, $g * $hp, "hourly"], [$d, $g * $dp, "daily"], [$m, $g * $mp, "monthly"]] | sort')" ] \
  || fail "按小时 / 天 / 月重置的 poolCost 应为 ${HOURLY_PERIODS} / ${DAILY_PERIODS} / ${MONTHLY_PERIODS} 期：$(cat "$WORK/body")"

# ---- 6. 新建、分配、续期各扣一次池；池不足 409 且什么都不变；用户数上限 409 ----
agent_user_body() {  # agent_user_body <用户名> [套餐 id]
  login_body_of "$1" "$USER_CRED" | jq -c --arg e "$1@$MAIL_DOMAIN" --arg p "${2:-}" '. + {email:$e,remark:"it18"} + (if $p == "" then {} else {planId:($p | tonumber)} end)'
}
agent_a POST /api/admin/agent/users "$(agent_user_body it18-a0)" >/dev/null; expect_error 400 invalid_argument "新建用户不带套餐"
agent_a POST /api/admin/agent/users "$(agent_user_body it18-a0 "$PLAN_GIFT")" >/dev/null; expect_error 400 invalid_argument "新建用户带白名单外的套餐"
agent_a POST /api/admin/agent/users "$(agent_user_body it18-a0 "$PLAN_UNLIMITED")" >/dev/null; expect_error 400 invalid_argument "新建用户带不限套餐"
[ "$(summary_a)" = "[0,0,$A_POOL,$A_MAX_USERS]" ] || fail "被拒的新建不应扣池或建用户：$(cat "$WORK/body")"
A1="$(agent_a POST /api/admin/agent/users "$(agent_user_body it18-a1 "$PLAN_ONCE")" | jq -r '.data.id // empty')"; expect_code 200 "代理新建用户 a1"
jq -e --argjson p "$PLAN_ONCE" --argjson g "$GIB" '.data | .planId == $p and .trafficLimit == $g and .status == "active" and (has("provisions") | not) and (has("invitedBy") | not)' \
  "$WORK/body" >/dev/null || fail "新建的名下用户不对或带了不该给代理看的字段：$(cat "$WORK/body")"
[ "$(summary_a)" = "[1,$GIB,$A_POOL,$A_MAX_USERS]" ] || fail "新建后应扣 1 GiB、名下 1 人：$(cat "$WORK/body")"
[ "$(hub GET "/api/admin/users/$A1" | jq -c '[.data.agent.id, .data.agent.username, .data.invitedBy]')" = "[$A_ID,\"$AGENT_A\",null]" ] \
  || fail "超管看 a1 应归 A：$(cat "$WORK/body")"
allocations_a() { agent_a GET "/api/admin/agent/allocations?pageSize=100" | jq -c '[.data.items[] | [.kind, .userId, .planId, .trafficBytes]]'; }
[ "$(allocations_a)" = "[[\"create\",$A1,$PLAN_ONCE,$GIB]]" ] || fail "应有一条新建开通记录：$(cat "$WORK/body")"
a1_view() { agent_a GET "/api/admin/agent/users/$A1" | jq -c '.data | {planId, trafficLimit, trafficUsed, expiresAt, status, isDisabled, remark}'; }
EXP_FIRST="$(a1_view | jq -r .expiresAt)"
agent_a POST "/api/admin/agent/users/$A1/plan" "$(jq -nc --argjson p "$PLAN_ONCE" '{planId:$p}')" >/dev/null; expect_code 200 "续期同一套餐"
EXP_RENEWED="$(a1_view | jq -r .expiresAt)"
[ $(( $(iso_epoch "$EXP_RENEWED") - $(iso_epoch "$EXP_FIRST") )) -eq $((PLAN_DAYS * DAY_SECONDS)) ] || fail "未到期续期应从原到期日顺延 ${PLAN_DAYS} 天：$EXP_FIRST → $EXP_RENEWED"
[ "$(summary_a)" = "[1,$((2 * GIB)),$A_POOL,$A_MAX_USERS]" ] || fail "续期应再扣 1 GiB：$(cat "$WORK/body")"
agent_a POST "/api/admin/agent/users/$A1/plan" "$(jq -nc --argjson p "$PLAN_GIFT" '{planId:$p}')" >/dev/null; expect_error 400 invalid_argument "分配白名单外的套餐"
# 换成另一个白名单套餐 = 换购（Ruling-108）：立即生效，到期时间从当时起算，原剩余的约 60 天作废、不顺延
SWITCH_FROM=$(date +%s)
agent_a POST "/api/admin/agent/users/$A1/plan" "$(jq -nc --argjson p "$PLAN_WEEKLY" '{planId:$p}')" >/dev/null; expect_code 200 "分配按周重置的套餐（换购）"
SWITCH_TO=$(date +%s)
[ "$(jq -c --argjson p "$PLAN_WEEKLY" --argjson g "$GIB" '.data | [.planId == $p, .trafficLimit == $g, .trafficUsed, .pendingDays]' "$WORK/body")" = '[true,true,0,0]' ] \
  || fail "换购后套餐、额度或已用流量不对：$(cat "$WORK/body")"
EXP_SWITCHED="$(iso_epoch "$(jq -r .data.expiresAt "$WORK/body")")"
[ "$EXP_SWITCHED" -ge $((SWITCH_FROM + PLAN_DAYS * DAY_SECONDS - CLOCK_SLACK_SECONDS)) ] && [ "$EXP_SWITCHED" -le $((SWITCH_TO + PLAN_DAYS * DAY_SECONDS + CLOCK_SLACK_SECONDS)) ] \
  || fail "换套餐应从当时起算 ${PLAN_DAYS} 天、不从原到期日 $EXP_RENEWED 顺延：$(cat "$WORK/body")"
USED_AFTER_WEEKLY=$((2 * GIB + WEEKLY_PERIODS * GIB))
[ "$(summary_a)" = "[1,$USED_AFTER_WEEKLY,$A_POOL,$A_MAX_USERS]" ] || fail "按周重置的套餐应扣 ${WEEKLY_PERIODS} GiB：$(cat "$WORK/body")"
A1_BEFORE="$(a1_view)"
agent_a POST "/api/admin/agent/users/$A1/plan" "$(jq -nc --argjson p "$PLAN_WEEKLY" '{planId:$p}')" >/dev/null; expect_error 409 agent_pool_insufficient "池不够再续一期"
[ "$(jq -c '.data | [.poolCost, .poolRemaining]' "$WORK/body")" = "[$((WEEKLY_PERIODS * GIB)),$((A_POOL - USED_AFTER_WEEKLY))]" ] \
  || fail "池不足应带本次扣减与剩余：$(cat "$WORK/body")"
allocations_a | jq -e --argjson u "$A1" --argjson p "$PLAN_WEEKLY" --argjson b $((WEEKLY_PERIODS * GIB)) 'any(.[]; . == ["assign", $u, $p, $b])' >/dev/null \
  || fail "换购的开通记录应扣 ${WEEKLY_PERIODS} GiB：$(allocations_a)"
[ "$(a1_view)" = "$A1_BEFORE" ] || fail "池不足时用户不应变化：$A1_BEFORE → $(a1_view)"
[ "$(summary_a)" = "[1,$USED_AFTER_WEEKLY,$A_POOL,$A_MAX_USERS]" ] || fail "池不足时不应扣池：$(cat "$WORK/body")"
A2="$(agent_a POST /api/admin/agent/users "$(agent_user_body it18-a2 "$PLAN_ONCE")" | jq -r '.data.id // empty')"; expect_code 200 "代理新建用户 a2（用尽流量池）"
[ "$(summary_a)" = "[2,$A_POOL,$A_POOL,$A_MAX_USERS]" ] || fail "a2 之后池应正好用尽：$(cat "$WORK/body")"
agent_a POST /api/admin/agent/users "$(agent_user_body it18-a3 "$PLAN_ONCE")" >/dev/null; expect_error 409 agent_pool_insufficient "池用尽时新建用户"
[ "$(jq -c '.data | [.poolCost, .poolRemaining]' "$WORK/body")" = "[$GIB,0]" ] || fail "池用尽时应带本次扣减与剩余 0：$(cat "$WORK/body")"
[ "$(agent_a GET "/api/admin/agent/users?keyword=it18-a3" | jq '.data.total')" = "0" ] || fail "池不足时不应建出用户：$(cat "$WORK/body")"
[ "$(summary_a)" = "[2,$A_POOL,$A_POOL,$A_MAX_USERS]" ] || fail "池不足的新建不应改变配额：$(cat "$WORK/body")"
hub PUT "/api/admin/agents/$A_ID" "$(agent_body "$A_MAX_USERS" "$A_POOL_RAISED" "[$PLAN_ONCE,$PLAN_WEEKLY]" false)" >/dev/null; expect_code 200 "超管调高 A 的流量池"
A3="$(agent_a POST /api/admin/agent/users "$(agent_user_body it18-a3 "$PLAN_ONCE")" | jq -r '.data.id // empty')"; expect_code 200 "调高流量池后新建 a3"
USED_A=$((A_POOL + GIB))
[ "$(summary_a)" = "[3,$USED_A,$A_POOL_RAISED,$A_MAX_USERS]" ] || fail "a3 之后配额不对：$(cat "$WORK/body")"
agent_a POST /api/admin/agent/users "$(agent_user_body it18-a4 "$PLAN_ONCE")" >/dev/null; expect_error 409 agent_user_limit "达到用户数上限时新建"
[ "$(jq -r .message "$WORK/body")" = "名下用户数已达上限，不能再新建；请联系平台调整上限" ] || fail "达到上限的提示语不对：$(cat "$WORK/body")"
[ "$(summary_a)" = "[3,$USED_A,$A_POOL_RAISED,$A_MAX_USERS]" ] || fail "达到上限的新建不应扣池：$(cat "$WORK/body")"
agent_a POST /api/admin/agent/users "$(agent_user_body it18-a1 "$PLAN_ONCE")" >/dev/null; expect_code 409 "达到上限时新建重名用户（任一 409 都可）"

# ---- 7. 调额只能收紧（docs/03 §5.23 调低的判定）----
A1_LIMIT="$(a1_view | jq -r .trafficLimit)"
A1_EXP="$(a1_view | jq -r .expiresAt)"
quota_a1() { agent_a PUT "/api/admin/agent/users/$A1/quota" "$1" >/dev/null; }
quota_a1 "$(jq -nc --argjson l $((A1_LIMIT * 2)) '{trafficLimit:$l}')"; expect_error 400 invalid_argument "调高流量额度"
quota_a1 '{"trafficLimit":0}'; expect_error 400 invalid_argument "额度设为不限"
quota_a1 "$(jq -nc --arg t "$(iso_shift "$A1_EXP" "$DAY_SECONDS")" '{expiresAt:$t}')"; expect_error 400 invalid_argument "到期时间推后"
quota_a1 '{"expiresAt":null}'; expect_error 400 invalid_argument "到期时间设为不过期"
quota_a1 '{}'; expect_error 400 invalid_argument "调额一个字段都不给"
quota_a1 '{"trafficUsed":0}'; expect_error 400 invalid_argument "调额改已用流量"
[ "$(a1_view | jq -c '[.trafficLimit, .expiresAt]')" = "$(jq -nc --argjson l "$A1_LIMIT" --arg e "$A1_EXP" '[$l, $e]')" ] || fail "被拒的调额不应改动用户：$(a1_view)"
LOWER_LIMIT=$((512 * MIB))
quota_a1 "$(jq -nc --argjson l "$LOWER_LIMIT" '{trafficLimit:$l}')"; expect_code 200 "调低流量额度"
[ "$(jq -r .data.trafficLimit "$WORK/body")" = "$LOWER_LIMIT" ] || fail "调低后额度不对：$(cat "$WORK/body")"
EARLIER="$(iso_shift "$A1_EXP" "-$DAY_SECONDS")"
quota_a1 "$(jq -nc --arg t "$EARLIER" '{expiresAt:$t}')"; expect_code 200 "到期时间提前"
[ "$(iso_epoch "$(jq -r .data.expiresAt "$WORK/body")")" = "$(iso_epoch "$EARLIER")" ] || fail "提前后到期时间不对：$(cat "$WORK/body")"
[ "$(summary_a)" = "[3,$USED_A,$A_POOL_RAISED,$A_MAX_USERS]" ] || fail "调额不应动流量池：$(cat "$WORK/body")"
[ "$(allocations_a | jq length)" = "5" ] || fail "调额不应写开通记录：$(allocations_a)"

# ---- 8. 改备注与停用 / 启用；重置订阅（a1 自己看到订阅地址变化）----
agent_a PUT "/api/admin/agent/users/$A1" '{"remark":"it18 vip","isDisabled":true}' >/dev/null; expect_code 200 "停用名下用户"
[ "$(jq -c '.data | [.remark, .isDisabled, .status]' "$WORK/body")" = '["it18 vip",true,"disabled"]' ] || fail "停用后状态不对：$(cat "$WORK/body")"
# remark 与 isDisabled 都必填（Ruling-108 同批修订）：缺 isDisabled 不能当成“启用”，缺 remark 不能当成“清空备注”
agent_a PUT "/api/admin/agent/users/$A1" '{"remark":"it18 vip"}' >/dev/null; expect_error 400 invalid_argument "改用户缺 isDisabled"
agent_a PUT "/api/admin/agent/users/$A1" '{"isDisabled":true}' >/dev/null; expect_error 400 invalid_argument "改用户缺 remark"
agent_a PUT "/api/admin/agent/users/$A1" '{"remark":"it18 vip","isDisabled":null}' >/dev/null; expect_error 400 invalid_argument "改用户 isDisabled 为 null"
[ "$(a1_view | jq -c '[.remark, .isDisabled, .status]')" = '["it18 vip",true,"disabled"]' ] || fail "被拒的修改不应改动停用状态与备注：$(a1_view)"
agent_a PUT "/api/admin/agent/users/$A1" '{"remark":"it18 vip","isDisabled":false,"trafficLimit":1}' >/dev/null; expect_error 400 invalid_argument "改用户时夹带额度字段"
agent_a PUT "/api/admin/agent/users/$A1" '{"remark":"it18 vip","isDisabled":false}' >/dev/null; expect_code 200 "启用名下用户"
[ "$(jq -c '.data | [.isDisabled, .status]' "$WORK/body")" = '[false,"active"]' ] || fail "启用后状态应回到 active：$(cat "$WORK/body")"
[ "$(hub GET "/api/admin/users/$A1" | jq -r .data.remark)" = "it18 vip" ] || fail "超管应看到代理改的备注：$(cat "$WORK/body")"
user_login() {  # user_login <jar> <用户名> <密码> → 打印 CSRF token
  call "$1" "" POST /api/user/auth/login "$(login_body_of "$2" "$3")" | jq -r '.data.csrfToken // empty'
}
A1_JAR="$WORK/a1.jar"
A1_CSRF="$(user_login "$A1_JAR" it18-a1 "$USER_CRED")"; [ -n "$A1_CSRF" ] || fail "a1 登录失败：$(cat "$WORK/body")"
a1() { call "$A1_JAR" "$A1_CSRF" "$@"; }
SUB_BEFORE="$(a1 GET /api/user/subscription | jq -r '.data.url // empty')"; [ -n "$SUB_BEFORE" ] || fail "a1 取订阅地址失败：$(cat "$WORK/body")"
agent_a POST "/api/admin/agent/users/$A1/subscription/reset" >/dev/null; expect_code 200 "代理重置订阅"
SUB_AFTER="$(a1 GET /api/user/subscription | jq -r '.data.url // empty')"
[ -n "$SUB_AFTER" ] && [ "$SUB_AFTER" != "$SUB_BEFORE" ] || fail "重置后订阅地址应变化"
[ "$(curl -s -o /dev/null -w '%{http_code}' "$SUB_BEFORE")" = "404" ] || fail "重置后旧订阅地址应 404"

# ---- 9. 删除名下用户：流量池不退回，开通记录保留（userId 置空）----
agent_a DELETE "/api/admin/agent/users/$A2" >/dev/null; expect_code 200 "删除名下用户 a2"
agent_a GET "/api/admin/agent/users/$A2" >/dev/null; expect_error 404 not_found "删除后再看 a2"
[ "$(summary_a)" = "[2,$USED_A,$A_POOL_RAISED,$A_MAX_USERS]" ] || fail "删除用户后池不应退回、名下应为 2 人：$(cat "$WORK/body")"
[ "$(agent_a GET "/api/admin/agent/allocations?pageSize=100" | jq -c '[.data.items[] | select(.username == "it18-a2") | [.kind, .userId]]')" = '[["create",null]]' ] \
  || fail "已删除用户的开通记录应保留且 userId 为 null：$(cat "$WORK/body")"

# ---- 10. 邀请码与注册（Ruling-102）：代理码注册归 A、无邀请人、不送注册套餐；名下用户的个人码也归 A；达到上限后都不能注册 ----
hub PUT "/api/admin/agents/$A_ID" "$(agent_body "$A_MAX_USERS_RAISED" "$A_POOL_RAISED" "[$PLAN_ONCE,$PLAN_WEEKLY]" false)" >/dev/null; expect_code 200 "超管把 A 的上限调到 ${A_MAX_USERS_RAISED}"
code_a="$(agent_a POST /api/admin/agent/invite-codes '{"maxUses":0,"isDisabled":false,"remark":"it18-a"}')"; expect_code 200 "代理 A 建邀请码"
CODE_A="$(printf '%s' "$code_a" | jq -r .data.code)"
CODE_A_ID="$(printf '%s' "$code_a" | jq -r .data.id)"
[ "$(printf '%s' "$code_a" | jq -c '.data | [.kind, .agentId]')" = "[\"agent\",$A_ID]" ] || fail "代理码的类型与归属不对：$code_a"
CODE_B_ID="$(agent_b POST /api/admin/agent/invite-codes '{"maxUses":5,"isDisabled":false,"remark":"it18-b"}' | jq -r '.data.id // empty')"; expect_code 200 "代理 B 建邀请码"
agent_a PUT "/api/admin/agent/invite-codes/$CODE_B_ID" '{"maxUses":1,"isDisabled":true,"remark":"hijack"}' >/dev/null; expect_error 404 not_found "A 改 B 的邀请码"
agent_a DELETE "/api/admin/agent/invite-codes/$CODE_B_ID" >/dev/null; expect_error 404 not_found "A 删 B 的邀请码"
[ "$(agent_b GET /api/admin/agent/invite-codes | jq -c '[.data.items[] | [.id, .isDisabled, .remark]]')" = "[[$CODE_B_ID,false,\"it18-b\"]]" ] \
  || fail "B 的邀请码不应被 A 改动：$(cat "$WORK/body")"
[ "$(agent_a GET /api/admin/agent/invite-codes | jq -c '[.data.items[].id]')" = "[$CODE_A_ID]" ] || fail "A 只应看到自己的邀请码：$(cat "$WORK/body")"
[ "$(hub GET "/api/admin/invite-codes?kind=agent&agentId=$A_ID" | jq -c '[.data.items[] | [.id, .agentUsername]]')" = "[[$CODE_A_ID,\"$AGENT_A\"]]" ] \
  || fail "超管按代理筛邀请码不对：$(cat "$WORK/body")"
register_body_of() {  # register_body_of <用户名> <邀请码>
  login_body_of "$1" "$USER_CRED" | jq -c --arg e "$1@$MAIL_DOMAIN" --arg c "$2" '. + {email:$e,inviteCode:$c}'
}
admin_user_brief() { hub GET "/api/admin/users?keyword=$1" | jq -c --arg u "$1" '.data.items[] | select(.username == $u) | [.agent.id, .invitedBy, .planId]'; }
R1_JAR="$WORK/r1.jar"
session="$(call "$R1_JAR" "" POST /api/user/auth/register "$(register_body_of it18-r1 "$CODE_A")")"; expect_code 200 "用代理码注册 r1"
R1_CSRF="$(printf '%s' "$session" | jq -r '.data.csrfToken // empty')"
R1_ID="$(printf '%s' "$session" | jq -r .data.user.id)"
[ "$(admin_user_brief it18-r1)" = "[$A_ID,null,null]" ] || fail "代理码注册的用户应归 A、无邀请人、不送注册套餐：$(admin_user_brief it18-r1)"
R1_CODE="$(call "$R1_JAR" "$R1_CSRF" GET /api/user/invite | jq -r '.data.code // empty')"; [ -n "$R1_CODE" ] || fail "r1 取个人邀请码失败：$(cat "$WORK/body")"
call "$WORK/r2.jar" "" POST /api/user/auth/register "$(register_body_of it18-r2 "$R1_CODE")" >/dev/null; expect_code 200 "用名下用户的个人码注册 r2"
[ "$(admin_user_brief it18-r2)" = "[$A_ID,$R1_ID,null]" ] || fail "名下用户邀请来的应归 A、邀请人为 r1、不送注册套餐：$(admin_user_brief it18-r2)"
[ "$(summary_a | jq -c '.[0]')" = "$A_MAX_USERS_RAISED" ] || fail "注册两人后 A 应达到上限：$(cat "$WORK/body")"
call "$WORK/r3.jar" "" POST /api/user/auth/register "$(register_body_of it18-r3 "$CODE_A")" >/dev/null; expect_code 400 "A 达到上限时用代理码注册"
call "$WORK/r3.jar" "" POST /api/user/auth/register "$(register_body_of it18-r3 "$R1_CODE")" >/dev/null; expect_code 400 "A 达到上限时用名下用户的个人码注册"
[ "$(hub GET "/api/admin/users?keyword=it18-r3" | jq '.data.total')" = "0" ] || fail "被拒的注册不应建出用户：$(cat "$WORK/body")"
[ "$(summary_a | jq -c '.[1]')" = "$USED_A" ] || fail "注册不应动流量池：$(cat "$WORK/body")"

# ---- 11. 准备 B 名下与平台用户的订单、流水、工单；用户自助下单不动代理的池 ----
B1="$(agent_b POST /api/admin/agent/users "$(agent_user_body it18-b1 "$PLAN_ONCE")" | jq -r '.data.id // empty')"; expect_code 200 "代理 B 新建用户 b1"
for uid in "$A1" "$B1" "$P_ID"; do
  hub POST "/api/admin/users/$uid/balance" "$(jq -nc --argjson a "$BALANCE_TOPUP" '{amount:$a,remark:"it18 充值"}')" >/dev/null; expect_code 200 "超管给用户 $uid 加余额"
done
B1_JAR="$WORK/b1.jar"
P_JAR="$WORK/p.jar"
B1_CSRF="$(user_login "$B1_JAR" it18-b1 "$USER_CRED")"; [ -n "$B1_CSRF" ] || fail "b1 登录失败：$(cat "$WORK/body")"
P_CSRF="$(user_login "$P_JAR" "$P_USER" "$USER_CRED")"; [ -n "$P_CSRF" ] || fail "平台用户登录失败：$(cat "$WORK/body")"
b1() { call "$B1_JAR" "$B1_CSRF" "$@"; }
puser() { call "$P_JAR" "$P_CSRF" "$@"; }
ORDER_BODY="$(jq -nc --argjson p "$GIFT_PRICE_ID" '{kind:"plan",planPriceId:$p}')"
a1 POST /api/user/orders "$ORDER_BODY" >/dev/null; expect_code 200 "a1 自助下单"
b1 POST /api/user/orders "$ORDER_BODY" >/dev/null; expect_code 200 "b1 自助下单"
puser POST /api/user/orders "$ORDER_BODY" >/dev/null; expect_code 200 "平台用户自助下单"
[ "$(summary_a | jq -c '.[1]')" = "$USED_A" ] || fail "名下用户自助下单不应动代理的池：$(cat "$WORK/body")"
TICKET_A="$(a1 POST /api/user/tickets '{"subject":"IT-18 a1","content":"a1 的问题"}' | jq -r '.data.id // empty')"; expect_code 200 "a1 提工单"
TICKET_B="$(b1 POST /api/user/tickets '{"subject":"IT-18 b1","content":"b1 的问题"}' | jq -r '.data.id // empty')"; expect_code 200 "b1 提工单"
TICKET_P="$(puser POST /api/user/tickets '{"subject":"IT-18 p","content":"平台用户的问题"}' | jq -r '.data.id // empty')"; expect_code 200 "平台用户提工单"

# ---- 12. 列表只有名下的数据：用户、订单、流水、工单 ----
agent_a GET "/api/admin/agent/users?pageSize=100" > "$WORK/a_users.json"; expect_code 200 "A 的用户列表"
[ "$(jq -c '[.data.items[].username] | sort' "$WORK/a_users.json")" = '["it18-a1","it18-a3","it18-r1","it18-r2"]' ] || fail "A 的用户列表应正好是名下 4 人：$(cat "$WORK/a_users.json")"
agent_a GET "/api/admin/agent/orders?pageSize=100" > "$WORK/a_orders.json"; expect_code 200 "A 的订单列表"
jq -e --argjson a1 "$A1" '(.data.items | length) == 1 and .data.items[0].userId == $a1' "$WORK/a_orders.json" >/dev/null || fail "A 的订单列表应只有 a1 的一笔：$(cat "$WORK/a_orders.json")"
[ "$(agent_a GET "/api/admin/agent/orders?userId=$P_ID" | jq '.data.items | length')" = "0" ] || fail "A 按平台用户筛订单应为空：$(cat "$WORK/body")"
[ "$(agent_a GET "/api/admin/agent/orders?userId=$B1" | jq '.data.items | length')" = "0" ] || fail "A 按 B 名下用户筛订单应为空：$(cat "$WORK/body")"
agent_a GET "/api/admin/agent/wallet-ledger?pageSize=100" > "$WORK/a_ledger.json"; expect_code 200 "A 的余额流水"
jq -e --argjson a1 "$A1" '(.data.items | length) == 2 and all(.data.items[]; .userId == $a1 and .adminUsername == "")' "$WORK/a_ledger.json" >/dev/null \
  || fail "A 的流水应只有 a1 的两笔且不带员工账号名：$(cat "$WORK/a_ledger.json")"
[ "$(hub GET "/api/admin/wallet-ledger?userId=$A1" | jq -r '[.data.items[] | select(.adminUsername != "")] | length')" = "1" ] \
  || fail "超管看同一笔调整应带操作人：$(cat "$WORK/body")"
[ "$(agent_a GET "/api/admin/agent/wallet-ledger?userId=$B1" | jq '.data.items | length')" = "0" ] || fail "A 按 B 名下用户筛流水应为空：$(cat "$WORK/body")"
agent_a GET "/api/admin/agent/tickets?pageSize=100" > "$WORK/a_tickets.json"; expect_code 200 "A 的工单列表"
[ "$(jq -c '[.data.items[].id]' "$WORK/a_tickets.json")" = "[$TICKET_A]" ] || fail "A 的工单列表应只有 a1 的工单：$(cat "$WORK/a_tickets.json")"
[ "$(agent_a GET "/api/admin/agent/tickets?userId=$P_ID" | jq '.data.items | length')" = "0" ] || fail "A 按平台用户筛工单应为空：$(cat "$WORK/body")"

# ---- 13. 工单：员工回复不带账号名，代理回复 answered、关闭；B 名下与平台用户的工单 404 ----
hub POST "/api/admin/tickets/$TICKET_A/messages" '{"content":"员工先回一句"}' >/dev/null; expect_code 200 "员工回复 a1 的工单"
agent_a GET "/api/admin/agent/tickets/$TICKET_A" >/dev/null; expect_code 200 "A 看 a1 的工单"
[ "$(jq -c '[.data.messages[] | select(.authorKind == "admin") | (.authorName // "")]' "$WORK/body")" = '[""]' ] \
  || fail "代理侧员工的回复不应带账号名：$(cat "$WORK/body")"
agent_a POST "/api/admin/agent/tickets/$TICKET_A/messages" '{"content":"代理来回复"}' >/dev/null; expect_code 200 "A 回复工单"
[ "$(jq -c '[.data.status, .data.messages[-1].authorKind, .data.messages[-1].authorName]' "$WORK/body")" = "[\"answered\",\"admin\",\"$AGENT_A\"]" ] \
  || fail "代理回复后应为 answered、带代理自己的用户名：$(cat "$WORK/body")"
[ "$(a1 GET "/api/user/tickets/$TICKET_A" | jq -c '[.data.messages[-1].authorKind, (.data.messages[-1].authorName // "")]')" = '["admin",""]' ] \
  || fail "用户侧代理的回复应显示为客服、不带用户名：$(cat "$WORK/body")"
[ "$(hub GET "/api/admin/tickets/$TICKET_A" | jq -r '.data.messages[1].authorName')" = "$ROOT_USER" ] || fail "员工侧应看到员工回复人：$(cat "$WORK/body")"
agent_a POST "/api/admin/agent/tickets/$TICKET_A/close" >/dev/null; expect_code 200 "A 关闭工单"
[ "$(jq -r .data.status "$WORK/body")" = "closed" ] || fail "关闭后应为 closed：$(cat "$WORK/body")"
for t in "$TICKET_B" "$TICKET_P"; do
  agent_a GET "/api/admin/agent/tickets/$t" >/dev/null; expect_error 404 not_found "A 看不在名下的工单 $t"
  agent_a POST "/api/admin/agent/tickets/$t/messages" '{"content":"越权"}' >/dev/null; expect_error 404 not_found "A 回复不在名下的工单 $t"
  agent_a POST "/api/admin/agent/tickets/$t/close" >/dev/null; expect_error 404 not_found "A 关闭不在名下的工单 $t"
  [ "$(hub GET "/api/admin/tickets/$t" | jq -c '[.data.status, (.data.messages | length)]')" = '["open",1]' ] || fail "工单 $t 不应被 A 改动：$(cat "$WORK/body")"
done

# ---- 14. 水平越权：B 名下用户与平台用户的 id，每个按 id 的接口都 404，超管侧看不出变化 ----
snapshot() { hub GET "/api/admin/users/$1" | jq -c '.data | {remark, isDisabled, status, planId, trafficLimit, trafficUsed, expiresAt, totpEnabled, agent}'; }
SNAP_B1="$(snapshot "$B1")"
SNAP_P="$(snapshot "$P_ID")"
P_SUB="$(puser GET /api/user/subscription | jq -r '.data.url // empty')"
SUMMARY_BEFORE="$(summary_a)"
for target in "$B1" "$P_ID"; do
  agent_a GET "/api/admin/agent/users/$target" >/dev/null; expect_error 404 not_found "A 看 $target"
  agent_a PUT "/api/admin/agent/users/$target" '{"remark":"hijack","isDisabled":true}' >/dev/null; expect_error 404 not_found "A 改 $target"
  agent_a POST "/api/admin/agent/users/$target/plan" "$(jq -nc --argjson p "$PLAN_ONCE" '{planId:$p}')" >/dev/null; expect_error 404 not_found "A 给 $target 分配套餐"
  agent_a PUT "/api/admin/agent/users/$target/quota" '{"trafficLimit":1}' >/dev/null; expect_error 404 not_found "A 调 $target 的额度"
  agent_a PUT "/api/admin/agent/users/$target/password" "$(jq -nc --arg p "$USER_CRED_NEW" '{password:$p}')" >/dev/null; expect_error 404 not_found "A 重置 $target 的密码"
  agent_a DELETE "/api/admin/agent/users/$target/totp" >/dev/null; expect_error 404 not_found "A 清 $target 的两步验证"
  agent_a POST "/api/admin/agent/users/$target/subscription/reset" >/dev/null; expect_error 404 not_found "A 重置 $target 的订阅"
  agent_a DELETE "/api/admin/agent/users/$target" >/dev/null; expect_error 404 not_found "A 删 $target"
done
[ "$(snapshot "$B1")" = "$SNAP_B1" ] || fail "B 名下用户不应被 A 改动：$SNAP_B1 → $(snapshot "$B1")"
[ "$(snapshot "$P_ID")" = "$SNAP_P" ] || fail "平台用户不应被 A 改动：$SNAP_P → $(snapshot "$P_ID")"
[ "$(puser GET /api/user/subscription | jq -r '.data.url // empty')" = "$P_SUB" ] || fail "平台用户的订阅地址不应被 A 重置"
[ -n "$(user_login "$WORK/p-check.jar" "$P_USER" "$USER_CRED")" ] || fail "平台用户仍应能用原密码登录：$(cat "$WORK/body")"
[ "$(summary_a)" = "$SUMMARY_BEFORE" ] || fail "越权请求不应动 A 的配额：$(summary_a)"
[ "$(agent_b GET "/api/admin/agent/users/$B1" | jq -r .data.remark)" = "it18" ] || fail "B 看自己的用户应不受影响：$(cat "$WORK/body")"
agent_b GET "/api/admin/agent/users/$A1" >/dev/null; expect_error 404 not_found "B 看 A 名下的用户"

# ---- 15. 超管侧：划转与收回、池低于已用量 409、改白名单、开通记录、用户列表筛选 ----
hub PUT "/api/admin/users/$P_ID/agent" "$(jq -nc --argjson a "$A_ID" '{agentId:$a}')" >/dev/null; expect_code 200 "把平台用户划给 A（已达上限也可以）"
[ "$(jq -c '.data.agent | [.id, .username]' "$WORK/body")" = "[$A_ID,\"$AGENT_A\"]" ] || fail "划转后归属应为 A：$(cat "$WORK/body")"
agent_a GET "/api/admin/agent/users/$P_ID" >/dev/null; expect_code 200 "划转后 A 能看到该用户"
[ "$(summary_a | jq -c '[.[0], .[1]]')" = "[$((A_MAX_USERS_RAISED + 1)),$USED_A]" ] || fail "划转不受上限限制、不动流量池：$(cat "$WORK/body")"
hub PUT "/api/admin/users/$P_ID/agent" '{"agentId":999999}' >/dev/null; expect_error 400 invalid_argument "划给不存在的代理"
hub PUT "/api/admin/users/$P_ID/agent" '{"agentId":null}' >/dev/null; expect_code 200 "收回为平台用户"
[ "$(jq -c .data.agent "$WORK/body")" = "null" ] || fail "收回后应没有归属：$(cat "$WORK/body")"
agent_a GET "/api/admin/agent/users/$P_ID" >/dev/null; expect_error 404 not_found "收回后 A 看该用户"
hub PUT "/api/admin/users/999999/agent" "$(jq -nc --argjson a "$A_ID" '{agentId:$a}')" >/dev/null; expect_error 404 not_found "划转不存在的用户"
hub PUT "/api/admin/agents/$A_ID" "$(agent_body "$A_MAX_USERS_RAISED" $((USED_A - 1)) "[$PLAN_ONCE,$PLAN_WEEKLY]" false)" >/dev/null; expect_error 409 conflict "流量池调到低于已用量"
hub PUT "/api/admin/agents/$A_ID" "$(agent_body "$A_MAX_USERS_RAISED" "$A_POOL_RAISED" "[$PLAN_ONCE,$PLAN_UNLIMITED]" false)" >/dev/null; expect_error 400 invalid_argument "白名单改成含不限套餐"
hub PUT "/api/admin/agents/$A_ID" "$(agent_body "$A_MAX_USERS_RAISED" "$A_POOL_RAISED" "[$PLAN_ONCE]" false)" >/dev/null; expect_code 200 "白名单去掉按周重置的套餐"
[ "$(agent_a GET /api/admin/agent/plans | jq -c '[.data[].id]')" = "[$PLAN_ONCE]" ] || fail "改白名单后 A 只应看到有限套餐：$(cat "$WORK/body")"
agent_a POST "/api/admin/agent/users/$A1/plan" "$(jq -nc --argjson p "$PLAN_WEEKLY" '{planId:$p}')" >/dev/null; expect_error 400 invalid_argument "白名单去掉后再分配该套餐"
hub GET "/api/admin/agents/$A_ID/allocations?pageSize=100" > "$WORK/allocations.json"; expect_code 200 "超管看 A 的开通记录"
[ "$(jq -c '[.data.items[] | .kind] | sort' "$WORK/allocations.json")" = '["assign","assign","create","create","create"]' ] \
  || fail "开通记录应为 3 条新建、2 条分配：$(cat "$WORK/allocations.json")"
[ "$(jq '[.data.items[].trafficBytes] | add' "$WORK/allocations.json")" = "$USED_A" ] || fail "开通记录的扣减之和应等于已用量：$(cat "$WORK/allocations.json")"
[ "$(hub GET "/api/admin/agents/$B_ID/allocations" | jq -c '[.data.items[] | [.userId, .kind]]')" = "[[$B1,\"create\"]]" ] || fail "B 的开通记录不对：$(cat "$WORK/body")"
[ "$(hub GET "/api/admin/users?agentId=$A_ID&pageSize=100" | jq -c '[.data.items[].username] | sort')" = '["it18-a1","it18-a3","it18-r1","it18-r2"]' ] \
  || fail "员工按代理筛用户不对：$(cat "$WORK/body")"
hub GET "/api/admin/users?keyword=it18-&pageSize=100" | jq -e --argjson b "$B_ID" \
  '(.data.items | all(has("agent"))) and ([.data.items[] | select(.username == "it18-b1") | .agent.id] == [$b]) and ([.data.items[] | select(.username == "it18-platform") | .agent] == [null])' \
  >/dev/null || fail "员工的用户列表应带归属：$(cat "$WORK/body")"
hub GET "/api/admin/users?agentId=0" >/dev/null; expect_code 400 "按代理筛用户 agentId 非法"
hub GET "/api/admin/audit-logs?pageSize=$AUDIT_PAGE_SIZE" > "$WORK/audit.json"; expect_code 200 "审计日志"
for action in agent.create agent.update user.agent_transfer; do
  jq -e --arg a "$action" 'any(.data.items[]; .action == $a)' "$WORK/audit.json" >/dev/null || fail "审计里应有 ${action}：$(jq -c '[.data.items[].action]' "$WORK/audit.json")"
done
jq -e --arg id "$A_ID" '[.data.items[] | select(.action == "agent.update" and .targetId == $id)] | length >= 3' "$WORK/audit.json" >/dev/null \
  || fail "A 的每次修改都应记 agent.update：$(jq -c '[.data.items[] | select(.action == "agent.update")]' "$WORK/audit.json")"
jq -e --argjson a "$A_ID" 'any(.data.items[]; .actorId == $a and .actorType == "admin")' "$WORK/audit.json" >/dev/null \
  || fail "代理的写操作应以代理自己的账号记审计：$(jq -c '[.data.items[] | [.actorId, .action]]' "$WORK/audit.json")"

# ---- 16. 高危（R12）：换 reauthWindow=$SHORT_WINDOW 的配置重启，过期后删除 / 重置密码 / 清两步验证须二次验证 ----
# 先让 a1 开两步验证，清除才有意义
TOTP_SECRET="$(a1 POST /api/user/auth/totp/setup | jq -r '.data.secret // empty')"; [ -n "$TOTP_SECRET" ] || fail "a1 生成两步验证失败：$(cat "$WORK/body")"
a1 POST /api/user/auth/totp/enable "$(jq -nc --arg c "$(it_totp_code "$TOTP_SECRET" "$(it_totp_now_step)")" '{code:$c}')" >/dev/null; expect_code 200 "a1 开启两步验证"
[ "$(agent_a GET "/api/admin/agent/users/$A1" | jq -r .data.totpEnabled)" = "true" ] || fail "代理应看到 a1 已开两步验证：$(cat "$WORK/body")"
stop_modules
wait_port_free 8080 30 || fail "hub-server 没有退出"
start_module v2-hub-server 8080 CONFIG="$WORK/hub-server-short.yaml"
A_CSRF="$(it_admin_login "$A_JAR" "$AGENT_A" "$AGENT_CRED")" || fail "短有效期下代理 A 登录失败"
sleep "$SHORT_WINDOW_WAIT"
agent_a DELETE "/api/admin/agent/users/$A3" >/dev/null; expect_error 403 reauth_required "过期后删除名下用户"
agent_a PUT "/api/admin/agent/users/$A1/password" "$(jq -nc --arg p "$USER_CRED_NEW" '{password:$p}')" >/dev/null; expect_error 403 reauth_required "过期后重置名下用户密码"
agent_a DELETE "/api/admin/agent/users/$A1/totp" >/dev/null; expect_error 403 reauth_required "过期后清名下用户两步验证"
agent_a PUT "/api/admin/agent/users/$A1" '{"remark":"it18 vip","isDisabled":false}' >/dev/null; expect_code 200 "过期后普通写操作照常"
agent_a GET "/api/admin/agent/users/$A3" >/dev/null; expect_code 200 "被拒的删除没有生效"
agent_a POST /api/admin/auth/reauth "$(jq -nc --arg p "wrong-$AGENT_CRED" '{password:$p}')" >/dev/null; expect_error 400 invalid_argument "代理二次验证密码错"
out="$(it_admin_reauth "$A_JAR" "$A_CSRF" "$AGENT_A" "$AGENT_CRED")" || fail "代理二次验证失败：$out"
agent_a DELETE "/api/admin/agent/users/$A3" >/dev/null; expect_code 200 "二次验证后删除名下用户"
agent_a PUT "/api/admin/agent/users/$A1/password" "$(jq -nc --arg p "$USER_CRED_NEW" '{password:$p}')" >/dev/null; expect_code 200 "二次验证后重置名下用户密码"
agent_a DELETE "/api/admin/agent/users/$A1/totp" >/dev/null; expect_code 200 "二次验证后清名下用户两步验证"
[ "$(agent_a GET "/api/admin/agent/users/$A1" | jq -r .data.totpEnabled)" = "false" ] || fail "清除后 a1 应未开两步验证：$(cat "$WORK/body")"
a1 GET /api/user/auth/me >/dev/null; expect_code 401 "重置密码 / 清两步验证后 a1 原会话失效"
[ -n "$(user_login "$WORK/a1-new.jar" it18-a1 "$USER_CRED_NEW")" ] || fail "a1 应能用新密码登录（不再要验证码）：$(cat "$WORK/body")"
[ "$(summary_a | jq -c '[.[0], .[1]]')" = "[$((A_MAX_USERS_RAISED - 1)),$USED_A]" ] || fail "删除 a3 后名下少一人、池不退回：$(cat "$WORK/body")"

# ---- 17. 限速（Ruling-104）：换 writeRatePerHour=$WRITE_RATE 的配置重启，B 的写操作超出即 429，读不受影响 ----
stop_modules
wait_port_free 8080 30 || fail "hub-server 没有退出"
start_module v2-hub-server 8080 CONFIG="$WORK/hub-server-rate.yaml"
CSRF="$(it_admin_login "$HUB_JAR" "$ROOT_USER" "$CRED")" || fail "限速配置下超管登录失败"
B_CSRF="$(it_admin_login "$B_JAR" "$AGENT_B" "$AGENT_CRED")" || fail "限速配置下代理 B 登录失败"
A_CSRF="$(it_admin_login "$A_JAR" "$AGENT_A" "$AGENT_CRED")" || fail "限速配置下代理 A 登录失败"
agent_b PUT "/api/admin/agent/users/$B1" '{"remark":"","isDisabled":false,"bogus":1}' >/dev/null; expect_error 400 invalid_argument "B 第 1 次写（参数不合法，也计数）"
for i in 2 3; do
  agent_b PUT "/api/admin/agent/users/$B1" "$(jq -nc --arg r "it18-rate-$i" '{remark:$r,isDisabled:false}')" >/dev/null; expect_code 200 "B 第 $i 次写"
done
agent_b PUT "/api/admin/agent/users/$B1" '{"remark":"it18-rate-4","isDisabled":false}' >/dev/null; expect_error 429 rate_limited "B 超出每小时写操作上限"
[ "$(hub GET "/api/admin/users/$B1" | jq -r .data.remark)" = "it18-rate-3" ] || fail "被限速的写不应生效：$(cat "$WORK/body")"
agent_b POST /api/admin/agent/invite-codes '{"maxUses":1,"isDisabled":false}' >/dev/null; expect_error 429 rate_limited "B 超限后换个写接口"
agent_b GET /api/admin/agent/summary >/dev/null; expect_code 200 "B 超限后读配额"
agent_b GET "/api/admin/agent/users/$B1" >/dev/null; expect_code 200 "B 超限后读用户"
agent_a PUT "/api/admin/agent/users/$A1" '{"remark":"it18 vip","isDisabled":false}' >/dev/null; expect_code 200 "B 超限不影响 A 的写"

# ---- 18. 停用 A：会话立即 401、A 的码不能注册；名下用户的个人码照常可用，新用户仍归 A（docs/03 §5.23 代理停用期间）----
hub PUT "/api/admin/agents/$A_ID" "$(agent_body "$A_MAX_USERS_RAISED" "$A_POOL_RAISED" "[$PLAN_ONCE]" true)" >/dev/null; expect_code 200 "停用代理 A"
agent_a GET /api/admin/agent/summary >/dev/null; expect_error 401 unauthenticated "停用后 A 的会话"
call "$WORK/a-relogin.jar" "" POST /api/admin/auth/login "$(login_body_of "$AGENT_A" "$AGENT_CRED")" >/dev/null; expect_error 401 unauthenticated "停用后 A 登录"
call "$WORK/r3.jar" "" POST /api/user/auth/register "$(register_body_of it18-r3 "$CODE_A")" >/dev/null; expect_code 400 "A 停用时用代理码注册（名下未满）"
call "$WORK/r3.jar" "" POST /api/user/auth/register "$(register_body_of it18-r3 "$R1_CODE")" >/dev/null; expect_code 200 "A 停用时用名下用户的个人码注册"
[ "$(admin_user_brief it18-r3)" = "[$A_ID,$R1_ID,null]" ] || fail "代理停用期间个人码邀请来的用户仍归 A：$(admin_user_brief it18-r3)"
[ "$(hub GET "/api/admin/users/$A1" | jq -c '[.data.agent.id, .data.isDisabled]')" = "[$A_ID,false]" ] || fail "停用代理不影响名下用户：$(cat "$WORK/body")"
[ "$(hub GET "/api/admin/agents/$A_ID" | jq -c '.data | [.isDisabled, .userCount, .trafficPoolUsed]')" = "[true,$A_MAX_USERS_RAISED,$USED_A]" ] \
  || fail "停用后的代理详情不对：$(cat "$WORK/body")"
echo "IT-18 PASS"
