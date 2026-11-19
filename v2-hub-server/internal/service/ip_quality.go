package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/netip"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/ipprobe"
	"v2-hub-server/internal/manager/ipquality"
	"v2-hub-server/internal/manager/panel"
	"v2-hub-server/internal/manager/ratelimit"
	"v2-hub-server/internal/manager/remote"
)

// IP 质量检测的状态、来源、范围与服务器侧情况（docs/03 §5.23，Ruling-81）。
const (
	IPCheckQueued    = "queued"
	IPCheckRunning   = "running"
	IPCheckSucceeded = "succeeded"
	IPCheckFailed    = "failed"

	IPCheckManual = "manual"
	IPCheckAuto   = "auto"

	IPCheckScopeFull     = "full"
	IPCheckScopePlatform = "platform"

	HostProbeDone           = "done"
	HostProbeNoHost         = "no_host"
	HostProbeNoPermission   = "no_permission"
	HostProbeHostUnverified = "host_unverified"
	HostProbeSSHFailed      = "ssh_failed"
	HostProbeNoCurl         = "no_curl"

	IPSourceHost    = "host"
	IPSourcePanel   = "panel"
	IPSourceAddress = "address"

	IPCheckErrNoPublicIP   = "no_public_ip"
	IPCheckErrInterrupted  = "interrupted"
	IPCheckErrTimeout      = "timeout"
	IPCheckErrInternal     = "internal"
	ipCheckResultSchema    = 1
	ipCheckStackV4         = "v4"
	ActionIPCheckCreate    = "ip_check.create"
	targetIPCheck          = "server_ip_check"
	ipCheckGuardMaxKeys    = 1024
	ipCheckWriteTimeout    = 10 * time.Second
	ipCheckNonceAlphabet   = "abcdefghijklmnopqrstuvwxyz0123456789"
	ipCheckNonceLen        = 12
	hoursPerDay            = 24
	defaultIPCheckTimeout  = 3 * time.Minute
	defaultIPCheckLimit    = 10
	defaultIPCheckWindow   = 10 * time.Minute
	defaultIPCheckAutoMax  = 5
	defaultIPCheckKeepDays = 180
	defaultIPCheckKeepRows = 60

	msgIPCheckBusy        = "这台服务器有进行中的 IP 质量检测"
	msgIPCheckNotFound    = "检测记录不存在"
	msgIPCheckRateLimited = "检测太频繁，请稍后再试"
	msgIPCheckNoPublicIP  = "取不到服务器的公网 IPv4：服务器侧没取到出口 IP，面板没报公网 IP，面板地址也不是公网 IP"
	msgIPCheckInterrupted = "hub-server 重启，检测中断"
	msgIPCheckTimeout     = "检测超时"
	msgIPCheckInternal    = "内部错误，详见服务端日志"
)

// IPQualityConfig 是 IP 质量检测的参数；零值取默认。
type IPQualityConfig struct {
	// CheckTimeout 是单次检测的总时限。
	CheckTimeout time.Duration
	// ManualLimit / ManualWindow 是每个管理员手动发起的频率。
	ManualLimit  int
	ManualWindow time.Duration
	// AutoBatch 是定期复检每轮最多排几台。
	AutoBatch int
	// RetentionDays / KeepPerServer 是记录保留的天数与每台服务器保留的条数。
	RetentionDays int
	KeepPerServer int
	Now           func() time.Time
}

// IPQualityDeps 是 IPQualityService 依赖的能力。
type IPQualityDeps struct {
	Panels   *PanelService
	Hosts    *HostService
	Settings *SettingsService
	Sources  *ipquality.Registry
	DNSBL    *ipquality.DNSBL
	Prober   *ipprobe.Prober
}

// IPQualityService 负责服务器 IP 质量检测的记录与执行（docs/03 §5.23，Ruling-81、Ruling-82）。
type IPQualityService struct {
	store
	deps   IPQualityDeps
	cfg    IPQualityConfig
	manual *ratelimit.FailureGuard
	kick   chan struct{}
	now    func() time.Time
}

