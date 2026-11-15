package api

import (
	"context"
	"net/http"
	"time"

	"v2-hub-server/internal/service"
)

// 运营接口（docs/03 §5.20）：管理端总览、落地页的公开套餐、收支按月与 CSV 导出。

const (
	queryGranularity  = "granularity"
	queryMonths       = "months"
	msgBadMonths      = "months 须为正整数"
	headerDisposition = "Content-Disposition"
	contentTypeCSV    = "text/csv; charset=utf-8"
)

// OpsManager 是管理端总览需要的能力（由 service.OpsService 实现）。
type OpsManager interface {
	Dashboard(ctx context.Context, p *service.AdminPrincipal) (*service.AdminDashboard, error)
}

type dashboardUsersVO struct {
	Total    int `json:"total"`
	Active   int `json:"active"`
	NewToday int `json:"newToday"`
	New7d    int `json:"new7d"`
	Online   int `json:"online"`
}

type dashboardDayVO struct {
	Date  string `json:"date"`
	Bytes int64  `json:"bytes"`
}

type dashboardTrafficVO struct {
	Today int64            `json:"today"`
	Days  []dashboardDayVO `json:"days"`
}

type offlineServerVO struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	Status       string     `json:"status"`
	OfflineSince *time.Time `json:"offlineSince"`
}

type dashboardServersVO struct {
	Total     int               `json:"total"`
	Online    int               `json:"online"`
	NodeCount int               `json:"nodeCount"`
	Offline   []offlineServerVO `json:"offline"`
}

type dashboardFinanceVO struct {
	TodaySpend int64 `json:"todaySpend"`
	TodayTopup int64 `json:"todayTopup"`
	MonthSpend int64 `json:"monthSpend"`
	MonthTopup int64 `json:"monthTopup"`
}

type dashboardTicketsVO struct {
	Open int `json:"open"`
}

// adminDashboardVO 对应 hub-api.yaml AdminDashboard；缺权限的区块为 null。
type adminDashboardVO struct {
	Users   *dashboardUsersVO   `json:"users"`
	Traffic *dashboardTrafficVO `json:"traffic"`
	Servers *dashboardServersVO `json:"servers"`
	Finance *dashboardFinanceVO `json:"finance"`
	Tickets *dashboardTicketsVO `json:"tickets"`
}

func toAdminDashboardVO(d *service.AdminDashboard) adminDashboardVO {
	var out adminDashboardVO
	if u := d.Users; u != nil {
		out.Users = &dashboardUsersVO{Total: u.Total, Active: u.Active, NewToday: u.NewToday, New7d: u.New7d, Online: u.Online}
	}
	if t := d.Traffic; t != nil {
		out.Traffic = &dashboardTrafficVO{Today: t.Today, Days: mapSlice(t.Days, func(x service.DashboardDay) dashboardDayVO {
			return dashboardDayVO{Date: x.Date, Bytes: x.Bytes}
		})}
	}
	if sv := d.Servers; sv != nil {
		out.Servers = &dashboardServersVO{Total: sv.Total, Online: sv.Online, NodeCount: sv.NodeCount, Offline: mapSlice(sv.Offline, func(x service.OfflineServer) offlineServerVO {
			return offlineServerVO{ID: x.ID, Name: x.Name, Status: x.Status, OfflineSince: x.OfflineSince}
		})}
	}
	if f := d.Finance; f != nil {
		out.Finance = &dashboardFinanceVO{TodaySpend: f.TodaySpend, TodayTopup: f.TodayTopup, MonthSpend: f.MonthSpend, MonthTopup: f.MonthTopup}
	}
	if d.OpenTickets != nil {
		out.Tickets = &dashboardTicketsVO{Open: *d.OpenTickets}
	}
	return out
}

func (h *handlers) getAdminDashboard(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	d, err := h.ops.Dashboard(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toAdminDashboardVO(d))
}

// publicPlanVO 对应 hub-api.yaml PublicPlan。
type publicPlanVO struct {
	ID           int64         `json:"id"`
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	TrafficLimit int64         `json:"trafficLimit"`
	TrafficReset string        `json:"trafficReset"`
	LimitIP      int           `json:"limitIp"`
	NodeCount    int           `json:"nodeCount"`
	Prices       []planPriceVO `json:"prices"`
}

func (h *handlers) getPublicPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := h.shop.PublicPlans(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, mapSlice(plans, func(sp service.ShopPlan) publicPlanVO {
		return publicPlanVO{
			ID: sp.ID, Name: sp.Name, Description: sp.Description, TrafficLimit: sp.TrafficLimit, TrafficReset: sp.TrafficReset,
			LimitIP: sp.LimitIP, NodeCount: sp.NodeCount, Prices: mapSlice(sp.Prices, func(p service.PlanPrice) planPriceVO {
				return planPriceVO{ID: p.ID, planPriceBody: planPriceBody{DurationDays: p.DurationDays, Price: p.Price}}
			}),
		}
	}))
}

// financeQuery 解析收支统计的查询参数（granularity、days、months）；范围由 service 校验。
func financeQuery(r *http.Request) (service.FinanceQuery, error) {
	fq := service.FinanceQuery{Granularity: r.URL.Query().Get(queryGranularity)}
	days, err := optionalPositiveInt(r, queryDays, msgBadDays)
	if err != nil {
		return fq, err
	}
	fq.Days = days
	months, err := optionalPositiveInt(r, queryMonths, msgBadMonths)
	if err != nil {
		return fq, err
	}
	fq.Months = months
	return fq, nil
}

func (h *handlers) exportFinanceStats(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	fq, err := financeQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	body, name, err := h.finance.ExportFinanceCSV(r.Context(), p, fq, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set(headerContent, contentTypeCSV)
	w.Header().Set(headerDisposition, "attachment; filename="+name)
	w.WriteHeader(http.StatusOK)
	writeBody(w, body)
}
