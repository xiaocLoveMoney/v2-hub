package service

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"v2-hub-server/internal/dao/sqlcgen"
)

// 服务端链式（docs/03 §5.19）：在入口面板的内核模板里按用户加出站与路由，本人连入口的流量经落地出网（Ruling-39）。
// 本文件是用户侧规则的增删改与状态；下发到面板见 chain_apply.go。

// 服务端规则的下发情况（与 hub-api.yaml 的 ServerChainApplyState 逐项对应）。
const (
	ServerApplyNone    = ""
	ServerApplyApplied = "applied"
	ServerApplyPending = "pending"
	ServerApplyFailed  = "failed"
)

const (
	// MaxServerChainRules 是每个用户的服务端规则上限（Ruling-41）。
	MaxServerChainRules = 10

	msgServerChainNotAllowed = "当前套餐不支持服务端链式"
	msgServerChainNotFound   = "服务端链式规则不存在"
	msgServerChainTooMany    = "服务端链式规则已达上限"
	msgServerChainDuplicate  = "这个入口节点已有服务端链式规则"
	msgServerChainNoCore     = "入口节点所在的面板不支持服务端链式"
)

// ServerChainRule 是一条服务端规则；Status 与 ApplyStatus 是计算值。
type ServerChainRule struct {
	ID            int64
	EntryNodeID   int64
	EntryName     string
	EntryServerID int64
	ExitNodeID    int64
	ExitName      string
	ExitProtocol  string
	Enabled       bool
	Status        string
	BlockedReason string
	ApplyStatus   string
	CreatedAt     time.Time
}

// ServerChainRuleInput 是新建服务端规则的输入。
type ServerChainRuleInput struct {
	EntryNodeID int64
	ExitNodeID  int64
	Enabled     bool
}

// applyView 是一台入口服务器的下发记录里判断规则状态要用的部分。
type applyView struct {
	status  string
	applied []int64
}

// serverRuleBlockedReason 算服务端规则的阻断原因（docs/03 §5.19 状态）；不阻断返回空串。
func serverRuleBlockedReason(flags ChainFlags, available map[int64]sqlcgen.ListUserSubNodesRow, coreServers map[int64]bool, entryID, exitID int64) string {
	entry, isEntryAvailable := available[entryID]
	exit, isExitAvailable := available[exitID]
	switch {
	case !flags.ServerChain:
		return ChainReasonPlan
	case !isEntryAvailable || !coreServers[entry.ServerID]:
		return ChainReasonEntry
	case !isExitAvailable || isQUICProtocol(exit.Protocol):
		return ChainReasonExit
	default:
		return ChainReasonNone
	}
}

// serverApplyState 算一条不阻断且启用的规则的下发情况：已在面板上生效为 applied；否则看入口服务器最近一次下发是否失败。
func serverApplyState(ruleID int64, view applyView, hasView bool) string {
	if hasView && slices.Contains(view.applied, ruleID) {
		return ServerApplyApplied
	}
	if hasView && view.status == chainApplyFailed {
		return ServerApplyFailed
	}
	return ServerApplyPending
}

// finishServerRule 填好状态与下发情况。
func finishServerRule(rule *ServerChainRule, reason string, views map[int64]applyView) {
	rule.Status, rule.BlockedReason, rule.ApplyStatus = ChainOK, reason, ServerApplyNone
	if reason != ChainReasonNone {
		rule.Status = ChainBlocked
		return
	}
	if rule.Enabled {
		view, ok := views[rule.EntryServerID]
		rule.ApplyStatus = serverApplyState(rule.ID, view, ok)
	}
}

// coreConfigServers 返回各服务器的面板是否支持改内核配置。
func (s *ChainService) coreConfigServers(ctx context.Context, q *sqlcgen.Queries) (map[int64]bool, error) {
	return coreConfigServers(ctx, q, s.applier)
}

