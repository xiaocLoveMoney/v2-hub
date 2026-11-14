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
│   │   ├── chain/               # 服务端链式的面板配置变更（M7）；客户端链式的渲染在 render/（M6）；M9 起一条规则可带多个入站
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
// 可选能力，服务端链式需要（Capabilities.CoreConfig 为真时可断言出来，docs/03 §5.19）
type CoreConfigurator interface { GetCoreTemplate / SetCoreTemplate / RestartCore / CoreState }
```

中立模型只收敛公共字段；面板专有配置以“原始 JSON + provider/内核类型标记”携带。

## 3. 关键不变式（违背即返工）

1. 面板调用一律经 Provider，并发按服务器限流（每台同时最多 N 个请求，N 在配置里）。
2. 面板 token、用户凭据入库前 AES-GCM 加密，密钥来自环境变量；日志不打印分享链接与 token。
3. 批量禁用 / 删除 client 合并成一次调用：3x-ui 默认禁用 client 会重启 Xray。
4. 所有写面板的操作写审计日志。

M9 链式改造（docs/03 §5.18、§5.19、§5.21）在 `internal/service` 里的分工：`chain.go` 管规则与用户出口的增删改与调顺序，
`chain_resolve.go` 是生效计算的纯函数（哪条规则在哪个起飞入站上生效），`chain_guard.go` 管用户出口用于服务端链式的防护
（公网地址判定、域名解析与固定 IP、字段白名单、防环；DNS 解析走可注入的接口，单测不发真实查询），
`chain_exit_admin.go` 是管理端的出口管理，`chain_apply.go` 的期望条目改由生效计算的结果生成并带失败隔离。

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
- 首个超管：`make admin-create USERNAME=<名称>`，密码终端无回显输入或经管道传入一行。
- `make run / test / build / lint`。

## 6. 测试策略

- 单测：表驱动；`api` 用 `httptest`；`service` 用内存 fake Provider。
- 契约测试：`manager/panel/xui` 用 `httptest` 模拟面板，请求 / 响应形状对照上游快照。
- 集成：IT 用例起真实 3x-ui 容器（`--profile xui`）。

M10 的做法（Ruling-51 – Ruling-53）：`manager/panel` 加两个可选能力接口——`OutboundTester`（在面板上测一个出站）与 `CertLocator`（面板自己的网页证书路径），
业务层用类型断言判断面板支不支持，3x-ui 实现分别对应 `/panel/api/xray/testOutbound` 与 `/panel/api/server/getWebCertFiles`。
出口测试在 `service/chain_exit_test_run.go`：先做归属与地址限制（复用 `chain_guard.go`），再按用户频控、按服务器互斥，出站 JSON 由 `render.XrayOutbound` 生成；
本文件不写日志（出站含凭据）。HTTP / HTTPS 出口只是 `manager/render` 多一个协议。
不通的原因大类（Ruling-56）分两层：`manager/panel` 的 `OutboundTestResult.Failure` 是面板适配层对一次探测的归类（连接被拒、超时、被断开、其他），3x-ui 实现按面板返回的错误文本归类（文本是面板的，只能匹配，关键字集中在 `xui/probe.go` 的常量里）；`service` 把它与方式、HTTP 状态合成对外的 `reason`，`http` / `real` 不通且没有 HTTP 状态时在同一把服务器锁里补一次 `tcp` 探测。
REALITY 目标扫描（Ruling-54）是第三个可选能力 `RealityScanner`，3x-ui 实现对应 `/panel/api/server/scanRealityTarget(s)`；目标的形状在 service 层校验（不收网段），从不带 `allowPrivate`。
平台拦截规则（Ruling-55）的条目由 `manager/chain` 生成（`BlockTag = "hub-block"`，域名表是包内常量），`ChainApplier` 读设置 `routing.blockRule` 决定期望里带不带它；开着时 `ListChainApplyServerIDs` 返回全部启用中的服务器，关掉后有下发记录的服务器照常检查一轮以撤掉条目。

服务器位置与负载采样（Ruling-65）：`panel.PanelInfo` 加 `PublicIP`（面板自报的公网 IP，3x-ui 取 `/server/status` 的 `publicIP`，优先 IPv4，`N/A` 视为没有）；
IP 换国家在 `internal/manager/geoip`：`Locator` 接口 + HTTP 实现，查询地址是配置 `geoip.lookupURL`（含 `{ip}` 占位，默认 `https://api.country.is/{ip}`，留空即关闭扫描），
响应取 JSON 的 `country`，只认 ISO 3166-1 两位码（码表是包内常量，`geoip.IsCountryCode` 同时供 service 校验管理员的选择）；只把公网单播地址发出去。
`PanelService.LocateServer` 经 Provider 探测拿 IP 再查库，新建服务器时位置留空就用接入探测拿到的 IP 顺手查一次（失败不影响接入），
启动时 `job.RunServerLocate` 在后台对位置为空的启用服务器各扫一次（单次、自带 recover）。
健康探测成功后 `ProbeServer` 写一条 `server_metric`（`date_bin` 对齐 5 分钟，冲突时覆盖为最新值，在线数取 `server.online_clients`），过期采样由清理任务按 7 天删除；
用户端趋势图走 `UserService.ListNodeMetrics`：先按本人节点列表的同一套连接条件确认节点可见，再按范围取 5 分钟原值或按小时平均。

## 7. 接口待补 / 契约缺口

- 见《任务与边界》契约缺口节。
