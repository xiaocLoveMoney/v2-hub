#!/usr/bin/env bash
# 拉取指定版本 3x-ui 的 OpenAPI 规范快照，作为 Provider(xui) 契约测试的上游基准（Ruling-7）；
# 同时拉取安装与升级脚本的快照，平台经 SSH 部署面板时执行的就是这两份（Ruling-69）。
# 用法：fetch.sh [tag]，默认 v3.11.0。升级支持的 3x-ui 版本时重新拉取并 review diff。
set -euo pipefail
TAG="${1:-v3.11.0}"
cd "$(dirname "$0")"
curl -fsSL "https://raw.githubusercontent.com/MHSanaei/3x-ui/${TAG}/docs/public/openapi.json" -o "openapi-${TAG}.json"
for script in install update; do
  curl -fsSL "https://raw.githubusercontent.com/MHSanaei/3x-ui/${TAG}/${script}.sh" -o "${script}-${TAG}.sh"
done
echo "fetch: openapi-${TAG}.json、install-${TAG}.sh、update-${TAG}.sh 已更新"
