package api

import (
	"context"
	"net/http"
	"time"

	"v2-hub-server/internal/service"
)

// 总览分析接口（docs/03 §5.20 总览分析，Ruling-66）与大陆连通检测接口（Ruling-67）。

const msgBadAnalyticsDays = "days 只能是 7、30 或 90"

// AnalyticsManager 是总览分析需要的能力（由 service.AnalyticsService 实现）。
type AnalyticsManager interface {
	Users(ctx context.Context, days int) (*service.AnalyticsUsers, error)
	Traffic(ctx context.Context, days int, withServers bool) (*service.AnalyticsTraffic, error)
	Servers(ctx context.Context, rng string) (*service.AnalyticsServers, error)
	Finance(ctx context.Context, days int) (*service.AnalyticsFinance, error)
	Backlog(ctx context.Context, p *service.AdminPrincipal) (*service.AnalyticsBacklog, error)
}

// ReachManager 是大陆连通检测需要的能力（由 service.ReachService 实现）。
type ReachManager interface {
	HasToken() bool
	NodeBriefs(ctx context.Context) (map[int64]service.ReachBrief, error)
	ServerCounts(ctx context.Context) (map[int64]service.ReachCount, error)
	NodeDetail(ctx context.Context, nodeID int64) (*service.ReachDetail, error)
	CheckNode(ctx context.Context, actor *service.AdminPrincipal, nodeID int64) (*service.ReachDetail, error)
}

type userGrowthDayVO struct {
	Date       string `json:"date"`
	NewUsers   int    `json:"newUsers"`
	TotalUsers int    `json:"totalUsers"`
}

type activeDayVO struct {
	Date  string `json:"date"`
	Users int    `json:"users"`
}

type userStatusVO struct {
	Active   int `json:"active"`
	Limited  int `json:"limited"`
	Expired  int `json:"expired"`
	Disabled int `json:"disabled"`
}

type userExpiryVO struct {
	Expired             int `json:"expired"`
	Within7d            int `json:"within7d"`
	Within30d           int `json:"within30d"`
	Later               int `json:"later"`
	Never               int `json:"never"`
	Within7dNoAutoRenew int `json:"within7dNoAutoRenew"`
}

type planUsersVO struct {
	PlanID      *int64 `json:"planId"`
	Name        string `json:"name"`
	Users       int    `json:"users"`
	ActiveUsers int    `json:"activeUsers"`
}

type topUserVO struct {
	UserID   int64  `json:"userId"`
	Username string `json:"username"`
	PlanName string `json:"planName"`
	Bytes    int64  `json:"bytes"`
}

type subscribeDayVO struct {
	Date  string `json:"date"`
	Pulls int    `json:"pulls"`
	Users int    `json:"users"`
}

type subscribeClientVO struct {
	Client string `json:"client"`
	Pulls  int    `json:"pulls"`
	Users  int    `json:"users"`
}

type subscribeFormatVO struct {
	Format string `json:"format"`
	Pulls  int    `json:"pulls"`
}

type analyticsSubscriptionVO struct {
	Days    []subscribeDayVO    `json:"days"`
	Clients []subscribeClientVO `json:"clients"`
	Formats []subscribeFormatVO `json:"formats"`
}

// analyticsUsersVO 对应 hub-api.yaml AnalyticsUsers。
type analyticsUsersVO struct {
	Growth       []userGrowthDayVO       `json:"growth"`
	ActiveDays   []activeDayVO           `json:"activeDays"`
	DAU          int                     `json:"dau"`
	WAU          int                     `json:"wau"`
	MAU          int                     `json:"mau"`
	Status       userStatusVO            `json:"status"`
	Expiry       userExpiryVO            `json:"expiry"`
	Plans        []planUsersVO           `json:"plans"`
	SilentUsers  int                     `json:"silentUsers"`
	TopUsers     []topUserVO             `json:"topUsers"`
	Subscription analyticsSubscriptionVO `json:"subscription"`
}