// NewIPQualityService 创建 IP 质量检测服务。
func NewIPQualityService(pool *pgxpool.Pool, deps IPQualityDeps, cfg IPQualityConfig) *IPQualityService {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.CheckTimeout = orDuration(cfg.CheckTimeout, defaultIPCheckTimeout)
	cfg.ManualWindow = orDuration(cfg.ManualWindow, defaultIPCheckWindow)
	cfg.ManualLimit = orInt(cfg.ManualLimit, defaultIPCheckLimit)
	cfg.AutoBatch = orInt(cfg.AutoBatch, defaultIPCheckAutoMax)
	cfg.RetentionDays = orInt(cfg.RetentionDays, defaultIPCheckKeepDays)
	cfg.KeepPerServer = orInt(cfg.KeepPerServer, defaultIPCheckKeepRows)
	return &IPQualityService{
		store: newStore(pool), deps: deps, cfg: cfg, now: cfg.Now, kick: make(chan struct{}, 1),
		manual: ratelimit.NewFailureGuard(ratelimit.FailureGuardConfig{
			MaxFailures: cfg.ManualLimit, Window: cfg.ManualWindow, Lockout: cfg.ManualWindow, MaxKeys: ipCheckGuardMaxKeys,
		}, cfg.Now),
	}
}

func orDuration(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return v
}

func orInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// IPCheck 是一次检测的记录与摘要。
type IPCheck struct {
	ID            int64
	ServerID      int64
	Status        string
	Origin        string
	Scope         string
	HostProbe     string
	IP            string
	IPSource      string
	Country       string
	RegCountry    string
	IPKind        string
	ASN           *int64
	ASOrg         string
	UsageType     string
	RiskLevel     string
	DNSBLListed   int
	DNSBLTotal    int
	MediaOK       int
	MediaTotal    int
	Port25        string
	ErrorCode     string
	LastError     string
	CreatedByName string
	CreatedAt     time.Time
	StartedAt     *time.Time
	FinishedAt    *time.Time
}

// IPCheckDetail 是记录加完整报告；Result 是报告 JSON（还没执行完为 nil）。
type IPCheckDetail struct {
	IPCheck
	Result json.RawMessage
}

// Sources 是数据源目录（设置页显示各源是否已配置 key）。
func (s *IPQualityService) Sources() []ipquality.SourceInfo {
	if s.deps.Sources == nil {
		return nil
	}
	return s.deps.Sources.Catalog()
}

// StartCheck 手动发起一次检测（server:write）：有 host:write 时范围为 full，没有时为 platform（服务器侧标 no_permission）。
// 已有进行中的检测返回 409，超出管理员频控返回 429。写审计 ip_check.create。
func (s *IPQualityService) StartCheck(ctx context.Context, actor *AdminPrincipal, serverID int64, ip *netip.Addr) (*IPCheck, error) {
	if _, err := s.deps.Panels.GetServer(ctx, serverID); err != nil {
		return nil, err
	}
	scope, probe := IPCheckScopeFull, ""
	if !actor.Can(PermHostWrite) {
		scope, probe = IPCheckScopePlatform, HostProbeNoPermission
	}
	key := fmt.Sprint(actor.AdminID)
	if s.manual.IsLocked(key) {
		return nil, newError(CodeRateLimited, msgIPCheckRateLimited)
	}
	var id int64
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		id, err = q.CreateIPCheck(ctx, sqlcgen.CreateIPCheckParams{
			ServerID: serverID, Origin: IPCheckManual, Scope: scope, HostProbe: probe,
			CreatedBy: pgtype.Int8{Int64: actor.AdminID, Valid: true},
		})
		if isUniqueViolation(err) {
			return newError(CodeConflict, msgIPCheckBusy)
		}
		if err != nil {
			return fmt.Errorf("create ip check: %w", err)
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionIPCheckCreate, TargetType: targetIPCheck, TargetID: fmt.Sprint(id), ClientIP: ip,
			Detail: map[string]any{"serverId": serverID, "scope": scope},
		})
	})
	if err != nil {
		return nil, err
	}
	s.manual.RecordFailure(key)
	s.kickRunner()
	return s.GetCheck(ctx, id)
}

