package api

import (
	"context"
	"net/http"
	"net/netip"
	"time"

	"v2-hub-server/internal/service"
)

// 客户后台的认证接口（docs/03 §5.3），与 hub-api.yaml 的 user-auth 逐条对应。

const (
	userSessionCookie = "hub_user_session"
	userCookiePath    = "/api/user"
)

// PortalAuthenticator 是 Web 层需要的终端用户认证能力（由 service.PortalAuthService 实现）。
type PortalAuthenticator interface {
	Login(ctx context.Context, in service.LoginInput) (*service.PortalLoginResult, error)
	Authenticate(ctx context.Context, token string) (*service.UserPrincipal, error)
	Logout(ctx context.Context, p *service.UserPrincipal) error
	ChangeLoginSecret(ctx context.Context, p *service.UserPrincipal, oldSecret, newSecret string, ip *netip.Addr) error
	SetupTOTP(ctx context.Context, p *service.UserPrincipal) (*service.TOTPSetup, error)
	EnableTOTP(ctx context.Context, p *service.UserPrincipal, code string, ip *netip.Addr) error
	DisableTOTP(ctx context.Context, p *service.UserPrincipal, code string, ip *netip.Addr) error
	ListLoginLogs(ctx context.Context, p *service.UserPrincipal) ([]service.LoginLogItem, error)
	IssueImpersonateTicket(ctx context.Context, actor *service.AdminPrincipal, userID int64, ip *netip.Addr) (*service.ImpersonateTicket, error)
	RedeemImpersonateTicket(ctx context.Context, ticket string, ip *netip.Addr, userAgent string) (*service.PortalLoginResult, error)
}

type userBriefVO struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Status   string `json:"status"`
	// 0.3.0 起：未绑定邮箱为 null
	Email           *string `json:"email"`
	IsEmailVerified bool    `json:"isEmailVerified"`
}

type userSessionVO struct {
	User        userBriefVO `json:"user"`
	CSRFToken   string      `json:"csrfToken"`
	TOTPEnabled bool        `json:"totpEnabled"`
	// 0.4.0 起：真 = 管理员代入的只读会话（docs/03 §5.17）
	Impersonated bool `json:"impersonated"`
	// 0.4.1 起：当前套餐开了哪些功能（docs/03 §5.18），由 handlers.sessionVO 填
	Features userFeaturesVO `json:"features"`
}

func toUserSessionVO(p *service.UserPrincipal) userSessionVO {
	return userSessionVO{
		User:         userBriefVO{ID: p.UserID, Username: p.Username, Status: p.Status, Email: optionalText(p.Email), IsEmailVerified: p.EmailVerified},
		CSRFToken:    p.CSRFToken,
		TOTPEnabled:  p.TOTPEnabled,
		Impersonated: p.Impersonated(),
	}
}

// optionalText 把空串转成 nil（契约里“没有”用 null 表达）。
func optionalText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// userHandler 是已登录终端用户接口的处理函数。
type userHandler func(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal)

// requireUser 校验会话与 CSRF（非 GET/HEAD）；allowDisabled 为 false 时停用的账号返回 account_disabled（docs/03 §5.3）。
func (h *handlers) requireUser(allowDisabled bool, next userHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if c, err := r.Cookie(userSessionCookie); err == nil {
			token = c.Value
		}
		p, err := h.portalAuth.Authenticate(r.Context(), token)
		if err != nil {
			writeError(w, r, err)
			return
		}
		if isUnsafeMethod(r.Method) && !csrfMatches(r.Header.Get(headerCSRF), p.CSRFToken) {
			writeError(w, r, &service.Error{Code: service.CodePermissionDenied, Message: msgCSRFFailed})
			return
		}
		// 代入会话只读：除退出外的写请求一律拒绝（Ruling-33）
		if p.Impersonated() && isUnsafeMethod(r.Method) && r.URL.Path != userLogoutPath {
			writeError(w, r, &service.Error{Code: service.CodePermissionDenied, Message: msgImpersonateReadOnly})
			return
		}
		if !allowDisabled {
			if err := p.RequireEnabled(); err != nil {
				writeError(w, r, err)
				return
			}
		}
		next(w, r, p)
	}
}

