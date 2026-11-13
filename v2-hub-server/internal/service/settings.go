package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
)

// 系统设置（docs/03 §5.6）：键与类型由本文件的清单固定；库里没有的键取默认值；凭据不进这张表（Ruling-24）。

// 注册方式（docs/03 §5.8）。
const (
	RegisterOpen   = "open"
	RegisterInvite = "invite"
	RegisterClosed = "closed"
)

const (
	settingsCacheTTL = 30 * time.Second

	defaultSiteName        = "v2-hub"
	defaultSiteTimezone    = "Asia/Shanghai"
	defaultInviteMaxUses   = 10
	defaultCheckinMinMB    = 100
	defaultCheckinMaxMB    = 500
	defaultMailRatePerMin  = 30
	defaultLogRetentionDay = 30

	maxSiteNameLen      = 32
	maxTimezoneLen      = 64
	maxEmailDomains     = 100
	maxDomainLen        = 253
	maxInviteUses       = 10000
	maxCheckinMB        = 102400
	maxMailRatePerMin   = 600
	maxSubTitleLen      = 64
	maxSubBaseURLLen    = 255
	maxSubIntervalHours = 168
	maxLogRetentionDays = 365
	maxEmailLen         = 254
	maxRebatePercent    = 100
	maxNotifyExpireDays = 30
	maxNotifyTrafficPct = 50

	defaultCurrency         = "CNY"
	defaultNotifyExpireDays = 3
	defaultNotifyTrafficPct = 10

	// 返利方式（docs/03 §5.16）。
	RebateFirst  = "first"
	RebateAlways = "always"

	ActionSettingUpdate = "setting.update"
	targetSetting       = "setting"

	msgSettingUnknown    = "不认识的设置项："
	msgSettingBadValue   = "设置项取值不合法："
	msgSettingCheckinMin = "签到奖励下限不能大于上限"
	msgSettingPlanGone   = "注册赠送的套餐不存在"
	msgSettingBadGroup   = "设置分组必须是对象："
)

var domainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// Settings 是全部系统设置的当前值，字段与 hub-api.yaml 的 Settings 一一对应。
type Settings struct {
	Site         SiteSettings
	Register     RegisterSettings
	Invite       InviteSettings
	Checkin      CheckinSettings
	Ticket       TicketSettings
	Shared       SharedSettings
	Mail         MailSettings
	Subscription SubscriptionSettings
	Shop         ShopSettings
	Rebate       RebateSettings
	Notify       NotifySettings
}

// SiteSettings 是站点名、时区与客户门户的对外地址。
type SiteSettings struct {
	Name     string
	Timezone string
	// PortalURL 是客户门户的对外地址，切换为用户身份时用；空串 = 与管理端同源（docs/03 §5.17）。
	PortalURL string
}

// ShopSettings 是商店开关与显示币种（docs/03 §5.12）。
type ShopSettings struct {
	Enabled  bool
	Currency string
}

// RebateSettings 是邀请返利（docs/03 §5.16）：Percent 为 0 即关闭。
type RebateSettings struct {
	Percent int
	Mode    string
}

// NotifySettings 是到期与流量提醒邮件的阈值（docs/03 §5.16）：0 = 不提醒。
type NotifySettings struct {
	ExpireDays     int
	TrafficPercent int
}

// shopCurrencies 是可选的显示币种；只影响前端显示，不做换算（Ruling-29）。
var shopCurrencies = []string{"CNY", "USD", "EUR", "HKD", "JPY"}

// RegisterSettings 是注册相关设置。
type RegisterSettings struct {
	Mode        string
	EmailVerify bool
	EmailAllow  []string
	EmailBlock  []string
	PlanID      int64
}

// InviteSettings 是个人邀请码设置。
type InviteSettings struct {
	UserEnabled bool
	UserMaxUses int
}

// CheckinSettings 是签到设置。
type CheckinSettings struct {
	Enabled bool
	MinMB   int
	MaxMB   int
}

// TicketSettings 是工单设置。
type TicketSettings struct {
	Enabled     bool
	NotifyEmail string
}

