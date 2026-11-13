package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
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

// 服务端链式下发（docs/03 §5.19、§3 C1–C5，Ruling-40）：以入口服务器为单位，把面板内核模板里的平台条目整体对齐到期望；
// 写入前加密备份整份模板，重启后内核未运行即写回备份并记失败。

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
}

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

// ChainApply 是一台入口服务器的下发状态（管理端展示）。
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

// AdminServerChainRule 是管理端全站列表里的一条服务端规则。
type AdminServerChainRule struct {
	ServerChainRule
	UserID          int64
	Username        string
	EntryServerName string
}

// ServerChainRuleFilter 是全站列表的筛选条件；0 表示不限。
type ServerChainRuleFilter struct {
	UserID   int64
	ServerID int64
}

// ChainApplier 负责把服务端规则下发到入口面板。并发安全：后台任务串行处理各服务器，接口只读状态或触发一轮。
type ChainApplier struct {
	store
	sealer *crypto.Sealer
	users  *UserService
	panels *PanelService
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
		store: newStore(pool), sealer: sealer, users: users, panels: panels,
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

// ListServerIDs 返回需要检查下发的服务器。
func (a *ChainApplier) ListServerIDs(ctx context.Context) ([]int64, error) {
	ids, err := a.q.ListChainApplyServerIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list chain apply servers: %w", err)
	}
	return ids, nil
}

// chainUserCtx 是计算期望时某个用户的缓存：开关、可用节点、各节点参数、在这台服务器上的 client email。
type chainUserCtx struct {
	flags     ChainFlags
	available map[int64]sqlcgen.ListUserSubNodesRow
	nodes     map[int64]render.ProxyNode
	email     string
}

