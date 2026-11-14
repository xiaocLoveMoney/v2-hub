# CLAUDE.md

## 这是什么

多服务器 3x-ui 节点聚合与订阅分发平台：聚合各面板的入站，按权限派发给用户，提供统一订阅与链式代理。
仓库是 **monorepo**（GitHub，账号 xiaoclux），顶层目录按模块平铺，各模块目录下的 `contract` 是指向
`../v2-hub-contract` 的符号链接。

| 目录 | 技术栈 | 负责人 | 说明 |
|------|--------|--------|------|
| v2-hub-contract | OpenAPI / SQL / compose | 全体 | 契约目录：`hub-api.yaml`、`deploy/init.sql`、compose、IT 联调、3x-ui 上游快照 |
| v2-hub-server | Go 1.27 | @xiaoclux | 后端 |
| v2-hub-admin-web | React + TS + Vite | @xiaoclux | 管理门户 + 管理端 |
| v2-hub-user-web | React + TS + Vite | @xiaoclux | 客户门户 + 客户后台 |

## 常用命令

```bash
# 中间件（PostgreSQL :15432、MinIO :19000/:19001；加 --profile xui 另起两台 3x-ui）
cd v2-hub-contract/deploy && bash init_env.sh && docker compose -f docker-compose.yml up -d --wait && bash reset_db.sh
# 每个模块统一四个目标
cd v2-hub-<module> && make run | make test | make build | make lint
# 联调回归（脚本自起自停各模块进程，会重置数据库）
cd v2-hub-contract/deploy/it && bash run_all.sh
```

开发用的 PostgreSQL / MinIO 一律用 docker 起，不装本机服务；本机 5432/9000/9001 被其它项目占用，所以宿主机端口错开。

## 架构（跨模块大图）

- v2-hub 只做控制面，不转发代理流量（Ruling-4）。
- 面板是入站配置的事实来源；hub-server 缓存副本，定时同步并对账用户 client（docs/02 §3）。
- 业务层只依赖 `manager/panel` 的 Provider 接口与中立模型，3x-ui 是 `manager/panel/xui` 一个实现（Ruling-8）。
- 前端静态文件放 MinIO，Caddy 同域反代 `/api`、`/sub`（Ruling-5、Ruling-6）。

契约（全部在 v2-hub-contract/）：`hub-api.yaml`（server → 前端）、`deploy/init.sql`（server 的表结构）、
`upstream/3x-ui/openapi-v3.11.0.json`（3x-ui → server 的上游基准）。契约版本用 git tag `vX.Y`，当前 `v0.16`。

## 跨目录纪律

- 契约变更只发生在 `v2-hub-contract/`；破坏性变更在同一 PR 同步改引用方。
- 一条分支只碰一个模块目录；要改别的目录 = 契约缺口，记进《任务与边界》并开 issue。
- 业务语义以 docs/03 为准，**改动先改文档再改代码**；代码注释引用 `Ruling-N` / `T-N` 编号，不复述规则。
- 生成物入库，重生成后 review diff。
- 配置文件入库（`etc/`），凭据只在 `.env` / 环境变量 / GitHub Secrets。
- 提交格式 `type(scope): 中文摘要 —— 原因`，小步提交；合并用 merge commit。

## 开发文档（改代码前先读）

`docs/` 编号篇，索引在 `docs/README.md`。最小阅读集 = 01/02/03 + 自己模块那篇 + 自己目录的《任务与边界.md》。
GitHub 仓库设置见 `docs/github-仓库设置清单.md`。

## 当前完成度

- M0 完成（2026-11-12，tag `v0.1`）：管理员登录 / 会话 / CSRF / 登录频控、RBAC（R1–R7）与管理员 / 角色 / 审计日志接口和页面、
  TOTP 两步验证（AES-256-GCM 加密，主密钥 `HUB_ENCRYPTION_KEY`）、`make admin-create`；IT-1、IT-2 通过。
- M1 完成（2026-11-12，tag `v0.2`）：面板 Provider 抽象与 3x-ui 实现、服务器接入 / 探测 / 同步、入站增删改查
  （表单与 3x-ui 同构，Ruling-16）、Host 对外地址、节点目录与节点组、漂移与面板标识复用处理；IT-3 通过。
- M2 完成（2026-11-13，tag `v0.3`）：套餐与用户（额度模型照 3x-ui：到期、首次使用起算、周期流量重置）、
  对账器（每用户在每台面板一个 client，失败重试）、流量汇总、超额 / 到期自动禁用、管理员直接改用量；IT-4 通过。
- M3 完成（2026-11-13，tag `v0.4`）：终端用户登录 / 会话 / 两步验证、订阅（`/sub/{token}`，Base64 / Mihomo / sing-box /
  Xray JSON，节点参数取自面板分享链接）、重置订阅即轮换连接凭据、客户后台（仪表盘、节点、账号、订阅记录，自写设计系统）、
  管理端换肤；IT-5 通过。真实客户端导入连通未自动化，待人工验证。
