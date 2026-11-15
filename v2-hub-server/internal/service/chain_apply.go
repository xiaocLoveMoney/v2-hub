package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/chain"
	"v2-hub-server/internal/manager/crypto"
	"v2-hub-server/internal/manager/panel"
	"v2-hub-server/internal/manager/render"
)

// 服务端链式下发（docs/03 §5.19、§3 C1–C5，Ruling-40）：以起飞服务器为单位，把面板内核模板里的平台条目整体对齐到期望；
// 写入前加密备份整份模板，重启后内核未运行即写回备份并记失败。期望条目由生效计算（chain_resolve.go）的结果生成，
// 设置 routing.blockRule 开着时最前面再加平台拦截条目（Ruling-55）。

// 下发状态（chain_apply.status，与 hub-api.yaml 的 ChainApplyStatus 对应；none 表示没有记录）。
const (
	chainApplyNone    = "none"
	chainApplyPending = "pending"
	chainApplyApplied = "applied"
	chainApplyFailed  = "failed"
)

const (
	chainBackupAADFormat = "chain_apply:%d:backup"
	maxChainErrorLen     = 500

	ActionChainApply       = "chain.apply"
	ActionChainApplyFailed = "chain.apply_failed"
	ActionChainApplyRetry  = "chain.apply_retry"

	msgChainCoreNotRunning = "重启后 Xray 未能运行，已回滚"
	msgChainBadTemplate    = "面板上的内核模板不是预期的 JSON"
	msgChainInternal       = "下发时发生内部错误，详见服务端日志"
)

// ChainApplyConfig 是下发的节奏；零值字段取默认值（docs/03 §5.19 下发与回滚）。
type ChainApplyConfig struct {
	// MinInterval 是同一台服务器两次下发的最小间隔（重启会断开全部连接）。
	MinInterval time.Duration
	// ConfirmDelay 是重启后第一次查内核状态前的等待（面板的状态有几秒缓存）。
	ConfirmDelay time.Duration
	// ConfirmTimeout 是确认内核运行的总时限；StablePause 是两次“运行中”之间的间隔，用来排除启动后立刻崩溃。
	ConfirmTimeout time.Duration
	StablePause    time.Duration
	PollInterval   time.Duration
	// MaxFailures 是同一期望连续失败多少次后停止自动重试；Backoff 是第 n 次失败后的退避。
	MaxFailures int
	Backoff     []time.Duration
	// Resolver 解析用户出口的域名（定期复查固定的地址）；nil 用系统解析器。
	Resolver Resolver
	// Settings 提供 routing.blockRule（平台拦截规则，Ruling-55）；nil 视为关闭。
	Settings *SettingsService
}

// errChainCoreDown 表示写入新模板并重启后内核没能运行（配置有问题的信号，用于失败隔离）。
var errChainCoreDown = errors.New("core not running after restart")

var defaultChainBackoff = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}

const (
	defaultChainMinInterval    = 60 * time.Second
	defaultChainConfirmDelay   = 3 * time.Second
	defaultChainConfirmTimeout = 15 * time.Second
	defaultChainStablePause    = 2 * time.Second
	defaultChainPollInterval   = time.Second
	defaultChainMaxFailures    = 3
)

func (c ChainApplyConfig) withDefaults() ChainApplyConfig {
	if c.MinInterval == 0 {
		c.MinInterval = defaultChainMinInterval
	}
	if c.ConfirmTimeout == 0 {
		c.ConfirmTimeout = defaultChainConfirmTimeout
	}
	if c.PollInterval == 0 {
		c.PollInterval = defaultChainPollInterval
	}
	if c.MaxFailures == 0 {
		c.MaxFailures = defaultChainMaxFailures
	}
	if len(c.Backoff) == 0 {
		c.Backoff = defaultChainBackoff
	}
	return c
}

// DefaultChainApplyConfig 是生产用的节奏。
func DefaultChainApplyConfig() ChainApplyConfig {
	return ChainApplyConfig{ConfirmDelay: defaultChainConfirmDelay, StablePause: defaultChainStablePause}.withDefaults()
}

// ChainApply 是一台起飞服务器的下发状态（管理端展示）。
type ChainApply struct {
	ServerID         int64
	Supported        bool
	Status           string
	RuleCount        int
	AppliedRuleCount int
	FailCount        int
	RetryStopped     bool
	LastError        string
	LastAttemptAt    *time.Time
	NextAttemptAt    *time.Time
	AppliedAt        *time.Time
}

