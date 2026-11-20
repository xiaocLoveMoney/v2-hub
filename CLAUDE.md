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
cd v2-hub-contract/deploy && bash init_env.sh && docker compose -f docker-compose.yml up -d --wait && bash reset_db.sh && bash init_uploads.sh
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
- 前端静态文件放 MinIO；生产单域名，管理端在 `/admin/`，Caddy 反代 `/api` 与订阅地址（Ruling-6、Ruling-61）。
- 生产自动部署：`main` 检查全绿后由生产机上的自托管 Runner 执行 `deploy_prod.sh`（Ruling-62，docs/02 §6）。

契约（全部在 v2-hub-contract/）：`hub-api.yaml`（server → 前端）、`deploy/init.sql`（server 的表结构）、
`upstream/3x-ui/openapi-v3.11.0.json`（3x-ui → server 的上游基准）。契约版本用 git tag `vX.Y`，当前 `v0.23`。

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
  新建入站预填路径、Host、SNI、证书路径与 REALITY 目标；补充（tag `v0.12`，Ruling-54）：入站表单的 REALITY 目标扫描与查找候选，由面板探测；补充（tag `v0.13`，Ruling-55）：平台拦截规则，系统设置 `routing.blockRule` 默认开，每台已接入服务器的 Xray 模板里加 `hub-block` 拦截条目；补充（tag `v0.14`，Ruling-56）：出口测试不通时返回原因大类，`http` / `real` 不通时补一次 `tcp` 探测；补充（tag `v0.15`，Ruling-57）：两端侧栏菜单顺序可在管理端“菜单顺序”页统一调整，管理端总览与系统设置改为卡片式布局；补充（tag `v0.16`，Ruling-58 – Ruling-60）：用户自设连接密码、订阅路径前缀改为随机且可改（旧前缀按天过渡）、每用户另存 SS2022 合法密钥；补充（tag `v0.17`，Ruling-61 – Ruling-64）：生产单域名与自托管 Runner 自动部署、节点默认名不带服务器名、新建优惠码可随机生成、平台出口可按节点组或指定用户分配；补充（tag `v0.18`，Ruling-65）：服务器位置（国家码，经面板公网 IP 查在线 IP 库）、节点地区跟随服务器、用户端国旗与节点在线 / 负载趋势图、仪表盘用量气泡图；补充（tag `v0.19`，Ruling-66、Ruling-67）：管理端总览加用户 / 流量 / 服务器 / 财务 / 待处理五个分析分区（ECharts 按需加载），大陆连通检测（经 Globalping 探测点判断节点是否被墙，默认关，令牌只走 `HUB_GLOBALPING_TOKEN`，连续 2 轮被墙才告警）；IT-12、IT-13 通过。真实代理出口上连通的测试结果、真实 Globalping 上的检测结果、两端 M10 页面登录后的浏览器实测待人工验证。
