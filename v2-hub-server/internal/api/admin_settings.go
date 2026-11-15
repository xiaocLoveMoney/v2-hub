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

// TelegramManager 是 Telegram 测试接口需要的能力（由 service.TelegramService 实现，docs/03 §5.20）。
type TelegramManager interface {
	Enabled() bool
	SendTest(ctx context.Context, actor *service.AdminPrincipal, ip *netip.Addr) error
}

func (h *handlers) sendTestTelegram(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	if err := h.telegram.SendTest(r.Context(), p, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

// MailManager 是发件箱接口需要的业务能力（由 service.MailService 实现）。
type MailManager interface {
	Enabled() bool
	SendTest(ctx context.Context, actor *service.AdminPrincipal, to string, ip *netip.Addr) error
	ListOutbox(ctx context.Context, status string, pq service.PageQuery) (*service.Page[service.MailOutboxItem], error)
}

type siteSettingsVO struct {
	Name      string `json:"name"`
	Timezone  string `json:"timezone"`
	PortalURL string `json:"portalUrl"`
}

type shopSettingsVO struct {
	Enabled  bool   `json:"enabled"`
	Currency string `json:"currency"`
}

type rebateSettingsVO struct {
	Percent int    `json:"percent"`
	Mode    string `json:"mode"`
}

type notifySettingsVO struct {
	ExpireDays     int `json:"expireDays"`
	TrafficPercent int `json:"trafficPercent"`
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
	Path                string `json:"path"`
	PathGraceDays       int    `json:"pathGraceDays"`
	OldPath             string `json:"oldPath"`
	// OldPathUntil 为 nil 表示没有过渡中的旧前缀。
	OldPathUntil *time.Time `json:"oldPathUntil"`
}

// optionalTime 把零值时间转成 nil（接口里写 null）。
func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

type alertSettingsVO struct {
	Emails         []string `json:"emails"`
	OfflineMinutes int      `json:"offlineMinutes"`
}

type telegramSettingsVO struct {
	ChatID         string `json:"chatId"`
	NotifyAlerts   bool   `json:"notifyAlerts"`
	NotifyTickets  bool   `json:"notifyTickets"`
	NotifyPayments bool   `json:"notifyPayments"`
}

type reportSettingsVO struct {
	DailyEnabled bool `json:"dailyEnabled"`
	DailyHour    int  `json:"dailyHour"`
}

type landingSettingsVO struct {
	Enabled  bool   `json:"enabled"`
	Headline string `json:"headline"`
	Intro    string `json:"intro"`
}

type supportSettingsVO struct {
	URL   string `json:"url"`
	Label string `json:"label"`
}

type routingSettingsVO struct {
	BlockRule bool `json:"blockRule"`
}

// reachSettingsVO 是大陆连通检测设置；HasToken 只读，表示环境变量里配了探测服务的令牌。
type reachSettingsVO struct {
	Enabled         bool `json:"enabled"`
	IntervalMinutes int  `json:"intervalMinutes"`
	Notify          bool `json:"notify"`
	HasToken        bool `json:"hasToken"`
}

type menuSettingsVO struct {
	Admin []string `json:"admin"`
	User  []string `json:"user"`
}

// settingsVO 对应 hub-api.yaml Settings。
type settingsVO struct {
	MailEnabled     bool                   `json:"mailEnabled"`
	TelegramEnabled bool                   `json:"telegramEnabled"`
	Alert           alertSettingsVO        `json:"alert"`
	Telegram        telegramSettingsVO     `json:"telegram"`
	Report          reportSettingsVO       `json:"report"`
	Landing         landingSettingsVO      `json:"landing"`
	Support         supportSettingsVO      `json:"support"`
	Routing         routingSettingsVO      `json:"routing"`
	Reach           reachSettingsVO        `json:"reach"`
	Menu            menuSettingsVO         `json:"menu"`
	Site            siteSettingsVO         `json:"site"`
	Register        registerSettingsVO     `json:"register"`
	Invite          inviteSettingsVO       `json:"invite"`
	Checkin         checkinSettingsVO      `json:"checkin"`
	Ticket          ticketSettingsVO       `json:"ticket"`
	Shared          sharedSettingsVO       `json:"shared"`
	Mail            mailSettingsVO         `json:"mail"`
	Subscription    subscriptionSettingsVO `json:"subscription"`
	Shop            shopSettingsVO         `json:"shop"`
	Rebate          rebateSettingsVO       `json:"rebate"`
	Notify          notifySettingsVO       `json:"notify"`
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func toSettingsVO(s service.Settings, mailEnabled, telegramEnabled, hasReachToken bool) settingsVO {
	return settingsVO{
		MailEnabled: mailEnabled, TelegramEnabled: telegramEnabled,
		Alert: alertSettingsVO{Emails: nonNil(s.Alert.Emails), OfflineMinutes: s.Alert.OfflineMinutes},
		Telegram: telegramSettingsVO{ChatID: s.Telegram.ChatID, NotifyAlerts: s.Telegram.NotifyAlerts, NotifyTickets: s.Telegram.NotifyTickets,
			NotifyPayments: s.Telegram.NotifyPayments},
		Report:  reportSettingsVO{DailyEnabled: s.Report.DailyEnabled, DailyHour: s.Report.DailyHour},
		Landing: landingSettingsVO{Enabled: s.Landing.Enabled, Headline: s.Landing.Headline, Intro: s.Landing.Intro},
		Support: supportSettingsVO{URL: s.Support.URL, Label: s.Support.Label},
		Routing: routingSettingsVO{BlockRule: s.Routing.BlockRule},
		Reach:   reachSettingsVO{Enabled: s.Reach.Enabled, IntervalMinutes: s.Reach.IntervalMinutes, Notify: s.Reach.Notify, HasToken: hasReachToken},
		Menu:    menuSettingsVO{Admin: nonNil(s.Menu.Admin), User: nonNil(s.Menu.User)},
		Site:    siteSettingsVO{Name: s.Site.Name, Timezone: s.Site.Timezone, PortalURL: s.Site.PortalURL},
		Shop:    shopSettingsVO{Enabled: s.Shop.Enabled, Currency: s.Shop.Currency},
		Rebate:  rebateSettingsVO{Percent: s.Rebate.Percent, Mode: s.Rebate.Mode},
		Notify:  notifySettingsVO{ExpireDays: s.Notify.ExpireDays, TrafficPercent: s.Notify.TrafficPercent},
		Register: registerSettingsVO{Mode: s.Register.Mode, EmailVerify: s.Register.EmailVerify, EmailAllow: nonNil(s.Register.EmailAllow),
			EmailBlock: nonNil(s.Register.EmailBlock), PlanID: s.Register.PlanID},
		Invite:  inviteSettingsVO{UserEnabled: s.Invite.UserEnabled, UserMaxUses: s.Invite.UserMaxUses},
		Checkin: checkinSettingsVO{Enabled: s.Checkin.Enabled, MinMB: s.Checkin.MinMB, MaxMB: s.Checkin.MaxMB},
		Ticket:  ticketSettingsVO{Enabled: s.Ticket.Enabled, NotifyEmail: s.Ticket.NotifyEmail},
		Shared:  sharedSettingsVO{Enabled: s.Shared.Enabled},
		Mail:    mailSettingsVO{RatePerMinute: s.Mail.RatePerMinute},
		Subscription: subscriptionSettingsVO{BaseURL: s.Subscription.BaseURL, Title: s.Subscription.Title,
			UpdateIntervalHours: s.Subscription.UpdateIntervalHours, InfoNodes: s.Subscription.InfoNodes, LogRetentionDays: s.Subscription.LogRetentionDays,
			Path: s.Subscription.Path, PathGraceDays: s.Subscription.PathGraceDays, OldPath: s.Subscription.OldPath,
			OldPathUntil: optionalTime(s.Subscription.OldPathUntil)},
	}
}

// hasReachToken 报告探测服务是否带了令牌；没接连通检测时为假。
func (h *handlers) hasReachToken() bool {
	return h.reach != nil && h.reach.HasToken()
}

func (h *handlers) getSettings(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	cur, err := h.settings.Current(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toSettingsVO(cur, h.mail.Enabled(), h.telegram.Enabled(), h.hasReachToken()))
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
	writeOK(w, toSettingsVO(next, h.mail.Enabled(), h.telegram.Enabled(), h.hasReachToken()))
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
	writeOK(w, toPageVO(page, func(it service.MailOutboxItem) mailOutboxVO {
		return mailOutboxVO{ID: it.ID, Recipient: it.Recipient, Subject: it.Subject, Status: it.Status,
			Attempts: it.Attempts, LastError: it.LastError, CreatedAt: it.CreatedAt, SentAt: it.SentAt}
	}))
}
