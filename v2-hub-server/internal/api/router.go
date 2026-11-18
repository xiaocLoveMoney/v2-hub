// Package api 是 hub-server 的 Web 层：路由注册、参数基础校验、鉴权与响应编码。
// 业务逻辑放 service 层，这里只做转发（见 .claude/rules/architecture.md）。
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"v2-hub-server/internal/service"
)

const (
	headerContent     = "Content-Type"
	headerNoSniff     = "X-Content-Type-Options"
	headerCache       = "Cache-Control"
	contentTypeJSON   = "application/json; charset=utf-8"
	statusOK          = "ok"
	statusUnavailable = "unavailable"
	apiPrefix         = "/api/"
)

// Pinger 能探测某个依赖是否可用，*pgxpool.Pool 天然满足。
type Pinger interface {
	Ping(ctx context.Context) error
}

// Deps 是 Web 层需要的外部依赖，字段均不可为零值。
type Deps struct {
	// Database 用于 /readyz 探测数据库。
	Database Pinger
	// PingTimeout 是单次就绪探测的超时。
	PingTimeout time.Duration
	// Auth 提供管理员登录与会话校验。
	Auth AdminAuthenticator
	// RBAC 提供管理员、角色、权限码与审计日志管理。
	RBAC AdminManager
	// Panels 提供服务器、入站、节点管理（docs/03 §5.1）。
	Panels PanelManager
	// Users 提供套餐、用户与下发管理（docs/03 §5.2）。
	Users UserManager
	// PortalAuth 提供终端用户登录与会话校验（docs/03 §5.3）。
	PortalAuth PortalAuthenticator
	// Portal 提供客户后台数据与订阅（docs/03 §5.4、§5.5）。
	Portal PortalManager
	// Settings 提供系统设置（docs/03 §5.6）。
	Settings SettingsManager
	// Mail 提供发件箱查询与测试邮件（docs/03 §5.7）。
	Mail MailManager
	// Account 提供站点信息、邮箱验证码与账号自助（docs/03 §5.6–§5.8）。
	Account AccountManager
	// Content 提供公告、教程与共享账号（docs/03 §5.9）。
	Content ContentManager
	// Tickets 提供工单（docs/03 §5.10）。
	Tickets TicketManager
	// Wallet 提供余额流水与充值码（docs/03 §5.14、§5.15）。
	Wallet WalletManager
	// Shop 提供流量包、优惠码、询价、下单、订单与钱包总览（docs/03 §5.12、§5.13、§5.16）。
	Shop ShopManager
	// Finance 提供收支统计与返利列表（docs/03 §5.16）。
	Finance FinanceManager
	// Pay 提供在线充值单与支付回调（docs/03 §5.15）。
	Pay PayManager
	// Chain 提供用户侧的链式规则与出口（docs/03 §5.18、§5.19、§5.21）。
	Chain ChainManager
	// ChainApply 提供服务端链式的下发状态与重试（docs/03 §5.19）。
	ChainApply ChainApplyManager
	// ChainAdmin 提供管理端的全站规则列表与出口管理（docs/03 §5.21）。
	ChainAdmin ChainAdminManager
	// Telegram 提供 Telegram 测试消息，Ops 提供管理端总览（docs/03 §5.20）。
	Telegram TelegramManager
	Ops      OpsManager
	// Analytics 提供总览的分析分区（docs/03 §5.20 总览分析，Ruling-66）。
	Analytics AnalyticsManager
	// Reach 提供大陆连通检测的结论与手动检测（Ruling-67）；为 nil 时节点与服务器不带结论。
	Reach ReachManager
	// Hosts 提供 SSH 凭据、托管主机与主机任务（docs/03 §5.22）。
	Hosts HostManager
	// Cookie 是会话 Cookie 属性。
	Cookie CookieConfig
	// TrustedProxies 是可采信 X-Forwarded-For 的直连网段（如 Caddy 所在 docker 网络）。
	TrustedProxies []netip.Prefix
}

type handlers struct {
	auth           AdminAuthenticator
	rbac           AdminManager
	panels         PanelManager
	users          UserManager
	portalAuth     PortalAuthenticator
	portal         PortalManager
	settings       SettingsManager
	mail           MailManager
	account        AccountManager
	content        ContentManager
	tickets        TicketManager
	wallet         WalletManager
	shop           ShopManager
	finance        FinanceManager
	pay            PayManager
	chain          ChainManager
	chainApply     ChainApplyManager
	chainAdmin     ChainAdminManager
	telegram       TelegramManager
	ops            OpsManager
	analytics      AnalyticsManager
	reach          ReachManager
	hosts          HostManager
	cookie         CookieConfig
	trustedProxies []netip.Prefix
}