func (a *ChainApplier) loadUserCtx(ctx context.Context, userID, serverID int64) (chainUserCtx, error) {
	flags, err := chainFlags(ctx, a.q, userID)
	if err != nil {
		return chainUserCtx{}, err
	}
	_, available, err := availableNodes(ctx, a.q, userID)
	if err != nil {
		return chainUserCtx{}, err
	}
	items, err := a.users.loadSubItems(ctx, userID)
	if err != nil {
		return chainUserCtx{}, err
	}
	nodes := make(map[int64]render.ProxyNode, len(items))
	for _, it := range items {
		nodes[it.nodeID] = it.node
	}
	email, err := a.q.GetProvisionEmail(ctx, sqlcgen.GetProvisionEmailParams{UserID: userID, ServerID: serverID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return chainUserCtx{}, fmt.Errorf("get provision email of user %d on server %d: %w", userID, serverID, err)
	}
	return chainUserCtx{flags: flags, available: available, nodes: nodes, email: email}, nil
}

// desired 计算这台服务器上应有的平台条目：不阻断、启用、落地凭据与入口 client 都已就绪的规则（docs/03 §5.19 下发单位）。
func (a *ChainApplier) desired(ctx context.Context, serverID int64) (chain.Section, []int64, error) {
	rows, err := a.q.ListServerChainRulesOnServer(ctx, serverID)
	if err != nil {
		return chain.Section{}, nil, fmt.Errorf("list server chain rules on server %d: %w", serverID, err)
	}
	coreServers, err := coreConfigServers(ctx, a.q, a)
	if err != nil {
		return chain.Section{}, nil, err
	}
	users := make(map[int64]chainUserCtx, len(rows))
	entries := make([]chain.Entry, 0, len(rows))
	for _, r := range rows {
		if !r.IsEnabled {
			continue
		}
		u, ok := users[r.UserID]
		if !ok {
			if u, err = a.loadUserCtx(ctx, r.UserID, serverID); err != nil {
				return chain.Section{}, nil, err
			}
			users[r.UserID] = u
		}
		if serverRuleBlockedReason(u.flags, u.available, coreServers, r.EntryNodeID, r.ExitNodeID) != ChainReasonNone {
			continue
		}
		exit, hasExit := u.nodes[r.ExitNodeID]
		if !hasExit || u.email == "" || r.InboundTag == "" {
			continue
		}
		entries = append(entries, chain.Entry{RuleID: r.ID, InboundTag: r.InboundTag, UserEmail: u.email, Exit: exit})
	}
	return chain.Build(entries)
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
	sec, included, err := a.desired(ctx, serverID)
	if err != nil {
		return err
	}
	hash, err := sec.Hash()
	if err != nil {
		return err
	}
	prov, _, err := a.panels.openEnabledProvider(ctx, serverID)
	if err != nil {
		return a.markPending(ctx, serverID, rec, hash, included, errorMessage(serverID, err))
	}
	cc, ok := prov.(panel.CoreConfigurator)
	if !ok || !prov.Capabilities().CoreConfig {
		return nil
	}
	tpl, err := cc.GetCoreTemplate(ctx)
	if err != nil {
		return a.markPending(ctx, serverID, rec, hash, included, errorMessage(serverID, err))
	}
	current, err := chain.Extract(tpl.Config)
	if err != nil {
		return a.markPending(ctx, serverID, rec, hash, included, "面板上的内核模板不是预期的 JSON")
	}
	currentHash, err := current.Hash()
	if err != nil {
		return err
	}
	if currentHash == hash {
		return a.markConsistent(ctx, serverID, rec, hash, included)
	}
	return a.applyChange(ctx, serverID, rec, cc, tpl, sec, hash, included)
}

// markPending 记下“待下发”及原因（面板离线、读不到模板等，不算一次失败）；从没有过记录且没有要下发的规则时不建记录。
func (a *ChainApplier) markPending(ctx context.Context, serverID int64, rec applyRecord, hash string, included []int64, reason string) error {
	if !rec.exists && len(included) == 0 {
		return nil
	}
	if rec.desiredHash != hash {
		rec.failCount, rec.nextAttemptAt = 0, pgtype.Timestamptz{}
	}
	if rec.status != chainApplyFailed || rec.desiredHash != hash {
		rec.status = chainApplyPending
	}
	rec.desiredHash, rec.lastError = hash, reason
	return a.saveRecord(ctx, serverID, rec)
}

// markConsistent 面板现状与期望一致（C5，或被别处改成了期望的样子）。
func (a *ChainApplier) markConsistent(ctx context.Context, serverID int64, rec applyRecord, hash string, included []int64) error {
	if !rec.exists && len(included) == 0 {
		return nil
	}
	unchanged := rec.status == chainApplyApplied && rec.desiredHash == hash && len(rec.applied) == len(included)
	if unchanged {
		return nil
	}
	rec.status, rec.desiredHash, rec.applied = chainApplyApplied, hash, included
	rec.failCount, rec.lastError, rec.nextAttemptAt = 0, "", pgtype.Timestamptz{}
	rec.appliedAt = timestamptz(a.now())
	return a.saveRecord(ctx, serverID, rec)
}

// applyChange 面板现状与期望不同：在间隔、退避与失败上限允许时写入并确认，失败则回滚（C1–C4）。
func (a *ChainApplier) applyChange(ctx context.Context, serverID int64, rec applyRecord, cc panel.CoreConfigurator,
	tpl panel.CoreTemplate, sec chain.Section, hash string, included []int64) error {
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
	merged, err := chain.Merge(tpl.Config, sec)
	if err != nil {
		return a.markPending(ctx, serverID, rec, hash, included, "面板上的内核模板不是预期的 JSON")
	}
	rec.status, rec.desiredHash, rec.lastAttemptAt, rec.backupEnc = chainApplyPending, hash, timestamptz(now), backup
	if err := a.saveRecord(ctx, serverID, rec); err != nil {
		return err
	}

	written, writeErr := a.writeAndConfirm(ctx, cc, panel.CoreTemplate{Config: merged, Extra: tpl.Extra})
	if writeErr != nil {
		// 写入被面板拒绝时模板没变（3x-ui 保存前会校验），不必回滚，也不该为此再重启一次
		if written {
			a.rollback(ctx, serverID, cc, tpl)
		}
		rec.failCount++
		backoff := a.cfg.Backoff[min(rec.failCount, len(a.cfg.Backoff))-1]
		rec.status, rec.lastError, rec.nextAttemptAt = chainApplyFailed, errorMessage(serverID, writeErr), timestamptz(a.now().Add(backoff))
		if err := a.saveRecord(ctx, serverID, rec); err != nil {
			return err
		}
		return writeAudit(ctx, a.q, AuditEntry{ActorType: ActorSystem, Action: ActionChainApplyFailed, TargetType: targetServer,
			TargetID: fmt.Sprint(serverID), Detail: map[string]any{"ruleCount": len(included), "failCount": rec.failCount, "error": rec.lastError}})
	}
	rec.status, rec.applied, rec.appliedAt = chainApplyApplied, included, timestamptz(a.now())
	rec.failCount, rec.lastError, rec.nextAttemptAt = 0, "", pgtype.Timestamptz{}
	if err := a.saveRecord(ctx, serverID, rec); err != nil {
		return err
	}
	return writeAudit(ctx, a.q, AuditEntry{ActorType: ActorSystem, Action: ActionChainApply, TargetType: targetServer,
		TargetID: fmt.Sprint(serverID), Detail: map[string]any{"ruleCount": len(included)}})
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
	return newError(CodeUpstream, msg)
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
	rows, err := a.q.ListServerProviderTypes(ctx)
	if err != nil {
		return "", fmt.Errorf("list server provider types: %w", err)
	}
	for _, r := range rows {
		if r.ID == serverID {
			return r.ProviderType, nil
		}
	}
	return "", newError(CodeNotFound, msgServerNotFound)
}

// Status 返回一台服务器作为入口的下发状态（管理端，server:read）。
func (a *ChainApplier) Status(ctx context.Context, serverID int64) (*ChainApply, error) {
	providerType, err := a.serverProviderType(ctx, serverID)
	if err != nil {
		return nil, err
	}
	rec, err := a.loadRecord(ctx, a.q, serverID)
	if err != nil {
		return nil, err
	}
	_, included, err := a.desired(ctx, serverID)
	if err != nil {
		return nil, err
	}
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

// ListServerRules 返回全站服务端规则（管理端只读，Ruling-41），按 id 倒序分页。
func (a *ChainApplier) ListServerRules(ctx context.Context, f ServerChainRuleFilter, pq PageQuery) (*Page[AdminServerChainRule], error) {
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	userArg := pgtype.Int8{Int64: f.UserID, Valid: f.UserID > 0}
	serverArg := pgtype.Int8{Int64: f.ServerID, Valid: f.ServerID > 0}
	total, err := a.q.CountAdminServerChainRules(ctx, sqlcgen.CountAdminServerChainRulesParams{UserID: userArg, ServerID: serverArg})
	if err != nil {
		return nil, fmt.Errorf("count admin server chain rules: %w", err)
	}
	if total == 0 {
		return &Page[AdminServerChainRule]{Items: []AdminServerChainRule{}}, nil
	}
	rows, err := a.q.ListAdminServerChainRules(ctx, sqlcgen.ListAdminServerChainRulesParams{
		UserID: userArg, ServerID: serverArg, RowLimit: limit, RowOffset: offset,
	})
	if err != nil {
		return nil, fmt.Errorf("list admin server chain rules: %w", err)
	}
	coreServers, err := coreConfigServers(ctx, a.q, a)
	if err != nil {
		return nil, err
	}
	serverIDs := make([]int64, 0, len(rows))
	for _, r := range rows {
		serverIDs = append(serverIDs, r.EntryServerID)
	}
	views, err := applyViews(ctx, a.q, serverIDs)
	if err != nil {
		return nil, err
	}
	type userState struct {
		flags     ChainFlags
		available map[int64]sqlcgen.ListUserSubNodesRow
	}
	states := make(map[int64]userState, len(rows))
	items := make([]AdminServerChainRule, 0, len(rows))
	for _, r := range rows {
		st, ok := states[r.UserID]
		if !ok {
			if st.flags, err = chainFlags(ctx, a.q, r.UserID); err != nil {
				return nil, err
			}
			if _, st.available, err = availableNodes(ctx, a.q, r.UserID); err != nil {
				return nil, err
			}
			states[r.UserID] = st
		}
		item := AdminServerChainRule{
			ServerChainRule: ServerChainRule{
				ID: r.ID, EntryNodeID: r.EntryNodeID, EntryName: r.EntryName, EntryServerID: r.EntryServerID,
				ExitNodeID: r.ExitNodeID, ExitName: r.ExitName, Enabled: r.IsEnabled, CreatedAt: r.CreatedAt.Time,
			},
			UserID: r.UserID, Username: r.Username, EntryServerName: r.EntryServerName,
		}
		finishServerRule(&item.ServerChainRule, serverRuleBlockedReason(st.flags, st.available, coreServers, r.EntryNodeID, r.ExitNodeID), views)
		items = append(items, item)
	}
	return &Page[AdminServerChainRule]{Items: items, Total: total}, nil
}
