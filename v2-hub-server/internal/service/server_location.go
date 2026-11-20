package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/geoip"
	"v2-hub-server/internal/manager/panel"
)

// 服务器位置扫描与负载采样（docs/03 §5.1，Ruling-65）。
const (
	// MetricRange24h / MetricRange7d 是节点趋势图的两种范围。
	MetricRange24h = "24h"
	MetricRange7d  = "7d"

	metricRetention     = 7 * 24 * time.Hour
	metricWindow24h     = 24 * time.Hour
	metricWindow7d      = 7 * 24 * time.Hour
	metricHourlyCap     = 7 * 24
	metricFiveMinuteCap = 24 * 12
	// loadScale：负载乘 100 存整数（Ruling-83）。
	loadScale = 100

	msgLocateDisabled    = "位置扫描已关闭"
	msgLocateNoIP        = "面板没有报出服务器的公网 IP"
	msgLocateNotPublic   = "面板报出的 IP 不是公网地址"
	msgLocateUnknown     = "IP 库查不到这个 IP 的位置"
	msgLocateUnreachable = "IP 库暂时连不上，请稍后再试"
	msgBadServerRegion   = "位置只能从国家列表里选"
	msgBadMetricRange    = "范围只能是 24h 或 7d"
)

// ServerLocation 是一次位置扫描的结果。
type ServerLocation struct {
	Region   string
	PublicIP string
}

// MetricPoint 是趋势图上的一个点；Load 为 nil 表示该时段 CPU 未知。
type MetricPoint struct {
	At            time.Time
	OnlineClients int
	Load          *float64
}

// validateServerRegion 校验管理员选的位置：空串或合法国家码。
func validateServerRegion(region string) error {
	if region == "" || geoip.IsCountryCode(region) {
		return nil
	}
	return newError(CodeInvalidArgument, msgBadServerRegion)
}

// LocateServer 经面板读服务器自报的公网 IP 再查 IP 库，只返回结果、不保存（编辑表单里的“扫描位置”）。
// 扫描关闭、面板没报 IP、IP 不是公网、IP 库查不到或连不上返回 409；面板连不上返回 502。
func (s *PanelService) LocateServer(ctx context.Context, id int64) (*ServerLocation, error) {
	if s.cfg.Locator == nil {
		return nil, newError(CodeConflict, msgLocateDisabled)
	}
	p, _, err := s.openProvider(ctx, id)
	if err != nil {
		return nil, err
	}
	info, err := p.Probe(ctx)
	if err != nil {
		return nil, panelError(err)
	}
	return s.locateIP(ctx, id, info.PublicIP)
}

func (s *PanelService) locateIP(ctx context.Context, serverID int64, raw string) (*ServerLocation, error) {
	ip, err := netip.ParseAddr(raw)
	if raw == "" || err != nil {
		return nil, newError(CodeConflict, msgLocateNoIP)
	}
	code, err := s.cfg.Locator.Country(ctx, ip)
	switch {
	case err == nil:
		return &ServerLocation{Region: code, PublicIP: ip.String()}, nil
	case errors.Is(err, geoip.ErrNotPublic):
		return nil, newError(CodeConflict, msgLocateNotPublic)
	case errors.Is(err, geoip.ErrUnknown):
		return nil, newError(CodeConflict, msgLocateUnknown)
	default:
		slog.Warn("geoip lookup failed", "serverId", serverID, "err", err)
		return nil, newError(CodeConflict, msgLocateUnreachable)
	}
}

// fillRegionFromIP 用已拿到的公网 IP 补上仍为空的位置；失败只记日志（新建服务器时调用，不影响接入）。
func (s *PanelService) fillRegionFromIP(ctx context.Context, serverID int64, publicIP string) {
	if s.cfg.Locator == nil {
		return
	}
	loc, err := s.locateIP(ctx, serverID, publicIP)
	if err != nil {
		slog.Warn("locate new server failed", "serverId", serverID, "err", err)
		return
	}
	if _, err := s.q.FillServerRegion(ctx, sqlcgen.FillServerRegionParams{ID: serverID, Region: loc.Region}); err != nil {
		slog.Error("save server region failed", "serverId", serverID, "err", err)
	}
}

// BackfillServerLocations 对位置为空的启用服务器各扫一次并保存（启动时调用），返回补上了几台。
// 单台失败只记日志、继续下一台；扫描关闭时什么都不做。
func (s *PanelService) BackfillServerLocations(ctx context.Context) (int, error) {
	if s.cfg.Locator == nil {
		return 0, nil
	}
	ids, err := s.q.ListUnlocatedServerIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("list unlocated servers: %w", err)
	}
	filled := 0
	for _, id := range ids {
		loc, err := s.LocateServer(ctx, id)
		if err != nil {
			slog.Warn("backfill server location failed", "serverId", id, "err", err)
			continue
		}
		n, err := s.q.FillServerRegion(ctx, sqlcgen.FillServerRegionParams{ID: id, Region: loc.Region})
		if err != nil {
			return filled, fmt.Errorf("save region of server %d: %w", id, err)
		}
		filled += int(n)
	}
	return filled, nil
}

