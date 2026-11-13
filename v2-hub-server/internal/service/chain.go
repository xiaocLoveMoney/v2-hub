package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/crypto"
	"v2-hub-server/internal/manager/render"
)

// 客户端链式（docs/03 §5.18）：入口节点 → 落地的两跳链路，只在订阅里表达（Ruling-36）。
// 落地是用户可用的另一个平台节点，或用户的自有落地节点（Ruling-37）。平台不探测连通性（Ruling-38）。

// 链式规则的状态与阻断原因（与 hub-api.yaml 的 ChainStatus、ChainBlockedReason 逐项对应）。
const (
	ChainOK      = "ok"
	ChainBlocked = "blocked"

	ChainReasonNone  = ""
	ChainReasonPlan  = "plan"
	ChainReasonEntry = "entry"
	ChainReasonExit  = "exit"
)

// 落地的种类。
const (
	ChainExitNode   = "node"
	ChainExitCustom = "custom"
)

const (
	// MaxChainRules、MaxCustomNodes 是每个用户的数量上限（docs/03 §5.18）。
	MaxChainRules  = 20
	MaxCustomNodes = 10

	maxChainNameLen      = 64
	maxCustomNodeLinkLen = 2048
	// 基于 QUIC 的协议不能作落地：经代理拨出不可靠。面板里的协议名是 hysteria / hysteria2。
	quicProtocolPrefix = "hysteria"

	customNodeAADFormat = "custom_node:%d:link"

	msgChainNotAllowed      = "当前套餐不支持链式代理"
	msgCustomNodeNotAllowed = "当前套餐不支持自有落地节点"
	msgChainRuleNotFound    = "链式规则不存在"
	msgCustomNodeNotFound   = "自有落地节点不存在"
	msgChainBadName         = "名称不能超过 64 个字符，且不能含换行等控制字符"
	msgChainBadExit         = "落地须在平台节点与自有落地节点里恰好选一个"
	msgChainEntryGone       = "入口节点不存在或当前不可用"
	msgChainExitGone        = "落地节点不存在或当前不可用"
	msgChainSameNode        = "入口与落地不能是同一个节点"
	msgChainExitQUIC        = "hysteria2 节点不能作落地"
	msgChainTooMany         = "链式规则已达上限"
	msgChainDuplicate       = "已有相同入口与落地的规则"
	msgCustomNodeBadLink    = "链接无法识别：支持 vless、vmess、trojan、ss 分享链接或 socks5://[用户名:口令@]地址:端口"
	msgCustomNodeLinkLen    = "链接长度须为 1–2048 个字符"
	msgCustomNodeNameEmpty  = "名称不能为空"
	msgCustomNodeTooMany    = "自有落地节点已达上限"
	msgCustomNodeDuplicate  = "已有同名的自有落地节点"
)

// ChainFlags 是当前套餐的链式开关。
type ChainFlags struct {
	Chain       bool
	CustomNode  bool
	ServerChain bool
}

// ChainNodeOption 是新建规则时可选的平台节点。
type ChainNodeOption struct {
	ID               int64
	Name             string
	Region           string
	Protocol         string
	CanBeExit        bool
	CanBeServerEntry bool
}

// ChainRule 是一条链式规则。ExitID 是平台节点 id 或自有落地节点 id，取决于 ExitKind；Status 是计算值。
type ChainRule struct {
	ID            int64
	Name          string
	DisplayName   string
	EntryNodeID   int64
	EntryName     string
	ExitKind      string
	ExitID        int64
	ExitName      string
	ExitProtocol  string
	Enabled       bool
	Status        string
	BlockedReason string
	CreatedAt     time.Time
}

// CustomNode 是一个自有落地节点；原链接属于凭据，不在这里。
type CustomNode struct {
	ID        int64
	Name      string
	Protocol  string
	Address   string
	Port      int
	RuleCount int
	CreatedAt time.Time
}

// ChainOverview 是链式代理页需要的全部数据。
type ChainOverview struct {
	Flags       ChainFlags
	Rules       []ChainRule
	CustomNodes []CustomNode
	ServerRules []ServerChainRule
	Nodes       []ChainNodeOption
}