func toAnalyticsUsersVO(a *service.AnalyticsUsers) analyticsUsersVO {
	return analyticsUsersVO{
		Growth: mapSlice(a.Growth, func(d service.UserGrowthDay) userGrowthDayVO {
			return userGrowthDayVO{Date: d.Date, NewUsers: d.NewUsers, TotalUsers: d.TotalUsers}
		}),
		ActiveDays: mapSlice(a.ActiveDays, func(d service.CountDay) activeDayVO { return activeDayVO{Date: d.Date, Users: d.Count} }),
		DAU:        a.DAU, WAU: a.WAU, MAU: a.MAU,
		Status: userStatusVO{Active: a.Status.Active, Limited: a.Status.Limited, Expired: a.Status.Expired, Disabled: a.Status.Disabled},
		Expiry: userExpiryVO{
			Expired: a.Expiry.Expired, Within7d: a.Expiry.Within7d, Within30d: a.Expiry.Within30d, Later: a.Expiry.Later,
			Never: a.Expiry.Never, Within7dNoAutoRenew: a.Expiry.Within7dNoAutoRenew,
		},
		Plans: mapSlice(a.Plans, func(p service.PlanUsers) planUsersVO {
			return planUsersVO{PlanID: p.PlanID, Name: p.Name, Users: p.Users, ActiveUsers: p.ActiveUsers}
		}),
		SilentUsers: a.SilentUsers,
		TopUsers: mapSlice(a.TopUsers, func(u service.TopUser) topUserVO {
			return topUserVO{UserID: u.UserID, Username: u.Username, PlanName: u.PlanName, Bytes: u.Bytes}
		}),
		Subscription: analyticsSubscriptionVO{
			Days: mapSlice(a.SubscribeDays, func(d service.SubscribeDay) subscribeDayVO {
				return subscribeDayVO{Date: d.Date, Pulls: d.Pulls, Users: d.Users}
			}),
			Clients: mapSlice(a.SubscribeClients, func(c service.SubscribeClient) subscribeClientVO {
				return subscribeClientVO{Client: c.Client, Pulls: c.Pulls, Users: c.Users}
			}),
			Formats: mapSlice(a.SubscribeFormats, func(f service.SubscribeFormat) subscribeFormatVO {
				return subscribeFormatVO{Format: f.Format, Pulls: f.Pulls}
			}),
		},
	}
}

// analyticsDays 读取 days 查询参数：缺省返回 0（由 service 取默认），不是正整数返回 400；取值范围由 service 校验。
func analyticsDays(r *http.Request) (int, error) {
	return optionalPositiveInt(r, queryDays, msgBadAnalyticsDays)
}

func (h *handlers) getAnalyticsUsers(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	days, err := analyticsDays(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a, err := h.analytics.Users(r.Context(), days)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toAnalyticsUsersVO(a))
}

type trafficDayVO struct {
	Date string `json:"date"`
	Up   int64  `json:"up"`
	Down int64  `json:"down"`
}

type trafficHourVO struct {
	Hour  int   `json:"hour"`
	Bytes int64 `json:"bytes"`
}

type serverTrafficVO struct {
	ServerID int64  `json:"serverId"`
	Name     string `json:"name"`
	Up       int64  `json:"up"`
	Down     int64  `json:"down"`
}

// analyticsTrafficVO 对应 hub-api.yaml AnalyticsTraffic；servers 为 null 表示没有 server:read。
type analyticsTrafficVO struct {
	Days    []trafficDayVO    `json:"days"`
	Hours   []trafficHourVO   `json:"hours"`
	Servers []serverTrafficVO `json:"servers"`
}

