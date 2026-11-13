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
	cookie         CookieConfig
	trustedProxies []netip.Prefix
}

// NewRouter 返回注册好全部路由的 http.Handler。路由与 hub-api.yaml 的 paths 逐条对应。
func NewRouter(deps Deps) http.Handler {
	h := &handlers{auth: deps.Auth, rbac: deps.RBAC, panels: deps.Panels, users: deps.Users, portalAuth: deps.PortalAuth, portal: deps.Portal, cookie: deps.Cookie, trustedProxies: deps.TrustedProxies}
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
	mux.HandleFunc("POST /api/admin/servers/{id}/sync", h.requireAdmin(service.PermServerWrite, h.syncServer))
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
	mux.HandleFunc("POST /api/admin/users/{id}/subscription/reset", h.requireAdmin(service.PermUserWrite, h.resetUserSubscription))
	mux.HandleFunc("GET /api/admin/subscribe-logs", h.requireAdmin(service.PermUserRead, h.listSubscribeLogs))
	mux.HandleFunc("DELETE /api/admin/endpoints/{id}", h.requireAdmin(service.PermNodeWrite, h.deleteEndpoint))
	mux.HandleFunc("GET /api/admin/nodes", h.requireAdmin(service.PermNodeRead, h.listNodes))
	mux.HandleFunc("PUT /api/admin/nodes/{id}", h.requireAdmin(service.PermNodeWrite, h.updateNode))
	mux.HandleFunc("GET /api/admin/node-groups", h.requireAdmin(service.PermNodeRead, h.listNodeGroups))
	mux.HandleFunc("POST /api/admin/node-groups", h.requireAdmin(service.PermNodeWrite, h.createNodeGroup))
	mux.HandleFunc("PUT /api/admin/node-groups/{id}", h.requireAdmin(service.PermNodeWrite, h.updateNodeGroup))
	mux.HandleFunc("DELETE /api/admin/node-groups/{id}", h.requireAdmin(service.PermNodeWrite, h.deleteNodeGroup))

	// 客户后台（docs/03 §5.3–§5.5）：身份只取自会话（R8）。auth/me 与登出允许停用的账号访问。
	mux.HandleFunc("POST /api/user/auth/login", h.userLogin)
	mux.HandleFunc("POST /api/user/auth/logout", h.requireUser(true, h.userLogout))
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
	mux.HandleFunc("GET /api/user/subscription", h.requireUser(false, h.getUserSubscription))
	mux.HandleFunc("POST /api/user/subscription/reset", h.requireUser(false, h.resetOwnSubscription))
	mux.HandleFunc("GET /api/user/subscribe-logs", h.requireUser(false, h.listOwnSubscribeLogs))

	// 订阅端点：凭 token 访问，供代理客户端直接拉取（docs/03 §5.4）
	mux.HandleFunc("GET /sub/{token}", h.getSubscription)
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