// SharedSettings 是共享账号页设置。
type SharedSettings struct {
	Enabled bool
}

// MailSettings 是发信速率。
type MailSettings struct {
	RatePerMinute int
}

// SubscriptionSettings 是订阅设置（docs/03 §5.4）。
type SubscriptionSettings struct {
	BaseURL             string
	Title               string
	UpdateIntervalHours int
	InfoNodes           bool
	LogRetentionDays    int
}

// Location 返回 site.timezone 对应的时区；设置写入时已校验，解析失败只可能是运行环境缺时区库，此时退回 UTC。
func (s Settings) Location() *time.Location {
	loc, err := time.LoadLocation(s.Site.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// settingDef 描述一个设置项：怎么取当前值、怎么校验并写入新值。
type settingDef struct {
	get func(s *Settings) any
	set func(s *Settings, raw json.RawMessage) error
}

func decodeSetting[T any](raw json.RawMessage, check func(v T) bool, assign func(v T)) error {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	if check != nil && !check(v) {
		return errSettingRange
	}
	assign(v)
	return nil
}

var errSettingRange = fmt.Errorf("out of range")

func intBetween(lo, hi int) func(int) bool {
	return func(v int) bool { return v >= lo && v <= hi }
}

func textBetween(lo, hi int) func(string) bool {
	return func(v string) bool { n := len([]rune(v)); return n >= lo && n <= hi && v == strings.TrimSpace(v) }
}

func validDomains(v []string) bool {
	if len(v) > maxEmailDomains {
		return false
	}
	for _, d := range v {
		if len(d) > maxDomainLen || !domainPattern.MatchString(d) {
			return false
		}
	}
	return true
}

func validTimezone(v string) bool {
	if v == "" || len(v) > maxTimezoneLen {
		return false
	}
	_, err := time.LoadLocation(v)
	return err == nil
}

func validBaseURL(v string) bool {
	if v == "" {
		return true
	}
	u, err := url.Parse(v)
	return err == nil && len(v) <= maxSubBaseURLLen && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.RawQuery == "" && u.Fragment == ""
}

func validOptionalEmail(v string) bool {
	return v == "" || normalizeEmail(v) == v
}

// normalizeEmail 返回小写的裸邮箱地址；不是合法的单个地址时返回空串。
func normalizeEmail(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" || len(v) > maxEmailLen {
		return ""
	}
	addr, err := mail.ParseAddress(v)
	if err != nil || addr.Address != v {
		return ""
	}
	at := strings.LastIndexByte(v, '@')
	if at < 1 || !domainPattern.MatchString(v[at+1:]) {
		return ""
	}
	return v
}

func lowerAll(v []string) []string {
	out := make([]string, 0, len(v))
	for _, d := range v {
		d = strings.ToLower(strings.TrimSpace(d))
		if !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

// settingDefs 是设置清单，键名与 docs/03 §5.6 的表逐行对应。
var settingDefs = map[string]settingDef{
	"site.name": {
		get: func(s *Settings) any { return s.Site.Name },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, textBetween(1, maxSiteNameLen), func(v string) { s.Site.Name = v })
		},
	},
	"site.timezone": {
		get: func(s *Settings) any { return s.Site.Timezone },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, validTimezone, func(v string) { s.Site.Timezone = v })
		},
	},
	"site.portalUrl": {
		get: func(s *Settings) any { return s.Site.PortalURL },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, validBaseURL, func(v string) { s.Site.PortalURL = strings.TrimRight(v, "/") })
		},
	},
	"shop.enabled": {
		get: func(s *Settings) any { return s.Shop.Enabled },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, nil, func(v bool) { s.Shop.Enabled = v })
		},
	},
	"shop.currency": {
		get: func(s *Settings) any { return s.Shop.Currency },
		set: func(s *Settings, raw json.RawMessage) error {
			ok := func(v string) bool { return slices.Contains(shopCurrencies, v) }
			return decodeSetting(raw, ok, func(v string) { s.Shop.Currency = v })
		},
	},
	"rebate.percent": {
		get: func(s *Settings) any { return s.Rebate.Percent },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, intBetween(0, maxRebatePercent), func(v int) { s.Rebate.Percent = v })
		},
	},
	"rebate.mode": {
		get: func(s *Settings) any { return s.Rebate.Mode },
		set: func(s *Settings, raw json.RawMessage) error {
			ok := func(v string) bool { return v == RebateFirst || v == RebateAlways }
			return decodeSetting(raw, ok, func(v string) { s.Rebate.Mode = v })
		},
	},
	"notify.expireDays": {
		get: func(s *Settings) any { return s.Notify.ExpireDays },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, intBetween(0, maxNotifyExpireDays), func(v int) { s.Notify.ExpireDays = v })
		},
	},
	"notify.trafficPercent": {
		get: func(s *Settings) any { return s.Notify.TrafficPercent },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, intBetween(0, maxNotifyTrafficPct), func(v int) { s.Notify.TrafficPercent = v })
		},
	},
	"register.mode": {
		get: func(s *Settings) any { return s.Register.Mode },
		set: func(s *Settings, raw json.RawMessage) error {
			ok := func(v string) bool { return v == RegisterOpen || v == RegisterInvite || v == RegisterClosed }
			return decodeSetting(raw, ok, func(v string) { s.Register.Mode = v })
		},
	},
	"register.emailVerify": {
		get: func(s *Settings) any { return s.Register.EmailVerify },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, nil, func(v bool) { s.Register.EmailVerify = v })
		},
	},
	"register.emailAllow": {
		get: func(s *Settings) any { return s.Register.EmailAllow },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, func(v []string) bool { return validDomains(lowerAll(v)) }, func(v []string) { s.Register.EmailAllow = lowerAll(v) })
		},
	},
	"register.emailBlock": {
		get: func(s *Settings) any { return s.Register.EmailBlock },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, func(v []string) bool { return validDomains(lowerAll(v)) }, func(v []string) { s.Register.EmailBlock = lowerAll(v) })
		},
	},
	"register.planId": {
		get: func(s *Settings) any { return s.Register.PlanID },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, func(v int64) bool { return v >= 0 }, func(v int64) { s.Register.PlanID = v })
		},
	},
	"invite.userEnabled": {
		get: func(s *Settings) any { return s.Invite.UserEnabled },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, nil, func(v bool) { s.Invite.UserEnabled = v })
		},
	},
	"invite.userMaxUses": {
		get: func(s *Settings) any { return s.Invite.UserMaxUses },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, intBetween(0, maxInviteUses), func(v int) { s.Invite.UserMaxUses = v })
		},
	},
	"checkin.enabled": {
		get: func(s *Settings) any { return s.Checkin.Enabled },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, nil, func(v bool) { s.Checkin.Enabled = v })
		},
	},
	"checkin.minMb": {
		get: func(s *Settings) any { return s.Checkin.MinMB },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, intBetween(1, maxCheckinMB), func(v int) { s.Checkin.MinMB = v })
		},
	},
	"checkin.maxMb": {
		get: func(s *Settings) any { return s.Checkin.MaxMB },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, intBetween(1, maxCheckinMB), func(v int) { s.Checkin.MaxMB = v })
		},
	},
	"ticket.enabled": {
		get: func(s *Settings) any { return s.Ticket.Enabled },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, nil, func(v bool) { s.Ticket.Enabled = v })
		},
	},
	"ticket.notifyEmail": {
		get: func(s *Settings) any { return s.Ticket.NotifyEmail },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, validOptionalEmail, func(v string) { s.Ticket.NotifyEmail = v })
		},
	},
	"shared.enabled": {
		get: func(s *Settings) any { return s.Shared.Enabled },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, nil, func(v bool) { s.Shared.Enabled = v })
		},
	},
	"mail.ratePerMinute": {
		get: func(s *Settings) any { return s.Mail.RatePerMinute },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, intBetween(1, maxMailRatePerMin), func(v int) { s.Mail.RatePerMinute = v })
		},
	},
	"subscription.baseUrl": {
		get: func(s *Settings) any { return s.Subscription.BaseURL },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, validBaseURL, func(v string) { s.Subscription.BaseURL = strings.TrimRight(v, "/") })
		},
	},
	"subscription.title": {
		get: func(s *Settings) any { return s.Subscription.Title },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, textBetween(1, maxSubTitleLen), func(v string) { s.Subscription.Title = v })
		},
	},
	"subscription.updateIntervalHours": {
		get: func(s *Settings) any { return s.Subscription.UpdateIntervalHours },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, intBetween(1, maxSubIntervalHours), func(v int) { s.Subscription.UpdateIntervalHours = v })
		},
	},
	"subscription.infoNodes": {
		get: func(s *Settings) any { return s.Subscription.InfoNodes },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, nil, func(v bool) { s.Subscription.InfoNodes = v })
		},
	},
	"subscription.logRetentionDays": {
		get: func(s *Settings) any { return s.Subscription.LogRetentionDays },
		set: func(s *Settings, raw json.RawMessage) error {
			return decodeSetting(raw, intBetween(1, maxLogRetentionDays), func(v int) { s.Subscription.LogRetentionDays = v })
		},
	},
}

