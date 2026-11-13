#!/usr/bin/env bash
# 逐条跑 IT 用例，单条失败不中断整轮，结尾打印 PASS/FAIL/SKIP 表。
# 注意：会重置数据库，不要对着演示中的库跑。兼容 macOS 自带 bash 3.2（不用关联数组）。
set -uo pipefail
cd "$(dirname "$0")"
bash ../reset_db.sh || { echo "reset_db 失败"; exit 1; }

RESULTS=""
fail=0
run_case() {  # run_case <编号> <脚本>
  local id="$1" script="$2" result
  if [ ! -f "$script" ]; then
    result=SKIP
  elif bash "$script"; then
    result=PASS
  else
    result=FAIL
    fail=1
  fi
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
# 新用例登记在这里，并在 docs/08-联调与测试方案.md 用例库加一行

echo
printf '%-8s %s\n' 用例 结果
printf '%s' "$RESULTS" | while read -r id result; do printf '%-8s %s\n' "$id" "$result"; done
exit $fail