// ChainApplier 负责把服务端规则下发到起飞面板。并发安全：后台任务串行处理各服务器，接口只读状态或触发一轮。
type ChainApplier struct {
	store
	sealer *crypto.Sealer
	users  *UserService
	panels *PanelService
	guard  *chainGuard
	cfg    ChainApplyConfig
	now    func() time.Time
	kicks  chan struct{}
}

// NewChainApplier 创建下发器；users 用来取落地节点参数（与订阅同源），panels 用来连面板；now 为 nil 时取系统时间。
func NewChainApplier(pool *pgxpool.Pool, sealer *crypto.Sealer, users *UserService, panels *PanelService, cfg ChainApplyConfig, now func() time.Time) *ChainApplier {
	if now == nil {
		now = time.Now
	}
	return &ChainApplier{
		store: newStore(pool), sealer: sealer, users: users, panels: panels, guard: newChainGuard(cfg.Resolver),
		cfg: cfg.withDefaults(), now: now, kicks: make(chan struct{}, 1),
	}
}

func chainBackupAAD(serverID int64) []byte {
	return fmt.Appendf(nil, chainBackupAADFormat, serverID)
}

// Kick 请求尽快检查一轮；已有待处理的请求时合并。
func (a *ChainApplier) Kick() {
	select {
	case a.kicks <- struct{}{}:
	default:
	}
}

// Requests 返回触发信号，供后台任务监听。
func (a *ChainApplier) Requests() <-chan struct{} {
	return a.kicks
}

// isBlockRuleOn 返回是否下发平台拦截规则（设置 routing.blockRule）。
func (a *ChainApplier) isBlockRuleOn(ctx context.Context) (bool, error) {
	if a.cfg.Settings == nil {
		return false, nil
	}
	cur, err := a.cfg.Settings.Current(ctx)
	if err != nil {
		return false, fmt.Errorf("load routing settings: %w", err)
	}
	return cur.Routing.BlockRule, nil
}