// SettingsService 读写系统设置，并在进程内缓存当前值。并发安全。
type SettingsService struct {
	store
	defaults Settings
	now      func() time.Time

	mu       sync.Mutex
	cached   Settings
	cachedAt time.Time
}

// NewSettingsService 创建设置服务。sub 是配置文件里的订阅设置，作为订阅各键的默认值（docs/03 §5.6）；now 为 nil 时用 time.Now。
func NewSettingsService(pool *pgxpool.Pool, sub SubscriptionConfig, now func() time.Time) *SettingsService {
	if now == nil {
		now = time.Now
	}
	return &SettingsService{store: newStore(pool), defaults: defaultSettings(sub), now: now}
}

func defaultSettings(sub SubscriptionConfig) Settings {
	title, interval, retention := sub.Title, sub.UpdateIntervalHours, int(sub.LogRetention/(24*time.Hour))
	if title == "" {
		title = defaultSubscriptionTitle
	}
	if interval <= 0 {
		interval = defaultSubscriptionIntervalHours
	}
	if retention <= 0 {
		retention = defaultLogRetentionDay
	}
	return Settings{
		Site:     SiteSettings{Name: defaultSiteName, Timezone: defaultSiteTimezone},
		Register: RegisterSettings{Mode: RegisterInvite, EmailVerify: true, EmailAllow: []string{}, EmailBlock: []string{}},
		Invite:   InviteSettings{UserEnabled: true, UserMaxUses: defaultInviteMaxUses},
		Checkin:  CheckinSettings{MinMB: defaultCheckinMinMB, MaxMB: defaultCheckinMaxMB},
		Ticket:   TicketSettings{Enabled: true},
		Mail:     MailSettings{RatePerMinute: defaultMailRatePerMin},
		Shop:     ShopSettings{Currency: defaultCurrency},
		Rebate:   RebateSettings{Mode: RebateFirst},
		Notify:   NotifySettings{ExpireDays: defaultNotifyExpireDays, TrafficPercent: defaultNotifyTrafficPct},
		Subscription: SubscriptionSettings{
			BaseURL: strings.TrimRight(sub.BaseURL, "/"), Title: title, UpdateIntervalHours: interval, InfoNodes: sub.InfoNodes, LogRetentionDays: retention,
		},
	}
}