// ChainRuleInput 是新建规则的输入；ExitNodeID 与 ExitCustomNodeID 必须恰好一个非 0。
type ChainRuleInput struct {
	EntryNodeID      int64
	ExitNodeID       int64
	ExitCustomNodeID int64
	Name             string
	Enabled          bool
}

// ChainService 负责链式规则（客户端与服务端）与自有落地节点。
type ChainService struct {
	store
	sealer  *crypto.Sealer
	applier *ChainApplier
}

// NewChainService 创建链式服务；sealer 用于加密自有落地节点的链接；applier 负责把服务端规则下发到面板，
// 为 nil 时任何节点都不能作服务端链式的入口。
func NewChainService(pool *pgxpool.Pool, sealer *crypto.Sealer, applier *ChainApplier) *ChainService {
	return &ChainService{store: newStore(pool), sealer: sealer, applier: applier}
}

func customNodeAAD(id int64) []byte {
	return fmt.Appendf(nil, customNodeAADFormat, id)
}

func isQUICProtocol(protocol string) bool {
	return strings.HasPrefix(strings.ToLower(protocol), quicProtocolPrefix)
}

// chainFlags 读用户当前套餐的开关；用户不存在按都未开处理。
func chainFlags(ctx context.Context, q *sqlcgen.Queries, userID int64) (ChainFlags, error) {
	row, err := q.GetUserChainFlags(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ChainFlags{}, nil
	}
	if err != nil {
		return ChainFlags{}, fmt.Errorf("get chain flags of user %d: %w", userID, err)
	}
	return ChainFlags{Chain: row.AllowClientChain, CustomNode: row.AllowCustomNode, ServerChain: row.AllowServerChain}, nil
}