// ListServerIDs 返回需要检查下发的服务器：拦截规则开着时是全部启用中的服务器。
func (a *ChainApplier) ListServerIDs(ctx context.Context) ([]int64, error) {
	isBlockOn, err := a.isBlockRuleOn(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := a.q.ListChainApplyServerIDs(ctx, isBlockOn)
	if err != nil {
		return nil, fmt.Errorf("list chain apply servers: %w", err)
	}
	return ids, nil
}

// desiredSet 是一台服务器上应有的平台条目及其来历。
type desiredSet struct {
	section  chain.Section
	included []int64
	// changedAt 是纳入的各规则（或其出口）最近一次改动的时间；ownRoutes 是其中落地为用户出口的规则及其内容指纹
	changedAt map[int64]time.Time
	ownRoutes map[int64]string
}

// latestChange 返回纳入的规则里最近一次改动的时间。
func (d desiredSet) latestChange() time.Time {
	var latest time.Time
	for _, id := range d.included {
		if at := d.changedAt[id]; at.Compare(latest) > 0 {
			latest = at
		}
	}
	return latest
}

// repinStale 复查一个用户的服务端规则引用的用户出口：固定的地址超过复查周期的重新解析（docs/03 §5.19 用户出口的地址限制）。
// 仍在解析结果里就不变；解析结果里有非公网地址或指向平台自己的服务器则作废（规则随之阻断）；暂时解析不了保持原样。
// 返回是否有出口的固定地址变了（调用方需要重读状态）。platform 缓存本轮算好的平台地址，一轮下发只算一次。
func (a *ChainApplier) repinStale(ctx context.Context, st *chainState, platform *platformAddrCache) (bool, error) {
	now := a.now()
	isChanged := false
	isChecked := make(map[int64]bool, len(st.exits))
	for _, r := range st.routes {
		isOwnServer := r.Kind == ChainKindServer && r.Exit.Kind == ChainExitOwn
		if !isOwnServer || isChecked[r.Exit.ID] {
			continue
		}
		isChecked[r.Exit.ID] = true
		sec := st.exits[r.Exit.ID]
		if !sec.isParsed || sec.pinned == nil || now.Sub(sec.resolvedAt) < chainRepinAfter {
			continue
		}
		if _, err := netip.ParseAddr(sec.node.Address); err == nil {
			continue
		}
		next, err := a.repinExit(ctx, sec, platform)
		if errors.Is(err, errExitResolve) {
			next = sec.pinned
		}
		if next == nil || *next != *sec.pinned {
			isChanged = true
		}
		err = a.q.SetChainExitResolved(ctx, sqlcgen.SetChainExitResolvedParams{ID: r.Exit.ID, ResolvedIp: next, ResolvedAt: timestamptz(now)})
		if err != nil {
			return false, fmt.Errorf("repin chain exit %d: %w", r.Exit.ID, err)
		}
	}
	return isChanged, nil
}

// repinExit 重新解析一个出口：返回新固定的地址；不再安全时返回 nil；暂时解析不了返回 errExitResolve。
// platform 缓存本轮算好的平台地址。
func (a *ChainApplier) repinExit(ctx context.Context, sec exitSecret, platform *platformAddrCache) (*netip.Addr, error) {
	ip, err := a.guard.pinOffPlatform(ctx, a.q, sec.node.Address, sec.pinned, platform)
	switch {
	case err == nil:
		return &ip, nil
	case errors.Is(err, errExitResolve):
		return nil, err
	case errors.Is(err, errExitPrivate), errors.Is(err, errExitPlatform):
		return nil, nil
	default:
		return nil, err
	}
}

// exitNodeFor 返回一条规则的落地写进面板时用的参数；平台节点取本人在落地面板上的参数（与订阅同源），还没同步到时返回 false。
func (a *ChainApplier) exitNodeFor(ctx context.Context, st *chainState, r ChainRoute, userID int64, subNodes *map[int64]render.ProxyNode) (render.ProxyNode, bool, error) {
	if r.Exit.Kind != ChainExitNode {
		node, ok := st.exits[r.Exit.ID].serverNode()
		return node, ok, nil
	}
	if *subNodes == nil {
		items, err := a.users.loadSubItems(ctx, userID)
		if err != nil {
			return render.ProxyNode{}, false, err
		}
		nodes := make(map[int64]render.ProxyNode, len(items))
		for _, it := range items {
			nodes[it.nodeID] = it.node
		}
		*subNodes = nodes
	}
	node, ok := (*subNodes)[r.Exit.ID]
	return node, ok, nil
}

// userEntries 生成一个用户在这台服务器上的条目：每条有起飞入站在这里生效的规则一条（docs/03 §5.19 下发单位）。
// platform 是本轮共用的平台地址缓存，复查出口时用。
func (a *ChainApplier) userEntries(ctx context.Context, serverID, userID int64, coreServers map[int64]bool, isRepinAllowed bool, set *desiredSet, platform *platformAddrCache) ([]chain.Entry, error) {
	email, err := a.q.GetProvisionEmail(ctx, sqlcgen.GetProvisionEmailParams{UserID: userID, ServerID: serverID})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && email == "") {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get provision email of user %d on server %d: %w", userID, serverID, err)
	}
	st, err := loadChainState(ctx, a.q, a.sealer, coreServers, userID)
	if err != nil {
		return nil, err
	}
	if isRepinAllowed {
		isChanged, err := a.repinStale(ctx, st, platform)
		if err != nil {
			return nil, err
		}
		if isChanged {
			if st, err = loadChainState(ctx, a.q, a.sealer, coreServers, userID); err != nil {
				return nil, err
			}
		}
	}
	var subNodes map[int64]render.ProxyNode
	out := make([]chain.Entry, 0, len(st.routes))
	for _, r := range st.routes {
		if r.Kind != ChainKindServer {
			continue
		}
		tags := make([]string, 0, len(r.Entries))
		for _, e := range r.Entries {
			isHere := e.ServerID == serverID && isEffectiveEntry(e.State) && e.InboundTag != ""
			if isHere && !slices.Contains(tags, e.InboundTag) {
				tags = append(tags, e.InboundTag)
			}
		}
		if len(tags) == 0 {
			continue
		}
		exit, ok, err := a.exitNodeFor(ctx, st, r, userID, &subNodes)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		out = append(out, chain.Entry{RuleID: r.ID, InboundTags: tags, UserEmail: email, Exit: exit})
		set.changedAt[r.ID] = r.changedAt
		if r.Exit.Kind == ChainExitOwn {
			set.ownRoutes[r.ID] = routeFingerprint(r.ID, st.exits[r.Exit.ID])
		}
	}
	return out, nil
}

