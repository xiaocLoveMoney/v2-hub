package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"v2-hub-server/internal/manager/reach"
)

const (
	sqlReachRow         = `SELECT verdict, blocked_streak, alert_sent_at IS NOT NULL, ip_reachable FROM node_reach WHERE port = $1`
	sqlCountReach       = `SELECT COUNT(*) FROM node_reach WHERE id > $1`
	sqlDisableNode      = `UPDATE node SET is_enabled = FALSE WHERE id = $1`
	sqlSetInboundProto  = `UPDATE inbound SET protocol = $2 WHERE id = $1`
	reachPortA          = 443
	reachPortB          = 8443
	reachIntervalMinute = 30
)

// fakeProber 按（端口，方式）返回预设的探测结果；没预设的一律全通。
type fakeProber struct {
	mu      sync.Mutex
	results map[string][]reach.ProbeResult
	fail    error
	calls   []reach.Request
}

func probeKey(port int, mode reach.Mode) string {
	return fmt.Sprintf("%d/%s", port, mode)
}

// probes 造一组探测结果：cnOK / cnAll 个大陆点，controlOK / controlAll 个境外对照点。
func probes(cnOK, cnAll, controlOK, controlAll int) []reach.ProbeResult {
	out := make([]reach.ProbeResult, 0, cnAll+controlAll)
	rtt := 20.5
	for i := range cnAll {
		r := reach.ProbeResult{IsMainland: true, IsCounted: true, Country: "CN", City: "Shanghai", Network: "China Telecom"}
		if i < cnOK {
			r.IsOK, r.RTTMs = true, &rtt
		}
		out = append(out, r)
	}
	for i := range controlAll {
		r := reach.ProbeResult{IsCounted: true, Country: "JP", City: "Tokyo", Network: "NTT"}
		if i < controlOK {
			r.IsOK, r.RTTMs = true, &rtt
		}
		out = append(out, r)
	}
	return out
}

func (p *fakeProber) set(port int, mode reach.Mode, results []reach.ProbeResult) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.results[probeKey(port, mode)] = results
}

func (p *fakeProber) Probe(_ context.Context, req reach.Request) ([]reach.ProbeResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, req)
	if p.fail != nil {
		return nil, p.fail
	}
	if r, ok := p.results[probeKey(req.Port, req.Mode)]; ok {
		return r, nil
	}
	return probes(3, 3, 2, 2), nil
}

func (p *fakeProber) Cost(req reach.Request) int {
	if req.MainlandOnly {
		return reach.MainlandProbes
	}
	return reach.MainlandProbes + 2
}

func (p *fakeProber) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

type reachFixture struct {
	*opsFixture
	prober *fakeProber
	reach  *ReachService
}

// newReachFixture：opsFixture 里的一台服务器（面板主机名 panel-a.example.com）上有 443、8443 两个直连节点，即两个检测目标。
func newReachFixture(t *testing.T) *reachFixture {
	t.Helper()
	of := newOpsFixture(t)
	prober := &fakeProber{results: make(map[string][]reach.ProbeResult, 4)}
	svc := NewReachService(of.pool, of.settings, of.ops, prober, ReachOptions{Now: of.clock.now})
	return &reachFixture{opsFixture: of, prober: prober, reach: svc}
}

func (f *reachFixture) enable(t *testing.T) {
	t.Helper()
	if _, err := f.set(t, map[string]map[string]any{"reach": {"enabled": true, "intervalMinutes": reachIntervalMinute}}); err != nil {
		t.Fatalf("enable reach: %v", err)
	}
}

func (f *reachFixture) round(t *testing.T) int {
	t.Helper()
	n, err := f.reach.RunRound(context.Background())
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	return n
}

// nextRound 把时钟拨过一个检测间隔再跑一轮。
func (f *reachFixture) nextRound(t *testing.T) int {
	t.Helper()
	f.clock.advance((reachIntervalMinute + 1) * time.Minute)
	return f.round(t)
}

type reachRow struct {
	verdict     string
	streak      int
	isAlerted   bool
	ipReachable *bool
}

func (f *reachFixture) row(t *testing.T, port int) reachRow {
	t.Helper()
	var r reachRow
	if err := f.pool.QueryRow(context.Background(), sqlReachRow, port).Scan(&r.verdict, &r.streak, &r.isAlerted, &r.ipReachable); err != nil {
		t.Fatalf("reach row of port %d: %v", port, err)
	}
	return r
}

