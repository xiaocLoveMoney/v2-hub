#!/usr/bin/env bash
# preflight:每个顶层模块目录都必须在 CODEOWNERS 里有归属,否则 MR 没人被自动指派、审批形同虚设。
set -euo pipefail
bad=0
for d in */; do
  d="${d%/}"
  case "$d" in docs|scripts|node_modules) continue ;; esac
  [ -f "$d/任务与边界.md" ] || continue          # 只管模块目录
  grep -qE "^/$d/" CODEOWNERS || { echo "CODEOWNERS 缺目录: /$d/"; bad=1; }
done
grep -q '@TODO' CODEOWNERS && echo "CODEOWNERS 仍有 @TODO,请项目经理填负责人" && bad=1
exit $bad