// desired 计算这台服务器上应有的平台条目（链式条目，拦截规则开着时最前面加拦截条目）。isRepinAllowed 为真时顺带复查用户出口固定的地址（要做 DNS 查询，只在后台下发时开）。
func (a *ChainApplier) desired(ctx context.Context, serverID int64, isRepinAllowed bool) (desiredSet, error) {
	set := desiredSet{changedAt: map[int64]time.Time{}, ownRoutes: map[int64]string{}}
	userIDs, err := a.q.ListServerRouteUsersOnServer(ctx, serverID)
	if err != nil {
		return set, fmt.Errorf("list server route users on server %d: %w", serverID, err)
	}
	coreServers, err := coreConfigServers(ctx, a.q, a)
	if err != nil {
		return set, err
	}
	entries := make([]chain.Entry, 0, len(userIDs))
	var platform platformAddrCache
	for _, userID := range userIDs {
		more, err := a.userEntries(ctx, serverID, userID, coreServers, isRepinAllowed, &set, &platform)
		if err != nil {
			return set, err
		}
		entries = append(entries, more...)
	}
	if set.section, set.included, err = chain.Build(entries); err != nil {
		return set, err
	}
	isBlockOn, err := a.isBlockRuleOn(ctx)
	if err != nil || !isBlockOn {
		return set, err
	}
	if set.section, err = set.section.WithBlock(); err != nil {
		return set, err
	}
	return set, nil
}

// quarantine 失败隔离（docs/03 §5.19，Ruling-49）：把本次相对上次成功新增或有变、且落地为用户出口的规则记为隔离，
// 下一轮不带它们重新下发。返回隔离了几条。
func (a *ChainApplier) quarantine(ctx context.Context, rec applyRecord, set desiredSet) (int, error) {
	count := 0
	for _, id := range set.included {
		fingerprint, isOwn := set.ownRoutes[id]
		if !isOwn || fingerprint == "" {
			continue
		}
		isNew := !slices.Contains(rec.applied, id)
		isChanged := !rec.appliedAt.Valid || set.changedAt[id].Compare(rec.appliedAt.Time) > 0
		if !isNew && !isChanged {
			continue
		}
		if err := a.q.SetChainRouteQuarantine(ctx, sqlcgen.SetChainRouteQuarantineParams{ID: id, QuarantineHash: fingerprint}); err != nil {
			return count, fmt.Errorf("quarantine chain route %d: %w", id, err)
		}
		count++
	}
	return count, nil
}

// applyRecord 是 chain_apply 一行的内存形态。
type applyRecord struct {
	exists        bool
	status        string
	desiredHash   string
	applied       []int64
	failCount     int
	lastError     string
	lastAttemptAt pgtype.Timestamptz
	nextAttemptAt pgtype.Timestamptz
	appliedAt     pgtype.Timestamptz
	backupEnc     []byte
}

func (a *ChainApplier) loadRecord(ctx context.Context, q *sqlcgen.Queries, serverID int64) (applyRecord, error) {
	row, err := q.GetChainApply(ctx, serverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return applyRecord{status: chainApplyNone}, nil
	}
	if err != nil {
		return applyRecord{}, fmt.Errorf("get chain apply of server %d: %w", serverID, err)
	}
	return applyRecord{
		exists: true, status: row.Status, desiredHash: row.DesiredHash, applied: row.AppliedRuleIds, failCount: int(row.FailCount),
		lastError: row.LastError, lastAttemptAt: row.LastAttemptAt, nextAttemptAt: row.NextAttemptAt, appliedAt: row.AppliedAt,
		backupEnc: row.BackupEnc,
	}, nil
}

func (a *ChainApplier) saveRecord(ctx context.Context, serverID int64, r applyRecord) error {
	applied := r.applied
	if applied == nil {
		applied = []int64{}
	}
	err := a.q.UpsertChainApply(ctx, sqlcgen.UpsertChainApplyParams{
		ServerID: serverID, Status: r.status, DesiredHash: r.desiredHash, AppliedRuleIds: applied, FailCount: int32(r.failCount),
		LastError: truncate(r.lastError, maxChainErrorLen), LastAttemptAt: r.lastAttemptAt, NextAttemptAt: r.nextAttemptAt,
		AppliedAt: r.appliedAt, BackupEnc: r.backupEnc,
	})
	if err != nil {
		return fmt.Errorf("save chain apply of server %d: %w", serverID, err)
	}
	return nil
}