func (h *handlers) getAnalyticsTraffic(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	days, err := analyticsDays(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	canSeeServers := p.Can(service.PermServerRead)
	a, err := h.analytics.Traffic(r.Context(), days, canSeeServers)
	if err != nil {
		writeError(w, r, err)
		return
	}
	vo := analyticsTrafficVO{
		Days:  mapSlice(a.Days, func(d service.TrafficDay) trafficDayVO { return trafficDayVO{Date: d.Date, Up: d.Up, Down: d.Down} }),
		Hours: mapSlice(a.Hours, func(x service.TrafficHour) trafficHourVO { return trafficHourVO{Hour: x.Hour, Bytes: x.Bytes} }),
	}
	if canSeeServers {
		vo.Servers = mapSlice(a.Servers, func(s service.ServerTraffic) serverTrafficVO {
			return serverTrafficVO{ServerID: s.ServerID, Name: s.Name, Up: s.Up, Down: s.Down}
		})
	}
	writeOK(w, vo)
}

type serverAvailabilityVO struct {
	ServerID int64   `json:"serverId"`
	Name     string  `json:"name"`
	Percent  float64 `json:"percent"`
	Samples  int     `json:"samples"`
	Expected int     `json:"expected"`
}

type onlinePointVO struct {
	At          time.Time `json:"at"`
	Connections int       `json:"connections"`
}

type serverLoadVO struct {
	ServerID      int64    `json:"serverId"`
	Name          string   `json:"name"`
	CPU           *float64 `json:"cpu"`
	OnlineClients int      `json:"onlineClients"`
}

type reachCountsVO struct {
	Pending  int `json:"pending"`
	OK       int `json:"ok"`
	Partial  int `json:"partial"`
	Blocked  int `json:"blocked"`
	PortDown int `json:"portDown"`
	Unknown  int `json:"unknown"`
}

// reachProblemVO 对应 hub-api.yaml ReachProblem。
type reachProblemVO struct {
	Address      string     `json:"address"`
	Port         int        `json:"port"`
	Transport    string     `json:"transport"`
	Verdict      string     `json:"verdict"`
	Scope        string     `json:"scope"`
	CnOK         int        `json:"cnOk"`
	CnTotal      int        `json:"cnTotal"`
	IPReachable  *bool      `json:"ipReachable"`
	CheckedAt    *time.Time `json:"checkedAt"`
	BlockedSince *time.Time `json:"blockedSince"`
	Servers      []string   `json:"servers"`
	Nodes        []string   `json:"nodes"`
	NodeCount    int        `json:"nodeCount"`
}

type reachSummaryVO struct {
	Counts   reachCountsVO    `json:"counts"`
	Problems []reachProblemVO `json:"problems"`
}

// analyticsServersVO 对应 hub-api.yaml AnalyticsServers；reach 为 null 表示大陆连通检测没开。
type analyticsServersVO struct {
	Availability []serverAvailabilityVO `json:"availability"`
	Online       []onlinePointVO        `json:"online"`
	TopCPU       []serverLoadVO         `json:"topCpu"`
	TopOnline    []serverLoadVO         `json:"topOnline"`
	Reach        *reachSummaryVO        `json:"reach"`
}

func toServerLoadVO(l service.ServerLoad) serverLoadVO {
	return serverLoadVO{ServerID: l.ServerID, Name: l.Name, CPU: l.CPU, OnlineClients: l.OnlineClients}
}

func toReachSummaryVO(s *service.ReachSummary) *reachSummaryVO {
	if s == nil {
		return nil
	}
	return &reachSummaryVO{
		Counts: reachCountsVO{Pending: s.Pending, OK: s.OK, Partial: s.Partial, Blocked: s.Blocked, PortDown: s.PortDown, Unknown: s.Unknown},
		Problems: mapSlice(s.Problems, func(p service.ReachProblem) reachProblemVO {
			return reachProblemVO{
				Address: p.Address, Port: p.Port, Transport: p.Transport, Verdict: p.Verdict, Scope: p.Scope, CnOK: p.CnOK, CnTotal: p.CnTotal,
				IPReachable: p.IPReachable, CheckedAt: p.CheckedAt, BlockedSince: p.BlockedSince,
				Servers: nonNil(p.Servers), Nodes: nonNil(p.Nodes), NodeCount: p.NodeCount,
			}
		}),
	}
}

func (h *handlers) getAnalyticsServers(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	a, err := h.analytics.Servers(r.Context(), r.URL.Query().Get(queryMetricRange))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, analyticsServersVO{
		Availability: mapSlice(a.Availability, func(s service.ServerAvailability) serverAvailabilityVO {
			return serverAvailabilityVO{ServerID: s.ServerID, Name: s.Name, Percent: s.Percent, Samples: s.Samples, Expected: s.Expected}
		}),
		Online: mapSlice(a.Online, func(p service.OnlinePoint) onlinePointVO {
			return onlinePointVO{At: p.At, Connections: p.Connections}
		}),
		TopCPU: mapSlice(a.TopCPU, toServerLoadVO), TopOnline: mapSlice(a.TopOnline, toServerLoadVO),
		Reach: toReachSummaryVO(a.Reach),
	})
}

type financeDayVO struct {
	Date      string `json:"date"`
	OrderPaid int64  `json:"orderPaid"`
	Topup     int64  `json:"topup"`
}

type orderKindVO struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
	Paid  int64  `json:"paid"`
}

type planSalesVO struct {
	PlanID *int64 `json:"planId"`
	Name   string `json:"name"`
	Count  int    `json:"count"`
	Paid   int64  `json:"paid"`
}

type funnelVO struct {
	Registered int `json:"registered"`
	Trial      int `json:"trial"`
	Paid       int `json:"paid"`
}

type payersVO struct {
	Users int   `json:"users"`
	Paid  int64 `json:"paid"`
}

type paymentCountsVO struct {
	Paid    int `json:"paid"`
	Expired int `json:"expired"`
	Pending int `json:"pending"`
}