// Current 返回当前设置；缓存超过 30 秒或尚未加载时读库。
func (s *SettingsService) Current(ctx context.Context) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cachedAt.IsZero() && s.now().Sub(s.cachedAt) < settingsCacheTTL {
		return s.cached, nil
	}
	cur, err := s.load(ctx, s.q)
	if err != nil {
		return Settings{}, err
	}
	s.cached, s.cachedAt = cur, s.now()
	return cur, nil
}

// load 以默认值为底，叠加库里的值。库里不认识的键或解析不了的值（旧版本遗留）跳过并取默认值。
func (s *SettingsService) load(ctx context.Context, q *sqlcgen.Queries) (Settings, error) {
	rows, err := q.ListSettings(ctx)
	if err != nil {
		return Settings{}, fmt.Errorf("list settings: %w", err)
	}
	cur := s.defaults
	cur.Register.EmailAllow = slices.Clone(s.defaults.Register.EmailAllow)
	cur.Register.EmailBlock = slices.Clone(s.defaults.Register.EmailBlock)
	for _, row := range rows {
		def, ok := settingDefs[row.Key]
		if !ok {
			continue
		}
		if err := def.set(&cur, row.Value); err != nil {
			return Settings{}, fmt.Errorf("stored setting %s is invalid: %w", row.Key, err)
		}
	}
	return cur, nil
}