// NewRouter 返回注册好全部路由的 http.Handler。路由与 hub-api.yaml 的 paths 逐条对应。
func NewRouter(deps Deps) http.Handler {
	h := &handlers{auth: deps.Auth, rbac: deps.RBAC, panels: deps.Panels, users: deps.Users, portalAuth: deps.PortalAuth, portal: deps.Portal,
		settings: deps.Settings, mail: deps.Mail, account: deps.Account, content: deps.Content, tickets: deps.Tickets, wallet: deps.Wallet, shop: deps.Shop, finance: deps.Finance, pay: deps.Pay, chain: deps.Chain, chainApply: deps.ChainApply, chainAdmin: deps.ChainAdmin, telegram: deps.Telegram, ops: deps.Ops, analytics: deps.Analytics, reach: deps.Reach, hosts: deps.Hosts, cookie: deps.Cookie, trustedProxies: deps.TrustedProxies}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /readyz", newReadyzHandler(deps.Database, deps.PingTimeout))

	mux.HandleFunc("POST /api/admin/auth/login", h.login)
	mux.HandleFunc("POST /api/admin/auth/logout", h.requireAdmin("", h.logout))
	mux.HandleFunc("GET /api/admin/auth/me", h.requireAdmin("", h.me))
	mux.HandleFunc("POST /api/admin/auth/totp/setup", h.requireAdmin("", h.totpSetup))
	mux.HandleFunc("POST /api/admin/auth/totp/enable", h.requireAdmin("", h.totpEnable))
	mux.HandleFunc("POST /api/admin/auth/totp/disable", h.requireAdmin("", h.totpDisable))

	mux.HandleFunc("GET /api/admin/permissions", h.requireAdmin(service.PermAdminRead, h.listPermissions))
	mux.HandleFunc("GET /api/admin/roles", h.requireAdmin(service.PermAdminRead, h.listRoles))
	mux.HandleFunc("POST /api/admin/roles", h.requireAdmin(service.PermAdminWrite, h.createRole))
	mux.HandleFunc("PUT /api/admin/roles/{id}", h.requireAdmin(service.PermAdminWrite, h.updateRole))
	mux.HandleFunc("DELETE /api/admin/roles/{id}", h.requireAdmin(service.PermAdminWrite, h.deleteRole))
	mux.HandleFunc("GET /api/admin/admins", h.requireAdmin(service.PermAdminRead, h.listAdmins))
	mux.HandleFunc("POST /api/admin/admins", h.requireAdmin(service.PermAdminWrite, h.createAdmin))
	mux.HandleFunc("PUT /api/admin/admins/{id}", h.requireAdmin(service.PermAdminWrite, h.updateAdmin))
	mux.HandleFunc("PUT /api/admin/admins/{id}/password", h.requireAdmin(service.PermAdminWrite, h.resetAdminCredential))
	mux.HandleFunc("DELETE /api/admin/admins/{id}/totp", h.requireAdmin(service.PermAdminWrite, h.resetAdminTOTP))
	mux.HandleFunc("GET /api/admin/audit-logs", h.requireAdmin(service.PermAuditRead, h.listAuditLogs))

	mux.HandleFunc("GET /api/admin/servers", h.requireAdmin(service.PermServerRead, h.listServers))
	mux.HandleFunc("POST /api/admin/servers", h.requireAdmin(service.PermServerWrite, h.createServer))
	mux.HandleFunc("POST /api/admin/servers/test", h.requireAdmin(service.PermServerWrite, h.testServer))
	mux.HandleFunc("POST /api/admin/servers/cert-fingerprint", h.requireAdmin(service.PermServerWrite, h.certFingerprint))
	mux.HandleFunc("GET /api/admin/servers/{id}", h.requireAdmin(service.PermServerRead, h.getServer))
	mux.HandleFunc("PUT /api/admin/servers/{id}", h.requireAdmin(service.PermServerWrite, h.updateServer))
	mux.HandleFunc("DELETE /api/admin/servers/{id}", h.requireAdmin(service.PermServerWrite, h.deleteServer))
	mux.HandleFunc("PUT /api/admin/servers/{id}/client-flow", h.requireAdmin(service.PermServerWrite, h.setServerClientFlow))
	mux.HandleFunc("POST /api/admin/servers/{id}/sync", h.requireAdmin(service.PermServerWrite, h.syncServer))
	mux.HandleFunc("GET /api/admin/servers/{id}/chain-apply", h.requireAdmin(service.PermServerRead, h.getServerChainApply))
	mux.HandleFunc("POST /api/admin/servers/{id}/chain-apply/retry", h.requireAdmin(service.PermServerWrite, h.retryServerChainApply))
	mux.HandleFunc("GET /api/admin/ssh-credentials", h.requireAdmin(service.PermHostRead, h.listSSHCredentials))
	mux.HandleFunc("POST /api/admin/ssh-credentials", h.requireAdmin(service.PermHostWrite, h.createSSHCredential))
	mux.HandleFunc("PUT /api/admin/ssh-credentials/{id}", h.requireAdmin(service.PermHostWrite, h.updateSSHCredential))
	mux.HandleFunc("DELETE /api/admin/ssh-credentials/{id}", h.requireAdmin(service.PermHostWrite, h.deleteSSHCredential))
	mux.HandleFunc("GET /api/admin/dns-accounts", h.requireAdmin(service.PermHostRead, h.listDNSAccounts))
	mux.HandleFunc("POST /api/admin/dns-accounts", h.requireAdmin(service.PermHostWrite, h.createDNSAccount))
	mux.HandleFunc("PUT /api/admin/dns-accounts/{id}", h.requireAdmin(service.PermHostWrite, h.updateDNSAccount))
	mux.HandleFunc("DELETE /api/admin/dns-accounts/{id}", h.requireAdmin(service.PermHostWrite, h.deleteDNSAccount))
	mux.HandleFunc("GET /api/admin/server-hosts", h.requireAdmin(service.PermHostRead, h.listServerHosts))
	mux.HandleFunc("POST /api/admin/server-hosts", h.requireAdmin(service.PermHostWrite, h.createServerHosts))
	mux.HandleFunc("POST /api/admin/server-hosts/test", h.requireAdmin(service.PermHostWrite, h.testServerHost))
	mux.HandleFunc("GET /api/admin/server-hosts/{id}", h.requireAdmin(service.PermHostRead, h.getServerHost))
	mux.HandleFunc("PUT /api/admin/server-hosts/{id}", h.requireAdmin(service.PermHostWrite, h.updateServerHost))
	mux.HandleFunc("DELETE /api/admin/server-hosts/{id}", h.requireAdmin(service.PermHostWrite, h.deleteServerHost))
	mux.HandleFunc("POST /api/admin/server-hosts/{id}/reset-host-key", h.requireAdmin(service.PermHostWrite, h.resetServerHostKey))
	mux.HandleFunc("GET /api/admin/server-hosts/{id}/tasks", h.requireAdmin(service.PermHostRead, h.listServerHostTasks))
	mux.HandleFunc("POST /api/admin/server-hosts/{id}/actions", h.requireAdmin(service.PermHostWrite, h.createServerHostAction))
	mux.HandleFunc("POST /api/admin/server-hosts/{id}/panel-login/reset", h.requireAdmin(service.PermHostWrite, h.resetServerHostPanelLogin))
	mux.HandleFunc("GET /api/admin/host-tasks/{id}", h.requireAdmin(service.PermHostRead, h.getHostTask))
	mux.HandleFunc("POST /api/admin/host-tasks/{id}/cancel", h.requireAdmin(service.PermHostWrite, h.cancelHostTask))
	mux.HandleFunc("PUT /api/admin/servers/{id}/host", h.requireAdmin(service.PermHostWrite, h.bindServerHost))
	mux.HandleFunc("GET /api/admin/chain-routes", h.requireAdmin(service.PermServerRead, h.listChainRoutes))
	mux.HandleFunc("GET /api/admin/chain-exits", h.requireAdmin(service.PermNodeRead, h.listChainExits))
	mux.HandleFunc("POST /api/admin/chain-exits", h.requireAdmin(service.PermNodeWrite, h.createPlatformChainExit))
	mux.HandleFunc("PUT /api/admin/chain-exits/{id}", h.requireAdmin(service.PermNodeWrite, h.updatePlatformChainExit))
	mux.HandleFunc("DELETE /api/admin/chain-exits/{id}", h.requireAdmin(service.PermNodeWrite, h.deleteChainExitAsAdmin))
	mux.HandleFunc("PUT /api/admin/chain-exits/{id}/enabled", h.requireAdmin(service.PermNodeWrite, h.setChainExitEnabled))
	mux.HandleFunc("GET /api/admin/chain-exits/{id}/link", h.requireAdmin(service.PermNodeWrite, h.getPlatformChainExitLink))
	mux.HandleFunc("POST /api/admin/chain-exits/{id}/test", h.requireAdmin(service.PermNodeWrite, h.testChainExitAsAdmin))
	mux.HandleFunc("GET /api/admin/servers/{id}/inbounds", h.requireAdmin(service.PermNodeRead, h.listServerInbounds))
	mux.HandleFunc("POST /api/admin/servers/{id}/inbounds", h.requireAdmin(service.PermNodeWrite, h.createInbound))
	mux.HandleFunc("POST /api/admin/servers/{id}/endpoints", h.requireAdmin(service.PermNodeWrite, h.createEndpoints))
	mux.HandleFunc("GET /api/admin/inbounds/{id}", h.requireAdmin(service.PermNodeRead, h.getInbound))
	mux.HandleFunc("PUT /api/admin/inbounds/{id}", h.requireAdmin(service.PermNodeWrite, h.updateInbound))
	mux.HandleFunc("DELETE /api/admin/inbounds/{id}", h.requireAdmin(service.PermNodeWrite, h.deleteInbound))
	mux.HandleFunc("PUT /api/admin/inbounds/{id}/enabled", h.requireAdmin(service.PermNodeWrite, h.setInboundEnabled))
	mux.HandleFunc("POST /api/admin/inbounds/{id}/drift/ack", h.requireAdmin(service.PermNodeWrite, h.ackInboundDrift))
	mux.HandleFunc("GET /api/admin/inbounds/{id}/panel-config", h.requireAdmin(service.PermNodeWrite, h.getInboundPanelConfig))
	mux.HandleFunc("POST /api/admin/servers/{id}/keys", h.requireAdmin(service.PermNodeWrite, h.generatePanelKeys))
	mux.HandleFunc("GET /api/admin/servers/{id}/web-cert", h.requireAdmin(service.PermNodeWrite, h.getServerWebCert))
	mux.HandleFunc("POST /api/admin/servers/{id}/locate", h.requireAdmin(service.PermServerWrite, h.locateServer))
	mux.HandleFunc("POST /api/admin/servers/{id}/reality-scan", h.requireAdmin(service.PermNodeWrite, h.scanRealityTarget))
	mux.HandleFunc("POST /api/admin/servers/{id}/reality-targets", h.requireAdmin(service.PermNodeWrite, h.scanRealityTargets))

	mux.HandleFunc("GET /api/admin/plans", h.requireAdmin(service.PermPlanRead, h.listPlans))
	mux.HandleFunc("POST /api/admin/plans", h.requireAdmin(service.PermPlanWrite, h.createPlan))
	mux.HandleFunc("PUT /api/admin/plans/{id}", h.requireAdmin(service.PermPlanWrite, h.updatePlan))
	mux.HandleFunc("DELETE /api/admin/plans/{id}", h.requireAdmin(service.PermPlanWrite, h.deletePlan))
	mux.HandleFunc("GET /api/admin/users", h.requireAdmin(service.PermUserRead, h.listUsers))
	mux.HandleFunc("POST /api/admin/users", h.requireAdmin(service.PermUserWrite, h.createUser))
	mux.HandleFunc("GET /api/admin/users/{id}", h.requireAdmin(service.PermUserRead, h.getUser))
	mux.HandleFunc("PUT /api/admin/users/{id}", h.requireAdmin(service.PermUserWrite, h.updateUser))
	mux.HandleFunc("DELETE /api/admin/users/{id}", h.requireAdmin(service.PermUserWrite, h.deleteUser))
	mux.HandleFunc("POST /api/admin/users/{id}/plan", h.requireAdmin(service.PermUserWrite, h.assignUserPlan))
	mux.HandleFunc("PUT /api/admin/users/{id}/quota", h.requireAdmin(service.PermUserWrite, h.updateUserQuota))
	mux.HandleFunc("PUT /api/admin/users/{id}/password", h.requireAdmin(service.PermUserWrite, h.resetUserPassword))
	mux.HandleFunc("GET /api/admin/users/{id}/provisions", h.requireAdmin(service.PermUserRead, h.listUserProvisions))
	mux.HandleFunc("POST /api/admin/users/{id}/reconcile", h.requireAdmin(service.PermUserWrite, h.reconcileUser))
	mux.HandleFunc("DELETE /api/admin/users/{id}/totp", h.requireAdmin(service.PermUserWrite, h.clearUserTOTP))
	mux.HandleFunc("POST /api/admin/users/{id}/balance", h.requireAdmin(service.PermFinanceWrite, h.adjustUserBalance))
	mux.HandleFunc("POST /api/admin/users/{id}/impersonate", h.requireAdmin(service.PermImpersonateWrite, h.impersonateUser))

	mux.HandleFunc("GET /api/admin/traffic-packs", h.requireAdmin(service.PermShopRead, h.listTrafficPacks))
	mux.HandleFunc("POST /api/admin/traffic-packs", h.requireAdmin(service.PermShopWrite, h.createTrafficPack))
	mux.HandleFunc("PUT /api/admin/traffic-packs/{id}", h.requireAdmin(service.PermShopWrite, h.updateTrafficPack))
	mux.HandleFunc("DELETE /api/admin/traffic-packs/{id}", h.requireAdmin(service.PermShopWrite, h.deleteTrafficPack))
	mux.HandleFunc("GET /api/admin/coupons", h.requireAdmin(service.PermShopRead, h.listCoupons))
	mux.HandleFunc("POST /api/admin/coupons", h.requireAdmin(service.PermShopWrite, h.createCoupon))
	mux.HandleFunc("PUT /api/admin/coupons/{id}", h.requireAdmin(service.PermShopWrite, h.updateCoupon))
	mux.HandleFunc("DELETE /api/admin/coupons/{id}", h.requireAdmin(service.PermShopWrite, h.deleteCoupon))
	mux.HandleFunc("GET /api/admin/orders", h.requireAdmin(service.PermFinanceRead, h.listOrders))
	mux.HandleFunc("GET /api/admin/wallet-ledger", h.requireAdmin(service.PermFinanceRead, h.listWalletLedger))
	mux.HandleFunc("GET /api/admin/payments", h.requireAdmin(service.PermFinanceRead, h.listPayments))
	mux.HandleFunc("GET /api/admin/rebates", h.requireAdmin(service.PermFinanceRead, h.listRebates))
	mux.HandleFunc("GET /api/admin/finance/stats", h.requireAdmin(service.PermFinanceRead, h.getFinanceStats))
	mux.HandleFunc("GET /api/admin/finance/stats.csv", h.requireAdmin(service.PermFinanceRead, h.exportFinanceStats))
	mux.HandleFunc("GET /api/admin/dashboard", h.requireAdmin("", h.getAdminDashboard))
	mux.HandleFunc("GET /api/admin/analytics/users", h.requireAdmin(service.PermUserRead, h.getAnalyticsUsers))
	mux.HandleFunc("GET /api/admin/analytics/traffic", h.requireAdmin(service.PermUserRead, h.getAnalyticsTraffic))
	mux.HandleFunc("GET /api/admin/analytics/servers", h.requireAdmin(service.PermServerRead, h.getAnalyticsServers))
	mux.HandleFunc("GET /api/admin/analytics/finance", h.requireAdmin(service.PermFinanceRead, h.getAnalyticsFinance))
	mux.HandleFunc("GET /api/admin/analytics/backlog", h.requireAdmin("", h.getAnalyticsBacklog))
	mux.HandleFunc("GET /api/admin/topup-codes", h.requireAdmin(service.PermFinanceRead, h.listTopupCodes))
	mux.HandleFunc("POST /api/admin/topup-codes", h.requireAdmin(service.PermFinanceWrite, h.createTopupCodes))
	mux.HandleFunc("PUT /api/admin/topup-codes/{id}", h.requireAdmin(service.PermFinanceWrite, h.updateTopupCode))
	mux.HandleFunc("DELETE /api/admin/topup-codes/{id}", h.requireAdmin(service.PermFinanceWrite, h.deleteTopupCode))
	mux.HandleFunc("GET /api/admin/topup-codes/{id}/code", h.requireAdmin(service.PermFinanceWrite, h.revealTopupCode))
	mux.HandleFunc("POST /api/admin/users/{id}/subscription/reset", h.requireAdmin(service.PermUserWrite, h.resetUserSubscription))
	mux.HandleFunc("GET /api/admin/subscribe-logs", h.requireAdmin(service.PermUserRead, h.listSubscribeLogs))
	mux.HandleFunc("DELETE /api/admin/endpoints/{id}", h.requireAdmin(service.PermNodeWrite, h.deleteEndpoint))
	mux.HandleFunc("GET /api/admin/nodes", h.requireAdmin(service.PermNodeRead, h.listNodes))
	mux.HandleFunc("PUT /api/admin/nodes/{id}", h.requireAdmin(service.PermNodeWrite, h.updateNode))
	mux.HandleFunc("GET /api/admin/nodes/{id}/reach", h.requireAdmin(service.PermNodeRead, h.getNodeReach))
	mux.HandleFunc("POST /api/admin/nodes/{id}/reach/check", h.requireAdmin(service.PermNodeWrite, h.checkNodeReach))
	mux.HandleFunc("GET /api/admin/node-groups", h.requireAdmin(service.PermNodeRead, h.listNodeGroups))
	mux.HandleFunc("POST /api/admin/node-groups", h.requireAdmin(service.PermNodeWrite, h.createNodeGroup))
	mux.HandleFunc("PUT /api/admin/node-groups/{id}", h.requireAdmin(service.PermNodeWrite, h.updateNodeGroup))
	mux.HandleFunc("DELETE /api/admin/node-groups/{id}", h.requireAdmin(service.PermNodeWrite, h.deleteNodeGroup))

	mux.HandleFunc("GET /api/admin/settings", h.requireAdmin(service.PermSettingRead, h.getSettings))
	mux.HandleFunc("PUT /api/admin/settings", h.requireAdmin(service.PermSettingWrite, h.updateSettings))
	mux.HandleFunc("POST /api/admin/settings/test-mail", h.requireAdmin(service.PermSettingWrite, h.sendTestMail))
	mux.HandleFunc("POST /api/admin/settings/test-telegram", h.requireAdmin(service.PermSettingWrite, h.sendTestTelegram))
	mux.HandleFunc("GET /api/admin/mail-outbox", h.requireAdmin(service.PermSettingRead, h.listMailOutbox))

	mux.HandleFunc("GET /api/admin/announcements", h.requireAdmin(service.PermContentRead, h.listAnnouncements))
	mux.HandleFunc("POST /api/admin/announcements", h.requireAdmin(service.PermContentWrite, h.createAnnouncement))
	mux.HandleFunc("PUT /api/admin/announcements/{id}", h.requireAdmin(service.PermContentWrite, h.updateAnnouncement))
	mux.HandleFunc("DELETE /api/admin/announcements/{id}", h.requireAdmin(service.PermContentWrite, h.deleteAnnouncement))
	mux.HandleFunc("POST /api/admin/announcements/{id}/mail", h.requireAdmin(service.PermContentWrite, h.mailAnnouncement))
	mux.HandleFunc("GET /api/admin/tutorials", h.requireAdmin(service.PermContentRead, h.listTutorials))
	mux.HandleFunc("POST /api/admin/tutorials", h.requireAdmin(service.PermContentWrite, h.createTutorial))
	mux.HandleFunc("PUT /api/admin/tutorials/{id}", h.requireAdmin(service.PermContentWrite, h.updateTutorial))
	mux.HandleFunc("DELETE /api/admin/tutorials/{id}", h.requireAdmin(service.PermContentWrite, h.deleteTutorial))
	mux.HandleFunc("GET /api/admin/shared-accounts", h.requireAdmin(service.PermContentRead, h.listSharedAccounts))
	mux.HandleFunc("POST /api/admin/shared-accounts", h.requireAdmin(service.PermContentWrite, h.createSharedAccount))
	mux.HandleFunc("PUT /api/admin/shared-accounts/{id}", h.requireAdmin(service.PermContentWrite, h.updateSharedAccount))
	mux.HandleFunc("DELETE /api/admin/shared-accounts/{id}", h.requireAdmin(service.PermContentWrite, h.deleteSharedAccount))
	mux.HandleFunc("GET /api/admin/shared-accounts/{id}/credential", h.requireAdmin(service.PermContentWrite, h.getSharedAccountCredential))
	mux.HandleFunc("GET /api/admin/tickets", h.requireAdmin(service.PermTicketRead, h.listTickets))
	mux.HandleFunc("GET /api/admin/tickets/{id}", h.requireAdmin(service.PermTicketRead, h.getTicket))
	mux.HandleFunc("POST /api/admin/tickets/{id}/messages", h.requireAdmin(service.PermTicketWrite, h.replyTicket))
	mux.HandleFunc("POST /api/admin/tickets/{id}/close", h.requireAdmin(service.PermTicketWrite, h.closeTicket))

	// 未登录可用：站点信息与邮箱验证码（docs/03 §5.6 公开子集、§5.7）
	mux.HandleFunc("GET /api/user/site", h.getSiteInfo)
	mux.HandleFunc("GET /api/user/site/plans", h.getPublicPlans)
	mux.HandleFunc("POST /api/user/auth/email-code", h.sendEmailCode)
	mux.HandleFunc("POST /api/user/auth/register", h.registerUser)
	mux.HandleFunc("POST /api/user/auth/password/reset", h.resetOwnSecret)

	mux.HandleFunc("GET /api/admin/invite-codes", h.requireAdmin(service.PermUserRead, h.listInviteCodes))
	mux.HandleFunc("POST /api/admin/invite-codes", h.requireAdmin(service.PermUserWrite, h.createInviteCode))
	mux.HandleFunc("PUT /api/admin/invite-codes/{id}", h.requireAdmin(service.PermUserWrite, h.updateInviteCode))
	mux.HandleFunc("DELETE /api/admin/invite-codes/{id}", h.requireAdmin(service.PermUserWrite, h.deleteInviteCode))

	// 客户后台（docs/03 §5.3–§5.5）：身份只取自会话（R8）。auth/me 与登出允许停用的账号访问。
	mux.HandleFunc("POST /api/user/auth/login", h.userLogin)
	mux.HandleFunc("POST /api/user/auth/impersonate", h.redeemImpersonateTicket)
	mux.HandleFunc("POST "+userLogoutPath, h.requireUser(true, h.userLogout))
	mux.HandleFunc("GET /api/user/auth/me", h.requireUser(true, h.userMe))
	mux.HandleFunc("PUT /api/user/auth/password", h.requireUser(false, h.userChangeSecret))
	mux.HandleFunc("POST /api/user/auth/totp/setup", h.requireUser(false, h.userTotpSetup))
	mux.HandleFunc("POST /api/user/auth/totp/enable", h.requireUser(false, h.userTotpEnable))
	mux.HandleFunc("POST /api/user/auth/totp/disable", h.requireUser(false, h.userTotpDisable))
	mux.HandleFunc("GET /api/user/login-logs", h.requireUser(false, h.listUserLoginLogs))
	mux.HandleFunc("GET /api/user/dashboard", h.requireUser(false, h.getUserDashboard))
	mux.HandleFunc("GET /api/user/traffic", h.requireUser(false, h.listUserTraffic))
	mux.HandleFunc("GET /api/user/nodes", h.requireUser(false, h.listUserNodes))
	mux.HandleFunc("GET /api/user/nodes/{id}/link", h.requireUser(false, h.getUserNodeLink))
	mux.HandleFunc("GET /api/user/nodes/{id}/metrics", h.requireUser(false, h.getUserNodeMetrics))
	mux.HandleFunc("GET /api/user/subscription", h.requireUser(false, h.getUserSubscription))
	mux.HandleFunc("POST /api/user/subscription/reset", h.requireUser(false, h.resetOwnSubscription))
	mux.HandleFunc("PUT /api/user/subscription/password", h.requireUser(false, h.setConnectionPassword))
	mux.HandleFunc("GET /api/user/subscribe-logs", h.requireUser(false, h.listOwnSubscribeLogs))

	// 公告、教程、共享账号、工单、签到（docs/03 §5.9–§5.11）
	mux.HandleFunc("GET /api/user/announcements", h.requireUser(false, h.listUserAnnouncements))
	mux.HandleFunc("GET /api/user/tutorials", h.requireUser(false, h.listUserTutorials))
	mux.HandleFunc("GET /api/user/shared-accounts", h.requireUser(false, h.listUserSharedAccounts))
	mux.HandleFunc("GET /api/user/shared-accounts/{id}/credential", h.requireUser(false, h.getUserSharedAccountCredential))
	mux.HandleFunc("GET /api/user/tickets", h.requireUser(false, h.listOwnTickets))
	mux.HandleFunc("POST /api/user/tickets", h.requireUser(false, h.createTicket))
	mux.HandleFunc("GET /api/user/tickets/{id}", h.requireUser(false, h.getOwnTicket))
	mux.HandleFunc("POST /api/user/tickets/{id}/messages", h.requireUser(false, h.replyOwnTicket))
	mux.HandleFunc("POST /api/user/tickets/{id}/close", h.requireUser(false, h.closeOwnTicket))
	mux.HandleFunc("GET /api/user/checkin", h.requireUser(false, h.getCheckin))
	mux.HandleFunc("POST /api/user/checkin", h.requireUser(false, h.checkin))
	mux.HandleFunc("GET /api/user/shop", h.requireUser(false, h.getShop))
	mux.HandleFunc("POST /api/user/shop/quote", h.requireUser(false, h.quoteOrder))
	mux.HandleFunc("GET /api/user/orders", h.requireUser(false, h.listOwnOrders))
	mux.HandleFunc("POST /api/user/orders", h.requireUser(false, h.createOrder))
	mux.HandleFunc("GET /api/user/wallet", h.requireUser(false, h.getWallet))
	mux.HandleFunc("PUT /api/user/wallet/auto-renew", h.requireUser(false, h.setAutoRenew))
	mux.HandleFunc("POST /api/user/wallet/payments", h.requireUser(false, h.createPayment))
	mux.HandleFunc("GET /api/user/chain", h.requireUser(false, h.getChainOverview))
	mux.HandleFunc("POST /api/user/chain/routes", h.requireUser(false, h.createChainRoute))
	mux.HandleFunc("PUT /api/user/chain/routes/order", h.requireUser(false, h.reorderChainRoutes))
	mux.HandleFunc("PUT /api/user/chain/routes/{id}", h.requireUser(false, h.updateChainRoute))
	mux.HandleFunc("DELETE /api/user/chain/routes/{id}", h.requireUser(false, h.deleteChainRoute))
	mux.HandleFunc("POST /api/user/chain/exits", h.requireUser(false, h.createChainExit))
	mux.HandleFunc("PUT /api/user/chain/exits/{id}", h.requireUser(false, h.updateChainExit))
	mux.HandleFunc("DELETE /api/user/chain/exits/{id}", h.requireUser(false, h.deleteChainExit))
	mux.HandleFunc("GET /api/user/chain/exits/{id}/link", h.requireUser(false, h.getChainExitLink))
	mux.HandleFunc("POST /api/user/chain/exits/{id}/test", h.requireUser(false, h.testChainExit))
	// 支付网关回调：没有会话与 CSRF，真实性靠网关实现验签
	mux.HandleFunc("POST /api/pay/notify/{gateway}", h.paymentNotify)
	mux.HandleFunc("GET /api/user/wallet/ledger", h.requireUser(false, h.listOwnLedger))
	mux.HandleFunc("POST /api/user/wallet/redeem", h.requireUser(false, h.redeemTopupCode))

	// 账号自助（docs/03 §5.8）
	mux.HandleFunc("POST /api/user/account/email-code", h.requireUser(false, h.sendBindEmailCode))
	mux.HandleFunc("PUT /api/user/account/email", h.requireUser(false, h.bindEmail))
	mux.HandleFunc("POST /api/user/account/delete", h.requireUser(false, h.deleteOwnAccount))
	mux.HandleFunc("GET /api/user/invite", h.requireUser(false, h.getInvite))
	mux.HandleFunc("POST /api/user/invite/reset", h.requireUser(false, h.resetInviteCode))

	// 订阅端点：凭 token 访问，供代理客户端直接拉取（docs/03 §5.4）
	// 订阅地址的前缀可由管理员改（Ruling-59），所以这里匹配任意前缀，由业务层只认当前与过渡中的前缀
	mux.HandleFunc("GET /{subPath}/{token}", h.getSubscription)
	return withSecurityHeaders(mux)
}

// withSecurityHeaders 给所有响应加 nosniff，给 /api 响应禁用缓存（含会话与权限数据）。
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headerNoSniff, "nosniff")
		if strings.HasPrefix(r.URL.Path, apiPrefix) {
			w.Header().Set(headerCache, "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

type statusResponse struct {
	Status string `json:"status"`
}

// handleHealthz 是存活探针：只说明进程在跑，不探测数据库等依赖。
func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, statusResponse{Status: statusOK})
}

// newReadyzHandler 返回就绪探针：数据库可达才返回 200，否则 503。
// 失败原因只写日志，不回给调用方（可能含内部地址）。
func newReadyzHandler(db Pinger, timeout time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			slog.Warn("readiness check failed", "dependency", "database", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, statusResponse{Status: statusUnavailable})
			return
		}
		writeJSON(w, http.StatusOK, statusResponse{Status: statusOK})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set(headerContent, contentTypeJSON)
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// 状态码已写出，只能记录；常见原因是客户端提前断开。
		slog.Warn("write json response", "status", status, "err", err)
	}
}