// GetCheck 返回一条检测记录（不带报告）。
func (s *IPQualityService) GetCheck(ctx context.Context, id int64) (*IPCheck, error) {
	row, err := s.q.GetIPCheck(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, newError(CodeNotFound, msgIPCheckNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get ip check %d: %w", id, err)
	}
	c := ipCheckFromRow(sqlcgen.ListIPChecksRow(row))
	return &c, nil
}

// GetCheckDetail 返回一条检测记录与完整报告。
func (s *IPQualityService) GetCheckDetail(ctx context.Context, id int64) (*IPCheckDetail, error) {
	c, err := s.GetCheck(ctx, id)
	if err != nil {
		return nil, err
	}
	raw, err := s.q.GetIPCheckResult(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get ip check result %d: %w", id, err)
	}
	return &IPCheckDetail{IPCheck: *c, Result: raw}, nil
}

// ListChecks 返回服务器的检测历史，新的在前；服务器不存在返回 404。
func (s *IPQualityService) ListChecks(ctx context.Context, serverID int64, pq PageQuery) (*Page[IPCheck], error) {
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	if _, err := s.deps.Panels.GetServer(ctx, serverID); err != nil {
		return nil, err
	}
	total, err := s.q.CountIPChecks(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("count ip checks: %w", err)
	}
	out := &Page[IPCheck]{Items: []IPCheck{}, Total: total}
	if total == 0 {
		return out, nil
	}
	rows, err := s.q.ListIPChecks(ctx, sqlcgen.ListIPChecksParams{ServerID: serverID, Limit: limit, Offset: offset})
	if err != nil {
		return nil, fmt.Errorf("list ip checks: %w", err)
	}
	out.Items = make([]IPCheck, 0, len(rows))
	for _, r := range rows {
		out.Items = append(out.Items, ipCheckFromRow(r))
	}
	return out, nil
}

func ipCheckFromRow(r sqlcgen.ListIPChecksRow) IPCheck {
	c := IPCheck{
		ID: r.ID, ServerID: r.ServerID, Status: r.Status, Origin: r.Origin, Scope: r.Scope, HostProbe: r.HostProbe,
		IP: r.Ip, IPSource: r.IpSource, Country: r.Country, RegCountry: r.RegCountry, IPKind: r.IpKind, ASOrg: r.AsOrg,
		UsageType: r.UsageType, RiskLevel: r.RiskLevel, DNSBLListed: int(r.DnsblListed), DNSBLTotal: int(r.DnsblTotal),
		MediaOK: int(r.MediaOk), MediaTotal: int(r.MediaTotal), Port25: r.Port25, ErrorCode: r.ErrorCode, LastError: r.LastError,
		CreatedByName: r.CreatedByName, CreatedAt: r.CreatedAt.Time, StartedAt: timePtr(r.StartedAt), FinishedAt: timePtr(r.FinishedAt),
	}
	if r.Asn.Valid {
		asn := r.Asn.Int64
		c.ASN = &asn
	}
	return c
}

// ---- 后台执行（与主机任务共用 job 里的任务循环） ----

// Requests 在有新检测排队时发信号，后台任务据此立即领取。
func (s *IPQualityService) Requests() <-chan struct{} { return s.kick }

func (s *IPQualityService) kickRunner() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// ClaimTasks 领取至多 limit 条排队的检测并置为进行中。
func (s *IPQualityService) ClaimTasks(ctx context.Context, limit int) ([]int64, error) {
	if limit <= 0 {
		return nil, nil
	}
	ids, err := s.q.ClaimQueuedIPChecks(ctx, int32(limit)) //nolint:gosec // limit 是并发上限，很小
	if err != nil {
		return nil, fmt.Errorf("claim ip checks: %w", err)
	}
	return ids, nil
}

// FailInterrupted 把上次进程退出时没跑完的检测记为失败，返回条数；启动时调一次。
func (s *IPQualityService) FailInterrupted(ctx context.Context) (int64, error) {
	n, err := s.q.FailRunningIPChecks(ctx, sqlcgen.FailRunningIPChecksParams{ErrorCode: IPCheckErrInterrupted, LastError: msgIPCheckInterrupted})
	if err != nil {
		return 0, fmt.Errorf("fail interrupted ip checks: %w", err)
	}
	return n, nil
}

// errNoPublicIP 表示取不到可检测的公网 IPv4。
var errNoPublicIP = errors.New("no public ipv4")

// RunTask 执行一条已领取的检测并记录结果；不返回错误，失败都落在记录上。
func (s *IPQualityService) RunTask(ctx context.Context, id int64) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.CheckTimeout)
	defer cancel()
	fin, err := s.runCheck(ctx, id)
	if err != nil {
		fin = sqlcgen.FinishIPCheckParams{Status: IPCheckFailed, HostProbe: fin.HostProbe}
		switch {
		case errors.Is(err, errNoPublicIP):
			fin.ErrorCode, fin.LastError = IPCheckErrNoPublicIP, msgIPCheckNoPublicIP
		case errors.Is(err, context.DeadlineExceeded):
			fin.ErrorCode, fin.LastError = IPCheckErrTimeout, msgIPCheckTimeout
		default:
			fin.ErrorCode, fin.LastError = IPCheckErrInternal, msgIPCheckInternal
		}
		slog.Warn("ip check failed", "check_id", id, "code", fin.ErrorCode, "err", err)
	}
	fin.ID = id
	wctx, wcancel := context.WithTimeout(context.Background(), ipCheckWriteTimeout)
	defer wcancel()
	if _, ferr := s.q.FinishIPCheck(wctx, fin); ferr != nil {
		slog.Error("finish ip check", "check_id", id, "err", ferr)
	}
}

