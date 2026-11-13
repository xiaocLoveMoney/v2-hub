package api

import (
	"context"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"v2-hub-server/internal/service"
)

// 客户后台数据接口与订阅端点（docs/03 §5.4、§5.5），与 hub-api.yaml 的 user-portal、subscription 逐条对应。

const (
	queryHours            = "hours"
	querySubFormat        = "format"
	pathParamToken        = "token"
	headerForwardedProto  = "X-Forwarded-Proto"
	schemeHTTP            = "http"
	schemeHTTPS           = "https"
	contentTypePlain      = "text/plain; charset=utf-8"
	msgBadHours           = "hours 不合法"
	subTokenMinLen        = 22
	subTokenMaxLen        = 64
	cacheNoStore          = "no-store"
	plainInternalErrorMsg = "internal error"
)

// PortalManager 是 Web 层需要的客户后台与订阅能力（由 service.UserService 实现）。
type PortalManager interface {
	GetDashboard(ctx context.Context, p *service.UserPrincipal) (*service.Dashboard, error)
	ListTraffic(ctx context.Context, p *service.UserPrincipal, hours int) ([]service.TrafficPoint, error)
	ListPortalNodes(ctx context.Context, p *service.UserPrincipal) ([]service.PortalNodeGroup, error)
	GetNodeLink(ctx context.Context, p *service.UserPrincipal, nodeID int64) (string, error)
	GetSubscription(ctx context.Context, p *service.UserPrincipal, requestBase string) (*service.SubscriptionInfo, error)
	ResetOwnSubscription(ctx context.Context, p *service.UserPrincipal, requestBase string, ip *netip.Addr) (*service.SubscriptionInfo, error)
	ListSubscribeLogs(ctx context.Context, userID int64, pq service.PageQuery) (*service.Page[service.SubscribeLogItem], error)
	RenderSubscription(ctx context.Context, req service.SubscriptionRequest) (*service.SubscriptionResult, error)
}

// requestBase 返回请求自身的根地址（scheme://host）；只有来自可信代理的请求才采信 X-Forwarded-Proto。
func (h *handlers) requestBase(r *http.Request) string {
	scheme := schemeHTTP
	if r.TLS != nil {
		scheme = schemeHTTPS
	}
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil && inPrefixes(ap.Addr().Unmap(), h.trustedProxies) {
		if proto := r.Header.Get(headerForwardedProto); proto == schemeHTTP || proto == schemeHTTPS {
			scheme = proto
		}
	}
	return scheme + "://" + r.Host
}

type dashboardVO struct {
	Status        string     `json:"status"`
	PlanName      string     `json:"planName"`
	TrafficLimit  int64      `json:"trafficLimit"`
	TrafficUsed   int64      `json:"trafficUsed"`
	TrafficToday  int64      `json:"trafficToday"`
	ExpiresAt     *time.Time `json:"expiresAt"`
	PendingDays   int        `json:"pendingDays"`
	LimitIP       int        `json:"limitIp"`
	OnlineDevices *int       `json:"onlineDevices"`
	LastUsedAt    *time.Time `json:"lastUsedAt"`
	NodeCount     int        `json:"nodeCount"`
}

func (h *handlers) getUserDashboard(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	d, err := h.portal.GetDashboard(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	online := d.OnlineDevices
	writeOK(w, dashboardVO{
		Status: d.Status, PlanName: d.PlanName, TrafficLimit: d.TrafficLimit, TrafficUsed: d.TrafficUsed, TrafficToday: d.TrafficToday,
		ExpiresAt: d.ExpiresAt, PendingDays: d.PendingDays, LimitIP: d.LimitIP, OnlineDevices: &online, LastUsedAt: d.LastUsedAt, NodeCount: d.NodeCount,
	})
}

type trafficPointVO struct {
	Hour time.Time `json:"hour"`
	Up   int64     `json:"up"`
	Down int64     `json:"down"`
}

func (h *handlers) listUserTraffic(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	hours := 0
	if raw := r.URL.Query().Get(queryHours); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 {
			writeError(w, r, badRequest(msgBadHours))
			return
		}
		hours = v
	}
	points, err := h.portal.ListTraffic(r.Context(), p, hours)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]trafficPointVO, 0, len(points))
	for _, pt := range points {
		out = append(out, trafficPointVO{Hour: pt.Hour, Up: pt.Up, Down: pt.Down})
	}
	writeOK(w, out)
}