// errorMessage 把错误转成可展示的脱敏摘要：面板错误取其说明，其它错误记日志后给通用说明。
func errorMessage(serverID int64, err error) string {
	var se *Error
	if errors.As(err, &se) {
		return se.Message
	}
	if pe := panel.AsError(err); pe != nil {
		return pe.Message
	}
	slog.Error("chain apply", "server_id", serverID, "err", err)
	return msgChainInternal
}

func timestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// ApplyServer 检查一台服务器并在需要时下发（docs/03 §3 C1–C5）。错误已记入下发记录时返回 nil；返回的错误只用于日志。
func (a *ChainApplier) ApplyServer(ctx context.Context, serverID int64) error {
	rec, err := a.loadRecord(ctx, a.q, serverID)
	if err != nil {
		return err
	}
	// 先取数据库时间再算期望：之后才发生的改动，时间一定晚于它，不会被误当成已下发
	checkedAt, err := a.q.GetDBNow(ctx)
	if err != nil {
		return fmt.Errorf("get db time: %w", err)
	}
	set, err := a.desired(ctx, serverID, true)
	if err != nil {
		return err
	}
	hash, err := set.section.Hash()
	if err != nil {
		return err
	}
	run := applyRun{serverID: serverID, set: set, hash: hash, checkedAt: checkedAt}
	prov, _, err := a.panels.openEnabledProvider(ctx, serverID)
	if err != nil {
		return a.markPending(ctx, run, rec, errorMessage(serverID, err))
	}
	cc, ok := prov.(panel.CoreConfigurator)
	if !ok || !prov.Capabilities().CoreConfig {
		return nil
	}
	tpl, err := cc.GetCoreTemplate(ctx)
	if err != nil {
		return a.markPending(ctx, run, rec, errorMessage(serverID, err))
	}
	current, err := chain.Extract(tpl.Config)
	if err != nil {
		return a.markPending(ctx, run, rec, msgChainBadTemplate)
	}
	currentHash, err := current.Hash()
	if err != nil {
		return err
	}
	if currentHash == hash {
		return a.markConsistent(ctx, run, rec)
	}
	return a.applyChange(ctx, run, rec, cc, tpl)
}

// applyRun 是一轮检查里不变的东西：哪台服务器、期望条目及其哈希、开始检查时的数据库时间。
type applyRun struct {
	serverID  int64
	set       desiredSet
	hash      string
	checkedAt pgtype.Timestamptz
}

// markPending 记下“待下发”及原因（面板离线、读不到模板等，不算一次失败）；从没有过记录且没有要下发的规则时不建记录。
func (a *ChainApplier) markPending(ctx context.Context, run applyRun, rec applyRecord, reason string) error {
	if !rec.exists && len(run.set.included) == 0 {
		return nil
	}
	if rec.desiredHash != run.hash {
		rec.failCount, rec.nextAttemptAt = 0, pgtype.Timestamptz{}
	}
	if rec.status != chainApplyFailed || rec.desiredHash != run.hash {
		rec.status = chainApplyPending
	}
	rec.desiredHash, rec.lastError = run.hash, reason
	return a.saveRecord(ctx, run.serverID, rec)
}

// markConsistent 面板现状与期望一致（C5，或被别处改成了期望的样子）。
// 条目没变但有规则在上次记录之后改动过（如只改了名称）时也要刷新时间，否则那条规则会一直显示下发中。
func (a *ChainApplier) markConsistent(ctx context.Context, run applyRun, rec applyRecord) error {
	included := run.set.included
	if !rec.exists && len(included) == 0 {
		return nil
	}
	isSame := rec.status == chainApplyApplied && rec.desiredHash == run.hash && len(rec.applied) == len(included)
	isStale := !rec.appliedAt.Valid || run.set.latestChange().Compare(rec.appliedAt.Time) > 0
	if isSame && !isStale {
		return nil
	}
	rec.status, rec.desiredHash, rec.applied = chainApplyApplied, run.hash, included
	rec.failCount, rec.lastError, rec.nextAttemptAt = 0, "", pgtype.Timestamptz{}
	rec.appliedAt = run.checkedAt
	return a.saveRecord(ctx, run.serverID, rec)
}