func TestReachDisabledByDefault(t *testing.T) {
	f := newReachFixture(t)
	ctx := context.Background()
	if n := f.round(t); n != 0 || f.prober.callCount() != 0 {
		t.Fatalf("disabled round checked %d targets, probes = %d", n, f.prober.callCount())
	}
	briefs, err := f.reach.NodeBriefs(ctx)
	if err != nil || briefs != nil {
		t.Fatalf("NodeBriefs when disabled = %v, %v", briefs, err)
	}
	if sum, err := f.reach.Summary(ctx); err != nil || sum != nil {
		t.Fatalf("Summary when disabled = %v, %v", sum, err)
	}
	_, err = f.reach.NodeDetail(ctx, f.nodes[0].ID)
	wantErr(t, "detail when disabled", err, ErrFeatureDisabled)
	_, err = f.reach.CheckNode(ctx, f.actor, f.nodes[0].ID)
	wantErr(t, "check when disabled", err, ErrFeatureDisabled)
}

func TestReachRoundAndAlerts(t *testing.T) {
	f := newReachFixture(t)
	ctx := context.Background()
	f.enable(t)

	// 第一轮：两个目标都测，都通
	if n := f.round(t); n != 2 {
		t.Fatalf("first round checked %d, want 2", n)
	}
	if r := f.row(t, reachPortA); r.verdict != ReachVerdictOK || r.streak != 0 {
		t.Fatalf("port %d = %+v", reachPortA, r)
	}
	// 没到间隔：不重测
	if n := f.round(t); n != 0 {
		t.Fatalf("round before interval checked %d", n)
	}
	briefs, err := f.reach.NodeBriefs(ctx)
	if err != nil || len(briefs) != 2 || briefs[f.nodes[0].ID].Verdict != ReachVerdictOK || briefs[f.nodes[0].ID].CnTotal != 3 {
		t.Fatalf("briefs = %+v, %v", briefs, err)
	}

	// 443 大陆全不通、境外通：第一轮 blocked 不告警，并补测 ICMP（还通 = 只封端口）
	f.prober.set(reachPortA, reach.ModeTCP, probes(0, 3, 2, 2))
	f.prober.set(0, reach.ModeICMP, probes(2, 3, 0, 0))
	f.nextRound(t)
	r := f.row(t, reachPortA)
	if r.verdict != ReachVerdictBlocked || r.streak != 1 || r.isAlerted || r.ipReachable == nil || !*r.ipReachable {
		t.Fatalf("after first blocked round = %+v", r)
	}
	if n := f.count(t, sqlCountMailSubject, "%大陆连不上%"); n != 0 {
		t.Fatalf("must not alert after one round, mails = %d", n)
	}

	// 第二轮仍 blocked：告警一次；第三轮不重复
	f.nextRound(t)
	f.nextRound(t)
	if r := f.row(t, reachPortA); r.streak != 3 || !r.isAlerted {
		t.Fatalf("after three blocked rounds = %+v", r)
	}
	if n := f.count(t, sqlCountMailSubject, "%panel-a.example.com:443 大陆连不上%"); n != 1 {
		t.Fatalf("blocked mails = %d, want 1", n)
	}
	if n := f.count(t, sqlCountAudit, ActionReachBlocked); n != 1 {
		t.Fatalf("blocked audits = %d, want 1", n)
	}
	if _, err := f.telegram.ProcessOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	msgs := f.tg.messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "大陆探测点：0 / 3 连通") || !strings.Contains(msgs[0], "只有这个端口不通") || !strings.Contains(msgs[0], f.server.Name) {
		t.Fatalf("telegram messages = %v", msgs)
	}

	// 汇总与服务器计数
	sum, err := f.reach.Summary(ctx)
	if err != nil || sum.Blocked != 1 || sum.OK != 1 || len(sum.Problems) != 1 || sum.Problems[0].Port != reachPortA || sum.Problems[0].NodeCount != 1 {
		t.Fatalf("summary = %+v, %v", sum, err)
	}
	counts, err := f.reach.ServerCounts(ctx)
	if err != nil || counts[f.server.ID] != (ReachCount{Total: 2, OK: 1, Blocked: 1}) {
		t.Fatalf("server counts = %+v, %v", counts, err)
	}

	// 部分通不算恢复；全通才发恢复通知
	f.prober.set(reachPortA, reach.ModeTCP, probes(1, 3, 2, 2))
	f.nextRound(t)
	if r := f.row(t, reachPortA); r.verdict != ReachVerdictPartial || r.streak != 0 || !r.isAlerted {
		t.Fatalf("partial = %+v", r)
	}
	f.prober.set(reachPortA, reach.ModeTCP, probes(3, 3, 2, 2))
	f.nextRound(t)
	if r := f.row(t, reachPortA); r.verdict != ReachVerdictOK || r.isAlerted {
		t.Fatalf("recovered = %+v", r)
	}
	if n := f.count(t, sqlCountMailSubject, "%大陆已恢复连通%"); n != 1 {
		t.Fatalf("recovery mails = %d, want 1", n)
	}
	if n := f.count(t, sqlCountAudit, ActionReachRecovered); n != 1 {
		t.Fatalf("recovery audits = %d, want 1", n)
	}
}

