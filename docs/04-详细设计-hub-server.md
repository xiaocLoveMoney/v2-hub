# 04 · 详细设计 · hub-server

> 负责人：@xiaoclux。职责：v2-hub 唯一后端。契约：`hub-api.yaml`、`deploy/init.sql`、`upstream/3x-ui/`。

## 1. 目录结构

```
v2-hub-server/
├── cmd/hub/main.go              # 入口：读配置、起 HTTP、优雅退出
├── etc/hub-server.yaml          # 配置（入库）
├── internal/
│   ├── api/                     # Web 层：路由、校验、鉴权、CSRF、限流、响应信封
│   ├── service/                 # 业务（M0 起）
│   ├── manager/
│   │   ├── panel/               # Provider 接口、Capabilities、中立模型、注册表（M1）
│   │   ├── panel/xui/           # 3x-ui 实现（M1）
│   │   ├── render/              # 分享链接解析 → ProxyNode → 各订阅格式（M3）
│   │   ├── chain/               # 服务端链式的面板配置变更（M7）；客户端链式的渲染在 render/（M6）
│   │   └── crypto/              # AES-GCM、argon2id、token 生成（M0）
│   ├── dao/                     # PostgreSQL 访问（sqlc 生成，参数化）
│   ├── domain/{entity,dto,vo}/  # 领域模型分层
│   ├── job/                     # 健康检查、同步、流量汇总、对账
│   └── config/                  # 配置加载与校验
└── Makefile / Dockerfile
```

目前已有：`cmd/hub`（服务 + `admin-create` 子命令）、`internal/api`（探活、管理员认证与 RBAC 接口、会话 / CSRF / 权限中间件）、
`internal/service`（AuthService、RBACService）、`internal/manager/{crypto,ratelimit}`、`internal/dao`（连接池、sqlc 生成物、daotest）、
`internal/job`（过期会话清理、面板探测与同步）、`internal/manager/{panel,panel/xui,panel/paneltest,inboundtpl}`、`internal/config`。其余目录在对应任务里创建，不预建空目录。

## 2. 模块划分与依赖方向

`api → service → manager → dao`，禁止反向；`job` 与 `api` 同级，只调 `service`。
`service` 只依赖 `manager/panel` 的接口，具体实现在 `cmd/hub` 组装时注入（Ruling-8）。

### Provider 接口（M1 落地，形状已在规划中钉死）

```go
type Provider interface {
    Probe(ctx) (PanelInfo, error)
    Capabilities() Capabilities
    Status(ctx) (ServerStatus, error)
    ListInbounds / GetInbound / CreateInbound / UpdateInbound / DeleteInbound / SetInboundEnabled
    ListEndpoints(ctx, inboundRef)
    UpsertClient / DeleteClient / SetClientsEnabled / AttachClient / DetachClient
    ListClientTraffic / ListOnlineClients
    ClientShareLinks(ctx, clientRef) ([]string, error)
}
// 可选能力，服务端链式需要
type CoreConfigurator interface { GetCoreConfig / ApplyCoreConfig / RestartCore / TestOutbound }
```

中立模型只收敛公共字段；面板专有配置以“原始 JSON + provider/内核类型标记”携带。

## 3. 关键不变式（违背即返工）

1. 面板调用一律经 Provider，并发按服务器限流（每台同时最多 N 个请求，N 在配置里）。
2. 面板 token、用户凭据入库前 AES-GCM 加密，密钥来自环境变量；日志不打印分享链接与 token。
3. 批量禁用 / 删除 client 合并成一次调用：3x-ui 默认禁用 client 会重启 Xray。
4. 所有写面板的操作写审计日志。

## 4. 与其它模块的握手

| 对方 | 方向 | 协议 / 端口 | 契约文件 |
|---|---|---|---|
| admin-web / user-web | 被调 | HTTP 8080（生产经 Caddy） | `hub-api.yaml` |
| 3x-ui 面板 | 主调 | HTTPS + Bearer | `upstream/3x-ui/openapi-v3.11.0.json` |
| PostgreSQL | 主调 | 5432 | `deploy/init.sql` |
| MinIO | 主调 | S3 API 9000 | —（上传文件，M6） |

## 5. 配置与启动

- `etc/hub-server.yaml`：监听地址、超时（已有）；数据库、轮询间隔、并发上限随任务补。
- 环境变量：`HUB_DATABASE_URL`；`HUB_INSECURE_DEV_COOKIE=1` 仅本机 HTTP 开发（`make run` 自动设置）；`HUB_ENCRYPTION_KEY`（Base64 的 32 字节，AES-256-GCM 主密钥，加密 TOTP 密钥，M1 起也加密面板 token；缺失则拒绝启动）。
- 首个超管：`make admin-create USERNAME=<名称>`，口令终端无回显输入或经管道传入一行。
- `make run / test / build / lint`。

## 6. 测试策略

- 单测：表驱动；`api` 用 `httptest`；`service` 用内存 fake Provider。
- 契约测试：`manager/panel/xui` 用 `httptest` 模拟面板，请求 / 响应形状对照上游快照。
- 集成：IT 用例起真实 3x-ui 容器（`--profile xui`）。

## 7. 接口待补 / 契约缺口

- 见《任务与边界》契约缺口节。
