#!/usr/bin/env bash
# 逐条跑 IT 用例，单条失败不中断整轮，结尾打印 PASS/FAIL/SKIP 表。
# 注意：会重置数据库，不要对着演示中的库跑。兼容 macOS 自带 bash 3.2（不用关联数组）。
# 判定（case_verdict）：退出码为 0 且输出里有该用例的结尾标记“IT-N PASS”（独占一行）才算 PASS。
# 只看退出码不够：bash 3.2 下脚本因 unbound variable 等中途退出时，EXIT trap 可能让退出码变成 0（docs/08 §1）。
set -uo pipefail
cd "$(dirname "$0")"

# case_verdict <编号> <退出码> <输出文件>：打印 PASS 或 FAIL
case_verdict() {
  local id="$1" status="$2" out="$3"
  if [ "$status" -eq 0 ] && grep -qx "$id PASS" "$out"; then
    echo PASS
  else
    echo FAIL
  fi
}

# 只加载判定函数、不跑用例（供单独校验判定逻辑）：IT_RUN_ALL_SOURCE_ONLY=1 source run_all.sh
if [ "${IT_RUN_ALL_SOURCE_ONLY:-}" = "1" ]; then
  return 0 2>/dev/null || exit 0
fi

bash ../reset_db.sh || { echo "reset_db 失败"; exit 1; }

RESULTS=""
fail=0
CASE_OUT="$(mktemp)"
trap 'rm -f "$CASE_OUT"' EXIT
run_case() {  # run_case <编号> <脚本>
  local id="$1" script="$2" result status
  if [ ! -f "$script" ]; then
    result=SKIP
  else
    # 输出先落文件再打印：不用管道接 tee，免得脚本留下的后台进程占着管道让本轮卡住
    bash "$script" >"$CASE_OUT" 2>&1
    status=$?
    cat "$CASE_OUT"
    result="$(case_verdict "$id" "$status" "$CASE_OUT")"
    if [ "$result" = FAIL ] && [ "$status" -eq 0 ]; then
      echo "run_all: $id 退出码为 0 但输出里没有“$id PASS”，按失败记（脚本可能中途退出）"
    fi
  fi
  [ "$result" != FAIL ] || fail=1
  RESULTS="${RESULTS}${id} ${result}
"
}

run_case IT-1 it1_smoke.sh
run_case IT-2 it2_admin_login.sh
run_case IT-3 it3_panel.sh
run_case IT-4 it4_users.sh
run_case IT-5 it5_subscription.sh
run_case IT-6 it6_self_service.sh
run_case IT-7 it7_shop.sh
run_case IT-8 it8_client_chain.sh
run_case IT-9 it9_server_chain.sh
run_case IT-10 it10_ops.sh
run_case IT-11 it11_chain_routes.sh
run_case IT-12 it12_exit_extras.sh
run_case IT-13 it13_analytics_reach.sh
run_case IT-14 it14_host_deploy.sh
run_case IT-16 it16_server_status.sh
run_case IT-17 it17_admin_tiers.sh
run_case IT-18 it18_agent.sh
# 新用例登记在这里，并在 docs/08-联调与测试方案.md 用例库加一行；脚本成功时最后一行打印“IT-N PASS”

echo
printf '%-8s %s\n' 用例 结果
printf '%s' "$RESULTS" | while read -r id result; do printf '%-8s %s\n' "$id" "$result"; done
exit $fail
