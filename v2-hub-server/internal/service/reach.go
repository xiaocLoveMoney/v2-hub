package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/ratelimit"
	"v2-hub-server/internal/manager/reach"
)

// 大陆连通检测（docs/03 §5.20 大陆连通检测，Ruling-67）：由第三方探测点从大陆与境外去连节点的对外地址，
// hub-server 自己不连节点。判定规则、额度与告警都写在 docs/03，这里不复述。

// 检测结论，对应 hub-api.yaml ReachVerdict。
const (
	ReachVerdictPending  = "pending"
	ReachVerdictOK       = "ok"
	ReachVerdictPartial  = "partial"
	ReachVerdictBlocked  = "blocked"
	ReachVerdictPortDown = "port_down"
	ReachVerdictUnknown  = "unknown"
)

// 检测范围，对应 hub-api.yaml ReachScope。
const (
	ReachScopePort = "port"
	ReachScopeIP   = "ip"
)

const (
	ActionReachBlocked   = "node.reach_blocked"
	ActionReachRecovered = "node.reach_recovered"
	targetReach          = "node_reach"

	reachTransportTCP = "tcp"
	reachTransportUDP = "udp"

	// reachRoundMax 是后台每轮最多测几个目标；reachAlertStreak 是连续几轮 blocked 才告警。
	reachRoundMax    = 5
	reachAlertStreak = 2
	// reachMinMainland 是判 blocked 至少要计入几个大陆探测点。
	reachMinMainland = 2
	// reachBudgetPercent 是每小时额度里实际使用的比例，留一成余量。
	reachBudgetPercent = 90
	reachBudgetWindow  = time.Hour

	defaultReachManualLimit  = 6
	defaultReachManualWindow = time.Minute
	reachGuardMaxKeys        = 1024
	reachMaxNames            = 5
	reachProblemsMax         = 20
	maxReachErrorLen         = 255
	serverStatusOnline       = "online"

	msgReachDisabled    = "大陆连通检测没有开启"
	msgReachOutOfScope  = "节点不在检测范围内（节点或入站已停用、面板上已不存在，或所在服务器已停用）"
	msgReachBusy        = "这个目标正在检测，请稍后再试"
	msgReachRateLimited = "检测太频繁，请稍后再试"
	msgReachNoBudget    = "本小时的探测额度已用完，请稍后再试"
	msgReachUpstream    = "探测服务暂时连不上，请稍后再试"
	msgReachNotPublic   = "地址不是公网地址，无法从外部探测"
	msgReachRejected    = "探测服务没有接受这个目标"
	msgReachNoProbes    = "没有可用的大陆探测点"
)

// reachUDPProtocols / reachUDPNetworks 是走 UDP 的协议与传输方式：这类入站没法用 TCP 连接验证，只测 ICMP。
var (
	reachUDPProtocols = map[string]bool{"hysteria": true, "hysteria2": true, "tuic": true, "wireguard": true, "masque": true}
	reachUDPNetworks  = map[string]bool{"kcp": true, "mkcp": true, "quic": true}
)

// errReachBudget 表示本小时的探测额度用完；errReachBusy 表示同一目标正在检测。
var (
	errReachBudget = errors.New("reach hourly budget exhausted")
	errReachBusy   = errors.New("reach target is being checked")
)

// ReachOptions 是连通检测服务的可选参数；零值取默认。
type ReachOptions struct {
	// HasToken 表示探测服务带了令牌（决定每小时额度）。
	HasToken bool
	// ManualLimit / ManualWindow 是每个管理员手动检测的频率。
	ManualLimit  int
	ManualWindow time.Duration
	Now          func() time.Time
}

// ReachService 负责检测目标的同步、定时与手动检测、被墙告警与恢复通知，以及给节点 / 服务器列表与总览提供结论。
type ReachService struct {
	store
	settings *SettingsService
	ops      *OpsService
	prober   reach.Prober
	hasToken bool
	now      func() time.Time
	budget   reachBudget
	locks    reachLocks
	manual   *ratelimit.FailureGuard
}

