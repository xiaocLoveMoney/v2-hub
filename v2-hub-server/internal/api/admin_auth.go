package api

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"v2-hub-server/internal/service"
)

const (
	adminSessionCookie = "hub_admin_session"
	adminCookiePath    = "/api/admin"
	headerCSRF         = "X-CSRF-Token"
	headerUserAgent    = "User-Agent"
	msgCSRFFailed      = "CSRF 校验失败，请刷新页面后重试"
	msgNoPermission    = "没有权限执行该操作"
	msgEmptyLogin      = "用户名和密码不能为空"
	// 两条 message 要让还没更新的旧管理端页面也看得懂、知道怎么办（R10、R12）
	msgTOTPSetupRequired  = "超级管理员必须先开启两步验证：请到“账号安全”页完成绑定后再操作"
	msgReauthRequired     = "该操作需要重新验证身份；如果页面没有弹出验证框，请退出后重新登录，再在几分钟内重试"
	msgReauthEmpty        = "请输入密码或两步验证码"
	msgOldCredentialEmpty = "请输入旧密码"
)

// AdminAuthenticator 是 Web 层需要的认证能力（由 service.AuthService 实现）。
type AdminAuthenticator interface {
	Login(ctx context.Context, in service.LoginInput) (*service.LoginResult, error)
	Authenticate(ctx context.Context, token string) (*service.AdminPrincipal, error)
	Logout(ctx context.Context, p *service.AdminPrincipal, clientIP *netip.Addr) error
	SetupTOTP(ctx context.Context, p *service.AdminPrincipal) (*service.TOTPSetup, error)
	EnableTOTP(ctx context.Context, p *service.AdminPrincipal, code string, ip *netip.Addr) error
	DisableTOTP(ctx context.Context, p *service.AdminPrincipal, code string, ip *netip.Addr) error
	// Reauth 做二次验证，返回新的有效期（R12）；ChangeOwnPassword 本人改密码（R9）。
	Reauth(ctx context.Context, p *service.AdminPrincipal, password, totpCode string, ip *netip.Addr) (time.Time, error)
	ChangeOwnPassword(ctx context.Context, p *service.AdminPrincipal, oldPassword, newPassword string, ip *netip.Addr) error
}

// CookieConfig 是会话 Cookie 的属性。
type CookieConfig struct {
	// Secure 为 true 时 Cookie 只经 HTTPS 发送；仅本机 HTTP 开发可关闭。
	Secure bool
}

type adminBriefVO struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type sessionVO struct {
	Admin adminBriefVO `json:"admin"`
	// Kind 是账号类型 staff / agent（Ruling-101）；代理的权限、菜单为空，超管 / 所有者 / 待开两步验证恒为假。
	Kind        string   `json:"kind"`
	Permissions []string `json:"permissions"`
	CSRFToken   string   `json:"csrfToken"`
	TOTPEnabled bool     `json:"totpEnabled"`
	// MenuOrder 是管理端侧栏菜单顺序（设置 menu.admin，Ruling-57），空 = 默认顺序。
	MenuOrder []string `json:"menuOrder"`
	// IsSuperAdmin / IsOwner / TOTPSetupRequired 供前端显隐与跳转，后端仍是最终裁决（docs/03 §4.2）。
	IsSuperAdmin      bool `json:"isSuperAdmin"`
	IsOwner           bool `json:"isOwner"`
	TOTPSetupRequired bool `json:"totpSetupRequired"`
}

func toSessionVO(p *service.AdminPrincipal) sessionVO {
	return sessionVO{
		Admin:             adminBriefVO{ID: p.AdminID, Username: p.Username},
		Kind:              sessionKind(p),
		Permissions:       p.Permissions.Sorted(),
		CSRFToken:         p.CSRFToken,
		TOTPEnabled:       p.TOTPEnabled,
		MenuOrder:         []string{},
		IsSuperAdmin:      p.IsSuperAdmin,
		IsOwner:           p.IsOwner,
		TOTPSetupRequired: p.TOTPSetupRequired(),
	}
}