type couponStatsVO struct {
	Code     string `json:"code"`
	Count    int    `json:"count"`
	Discount int64  `json:"discount"`
	Paid     int64  `json:"paid"`
}

// analyticsFinanceVO 对应 hub-api.yaml AnalyticsFinance。
type analyticsFinanceVO struct {
	Days         []financeDayVO  `json:"days"`
	OrderKinds   []orderKindVO   `json:"orderKinds"`
	TrialCount   int             `json:"trialCount"`
	Plans        []planSalesVO   `json:"plans"`
	Funnel       funnelVO        `json:"funnel"`
	Payers       payersVO        `json:"payers"`
	BalanceTotal int64           `json:"balanceTotal"`
	Payments     paymentCountsVO `json:"payments"`
	Coupons      []couponStatsVO `json:"coupons"`
}

func (h *handlers) getAnalyticsFinance(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	days, err := analyticsDays(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a, err := h.analytics.Finance(r.Context(), days)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, analyticsFinanceVO{
		Days: mapSlice(a.Days, func(d service.FinanceDay) financeDayVO {
			return financeDayVO{Date: d.Date, OrderPaid: d.OrderPaid, Topup: d.Topup}
		}),
		OrderKinds: mapSlice(a.OrderKinds, func(k service.OrderKindStats) orderKindVO {
			return orderKindVO{Kind: k.Kind, Count: k.Count, Paid: k.Paid}
		}),
		TrialCount: a.TrialCount,
		Plans: mapSlice(a.Plans, func(p service.PlanSales) planSalesVO {
			return planSalesVO{PlanID: p.PlanID, Name: p.Name, Count: p.Count, Paid: p.Paid}
		}),
		Funnel:       funnelVO{Registered: a.FunnelRegistered, Trial: a.FunnelTrial, Paid: a.FunnelPaid},
		Payers:       payersVO{Users: a.Payers, Paid: a.PayersPaid},
		BalanceTotal: a.BalanceTotal,
		Payments:     paymentCountsVO{Paid: a.PaymentsPaid, Expired: a.PaymentsExpired, Pending: a.PaymentsPending},
		Coupons: mapSlice(a.Coupons, func(c service.CouponStats) couponStatsVO {
			return couponStatsVO{Code: c.Code, Count: c.Count, Discount: c.Discount, Paid: c.Paid}
		}),
	})
}

type backlogTicketsVO struct {
	Open                 int        `json:"open"`
	OldestWaitingSince   *time.Time `json:"oldestWaitingSince"`
	AvgFirstReplyMinutes *int       `json:"avgFirstReplyMinutes"`
}

type serverFailedVO struct {
	ServerID int64  `json:"serverId"`
	Name     string `json:"name"`
	Failed   int    `json:"failed"`
}

type backlogProvisionsVO struct {
	Failed  int              `json:"failed"`
	Servers []serverFailedVO `json:"servers"`
}

type chainFailureVO struct {
	ServerID      int64      `json:"serverId"`
	Name          string     `json:"name"`
	FailCount     int        `json:"failCount"`
	LastAttemptAt *time.Time `json:"lastAttemptAt"`
}

type backlogPanelsVO struct {
	ChainFailed     []chainFailureVO `json:"chainFailed"`
	DriftInbounds   int              `json:"driftInbounds"`
	MissingInbounds int              `json:"missingInbounds"`
}

type backlogOutboxVO struct {
	MailPending       int `json:"mailPending"`
	MailFailed24h     int `json:"mailFailed24h"`
	TelegramPending   int `json:"telegramPending"`
	TelegramFailed24h int `json:"telegramFailed24h"`
}

// analyticsBacklogVO 对应 hub-api.yaml AnalyticsBacklog；缺权限的块为 null。
type analyticsBacklogVO struct {
	Tickets    *backlogTicketsVO    `json:"tickets"`
	Provisions *backlogProvisionsVO `json:"provisions"`
	Panels     *backlogPanelsVO     `json:"panels"`
	Outbox     *backlogOutboxVO     `json:"outbox"`
}

