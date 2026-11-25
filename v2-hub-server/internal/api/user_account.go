package api

import (
	"context"
	"net/http"
	"net/netip"
	"time"

	"v2-hub-server/internal/service"
)

// 站点信息与账号自助接口（docs/03 §5.6–§5.8），与 hub-api.yaml 的 user-auth / user-account 逐条对应。

// AccountManager 是账号自助接口需要的业务能力（由 service.AccountService 实现）。
type AccountManager interface {
	Site(ctx context.Context) (*service.SiteInfo, error)
	SendEmailCode(ctx context.Context, email, purpose string, ip *netip.Addr) error
	Register(ctx context.Context, in service.RegisterInput) (*service.PortalLoginResult, error)
	ResetLoginSecret(ctx context.Context, email, code, newSecret string, ip *netip.Addr) error
	SendBindEmailCode(ctx context.Context, p *service.UserPrincipal, email string, ip *netip.Addr) error
	BindEmail(ctx context.Context, p *service.UserPrincipal, secret, email, code string, ip *netip.Addr) error
	DeleteOwnAccount(ctx context.Context, p *service.UserPrincipal, secret, totpCode string, ip *netip.Addr) error
	GetInvite(ctx context.Context, p *service.UserPrincipal) (*service.InviteInfo, error)
	ResetInvite(ctx context.Context, p *service.UserPrincipal, ip *netip.Addr) (*service.InviteInfo, error)
	ListInviteCodes(ctx context.Context, kind string, agentID int64, pq service.PageQuery) (*service.Page[service.InviteCode], error)
	CreateInviteCode(ctx context.Context, actor *service.AdminPrincipal, in service.InviteCodeInput, ip *netip.Addr) (*service.InviteCode, error)
	UpdateInviteCode(ctx context.Context, actor *service.AdminPrincipal, id int64, in service.InviteCodeInput, ip *netip.Addr) (*service.InviteCode, error)
	DeleteInviteCode(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) error
}

// siteInfoVO 对应 hub-api.yaml SiteInfo。
type siteInfoVO struct {
	SiteName            string            `json:"siteName"`
	RegisterMode        string            `json:"registerMode"`
	EmailVerifyRequired bool              `json:"emailVerifyRequired"`
	MailEnabled         bool              `json:"mailEnabled"`
	CheckinEnabled      bool              `json:"checkinEnabled"`
	TicketEnabled       bool              `json:"ticketEnabled"`
	SharedEnabled       bool              `json:"sharedEnabled"`
	ShopEnabled         bool              `json:"shopEnabled"`
	Currency            string            `json:"currency"`
	Landing             landingSettingsVO `json:"landing"`
	Support             supportSettingsVO `json:"support"`
	MenuOrder           []string          `json:"menuOrder"`
}

func (h *handlers) getSiteInfo(w http.ResponseWriter, r *http.Request) {
	info, err := h.account.Site(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, siteInfoVO{
		SiteName: info.SiteName, RegisterMode: info.RegisterMode, EmailVerifyRequired: info.EmailVerifyRequired, MailEnabled: info.MailEnabled,
		CheckinEnabled: info.CheckinEnabled, TicketEnabled: info.TicketEnabled, SharedEnabled: info.SharedEnabled,
		ShopEnabled: info.ShopEnabled, Currency: info.Currency,
		Landing:   landingSettingsVO{Enabled: info.Landing.Enabled, Headline: info.Landing.Headline, Intro: info.Landing.Intro},
		Support:   supportSettingsVO{URL: info.Support.URL, Label: info.Support.Label},
		MenuOrder: nonNil(info.MenuOrder),
	})
}

type emailCodeRequest struct {
	Email   string `json:"email"`
	Purpose string `json:"purpose"`
}