// sessionKind 返回会话的账号类型；只有确认是代理才输出 agent，其余按员工。
func sessionKind(p *service.AdminPrincipal) string {
	if p.IsAgent() {
		return service.AdminKindAgent
	}
	return service.AdminKindStaff
}

// adminSessionVO 组装会话信息并带上管理端菜单顺序。设置读不到时按默认顺序返回并留痕：
// 它只影响菜单排列，不能因此让登录失败。代理的菜单固定，不读设置（docs/03 §5.6）。
func (h *handlers) adminSessionVO(r *http.Request, p *service.AdminPrincipal) sessionVO {
	vo := toSessionVO(p)
	if p.IsAgent() {
		return vo
	}
	cur, err := h.settings.Current(r.Context())
	if err != nil {
		slog.Error("load menu order", "admin_id", p.AdminID, "err", err)
		return vo
	}
	vo.MenuOrder = nonNil(cur.Menu.Admin)
	return vo
}

// loginRequest 对应 hub-api.yaml LoginRequest（JSON 字段名 password）。
type loginRequest struct {
	Username   string `json:"username"`
	Credential string `json:"password"`
	TOTPCode   string `json:"totpCode"`
}

// adminHandler 是已认证管理员接口的处理函数。
type adminHandler func(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal)

// parseLoginInput 解码并校验登录请求体，组装成 service.LoginInput（管理端与用户端共用）；
// 失败时已写出错误响应，返回 false。
func (h *handlers) parseLoginInput(w http.ResponseWriter, r *http.Request) (service.LoginInput, bool) {
	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return service.LoginInput{}, false
	}
	if req.Username == "" || req.Credential == "" {
		writeError(w, r, badRequest(msgEmptyLogin))
		return service.LoginInput{}, false
	}
	in := service.LoginInput{
		Username:  req.Username,
		TOTPCode:  req.TOTPCode,
		ClientIP:  clientIP(r, h.trustedProxies),
		UserAgent: r.Header.Get(headerUserAgent),
	}
	pw := req.Credential
	in.Password = pw
	return in, true
}

