#!/usr/bin/env bash
# 准备公告 / 教程图片上传用的 MinIO 桶与账号（Ruling-89），可重复执行。用法：
#   init_uploads.sh [compose 文件，默认 docker-compose.yml]
# 读同目录 .env 的 MINIO_ROOT_USER / MINIO_ROOT_PASSWORD 与 HUB_UPLOAD_USER / HUB_UPLOAD_PASSWORD；
# .env 里还没有上传账号的密码时（老环境），先补上 HUB_UPLOAD_USER=hub-upload 与随机密码（不打印）。然后：
#   - 桶 uploads：不存在则创建，设为匿名只读（download 只含 GetObject，不能列目录）
#   - 策略 hub-upload：只许往 uploads 写入对象
#   - 账号 HUB_UPLOAD_USER：建好（已存在则更新密码）并挂上该策略，hub-server 用它上传
set -euo pipefail
DEPLOY_DIR="$(cd "$(dirname "$0")" && pwd)"
COMPOSE_FILE="${1:-docker-compose.yml}"
MC_IMAGE="pgsty/mc:RELEASE.2026-10-26T00-00-00Z"
BUCKET="uploads"
POLICY="hub-upload"

cd "$DEPLOY_DIR"
[ -f .env ] || { echo "init_uploads: 缺 $DEPLOY_DIR/.env"; exit 1; }
if ! grep -q '^HUB_UPLOAD_PASSWORD=.' .env; then
  # 先删掉可能存在的空项再追加；不用 sed -i（GNU 与 BSD 写法不同）
  umask 077
  grep -v -e '^HUB_UPLOAD_USER=' -e '^HUB_UPLOAD_PASSWORD=' .env > .env.tmp || true
  printf 'HUB_UPLOAD_USER=hub-upload\nHUB_UPLOAD_PASSWORD=%s\n' "$(openssl rand -hex 16)" >> .env.tmp
  cat .env.tmp > .env && rm -f .env.tmp
  echo "init_uploads: 已在 .env 里生成上传账号"
fi
set -a && . ./.env && set +a
: "${MINIO_ROOT_USER:?}" "${MINIO_ROOT_PASSWORD:?}"
: "${HUB_UPLOAD_USER:?缺 HUB_UPLOAD_USER}" "${HUB_UPLOAD_PASSWORD:?缺 HUB_UPLOAD_PASSWORD}"

minio_container="$(docker compose -f "$COMPOSE_FILE" ps -q minio)"
[ -n "$minio_container" ] || { echo "init_uploads: minio 容器未运行"; exit 1; }

# 凭据经环境变量传给容器（-e 只写变量名），不出现在宿主机的 docker 命令行里。
export MC_HOST_hub="http://${MINIO_ROOT_USER}:${MINIO_ROOT_PASSWORD}@127.0.0.1:9000"
export HUB_UPLOAD_USER HUB_UPLOAD_PASSWORD
docker run --rm --network "container:$minio_container" -e MC_HOST_hub -e HUB_UPLOAD_USER -e HUB_UPLOAD_PASSWORD \
  --entrypoint sh "$MC_IMAGE" -c "
    set -e
    mc mb --ignore-existing hub/$BUCKET
    mc anonymous set download hub/$BUCKET
    cat > /tmp/policy.json <<'JSON'
{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":[\"s3:PutObject\"],\"Resource\":[\"arn:aws:s3:::$BUCKET/*\"]}]}
JSON
    mc admin policy create hub $POLICY /tmp/policy.json >/dev/null
    mc admin user add hub \"\$HUB_UPLOAD_USER\" \"\$HUB_UPLOAD_PASSWORD\" >/dev/null
    # 已挂过时 mc 报 already in effect，视为成功
    if ! out=\$(mc admin policy attach hub $POLICY --user \"\$HUB_UPLOAD_USER\" 2>&1); then
      echo \"\$out\" | grep -qi 'already' || { echo \"\$out\"; exit 1; }
    fi
  "
echo "init_uploads: 桶 $BUCKET 与账号 $HUB_UPLOAD_USER 已就绪"