func TestReachNotifyOffAndOfflineServer(t *testing.T) {
	f := newReachFixture(t)
	f.enable(t)
	if _, err := f.set(t, map[string]map[string]any{"reach": {"notify": false}}); err != nil {
		t.Fatal(err)
	}
	f.prober.set(reachPortA, reach.ModeTCP, probes(0, 3, 2, 2))
	f.round(t)
	f.nextRound(t)
	if r := f.row(t, reachPortA); r.streak != 2 || r.isAlerted {
		t.Fatalf("notify off must not mark alerted: %+v", r)
	}
	if n := f.count(t, sqlCountMailSubject, "%大陆连不上%"); n != 0 {
		t.Fatalf("notify off mails = %d", n)
	}

	// 服务器不在线：目标暂停检测，记录保留
	f.exec(t, sqlSetServerOffline, f.server.ID, f.clock.now())
	before := f.prober.callCount()
	if n := f.nextRound(t); n != 0 || f.prober.callCount() != before {
		t.Fatalf("offline server must pause checks: checked %d", n)
	}
	if n := f.count(t, sqlCountReach, 0); n != 2 {
		t.Fatalf("rows kept = %d, want 2", n)
	}

	// 节点停用：目标删除
	f.exec(t, sqlSetServerOnline, f.server.ID)
	f.exec(t, sqlDisableNode, f.nodes[0].ID)
	f.nextRound(t)
	if n := f.count(t, sqlCountReach, 0); n != 1 {
		t.Fatalf("rows after disabling a node = %d, want 1", n)
	}
	_, err := f.reach.NodeDetail(context.Background(), f.nodes[0].ID)
	wantErr(t, "disabled node is out of scope", err, ErrConflict)
}

func TestReachProbeFailuresAndUDP(t *testing.T) {
	f := newReachFixture(t)
	f.enable(t)

	// 探测服务连不上：结论 unknown 并记原因，不算一轮 blocked
	f.prober.fail = reach.ErrUnreachable
	if n := f.round(t); n != 2 {
		t.Fatalf("round with unreachable prober checked %d", n)
	}
	if r := f.row(t, reachPortA); r.verdict != ReachVerdictUnknown || r.streak != 0 {
		t.Fatalf("unreachable = %+v", r)
	}
	// 探测服务限流：本轮停止，不落库
	f.prober.fail = reach.ErrRateLimited
	before := f.prober.callCount()
	if n := f.nextRound(t); n != 0 || f.prober.callCount() != before+1 {
		t.Fatalf("rate limited round checked %d, probes +%d", n, f.prober.callCount()-before)
	}
	f.prober.fail = nil

	// hysteria2 入站：只测 ICMP，境内外都不通判 unknown，大陆不通境外通判 blocked
	f.exec(t, sqlSetInboundProto, f.nodes[1].InboundID, "hysteria2")
	f.reach.budget.used = 0
	f.prober.set(reachPortB, reach.ModeICMP, probes(0, 3, 0, 2))
	f.nextRound(t)
	var transport, verdict string
	if err := f.pool.QueryRow(context.Background(), `SELECT transport, verdict FROM node_reach WHERE port = $1`, reachPortB).Scan(&transport, &verdict); err != nil {
		t.Fatal(err)
	}
	if transport != reachTransportUDP || verdict != ReachVerdictUnknown {
		t.Fatalf("udp target = %s / %s", transport, verdict)
	}
	hasICMP := false
	for _, c := range f.prober.calls {
		hasICMP = hasICMP || (c.Port == reachPortB && c.Mode == reach.ModeICMP)
	}
	if !hasICMP {
		t.Fatalf("udp target must be probed with icmp: %+v", f.prober.calls)
	}
	detail, err := f.reach.NodeDetail(context.Background(), f.nodes[1].ID)
	if err != nil || detail.Scope != ReachScopeIP || detail.Transport != reachTransportUDP {
		t.Fatalf("udp detail = %+v, %v", detail, err)
	}
}