- M11 完成（2026-11-16，tag `v0.20`）：SSH 托管与面板部署（Ruling-68 – Ruling-71，docs/03 §5.22）：管理员保存 SSH 凭据（加密、只写不读）并添加主机（单台或批量），
  平台经 SSH 检测后安装面板或接管已有面板（只签发 `v2-hub` 令牌、不改其它配置），自动接入为服务器；之后可在平台上重启面板 / 内核、升级到基线版本、重签令牌、
  配置自签证书（`https` + `pin`）、查看状态与日志、重置面板登录口令。只有固定动作，没有任意命令；主机密钥首次连接后固定；权限码 `host:read` / `host:write`。
  部署抽象 `manager/deploy` 按面板类型注册，目前只有 3x-ui；SSH 执行器在 `manager/remote`。IT-14 通过（带 sshd 的 3x-ui 容器走接管全链路）。
  全新安装与升级（真实 systemd、从 GitHub 下载）、管理端 M11 页面登录后的浏览器实测未自动化，待人工验证。
  补充（tag `v0.21`，Ruling-72、Ruling-73）：面板的公开证书——管理端配一个 DNS 账号（目前只有 Cloudflare，令牌加密、只写不读、只在平台上使用），
  主机选了它并有域名时，平台建解析、经 ACME DNS-01 向 Let's Encrypt 申请证书、经 SSH 装到面板上，服务器改按 `https://域名` + `verify` 接入，
  没选 DNS 账号的主机仍用自签证书 + `pin`。DNS 抽象在 `manager/dns`。
  修订（tag `v0.22`，Ruling-74）：照 3x-ui 的做法改为在主机上用 acme.sh 签发——平台经 SSH 传固定版本的 acme.sh 与 `dns_cf` 钩子（快照在 `upstream/acme.sh/`），
  令牌经临时文件传入、用完即删，证书装到 `/root/cert/<域名>/`；没到续期时间就复用不重签，续期时间取 acme.sh 给出的（ARI），失败下一轮自动重试。
  实现在 `deploy/xui/cert.go`；真实主机测试（默认跳过，环境变量开启）在测试机上从全新系统走通，Let's Encrypt 测试与正式环境各签一次。
  真实的到期自动续期、管理端页面登录后的浏览器实测待人工验证。
  补充（tag `v0.23`，Ruling-75 – Ruling-77）：主机域名的解析只替换平台自己建的记录（别处建的冲突记录不动、任务失败）、带 DNS 账号的主机地址须是公网，支持在平台所在机器上托管它自己；
  服务器详情页“一键部署入站”：7 个预设（VLESS REALITY + Vision、同上加 ML-KEM、VLESS XHTTP + TLS、VMess WS + TLS、Trojan TLS、SS2022 aes-256、Hysteria2 + Salamander），
  作为主机任务 `deploy_inbounds`：先读主机监听端口避免冲突、要证书的先配置证书、建完连续确认 Xray 在跑（`xray_down`）、加进节点组；
  VLESS 流控按服务器开关（3x-ui 的 client flow 是全局的）；订阅支持 VLESS Encryption（mihomo / xray，sing-box 不输出）与 mihomo 的 XHTTP。
  实现在 `inboundtpl/presets.go`、`service/host_inbound_presets.go`；测试机上同机部署的平台给自己一键部署 7 项成功，7 个节点都实测出网（REALITY 两项在测试机上用 Xray 客户端测，本机网络按 SNI 干扰）。
  管理端一键部署弹窗登录后的浏览器实测、真实客户端导入待人工验证。
- 补充（Ruling-83 – Ruling-86，hub-api 0.16.0，migrations 0023）：服务器状态卡片——资源快照改为中立模型（`panel.Resources`），管理端服务器页默认卡片视图（CPU / 内存 / 磁盘 / 负载、实时速率、
  流量额度、三网延迟、在线与到期，可切表格），服务器可录入账单与流量额度（网卡计数按天累加、可校准）、详情页 24 小时 / 7 天趋势；三网延迟检测经 Globalping 按电信 / 联通 / 移动骨干网 AS 取点
  （`latency.enabled` 默认关，与连通检测共用额度、最多用一半）；IT-16 通过。编号顺延是因为未合并的 IP 质量检测分支已占用 Ruling-81 / 82、0.15.0、0022、IT-15。
  真实 Globalping 上三家探测点能否都取到、登录后的卡片页面浏览器实测待人工验证。
  修订（Ruling-88）：服务器列表分“管理 / 运行状态”两个页签，详情页分“入站与节点 / 运行状态 / 设置 / SSH 运维”四个页签，运行状态不再和日常管理挤在一页。
- 修订（Ruling-91）：用户端链式代理页的链路示意改为可拖动画布（React Flow）：起飞节点按落地分组排成网格，可平移、缩放、全屏、拖动换位（记在本浏览器），点节点或连线编辑相关规则。登录后的浏览器实测待人工验证。
- 路线图 M3–M8、M9（链式改造）、M10（出口补全与入站预填）与 M11（SSH 托管与面板部署）已完成；后续工作待定。
- 路线（2026-11-13，Ruling-17 – Ruling-22）：参考 SSPanel-Uim Malio 的功能与界面，自行实现；里程碑为
  M3 订阅与用户端基础 → M4 自助运营 → M5 商店与钱包 → M6 / M7 两种链式 → M8 运营，见 docs/07。