// runCheck 是一次检测：服务器侧（范围 full 时）→ 定被测 IP → 平台侧各源与黑名单 → 组装报告与摘要。
// 出错时返回的参数里带着已经知道的服务器侧情况。
func (s *IPQualityService) runCheck(ctx context.Context, id int64) (sqlcgen.FinishIPCheckParams, error) {
	run, err := s.q.GetIPCheckRun(ctx, id)
	if err != nil {
		return sqlcgen.FinishIPCheckParams{}, fmt.Errorf("load ip check %d: %w", id, err)
	}
	probeState := run.HostProbe
	var probed *ipprobe.Output
	if run.Scope == IPCheckScopeFull {
		probed, probeState = s.probeHost(ctx, run.ServerID)
	}
	fin := sqlcgen.FinishIPCheckParams{HostProbe: probeState}
	ip, source, err := s.targetIP(ctx, run.ServerID, probed)
	if err != nil {
		return fin, err
	}
	stack := s.platformSide(ctx, ip)
	stack.IP, stack.IPSource = ip.String(), source
	fillHostSide(&stack, probeState, probed)
	raw, err := json.Marshal(ipCheckResult{Schema: ipCheckResultSchema, Stacks: map[string]ipCheckStack{ipCheckStackV4: stack}})
	if err != nil {
		return fin, fmt.Errorf("encode ip check result: %w", err)
	}
	return summarize(fin, stack, raw), nil
}

// probeHost 执行服务器侧探测，返回结果与服务器侧情况；失败只归类、不让整次检测失败。
func (s *IPQualityService) probeHost(ctx context.Context, serverID int64) (*ipprobe.Output, string) {
	if s.deps.Hosts == nil || s.deps.Prober == nil {
		return nil, HostProbeNoHost
	}
	nonce, err := randomNonce()
	if err != nil {
		slog.Error("ip check nonce", "err", err)
		return nil, HostProbeSSHFailed
	}
	var out *ipprobe.Output
	err = s.deps.Hosts.withServerHost(ctx, serverID, func(ctx context.Context, r remote.Runner) error {
		var err error
		out, err = s.deps.Prober.Run(ctx, r, nonce)
		return err
	})
	switch {
	case err == nil:
		return out, HostProbeDone
	case errors.Is(err, errProbeNoHost):
		return nil, HostProbeNoHost
	case errors.Is(err, errProbeHostUnverified):
		return nil, HostProbeHostUnverified
	case errors.Is(err, ipprobe.ErrNoCurl):
		return nil, HostProbeNoCurl
	default:
		slog.Warn("ip check host probe failed", "server_id", serverID, "err", err)
		return nil, HostProbeSSHFailed
	}
}

