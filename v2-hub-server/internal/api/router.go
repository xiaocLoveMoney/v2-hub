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
	// Cookie 是会话 Cookie 属性。
	Cookie CookieConfig
	// TrustedProxies 是可采信 X-Forwarded-For 的直连网段（如 Caddy 所在 docker 网络）。
	TrustedProxies []netip.Prefix
}

type handlers struct {
	auth           AdminAuthenticator
	rbac           AdminManager
	cookie         CookieConfig
	trustedProxies []netip.Prefix
}

// NewRouter 返回注册好全部路由的 http.Handler。路由与 hub-api.yaml 的 paths 逐条对应。
func NewRouter(deps Deps) http.Handler {
	h := &handlers{auth: deps.Auth, rbac: deps.RBAC, cookie: deps.Cookie, trustedProxies: deps.TrustedProxies}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /readyz", newReadyzHandler(deps.Database, deps.PingTimeout))

	mux.HandleFunc("POST /api/admin/auth/login", h.login)
	mux.HandleFunc("POST /api/admin/auth/logout", h.requireAdmin("", h.logout))
	mux.HandleFunc("GET /api/admin/auth/me", h.requireAdmin("", h.me))

	mux.HandleFunc("GET /api/admin/permissions", h.requireAdmin(service.PermAdminRead, h.listPermissions))
	mux.HandleFunc("GET /api/admin/roles", h.requireAdmin(service.PermAdminRead, h.listRoles))
	mux.HandleFunc("POST /api/admin/roles", h.requireAdmin(service.PermAdminWrite, h.createRole))
	mux.HandleFunc("PUT /api/admin/roles/{id}", h.requireAdmin(service.PermAdminWrite, h.updateRole))
	mux.HandleFunc("DELETE /api/admin/roles/{id}", h.requireAdmin(service.PermAdminWrite, h.deleteRole))
	mux.HandleFunc("GET /api/admin/admins", h.requireAdmin(service.PermAdminRead, h.listAdmins))
	mux.HandleFunc("POST /api/admin/admins", h.requireAdmin(service.PermAdminWrite, h.createAdmin))
	mux.HandleFunc("PUT /api/admin/admins/{id}", h.requireAdmin(service.PermAdminWrite, h.updateAdmin))
	mux.HandleFunc("PUT /api/admin/admins/{id}/password", h.requireAdmin(service.PermAdminWrite, h.resetAdminCredential))
	mux.HandleFunc("GET /api/admin/audit-logs", h.requireAdmin(service.PermAuditRead, h.listAuditLogs))
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
