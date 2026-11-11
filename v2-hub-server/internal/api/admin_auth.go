package api

import (
	"context"
	"crypto/subtle"
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
)

// AdminAuthenticator 是 Web 层需要的认证能力（由 service.AuthService 实现）。
type AdminAuthenticator interface {
	Login(ctx context.Context, in service.LoginInput) (*service.LoginResult, error)
	Authenticate(ctx context.Context, token string) (*service.AdminPrincipal, error)
	Logout(ctx context.Context, p *service.AdminPrincipal, clientIP *netip.Addr) error
	SetupTOTP(ctx context.Context, p *service.AdminPrincipal) (*service.TOTPSetup, error)
	EnableTOTP(ctx context.Context, p *service.AdminPrincipal, code string, ip *netip.Addr) error
	DisableTOTP(ctx context.Context, p *service.AdminPrincipal, code string, ip *netip.Addr) error
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
	Admin       adminBriefVO `json:"admin"`
	Permissions []string     `json:"permissions"`
	CSRFToken   string       `json:"csrfToken"`
	TOTPEnabled bool         `json:"totpEnabled"`
}

func toSessionVO(p *service.AdminPrincipal) sessionVO {
	return sessionVO{
		Admin:       adminBriefVO{ID: p.AdminID, Username: p.Username},
		Permissions: p.Permissions.Sorted(),
		CSRFToken:   p.CSRFToken,
		TOTPEnabled: p.TOTPEnabled,
	}
}

// loginRequest 对应 hub-api.yaml LoginRequest（JSON 字段名 password）。
type loginRequest struct {
	Username   string `json:"username"`
	Credential string `json:"password"`
	TOTPCode   string `json:"totpCode"`
}

// adminHandler 是已认证管理员接口的处理函数。
type adminHandler func(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal)

func (h *handlers) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.Username == "" || req.Credential == "" {
		writeError(w, r, badRequest(msgEmptyLogin))
		return
	}
	in := service.LoginInput{
		Username:  req.Username,
		TOTPCode:  req.TOTPCode,
		ClientIP:  clientIP(r, h.trustedProxies),
		UserAgent: r.Header.Get(headerUserAgent),
	}
	pw := req.Credential
	in.Password = pw
	res, err := h.auth.Login(r.Context(), in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	http.SetCookie(w, h.sessionCookie(res.Token, res.ExpiresAt))
	writeOK(w, toSessionVO(res.Principal))
}

func (h *handlers) logout(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	if err := h.auth.Logout(r.Context(), p, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	expired := h.sessionCookie("", time.Unix(0, 0))
	expired.MaxAge = -1
	http.SetCookie(w, expired)
	writeOK(w, nil)
}

func (h *handlers) me(w http.ResponseWriter, _ *http.Request, p *service.AdminPrincipal) {
	writeOK(w, toSessionVO(p))
}

func (h *handlers) sessionCookie(value string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     adminSessionCookie,
		Value:    value,
		Path:     adminCookiePath,
		Expires:  expires,
		HttpOnly: true,
		Secure:   h.cookie.Secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// requireAdmin 校验会话、CSRF（非 GET/HEAD）与权限码（perm 为空表示只需登录），见 docs/03 §4.1 R1、§4.2。
func (h *handlers) requireAdmin(perm string, next adminHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if c, err := r.Cookie(adminSessionCookie); err == nil {
			token = c.Value
		}
		p, err := h.auth.Authenticate(r.Context(), token)
		if err != nil {
			writeError(w, r, err)
			return
		}
		if isUnsafeMethod(r.Method) && !csrfMatches(r.Header.Get(headerCSRF), p.CSRFToken) {
			writeError(w, r, &service.Error{Code: service.CodePermissionDenied, Message: msgCSRFFailed})
			return
		}
		if perm != "" && !p.Can(perm) {
			writeError(w, r, &service.Error{Code: service.CodePermissionDenied, Message: msgNoPermission})
			return
		}
		next(w, r, p)
	}
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
