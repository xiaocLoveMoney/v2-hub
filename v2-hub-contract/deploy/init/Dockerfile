# v2-hub-init：一次性初始化镜像（Ruling-109）。构建上下文是仓库根目录：
#   docker build -f v2-hub-contract/deploy/init/Dockerfile -t v2-hub-init .
# 内含两个前端的 dist、init.sql 与迁移；入口 init.sh 依次做迁移 → 上传桶与账号（Ruling-89）→ 发布前端到 MinIO（Ruling-6），完成即退出。
# 前端产物与架构无关，固定在构建机架构上构建；最终阶段按目标架构取 psql 与 mc。
ARG MC_IMAGE=pgsty/mc:RELEASE.2026-10-26T00-00-00Z

FROM --platform=$BUILDPLATFORM node:24-alpine AS admin-web
WORKDIR /app
COPY v2-hub-admin-web/package.json v2-hub-admin-web/package-lock.json ./
RUN npm ci
COPY v2-hub-admin-web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM node:24-alpine AS user-web
WORKDIR /app
COPY v2-hub-user-web/package.json v2-hub-user-web/package-lock.json ./
RUN npm ci
COPY v2-hub-user-web/ ./
RUN npm run build

FROM ${MC_IMAGE} AS mc

# 只要 psql / pg_isready 客户端，主版本与 compose 里的 postgres:17 一致
FROM alpine:3.23
RUN apk add --no-cache bash postgresql17-client \
    && adduser -D -H -u 10001 v2hub
COPY --from=mc /usr/bin/mc /usr/local/bin/mc
COPY v2-hub-contract/deploy/init.sql /opt/v2-hub/sql/init.sql
COPY v2-hub-contract/deploy/migrations/ /opt/v2-hub/sql/migrations/
COPY --from=admin-web /app/dist/ /opt/v2-hub/web/admin/
COPY --from=user-web /app/dist/ /opt/v2-hub/web/user/
COPY --chmod=0755 v2-hub-contract/deploy/init/init.sh /usr/local/bin/v2-hub-init
ENV MC_CONFIG_DIR=/tmp/.mc
USER 10001
ENTRYPOINT ["/usr/local/bin/v2-hub-init"]
