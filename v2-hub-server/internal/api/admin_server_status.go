package api

import (
	"net/http"
	"time"

	"v2-hub-server/internal/service"
)

// 服务器状态卡片的接口（docs/03 §5.1、§5.20；Ruling-83 – Ruling-86）：账单与流量额度、资源趋势、三网延迟。

const msgBadExpiresOn = "到期日须为 YYYY-MM-DD"

type serverBillingVO struct {
	PriceCents        *int32  `json:"priceCents"`
	PriceCurrency     string  `json:"priceCurrency"`
	BillingCycle      string  `json:"billingCycle"`
	ExpiresOn         *string `json:"expiresOn"`
	TrafficQuotaBytes *int64  `json:"trafficQuotaBytes"`
	TrafficResetDay   int     `json:"trafficResetDay"`
	TrafficMode       string  `json:"trafficMode"`
}

type serverCycleTrafficVO struct {
	CycleStart  string `json:"cycleStart"`
	NextResetOn string `json:"nextResetOn"`
	SentBytes   int64  `json:"sentBytes"`
	RecvBytes   int64  `json:"recvBytes"`
	UsedBytes   int64  `json:"usedBytes"`
}

type serverHostBriefVO struct {
	OSID         string     `json:"osId"`
	Arch         string     `json:"arch"`
	CertNotAfter *time.Time `json:"certNotAfter"`
}

func toServerBillingVO(b service.ServerBilling) serverBillingVO {
	vo := serverBillingVO{
		PriceCents: b.PriceCents, PriceCurrency: b.PriceCurrency, BillingCycle: b.BillingCycle,
		TrafficQuotaBytes: b.TrafficQuotaBytes, TrafficResetDay: b.TrafficResetDay, TrafficMode: b.TrafficMode,
	}
	if b.ExpiresOn != nil {
		v := b.ExpiresOn.Format(time.DateOnly)
		vo.ExpiresOn = &v
	}
	return vo
}

func toServerCycleTrafficVO(t service.ServerCycleTraffic) serverCycleTrafficVO {
	return serverCycleTrafficVO{
		CycleStart: t.CycleStart.Format(time.DateOnly), NextResetOn: t.NextResetOn.Format(time.DateOnly),
		SentBytes: t.SentBytes, RecvBytes: t.RecvBytes, UsedBytes: t.UsedBytes,
	}
}

func toServerHostBriefVO(h *service.ServerHostBrief) *serverHostBriefVO {
	if h == nil {
		return nil
	}
	return &serverHostBriefVO{OSID: h.OSID, Arch: h.Arch, CertNotAfter: h.CertNotAfter}
}

type serverBillingRequest struct {
	PriceCents        *int32  `json:"priceCents"`
	PriceCurrency     string  `json:"priceCurrency"`
	BillingCycle      string  `json:"billingCycle"`
	ExpiresOn         *string `json:"expiresOn"`
	TrafficQuotaBytes *int64  `json:"trafficQuotaBytes"`
	TrafficResetDay   int     `json:"trafficResetDay"`
	TrafficMode       string  `json:"trafficMode"`
	TrafficUsedBytes  *int64  `json:"trafficUsedBytes"`
}

// toInput 把请求转成业务输入；到期日不是 YYYY-MM-DD 返回 400。
func (req serverBillingRequest) toInput() (service.ServerBillingInput, error) {
	in := service.ServerBillingInput{
		ServerBilling: service.ServerBilling{
			PriceCents: req.PriceCents, PriceCurrency: req.PriceCurrency, BillingCycle: req.BillingCycle,
			TrafficQuotaBytes: req.TrafficQuotaBytes, TrafficResetDay: req.TrafficResetDay, TrafficMode: req.TrafficMode,
		},
		TrafficUsedBytes: req.TrafficUsedBytes,
	}
	if req.ExpiresOn != nil {
		day, err := time.Parse(time.DateOnly, *req.ExpiresOn)
		if err != nil {
			return in, badRequest(msgBadExpiresOn)
		}
		in.ExpiresOn = &day
	}
	return in, nil
}

