package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/chain"
	"v2-hub-server/internal/manager/crypto"
	"v2-hub-server/internal/manager/ratelimit"
	"v2-hub-server/internal/manager/render"
)

// 链式规则（docs/03 §5.18、§5.19，Ruling-46）：一组起飞节点 → 一个落地，有顺序。客户端链式只在订阅里表达（Ruling-36），
// 服务端链式由 chain_apply.go 下发到起飞面板（Ruling-39）。落地是平台节点、平台出口或用户出口（Ruling-47，出口见 chain_exit.go）。
// 平台不探测连通性（Ruling-38）。

const (
	// 每个用户的数量上限（docs/03 §5.18、§5.19、§5.21）。
	MaxClientRoutes = 20
	MaxServerRoutes = 10
	MaxChainExits   = 10
	// 起飞节点不设业务上限（Ruling-87）：都须是本人可用的节点且不重复，个数自然不超过可用节点数；这里只防超大请求。
	MaxRouteEntries = 1000

	maxChainNameLen = 64
	maxExitLinkLen  = 2048
	// 基于 QUIC 的协议不能作落地：经代理拨出不可靠。面板里的协议名是 hysteria / hysteria2。
	quicProtocolPrefix = "hysteria"

	// 出口链接密文的关联数据；沿用 M6 自有落地节点的写法，已有密文不用重加密（docs/03 §5.21 存储）。
	exitLinkAADFormat = "custom_node:%d:link"

	// 每个用户对服务端规则的写操作频率（docs/03 §5.19 频控，Ruling-49）；每次改动都会让起飞服务器重启内核。
	defaultServerWriteLimit  = 10
	defaultServerWriteWindow = time.Hour
	chainGuardMaxKeys        = 10000
	// 解析平台自己的地址（防环）的总时限
	platformLookupTimeout = 5 * time.Second

	msgChainNotAllowed       = "当前套餐不支持链式代理"
	msgServerChainNotAllowed = "当前套餐不支持服务端链式"
	msgExitNotAllowed        = "当前套餐不支持添加自己的出口"
	msgChainRouteNotFound    = "链式规则不存在"
	msgChainBadKind          = "规则类型须为 client 或 server"
	msgChainBadName          = "名称不能超过 64 个字符，且不能含换行等控制字符"
	msgChainBadEntries       = "起飞节点至少选 1 个，且不能重复"
	msgChainBadExit          = "落地须在平台节点、平台出口与我的出口里选一个"
	msgChainEntryGone        = "有起飞节点不存在或当前不可用"
	msgChainExitGone         = "落地不存在或当前不可用"
	msgChainSameNode         = "落地节点不能同时是这条规则的起飞节点"
	msgChainExitQUIC         = "hysteria2 节点不能作落地"
	msgChainExitKind         = "这个平台出口不能用于这类链式"
	msgChainExitDisabled     = "这个出口已被管理员停用"
	msgChainTooMany          = "链式规则已达上限"
	msgChainNoCore           = "有起飞节点所在的面板不支持服务端链式"
	msgChainLoop             = "服务端链式里，落地节点不能同时是起飞节点（会形成环路）"
	msgChainBadOrder         = "顺序须恰好包含这一类型的全部规则"
	msgChainRateLimited      = "服务端链式改动太频繁，请稍后再试"
	msgChainExitChanged      = "出口刚被修改，请重试"
)

// ChainFlags 是当前套餐的链式开关。
type ChainFlags struct {
	Chain       bool
	CustomNode  bool
	ServerChain bool
}

// ChainNodeOption 是可选的平台节点。
type ChainNodeOption struct {
	ID               int64
	Name             string
	Region           string
	Protocol         string
	CanBeExit        bool
	CanBeServerEntry bool
}

// ChainPlatformExitOption 是本人可用的平台出口；地址与链接不对用户返回。
type ChainPlatformExitOption struct {
	ID          int64
	Name        string
	Protocol    string
	AllowServer bool
	AllowClient bool
}

// ChainOverview 是链式代理页需要的全部数据。
type ChainOverview struct {
	Flags         ChainFlags
	ClientRoutes  []ChainRoute
	ServerRoutes  []ChainRoute
	Exits         []ChainExit
	PlatformExits []ChainPlatformExitOption
	Nodes         []ChainNodeOption
}

// ChainRouteInput 是新建或修改规则的输入；修改时 Kind 被忽略（类型不能改）。
type ChainRouteInput struct {
	Kind         string
	EntryNodeIDs []int64
	ExitKind     string
	ExitID       int64
	Name         string
	Enabled      bool
}

// ChainOptions 是链式服务的可选项；零值取默认。
type ChainOptions struct {
	// Resolver 解析用户出口的域名；nil 用系统解析器。
	Resolver Resolver
	// ServerWriteLimit / ServerWriteWindow 是每个用户对服务端规则的写操作频率。
	ServerWriteLimit  int
	ServerWriteWindow time.Duration
	// ExitTestLimit / ExitTestWindow 是每个用户发起出口延迟测试的频率（Ruling-52）。
	ExitTestLimit  int
	ExitTestWindow time.Duration
	// Now 取当前时间；nil 用系统时间。
	Now func() time.Time
}