func (h *handlers) login(w http.ResponseWriter, r *http.Request) {
	in, ok := h.parseLoginInput(w, r)
	if !ok {
		return
	}
	res, err := h.auth.Login(r.Context(), in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	http.SetCookie(w, h.sessionCookie(res.Token, res.ExpiresAt))
	writeOK(w, h.adminSessionVO(r, res.Principal))
}

func (h *handlers) logout(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	if err := h.auth.Logout(r.Context(), p, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	http.SetCookie(w, h.expiredCookie(adminSessionCookie, adminCookiePath))
	writeOK(w, nil)
}

func (h *handlers) me(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	writeOK(w, h.adminSessionVO(r, p))
}

func (h *handlers) sessionCookie(value string, expires time.Time) *http.Cookie {
	return h.newSessionCookie(adminSessionCookie, adminCookiePath, value, expires)
}

// newSessionCookie 构造会话 Cookie：HttpOnly、SameSite=Lax，Secure 取配置（管理端与用户端只有名称与路径不同）。
func (h *handlers) newSessionCookie(name, path, value string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		Expires:  expires,
		HttpOnly: true,
		Secure:   h.cookie.Secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// expiredCookie 构造让浏览器立即删除会话 Cookie 的同名 Cookie（空值、Expires 为 Unix 零点、MaxAge=-1）。
func (h *handlers) expiredCookie(name, path string) *http.Cookie {
	expired := h.newSessionCookie(name, path, "", time.Unix(0, 0))
	expired.MaxAge = -1
	return expired
}

// routePolicy 是一条管理端路由在权限码之外的鉴权要求，由 requireAdmin 的可变选项设置。
type routePolicy struct {
	// extraPerm 是同时要求的第二个权限码（一键部署入站要 node:write 与 host:write，Ruling-76）。
	extraPerm string
	// setupExempt 为 true 时尚未开启两步验证的超管也能访问（R10 的豁免接口）。
	setupExempt bool
	// fresh 为 true 时要求二次验证仍在有效期内（R12 的高危接口，docs/03 §4.4）。
	fresh bool
	// self 为 true 时代理账号也能访问（本人接口：查看会话、登出、两步验证、改自己密码、二次验证，Ruling-101）；默认不放行代理。
	self bool
}

// routeOption 修改一条路由的 routePolicy。
type routeOption func(*routePolicy)

// withPerm 追加一个必须同时拥有的权限码。
func withPerm(code string) routeOption {
	return func(p *routePolicy) { p.extraPerm = code }
}

// withSetupExempt 让尚未开启两步验证的超管也能访问（查看会话、登出、生成与确认 TOTP、改自己密码，R10）。
func withSetupExempt() routeOption {
	return func(p *routePolicy) { p.setupExempt = true }
}

// withFresh 要求二次验证仍在有效期内（R12）。
func withFresh() routeOption {
	return func(p *routePolicy) { p.fresh = true }
}

// withSelf 标出本人接口：员工与代理都放行（Ruling-101）。没有这个选项的管理端路由对代理一律 403，权限码为空串的也一样。
func withSelf() routeOption {
	return func(p *routePolicy) { p.self = true }
}

// authenticateAdmin 校验管理端会话与 CSRF（非 GET/HEAD）；失败时已写出错误响应，返回 nil。
func (h *handlers) authenticateAdmin(w http.ResponseWriter, r *http.Request) *service.AdminPrincipal {
	token := ""
	if c, err := r.Cookie(adminSessionCookie); err == nil {
		token = c.Value
	}
	p, err := h.auth.Authenticate(r.Context(), token)
	if err != nil {
		writeError(w, r, err)
		return nil
	}
	if isUnsafeMethod(r.Method) && !csrfMatches(r.Header.Get(headerCSRF), p.CSRFToken) {
		writeError(w, r, &service.Error{Code: service.CodePermissionDenied, Message: msgCSRFFailed})
		return nil
	}
	return p
}

// 管理端鉴权的固定错误；只读共享，不要修改字段。
var (
	errNoPermission      = &service.Error{Code: service.CodePermissionDenied, Message: msgNoPermission}
	errTOTPSetupRequired = &service.Error{Code: service.CodeTOTPSetupRequired, Message: msgTOTPSetupRequired}
	errReauthRequired    = &service.Error{Code: service.CodeReauthRequired, Message: msgReauthRequired}
)

// adminCheck 是认证通过后的一条路由的鉴权判定：返回 nil 放行，否则返回要写出的错误。
type adminCheck func(r *http.Request, p *service.AdminPrincipal) error

// guardAdmin 是 requireAdmin / requireAgent 共用的外壳：认证（会话 + CSRF）→ check → 写出错误或放行到 next。
func (h *handlers) guardAdmin(check adminCheck, next adminHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := h.authenticateAdmin(w, r)
		if p == nil {
			return
		}
		if err := check(r, p); err != nil {
			writeError(w, r, err)
			return
		}
		next(w, r, p)
	}
}

// buildPolicy 依次应用路由选项得到 routePolicy。
func buildPolicy(opts []routeOption) routePolicy {
	var policy routePolicy
	for _, opt := range opts {
		opt(&policy)
	}
	return policy
}

// requireAdmin 校验会话、CSRF（非 GET/HEAD）与权限码（perm 为空表示只需登录），见 docs/03 §4.1 R1、§4.2。
// 判定顺序见 checkAdmin。
func (h *handlers) requireAdmin(perm string, next adminHandler, opts ...routeOption) http.HandlerFunc {
	policy := buildPolicy(opts)
	return h.guardAdmin(func(_ *http.Request, p *service.AdminPrincipal) error {
		return checkAdmin(p, perm, policy)
	}, next)
}

// checkAdmin 是员工管理端路由的判定，顺序：代理账号（只放行 withSelf 的本人接口，Ruling-101）→ 超管未开启两步验证（R10）→
// 权限码 → 二次验证（R12）；没有权限的人不会被要求二次验证。认证与 CSRF 已由外壳先做。
func checkAdmin(p *service.AdminPrincipal, perm string, policy routePolicy) error {
	// 默认拒绝：代理只能用本人接口与 /api/admin/agent/*（Ruling-101）
	if p.IsAgent() && !policy.self {
		return errNoPermission
	}
	if p.TOTPSetupRequired() && !policy.setupExempt {
		return errTOTPSetupRequired
	}
	lacksPerm := (perm != "" && !p.Can(perm)) || (policy.extraPerm != "" && !p.Can(policy.extraPerm))
	if lacksPerm {
		return errNoPermission
	}
	if policy.fresh && !p.ReauthFresh {
		return errReauthRequired
	}
	return nil
}

// requireAgent 是代理专用接口 /api/admin/agent/* 的鉴权（Ruling-101）：只放行代理会话，员工（含超管）一律 403，不看权限码。
// 判定顺序见 checkAgent。
func (h *handlers) requireAgent(next adminHandler, opts ...routeOption) http.HandlerFunc {
	policy := buildPolicy(opts)
	return h.guardAdmin(func(r *http.Request, p *service.AdminPrincipal) error {
		return h.checkAgent(r, p, policy)
	}, next)
}

// checkAgent 是代理专用路由的判定，顺序：账号类型 → 二次验证（带 withFresh 时，R12）→ 写操作频控（非 GET/HEAD，Ruling-104）。
// 认证与 CSRF 已由外壳先做。
func (h *handlers) checkAgent(r *http.Request, p *service.AdminPrincipal, policy routePolicy) error {
	if !p.IsAgent() {
		return errNoPermission
	}
	if policy.fresh && !p.ReauthFresh {
		return errReauthRequired
	}
	if isUnsafeMethod(r.Method) {
		return h.agents.AllowWrite(p)
	}
	return nil
}

func isUnsafeMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead
}

func csrfMatches(got, want string) bool {
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

type totpSetupVO struct {
	Secret     string `json:"secret"`
	OtpauthURI string `json:"otpauthUri"`
}

type totpCodeRequest struct {
	Code string `json:"code"`
}

func (h *handlers) totpSetup(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	setup, err := h.auth.SetupTOTP(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, totpSetupVO{Secret: setup.Secret, OtpauthURI: setup.OtpauthURI})
}

func (h *handlers) totpEnable(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req totpCodeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.auth.EnableTOTP(r.Context(), p, req.Code, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) totpDisable(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req totpCodeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.auth.DisableTOTP(r.Context(), p, req.Code, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

// adminPasswordChangeRequest 对应 hub-api.yaml AdminPasswordChangeInput。
type adminPasswordChangeRequest struct {
	OldCredential string `json:"oldPassword"`
	NewCredential string `json:"newPassword"`
}

// changeOwnPassword 本人改密码（R9，Ruling-96）。
func (h *handlers) changeOwnPassword(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req adminPasswordChangeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.OldCredential == "" {
		writeError(w, r, badRequest(msgOldCredentialEmpty))
		return
	}
	if err := h.auth.ChangeOwnPassword(r.Context(), p, req.OldCredential, req.NewCredential, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

// adminReauthRequest 对应 hub-api.yaml AdminReauthInput：二选一，由 service 按账号是否开启两步验证决定用哪个。
type adminReauthRequest struct {
	Credential string `json:"password"`
	TOTPCode   string `json:"totpCode"`
}

type adminReauthVO struct {
	ReauthUntil time.Time `json:"reauthUntil"`
}

// reauth 高危操作前的二次验证（R12，Ruling-100）。
func (h *handlers) reauth(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req adminReauthRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.Credential == "" && req.TOTPCode == "" {
		writeError(w, r, badRequest(msgReauthEmpty))
		return
	}
	until, err := h.auth.Reauth(r.Context(), p, req.Credential, req.TOTPCode, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, adminReauthVO{ReauthUntil: until})
}