// targetIP 依次取：服务器侧的出口 IP → 面板报告的公网 IP → 面板地址里的 IP 字面量；必须是公网 IPv4。
func (s *IPQualityService) targetIP(ctx context.Context, serverID int64, probed *ipprobe.Output) (netip.Addr, string, error) {
	if probed != nil && usableIPv4(probed.Egress.IP) {
		return probed.Egress.IP, IPSourceHost, nil
	}
	if ip, ok := s.panelPublicIP(ctx, serverID); ok {
		return ip, IPSourcePanel, nil
	}
	srv, err := s.deps.Panels.GetServer(ctx, serverID)
	if err != nil {
		return netip.Addr{}, "", err
	}
	if u, err := panel.ParseBaseURL(srv.BaseURL); err == nil {
		if ip, err := netip.ParseAddr(u.Hostname()); err == nil && usableIPv4(ip.Unmap()) {
			return ip.Unmap(), IPSourceAddress, nil
		}
	}
	return netip.Addr{}, "", errNoPublicIP
}

func (s *IPQualityService) panelPublicIP(ctx context.Context, serverID int64) (netip.Addr, bool) {
	p, _, err := s.deps.Panels.openProvider(ctx, serverID)
	if err != nil {
		slog.Warn("ip check open panel", "server_id", serverID, "err", err)
		return netip.Addr{}, false
	}
	info, err := p.Probe(ctx)
	if err != nil {
		slog.Warn("ip check probe panel", "server_id", serverID, "err", err)
		return netip.Addr{}, false
	}
	ip, err := netip.ParseAddr(info.PublicIP)
	if err != nil || !usableIPv4(ip.Unmap()) {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

// usableIPv4 第一期只测公网 IPv4（规则同服务端链式的地址限制，§5.19）。
func usableIPv4(ip netip.Addr) bool { return ip.Is4() && isPublicAddr(ip) }

// platformSide 并发查各数据源与 DNS 黑名单。
func (s *IPQualityService) platformSide(ctx context.Context, ip netip.Addr) ipCheckStack {
	var (
		results []ipquality.SourceResult
		bl      ipquality.DNSBLReport
		wg      sync.WaitGroup
	)
	if s.deps.Sources != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			if results, err = s.deps.Sources.Lookup(ctx, ip); err != nil {
				slog.Warn("ip check sources", "ip", ip, "err", err)
			}
		}()
	}
	if s.deps.DNSBL != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bl = s.deps.DNSBL.Check(ctx, ip)
		}()
	}
	wg.Wait()
	return ipCheckStack{
		Basics:  toBasicsJSON(ipquality.BuildBasics(results)),
		Sources: toSourcesJSON(results),
		DNSBL:   toDNSBLJSON(bl),
		Media:   ipMediaJSON{Items: []ipMediaItemJSON{}},
		Mail:    ipMailJSON{Items: []ipMailItemJSON{}},
		summary: ipStackSummary{usage: string(ipquality.SummaryUsage(results)), risk: string(ipquality.MaxLevel(results))},
	}
}

// fillHostSide 写入服务器侧的出口、流媒体与邮局；没做时两段的状态是原因。
func fillHostSide(stack *ipCheckStack, state string, probed *ipprobe.Output) {
	stack.Media.Status, stack.Mail.Status = state, state
	if probed == nil {
		return
	}
	stack.Egress = ipEgressJSON{Country: probed.Egress.Country}
	if probed.Egress.IP.IsValid() {
		stack.Egress.IP = probed.Egress.IP.String()
	}
	for _, m := range probed.Media {
		stack.Media.Items = append(stack.Media.Items, ipMediaItemJSON{Key: m.Key, Status: m.Status, Region: m.Region, Method: m.Method, Evidence: m.Evidence})
	}
	for _, m := range probed.Mail {
		stack.Mail.Items = append(stack.Mail.Items, ipMailItemJSON{Key: m.Key, MX: m.MX, Result: m.Result, Code: m.Code})
	}
	stack.Mail.Port25 = ipprobe.Port25(probed.Mail)
}