type userNodeVO struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name"`
	Region         string   `json:"region"`
	Protocol       string   `json:"protocol"`
	IsServerOnline bool     `json:"isServerOnline"`
	ServerLoad     *float64 `json:"serverLoad"`
	OnlineClients  int      `json:"onlineClients"`
}

type userNodeGroupVO struct {
	ID    int64        `json:"id"`
	Name  string       `json:"name"`
	Nodes []userNodeVO `json:"nodes"`
}

func (h *handlers) listUserNodes(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	groups, err := h.portal.ListPortalNodes(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]userNodeGroupVO, 0, len(groups))
	for _, g := range groups {
		nodes := make([]userNodeVO, 0, len(g.Nodes))
		for _, n := range g.Nodes {
			nodes = append(nodes, userNodeVO{ID: n.ID, Name: n.Name, Region: n.Region, Protocol: n.Protocol, IsServerOnline: n.IsServerOnline, ServerLoad: n.ServerLoad, OnlineClients: n.OnlineClients})
		}
		out = append(out, userNodeGroupVO{ID: g.ID, Name: g.Name, Nodes: nodes})
	}
	writeOK(w, out)
}

type nodeLinkVO struct {
	Link string `json:"link"`
}

func (h *handlers) getUserNodeLink(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	link, err := h.portal.GetNodeLink(r.Context(), p, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nodeLinkVO{Link: link})
}

type subscriptionFormatVO struct {
	Format string `json:"format"`
	URL    string `json:"url"`
}

type subscriptionInfoVO struct {
	URL                 string                 `json:"url"`
	Formats             []subscriptionFormatVO `json:"formats"`
	UpdateIntervalHours int                    `json:"updateIntervalHours"`
}

func toSubscriptionInfoVO(info *service.SubscriptionInfo) subscriptionInfoVO {
	formats := make([]subscriptionFormatVO, 0, len(info.Formats))
	for _, f := range info.Formats {
		formats = append(formats, subscriptionFormatVO{Format: f.Format, URL: f.URL})
	}
	return subscriptionInfoVO{URL: info.URL, Formats: formats, UpdateIntervalHours: info.UpdateIntervalHours}
}

func (h *handlers) getUserSubscription(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	info, err := h.portal.GetSubscription(r.Context(), p, h.requestBase(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toSubscriptionInfoVO(info))
}

func (h *handlers) resetOwnSubscription(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	info, err := h.portal.ResetOwnSubscription(r.Context(), p, h.requestBase(r), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toSubscriptionInfoVO(info))
}

func (h *handlers) listOwnSubscribeLogs(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.portal.ListSubscribeLogs(r.Context(), p.UserID, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toSubscribeLogPageVO(page))
}

// writePlain 输出纯文本（订阅端点不走响应信封）。
func writePlain(w http.ResponseWriter, status int, body string) {
	w.Header().Set(headerContent, contentTypePlain)
	w.Header().Set(headerCache, cacheNoStore)
	w.WriteHeader(status)
	writeBody(w, []byte(body))
}

// getSubscription 处理 GET /sub/{token}：凭 token 访问，无需登录；错误以纯文本返回，不透出内部细节。
func (h *handlers) getSubscription(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue(pathParamToken)
	if len(token) < subTokenMinLen || len(token) > subTokenMaxLen {
		writePlain(w, http.StatusNotFound, string(service.CodeNotFound))
		return
	}
	res, err := h.portal.RenderSubscription(r.Context(), service.SubscriptionRequest{
		Token: token, Format: r.URL.Query().Get(querySubFormat), UserAgent: r.Header.Get(headerUserAgent), ClientIP: clientIP(r, h.trustedProxies),
	})
	if err != nil {
		status, body := plainError(r, err)
		writePlain(w, status, body)
		return
	}
	for k, v := range res.Headers {
		w.Header().Set(k, v)
	}
	w.Header().Set(headerContent, res.ContentType)
	w.Header().Set(headerCache, cacheNoStore)
	w.WriteHeader(http.StatusOK)
	writeBody(w, res.Body)
}
