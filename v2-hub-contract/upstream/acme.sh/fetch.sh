#!/usr/bin/env bash
# 拉取指定版本 acme.sh 与平台用到的 DNS 钩子的快照：平台经 SSH 把它们传到托管主机上签发面板证书（Ruling-74），
# 不在主机上 curl get.acme.sh。用法：fetch.sh [tag]，默认 3.1.6。升级时重新拉取并 review diff，再改 hub-server 里引用的文件名。
set -euo pipefail
TAG="${1:-3.1.6}"
cd "$(dirname "$0")"
curl -fsSL "https://raw.githubusercontent.com/acmesh-official/acme.sh/${TAG}/acme.sh" -o "acme-${TAG}.sh"
for hook in dns_cf; do
  curl -fsSL "https://raw.githubusercontent.com/acmesh-official/acme.sh/${TAG}/dnsapi/${hook}.sh" -o "${hook}-${TAG}.sh"
done
echo "fetch: acme-${TAG}.sh、dns_cf-${TAG}.sh 已更新"
