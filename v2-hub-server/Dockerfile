# 多阶段构建：编译阶段用官方 Go 镜像，运行阶段用 distroless 非 root。
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/hub ./cmd/hub

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/hub /app/hub
COPY etc/ /app/etc/
USER 65532:65532
EXPOSE 8080
# distroless 无 shell/curl，健康检查由 compose.deploy.yml 经 Caddy 或外部探活完成
ENTRYPOINT ["/app/hub", "-f", "/app/etc/hub-server.yaml"]