// NewReachService 创建连通检测服务。ops 用来发告警（邮件 + Telegram）；prober 为 nil 时检测不可用（只能看已有结论）。
func NewReachService(pool *pgxpool.Pool, settings *SettingsService, ops *OpsService, prober reach.Prober, opts ReachOptions) *ReachService {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.ManualLimit <= 0 {
		opts.ManualLimit = defaultReachManualLimit
	}
	if opts.ManualWindow <= 0 {
		opts.ManualWindow = defaultReachManualWindow
	}
	limit := reach.FreeHourlyLimit
	if opts.HasToken {
		limit = reach.TokenHourlyLimit
	}
	return &ReachService{
		store: newStore(pool), settings: settings, ops: ops, prober: prober, hasToken: opts.HasToken, now: opts.Now,
		budget: reachBudget{limit: limit * reachBudgetPercent / int(percentBase), now: opts.Now},
		locks:  reachLocks{busy: make(map[int64]bool, reachRoundMax)},
		manual: ratelimit.NewFailureGuard(ratelimit.FailureGuardConfig{
			MaxFailures: opts.ManualLimit, Window: opts.ManualWindow, Lockout: opts.ManualWindow, MaxKeys: reachGuardMaxKeys,
		}, opts.Now),
	}
}

// HasToken 报告探测服务是否带了令牌（系统设置页据此显示每小时额度）。
func (s *ReachService) HasToken() bool {
	return s.hasToken
}

// sharedBudget 返回按小时的探测额度，三网延迟检测与它共用（Ruling-84）。
func (s *ReachService) sharedBudget() *reachBudget {
	return &s.budget
}

// reachBudget 按小时记已用的探测次数，并发安全。
type reachBudget struct {
	mu          sync.Mutex
	limit       int
	used        int
	windowStart time.Time
	now         func() time.Time
}

// take 占用 cost 次探测；本小时剩余不够返回 false。
func (b *reachBudget) take(cost int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if now.Sub(b.windowStart) >= reachBudgetWindow {
		b.windowStart, b.used = now, 0
	}
	if b.used+cost > b.limit {
		return false
	}
	b.used += cost
	return true
}

// exhaust 把本小时的额度记为用完（探测服务自己返回限流时调用）。
func (b *reachBudget) exhaust() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.used = b.limit
}

// reachLocks 保证同一目标同时只跑一个检测。
type reachLocks struct {
	mu   sync.Mutex
	busy map[int64]bool
}

func (l *reachLocks) tryLock(id int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.busy[id] {
		return false
	}
	l.busy[id] = true
	return true
}

func (l *reachLocks) unlock(id int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.busy, id)
}

// reachKey 是一个去重后的检测目标。
type reachKey struct {
	Address   string
	Port      int
	Transport string
}

// reachTarget 是一个目标及引用它的节点、服务器。isOnline 为真表示至少有一台所在服务器在线。
type reachTarget struct {
	key         reachKey
	nodeNames   []string
	nodeCount   int
	serverIDs   []int64
	serverNames []string
	isOnline    bool
}

// reachIndex 是检测范围的快照：全部目标，以及节点属于哪个目标。
type reachIndex struct {
	targets map[reachKey]*reachTarget
	byNode  map[int64]reachKey
}

func reachTransport(protocol, network string) string {
	if reachUDPProtocols[strings.ToLower(protocol)] || reachUDPNetworks[strings.ToLower(network)] {
		return reachTransportUDP
	}
	return reachTransportTCP
}

func reachScope(transport string) string {
	if transport == reachTransportUDP {
		return ReachScopeIP
	}
	return ReachScopePort
}

func candidateKey(r sqlcgen.ListReachCandidatesRow) (reachKey, bool) {
	address, port := serverHost(r.BaseUrl), int(r.InboundPort)
	if r.EndpointAddress != "" {
		address = r.EndpointAddress
		if r.EndpointPort > 0 {
			port = int(r.EndpointPort)
		}
	}
	address = strings.ToLower(strings.TrimSpace(address))
	if address == "" || port < 1 || port > maxPort {
		return reachKey{}, false
	}
	return reachKey{Address: address, Port: port, Transport: reachTransport(r.Protocol, r.Network)}, true
}

