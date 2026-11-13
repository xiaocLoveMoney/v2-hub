package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"time"

	"v2-hub-server/internal/service"
)

// 系统设置与发件箱接口（docs/03 §5.6、§5.7），与 hub-api.yaml 的 admin-settings 逐条对应。

// SettingsManager 是系统设置接口需要的业务能力（由 service.SettingsService 实现）。
type SettingsManager interface {
	Current(ctx context.Context) (service.Settings, error)
	Update(ctx context.Context, actor *service.AdminPrincipal, patch map[string]json.RawMessage, ip *netip.Addr) (service.Settings, error)
}

// MailManager 是发件箱接口需要的业务能力（由 service.MailService 实现）。
type MailManager interface {
	Enabled() bool
	SendTest(ctx context.Context, actor *service.AdminPrincipal, to string, ip *netip.Addr) error
	ListOutbox(ctx context.Context, status string, pq service.PageQuery) (*service.Page[service.MailOutboxItem], error)
}

type siteSettingsVO struct {
	Name     string `json:"name"`
	Timezone string `json:"timezone"`
}

type registerSettingsVO struct {
	Mode        string   `json:"mode"`
	EmailVerify bool     `json:"emailVerify"`
	EmailAllow  []string `json:"emailAllow"`
	EmailBlock  []string `json:"emailBlock"`
	PlanID      int64    `json:"planId"`
}

type inviteSettingsVO struct {
	UserEnabled bool `json:"userEnabled"`
	UserMaxUses int  `json:"userMaxUses"`
}

type checkinSettingsVO struct {
	Enabled bool `json:"enabled"`
	MinMB   int  `json:"minMb"`
	MaxMB   int  `json:"maxMb"`
}

type ticketSettingsVO struct {
	Enabled     bool   `json:"enabled"`
	NotifyEmail string `json:"notifyEmail"`
}

type sharedSettingsVO struct {
	Enabled bool `json:"enabled"`
}

type mailSettingsVO struct {
	RatePerMinute int `json:"ratePerMinute"`
}

type subscriptionSettingsVO struct {
	BaseURL             string `json:"baseUrl"`
	Title               string `json:"title"`
	UpdateIntervalHours int    `json:"updateIntervalHours"`
	InfoNodes           bool   `json:"infoNodes"`
	LogRetentionDays    int    `json:"logRetentionDays"`
}

// settingsVO 对应 hub-api.yaml Settings。
type settingsVO struct {
	MailEnabled  bool                   `json:"mailEnabled"`
	Site         siteSettingsVO         `json:"site"`
	Register     registerSettingsVO     `json:"register"`
	Invite       inviteSettingsVO       `json:"invite"`
	Checkin      checkinSettingsVO      `json:"checkin"`
	Ticket       ticketSettingsVO       `json:"ticket"`
	Shared       sharedSettingsVO       `json:"shared"`
	Mail         mailSettingsVO         `json:"mail"`
	Subscription subscriptionSettingsVO `json:"subscription"`
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func toSettingsVO(s service.Settings, mailEnabled bool) settingsVO {
	return settingsVO{
		MailEnabled: mailEnabled,
		Site:        siteSettingsVO{Name: s.Site.Name, Timezone: s.Site.Timezone},
		Register: registerSettingsVO{Mode: s.Register.Mode, EmailVerify: s.Register.EmailVerify, EmailAllow: nonNil(s.Register.EmailAllow),
			EmailBlock: nonNil(s.Register.EmailBlock), PlanID: s.Register.PlanID},
		Invite:  inviteSettingsVO{UserEnabled: s.Invite.UserEnabled, UserMaxUses: s.Invite.UserMaxUses},
		Checkin: checkinSettingsVO{Enabled: s.Checkin.Enabled, MinMB: s.Checkin.MinMB, MaxMB: s.Checkin.MaxMB},
		Ticket:  ticketSettingsVO{Enabled: s.Ticket.Enabled, NotifyEmail: s.Ticket.NotifyEmail},
		Shared:  sharedSettingsVO{Enabled: s.Shared.Enabled},
		Mail:    mailSettingsVO{RatePerMinute: s.Mail.RatePerMinute},
		Subscription: subscriptionSettingsVO{BaseURL: s.Subscription.BaseURL, Title: s.Subscription.Title,
			UpdateIntervalHours: s.Subscription.UpdateIntervalHours, InfoNodes: s.Subscription.InfoNodes, LogRetentionDays: s.Subscription.LogRetentionDays},
	}
}

func (h *handlers) getSettings(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	cur, err := h.settings.Current(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toSettingsVO(cur, h.mail.Enabled()))
}

func (h *handlers) updateSettings(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	// 请求体是 {分组: {键: 值}}，键是否认识、取值是否合法由 service 按设置清单校验
	var groups map[string]json.RawMessage
	if err := decodeJSON(w, r, &groups); err != nil {
		writeError(w, r, err)
		return
	}
	patch, err := service.FlattenSettingsPatch(groups)
	if err != nil {
		writeError(w, r, err)
		return
	}
	next, err := h.settings.Update(r.Context(), p, patch, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toSettingsVO(next, h.mail.Enabled()))
}

type testMailRequest struct {
	To string `json:"to"`
}

func (h *handlers) sendTestMail(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req testMailRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.mail.SendTest(r.Context(), p, req.To, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

// mailOutboxVO 对应 hub-api.yaml MailOutboxItem。
type mailOutboxVO struct {
	ID        int64      `json:"id"`
	Recipient string     `json:"recipient"`
	Subject   string     `json:"subject"`
	Status    string     `json:"status"`
	Attempts  int        `json:"attempts"`
	LastError string     `json:"lastError"`
	CreatedAt time.Time  `json:"createdAt"`
	SentAt    *time.Time `json:"sentAt"`
}

type mailOutboxPageVO struct {
	Items []mailOutboxVO `json:"items"`
	Total int64          `json:"total"`
}

func (h *handlers) listMailOutbox(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.mail.ListOutbox(r.Context(), r.URL.Query().Get(queryStatus), pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := mailOutboxPageVO{Items: make([]mailOutboxVO, 0, len(page.Items)), Total: page.Total}
	for _, it := range page.Items {
		out.Items = append(out.Items, mailOutboxVO{ID: it.ID, Recipient: it.Recipient, Subject: it.Subject, Status: it.Status,
			Attempts: it.Attempts, LastError: it.LastError, CreatedAt: it.CreatedAt, SentAt: it.SentAt})
	}
	writeOK(w, out)
}