func TestReachManualCheck(t *testing.T) {
	f := newReachFixture(t)
	ctx := context.Background()
	f.enable(t)
	nodeID := f.nodes[0].ID

	// 还没测过：pending
	d, err := f.reach.NodeDetail(ctx, nodeID)
	if err != nil || d.Verdict != ReachVerdictPending || d.Port != reachPortA || d.Address != "panel-a.example.com" {
		t.Fatalf("pending detail = %+v, %v", d, err)
	}
	_, err = f.reach.NodeDetail(ctx, nodeID+100000)
	wantErr(t, "unknown node", err, ErrNotFound)

	f.prober.set(reachPortA, reach.ModeTCP, probes(0, 3, 0, 2))
	d, err = f.reach.CheckNode(ctx, f.actor, nodeID)
	if err != nil || d.Verdict != ReachVerdictPortDown || d.ControlTotal != 2 || len(d.Probes) != 5 || d.CheckedAt == nil {
		t.Fatalf("manual check = %+v, %v", d, err)
	}

	// 探测服务连不上：502，不改已有结论
	f.prober.fail = reach.ErrUnreachable
	_, err = f.reach.CheckNode(ctx, f.actor, nodeID)
	wantErr(t, "prober unreachable", err, ErrUpstream)
	if r := f.row(t, reachPortA); r.verdict != ReachVerdictPortDown {
		t.Fatalf("failed manual check must keep the verdict: %+v", r)
	}
	f.prober.fail = nil

	// 每个管理员每分钟 6 次
	for i := 2; i < defaultReachManualLimit; i++ {
		if _, err := f.reach.CheckNode(ctx, f.actor, nodeID); err != nil {
			t.Fatalf("check %d: %v", i+1, err)
		}
	}
	_, err = f.reach.CheckNode(ctx, f.actor, nodeID)
	wantErr(t, "manual rate limit", err, ErrRateLimited)
	f.clock.advance(2 * time.Minute)

	// 本小时额度用完：429
	f.reach.budget.used = f.reach.budget.limit
	_, err = f.reach.CheckNode(ctx, f.actor, nodeID)
	wantErr(t, "budget used up", err, ErrRateLimited)
	f.clock.advance(time.Hour)
	if _, err := f.reach.CheckNode(ctx, f.actor, nodeID); err != nil {
		t.Fatalf("budget resets hourly: %v", err)
	}
}

func TestJudgeReach(t *testing.T) {
	for _, tc := range []struct {
		name      string
		results   []reach.ProbeResult
		transport string
		want      string
	}{
		{"all mainland ok", probes(3, 3, 2, 2), reachTransportTCP, ReachVerdictOK},
		{"mainland ok without control", probes(2, 2, 0, 0), reachTransportTCP, ReachVerdictOK},
		{"some mainland fail", probes(2, 3, 2, 2), reachTransportTCP, ReachVerdictPartial},
		{"blocked", probes(0, 3, 1, 2), reachTransportTCP, ReachVerdictBlocked},
		{"port down", probes(0, 3, 0, 2), reachTransportTCP, ReachVerdictPortDown},
		{"one mainland probe is not enough", probes(0, 1, 2, 2), reachTransportTCP, ReachVerdictUnknown},
		{"no control probe", probes(0, 3, 0, 0), reachTransportTCP, ReachVerdictUnknown},
		{"no mainland probe", probes(0, 0, 2, 2), reachTransportTCP, ReachVerdictUnknown},
		{"udp blocked", probes(0, 3, 2, 2), reachTransportUDP, ReachVerdictBlocked},
		{"udp no ping anywhere", probes(0, 3, 0, 2), reachTransportUDP, ReachVerdictUnknown},
	} {
		if got := judgeReach(tc.results, tc.transport); got.verdict != tc.want {
			t.Errorf("%s: verdict = %s, want %s", tc.name, got.verdict, tc.want)
		}
	}
	// 探测点自己出错的不计入
	mixed := append(probes(0, 2, 1, 1), reach.ProbeResult{IsMainland: true, IsOK: true})
	if got := judgeReach(mixed, reachTransportTCP); got.verdict != ReachVerdictBlocked || got.cnTotal != 2 {
		t.Fatalf("uncounted probe must be ignored: %+v", got)
	}
}

func TestReachSettingsValidation(t *testing.T) {
	f := newReachFixture(t)
	for what, patch := range map[string]map[string]any{
		"interval too short": {"reach.intervalMinutes": 5},
		"interval too long":  {"reach.intervalMinutes": 1441},
	} {
		if _, err := f.update(t, patch); err == nil {
			t.Fatalf("%s should be rejected", what)
		}
	}
	cur, err := f.update(t, map[string]any{"reach.intervalMinutes": 10})
	if err != nil || cur.Reach.IntervalMinutes != 10 || cur.Reach.Enabled || !cur.Reach.Notify {
		t.Fatalf("reach settings = %+v, %v", cur.Reach, err)
	}
}