// ChainService 负责链式规则与出口。
type ChainService struct {
	store
	sealer       *crypto.Sealer
	applier      *ChainApplier
	guard        *chainGuard
	serverWrites *ratelimit.FailureGuard
	exitTests    *ratelimit.FailureGuard
	testing      *exitTestLocks
	now          func() time.Time
}

// NewChainService 创建链式服务；sealer 用于加密出口的链接；applier 负责把服务端规则下发到面板，
// 为 nil 时任何节点都不能作服务端链式的起飞节点。
func NewChainService(pool *pgxpool.Pool, sealer *crypto.Sealer, applier *ChainApplier, opts ChainOptions) *ChainService {
	if opts.ServerWriteLimit <= 0 {
		opts.ServerWriteLimit = defaultServerWriteLimit
	}
	if opts.ServerWriteWindow <= 0 {
		opts.ServerWriteWindow = defaultServerWriteWindow
	}
	if opts.ExitTestLimit <= 0 {
		opts.ExitTestLimit = defaultExitTestLimit
	}
	if opts.ExitTestWindow <= 0 {
		opts.ExitTestWindow = defaultExitTestWindow
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	tests := ratelimit.NewFailureGuard(ratelimit.FailureGuardConfig{
		MaxFailures: opts.ExitTestLimit, Window: opts.ExitTestWindow, Lockout: opts.ExitTestWindow, MaxKeys: chainGuardMaxKeys,
	}, opts.Now)
	writes := ratelimit.NewFailureGuard(ratelimit.FailureGuardConfig{
		MaxFailures: opts.ServerWriteLimit, Window: opts.ServerWriteWindow, Lockout: opts.ServerWriteWindow, MaxKeys: chainGuardMaxKeys,
	}, opts.Now)
	return &ChainService{
		store: newStore(pool), sealer: sealer, applier: applier, guard: newChainGuard(opts.Resolver), serverWrites: writes,
		exitTests: tests, testing: newExitTestLocks(), now: opts.Now,
	}
}

func exitLinkAAD(id int64) []byte {
	return fmt.Appendf(nil, exitLinkAADFormat, id)
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
	return rows, nodesByID(rows), nil
}

// nodesByID 把可用节点按 id 索引。
func nodesByID(rows []sqlcgen.ListUserSubNodesRow) map[int64]sqlcgen.ListUserSubNodesRow {
	byID := make(map[int64]sqlcgen.ListUserSubNodesRow, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	return byID
}

// coreConfigServers 返回各服务器的面板是否支持改内核配置；applier 为 nil 时都不支持。
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

// applyViews 读取这些起飞服务器的下发记录。
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
		out[r.ServerID] = applyView{status: r.Status, applied: r.AppliedRuleIds, appliedAt: r.AppliedAt.Time}
	}
	return out, nil
}

// exitSecret 是一个出口解密并解析后的参数，连同它固定下来的地址。
type exitSecret struct {
	ownerID    int64
	linkEnc    []byte
	node       render.ProxyNode
	isParsed   bool
	pinned     *netip.Addr
	resolvedAt time.Time
	enabled    bool
}

