#!/usr/bin/env bash
# preflight：校验本次 push / PR 范围内的提交信息格式 `type(scope): 摘要` 与分支名 `<type>/<模块>-<主题>`。
# 纯 shell，不引入 commitlint。范围起点由工作流经 BASE_SHA 传入（PR 用 base sha，push 用 before）。
set -euo pipefail
TYPES='feat|fix|docs|chore|refactor|test|build|ci|merge'
ZERO_SHA='0000000000000000000000000000000000000000'
base="${BASE_SHA:-}"
if [ -z "$base" ] || [ "$base" = "$ZERO_SHA" ] || ! git cat-file -e "$base^{commit}" 2>/dev/null; then
  base="HEAD~1"
fi

bad=0
while IFS= read -r line; do
  [ -z "$line" ] && continue
  case "$line" in
    Merge\ *) continue ;;               # GitHub 自动生成的合并提交
  esac
  if ! printf '%s' "$line" | grep -qE "^($TYPES)(\([a-z0-9,-]+\))?: .+"; then
    echo "提交信息不合规: $line"
    bad=1
  fi
done < <(git log --format=%s "$base..HEAD" 2>/dev/null || git log --format=%s -1)

branch="${GITHUB_HEAD_REF:-${GITHUB_REF_NAME:-}}"
# tag 推送不是分支，不校验名字（工作流已只监听分支，这里兜底）
if [ "${GITHUB_REF_TYPE:-branch}" = "tag" ] && [ -z "${GITHUB_HEAD_REF:-}" ]; then
  branch=""
fi
if [ -n "$branch" ] && [ "$branch" != "main" ]; then
  if ! printf '%s' "$branch" | grep -qE '^(feat|fix|docs|chore|refactor|test|ci)/[a-z0-9]+-[a-z0-9-]+$'; then
    echo "分支名不合规: $branch（应为 <type>/<模块>-<主题>）"
    bad=1
  fi
fi
exit $bad