func (s *ReachService) loadIndex(ctx context.Context) (*reachIndex, error) {
	rows, err := s.q.ListReachCandidates(ctx)
	if err != nil {
		return nil, fmt.Errorf("list reach candidates: %w", err)
	}
	idx := &reachIndex{targets: make(map[reachKey]*reachTarget, len(rows)), byNode: make(map[int64]reachKey, len(rows))}
	for _, r := range rows {
		key, ok := candidateKey(r)
		if !ok {
			continue
		}
		t := idx.targets[key]
		if t == nil {
			t = &reachTarget{key: key}
			idx.targets[key] = t
		}
		idx.byNode[r.NodeID] = key
		t.nodeCount++
		if len(t.nodeNames) < reachMaxNames {
			t.nodeNames = append(t.nodeNames, r.DisplayName)
		}
		if !containsID(t.serverIDs, r.ServerID) {
			t.serverIDs = append(t.serverIDs, r.ServerID)
			if len(t.serverNames) < reachMaxNames {
				t.serverNames = append(t.serverNames, r.ServerName)
			}
		}
		if r.ServerStatus == serverStatusOnline {
			t.isOnline = true
		}
	}
	return idx, nil
}

func containsID(ids []int64, id int64) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

func rowKey(r sqlcgen.ListReachTargetsRow) reachKey {
	return reachKey{Address: r.Address, Port: int(r.Port), Transport: r.Transport}
}

// syncTargets 让 node_reach 与检测范围一致：补上新目标，删掉不再对应任何节点的记录；返回同步后的全部记录（最久没测的在前）。
func (s *ReachService) syncTargets(ctx context.Context, idx *reachIndex) ([]sqlcgen.ListReachTargetsRow, error) {
	rows, err := s.q.ListReachTargets(ctx)
	if err != nil {
		return nil, fmt.Errorf("list reach targets: %w", err)
	}
	known := make(map[reachKey]bool, len(rows))
	stale := make([]int64, 0, len(rows))
	for _, r := range rows {
		key := rowKey(r)
		known[key] = true
		if idx.targets[key] == nil {
			stale = append(stale, r.ID)
		}
	}
	added := 0
	for key := range idx.targets {
		if known[key] {
			continue
		}
		if err := s.q.InsertReachTarget(ctx, sqlcgen.InsertReachTargetParams{Address: key.Address, Port: int32(key.Port), Transport: key.Transport}); err != nil {
			return nil, fmt.Errorf("insert reach target: %w", err)
		}
		added++
	}
	if len(stale) > 0 {
		if err := s.q.DeleteReachTargets(ctx, stale); err != nil {
			return nil, fmt.Errorf("delete stale reach targets: %w", err)
		}
	}
	if added == 0 && len(stale) == 0 {
		return rows, nil
	}
	rows, err = s.q.ListReachTargets(ctx)
	if err != nil {
		return nil, fmt.Errorf("list reach targets: %w", err)
	}
	return rows, nil
}

// ReachProbe 是一个探测点的结果（落库与对外展示用）。RTTMs 为 nil 表示不通。
type ReachProbe struct {
	IsMainland bool     `json:"isMainland"`
	Country    string   `json:"country"`
	City       string   `json:"city"`
	Network    string   `json:"network"`
	IsOK       bool     `json:"isOk"`
	RTTMs      *float64 `json:"rttMs"`
}

// reachOutcome 是一次检测得出的结论。
type reachOutcome struct {
	verdict                              string
	cnOK, cnTotal, controlOK, controlAll int
	ipReachable                          *bool
	probes                               []ReachProbe
	lastError                            string
}

// judgeReach 按 docs/03 §5.20 的规则把各探测点的结果归成结论。
func judgeReach(results []reach.ProbeResult, transport string) reachOutcome {
	out := reachOutcome{probes: make([]ReachProbe, 0, len(results))}
	for _, r := range results {
		if !r.IsCounted {
			continue
		}
		out.probes = append(out.probes, ReachProbe{IsMainland: r.IsMainland, Country: r.Country, City: r.City, Network: r.Network, IsOK: r.IsOK, RTTMs: r.RTTMs})
		switch {
		case r.IsMainland:
			out.cnTotal++
			if r.IsOK {
				out.cnOK++
			}
		default:
			out.controlAll++
			if r.IsOK {
				out.controlOK++
			}
		}
	}
	isJudgeable := out.cnTotal >= reachMinMainland && out.controlAll > 0
	switch {
	case out.cnTotal == 0:
		out.verdict, out.lastError = ReachVerdictUnknown, msgReachNoProbes
	case out.cnOK == out.cnTotal:
		out.verdict = ReachVerdictOK
	case out.cnOK > 0:
		out.verdict = ReachVerdictPartial
	case isJudgeable && out.controlOK > 0:
		out.verdict = ReachVerdictBlocked
	case isJudgeable && transport == reachTransportTCP:
		out.verdict = ReachVerdictPortDown
	default:
		// 探测点不够，或 udp 目标境内外都不通（多半是禁 ping）
		out.verdict = ReachVerdictUnknown
	}
	return out
}