// applyChange 面板现状与期望不同：在间隔、退避与失败上限允许时写入并确认，失败则回滚（C1–C4）。
func (a *ChainApplier) applyChange(ctx context.Context, run applyRun, rec applyRecord, cc panel.CoreConfigurator, tpl panel.CoreTemplate) error {
	serverID, hash, included := run.serverID, run.hash, run.set.included
	now := a.now()
	sameDesired := rec.exists && rec.desiredHash == hash
	if !sameDesired {
		rec.failCount, rec.nextAttemptAt = 0, pgtype.Timestamptz{}
	}
	if sameDesired && rec.failCount >= a.cfg.MaxFailures {
		return nil
	}
	if sameDesired && rec.nextAttemptAt.Valid && now.Before(rec.nextAttemptAt.Time) {
		return nil
	}
	if rec.lastAttemptAt.Valid && now.Sub(rec.lastAttemptAt.Time) < a.cfg.MinInterval {
		if !sameDesired || rec.status != chainApplyFailed {
			rec.status = chainApplyPending
		}
		rec.desiredHash = hash
		return a.saveRecord(ctx, serverID, rec)
	}

	backup, err := a.sealer.Seal(tpl.Config, chainBackupAAD(serverID))
	if err != nil {
		return fmt.Errorf("seal core template backup of server %d: %w", serverID, err)
	}
	merged, err := chain.Merge(tpl.Config, run.set.section)
	if err != nil {
		return a.markPending(ctx, run, rec, msgChainBadTemplate)
	}
	rec.status, rec.desiredHash, rec.lastAttemptAt, rec.backupEnc = chainApplyPending, hash, timestamptz(now), backup
	if err := a.saveRecord(ctx, serverID, rec); err != nil {
		return err
	}

	written, writeErr := a.writeAndConfirm(ctx, cc, panel.CoreTemplate{Config: merged, Extra: tpl.Extra})
	if writeErr != nil {
		return a.recordFailure(ctx, run, rec, cc, tpl, written, writeErr)
	}
	rec.status, rec.applied, rec.appliedAt = chainApplyApplied, included, run.checkedAt
	rec.failCount, rec.lastError, rec.nextAttemptAt = 0, "", pgtype.Timestamptz{}
	if err := a.saveRecord(ctx, serverID, rec); err != nil {
		return err
	}
	return writeAudit(ctx, a.q, AuditEntry{ActorType: ActorSystem, Action: ActionChainApply, TargetType: targetServer,
		TargetID: fmt.Sprint(serverID), Detail: map[string]any{"ruleCount": len(included)}})
}

// recordFailure 处理一次下发失败（C3）：必要时回滚，记失败与退避；配置被面板拒绝或内核起不来时做失败隔离。
func (a *ChainApplier) recordFailure(ctx context.Context, run applyRun, rec applyRecord, cc panel.CoreConfigurator,
	original panel.CoreTemplate, written bool, writeErr error) error {
	serverID := run.serverID
	// 写入被面板拒绝时模板没变（3x-ui 保存前会校验），不必回滚，也不该为此再重启一次
	if written {
		a.rollback(ctx, serverID, cc, original)
	}
	quarantined := 0
	isConfigFault := errors.Is(writeErr, panel.ErrRejected) || errors.Is(writeErr, errChainCoreDown)
	if isConfigFault {
		var err error
		if quarantined, err = a.quarantine(ctx, rec, run.set); err != nil {
			return err
		}
	}
	rec.failCount++
	backoff := a.cfg.Backoff[min(rec.failCount, len(a.cfg.Backoff))-1]
	rec.status, rec.lastError, rec.nextAttemptAt = chainApplyFailed, errorMessage(serverID, writeErr), timestamptz(a.now().Add(backoff))
	if err := a.saveRecord(ctx, serverID, rec); err != nil {
		return err
	}
	if quarantined > 0 {
		// 隔离后期望变了，不必等退避，尽快带着剩下的规则再下发
		a.Kick()
	}
	return writeAudit(ctx, a.q, AuditEntry{ActorType: ActorSystem, Action: ActionChainApplyFailed, TargetType: targetServer,
		TargetID: fmt.Sprint(serverID), Detail: map[string]any{
			"ruleCount": len(run.set.included), "failCount": rec.failCount, "error": rec.lastError, "quarantined": quarantined}})
}

// writeAndConfirm 写入模板、重启内核，并确认内核稳定运行；written 表示模板已写进面板（失败时需要回滚）。
func (a *ChainApplier) writeAndConfirm(ctx context.Context, cc panel.CoreConfigurator, tpl panel.CoreTemplate) (bool, error) {
	if err := cc.SetCoreTemplate(ctx, tpl); err != nil {
		return false, err
	}
	if err := cc.RestartCore(ctx); err != nil {
		return true, err
	}
	return true, a.confirmRunning(ctx, cc)
}