func (h *handlers) sendEmailCode(w http.ResponseWriter, r *http.Request) {
	var req emailCodeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.account.SendEmailCode(r.Context(), req.Email, req.Purpose, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

// registerRequest 对应 hub-api.yaml RegisterInput。
type registerRequest struct {
	Username   string `json:"username"`
	Email      string `json:"email"`
	Credential string `json:"password"`
	InviteCode string `json:"inviteCode"`
	EmailCode  string `json:"emailCode"`
}

func (h *handlers) registerUser(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	in := service.RegisterInput{
		Username: req.Username, Email: req.Email, InviteCode: req.InviteCode, EmailCode: req.EmailCode,
		ClientIP: clientIP(r, h.trustedProxies), UserAgent: r.Header.Get(headerUserAgent),
	}
	in.Password = req.Credential
	res, err := h.account.Register(r.Context(), in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	http.SetCookie(w, h.userSessionCookie(res.Token, res.ExpiresAt))
	writeOK(w, h.sessionVO(r, res.Principal))
}

// secretResetRequest 对应 hub-api.yaml PasswordResetInput。
type secretResetRequest struct {
	Email     string `json:"email"`
	EmailCode string `json:"emailCode"`
	New       string `json:"newPassword"`
}

func (h *handlers) resetOwnSecret(w http.ResponseWriter, r *http.Request) {
	var req secretResetRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.account.ResetLoginSecret(r.Context(), req.Email, req.EmailCode, req.New, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

type bindEmailCodeRequest struct {
	Email string `json:"email"`
}

func (h *handlers) sendBindEmailCode(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	var req bindEmailCodeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.account.SendBindEmailCode(r.Context(), p, req.Email, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

// bindEmailRequest 对应 hub-api.yaml BindEmailInput。
type bindEmailRequest struct {
	Current   string `json:"currentPassword"`
	Email     string `json:"email"`
	EmailCode string `json:"emailCode"`
}

func (h *handlers) bindEmail(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	var req bindEmailRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.account.BindEmail(r.Context(), p, req.Current, req.Email, req.EmailCode, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, h.sessionVO(r, p))
}

// accountDeleteRequest 对应 hub-api.yaml AccountDeleteInput。
type accountDeleteRequest struct {
	Current  string `json:"currentPassword"`
	TOTPCode string `json:"totpCode"`
}

func (h *handlers) deleteOwnAccount(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	var req accountDeleteRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.account.DeleteOwnAccount(r.Context(), p, req.Current, req.TOTPCode, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	http.SetCookie(w, h.expiredCookie(userSessionCookie, userCookiePath))
	writeOK(w, nil)
}

type inviteeVO struct {
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"createdAt"`
}

// inviteInfoVO 对应 hub-api.yaml InviteInfo。
type inviteInfoVO struct {
	Enabled   bool        `json:"enabled"`
	Code      string      `json:"code"`
	MaxUses   int         `json:"maxUses"`
	UsedCount int         `json:"usedCount"`
	Invitees  []inviteeVO `json:"invitees"`
	// 0.4.0 起：返利比例（0 = 未开启）、累计返利（分）与最近的返利记录
	RebatePercent int              `json:"rebatePercent"`
	RebateTotal   int64            `json:"rebateTotal"`
	Rebates       []inviteRebateVO `json:"rebates"`
}

type inviteRebateVO struct {
	Username  string    `json:"username"`
	Amount    int64     `json:"amount"`
	CreatedAt time.Time `json:"createdAt"`
}

func toInviteInfoVO(info *service.InviteInfo) inviteInfoVO {
	out := inviteInfoVO{Enabled: info.Enabled, Code: info.Code, MaxUses: info.MaxUses, UsedCount: info.UsedCount, Invitees: make([]inviteeVO, 0, len(info.Invitees))}
	for _, it := range info.Invitees {
		out.Invitees = append(out.Invitees, inviteeVO{Username: it.Username, CreatedAt: it.CreatedAt})
	}
	out.RebatePercent, out.RebateTotal, out.Rebates = info.RebatePercent, info.RebateTotal, make([]inviteRebateVO, 0, len(info.Rebates))
	for _, rb := range info.Rebates {
		out.Rebates = append(out.Rebates, inviteRebateVO{Username: rb.Username, Amount: rb.Amount, CreatedAt: rb.CreatedAt})
	}
	return out
}

func (h *handlers) getInvite(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	info, err := h.account.GetInvite(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toInviteInfoVO(info))
}

func (h *handlers) resetInviteCode(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	info, err := h.account.ResetInvite(r.Context(), p, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toInviteInfoVO(info))
}
