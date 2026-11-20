.PHONY: run test build lint vuln sqlc gen-upstream admin-create

REPO_ROOT := $(abspath ..)
SQLC_IMAGE := sqlc/sqlc:1.31.1
GOVULNCHECK_VERSION := v1.9.0
# 联调可传 CONFIG=另一份配置（如缩短下发间隔）
CONFIG ?= etc/hub-server.yaml

# 开发库连接串：未显式设置 HUB_DATABASE_URL 时，从契约目录的 deploy/.env（init_env.sh 生成）拼出。
DEV_ENV_FILE := contract/deploy/.env
DEV_DB_URL = $$( [ -f $(DEV_ENV_FILE) ] && set -a && . ./$(DEV_ENV_FILE) && set +a && \
	echo "postgres://$$POSTGRES_USER:$$POSTGRES_PASSWORD@127.0.0.1:$$POSTGRES_PORT/$$POSTGRES_DB?sslmode=disable" )

DEV_ENC_KEY = $$( [ -f $(DEV_ENV_FILE) ] && set -a && . ./$(DEV_ENV_FILE) && set +a && echo "$$HUB_ENCRYPTION_KEY" )
# 图片上传（Ruling-89）：账号取自 contract/deploy/.env（init_uploads.sh 生成），MinIO 走宿主机端口
DEV_UPLOAD_USER = $$( [ -f $(DEV_ENV_FILE) ] && set -a && . ./$(DEV_ENV_FILE) && set +a && echo "$$HUB_UPLOAD_USER" )
DEV_UPLOAD_SECRET = $$( [ -f $(DEV_ENV_FILE) ] && set -a && . ./$(DEV_ENV_FILE) && set +a && echo "$$HUB_UPLOAD_PASSWORD" )
DEV_UPLOAD_ENDPOINT = $$( [ -f $(DEV_ENV_FILE) ] && set -a && . ./$(DEV_ENV_FILE) && set +a && echo "http://127.0.0.1:$${MINIO_API_PORT:-19000}" )

# 本机 HTTP 开发：关闭会话 Cookie 的 Secure（生产不设置该变量）；加密主密钥取自 contract/deploy/.env
run:
	@HUB_INSECURE_DEV_COOKIE=1 HUB_DATABASE_URL="$${HUB_DATABASE_URL:-$(DEV_DB_URL)}" \
		HUB_ENCRYPTION_KEY="$${HUB_ENCRYPTION_KEY:-$(DEV_ENC_KEY)}" \
		HUB_UPLOAD_USER="$${HUB_UPLOAD_USER:-$(DEV_UPLOAD_USER)}" HUB_UPLOAD_PASSWORD="$${HUB_UPLOAD_PASSWORD:-$(DEV_UPLOAD_SECRET)}" \
		HUB_UPLOAD_ENDPOINT="$${HUB_UPLOAD_ENDPOINT:-$(DEV_UPLOAD_ENDPOINT)}" go run ./cmd/hub -f $(CONFIG)

# 创建超级管理员：make admin-create USERNAME=root（密码在终端无回显输入，或经管道传入一行）
admin-create:
	@test -n "$(USERNAME)" || { echo "用法: make admin-create USERNAME=<用户名>"; exit 1; }
	@HUB_DATABASE_URL="$${HUB_DATABASE_URL:-$(DEV_DB_URL)}" go run ./cmd/hub admin-create -f $(CONFIG) -username "$(USERNAME)"

# 本机有开发库时顺带跑真实数据库用例（每个用例独立 schema，不影响开发数据）；无库时这些用例自动跳过。
test:
	@HUB_TEST_DATABASE_URL="$${HUB_TEST_DATABASE_URL:-$(DEV_DB_URL)}" go test ./... -race -count=1

build:
	CGO_ENABLED=0 go build -trimpath -o bin/hub ./cmd/hub

# 由 contract/deploy/init.sql + internal/dao/queries 生成 internal/dao/sqlcgen（生成物入库，Ruling-1）。
# 挂载仓库根，让 contract 符号链接在容器内也能解析。
sqlc:
	docker run --rm -v $(REPO_ROOT):/src -w /src/v2-hub-server $(SQLC_IMAGE) generate

# 把契约目录里的上游安装与升级脚本快照拷进 deploy/xui 供内嵌（生成物入库，Ruling-69）；升级基线版本时重跑并 review diff。
gen-upstream:
	cp contract/upstream/3x-ui/install-v*.sh contract/upstream/3x-ui/update-v*.sh internal/manager/deploy/xui/upstream/
	cp contract/upstream/acme.sh/acme-*.sh contract/upstream/acme.sh/dns_cf-*.sh internal/manager/deploy/xui/upstream/

# 扫描代码实际调用到的已知漏洞（标准库与依赖）；有可达漏洞时退出码非 0，CI 据此失败。
vuln:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

lint:
	go vet ./...
	@if command -v golangci-lint >/dev/null; then golangci-lint run ./...; else echo "lint: golangci-lint 未安装, 跳过"; fi