- M4 完成（2026-11-13，tag `v0.5`）：系统设置表、邮件发件箱与邮箱验证码（SMTP 只走 `HUB_SMTP_*` 环境变量）、自助注册（开放 / 仅邀请 / 关闭）
  与邀请码、邮箱登录、找回密码、换绑邮箱、注销账号、公告（Markdown、弹窗、群发）、教程、共享账号、工单、签到；两端页面齐备；IT-6 通过。
  真实 SMTP 投递与管理端 M4 页面的浏览器实测未自动化，待人工验证。
- M5 完成（2026-11-13，tag `v0.6`）：金额按整数分、购买一律扣余额（Ruling-28 – Ruling-35）；套餐时长选项、流量包、试用、优惠码、订单、
  余额流水、充值码、管理员调整余额、到期扣余额自动续费与提醒邮件、邀请返利、支付网关接口（幂等入账，不带具体网关）、收支统计、
  切换为用户身份（一次性票据换只读会话）；两端页面齐备；IT-7 通过。
  真实支付网关、真实 SMTP 投递与管理端 M4 / M5 页面的浏览器实测未自动化，待人工验证。
- M6 完成（2026-11-13，tag `v0.7`）：客户端链式（Ruling-36 – Ruling-38）：链式规则（入口 → 落地，通畅 / 阻断按套餐与节点可用性计算）、
  自有落地节点（分享链接或 SOCKS5，加密存储，只能作落地）；Mihomo / sing-box / Xray JSON 订阅带两跳链路，Base64 不带；用户端链式代理页；IT-8 通过。
  真实流量经落地出网（出口 IP）未自动化，待人工验证。
- M7 完成（2026-11-14，tag `v0.8`）：服务端链式（Ruling-39 – Ruling-41）：用户自助配置“入口 → 落地”，平台在入口面板 Xray 模板里增删 `hub-r{id}`
  出站与按入站 + 用户匹配的路由；以入口服务器为单位整体对账，加密备份、重启后确认运行、失败回滚与退避；管理端看下发状态与重试；IT-9 通过。
  真实外网流量经落地出网与管理端 M7 页面的浏览器实测未自动化，待人工验证。
- M8 完成（2026-11-14，tag `v0.9`）：运营（Ruling-42 – Ruling-45）：管理端总览（按权限出区块）、服务器掉线告警与恢复通知（邮件 + Telegram，
  Bot token 只走 `HUB_TELEGRAM_BOT_TOKEN`）、运营日报、落地页与公开套餐、客服入口、收支按月与 CSV 导出；用户端中英双语（自写 i18n，`src/i18n/`）；IT-10 通过。
  真实 Telegram / SMTP 投递、英文界面人工通读与管理端 M8 页面的浏览器实测未自动化，待人工验证。
- M9 完成（2026-11-14，tag `v0.10`）：链式改造（Ruling-46 – Ruling-50）：规则改为有序的“起飞节点（多选）→ 落地（单选）”，服务端同一起飞入站取最靠前的可用规则、
  客户端每个组合都进订阅；落地可选平台节点、平台出口（管理员添加，挂节点组授权）或用户出口；用户出口可用于服务端链式，
  配套防护：只许公网地址并固定 IP、字段白名单、防环、每用户写操作频控、下发失败隔离；管理端可管理全部出口；IT-11 通过。
  真实流量的出口 IP、两端 M9 页面登录后的浏览器实测未自动化，待人工验证。
- M10 完成（2026-11-14，tag `v0.11`）：出口补全与入站预填（Ruling-51 – Ruling-53）：出口支持 HTTP / HTTPS 代理；出口延迟测试（TCP、HTTP、真实延迟），
  手动发起、由起飞面板执行，用户出口先过服务端链式的地址限制，按用户频控、按服务器互斥；出口弹窗改为“表单 / 分享链接”两种填法；
  新建入站预填路径、Host、SNI、证书路径与 REALITY 目标；补充（tag `v0.12`，Ruling-54）：入站表单的 REALITY 目标扫描与查找候选，由面板探测；补充（tag `v0.13`，Ruling-55）：平台拦截规则，系统设置 `routing.blockRule` 默认开，每台已接入服务器的 Xray 模板里加 `hub-block` 拦截条目；补充（tag `v0.14`，Ruling-56）：出口测试不通时返回原因大类，`http` / `real` 不通时补一次 `tcp` 探测；补充（tag `v0.15`，Ruling-57）：两端侧栏菜单顺序可在管理端“菜单顺序”页统一调整，管理端总览与系统设置改为卡片式布局；补充（tag `v0.16`，Ruling-58 – Ruling-60）：用户自设连接密码、订阅路径前缀改为随机且可改（旧前缀按天过渡）、每用户另存 SS2022 合法密钥；IT-12 通过。真实代理出口上连通的测试结果、两端 M10 页面登录后的浏览器实测待人工验证。
- 路线图 M3–M8、M9（链式改造）与 M10（出口补全与入站预填）已完成；后续工作待定。
- 路线（2026-11-13，Ruling-17 – Ruling-22）：参考 SSPanel-Uim Malio 的功能与界面，自行实现；里程碑为
  M3 订阅与用户端基础 → M4 自助运营 → M5 商店与钱包 → M6 / M7 两种链式 → M8 运营，见 docs/07。