// Update 修改设置：patch 的键是“分组.键”（如 register.mode），只改出现的键；任一项不合法则整体不保存。
// 返回修改后的全部设置。错误：CodeInvalidArgument。
func (s *SettingsService) Update(ctx context.Context, actor *AdminPrincipal, patch map[string]json.RawMessage, ip *netip.Addr) (Settings, error) {
	keys := make([]string, 0, len(patch))
	for k := range patch {
		if _, ok := settingDefs[k]; !ok {
			return Settings{}, newError(CodeInvalidArgument, msgSettingUnknown+k)
		}
		keys = append(keys, k)
	}
	slices.Sort(keys)

	var next Settings
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		cur, err := s.load(ctx, q)
		if err != nil {
			return err
		}
		next = cur
		for _, k := range keys {
			if err := settingDefs[k].set(&next, patch[k]); err != nil {
				return newError(CodeInvalidArgument, msgSettingBadValue+k)
			}
		}
		if err := s.checkCrossFields(ctx, q, next); err != nil {
			return err
		}
		for _, k := range keys {
			raw, err := json.Marshal(settingDefs[k].get(&next))
			if err != nil {
				return fmt.Errorf("marshal setting %s: %w", k, err)
			}
			if err := q.UpsertSetting(ctx, sqlcgen.UpsertSettingParams{Key: k, Value: raw, UpdatedBy: pgtype.Int8{Int64: actor.AdminID, Valid: true}}); err != nil {
				return fmt.Errorf("upsert setting %s: %w", k, err)
			}
		}
		// 审计只记键名，不记值（docs/03 §5.6 修改）
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionSettingUpdate, TargetType: targetSetting,
			Detail: map[string]any{"keys": keys}, ClientIP: ip})
	})
	if err != nil {
		return Settings{}, err
	}
	s.mu.Lock()
	s.cached, s.cachedAt = next, s.now()
	s.mu.Unlock()
	return next, nil
}

func (s *SettingsService) checkCrossFields(ctx context.Context, q *sqlcgen.Queries, next Settings) error {
	if next.Checkin.MinMB > next.Checkin.MaxMB {
		return newError(CodeInvalidArgument, msgSettingCheckinMin)
	}
	if next.Register.PlanID == 0 {
		return nil
	}
	ok, err := q.ExistsPlan(ctx, next.Register.PlanID)
	if err != nil {
		return fmt.Errorf("check register plan: %w", err)
	}
	if !ok {
		return newError(CodeInvalidArgument, msgSettingPlanGone)
	}
	return nil
}

// FlattenSettingsPatch 把接口收到的 {分组: {键: 值}} 摊平成 {"分组.键": 值}。分组不是对象时返回 CodeInvalidArgument。
func FlattenSettingsPatch(groups map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage, len(settingDefs))
	for group, raw := range groups {
		var items map[string]json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, newError(CodeInvalidArgument, msgSettingBadGroup+group)
		}
		for k, v := range items {
			out[group+"."+k] = v
		}
	}
	return out, nil
}

// subscriptionConfig 把设置里的订阅段转成订阅服务用的形状。
func (s Settings) subscriptionConfig() SubscriptionConfig {
	sub := s.Subscription
	return SubscriptionConfig{
		BaseURL: sub.BaseURL, Title: sub.Title, UpdateIntervalHours: sub.UpdateIntervalHours, InfoNodes: sub.InfoNodes,
		LogRetention: time.Duration(sub.LogRetentionDays) * 24 * time.Hour,
	}
}
