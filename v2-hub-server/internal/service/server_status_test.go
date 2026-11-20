package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"v2-hub-server/internal/manager/panel"
	"v2-hub-server/internal/manager/reach"
)

// 服务器状态卡片（Ruling-81 – Ruling-84）：资源快照、按天流量与周期、账单、趋势、三网延迟。

func calendarDay(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestTrafficCycle(t *testing.T) {
	cases := []struct {
		today       time.Time
		resetDay    int
		start, next time.Time
	}{
		{calendarDay(2026, 11, 20), 1, calendarDay(2026, 11, 1), calendarDay(2026, 12, 1)},
		{calendarDay(2026, 11, 1), 1, calendarDay(2026, 11, 1), calendarDay(2026, 12, 1)},
		{calendarDay(2026, 11, 20), 25, calendarDay(2026, 10, 25), calendarDay(2026, 11, 25)},
		{calendarDay(2026, 11, 25), 25, calendarDay(2026, 11, 25), calendarDay(2026, 12, 25)},
		// 31 号重置：当月没有 31 号取月末
		{calendarDay(2026, 2, 28), 31, calendarDay(2026, 2, 28), calendarDay(2026, 3, 31)},
		{calendarDay(2026, 2, 27), 31, calendarDay(2026, 1, 31), calendarDay(2026, 2, 28)},
		{calendarDay(2026, 4, 30), 31, calendarDay(2026, 4, 30), calendarDay(2026, 5, 31)},
		// 跨年
		{calendarDay(2027, 1, 5), 10, calendarDay(2026, 12, 10), calendarDay(2027, 1, 10)},
		{calendarDay(2026, 12, 15), 10, calendarDay(2026, 12, 10), calendarDay(2027, 1, 10)},
	}
	for _, c := range cases {
		start, next := trafficCycle(c.today, c.resetDay)
		if !start.Equal(c.start) || !next.Equal(c.next) {
			t.Errorf("trafficCycle(%s, %d) = %s, %s; want %s, %s", c.today.Format(time.DateOnly), c.resetDay,
				start.Format(time.DateOnly), next.Format(time.DateOnly), c.start.Format(time.DateOnly), c.next.Format(time.DateOnly))
		}
	}
}

func TestCivilDateUsesSiteTimezone(t *testing.T) {
	shanghai := time.FixedZone("UTC+8", 8*60*60)
	// UTC 11-19 17:00 在东八区已是 11-20
	if got := civilDate(time.Date(2026, 11, 19, 17, 0, 0, 0, time.UTC), shanghai); !got.Equal(calendarDay(2026, 11, 20)) {
		t.Fatalf("civilDate = %s", got)
	}
}

func TestUsedByMode(t *testing.T) {
	cases := map[string]int64{TrafficModeBoth: 30, TrafficModeOut: 10, TrafficModeMax: 20}
	for mode, want := range cases {
		if got := usedByMode(mode, 10, 20); got != want {
			t.Errorf("usedByMode(%s) = %d, want %d", mode, got, want)
		}
	}
}

func TestLossPercentOf(t *testing.T) {
	cases := map[float64]int16{0: 0, 12.5: 13, 100: 100, 130: 100, -1: 0}
	for in, want := range cases {
		if got := lossPercentOf(in); got != want {
			t.Errorf("lossPercentOf(%v) = %d, want %d", in, got, want)
		}
	}
}

func TestISPOfASN(t *testing.T) {
	for asn, want := range map[int]string{asnChinaTelecom: LatencyISPTelecom, asnChinaUnicom: LatencyISPUnicom, asnChinaMobile: LatencyISPMobile} {
		if got, ok := ispOfASN(asn); !ok || got != want {
			t.Errorf("ispOfASN(%d) = %q, %v", asn, got, ok)
		}
	}
	if _, ok := ispOfASN(45090); ok {
		t.Error("cloud network should not map to an isp")
	}
}

func cpuOf(v float64) *float64 {
	return &v
}

// TestServerResourcesAndTraffic：探测写快照、公网 IP 与采样；网卡计数按天累加（含重启），周期用量与校准。
func TestServerResourcesAndTraffic(t *testing.T) {
	f := newPanelFixture(t)
	ctx := context.Background()
	f.clock.t = time.Date(2026, 11, 20, 2, 1, 0, 0, time.UTC)
	f.panels.cfg.SiteLocation = func(context.Context) *time.Location { return time.UTC }
	srv, err := f.panels.CreateServer(ctx, f.actor, serverInput("status"), nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	fake := f.fleet.get(fakeBase)
	probe := func(sent, recv int64) {
		t.Helper()
		fake.SetResources(panel.Resources{
			CPU: cpuOf(12.5), CPUCores: 2, MemUsed: 300, MemTotal: 1000, DiskUsed: 30, DiskTotal: 200, Load1: 0.37,
			NetUpRate: 2900, NetDownRate: 2950, NetSent: sent, NetRecv: recv, TCPCount: 42, UDPCount: 7, CoreState: panel.CoreRunning,
		})
		if err := f.panels.ProbeServer(ctx, srv.ID); err != nil {
			t.Fatalf("ProbeServer: %v", err)
		}
	}
	// 接入时第一次探测只记基线；之后的增量记到当天
	probe(1000, 2000)
	f.clock.advance(10 * time.Minute)
	probe(1500, 2600)
	f.clock.advance(time.Hour)
	probe(200, 100) // 机器重启：计数变小，本次增量取当前值
	got, err := f.panels.GetServer(ctx, srv.ID)
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	if got.Resources == nil || got.Resources.MemTotal != 1000 || got.Resources.TCPCount != 42 || got.PublicIPv4 == "" {
		t.Fatalf("resources = %+v, ipv4 %q", got.Resources, got.PublicIPv4)
	}
	if got.Traffic.SentBytes != 700 || got.Traffic.RecvBytes != 700 || got.Traffic.UsedBytes != 1400 {
		t.Fatalf("traffic = %+v", got.Traffic)
	}
	if !got.Traffic.CycleStart.Equal(calendarDay(2026, 11, 1)) || !got.Traffic.NextResetOn.Equal(calendarDay(2026, 12, 1)) {
		t.Fatalf("cycle = %+v", got.Traffic)
	}

	// 离线时保留上一份快照
	fake.FailWith(&panel.Error{Kind: panel.KindUnreachable, Message: "down"})
	if err := f.panels.ProbeServer(ctx, srv.ID); err == nil {
		t.Fatal("probe should fail")
	}
	fake.FailWith(nil)
	if s, _ := f.panels.GetServer(ctx, srv.ID); s.Status != ServerStatusOffline || s.Resources == nil {
		t.Fatalf("offline server = %s, %+v", s.Status, s.Resources)
	}

	// 账单：非法取值 400；校准后本周期已用等于填写值，改了计量方式校准作废
	quota := int64(1 << 40)
	price := int32(4990)
	expires := calendarDay(2027, 11, 1)
	in := ServerBillingInput{ServerBilling: ServerBilling{
		PriceCents: &price, PriceCurrency: "USD", BillingCycle: BillingCycleYear, ExpiresOn: &expires,
		TrafficQuotaBytes: &quota, TrafficResetDay: 1, TrafficMode: TrafficModeBoth,
	}}
	for name, mutate := range map[string]func(b *ServerBillingInput){
		"currency": func(b *ServerBillingInput) { b.PriceCurrency = "usd" },
		"cycle":    func(b *ServerBillingInput) { b.BillingCycle = "week" },
		"resetDay": func(b *ServerBillingInput) { b.TrafficResetDay = 32 },
		"mode":     func(b *ServerBillingInput) { b.TrafficMode = "in" },
		"price":    func(b *ServerBillingInput) { v := int32(-1); b.PriceCents = &v },
		"used":     func(b *ServerBillingInput) { v := int64(-1); b.TrafficUsedBytes = &v },
	} {
		bad := in
		mutate(&bad)
		if _, err := f.panels.SetServerBilling(ctx, f.actor, srv.ID, bad, nil); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := f.panels.SetServerBilling(ctx, f.actor, srv.ID+1000, in, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing server err = %v", err)
	}
	used := int64(173 << 30)
	in.TrafficUsedBytes = &used
	saved, err := f.panels.SetServerBilling(ctx, f.actor, srv.ID, in, nil)
	if err != nil {
		t.Fatalf("SetServerBilling: %v", err)
	}
	if saved.Traffic.UsedBytes != used || *saved.Billing.PriceCents != price || !saved.Billing.ExpiresOn.Equal(expires) {
		t.Fatalf("saved = %+v / %+v", saved.Billing, saved.Traffic)
	}
	// 不带校准再保存一次：校准值保留
	in.TrafficUsedBytes = nil
	if again, err := f.panels.SetServerBilling(ctx, f.actor, srv.ID, in, nil); err != nil || again.Traffic.UsedBytes != used {
		t.Fatalf("resave = %+v, %v", again, err)
	}
	in.TrafficMode = TrafficModeOut
	if changed, err := f.panels.SetServerBilling(ctx, f.actor, srv.ID, in, nil); err != nil || changed.Traffic.UsedBytes != 700 {
		t.Fatalf("mode change = %+v, %v", changed, err)
	}
	// 下一个周期：校准值作废，用量从 0 起
	in.TrafficMode = TrafficModeBoth
	in.TrafficUsedBytes = &used
	if _, err := f.panels.SetServerBilling(ctx, f.actor, srv.ID, in, nil); err != nil {
		t.Fatalf("calibrate: %v", err)
	}
	f.clock.t = time.Date(2026, 12, 1, 3, 0, 0, 0, time.UTC)
	if next, _ := f.panels.GetServer(ctx, srv.ID); next.Traffic.UsedBytes != 0 || !next.Traffic.CycleStart.Equal(calendarDay(2026, 12, 1)) {
		t.Fatalf("next cycle = %+v", next.Traffic)
	}
}

func TestServerResourceMetricsAndTrends(t *testing.T) {
	f := newPanelFixture(t)
	ctx := context.Background()
	f.clock.t = time.Date(2026, 11, 20, 10, 1, 0, 0, time.UTC)
	srv, err := f.panels.CreateServer(ctx, f.actor, serverInput("metrics"), nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	fake := f.fleet.get(fakeBase)
	for i, up := range []int64{100, 300} {
		f.clock.advance(10 * time.Minute)
		fake.SetResources(panel.Resources{CPU: cpuOf(float64(10 * (i + 1))), MemUsed: 100, MemTotal: 400, Load1: 1.25, NetUpRate: up, NetDownRate: up * 2})
		if err := f.panels.ProbeServer(ctx, srv.ID); err != nil {
			t.Fatalf("ProbeServer: %v", err)
		}
	}
	m, err := f.panels.GetServerMetrics(ctx, srv.ID, MetricRange24h, false)
	if err != nil || m.Latency != nil {
		t.Fatalf("24h = %+v, %v", m, err)
	}
	last := m.Points[len(m.Points)-1]
	if last.MemTotal == nil || *last.MemTotal != 400 || last.Load1 == nil || *last.Load1 != 1.25 || *last.NetUpRate != 300 {
		t.Fatalf("last point = %+v", last)
	}
	hourly, err := f.panels.GetServerMetrics(ctx, srv.ID, MetricRange7d, true)
	if err != nil || len(hourly.Points) == 0 || hourly.Latency == nil || hourly.Points[0].MemTotal == nil {
		t.Fatalf("7d = %+v, %v", hourly, err)
	}
	if _, err := f.panels.GetServerMetrics(ctx, srv.ID, "1h", false); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("bad range err = %v", err)
	}
	if _, err := f.panels.GetServerMetrics(ctx, srv.ID+1000, MetricRange24h, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing server err = %v", err)
	}
	trends, err := f.panels.ListServerTrends(ctx, false)
	if err != nil || len(trends) != 1 || trends[0].ServerID != srv.ID || trends[0].Latency != nil {
		t.Fatalf("trends = %+v, %v", trends, err)
	}
	net := trends[0].Net
	if net[len(net)-1].Up != 300 || net[len(net)-1].Down != 600 {
		t.Fatalf("net = %+v", net)
	}
}

// fakeLatencyProber 按 AS 号回结果；fail 非空时整次失败。
type fakeLatencyProber struct {
	mu      sync.Mutex
	results []reach.LatencyResult
	fail    error
	calls   []reach.LatencyRequest
}

func (p *fakeLatencyProber) Latency(_ context.Context, req reach.LatencyRequest) ([]reach.LatencyResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, req)
	if p.fail != nil {
		return nil, p.fail
	}
	return p.results, nil
}

func (p *fakeLatencyProber) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func TestLatencyRound(t *testing.T) {
	f := newReachFixture(t)
	ctx := context.Background()
	rtt := 130.4
	prober := &fakeLatencyProber{results: []reach.LatencyResult{
		{ASN: asnChinaTelecom, City: "Shanghai", AvgMs: &rtt, LossPercent: 1.7},
		{ASN: asnChinaUnicom, City: "Wuhan", LossPercent: 100},
		{ASN: 45090, City: "Shenzhen", AvgMs: &rtt},
	}}
	svc := NewLatencyService(f.pool, f.settings, prober, f.reach, f.clock.now)

	// 没开：不测
	if n, err := svc.RunRound(ctx); err != nil || n != 0 || prober.callCount() != 0 {
		t.Fatalf("disabled round = %d, %v, calls %d", n, err, prober.callCount())
	}
	const interval = 15
	if _, err := f.set(t, map[string]map[string]any{"latency": {"enabled": true, "intervalMinutes": interval}}); err != nil {
		t.Fatalf("enable latency: %v", err)
	}
	if _, err := f.set(t, map[string]map[string]any{"latency": {"intervalMinutes": 5}}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("interval below minimum err = %v", err)
	}
	if n, err := svc.RunRound(ctx); err != nil || n != 1 {
		t.Fatalf("first round = %d, %v", n, err)
	}
	req := prober.calls[0]
	if req.Address == "" || len(req.ASNs) != len(latencyNetworks) {
		t.Fatalf("request = %+v", req)
	}
	// 间隔内不重测
	if n, _ := svc.RunRound(ctx); n != 0 {
		t.Fatalf("within interval = %d", n)
	}
	m, err := f.panels.GetServerMetrics(ctx, f.server.ID, MetricRange24h, true)
	if err != nil {
		t.Fatalf("GetServerMetrics: %v", err)
	}
	l := m.Latency
	if len(l.Telecom) != 1 || *l.Telecom[0].AvgMs != 130 || l.Telecom[0].LossPercent != 2 {
		t.Fatalf("telecom = %+v", l.Telecom)
	}
	if len(l.Unicom) != 1 || l.Unicom[0].AvgMs != nil || l.Unicom[0].LossPercent != 100 || len(l.Mobile) != 0 {
		t.Fatalf("unicom / mobile = %+v / %+v", l.Unicom, l.Mobile)
	}
	trends, err := f.panels.ListServerTrends(ctx, true)
	if err != nil || len(trends) != 1 || trends[0].Latency == nil || len(trends[0].Latency.Telecom) != 1 {
		t.Fatalf("trends = %+v, %v", trends, err)
	}

	// 限流：本轮停止，共用额度记为用完，连通检测本小时也不再测
	prober.fail = reach.ErrRateLimited
	f.clock.advance((interval + 1) * time.Minute)
	if n, err := svc.RunRound(ctx); err != nil || n != 0 {
		t.Fatalf("rate limited round = %d, %v", n, err)
	}
	if f.reach.budget.take(1) {
		t.Fatal("shared budget should be exhausted")
	}
	// 探测服务不接受（如没有探测点）：记下检测时间，按间隔再试
	f.clock.advance(time.Hour)
	prober.fail = reach.ErrRejected
	if n, err := svc.RunRound(ctx); err != nil || n != 1 {
		t.Fatalf("rejected round = %d, %v", n, err)
	}
	if n, _ := svc.RunRound(ctx); n != 0 {
		t.Fatalf("rejected server should wait for the interval, got %d", n)
	}

	f.clock.advance(8 * 24 * time.Hour)
	if _, err := f.panels.PurgeServerMetrics(ctx); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if m, _ := f.panels.GetServerMetrics(ctx, f.server.ID, MetricRange7d, true); len(m.Latency.Telecom) != 0 {
		t.Fatalf("latency should be purged: %+v", m.Latency.Telecom)
	}
}

// TestLatencyBudgetShare：延迟检测最多用共用额度的一半，余下留给连通检测。
func TestLatencyBudgetShare(t *testing.T) {
	shared := &reachBudget{limit: 10, now: time.Now}
	svc := &LatencyService{shared: shared, own: reachBudget{limit: shared.limit * latencyBudgetSharePercent / int(percentBase), now: time.Now}}
	if !svc.takeBudget(3) || svc.takeBudget(3) {
		t.Fatal("latency should get at most half of the budget")
	}
	if !shared.take(7) {
		t.Fatal("the rest should stay available to reach checks")
	}
}
