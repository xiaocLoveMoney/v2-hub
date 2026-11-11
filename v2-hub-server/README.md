# v2-hub-server

v2-hub 唯一后端：管理端 / 客户端 API、订阅端点、面板聚合与下发任务。技术栈 Go 1.27（标准库 `net/http` + `log/slog`），端口 8080。

```bash
make run    # 起服务（配置 etc/hub-server.yaml；凭据从环境变量读）
make test   # 单测（-race）
make build  # 产出 bin/hub
make lint   # go vet；装了 golangci-lint 时一并执行
```

目录分层：`cmd/hub`（入口）→ `internal/api`（Web 层）→ `internal/service` → `internal/manager` → `internal/dao`。

契约：`contract/` → `../v2-hub-contract`。任务与边界见《任务与边界.md》；设计见 `docs/04-详细设计-hub-server.md`。
