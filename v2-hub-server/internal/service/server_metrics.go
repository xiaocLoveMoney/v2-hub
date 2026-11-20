package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"v2-hub-server/internal/dao/sqlcgen"
)

// 服务器资源趋势与三网延迟的读取（docs/03 §5.1 负载采样、§5.20 三网延迟检测；Ruling-83、Ruling-84）。
const (
	// 运营商标识。
	LatencyISPTelecom = "telecom"
	LatencyISPUnicom  = "unicom"
	LatencyISPMobile  = "mobile"

	// trendPoints 是列表卡片每条小图的点数；trendNetWindow 是这些点覆盖的时长（5 分钟一点）。
	trendPoints    = 30
	trendNetWindow = trendPoints * 5 * time.Minute
)

// ServerMetricPoint 是一个时段的资源采样。除 OnlineClients 外的字段为 nil 表示这个时段没有该项数据
// （面板没报，或是 Ruling-83 之前的采样）。Load1 已从“乘 100 的整数”换回小数。
type ServerMetricPoint struct {
	At            time.Time
	CPU           *float64
	OnlineClients int
	MemUsed       *int64
	MemTotal      *int64
	DiskUsed      *int64
	DiskTotal     *int64
	Load1         *float64
	NetUpRate     *int64
	NetDownRate   *int64
	TCPCount      *int
	UDPCount      *int
}

// LatencyPoint 是一次延迟检测里一家运营商的结果；AvgMs 为 nil 表示全丢。
type LatencyPoint struct {
	At          time.Time
	AvgMs       *int
	LossPercent int
}

// LatencySeries 是三家运营商各自的延迟序列，按时间升序。
type LatencySeries struct {
	Telecom []LatencyPoint
	Unicom  []LatencyPoint
	Mobile  []LatencyPoint
}

// ServerMetrics 是一台服务器的趋势；Latency 为 nil 表示没要延迟数据（功能没开）。
type ServerMetrics struct {
	Range   string
	Points  []ServerMetricPoint
	Latency *LatencySeries
}

// ServerNetPoint 是一个 5 分钟时段的上下行速率（字节每秒）。
type ServerNetPoint struct {
	At   time.Time
	Up   int64
	Down int64
}

// ServerTrend 是一台服务器最近的速率与延迟，给列表卡片用；Latency 为 nil 表示没要延迟数据。
type ServerTrend struct {
	ServerID int64
	Net      []ServerNetPoint
	Latency  *LatencySeries
}

func newLatencySeries(capacity int) *LatencySeries {
	return &LatencySeries{
		Telecom: make([]LatencyPoint, 0, capacity), Unicom: make([]LatencyPoint, 0, capacity), Mobile: make([]LatencyPoint, 0, capacity),
	}
}

// add 把一个点归到它的运营商；不认识的运营商忽略。
func (l *LatencySeries) add(isp string, pt LatencyPoint) {
	switch isp {
	case LatencyISPTelecom:
		l.Telecom = append(l.Telecom, pt)
	case LatencyISPUnicom:
		l.Unicom = append(l.Unicom, pt)
	case LatencyISPMobile:
		l.Mobile = append(l.Mobile, pt)
	default:
	}
}

func int4Ptr(v pgtype.Int4) *int {
	if !v.Valid {
		return nil
	}
	out := int(v.Int32)
	return &out
}

func loadPtr(v pgtype.Int4) *float64 {
	if !v.Valid {
		return nil
	}
	out := float64(v.Int32) / loadScale
	return &out
}

// GetServerMetrics 返回一台服务器的资源趋势：24h 为 5 分钟原值，7d 按小时平均；withLatency 为真时带三网延迟
// （24h 为每次检测的原值，7d 按小时平均）。范围不合法返回 400，服务器不存在返回 404。
func (s *PanelService) GetServerMetrics(ctx context.Context, id int64, rng string, withLatency bool) (*ServerMetrics, error) {
	if rng != MetricRange24h && rng != MetricRange7d {
		return nil, newError(CodeInvalidArgument, msgBadMetricRange)
	}
	if _, err := s.q.GetServerConn(ctx, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, newError(CodeNotFound, msgServerNotFound)
		}
		return nil, fmt.Errorf("get server %d: %w", id, err)
	}
	out := &ServerMetrics{Range: rng}
	isHourly := rng == MetricRange7d
	window := metricWindow24h
	if isHourly {
		window = metricWindow7d
	}
	since := pgtype.Timestamptz{Time: s.now().Add(-window), Valid: true}
	var err error
	if isHourly {
		out.Points, err = s.listHourlyResourceMetrics(ctx, id, since)
	} else {
		out.Points, err = s.listResourceMetrics(ctx, id, since)
	}
	if err != nil {
		return nil, err
	}
	if !withLatency {
		return out, nil
	}
	if isHourly {
		out.Latency, err = s.listHourlyLatency(ctx, id, since)
	} else {
		out.Latency, err = s.listLatency(ctx, id, since)
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PanelService) listResourceMetrics(ctx context.Context, id int64, since pgtype.Timestamptz) ([]ServerMetricPoint, error) {
	rows, err := s.q.ListServerResourceMetrics(ctx, sqlcgen.ListServerResourceMetricsParams{ServerID: id, BucketAt: since})
	if err != nil {
		return nil, fmt.Errorf("list resource metrics of server %d: %w", id, err)
	}
	out := make([]ServerMetricPoint, 0, metricFiveMinuteCap)
	for _, r := range rows {
		pt := ServerMetricPoint{
			At: r.BucketAt.Time, OnlineClients: int(r.OnlineClients),
			MemUsed: int8Ptr(r.MemUsed), MemTotal: int8Ptr(r.MemTotal), DiskUsed: int8Ptr(r.DiskUsed), DiskTotal: int8Ptr(r.DiskTotal),
			Load1: loadPtr(r.Load1X100), NetUpRate: int8Ptr(r.NetUpRate), NetDownRate: int8Ptr(r.NetDownRate),
			TCPCount: int4Ptr(r.TcpCount), UDPCount: int4Ptr(r.UdpCount),
		}
		if r.Cpu.Valid {
			v := r.Cpu.Float64
			pt.CPU = &v
		}
		out = append(out, pt)
	}
	return out, nil
}