// confirmRunning 在时限内等内核进入运行状态，再隔一段时间确认仍在运行（排除启动后立刻崩溃）。
// 按次数而不是按时钟计时限：时钟可由调用方注入，不能依赖它前进。
func (a *ChainApplier) confirmRunning(ctx context.Context, cc panel.CoreConfigurator) error {
	if err := sleepCtx(ctx, a.cfg.ConfirmDelay); err != nil {
		return err
	}
	attempts := max(int(a.cfg.ConfirmTimeout/a.cfg.PollInterval), 1)
	lastMessage := ""
	for i := 0; i < attempts; i++ {
		if i > 0 {
			if err := sleepCtx(ctx, a.cfg.PollInterval); err != nil {
				return err
			}
		}
		state, err := cc.CoreState(ctx)
		if err != nil {
			return err
		}
		if state.Running {
			if err := sleepCtx(ctx, a.cfg.StablePause); err != nil {
				return err
			}
			if state, err = cc.CoreState(ctx); err != nil {
				return err
			}
			if state.Running {
				return nil
			}
		}
		if state.Message != "" {
			lastMessage = state.Message
		}
	}
	msg := msgChainCoreNotRunning
	if lastMessage != "" {
		msg += "：" + lastMessage
	}
	return fmt.Errorf("%w: %w", errChainCoreDown, newError(CodeUpstream, msg))
}

// rollback 写回写入前的模板并重启；失败只能记日志，下发记录里已标为失败，下一轮会重新对账。
func (a *ChainApplier) rollback(ctx context.Context, serverID int64, cc panel.CoreConfigurator, original panel.CoreTemplate) {
	if err := cc.SetCoreTemplate(ctx, original); err != nil {
		slog.Error("chain apply rollback: restore template", "server_id", serverID, "err", err)
		return
	}
	if err := cc.RestartCore(ctx); err != nil {
		slog.Error("chain apply rollback: restart core", "server_id", serverID, "err", err)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// serverProviderType 返回服务器的面板类型；服务器不存在返回 404。
func (a *ChainApplier) serverProviderType(ctx context.Context, serverID int64) (string, error) {
	srv, err := a.panels.GetServer(ctx, serverID)
	if err != nil {
		return "", err
	}
	return srv.ProviderType, nil
}

// Status 返回一台服务器作为起飞服务器的下发状态（管理端，server:read）。
func (a *ChainApplier) Status(ctx context.Context, serverID int64) (*ChainApply, error) {
	providerType, err := a.serverProviderType(ctx, serverID)
	if err != nil {
		return nil, err
	}
	rec, err := a.loadRecord(ctx, a.q, serverID)
	if err != nil {
		return nil, err
	}
	set, err := a.desired(ctx, serverID, false)
	if err != nil {
		return nil, err
	}
	included := set.included
	return &ChainApply{
		ServerID: serverID, Supported: a.panels.SupportsCoreConfig(providerType), Status: rec.status,
		RuleCount: len(included), AppliedRuleCount: len(rec.applied), FailCount: rec.failCount,
		RetryStopped: rec.status == chainApplyFailed && rec.failCount >= a.cfg.MaxFailures, LastError: rec.lastError,
		LastAttemptAt: timePtr(rec.lastAttemptAt), NextAttemptAt: timePtr(rec.nextAttemptAt), AppliedAt: timePtr(rec.appliedAt),
	}, nil
}

// Retry 清零失败次数与退避并触发一轮（docs/03 §3 C4，server:write），写审计。
func (a *ChainApplier) Retry(ctx context.Context, actor *AdminPrincipal, serverID int64, ip *netip.Addr) (*ChainApply, error) {
	if _, err := a.serverProviderType(ctx, serverID); err != nil {
		return nil, err
	}
	err := a.inTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := q.ResetChainApplyFailures(ctx, serverID); err != nil {
			return fmt.Errorf("reset chain apply failures of server %d: %w", serverID, err)
		}
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionChainApplyRetry,
			TargetType: targetServer, TargetID: fmt.Sprint(serverID), ClientIP: ip})
	})
	if err != nil {
		return nil, err
	}
	a.Kick()
	return a.Status(ctx, serverID)
}
