.PHONY: run test build lint sqlc admin-create

REPO_ROOT := $(abspath ..)
SQLC_IMAGE := sqlc/sqlc:1.31.1

# 开发库连接串：未显式设置 HUB_DATABASE_URL 时，从契约目录的 deploy/.env（init_env.sh 生成）拼出。
DEV_ENV_FILE := contract/deploy/.env
DEV_DB_URL = $$( [ -f $(DEV_ENV_FILE) ] && set -a && . ./$(DEV_ENV_FILE) && set +a && \
	echo "postgres://$$POSTGRES_USER:$$POSTGRES_PASSWORD@127.0.0.1:$$POSTGRES_PORT/$$POSTGRES_DB?sslmode=disable" )

run:
	@HUB_DATABASE_URL="$${HUB_DATABASE_URL:-$(DEV_DB_URL)}" go run ./cmd/hub -f etc/hub-server.yaml

# 本机有开发库时顺带跑真实数据库用例；CI 无 .env 时这些用例自动跳过。
test:
	@HUB_TEST_DATABASE_URL="$${HUB_TEST_DATABASE_URL:-$(DEV_DB_URL)}" go test ./... -race -count=1

build:
	CGO_ENABLED=0 go build -trimpath -o bin/hub ./cmd/hub

# 由 contract/deploy/init.sql + internal/dao/queries 生成 internal/dao/sqlcgen（生成物入库，Ruling-1）。
# 挂载仓库根，让 contract 符号链接在容器内也能解析。
sqlc:
	docker run --rm -v $(REPO_ROOT):/src -w /src/v2-hub-server $(SQLC_IMAGE) generate

lint:
	go vet ./...
	@if command -v golangci-lint >/dev/null; then golangci-lint run ./...; else echo "lint: golangci-lint 未安装, 跳过"; fi