// probe 对一个目标做一次检测。额度用完返回 errReachBudget，探测服务限流返回 reach.ErrRateLimited，
// 连不上或被拒绝返回对应错误（outcome 仍给出 unknown 与原因，供后台任务落库）。
func (s *ReachService) probe(ctx context.Context, key reachKey) (reachOutcome, error) {
	if !reach.IsPublicTarget(key.Address) {
		return reachOutcome{verdict: ReachVerdictUnknown, lastError: msgReachNotPublic, probes: []ReachProbe{}}, nil
	}
	req := reach.Request{Address: key.Address, Port: key.Port, Mode: reach.ModeTCP}
	if key.Transport == reachTransportUDP {
		req.Mode = reach.ModeICMP
	}
	if !s.budget.take(s.prober.Cost(req)) {
		return reachOutcome{}, errReachBudget
	}
	results, err := s.prober.Probe(ctx, req)
	if err != nil {
		failed := reachOutcome{verdict: ReachVerdictUnknown, probes: []ReachProbe{}}
		switch {
		case errors.Is(err, reach.ErrRateLimited):
			s.budget.exhaust()
			return reachOutcome{}, err
		case errors.Is(err, reach.ErrNotPublic):
			failed.lastError = msgReachNotPublic
			return failed, nil
		case errors.Is(err, reach.ErrRejected):
			failed.lastError = msgReachRejected
		default:
			failed.lastError = msgReachUpstream
		}
		return failed, err
	}
	out := judgeReach(results, key.Transport)
	if out.verdict == ReachVerdictBlocked && key.Transport == reachTransportTCP {
		out.ipReachable = s.probeMainlandICMP(ctx, key)
	}
	return out, nil
}

// probeMainlandICMP 是 tcp 目标判为 blocked 后的补测：大陆 ICMP 还通说明只封了端口。测不出返回 nil。
func (s *ReachService) probeMainlandICMP(ctx context.Context, key reachKey) *bool {
	req := reach.Request{Address: key.Address, Mode: reach.ModeICMP, MainlandOnly: true}
	if !s.budget.take(s.prober.Cost(req)) {
		return nil
	}
	results, err := s.prober.Probe(ctx, req)
	if err != nil {
		if errors.Is(err, reach.ErrRateLimited) {
			s.budget.exhaust()
		}
		slog.Warn("reach icmp follow-up failed", "err", err)
		return nil
	}
	counted, isReachable := 0, false
	for _, r := range results {
		if r.IsCounted && r.IsMainland {
			counted++
			isReachable = isReachable || r.IsOK
		}
	}
	if counted == 0 {
		return nil
	}
	return &isReachable
}

