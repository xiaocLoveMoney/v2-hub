# 前端不跑容器：本镜像只用于在干净环境里构建 dist（Ruling-6，产物发布到 MinIO）。
#   docker build --output type=local,dest=dist .
#   bash ../v2-hub-contract/deploy/publish_web.sh dist <桶名>
FROM node:24-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM scratch AS dist
COPY --from=build /app/dist/ /