// loadExitSecrets 解密并解析这些出口；解不开或解析不了的标为未解析并留痕，不让一个坏出口拖垮整体。节点名用出口的名称。
func loadExitSecrets(ctx context.Context, q *sqlcgen.Queries, sealer *crypto.Sealer, ids []int64) (map[int64]exitSecret, error) {
	out := make(map[int64]exitSecret, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.ListChainExitSecrets(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list chain exit secrets: %w", err)
	}
	for _, r := range rows {
		sec := exitSecret{ownerID: r.OwnerID, linkEnc: r.LinkEnc, pinned: r.ResolvedIp, resolvedAt: r.ResolvedAt.Time, enabled: r.IsEnabled}
		plain, err := sealer.Open(r.LinkEnc, exitLinkAAD(r.ID))
		if err != nil {
			slog.Error("open link of chain exit", "exit_id", r.ID, "err", err)
			out[r.ID] = sec
			continue
		}
		node, err := render.ParseLink(string(plain))
		if err != nil {
			slog.Warn("skip unparsable chain exit link", "exit_id", r.ID)
			out[r.ID] = sec
			continue
		}
		node.Name = r.Name
		sec.node, sec.isParsed = node, true
		out[r.ID] = sec
	}
	return out, nil
}

// isUnsafeForServer 报告一个用户出口是否不能用于服务端链式：参数不合要求，或没有固定到公网地址（Ruling-48）。
func (e exitSecret) isUnsafeForServer() bool {
	if !e.isParsed || validateExitFields(e.node) != nil {
		return true
	}
	return e.pinned == nil || !isPublicAddr(*e.pinned)
}

// serverNode 返回出口写进起飞面板时用的参数：用户出口的地址换成固定的 IP；平台出口原样。
func (e exitSecret) serverNode() (render.ProxyNode, bool) {
	if !e.isParsed {
		return render.ProxyNode{}, false
	}
	isOwn := e.ownerID != 0
	if !isOwn {
		return e.node, true
	}
	if e.isUnsafeForServer() {
		return render.ProxyNode{}, false
	}
	return pinnedExit(e.node, *e.pinned), true
}

// routeFingerprint 是一条服务端规则落地内容的指纹（出站配置的 SHA-256），失败隔离用它判断内容有没有变（docs/03 §5.19）。
// 生成不了出站时返回空串。
func routeFingerprint(routeID int64, e exitSecret) string {
	node, ok := e.serverNode()
	if !ok {
		return ""
	}
	outbound, ok := render.XrayOutbound(node, chain.Tag(routeID))
	if !ok {
		return ""
	}
	raw, err := json.Marshal(outbound)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// chainState 是一个用户的全部规则（已算好状态）与算它们用到的数据。
type chainState struct {
	flags         ChainFlags
	nodes         []sqlcgen.ListUserSubNodesRow
	available     map[int64]sqlcgen.ListUserSubNodesRow
	platformExits []sqlcgen.ListUserPlatformExitsRow
	routes        []ChainRoute
	exits         map[int64]exitSecret
}

// routesOf 返回某一类型的规则，按顺序。
func (st *chainState) routesOf(kind string) []ChainRoute {
	out := make([]ChainRoute, 0, len(st.routes))
	for _, r := range st.routes {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

func (st *chainState) route(id int64) (*ChainRoute, bool) {
	for i := range st.routes {
		if st.routes[i].ID == id {
			return &st.routes[i], true
		}
	}
	return nil, false
}

// chainRouteFromRow 组装一条规则（不含起飞节点与状态）。
func chainRouteFromRow(r sqlcgen.ListChainRoutesRow) ChainRoute {
	route := ChainRoute{
		ID: r.ID, UserID: r.UserID, Kind: r.Kind, Name: r.Name, Enabled: r.IsEnabled, CreatedAt: r.CreatedAt.Time,
		Exit:           ChainRouteExit{Kind: ChainExitNode, ID: r.ExitNodeID, Name: r.ExitNodeName, Protocol: r.ExitNodeProtocol},
		quarantineHash: r.QuarantineHash, changedAt: r.UpdatedAt.Time,
	}
	if r.ExitID != 0 {
		kind := ChainExitPlatform
		if r.ExitOwnerID != 0 {
			kind = ChainExitOwn
		}
		route.Exit = ChainRouteExit{Kind: kind, ID: r.ExitID, Name: r.ExitName, Protocol: r.ExitProtocol}
		route.exitEnabled, route.exitAllowServer, route.exitAllowClient = r.ExitEnabled, r.ExitAllowServer, r.ExitAllowClient
		if exitChangedAt := r.ExitUpdatedAt.Time; exitChangedAt.Compare(route.changedAt) > 0 {
			route.changedAt = exitChangedAt
		}
	}
	route.DisplayName = route.Name
	if route.DisplayName == "" {
		route.DisplayName = route.Exit.Name
	}
	return route
}

// loadChainBase 只读校验规则用到的部分：链式开关、可用节点与被授权的平台出口，不读规则本身（routes、exits 为空）。
func loadChainBase(ctx context.Context, q *sqlcgen.Queries, userID int64) (*chainState, error) {
	return loadChainBaseWithNodes(ctx, q, userID, nil, false)
}

// loadChainBaseWithNodes 同 loadChainBase；hasNodes 为 true 时用调用方传入的可用节点（刚用 ListUserSubNodes 查过，免得重查）。
func loadChainBaseWithNodes(ctx context.Context, q *sqlcgen.Queries, userID int64, nodes []sqlcgen.ListUserSubNodesRow, hasNodes bool) (*chainState, error) {
	st := &chainState{}
	var err error
	if st.flags, err = chainFlags(ctx, q, userID); err != nil {
		return nil, err
	}
	if hasNodes {
		st.nodes, st.available = nodes, nodesByID(nodes)
	} else if st.nodes, st.available, err = availableNodes(ctx, q, userID); err != nil {
		return nil, err
	}
	if st.platformExits, err = q.ListUserPlatformExits(ctx, userID); err != nil {
		return nil, fmt.Errorf("list platform exits of user %d: %w", userID, err)
	}
	return st, nil
}

// loadChainState 读一个用户的全部规则并算好状态。coreServers 为 nil 时服务端规则的起飞节点一律算不可用、
// 阻断原因不可信（只关心客户端规则的调用方，如订阅渲染）。
func loadChainState(ctx context.Context, q *sqlcgen.Queries, sealer *crypto.Sealer, coreServers map[int64]bool, userID int64) (*chainState, error) {
	st, err := loadChainBase(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	return loadChainRoutes(ctx, q, sealer, coreServers, userID, st)
}

// loadClientChainState 读一个用户的全部规则，只保证客户端规则的状态准确：服务端规则的起飞节点一律算不可用、
// 阻断原因不可信（见 loadChainRoutes）。nodes 是调用方刚用 ListUserSubNodes 查到的可用节点。
func loadClientChainState(ctx context.Context, q *sqlcgen.Queries, sealer *crypto.Sealer, userID int64, nodes []sqlcgen.ListUserSubNodesRow) (*chainState, error) {
	st, err := loadChainBaseWithNodes(ctx, q, userID, nodes, true)
	if err != nil {
		return nil, err
	}
	return loadChainRoutes(ctx, q, sealer, nil, userID, st)
}

// loadChainRoutes 在 st（已有 loadChainBase 的部分）上读规则与出口并算好状态。coreServers 为 nil 时跳过服务端规则的
// 出口安全检查、内容指纹与下发记录：此时服务端起飞节点本就一律不可用，客户端规则的计算不依赖这几项。
func loadChainRoutes(ctx context.Context, q *sqlcgen.Queries, sealer *crypto.Sealer, coreServers map[int64]bool, userID int64, st *chainState) (*chainState, error) {
	userIDs := []int64{userID}
	rows, err := q.ListChainRoutes(ctx, userIDs)
	if err != nil {
		return nil, fmt.Errorf("list chain routes of user %d: %w", userID, err)
	}
	entryRows, err := q.ListChainRouteEntries(ctx, userIDs)
	if err != nil {
		return nil, fmt.Errorf("list chain route entries of user %d: %w", userID, err)
	}
	entries := make(map[int64][]ChainRouteEntry, len(rows))
	serverIDs := make([]int64, 0, len(entryRows))
	for _, e := range entryRows {
		entries[e.RouteID] = append(entries[e.RouteID], ChainRouteEntry{
			NodeID: e.EntryNodeID, Name: e.DisplayName, InboundID: e.InboundID, InboundTag: e.InboundTag, ServerID: e.ServerID, ServerName: e.ServerName,
		})
		serverIDs = append(serverIDs, e.ServerID)
	}
	st.routes = make([]ChainRoute, 0, len(rows))
	exitIDs := make([]int64, 0, len(rows))
	for _, r := range rows {
		route := chainRouteFromRow(r)
		route.Entries = entries[r.ID]
		st.routes = append(st.routes, route)
		if r.ExitID != 0 {
			exitIDs = append(exitIDs, r.ExitID)
		}
	}
	if st.exits, err = loadExitSecrets(ctx, q, sealer, exitIDs); err != nil {
		return nil, err
	}
	env := chainEnv{
		flags: st.flags, available: st.available, coreServers: coreServers,
		platformExits: make(map[int64]bool, len(st.platformExits)), unsafeExits: map[int64]bool{}, fingerprints: map[int64]string{},
	}
	for _, x := range st.platformExits {
		env.platformExits[x.ID] = true
	}
	if coreServers == nil {
		resolveRoutes(st.routes, env)
		return st, nil
	}
	for _, r := range st.routes {
		isOwnServer := r.Kind == ChainKindServer && r.Exit.Kind == ChainExitOwn
		if !isOwnServer {
			continue
		}
		sec := st.exits[r.Exit.ID]
		if sec.isUnsafeForServer() {
			env.unsafeExits[r.Exit.ID] = true
			continue
		}
		env.fingerprints[r.ID] = routeFingerprint(r.ID, sec)
	}
	if env.views, err = applyViews(ctx, q, serverIDs); err != nil {
		return nil, err
	}
	resolveRoutes(st.routes, env)
	return st, nil
}

// Features 返回用户当前套餐的链式开关，供会话信息决定菜单显隐。
func (s *ChainService) Features(ctx context.Context, userID int64) (ChainFlags, error) {
	return chainFlags(ctx, s.q, userID)
}

func (s *ChainService) loadState(ctx context.Context, q *sqlcgen.Queries, userID int64) (*chainState, map[int64]bool, error) {
	coreServers, err := coreConfigServers(ctx, q, s.applier)
	if err != nil {
		return nil, nil, err
	}
	st, err := loadChainState(ctx, q, s.sealer, coreServers, userID)
	if err != nil {
		return nil, nil, err
	}
	return st, coreServers, nil
}

// Overview 返回链式代理页的全部数据；套餐没开时照常返回，规则状态为阻断。
func (s *ChainService) Overview(ctx context.Context, p *UserPrincipal) (*ChainOverview, error) {
	st, coreServers, err := s.loadState(ctx, s.q, p.UserID)
	if err != nil {
		return nil, err
	}
	exits, err := listChainExits(ctx, s.q, p.UserID, 0)
	if err != nil {
		return nil, err
	}
	options := make([]ChainNodeOption, 0, len(st.nodes))
	for _, r := range st.nodes {
		options = append(options, ChainNodeOption{
			ID: r.ID, Name: r.DisplayName, Region: r.Region, Protocol: r.Protocol,
			CanBeExit: !isQUICProtocol(r.Protocol), CanBeServerEntry: coreServers[r.ServerID],
		})
	}
	platform := make([]ChainPlatformExitOption, 0, len(st.platformExits))
	for _, x := range st.platformExits {
		platform = append(platform, ChainPlatformExitOption{ID: x.ID, Name: x.Name, Protocol: x.Protocol, AllowServer: x.AllowServer, AllowClient: x.AllowClient})
	}
	return &ChainOverview{
		Flags: st.flags, ClientRoutes: st.routesOf(ChainKindClient), ServerRoutes: st.routesOf(ChainKindServer),
		Exits: exits, PlatformExits: platform, Nodes: options,
	}, nil
}

// validChainName 校验名称：去首尾空白后不超过上限、不含控制字符（名称会原样写进客户端配置）。
func validChainName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > maxChainNameLen || strings.ContainsFunc(name, unicode.IsControl) {
		return "", newError(CodeInvalidArgument, msgChainBadName)
	}
	return name, nil
}

// lockChainOwner 锁住用户行，让同一用户的规则与出口改动串行（数量上限、顺序、防环都依赖它）。
func lockChainOwner(ctx context.Context, q *sqlcgen.Queries, userID int64) error {
	_, err := lockUser(ctx, q, userID)
	return err
}

// kickApplier 让下发任务尽快检查一轮（服务端规则或它们的出口变化后调用）。
func (s *ChainService) kickApplier() {
	if s.applier != nil {
		s.applier.Kick()
	}
}

// allowServerWrite 给服务端规则的写操作计数；超出频率返回 rate_limited（docs/03 §5.19 频控）。
func (s *ChainService) allowServerWrite(userID int64) error {
	key := strconv.FormatInt(userID, 10)
	if s.serverWrites.IsLocked(key) {
		return newError(CodeRateLimited, msgChainRateLimited)
	}
	s.serverWrites.RecordFailure(key)
	return nil
}

// normalizeRouteInput 校验与请求本身有关的部分：类型、起飞节点个数与去重、落地种类、名称。
func normalizeRouteInput(in ChainRouteInput) (ChainRouteInput, error) {
	if in.Kind != ChainKindClient && in.Kind != ChainKindServer {
		return in, newError(CodeInvalidArgument, msgChainBadKind)
	}
	name, err := validChainName(in.Name)
	if err != nil {
		return in, err
	}
	in.Name = name
	ids := slices.Clone(in.EntryNodeIDs)
	slices.Sort(ids)
	isBadEntries := len(ids) == 0 || len(ids) > MaxRouteEntries || ids[0] <= 0 || len(slices.Compact(slices.Clone(ids))) != len(ids)
	if isBadEntries {
		return in, newError(CodeInvalidArgument, msgChainBadEntries)
	}
	in.EntryNodeIDs = ids
	isKnownExit := in.ExitKind == ChainExitNode || in.ExitKind == ChainExitPlatform || in.ExitKind == ChainExitOwn
	if !isKnownExit || in.ExitID <= 0 {
		return in, newError(CodeInvalidArgument, msgChainBadExit)
	}
	return in, nil
}

// ownExitPin 是事务外校验好的用户出口：当时的链接密文与固定下来的地址。
type ownExitPin struct {
	linkEnc []byte
	ip      netip.Addr
}

// checkOwnExitForServer 校验一个用户出口能否用于服务端链式并固定它的地址（docs/03 §5.19 用户出口的地址限制、防环）。
// 要做 DNS 查询，所以在事务外调用；返回的结果由 storeOwnExitPin 在事务内落库。
func (s *ChainService) checkOwnExitForServer(ctx context.Context, userID, exitID int64) (*ownExitPin, error) {
	secrets, err := loadExitSecrets(ctx, s.q, s.sealer, []int64{exitID})
	if err != nil {
		return nil, err
	}
	sec, ok := secrets[exitID]
	if !ok || sec.ownerID != userID {
		return nil, newError(CodeInvalidArgument, msgChainExitGone)
	}
	if !sec.isParsed {
		return nil, newError(CodeInvalidArgument, msgExitUnsupported)
	}
	ip, err := s.pinExitNode(ctx, sec.node, sec.pinned)
	if err != nil {
		return nil, err
	}
	return &ownExitPin{linkEnc: sec.linkEnc, ip: ip}, nil
}

// pinExitNode 校验出口参数并把地址固定成公网 IP；指向平台自己服务器的拒绝。
func (s *ChainService) pinExitNode(ctx context.Context, node render.ProxyNode, previous *netip.Addr) (netip.Addr, error) {
	if err := validateExitFields(node); err != nil {
		return netip.Addr{}, err
	}
	ip, err := s.guard.pinOffPlatform(ctx, s.q, node.Address, previous, nil)
	switch {
	case err == nil:
		return ip, nil
	case errors.Is(err, errExitPlatform):
		return netip.Addr{}, newError(CodeInvalidArgument, msgExitPlatform)
	case errors.Is(err, errExitResolve), errors.Is(err, errExitPrivate):
		return netip.Addr{}, guardError(err)
	default:
		return netip.Addr{}, err
	}
}

// storeOwnExitPin 在事务内落库固定的地址；出口在校验之后被改过链接则让用户重试。
func (s *ChainService) storeOwnExitPin(ctx context.Context, q *sqlcgen.Queries, userID, exitID int64, pin *ownExitPin) error {
	rows, err := q.ListChainExitSecrets(ctx, []int64{exitID})
	if err != nil {
		return fmt.Errorf("reload chain exit %d: %w", exitID, err)
	}
	if len(rows) == 0 || rows[0].OwnerID != userID {
		return newError(CodeInvalidArgument, msgChainExitGone)
	}
	if !bytes.Equal(rows[0].LinkEnc, pin.linkEnc) {
		return newError(CodeConflict, msgChainExitChanged)
	}
	err = q.SetChainExitResolved(ctx, sqlcgen.SetChainExitResolvedParams{ID: exitID, ResolvedIp: &pin.ip, ResolvedAt: timestamptz(s.now())})
	if err != nil {
		return fmt.Errorf("pin chain exit %d: %w", exitID, err)
	}
	return nil
}

// checkRouteTarget 在事务内校验起飞节点与落地（授权、协议、防环）。selfID 是正在修改的规则 id，新建时为 0。
// 只用到开关、可用节点与平台出口，所以不读规则（规则由随后的 reloadRoute 读）。
func (s *ChainService) checkRouteTarget(ctx context.Context, q *sqlcgen.Queries, userID, selfID int64, in ChainRouteInput) error {
	coreServers, err := coreConfigServers(ctx, q, s.applier)
	if err != nil {
		return err
	}
	st, err := loadChainBase(ctx, q, userID)
	if err != nil {
		return err
	}
	if !st.flags.isKindAllowed(in.Kind) {
		if in.Kind == ChainKindServer {
			return newError(CodeFeatureDisabled, msgServerChainNotAllowed)
		}
		return newError(CodeFeatureDisabled, msgChainNotAllowed)
	}
	isServer := in.Kind == ChainKindServer
	for _, id := range in.EntryNodeIDs {
		entry, ok := st.available[id]
		if !ok {
			return newError(CodeInvalidArgument, msgChainEntryGone)
		}
		if isServer && !coreServers[entry.ServerID] {
			return newError(CodeInvalidArgument, msgChainNoCore)
		}
	}
	if err := checkRouteExit(ctx, q, st, userID, in); err != nil {
		return err
	}
	if !isServer {
		return nil
	}
	return checkServerLoop(ctx, q, userID, selfID, in)
}

// checkRouteExit 校验落地在本人权限范围内（R8）：平台节点须当前可用且不是 QUIC；平台出口须已授权且允许这类链式；
// 用户出口须属于本人、套餐允许、没被停用。不存在与无权访问给同一个说明，不泄露别人的出口是否存在。
func checkRouteExit(ctx context.Context, q *sqlcgen.Queries, st *chainState, userID int64, in ChainRouteInput) error {
	isServer := in.Kind == ChainKindServer
	switch in.ExitKind {
	case ChainExitNode:
		exit, ok := st.available[in.ExitID]
		if !ok {
			return newError(CodeInvalidArgument, msgChainExitGone)
		}
		if slices.Contains(in.EntryNodeIDs, in.ExitID) {
			return newError(CodeInvalidArgument, msgChainSameNode)
		}
		if isQUICProtocol(exit.Protocol) {
			return newError(CodeInvalidArgument, msgChainExitQUIC)
		}
		return nil
	case ChainExitPlatform:
		i := slices.IndexFunc(st.platformExits, func(x sqlcgen.ListUserPlatformExitsRow) bool { return x.ID == in.ExitID })
		if i < 0 {
			return newError(CodeInvalidArgument, msgChainExitGone)
		}
		x := st.platformExits[i]
		isKindOK := (isServer && x.AllowServer) || (!isServer && x.AllowClient)
		if !isKindOK {
			return newError(CodeInvalidArgument, msgChainExitKind)
		}
		return nil
	default:
		if !st.flags.CustomNode {
			return newError(CodeFeatureDisabled, msgExitNotAllowed)
		}
		exits, err := listChainExits(ctx, q, userID, in.ExitID)
		if err != nil {
			return err
		}
		if len(exits) == 0 {
			return newError(CodeInvalidArgument, msgChainExitGone)
		}
		if !exits[0].Enabled {
			return newError(CodeInvalidArgument, msgChainExitDisabled)
		}
		return nil
	}
}

// checkServerLoop 防环（Ruling-49）：服务端链式里，落地节点不能是本人任何服务端规则的起飞节点，起飞节点也不能是任何规则的落地。
func checkServerLoop(ctx context.Context, q *sqlcgen.Queries, userID, selfID int64, in ChainRouteInput) error {
	roles, err := q.ListServerRouteNodeRoles(ctx, userID)
	if err != nil {
		return fmt.Errorf("list server route node roles of user %d: %w", userID, err)
	}
	entries := make(map[int64]bool, len(roles)+len(in.EntryNodeIDs))
	exits := make(map[int64]bool, len(roles)+1)
	for _, r := range roles {
		if r.RouteID == selfID {
			continue
		}
		if r.IsEntry {
			entries[r.NodeID] = true
			continue
		}
		exits[r.NodeID] = true
	}
	for _, id := range in.EntryNodeIDs {
		if exits[id] {
			return newError(CodeInvalidArgument, msgChainLoop)
		}
		entries[id] = true
	}
	if in.ExitKind == ChainExitNode && entries[in.ExitID] {
		return newError(CodeInvalidArgument, msgChainLoop)
	}
	return nil
}

func maxRoutesOf(kind string) int64 {
	if kind == ChainKindServer {
		return MaxServerRoutes
	}
	return MaxClientRoutes
}

// exitRefs 把落地拆成落库用的两个可空列。
func exitRefs(in ChainRouteInput) (pgtype.Int8, pgtype.Int8) {
	isNode := in.ExitKind == ChainExitNode
	return pgtype.Int8{Int64: in.ExitID, Valid: isNode}, pgtype.Int8{Int64: in.ExitID, Valid: !isNode}
}

// prepareServerWrite 是服务端规则写操作在事务外的准备：计频率；落地是用户出口时校验并固定它的地址。
func (s *ChainService) prepareServerWrite(ctx context.Context, userID int64, in ChainRouteInput) (*ownExitPin, error) {
	if err := s.allowServerWrite(userID); err != nil {
		return nil, err
	}
	if in.ExitKind != ChainExitOwn {
		return nil, nil
	}
	return s.checkOwnExitForServer(ctx, userID, in.ExitID)
}

// reloadRoute 在事务内重读一条规则（带最新状态）。
func (s *ChainService) reloadRoute(ctx context.Context, q *sqlcgen.Queries, userID, id int64) (*ChainRoute, error) {
	st, _, err := s.loadState(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	route, ok := st.route(id)
	if !ok {
		return nil, newError(CodeNotFound, msgChainRouteNotFound)
	}
	return route, nil
}

// CreateRoute 新建一条规则，排在同类型的最后（docs/03 §5.18、§5.19）。
func (s *ChainService) CreateRoute(ctx context.Context, p *UserPrincipal, in ChainRouteInput) (*ChainRoute, error) {
	in, err := normalizeRouteInput(in)
	if err != nil {
		return nil, err
	}
	isServer := in.Kind == ChainKindServer
	var pin *ownExitPin
	if isServer {
		if pin, err = s.prepareServerWrite(ctx, p.UserID, in); err != nil {
			return nil, err
		}
	}
	var created *ChainRoute
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := lockChainOwner(ctx, q, p.UserID); err != nil {
			return err
		}
		if err := s.checkRouteTarget(ctx, q, p.UserID, 0, in); err != nil {
			return err
		}
		count, err := q.CountChainRoutes(ctx, sqlcgen.CountChainRoutesParams{UserID: p.UserID, Kind: in.Kind})
		if err != nil {
			return fmt.Errorf("count chain routes of user %d: %w", p.UserID, err)
		}
		if count >= maxRoutesOf(in.Kind) {
			return newError(CodeInvalidArgument, msgChainTooMany)
		}
		if pin != nil {
			if err := s.storeOwnExitPin(ctx, q, p.UserID, in.ExitID, pin); err != nil {
				return err
			}
		}
		exitNodeID, exitID := exitRefs(in)
		id, err := q.InsertChainRoute(ctx, sqlcgen.InsertChainRouteParams{
			UserID: p.UserID, Kind: in.Kind, Name: in.Name, ExitNodeID: exitNodeID, ExitID: exitID, IsEnabled: in.Enabled,
		})
		if err != nil {
			return fmt.Errorf("insert chain route: %w", err)
		}
		if err := q.AddChainRouteEntries(ctx, sqlcgen.AddChainRouteEntriesParams{RouteID: id, NodeIds: in.EntryNodeIDs}); err != nil {
			return fmt.Errorf("insert entries of chain route %d: %w", id, err)
		}
		created, err = s.reloadRoute(ctx, q, p.UserID, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	if isServer {
		s.kickApplier()
	}
	return created, nil
}

// routeKind 返回本人某条规则的类型；不存在或不是本人的返回 404（R8）。
func (s *ChainService) routeKind(ctx context.Context, userID, id int64) (string, error) {
	rows, err := s.q.ListChainRoutes(ctx, []int64{userID})
	if err != nil {
		return "", fmt.Errorf("list chain routes of user %d: %w", userID, err)
	}
	for _, r := range rows {
		if r.ID == id {
			return r.Kind, nil
		}
	}
	return "", newError(CodeNotFound, msgChainRouteNotFound)
}

// UpdateRoute 改规则的起飞节点、落地、名称与启用开关（类型不能改）；套餐没开时拒绝，他人的规则返回 404（R8）。
func (s *ChainService) UpdateRoute(ctx context.Context, p *UserPrincipal, id int64, in ChainRouteInput) (*ChainRoute, error) {
	kind, err := s.routeKind(ctx, p.UserID, id)
	if err != nil {
		return nil, err
	}
	in.Kind = kind
	if in, err = normalizeRouteInput(in); err != nil {
		return nil, err
	}
	isServer := kind == ChainKindServer
	var pin *ownExitPin
	if isServer {
		if pin, err = s.prepareServerWrite(ctx, p.UserID, in); err != nil {
			return nil, err
		}
	}
	var updated *ChainRoute
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := lockChainOwner(ctx, q, p.UserID); err != nil {
			return err
		}
		if err := s.checkRouteTarget(ctx, q, p.UserID, id, in); err != nil {
			return err
		}
		if pin != nil {
			if err := s.storeOwnExitPin(ctx, q, p.UserID, in.ExitID, pin); err != nil {
				return err
			}
		}
		exitNodeID, exitID := exitRefs(in)
		n, err := q.UpdateChainRoute(ctx, sqlcgen.UpdateChainRouteParams{
			ID: id, UserID: p.UserID, Name: in.Name, ExitNodeID: exitNodeID, ExitID: exitID, IsEnabled: in.Enabled,
		})
		if err != nil {
			return fmt.Errorf("update chain route %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgChainRouteNotFound)
		}
		// 先加后删：起飞节点被删光时触发器会把规则一起删掉
		if err := q.AddChainRouteEntries(ctx, sqlcgen.AddChainRouteEntriesParams{RouteID: id, NodeIds: in.EntryNodeIDs}); err != nil {
			return fmt.Errorf("add entries of chain route %d: %w", id, err)
		}
		if err := q.RemoveChainRouteEntriesExcept(ctx, sqlcgen.RemoveChainRouteEntriesExceptParams{RouteID: id, NodeIds: in.EntryNodeIDs}); err != nil {
			return fmt.Errorf("remove entries of chain route %d: %w", id, err)
		}
		updated, err = s.reloadRoute(ctx, q, p.UserID, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	if isServer {
		s.kickApplier()
	}
	return updated, nil
}

// DeleteRoute 删除规则；套餐没开时也允许，他人的规则返回 404（R8）。服务端规则在面板上的条目由下发任务清掉。
func (s *ChainService) DeleteRoute(ctx context.Context, p *UserPrincipal, id int64) error {
	kind, err := s.routeKind(ctx, p.UserID, id)
	if err != nil {
		return err
	}
	isServer := kind == ChainKindServer
	if isServer {
		if err := s.allowServerWrite(p.UserID); err != nil {
			return err
		}
	}
	_, err = s.q.DeleteChainRoute(ctx, sqlcgen.DeleteChainRouteParams{ID: id, UserID: p.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return newError(CodeNotFound, msgChainRouteNotFound)
	}
	if err != nil {
		return fmt.Errorf("delete chain route %d: %w", id, err)
	}
	if isServer {
		s.kickApplier()
	}
	return nil
}

// ReorderRoutes 调整某一类型规则的顺序（Ruling-46）；ids 必须恰好是本人该类型全部规则的一个排列。返回调整后的该类型规则。
func (s *ChainService) ReorderRoutes(ctx context.Context, p *UserPrincipal, kind string, ids []int64) ([]ChainRoute, error) {
	if kind != ChainKindClient && kind != ChainKindServer {
		return nil, newError(CodeInvalidArgument, msgChainBadKind)
	}
	isServer := kind == ChainKindServer
	if isServer {
		if err := s.allowServerWrite(p.UserID); err != nil {
			return nil, err
		}
	}
	var routes []ChainRoute
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := lockChainOwner(ctx, q, p.UserID); err != nil {
			return err
		}
		flags, err := chainFlags(ctx, q, p.UserID)
		if err != nil {
			return err
		}
		if !flags.isKindAllowed(kind) {
			if isServer {
				return newError(CodeFeatureDisabled, msgServerChainNotAllowed)
			}
			return newError(CodeFeatureDisabled, msgChainNotAllowed)
		}
		// 校验排列只需要该类型规则的 id，不必算状态；调整后再整体重读
		rows, err := q.ListChainRoutes(ctx, []int64{p.UserID})
		if err != nil {
			return fmt.Errorf("list chain routes of user %d: %w", p.UserID, err)
		}
		want := make([]int64, 0, len(rows))
		for _, r := range rows {
			if r.Kind == kind {
				want = append(want, r.ID)
			}
		}
		got := slices.Clone(ids)
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(want, got) {
			return newError(CodeInvalidArgument, msgChainBadOrder)
		}
		// 全部重排（唯一约束延迟到提交时检查）；不只改动过的：相对顺序变了，没动位置的规则生效情况也可能变
		for i, id := range ids {
			n, err := q.SetChainRoutePosition(ctx, sqlcgen.SetChainRoutePositionParams{ID: id, UserID: p.UserID, Kind: kind, Position: int32(i + 1)})
			if err != nil {
				return fmt.Errorf("set position of chain route %d: %w", id, err)
			}
			if n == 0 {
				return newError(CodeInvalidArgument, msgChainBadOrder)
			}
		}
		st, _, err := s.loadState(ctx, q, p.UserID)
		if err != nil {
			return err
		}
		routes = st.routesOf(kind)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if isServer {
		s.kickApplier()
	}
	return routes, nil
}