// check 检测一个目标并落库，返回落库后的记录。同一目标正在检测返回 errReachBusy；
// 额度或限流错误原样返回且不落库；探测服务连不上时，isManual 为真直接返回错误，否则把 unknown 与原因落库。
func (s *ReachService) check(ctx context.Context, row sqlcgen.ListReachTargetsRow, isManual bool) (sqlcgen.ListReachTargetsRow, error) {
	if !s.locks.tryLock(row.ID) {
		return row, errReachBusy
	}
	defer s.locks.unlock(row.ID)
	out, err := s.probe(ctx, rowKey(row))
	if err != nil {
		if errors.Is(err, errReachBudget) || errors.Is(err, reach.ErrRateLimited) || isManual {
			return row, err
		}
		slog.Warn("reach probe failed", "targetId", row.ID, "err", err)
	}
	now := s.now()
	next := row
	next.Verdict, next.LastError = out.verdict, truncate(out.lastError, maxReachErrorLen)
	next.CnOk, next.CnTotal, next.ControlOk, next.ControlTotal = int32(out.cnOK), int32(out.cnTotal), int32(out.controlOK), int32(out.controlAll)
	next.IpReachable = pgtype.Bool{}
	if out.ipReachable != nil {
		next.IpReachable = pgtype.Bool{Bool: *out.ipReachable, Valid: true}
	}
	next.CheckedAt = timestamptz(now)
	switch out.verdict {
	case ReachVerdictBlocked:
		next.BlockedStreak = row.BlockedStreak + 1
		if !row.BlockedSince.Valid {
			next.BlockedSince = timestamptz(now)
		}
	case ReachVerdictUnknown:
		// 没测出结论：连续轮数与起点保持原样
	default:
		next.BlockedStreak, next.BlockedSince = 0, pgtype.Timestamptz{}
	}
	probes, err := json.Marshal(out.probes)
	if err != nil {
		return row, fmt.Errorf("marshal reach probes: %w", err)
	}
	next.Probes = probes
	err = s.q.SaveReachResult(ctx, sqlcgen.SaveReachResultParams{
		ID: next.ID, Verdict: next.Verdict, CnOk: next.CnOk, CnTotal: next.CnTotal, ControlOk: next.ControlOk, ControlTotal: next.ControlTotal,
		IpReachable: next.IpReachable, Probes: next.Probes, LastError: next.LastError, BlockedStreak: next.BlockedStreak,
		BlockedSince: next.BlockedSince, CheckedAt: next.CheckedAt,
	})
	if err != nil {
		return row, fmt.Errorf("save reach result of target %d: %w", row.ID, err)
	}
	return next, nil
}

// RunRound 跑一轮后台检测（docs/03 §5.20 频率与额度）：同步目标、测到期的目标（最久没测的在前，最多 5 个）、
// 再发被墙告警与恢复通知。功能没开或探测不可用时什么都不做；返回这一轮测了几个目标。
func (s *ReachService) RunRound(ctx context.Context) (int, error) {
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return 0, err
	}
	if !cur.Reach.Enabled || s.prober == nil {
		return 0, nil
	}
	idx, err := s.loadIndex(ctx)
	if err != nil {
		return 0, err
	}
	rows, err := s.syncTargets(ctx, idx)
	if err != nil {
		return 0, err
	}
	dueBefore := s.now().Add(-time.Duration(cur.Reach.IntervalMinutes) * time.Minute)
	checked := 0
	for i := range rows {
		if checked >= reachRoundMax {
			break
		}
		t := idx.targets[rowKey(rows[i])]
		isDue := !rows[i].CheckedAt.Valid || !rows[i].CheckedAt.Time.After(dueBefore) // rule-ignore: time.Time.After 比较，不是 select 里的 time.After
		if t == nil || !t.isOnline || !isDue {
			continue
		}
		next, err := s.check(ctx, rows[i], false)
		if errors.Is(err, errReachBusy) {
			continue
		}
		if errors.Is(err, errReachBudget) || errors.Is(err, reach.ErrRateLimited) {
			slog.Info("reach round stopped: hourly budget used up", "checked", checked)
			break
		}
		if err != nil {
			return checked, err
		}
		rows[i] = next
		checked++
	}
	return checked, s.notify(ctx, cur, idx, rows)
}