func (s *PanelService) listHourlyResourceMetrics(ctx context.Context, id int64, since pgtype.Timestamptz) ([]ServerMetricPoint, error) {
	rows, err := s.q.ListServerResourceMetricsHourly(ctx, sqlcgen.ListServerResourceMetricsHourlyParams{ServerID: id, BucketAt: since})
	if err != nil {
		return nil, fmt.Errorf("list hourly resource metrics of server %d: %w", id, err)
	}
	out := make([]ServerMetricPoint, 0, metricHourlyCap)
	for _, r := range rows {
		pt := ServerMetricPoint{At: r.BucketAt.Time, OnlineClients: int(r.OnlineClients)}
		if r.CpuSamples > 0 {
			v := r.Cpu
			pt.CPU = &v
		}
		if r.ResourceSamples > 0 {
			memUsed, memTotal, diskUsed, diskTotal := r.MemUsed, r.MemTotal, r.DiskUsed, r.DiskTotal
			up, down := r.NetUpRate, r.NetDownRate
			load := float64(r.Load1X100) / loadScale
			tcp, udp := int(r.TcpCount), int(r.UdpCount)
			pt.MemUsed, pt.MemTotal, pt.DiskUsed, pt.DiskTotal = &memUsed, &memTotal, &diskUsed, &diskTotal
			pt.Load1, pt.NetUpRate, pt.NetDownRate, pt.TCPCount, pt.UDPCount = &load, &up, &down, &tcp, &udp
		}
		out = append(out, pt)
	}
	return out, nil
}

func (s *PanelService) listLatency(ctx context.Context, id int64, since pgtype.Timestamptz) (*LatencySeries, error) {
	rows, err := s.q.ListServerLatency(ctx, sqlcgen.ListServerLatencyParams{ServerID: id, SampledAt: since})
	if err != nil {
		return nil, fmt.Errorf("list latency of server %d: %w", id, err)
	}
	out := newLatencySeries(len(rows))
	for _, r := range rows {
		out.add(r.Isp, LatencyPoint{At: r.SampledAt.Time, AvgMs: int4Ptr(r.AvgMs), LossPercent: int(r.LossPercent)})
	}
	return out, nil
}

func (s *PanelService) listHourlyLatency(ctx context.Context, id int64, since pgtype.Timestamptz) (*LatencySeries, error) {
	rows, err := s.q.ListServerLatencyHourly(ctx, sqlcgen.ListServerLatencyHourlyParams{ServerID: id, SampledAt: since})
	if err != nil {
		return nil, fmt.Errorf("list hourly latency of server %d: %w", id, err)
	}
	out := newLatencySeries(metricHourlyCap)
	for _, r := range rows {
		pt := LatencyPoint{At: r.SampledAt.Time, LossPercent: int(r.LossPercent)}
		if r.OkSamples > 0 {
			v := int(r.AvgMs)
			pt.AvgMs = &v
		}
		out.add(r.Isp, pt)
	}
	return out, nil
}

// ListServerTrends 返回各服务器最近 30 个 5 分钟时段的上下行速率；withLatency 为真时带三家运营商各自最近 30 次延迟检测。
// 两样都没有数据的服务器不在结果里；结果按服务器 id 升序。
func (s *PanelService) ListServerTrends(ctx context.Context, withLatency bool) ([]ServerTrend, error) {
	now := s.now()
	nets, err := s.q.ListRecentServerNet(ctx, pgtype.Timestamptz{Time: now.Add(-trendNetWindow), Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list recent server net: %w", err)
	}
	out := make([]ServerTrend, 0, 16)
	index := make(map[int64]int, 16)
	trendOf := func(serverID int64) *ServerTrend {
		i, ok := index[serverID]
		if !ok {
			i = len(out)
			index[serverID] = i
			t := ServerTrend{ServerID: serverID, Net: make([]ServerNetPoint, 0, trendPoints)}
			if withLatency {
				t.Latency = newLatencySeries(trendPoints)
			}
			out = append(out, t)
		}
		return &out[i]
	}
	for _, r := range nets {
		t := trendOf(r.ServerID)
		t.Net = append(t.Net, ServerNetPoint{At: r.BucketAt.Time, Up: r.NetUpRate.Int64, Down: r.NetDownRate.Int64})
	}
	if !withLatency {
		return out, nil
	}
	rows, err := s.q.ListRecentServerLatency(ctx, sqlcgen.ListRecentServerLatencyParams{
		Since: pgtype.Timestamptz{Time: now.Add(-metricRetention), Valid: true}, PerSeries: trendPoints,
	})
	if err != nil {
		return nil, fmt.Errorf("list recent server latency: %w", err)
	}
	for _, r := range rows {
		trendOf(r.ServerID).Latency.add(r.Isp, LatencyPoint{At: r.SampledAt.Time, AvgMs: int4Ptr(r.AvgMs), LossPercent: int(r.LossPercent)})
	}
	return out, nil
}
