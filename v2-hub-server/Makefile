.PHONY: run test build lint

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

lint:
	go vet ./...
	@if command -v golangci-lint >/dev/null; then golangci-lint run ./...; else echo "lint: golangci-lint 未安装, 跳过"; fi