// notify 发被墙告警与恢复通知：连续 2 轮 blocked 且没告警过的告警一次；告警过的目标回到 ok 时发恢复通知。
func (s *ReachService) notify(ctx context.Context, cur Settings, idx *reachIndex, rows []sqlcgen.ListReachTargetsRow) error {
	if !cur.Reach.Notify {
		return nil
	}
	now := s.now()
	for _, r := range rows {
		t := idx.targets[rowKey(r)]
		if t == nil {
			continue
		}
		isNewlyBlocked := r.Verdict == ReachVerdictBlocked && r.BlockedStreak >= reachAlertStreak && !r.AlertSentAt.Valid
		isRecovered := r.Verdict == ReachVerdictOK && r.AlertSentAt.Valid
		if !isNewlyBlocked && !isRecovered {
			continue
		}
		target := fmt.Sprintf("%s:%d", r.Address, r.Port)
		detail := map[string]any{"address": r.Address, "port": r.Port, "transport": r.Transport}
		err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
			if isRecovered {
				if err := q.ClearReachAlert(ctx, r.ID); err != nil {
					return fmt.Errorf("clear reach alert of target %d: %w", r.ID, err)
				}
				body := fmt.Sprintf("服务器：%s\n节点：%s\n大陆探测点：%d / %d 连通", joinNames(t.serverNames), joinNames(t.nodeNames), r.CnOk, r.CnTotal)
				s.ops.broadcast(ctx, q, cur, TelegramEventAlert, "节点 "+target+" 大陆已恢复连通", body)
				return writeAudit(ctx, q, AuditEntry{ActorType: ActorSystem, Action: ActionReachRecovered, TargetType: targetReach, TargetID: fmt.Sprint(r.ID), Detail: detail})
			}
			if err := q.MarkReachAlerted(ctx, sqlcgen.MarkReachAlertedParams{ID: r.ID, AlertSentAt: timestamptz(now)}); err != nil {
				return fmt.Errorf("mark reach target %d alerted: %w", r.ID, err)
			}
			body := fmt.Sprintf("服务器：%s\n节点：%s\n大陆探测点：%d / %d 连通\n境外对照点：%d / %d 连通\n%s",
				joinNames(t.serverNames), joinNames(t.nodeNames), r.CnOk, r.CnTotal, r.ControlOk, r.ControlTotal, blockKindLine(r))
			s.ops.broadcast(ctx, q, cur, TelegramEventAlert, "节点 "+target+" 大陆连不上（疑似被墙）", body)
			return writeAudit(ctx, q, AuditEntry{ActorType: ActorSystem, Action: ActionReachBlocked, TargetType: targetReach, TargetID: fmt.Sprint(r.ID), Detail: detail})
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func joinNames(names []string) string {
	return orDash(strings.Join(names, "、"))
}

// blockKindLine 说明是封 IP 还是只封端口（只有 tcp 目标补测过才知道）。
func blockKindLine(r sqlcgen.ListReachTargetsRow) string {
	switch {
	case r.Transport == reachTransportUDP:
		return "范围：只测了 ICMP，IP 层不通"
	case !r.IpReachable.Valid:
		return "范围：端口不通，IP 是否还通没测出来"
	case r.IpReachable.Bool:
		return "范围：IP 还能 ping 通，只有这个端口不通"
	default:
		return "范围：IP 也 ping 不通"
	}
}

// ReachBrief 是节点列表里一行的连通结论。
type ReachBrief struct {
	Verdict   string
	Scope     string
	CnOK      int
	CnTotal   int
	CheckedAt *time.Time
}

// ReachCount 是一台服务器上检测目标的数量：总数，以及其中正常、被墙、部分不通的。
type ReachCount struct {
	Total, OK, Blocked, Partial int
}

// ReachDetail 是一个目标的检测详情。IPReachable 只有 tcp 目标判为 blocked 时才有值。
type ReachDetail struct {
	Address      string
	Port         int
	Transport    string
	Verdict      string
	Scope        string
	CnOK         int
	CnTotal      int
	ControlOK    int
	ControlTotal int
	IPReachable  *bool
	CheckedAt    *time.Time
	BlockedSince *time.Time
	LastError    string
	Probes       []ReachProbe
}

// ReachProblem 是总览里一个结论不是 ok 的目标。
type ReachProblem struct {
	ReachDetail
	Servers   []string
	Nodes     []string
	NodeCount int
}

// ReachSummary 是总览服务器分区的大陆连通汇总：各结论的目标数与有问题的目标。
type ReachSummary struct {
	Pending, OK, Partial, Blocked, PortDown, Unknown int
	Problems                                         []ReachProblem
}

func toReachDetail(r sqlcgen.ListReachTargetsRow) ReachDetail {
	d := ReachDetail{
		Address: r.Address, Port: int(r.Port), Transport: r.Transport, Verdict: r.Verdict, Scope: reachScope(r.Transport),
		CnOK: int(r.CnOk), CnTotal: int(r.CnTotal), ControlOK: int(r.ControlOk), ControlTotal: int(r.ControlTotal),
		CheckedAt: timePtr(r.CheckedAt), BlockedSince: timePtr(r.BlockedSince), LastError: r.LastError, Probes: []ReachProbe{},
	}
	if r.IpReachable.Valid {
		v := r.IpReachable.Bool
		d.IPReachable = &v
	}
	if len(r.Probes) > 0 {
		if err := json.Unmarshal(r.Probes, &d.Probes); err != nil {
			// 探测点明细只用于展示，解不开当作没有
			slog.Warn("decode reach probes", "targetId", r.ID, "err", err)
			d.Probes = []ReachProbe{}
		}
	}
	return d
}

// snapshot 返回检测范围与现有记录；功能没开时两者都为 nil。
func (s *ReachService) snapshot(ctx context.Context) (*reachIndex, map[reachKey]sqlcgen.ListReachTargetsRow, error) {
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return nil, nil, err
	}
	if !cur.Reach.Enabled {
		return nil, nil, nil
	}
	idx, err := s.loadIndex(ctx)
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.q.ListReachTargets(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list reach targets: %w", err)
	}
	byKey := make(map[reachKey]sqlcgen.ListReachTargetsRow, len(rows))
	for _, r := range rows {
		byKey[rowKey(r)] = r
	}
	return idx, byKey, nil
}

// NodeBriefs 返回各节点的连通结论，键为节点 id；功能没开返回 nil，不在检测范围的节点不在结果里，还没测过的结论为 pending。
func (s *ReachService) NodeBriefs(ctx context.Context) (map[int64]ReachBrief, error) {
	idx, rows, err := s.snapshot(ctx)
	if err != nil || idx == nil {
		return nil, err
	}
	out := make(map[int64]ReachBrief, len(idx.byNode))
	for nodeID, key := range idx.byNode {
		brief := ReachBrief{Verdict: ReachVerdictPending, Scope: reachScope(key.Transport)}
		if r, ok := rows[key]; ok {
			brief.Verdict, brief.CnOK, brief.CnTotal, brief.CheckedAt = r.Verdict, int(r.CnOk), int(r.CnTotal), timePtr(r.CheckedAt)
		}
		out[nodeID] = brief
	}
	return out, nil
}

// ServerCounts 返回各服务器上检测目标的数量，键为服务器 id；功能没开返回 nil。
func (s *ReachService) ServerCounts(ctx context.Context) (map[int64]ReachCount, error) {
	idx, rows, err := s.snapshot(ctx)
	if err != nil || idx == nil {
		return nil, err
	}
	out := make(map[int64]ReachCount, len(idx.targets))
	for key, t := range idx.targets {
		verdict := rows[key].Verdict
		for _, serverID := range t.serverIDs {
			c := out[serverID]
			c.Total++
			switch verdict {
			case ReachVerdictOK:
				c.OK++
			case ReachVerdictBlocked:
				c.Blocked++
			case ReachVerdictPartial:
				c.Partial++
			default:
			}
			out[serverID] = c
		}
	}
	return out, nil
}

// reachSeverity 决定问题列表的顺序：被墙的在前。
func reachSeverity(verdict string) int {
	switch verdict {
	case ReachVerdictBlocked:
		return 0
	case ReachVerdictPartial:
		return 1
	default:
		return 2
	}
}

// Summary 返回总览用的汇总；功能没开返回 nil。
func (s *ReachService) Summary(ctx context.Context) (*ReachSummary, error) {
	idx, rows, err := s.snapshot(ctx)
	if err != nil || idx == nil {
		return nil, err
	}
	out := &ReachSummary{Problems: make([]ReachProblem, 0, reachProblemsMax)}
	for key, t := range idx.targets {
		r, ok := rows[key]
		verdict := ReachVerdictPending
		if ok {
			verdict = r.Verdict
		}
		switch verdict {
		case ReachVerdictOK:
			out.OK++
		case ReachVerdictPartial:
			out.Partial++
		case ReachVerdictBlocked:
			out.Blocked++
		case ReachVerdictPortDown:
			out.PortDown++
		case ReachVerdictUnknown:
			out.Unknown++
		default:
			out.Pending++
		}
		isProblem := verdict == ReachVerdictBlocked || verdict == ReachVerdictPartial || verdict == ReachVerdictPortDown
		if isProblem {
			out.Problems = append(out.Problems, ReachProblem{ReachDetail: toReachDetail(r), Servers: t.serverNames, Nodes: t.nodeNames, NodeCount: t.nodeCount})
		}
	}
	sort.SliceStable(out.Problems, func(i, j int) bool {
		a, b := out.Problems[i], out.Problems[j]
		if sa, sb := reachSeverity(a.Verdict), reachSeverity(b.Verdict); sa != sb {
			return sa < sb
		}
		if a.Address != b.Address {
			return a.Address < b.Address
		}
		return a.Port < b.Port
	})
	if len(out.Problems) > reachProblemsMax {
		out.Problems = out.Problems[:reachProblemsMax]
	}
	return out, nil
}

// nodeTarget 找出节点所属的目标：功能没开 403，节点不存在 404，不在检测范围 409。
func (s *ReachService) nodeTarget(ctx context.Context, nodeID int64) (reachKey, error) {
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return reachKey{}, err
	}
	if !cur.Reach.Enabled {
		return reachKey{}, newError(CodeFeatureDisabled, msgReachDisabled)
	}
	exists, err := s.q.NodeExists(ctx, nodeID)
	if err != nil {
		return reachKey{}, fmt.Errorf("check node %d: %w", nodeID, err)
	}
	if !exists {
		return reachKey{}, newError(CodeNotFound, msgNodeNotFound)
	}
	idx, err := s.loadIndex(ctx)
	if err != nil {
		return reachKey{}, err
	}
	key, ok := idx.byNode[nodeID]
	if !ok {
		return reachKey{}, newError(CodeConflict, msgReachOutOfScope)
	}
	return key, nil
}