func coreConfigServers(ctx context.Context, q *sqlcgen.Queries, applier *ChainApplier) (map[int64]bool, error) {
	rows, err := q.ListServerProviderTypes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list server provider types: %w", err)
	}
	out := make(map[int64]bool, len(rows))
	if applier == nil {
		return out, nil
	}
	for _, r := range rows {
		out[r.ID] = applier.panels.SupportsCoreConfig(r.ProviderType)
	}
	return out, nil
}

// applyViews 读取这些入口服务器的下发记录。
func applyViews(ctx context.Context, q *sqlcgen.Queries, serverIDs []int64) (map[int64]applyView, error) {
	out := make(map[int64]applyView, len(serverIDs))
	if len(serverIDs) == 0 {
		return out, nil
	}
	rows, err := q.ListChainApplies(ctx, serverIDs)
	if err != nil {
		return nil, fmt.Errorf("list chain applies: %w", err)
	}
	for _, r := range rows {
		out[r.ServerID] = applyView{status: r.Status, applied: r.AppliedRuleIds}
	}
	return out, nil
}

// listServerRules 返回用户的服务端规则；ruleID 非 0 时只取这一条。
func (s *ChainService) listServerRules(ctx context.Context, q *sqlcgen.Queries, userID, ruleID int64) ([]ServerChainRule, error) {
	flags, err := chainFlags(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	_, available, err := availableNodes(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	coreServers, err := s.coreConfigServers(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListServerChainRules(ctx, sqlcgen.ListServerChainRulesParams{UserID: userID, RuleID: pgtype.Int8{Int64: ruleID, Valid: ruleID != 0}})
	if err != nil {
		return nil, fmt.Errorf("list server chain rules of user %d: %w", userID, err)
	}
	serverIDs := make([]int64, 0, len(rows))
	for _, r := range rows {
		serverIDs = append(serverIDs, r.EntryServerID)
	}
	views, err := applyViews(ctx, q, serverIDs)
	if err != nil {
		return nil, err
	}
	out := make([]ServerChainRule, 0, len(rows))
	for _, r := range rows {
		rule := ServerChainRule{
			ID: r.ID, EntryNodeID: r.EntryNodeID, EntryName: r.EntryName, EntryServerID: r.EntryServerID,
			ExitNodeID: r.ExitNodeID, ExitName: r.ExitName, ExitProtocol: r.ExitProtocol, Enabled: r.IsEnabled, CreatedAt: r.CreatedAt.Time,
		}
		finishServerRule(&rule, serverRuleBlockedReason(flags, available, coreServers, r.EntryNodeID, r.ExitNodeID), views)
		out = append(out, rule)
	}
	return out, nil
}

func (s *ChainService) getServerRule(ctx context.Context, q *sqlcgen.Queries, userID, ruleID int64) (*ServerChainRule, error) {
	rules, err := s.listServerRules(ctx, q, userID, ruleID)
	if err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return nil, newError(CodeNotFound, msgServerChainNotFound)
	}
	return &rules[0], nil
}

// kickApplier 让下发任务尽快检查一轮（规则变化后调用）。
func (s *ChainService) kickApplier() {
	if s.applier != nil {
		s.applier.Kick()
	}
}

// CreateServerRule 新建一条服务端规则（docs/03 §5.19 入口与落地、开关与数量）。
func (s *ChainService) CreateServerRule(ctx context.Context, p *UserPrincipal, in ServerChainRuleInput) (*ServerChainRule, error) {
	if in.EntryNodeID <= 0 || in.ExitNodeID <= 0 {
		return nil, newError(CodeInvalidArgument, msgChainBadExit)
	}
	if in.EntryNodeID == in.ExitNodeID {
		return nil, newError(CodeInvalidArgument, msgChainSameNode)
	}
	var created *ServerChainRule
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := lockChainOwner(ctx, q, p.UserID); err != nil {
			return err
		}
		flags, err := chainFlags(ctx, q, p.UserID)
		if err != nil {
			return err
		}
		if !flags.ServerChain {
			return newError(CodeFeatureDisabled, msgServerChainNotAllowed)
		}
		if err := s.checkServerRuleNodes(ctx, q, p.UserID, in); err != nil {
			return err
		}
		count, err := q.CountServerChainRules(ctx, p.UserID)
		if err != nil {
			return fmt.Errorf("count server chain rules of user %d: %w", p.UserID, err)
		}
		if count >= MaxServerChainRules {
			return newError(CodeInvalidArgument, msgServerChainTooMany)
		}
		id, err := q.InsertServerChainRule(ctx, sqlcgen.InsertServerChainRuleParams{
			UserID: p.UserID, EntryNodeID: in.EntryNodeID, ExitNodeID: pgtype.Int8{Int64: in.ExitNodeID, Valid: true}, IsEnabled: in.Enabled,
		})
		if isUniqueViolation(err) {
			return newError(CodeConflict, msgServerChainDuplicate)
		}
		if err != nil {
			return fmt.Errorf("insert server chain rule: %w", err)
		}
		created, err = s.getServerRule(ctx, q, p.UserID, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.kickApplier()
	return created, nil
}

// checkServerRuleNodes 校验入口与落地：都须是本人当前可用的节点；入口的面板要支持改内核配置；落地不是 QUIC 协议。
func (s *ChainService) checkServerRuleNodes(ctx context.Context, q *sqlcgen.Queries, userID int64, in ServerChainRuleInput) error {
	_, available, err := availableNodes(ctx, q, userID)
	if err != nil {
		return err
	}
	entry, ok := available[in.EntryNodeID]
	if !ok {
		return newError(CodeInvalidArgument, msgChainEntryGone)
	}
	coreServers, err := s.coreConfigServers(ctx, q)
	if err != nil {
		return err
	}
	if !coreServers[entry.ServerID] {
		return newError(CodeInvalidArgument, msgServerChainNoCore)
	}
	exit, ok := available[in.ExitNodeID]
	if !ok {
		return newError(CodeInvalidArgument, msgChainExitGone)
	}
	if isQUICProtocol(exit.Protocol) {
		return newError(CodeInvalidArgument, msgChainExitQUIC)
	}
	return nil
}

// UpdateServerRule 启用或停用服务端规则；套餐没开时拒绝，他人的规则返回 404（R8）。
func (s *ChainService) UpdateServerRule(ctx context.Context, p *UserPrincipal, id int64, enabled bool) (*ServerChainRule, error) {
	var updated *ServerChainRule
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		flags, err := chainFlags(ctx, q, p.UserID)
		if err != nil {
			return err
		}
		if !flags.ServerChain {
			return newError(CodeFeatureDisabled, msgServerChainNotAllowed)
		}
		n, err := q.SetServerChainRuleEnabled(ctx, sqlcgen.SetServerChainRuleEnabledParams{ID: id, UserID: p.UserID, IsEnabled: enabled})
		if err != nil {
			return fmt.Errorf("update server chain rule %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgServerChainNotFound)
		}
		updated, err = s.getServerRule(ctx, q, p.UserID, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.kickApplier()
	return updated, nil
}

// DeleteServerRule 删除服务端规则；套餐没开时也允许，他人的规则返回 404（R8）。面板上的条目由下发任务清掉。
func (s *ChainService) DeleteServerRule(ctx context.Context, p *UserPrincipal, id int64) error {
	n, err := s.q.DeleteServerChainRule(ctx, sqlcgen.DeleteServerChainRuleParams{ID: id, UserID: p.UserID})
	if err != nil {
		return fmt.Errorf("delete server chain rule %d: %w", id, err)
	}
	if n == 0 {
		return newError(CodeNotFound, msgServerChainNotFound)
	}
	s.kickApplier()
	return nil
}