func (h *handlers) setServerBilling(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req serverBillingRequest
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	in, err := req.toInput()
	if err != nil {
		writeError(w, r, err)
		return
	}
	srv, err := h.panels.SetServerBilling(r.Context(), p, id, in, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	h.writeServer(w, r, srv)
}

type latencyPointVO struct {
	At          time.Time `json:"at"`
	AvgMs       *int      `json:"avgMs"`
	LossPercent int       `json:"lossPercent"`
}

type latencySeriesVO struct {
	Telecom []latencyPointVO `json:"telecom"`
	Unicom  []latencyPointVO `json:"unicom"`
	Mobile  []latencyPointVO `json:"mobile"`
}

func toLatencyPointVO(p service.LatencyPoint) latencyPointVO {
	return latencyPointVO{At: p.At, AvgMs: p.AvgMs, LossPercent: p.LossPercent}
}

func toLatencySeriesVO(s *service.LatencySeries) *latencySeriesVO {
	if s == nil {
		return nil
	}
	return &latencySeriesVO{
		Telecom: mapSlice(s.Telecom, toLatencyPointVO), Unicom: mapSlice(s.Unicom, toLatencyPointVO), Mobile: mapSlice(s.Mobile, toLatencyPointVO),
	}
}

type serverMetricPointVO struct {
	At            time.Time `json:"at"`
	CPU           *float64  `json:"cpu"`
	OnlineClients int       `json:"onlineClients"`
	MemUsed       *int64    `json:"memUsed"`
	MemTotal      *int64    `json:"memTotal"`
	DiskUsed      *int64    `json:"diskUsed"`
	DiskTotal     *int64    `json:"diskTotal"`
	Load1         *float64  `json:"load1"`
	NetUpRate     *int64    `json:"netUpRate"`
	NetDownRate   *int64    `json:"netDownRate"`
	TCPCount      *int      `json:"tcpCount"`
	UDPCount      *int      `json:"udpCount"`
}

type serverMetricsVO struct {
	Range   string                `json:"range"`
	Points  []serverMetricPointVO `json:"points"`
	Latency *latencySeriesVO      `json:"latency"`
}

type serverNetPointVO struct {
	At   time.Time `json:"at"`
	Up   int64     `json:"up"`
	Down int64     `json:"down"`
}

type serverTrendVO struct {
	ServerID int64              `json:"serverId"`
	Net      []serverNetPointVO `json:"net"`
	Latency  *latencySeriesVO   `json:"latency"`
}

// isLatencyEnabled 报告三网延迟检测是否开着（Ruling-86）；关着时接口不带延迟数据。
func (h *handlers) isLatencyEnabled(r *http.Request) (bool, error) {
	cur, err := h.settings.Current(r.Context())
	if err != nil {
		return false, err
	}
	return cur.Latency.Enabled, nil
}

func (h *handlers) getServerMetrics(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rng := r.URL.Query().Get(queryMetricRange)
	if rng == "" {
		rng = service.MetricRange24h
	}
	withLatency, err := h.isLatencyEnabled(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	m, err := h.panels.GetServerMetrics(r.Context(), id, rng, withLatency)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, serverMetricsVO{
		Range: m.Range, Latency: toLatencySeriesVO(m.Latency),
		Points: mapSlice(m.Points, func(p service.ServerMetricPoint) serverMetricPointVO {
			return serverMetricPointVO{
				At: p.At, CPU: p.CPU, OnlineClients: p.OnlineClients, MemUsed: p.MemUsed, MemTotal: p.MemTotal,
				DiskUsed: p.DiskUsed, DiskTotal: p.DiskTotal, Load1: p.Load1, NetUpRate: p.NetUpRate, NetDownRate: p.NetDownRate,
				TCPCount: p.TCPCount, UDPCount: p.UDPCount,
			}
		}),
	})
}

func (h *handlers) listServerTrends(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	withLatency, err := h.isLatencyEnabled(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	trends, err := h.panels.ListServerTrends(r.Context(), withLatency)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, mapSlice(trends, func(t service.ServerTrend) serverTrendVO {
		return serverTrendVO{
			ServerID: t.ServerID, Latency: toLatencySeriesVO(t.Latency),
			Net: mapSlice(t.Net, func(p service.ServerNetPoint) serverNetPointVO {
				return serverNetPointVO{At: p.At, Up: p.Up, Down: p.Down}
			}),
		}
	}))
}