// recordMetric 在一次成功的健康探测后记一条采样（Ruling-65、Ruling-83）；面板没给资源快照时只记在线数。
func (s *PanelService) recordMetric(ctx context.Context, serverID int64, res *panel.Resources) error {
	params := sqlcgen.RecordServerMetricParams{ServerID: serverID, SampledAt: pgtype.Timestamptz{Time: s.now(), Valid: true}}
	if res != nil {
		if res.CPU != nil {
			params.Cpu = pgtype.Float8{Float64: *res.CPU, Valid: true}
		}
		params.MemUsed, params.MemTotal = int8Of(res.MemUsed), int8Of(res.MemTotal)
		params.DiskUsed, params.DiskTotal = int8Of(res.DiskUsed), int8Of(res.DiskTotal)
		params.Load1X100 = int4Of(int(math.Round(res.Load1 * loadScale)))
		params.NetUpRate, params.NetDownRate = int8Of(res.NetUpRate), int8Of(res.NetDownRate)
		params.TcpCount, params.UdpCount = int4Of(res.TCPCount), int4Of(res.UDPCount)
	}
	if err := s.q.RecordServerMetric(ctx, params); err != nil {
		return fmt.Errorf("record metric of server %d: %w", serverID, err)
	}
	return nil
}

func int8Of(v int64) pgtype.Int8 {
	return pgtype.Int8{Int64: v, Valid: true}
}

// int4Of 把整数装成可空的 int4；超出 int32 范围的按上限记（采样值不该到这个量级，防面板报出异常值）。
func int4Of(v int) pgtype.Int4 {
	if v > math.MaxInt32 {
		v = math.MaxInt32
	}
	if v < 0 {
		v = 0
	}
	return pgtype.Int4{Int32: int32(v), Valid: true}
}

// PurgeServerMetrics 删除过期的服务器历史：超过 7 天的采样与三网延迟、超过 62 天的按天流量；返回删除条数，由清理任务定时调用。
func (s *PanelService) PurgeServerMetrics(ctx context.Context) (int64, error) {
	before := pgtype.Timestamptz{Time: s.now().Add(-metricRetention), Valid: true}
	total, err := s.q.PurgeServerMetrics(ctx, before)
	if err != nil {
		return 0, fmt.Errorf("purge server metrics: %w", err)
	}
	n, err := s.q.PurgeServerLatency(ctx, before)
	if err != nil {
		return total, fmt.Errorf("purge server latency: %w", err)
	}
	total += n
	n, err = s.q.PurgeServerNicTraffic(ctx, dateOf(s.today(ctx).AddDate(0, 0, -nicTrafficRetentionDays)))
	if err != nil {
		return total, fmt.Errorf("purge server nic traffic: %w", err)
	}
	return total + n, nil
}

// ListNodeMetrics 返回本人某个节点所在服务器的趋势：24h 为 5 分钟原值，7d 按小时平均。
// 节点不在本人节点列表里返回 404，范围不合法返回 400。
func (s *UserService) ListNodeMetrics(ctx context.Context, p *UserPrincipal, nodeID int64, rng string) ([]MetricPoint, error) {
	if rng != MetricRange24h && rng != MetricRange7d {
		return nil, newError(CodeInvalidArgument, msgBadMetricRange)
	}
	serverID, err := s.q.GetUserVisibleNodeServerID(ctx, sqlcgen.GetUserVisibleNodeServerIDParams{UserID: p.UserID, NodeID: nodeID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, newError(CodeNotFound, msgNodeNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("check node %d of user %d: %w", nodeID, p.UserID, err)
	}
	if rng == MetricRange7d {
		return s.listHourlyMetrics(ctx, serverID)
	}
	since := pgtype.Timestamptz{Time: s.now().Add(-metricWindow24h), Valid: true}
	rows, err := s.q.ListServerMetrics(ctx, sqlcgen.ListServerMetricsParams{ServerID: serverID, BucketAt: since})
	if err != nil {
		return nil, fmt.Errorf("list metrics of server %d: %w", serverID, err)
	}
	out := make([]MetricPoint, 0, metricFiveMinuteCap)
	for _, r := range rows {
		pt := MetricPoint{At: r.BucketAt.Time, OnlineClients: int(r.OnlineClients)}
		if r.Cpu.Valid {
			v := r.Cpu.Float64
			pt.Load = &v
		}
		out = append(out, pt)
	}
	return out, nil
}

func (s *UserService) listHourlyMetrics(ctx context.Context, serverID int64) ([]MetricPoint, error) {
	since := pgtype.Timestamptz{Time: s.now().Add(-metricWindow7d), Valid: true}
	rows, err := s.q.ListServerMetricsHourly(ctx, sqlcgen.ListServerMetricsHourlyParams{ServerID: serverID, BucketAt: since})
	if err != nil {
		return nil, fmt.Errorf("list hourly metrics of server %d: %w", serverID, err)
	}
	out := make([]MetricPoint, 0, metricHourlyCap)
	for _, r := range rows {
		pt := MetricPoint{At: r.BucketAt.Time, OnlineClients: int(r.OnlineClients)}
		if r.CpuSamples > 0 {
			v := r.Cpu
			pt.Load = &v
		}
		out = append(out, pt)
	}
	return out, nil
}
