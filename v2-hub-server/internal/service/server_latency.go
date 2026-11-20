package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/reach"
)

// 三网延迟检测（docs/03 §5.20，Ruling-86）：经探测服务从大陆三家运营商的骨干网各取一个探测点，测到服务器公网 IPv4 的延迟与丢包。
// 频率、额度与展示的约定都写在 docs/03，这里不复述。
const (
	// 三家运营商骨干网的 AS 号；按 AS 号取探测点（探测点自报的网络名写法不统一）。
	asnChinaTelecom = 4134
	asnChinaUnicom  = 4837
	asnChinaMobile  = 9808

	// latencyRoundMax 是一轮最多测的服务器数。
	latencyRoundMax = 5
	// latencyBudgetSharePercent 是延迟检测最多占用的额度比例（与大陆连通检测共用同一个小时额度）。
	latencyBudgetSharePercent = 50
	maxProbeCityLen           = 64
)

// latencyNetwork 是一家运营商与它的骨干网 AS 号。
type latencyNetwork struct {
	isp string
	asn int
}

var latencyNetworks = []latencyNetwork{
	{isp: LatencyISPTelecom, asn: asnChinaTelecom},
	{isp: LatencyISPUnicom, asn: asnChinaUnicom},
	{isp: LatencyISPMobile, asn: asnChinaMobile},
}

// LatencyService 负责定时检测各服务器的三网延迟并落库；读取在 PanelService（GetServerMetrics、ListServerTrends）。
type LatencyService struct {
	store
	settings *SettingsService
	prober   reach.LatencyProber
	now      func() time.Time
	// shared 是与大陆连通检测共用的小时额度，own 是其中延迟检测自己最多能用的部分。
	shared *reachBudget
	own    reachBudget
}

// NewLatencyService 创建延迟检测服务。prober 为 nil 时检测不可用（每轮什么都不做）；
// reachSvc 提供共用的小时额度；now 为 nil 时用 time.Now。
func NewLatencyService(pool *pgxpool.Pool, settings *SettingsService, prober reach.LatencyProber, reachSvc *ReachService, now func() time.Time) *LatencyService {
	if now == nil {
		now = time.Now
	}
	shared := reachSvc.sharedBudget()
	return &LatencyService{
		store: newStore(pool), settings: settings, prober: prober, now: now, shared: shared,
		own: reachBudget{limit: shared.limit * latencyBudgetSharePercent / int(percentBase), now: now},
	}
}

// takeBudget 占用一次检测的额度：先过延迟检测自己的上限，再过共用额度。
func (s *LatencyService) takeBudget(cost int) bool {
	return s.own.take(cost) && s.shared.take(cost)
}

// RunRound 跑一轮后台检测：测到期的服务器（最久没测的在前，最多 5 台）。功能没开或探测不可用时什么都不做；
// 额度用完、探测服务限流或连不上时本轮停止。返回这一轮测了几台。
func (s *LatencyService) RunRound(ctx context.Context) (int, error) {
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return 0, err
	}
	if !cur.Latency.Enabled || s.prober == nil {
		return 0, nil
	}
	dueBefore := s.now().Add(-time.Duration(cur.Latency.IntervalMinutes) * time.Minute)
	due, err := s.q.ListLatencyDueServers(ctx, sqlcgen.ListLatencyDueServersParams{
		DueBefore: pgtype.Timestamptz{Time: dueBefore, Valid: true}, RowLimit: latencyRoundMax,
	})
	if err != nil {
		return 0, fmt.Errorf("list latency due servers: %w", err)
	}
	asns := make([]int, 0, len(latencyNetworks))
	for _, n := range latencyNetworks {
		asns = append(asns, n.asn)
	}
	checked := 0
	for _, srv := range due {
		if !s.takeBudget(len(asns)) {
			slog.Info("latency round stopped: hourly budget used up", "checked", checked)
			break
		}
		results, err := s.prober.Latency(ctx, reach.LatencyRequest{Address: srv.PublicIpv4, ASNs: asns})
		if errors.Is(err, reach.ErrRateLimited) {
			s.shared.exhaust()
			slog.Info("latency round stopped: probe service rate limited", "checked", checked)
			break
		}
		if errors.Is(err, reach.ErrUnreachable) {
			slog.Warn("latency round stopped: probe service unreachable", "server_id", srv.ID, "err", err)
			break
		}
		if err != nil {
			// 目标不是公网地址，或探测服务不接受（如三家当时都没有探测点）：记下检测时间，按间隔再试
			slog.Info("latency check skipped", "server_id", srv.ID, "err", err)
			results = nil
		}
		if err := s.save(ctx, srv.ID, results); err != nil {
			return checked, err
		}
		checked++
	}
	return checked, nil
}

// save 落一次检测的结果并记下检测时间；结果为空也记时间。
func (s *LatencyService) save(ctx context.Context, serverID int64, results []reach.LatencyResult) error {
	at := pgtype.Timestamptz{Time: s.now(), Valid: true}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		for _, r := range results {
			isp, ok := ispOfASN(r.ASN)
			if !ok {
				continue
			}
			params := sqlcgen.InsertServerLatencyParams{
				ServerID: serverID, Isp: isp, SampledAt: at, LossPercent: lossPercentOf(r.LossPercent), ProbeCity: truncate(r.City, maxProbeCityLen),
			}
			if r.AvgMs != nil {
				params.AvgMs = int4Of(int(math.Round(*r.AvgMs)))
			}
			if err := q.InsertServerLatency(ctx, params); err != nil {
				return fmt.Errorf("save latency of server %d: %w", serverID, err)
			}
		}
		if err := q.SetServerLatencyChecked(ctx, sqlcgen.SetServerLatencyCheckedParams{ID: serverID, CheckedAt: at}); err != nil {
			return fmt.Errorf("mark latency checked of server %d: %w", serverID, err)
		}
		return nil
	})
}

// ispOfASN 把探测点的 AS 号换成运营商；不是三家骨干网的返回 false。
func ispOfASN(asn int) (string, bool) {
	for _, n := range latencyNetworks {
		if n.asn == asn {
			return n.isp, true
		}
	}
	return "", false
}

// lossPercentOf 把丢包率取整并收进 0–100。
func lossPercentOf(loss float64) int16 {
	return int16(min(max(math.Round(loss), 0), float64(percentBase)))
}
