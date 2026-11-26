# 多阶段构建：编译阶段用官方 Go 镜像，运行阶段用 distroless 非 root。
# Go 补丁版本与 go.mod 的 go 指令保持一致，本机、CI、镜像用同一个标准库；升级由 make vuln 的结果驱动。
# 编译阶段固定在构建机架构上交叉编译（纯 Go、CGO 关闭），出 arm64 镜像时不走 QEMU 模拟编译（Ruling-109）。
FROM --platform=$BUILDPLATFORM golang:1.27.4 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/hub ./cmd/hub

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/hub /app/hub
COPY etc/ /app/etc/
USER 65532:65532
EXPOSE 8080
# distroless 无 shell/curl，健康检查由 compose.deploy.yml 经 Caddy 或外部探活完成
ENTRYPOINT ["/app/hub", "-f", "/app/etc/hub-server.yaml"]