// availableNodes 返回用户当前可用的平台节点（与节点列表同一口径），按 id 索引。
func availableNodes(ctx context.Context, q *sqlcgen.Queries, userID int64) ([]sqlcgen.ListUserSubNodesRow, map[int64]sqlcgen.ListUserSubNodesRow, error) {
	rows, err := q.ListUserSubNodes(ctx, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("list available nodes of user %d: %w", userID, err)
	}
	byID := make(map[int64]sqlcgen.ListUserSubNodesRow, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	return rows, byID, nil
}

// chainRuleFromRow 组装一条规则并算出状态（docs/03 §5.18 状态）。
func chainRuleFromRow(r sqlcgen.ListChainRulesRow, flags ChainFlags, available map[int64]sqlcgen.ListUserSubNodesRow) ChainRule {
	rule := ChainRule{
		ID: r.ID, Name: r.Name, EntryNodeID: r.EntryNodeID, EntryName: r.EntryName, Enabled: r.IsEnabled, CreatedAt: r.CreatedAt.Time,
		ExitKind: ChainExitNode, ExitID: r.ExitNodeID, ExitName: r.ExitNodeName, ExitProtocol: r.ExitNodeProtocol,
		Status: ChainOK, BlockedReason: ChainReasonNone,
	}
	isCustomExit := r.ExitCustomID != 0
	if isCustomExit {
		rule.ExitKind, rule.ExitID, rule.ExitName, rule.ExitProtocol = ChainExitCustom, r.ExitCustomID, r.ExitCustomName, r.ExitCustomProtocol
	}
	rule.DisplayName = rule.Name
	if rule.DisplayName == "" {
		rule.DisplayName = render.ChainDefaultName(rule.EntryName, rule.ExitName)
	}
	_, isEntryAvailable := available[r.EntryNodeID]
	_, isExitNodeAvailable := available[r.ExitNodeID]
	switch {
	case !flags.Chain || (isCustomExit && !flags.CustomNode):
		rule.BlockedReason = ChainReasonPlan
	case !isEntryAvailable:
		rule.BlockedReason = ChainReasonEntry
	case !isCustomExit && !isExitNodeAvailable:
		rule.BlockedReason = ChainReasonExit
	default:
		// 配置与授权都成立
	}
	if rule.BlockedReason != ChainReasonNone {
		rule.Status = ChainBlocked
	}
	return rule
}

// listRules 返回用户的规则；ruleID 非 0 时只取这一条。
func listRules(ctx context.Context, q *sqlcgen.Queries, userID, ruleID int64) ([]ChainRule, error) {
	flags, err := chainFlags(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	_, available, err := availableNodes(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListChainRules(ctx, sqlcgen.ListChainRulesParams{UserID: userID, RuleID: pgtype.Int8{Int64: ruleID, Valid: ruleID != 0}})
	if err != nil {
		return nil, fmt.Errorf("list chain rules of user %d: %w", userID, err)
	}
	out := make([]ChainRule, 0, len(rows))
	for _, r := range rows {
		out = append(out, chainRuleFromRow(r, flags, available))
	}
	return out, nil
}

func getRule(ctx context.Context, q *sqlcgen.Queries, userID, ruleID int64) (*ChainRule, error) {
	rules, err := listRules(ctx, q, userID, ruleID)
	if err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return nil, newError(CodeNotFound, msgChainRuleNotFound)
	}
	return &rules[0], nil
}

func customNodeFromRow(r sqlcgen.ListCustomNodesRow) CustomNode {
	return CustomNode{ID: r.ID, Name: r.Name, Protocol: r.Protocol, Address: r.Address, Port: int(r.Port), RuleCount: int(r.RuleCount), CreatedAt: r.CreatedAt.Time}
}

func listCustomNodes(ctx context.Context, q *sqlcgen.Queries, userID, nodeID int64) ([]CustomNode, error) {
	rows, err := q.ListCustomNodes(ctx, sqlcgen.ListCustomNodesParams{UserID: userID, NodeID: pgtype.Int8{Int64: nodeID, Valid: nodeID != 0}})
	if err != nil {
		return nil, fmt.Errorf("list custom nodes of user %d: %w", userID, err)
	}
	out := make([]CustomNode, 0, len(rows))
	for _, r := range rows {
		out = append(out, customNodeFromRow(r))
	}
	return out, nil
}

func getCustomNode(ctx context.Context, q *sqlcgen.Queries, userID, nodeID int64) (*CustomNode, error) {
	nodes, err := listCustomNodes(ctx, q, userID, nodeID)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, newError(CodeNotFound, msgCustomNodeNotFound)
	}
	return &nodes[0], nil
}

// Features 返回用户当前套餐的链式开关，供会话信息决定菜单显隐。
func (s *ChainService) Features(ctx context.Context, userID int64) (ChainFlags, error) {
	return chainFlags(ctx, s.q, userID)
}

// Overview 返回链式代理页的全部数据；套餐没开时照常返回，规则状态为阻断。
func (s *ChainService) Overview(ctx context.Context, p *UserPrincipal) (*ChainOverview, error) {
	flags, err := chainFlags(ctx, s.q, p.UserID)
	if err != nil {
		return nil, err
	}
	rows, _, err := availableNodes(ctx, s.q, p.UserID)
	if err != nil {
		return nil, err
	}
	rules, err := listRules(ctx, s.q, p.UserID, 0)
	if err != nil {
		return nil, err
	}
	customNodes, err := listCustomNodes(ctx, s.q, p.UserID, 0)
	if err != nil {
		return nil, err
	}
	serverRules, err := s.listServerRules(ctx, s.q, p.UserID, 0)
	if err != nil {
		return nil, err
	}
	coreServers, err := s.coreConfigServers(ctx, s.q)
	if err != nil {
		return nil, err
	}
	options := make([]ChainNodeOption, 0, len(rows))
	for _, r := range rows {
		options = append(options, ChainNodeOption{
			ID: r.ID, Name: r.DisplayName, Region: r.Region, Protocol: r.Protocol,
			CanBeExit: !isQUICProtocol(r.Protocol), CanBeServerEntry: coreServers[r.ServerID],
		})
	}
	return &ChainOverview{Flags: flags, Rules: rules, CustomNodes: customNodes, ServerRules: serverRules, Nodes: options}, nil
}

// validChainName 校验名称：去首尾空白后不超过上限、不含控制字符（名称会原样写进客户端配置）。
func validChainName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > maxChainNameLen || strings.ContainsFunc(name, unicode.IsControl) {
		return "", newError(CodeInvalidArgument, msgChainBadName)
	}
	return name, nil
}