func (h *handlers) userSessionCookie(value string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     userSessionCookie,
		Value:    value,
		Path:     userCookiePath,
		Expires:  expires,
		HttpOnly: true,
		Secure:   h.cookie.Secure,
		SameSite: http.SameSiteLaxMode,
	}
}

func (h *handlers) userLogin(w http.ResponseWriter, r *http.Request) {
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
	res, err := h.portalAuth.Login(r.Context(), in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	http.SetCookie(w, h.userSessionCookie(res.Token, res.ExpiresAt))
	writeOK(w, h.sessionVO(r, res.Principal))
}

func (h *handlers) userLogout(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	if err := h.portalAuth.Logout(r.Context(), p); err != nil {
		writeError(w, r, err)
		return
	}
	expired := h.userSessionCookie("", time.Unix(0, 0))
	expired.MaxAge = -1
	http.SetCookie(w, expired)
	writeOK(w, nil)
}

func (h *handlers) userMe(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	writeOK(w, h.sessionVO(r, p))
}

// secretChangeRequest 对应 hub-api.yaml UserPasswordChangeInput。
type secretChangeRequest struct {
	Old string `json:"oldPassword"`
	New string `json:"newPassword"`
}

func (h *handlers) userChangeSecret(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	var req secretChangeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.portalAuth.ChangeLoginSecret(r.Context(), p, req.Old, req.New, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) userTotpSetup(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	setup, err := h.portalAuth.SetupTOTP(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, totpSetupVO{Secret: setup.Secret, OtpauthURI: setup.OtpauthURI})
}

func (h *handlers) userTotpEnable(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	var req totpCodeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.portalAuth.EnableTOTP(r.Context(), p, req.Code, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) userTotpDisable(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	var req totpCodeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.portalAuth.DisableTOTP(r.Context(), p, req.Code, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

type loginLogVO struct {
	ClientIP  string    `json:"clientIp"`
	IsSuccess bool      `json:"isSuccess"`
	CreatedAt time.Time `json:"createdAt"`
}

func (h *handlers) listUserLoginLogs(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	logs, err := h.portalAuth.ListLoginLogs(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]loginLogVO, 0, len(logs))
	for _, l := range logs {
		out = append(out, loginLogVO{ClientIP: l.ClientIP, IsSuccess: l.IsSuccess, CreatedAt: l.CreatedAt})
	}
	writeOK(w, out)
}

const (
	userLogoutPath         = "/api/user/auth/logout"
	msgImpersonateReadOnly = "正在以用户身份查看，不能执行写操作"
)

// impersonateTicketVO 对应 hub-api.yaml ImpersonateTicket；ticket 属于凭据，不写日志。
type impersonateTicketVO struct {
	Ticket    string    `json:"ticket"`
	ExpiresAt time.Time `json:"expiresAt"`
	PortalURL string    `json:"portalUrl"`
}

// impersonateUser 给管理员签发切换为该用户身份的一次性票据（impersonate:write，docs/03 §5.17）。
func (h *handlers) impersonateUser(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	t, err := h.portalAuth.IssueImpersonateTicket(r.Context(), p, id, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	cur, err := h.settings.Current(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, impersonateTicketVO{Ticket: t.Ticket, ExpiresAt: t.ExpiresAt, PortalURL: cur.Site.PortalURL})
}

// impersonateRequest 对应 hub-api.yaml ImpersonateInput。
type impersonateRequest struct {
	Ticket string `json:"ticket"`
}

// redeemImpersonateTicket 用票据换取只读的代入会话并下发会话 Cookie。
func (h *handlers) redeemImpersonateTicket(w http.ResponseWriter, r *http.Request) {
	var req impersonateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	res, err := h.portalAuth.RedeemImpersonateTicket(r.Context(), req.Ticket, clientIP(r, h.trustedProxies), r.Header.Get(headerUserAgent))
	if err != nil {
		writeError(w, r, err)
		return
	}
	http.SetCookie(w, h.userSessionCookie(res.Token, res.ExpiresAt))
	writeOK(w, h.sessionVO(r, res.Principal))
}