func (s *ReachService) targetRow(ctx context.Context, key reachKey) (sqlcgen.ListReachTargetsRow, bool, error) {
	r, err := s.q.GetReachTarget(ctx, sqlcgen.GetReachTargetParams{Address: key.Address, Port: int32(key.Port), Transport: key.Transport})
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlcgen.ListReachTargetsRow{}, false, nil
	}
	if err != nil {
		return sqlcgen.ListReachTargetsRow{}, false, fmt.Errorf("get reach target: %w", err)
	}
	return sqlcgen.ListReachTargetsRow(r), true, nil
}

// NodeDetail 返回节点所属目标的检测详情；还没测过时结论为 pending。错误见 nodeTarget。
func (s *ReachService) NodeDetail(ctx context.Context, nodeID int64) (*ReachDetail, error) {
	key, err := s.nodeTarget(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	row, ok, err := s.targetRow(ctx, key)
	if err != nil {
		return nil, err
	}
	if !ok {
		return &ReachDetail{Address: key.Address, Port: key.Port, Transport: key.Transport, Verdict: ReachVerdictPending,
			Scope: reachScope(key.Transport), Probes: []ReachProbe{}}, nil
	}
	d := toReachDetail(row)
	return &d, nil
}

// CheckNode 立即检测一次节点所属的目标并返回详情（docs/03 §5.20 手动检测）。
// 功能没开 403，节点不存在 404，不在检测范围或同一目标正在检测 409，管理员超过频率或本小时额度用完 429，探测服务连不上 502。
func (s *ReachService) CheckNode(ctx context.Context, actor *AdminPrincipal, nodeID int64) (*ReachDetail, error) {
	key, err := s.nodeTarget(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if s.prober == nil {
		return nil, newError(CodeUpstream, msgReachUpstream)
	}
	guardKey := strconv.FormatInt(actor.AdminID, 10)
	if s.manual.IsLocked(guardKey) {
		return nil, newError(CodeRateLimited, msgReachRateLimited)
	}
	s.manual.RecordFailure(guardKey)
	if err := s.q.InsertReachTarget(ctx, sqlcgen.InsertReachTargetParams{Address: key.Address, Port: int32(key.Port), Transport: key.Transport}); err != nil {
		return nil, fmt.Errorf("insert reach target: %w", err)
	}
	row, ok, err := s.targetRow(ctx, key)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, newError(CodeConflict, msgReachOutOfScope)
	}
	next, err := s.check(ctx, row, true)
	switch {
	case err == nil:
	case errors.Is(err, errReachBusy):
		return nil, newError(CodeConflict, msgReachBusy)
	case errors.Is(err, errReachBudget), errors.Is(err, reach.ErrRateLimited):
		return nil, newError(CodeRateLimited, msgReachNoBudget)
	case errors.Is(err, reach.ErrRejected):
		return nil, newError(CodeUpstream, msgReachRejected)
	case errors.Is(err, reach.ErrUnreachable):
		slog.Warn("manual reach check failed", "nodeId", nodeID, "err", err)
		return nil, newError(CodeUpstream, msgReachUpstream)
	default:
		return nil, err
	}
	d := toReachDetail(next)
	return &d, nil
}
