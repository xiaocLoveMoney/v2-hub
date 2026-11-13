package api

import (
	"context"
	"net/http"
	"net/netip"

	"v2-hub-server/internal/service"
)

// 站点信息与账号自助接口（docs/03 §5.6–§5.8），与 hub-api.yaml 的 user-auth / user-account 逐条对应。

// AccountManager 是账号自助接口需要的业务能力（由 service.AccountService 实现）。
type AccountManager interface {
	Site(ctx context.Context) (*service.SiteInfo, error)
	SendEmailCode(ctx context.Context, email, purpose string, ip *netip.Addr) error
}

// siteInfoVO 对应 hub-api.yaml SiteInfo。
type siteInfoVO struct {
	SiteName            string `json:"siteName"`
	RegisterMode        string `json:"registerMode"`
	EmailVerifyRequired bool   `json:"emailVerifyRequired"`
	MailEnabled         bool   `json:"mailEnabled"`
	CheckinEnabled      bool   `json:"checkinEnabled"`
	TicketEnabled       bool   `json:"ticketEnabled"`
	SharedEnabled       bool   `json:"sharedEnabled"`
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