// summarize 由报告得出摘要列。
func summarize(fin sqlcgen.FinishIPCheckParams, st ipCheckStack, raw []byte) sqlcgen.FinishIPCheckParams {
	fin.Status, fin.Result = IPCheckSucceeded, raw
	fin.Ip, fin.IpSource = st.IP, st.IPSource
	fin.Country, fin.RegCountry, fin.IpKind, fin.AsOrg = st.Basics.Country, st.Basics.RegCountry, st.Basics.IPKind, truncate(st.Basics.ASOrg, maxASOrgLen)
	if st.Basics.ASN != nil {
		fin.Asn = pgtype.Int8{Int64: *st.Basics.ASN, Valid: true}
	}
	fin.UsageType, fin.RiskLevel = st.summary.usage, st.summary.risk
	fin.DnsblListed, fin.DnsblTotal = int32(st.DNSBL.Listed), int32(st.DNSBL.Listed+st.DNSBL.Policy+st.DNSBL.Clean) //nolint:gosec // 列表数很小
	for _, m := range st.Media.Items {
		fin.MediaTotal++
		if ipprobe.Usable(m.Status) {
			fin.MediaOk++
		}
	}
	fin.Port25 = st.Mail.Port25
	return fin
}

// maxASOrgLen 是摘要列 as_org 的长度上限。
const maxASOrgLen = 128

func randomNonce() (string, error) {
	b := make([]byte, ipCheckNonceLen)
	alphabetSize := big.NewInt(int64(len(ipCheckNonceAlphabet)))
	for i := range b {
		n, err := rand.Int(rand.Reader, alphabetSize)
		if err != nil {
			return "", fmt.Errorf("random nonce: %w", err)
		}
		b[i] = ipCheckNonceAlphabet[n.Int64()]
	}
	return string(b), nil
}

// ---- 定期复检与清理 ----

// QueueDueChecks 给到期的服务器排定期复检（docs/03 §5.23 定期复检），返回排了几条；开关关着时什么都不做。
func (s *IPQualityService) QueueDueChecks(ctx context.Context) (int, error) {
	cur, err := s.deps.Settings.Current(ctx)
	if err != nil {
		return 0, err
	}
	if !cur.IPQuality.AutoEnabled {
		return 0, nil
	}
	cutoff := s.now().Add(-time.Duration(cur.IPQuality.IntervalDays) * hoursPerDay * time.Hour)
	ids, err := s.q.ListServersDueForIPCheck(ctx, sqlcgen.ListServersDueForIPCheckParams{
		Cutoff: pgtype.Timestamptz{Time: cutoff, Valid: true}, MaxCount: int32(s.cfg.AutoBatch), //nolint:gosec // 每轮台数很小
	})
	if err != nil {
		return 0, fmt.Errorf("list servers due for ip check: %w", err)
	}
	queued := 0
	for _, serverID := range ids {
		_, err := s.q.CreateIPCheck(ctx, sqlcgen.CreateIPCheckParams{ServerID: serverID, Origin: IPCheckAuto, Scope: IPCheckScopeFull})
		if isUniqueViolation(err) {
			continue // 挑选之后刚好有人手动发起了
		}
		if err != nil {
			slog.Error("queue ip check", "server_id", serverID, "err", err)
			continue
		}
		queued++
	}
	if queued > 0 {
		s.kickRunner()
	}
	return queued, nil
}

// PurgeChecks 清掉超出保留天数与每台保留条数的记录，返回条数。
func (s *IPQualityService) PurgeChecks(ctx context.Context) (int64, error) {
	cutoff := s.now().Add(-time.Duration(s.cfg.RetentionDays) * hoursPerDay * time.Hour)
	n, err := s.q.PurgeIPChecks(ctx, sqlcgen.PurgeIPChecksParams{
		Cutoff: pgtype.Timestamptz{Time: cutoff, Valid: true}, KeepPerServer: int64(s.cfg.KeepPerServer),
	})
	if err != nil {
		return 0, fmt.Errorf("purge ip checks: %w", err)
	}
	return n, nil
}