// lockChainOwner 锁住用户行，让同一用户的数量上限校验串行。
func lockChainOwner(ctx context.Context, q *sqlcgen.Queries, userID int64) error {
	_, err := lockUser(ctx, q, userID)
	return err
}

// CreateRule 新建一条规则（docs/03 §5.18 规则、协议限制）。
func (s *ChainService) CreateRule(ctx context.Context, p *UserPrincipal, in ChainRuleInput) (*ChainRule, error) {
	name, err := validChainName(in.Name)
	if err != nil {
		return nil, err
	}
	hasNodeExit, hasCustomExit := in.ExitNodeID > 0, in.ExitCustomNodeID > 0
	if in.EntryNodeID <= 0 || hasNodeExit == hasCustomExit {
		return nil, newError(CodeInvalidArgument, msgChainBadExit)
	}
	var created *ChainRule
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := lockChainOwner(ctx, q, p.UserID); err != nil {
			return err
		}
		flags, err := chainFlags(ctx, q, p.UserID)
		if err != nil {
			return err
		}
		if !flags.Chain {
			return newError(CodeFeatureDisabled, msgChainNotAllowed)
		}
		if hasCustomExit && !flags.CustomNode {
			return newError(CodeFeatureDisabled, msgCustomNodeNotAllowed)
		}
		_, available, err := availableNodes(ctx, q, p.UserID)
		if err != nil {
			return err
		}
		if _, ok := available[in.EntryNodeID]; !ok {
			return newError(CodeInvalidArgument, msgChainEntryGone)
		}
		if err := checkChainExit(ctx, q, p.UserID, in, available); err != nil {
			return err
		}
		count, err := q.CountChainRules(ctx, p.UserID)
		if err != nil {
			return fmt.Errorf("count chain rules of user %d: %w", p.UserID, err)
		}
		if count >= MaxChainRules {
			return newError(CodeInvalidArgument, msgChainTooMany)
		}
		id, err := q.InsertChainRule(ctx, sqlcgen.InsertChainRuleParams{
			UserID: p.UserID, Name: name, EntryNodeID: in.EntryNodeID, IsEnabled: in.Enabled,
			ExitNodeID:   pgtype.Int8{Int64: in.ExitNodeID, Valid: hasNodeExit},
			ExitCustomID: pgtype.Int8{Int64: in.ExitCustomNodeID, Valid: hasCustomExit},
		})
		if isUniqueViolation(err) {
			return newError(CodeConflict, msgChainDuplicate)
		}
		if err != nil {
			return fmt.Errorf("insert chain rule: %w", err)
		}
		created, err = getRule(ctx, q, p.UserID, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// checkChainExit 校验落地：平台节点须当前可用、不同于入口、不是 QUIC 协议；自有落地节点须属于本人（R8）。
func checkChainExit(ctx context.Context, q *sqlcgen.Queries, userID int64, in ChainRuleInput, available map[int64]sqlcgen.ListUserSubNodesRow) error {
	if in.ExitCustomNodeID > 0 {
		_, err := getCustomNode(ctx, q, userID, in.ExitCustomNodeID)
		return err
	}
	exit, ok := available[in.ExitNodeID]
	if !ok {
		return newError(CodeInvalidArgument, msgChainExitGone)
	}
	if in.ExitNodeID == in.EntryNodeID {
		return newError(CodeInvalidArgument, msgChainSameNode)
	}
	if isQUICProtocol(exit.Protocol) {
		return newError(CodeInvalidArgument, msgChainExitQUIC)
	}
	return nil
}

// UpdateRule 改规则的名称与启用开关；套餐没开链式时拒绝，他人的规则返回 404（R8）。
func (s *ChainService) UpdateRule(ctx context.Context, p *UserPrincipal, id int64, name string, enabled bool) (*ChainRule, error) {
	name, err := validChainName(name)
	if err != nil {
		return nil, err
	}
	var updated *ChainRule
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		flags, err := chainFlags(ctx, q, p.UserID)
		if err != nil {
			return err
		}
		if !flags.Chain {
			return newError(CodeFeatureDisabled, msgChainNotAllowed)
		}
		n, err := q.UpdateChainRule(ctx, sqlcgen.UpdateChainRuleParams{ID: id, UserID: p.UserID, Name: name, IsEnabled: enabled})
		if err != nil {
			return fmt.Errorf("update chain rule %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgChainRuleNotFound)
		}
		updated, err = getRule(ctx, q, p.UserID, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// DeleteRule 删除规则；套餐没开链式时也允许。他人的规则返回 404（R8）。
func (s *ChainService) DeleteRule(ctx context.Context, p *UserPrincipal, id int64) error {
	n, err := s.q.DeleteChainRule(ctx, sqlcgen.DeleteChainRuleParams{ID: id, UserID: p.UserID})
	if err != nil {
		return fmt.Errorf("delete chain rule %d: %w", id, err)
	}
	if n == 0 {
		return newError(CodeNotFound, msgChainRuleNotFound)
	}
	return nil
}

// parsedCustomLink 是一条校验过的自有落地节点链接。
type parsedCustomLink struct {
	link string
	node render.ProxyNode
}

// parseCustomLink 校验并解析自有落地节点的链接；错误信息不回显链接内容（含凭据）。
func parseCustomLink(link string) (parsedCustomLink, error) {
	link = strings.TrimSpace(link)
	if n := utf8.RuneCountInString(link); n < 1 || n > maxCustomNodeLinkLen {
		return parsedCustomLink{}, newError(CodeInvalidArgument, msgCustomNodeLinkLen)
	}
	node, err := render.ParseLink(link)
	if err != nil {
		return parsedCustomLink{}, newError(CodeInvalidArgument, msgCustomNodeBadLink)
	}
	switch node.Protocol {
	case render.ProtoVLESS, render.ProtoVMess, render.ProtoTrojan, render.ProtoShadowsocks, render.ProtoSocks:
		return parsedCustomLink{link: link, node: node}, nil
	default:
		return parsedCustomLink{}, newError(CodeInvalidArgument, msgCustomNodeBadLink)
	}
}

// customNodeName 定名称：给了就用给的，否则取链接里的名称（过长截断），再没有就用“协议-地址”。
func customNodeName(given string, node render.ProxyNode) (string, error) {
	name, err := validChainName(given)
	if err != nil {
		return "", err
	}
	if name != "" {
		return name, nil
	}
	fromLink := strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, node.Name))
	if fromLink != "" {
		return truncate(fromLink, maxChainNameLen), nil
	}
	return truncate(fmt.Sprintf("%s-%s", node.Protocol, node.Address), maxChainNameLen), nil
}

// sealCustomLink 加密链接并连同解析出的展示字段写回。
func (s *ChainService) sealCustomLink(ctx context.Context, q *sqlcgen.Queries, userID, id int64, parsed parsedCustomLink) error {
	sealed, err := s.sealer.Seal([]byte(parsed.link), customNodeAAD(id))
	if err != nil {
		return fmt.Errorf("seal link of custom node %d: %w", id, err)
	}
	err = q.SetCustomNodeLink(ctx, sqlcgen.SetCustomNodeLinkParams{
		ID: id, UserID: userID, LinkEnc: sealed, Protocol: parsed.node.Protocol, Address: parsed.node.Address, Port: int32(parsed.node.Port),
	})
	if err != nil {
		return fmt.Errorf("save link of custom node %d: %w", id, err)
	}
	return nil
}

// CreateCustomNode 添加一个自有落地节点（docs/03 §5.18 自有落地节点）。平台不向该地址发起任何连接（Ruling-38）。
func (s *ChainService) CreateCustomNode(ctx context.Context, p *UserPrincipal, link, name string) (*CustomNode, error) {
	parsed, err := parseCustomLink(link)
	if err != nil {
		return nil, err
	}
	name, err = customNodeName(name, parsed.node)
	if err != nil {
		return nil, err
	}
	var created *CustomNode
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := lockChainOwner(ctx, q, p.UserID); err != nil {
			return err
		}
		flags, err := chainFlags(ctx, q, p.UserID)
		if err != nil {
			return err
		}
		if !flags.CustomNode {
			return newError(CodeFeatureDisabled, msgCustomNodeNotAllowed)
		}
		count, err := q.CountCustomNodes(ctx, p.UserID)
		if err != nil {
			return fmt.Errorf("count custom nodes of user %d: %w", p.UserID, err)
		}
		if count >= MaxCustomNodes {
			return newError(CodeInvalidArgument, msgCustomNodeTooMany)
		}
		id, err := q.InsertCustomNode(ctx, sqlcgen.InsertCustomNodeParams{
			UserID: p.UserID, Name: name, Protocol: parsed.node.Protocol, Address: parsed.node.Address, Port: int32(parsed.node.Port),
		})
		if isUniqueViolation(err) {
			return newError(CodeConflict, msgCustomNodeDuplicate)
		}
		if err != nil {
			return fmt.Errorf("insert custom node: %w", err)
		}
		if err := s.sealCustomLink(ctx, q, p.UserID, id, parsed); err != nil {
			return err
		}
		created, err = getCustomNode(ctx, q, p.UserID, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// UpdateCustomNode 改名称，link 非空时一并换链接；他人的返回 404（R8）。
func (s *ChainService) UpdateCustomNode(ctx context.Context, p *UserPrincipal, id int64, name, link string) (*CustomNode, error) {
	name, err := validChainName(name)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, newError(CodeInvalidArgument, msgCustomNodeNameEmpty)
	}
	var parsed *parsedCustomLink
	if strings.TrimSpace(link) != "" {
		pl, err := parseCustomLink(link)
		if err != nil {
			return nil, err
		}
		parsed = &pl
	}
	var updated *CustomNode
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		flags, err := chainFlags(ctx, q, p.UserID)
		if err != nil {
			return err
		}
		if !flags.CustomNode {
			return newError(CodeFeatureDisabled, msgCustomNodeNotAllowed)
		}
		n, err := q.RenameCustomNode(ctx, sqlcgen.RenameCustomNodeParams{ID: id, UserID: p.UserID, Name: name})
		if isUniqueViolation(err) {
			return newError(CodeConflict, msgCustomNodeDuplicate)
		}
		if err != nil {
			return fmt.Errorf("rename custom node %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgCustomNodeNotFound)
		}
		if parsed != nil {
			if err := s.sealCustomLink(ctx, q, p.UserID, id, *parsed); err != nil {
				return err
			}
		}
		updated, err = getCustomNode(ctx, q, p.UserID, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// DeleteCustomNode 删除自有落地节点，引用它的规则随外键一并删除；他人的返回 404（R8）。
func (s *ChainService) DeleteCustomNode(ctx context.Context, p *UserPrincipal, id int64) error {
	n, err := s.q.DeleteCustomNode(ctx, sqlcgen.DeleteCustomNodeParams{ID: id, UserID: p.UserID})
	if err != nil {
		return fmt.Errorf("delete custom node %d: %w", id, err)
	}
	if n == 0 {
		return newError(CodeNotFound, msgCustomNodeNotFound)
	}
	return nil
}

// CustomNodeLink 返回自有落地节点的原链接（凭据，不得写日志）；他人的返回 404（R8）。
func (s *ChainService) CustomNodeLink(ctx context.Context, p *UserPrincipal, id int64) (string, error) {
	sealed, err := s.q.GetCustomNodeLink(ctx, sqlcgen.GetCustomNodeLinkParams{ID: id, UserID: p.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", newError(CodeNotFound, msgCustomNodeNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("get link of custom node %d: %w", id, err)
	}
	plain, err := s.sealer.Open(sealed, customNodeAAD(id))
	if err != nil {
		return "", fmt.Errorf("open link of custom node %d: %w", id, err)
	}
	return string(plain), nil
}