func (h *handlers) getAnalyticsBacklog(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	b, err := h.analytics.Backlog(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var vo analyticsBacklogVO
	if t := b.Tickets; t != nil {
		vo.Tickets = &backlogTicketsVO{Open: t.Open, OldestWaitingSince: t.OldestWaitingSince, AvgFirstReplyMinutes: t.AvgFirstReplyMinutes}
	}
	if v := b.Provisions; v != nil {
		vo.Provisions = &backlogProvisionsVO{Failed: v.Failed, Servers: mapSlice(v.Servers, func(c service.ServerCount) serverFailedVO {
			return serverFailedVO{ServerID: c.ServerID, Name: c.Name, Failed: c.Count}
		})}
	}
	if pn := b.Panels; pn != nil {
		vo.Panels = &backlogPanelsVO{DriftInbounds: pn.DriftInbounds, MissingInbounds: pn.MissingInbounds,
			ChainFailed: mapSlice(pn.ChainFailed, func(c service.ChainFailure) chainFailureVO {
				return chainFailureVO{ServerID: c.ServerID, Name: c.Name, FailCount: c.FailCount, LastAttemptAt: c.LastAttemptAt}
			})}
	}
	if o := b.Outbox; o != nil {
		vo.Outbox = &backlogOutboxVO{MailPending: o.MailPending, MailFailed24h: o.MailFailed24h,
			TelegramPending: o.TelegramPending, TelegramFailed24h: o.TelegramFailed24h}
	}
	writeOK(w, vo)
}

// nodeReachVO 对应 hub-api.yaml NodeReach。
type nodeReachVO struct {
	Verdict   string     `json:"verdict"`
	Scope     string     `json:"scope"`
	CnOK      int        `json:"cnOk"`
	CnTotal   int        `json:"cnTotal"`
	CheckedAt *time.Time `json:"checkedAt"`
}

// serverReachVO 是服务器上检测目标的数量（hub-api.yaml Server.reach）。
type serverReachVO struct {
	Total   int `json:"total"`
	OK      int `json:"ok"`
	Blocked int `json:"blocked"`
	Partial int `json:"partial"`
}

type reachProbeVO struct {
	IsMainland bool     `json:"isMainland"`
	Country    string   `json:"country"`
	City       string   `json:"city"`
	Network    string   `json:"network"`
	IsOK       bool     `json:"isOk"`
	RTTMs      *float64 `json:"rttMs"`
}

// nodeReachDetailVO 对应 hub-api.yaml NodeReachDetail。
type nodeReachDetailVO struct {
	Address      string         `json:"address"`
	Port         int            `json:"port"`
	Transport    string         `json:"transport"`
	Verdict      string         `json:"verdict"`
	Scope        string         `json:"scope"`
	CnOK         int            `json:"cnOk"`
	CnTotal      int            `json:"cnTotal"`
	ControlOK    int            `json:"controlOk"`
	ControlTotal int            `json:"controlTotal"`
	IPReachable  *bool          `json:"ipReachable"`
	CheckedAt    *time.Time     `json:"checkedAt"`
	BlockedSince *time.Time     `json:"blockedSince"`
	LastError    string         `json:"lastError"`
	Probes       []reachProbeVO `json:"probes"`
}

func toNodeReachDetailVO(d *service.ReachDetail) nodeReachDetailVO {
	return nodeReachDetailVO{
		Address: d.Address, Port: d.Port, Transport: d.Transport, Verdict: d.Verdict, Scope: d.Scope,
		CnOK: d.CnOK, CnTotal: d.CnTotal, ControlOK: d.ControlOK, ControlTotal: d.ControlTotal, IPReachable: d.IPReachable,
		CheckedAt: d.CheckedAt, BlockedSince: d.BlockedSince, LastError: d.LastError,
		Probes: mapSlice(d.Probes, func(p service.ReachProbe) reachProbeVO {
			return reachProbeVO{IsMainland: p.IsMainland, Country: p.Country, City: p.City, Network: p.Network, IsOK: p.IsOK, RTTMs: p.RTTMs}
		}),
	}
}

func (h *handlers) getNodeReach(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	d, err := h.reach.NodeDetail(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toNodeReachDetailVO(d))
}

func (h *handlers) checkNodeReach(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	d, err := h.reach.CheckNode(r.Context(), p, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toNodeReachDetailVO(d))
}

// serverReach 返回各服务器的检测目标数；功能没开（或没接连通检测）返回 nil。
func (h *handlers) serverReach(ctx context.Context) (map[int64]service.ReachCount, error) {
	if h.reach == nil {
		return nil, nil
	}
	return h.reach.ServerCounts(ctx)
}

// nodeReach 返回各节点的连通结论；功能没开（或没接连通检测）返回 nil。
func (h *handlers) nodeReach(ctx context.Context) (map[int64]service.ReachBrief, error) {
	if h.reach == nil {
		return nil, nil
	}
	return h.reach.NodeBriefs(ctx)
}
