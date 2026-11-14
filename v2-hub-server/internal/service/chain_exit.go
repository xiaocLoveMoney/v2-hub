package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/render"
)

// 链式出口（docs/03 §5.21，Ruling-47、Ruling-50）：只用作链路落地的外部节点。
// 用户出口由用户自己添加（M6 的自有落地节点）；平台出口由管理员添加，经节点组授权给用户。
// 链接含凭据：加密存储，不进日志与审计。平台不向出口地址发起任何连接（Ruling-38）。

// 出口的归属（与 hub-api.yaml 的 ChainExitOwner 对应）。
const (
	ExitOwnerPlatform = "platform"
	ExitOwnerUser     = "user"
)

// 平台出口的分配方式（Ruling-64）。
const (
	ExitAssignGroup = "group"
	ExitAssignUser  = "user"
)

const (
	maxExitRemarkLen     = 255
	maxExitKeywordLen    = 64
	maxExitNodeGroups    = 100
	maxExitUsers         = 500
	targetChainExit      = "chain_exit"
	ActionExitCreate     = "chain_exit.create"
	ActionExitUpdate     = "chain_exit.update"
	ActionExitDelete     = "chain_exit.delete"
	ActionExitLinkView   = "chain_exit.link_view"
	ActionExitDisable    = "chain_exit.disable"
	ActionExitEnable     = "chain_exit.enable"
	ActionExitDeleteUser = "chain_exit.delete_user"

	msgExitNotFound      = "出口不存在"
	msgExitBadLink       = "链接无法识别：支持 vless、vmess、trojan、ss 分享链接，或 socks5://、http://、https://[用户名:密码@]地址:端口"
	msgExitLinkLen       = "链接长度须为 1–2048 个字符"
	msgExitNameEmpty     = "名称不能为空"
	msgExitTooMany       = "我的出口已达上限"
	msgExitDuplicate     = "已有同名的出口"
	msgExitBadRemark     = "备注不能超过 255 个字符，且不能含换行等控制字符"
	msgExitBadOwner      = "归属须为 platform 或 user"
	msgExitBadKeyword    = "关键字不能超过 64 个字符"
	msgExitBadGroups     = "节点组不存在或数量超出上限"
	msgExitBadUsers      = "用户不存在、已注销或数量超出上限"
	msgExitBadAssign     = "分配方式须为 group 或 user"
	msgExitUserReadOnly  = "用户出口只能停用、启用或删除"
	msgExitNoUse         = "平台出口至少要允许一种链式"
	msgExitServerSupport = "这个出口的协议或传输方式不能用于服务端链式，请去掉“可用于服务端链式”"
)

// ChainExit 是用户自己的一个出口；原链接属于凭据，不在这里。Enabled 为假表示已被管理员停用。
type ChainExit struct {
	ID         int64
	Name       string
	Protocol   string
	Address    string
	Port       int
	Enabled    bool
	RouteCount int
	CreatedAt  time.Time
}

// ExitNodeGroup 是平台出口所属的一个节点组。
type ExitNodeGroup struct {
	ID   int64
	Name string
}

// ExitUser 是平台出口指定的一个用户（Ruling-64）。
type ExitUser struct {
	ID       int64
	Username string
}

// AdminChainExit 是管理端看到的出口；UserID 为 0 表示平台出口。AssignMode 只对平台出口有意义。
type AdminChainExit struct {
	ID          int64
	UserID      int64
	Username    string
	Name        string
	Protocol    string
	Address     string
	Port        int
	Remark      string
	Enabled     bool
	AllowServer bool
	AllowClient bool
	AssignMode  string
	NodeGroups  []ExitNodeGroup
	Users       []ExitUser
	RouteCount  int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ChainExitFilter 是管理端出口列表的筛选条件；零值表示不限。
type ChainExitFilter struct {
	Owner   string
	UserID  int64
	Keyword string
}

// PlatformExitInput 是新建或修改平台出口的输入；修改时 Link 为空表示不改链接。
// AssignMode 为空按 group；只保留所选方式那一组 id，另一组忽略（Ruling-64）。
type PlatformExitInput struct {
	Name         string
	Link         string
	Remark       string
	Enabled      bool
	AllowServer  bool
	AllowClient  bool
	AssignMode   string
	NodeGroupIDs []int64
	UserIDs      []int64
}

// parsedExitLink 是一条校验过的出口链接。
type parsedExitLink struct {
	link string
	node render.ProxyNode
}

// parseExitLink 校验并解析出口的链接；错误信息不回显链接内容（含凭据）。
func parseExitLink(link string) (parsedExitLink, error) {
	link = strings.TrimSpace(link)
	if n := utf8.RuneCountInString(link); n < 1 || n > maxExitLinkLen {
		return parsedExitLink{}, newError(CodeInvalidArgument, msgExitLinkLen)
	}
	node, err := render.ParseLink(link)
	if err != nil {
		return parsedExitLink{}, newError(CodeInvalidArgument, msgExitBadLink)
	}
	switch node.Protocol {
	case render.ProtoVLESS, render.ProtoVMess, render.ProtoTrojan, render.ProtoShadowsocks, render.ProtoSocks, render.ProtoHTTP:
		return parsedExitLink{link: link, node: node}, nil
	default:
		return parsedExitLink{}, newError(CodeInvalidArgument, msgExitBadLink)
	}
}

// exitName 定名称：给了就用给的，否则取链接里的名称（过长截断），再没有就用“协议-地址”。
func exitName(given string, node render.ProxyNode) (string, error) {
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

// sealExitLink 加密链接并连同解析出的展示字段写回；固定的地址随之作废。
func (s *ChainService) sealExitLink(ctx context.Context, q *sqlcgen.Queries, id int64, parsed parsedExitLink) error {
	sealed, err := s.sealer.Seal([]byte(parsed.link), exitLinkAAD(id))
	if err != nil {
		return fmt.Errorf("seal link of chain exit %d: %w", id, err)
	}
	err = q.SetChainExitLink(ctx, sqlcgen.SetChainExitLinkParams{
		ID: id, LinkEnc: sealed, Protocol: parsed.node.Protocol, Address: parsed.node.Address, Port: int32(parsed.node.Port),
	})
	if err != nil {
		return fmt.Errorf("save link of chain exit %d: %w", id, err)
	}
	return nil
}

func chainExitFromRow(r sqlcgen.ListChainExitsRow) ChainExit {
	return ChainExit{
		ID: r.ID, Name: r.Name, Protocol: r.Protocol, Address: r.Address, Port: int(r.Port), Enabled: r.IsEnabled,
		RouteCount: int(r.RouteCount), CreatedAt: r.CreatedAt.Time,
	}
}

// listChainExits 返回用户自己的出口；exitID 非 0 时只取这一个（归属按 userID 约束，R8）。
func listChainExits(ctx context.Context, q *sqlcgen.Queries, userID, exitID int64) ([]ChainExit, error) {
	rows, err := q.ListChainExits(ctx, sqlcgen.ListChainExitsParams{UserID: userID, ExitID: pgtype.Int8{Int64: exitID, Valid: exitID != 0}})
	if err != nil {
		return nil, fmt.Errorf("list chain exits of user %d: %w", userID, err)
	}
	out := make([]ChainExit, 0, len(rows))
	for _, r := range rows {
		out = append(out, chainExitFromRow(r))
	}
	return out, nil
}

func getChainExit(ctx context.Context, q *sqlcgen.Queries, userID, exitID int64) (*ChainExit, error) {
	exits, err := listChainExits(ctx, q, userID, exitID)
	if err != nil {
		return nil, err
	}
	if len(exits) == 0 {
		return nil, newError(CodeNotFound, msgExitNotFound)
	}
	return &exits[0], nil
}

// CreateExit 添加一个用户出口（docs/03 §5.21 用户出口）。
func (s *ChainService) CreateExit(ctx context.Context, p *UserPrincipal, link, name string) (*ChainExit, error) {
	parsed, err := parseExitLink(link)
	if err != nil {
		return nil, err
	}
	name, err = exitName(name, parsed.node)
	if err != nil {
		return nil, err
	}
	var created *ChainExit
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := lockChainOwner(ctx, q, p.UserID); err != nil {
			return err
		}
		flags, err := chainFlags(ctx, q, p.UserID)
		if err != nil {
			return err
		}
		if !flags.CustomNode {
			return newError(CodeFeatureDisabled, msgExitNotAllowed)
		}
		count, err := q.CountChainExits(ctx, p.UserID)
		if err != nil {
			return fmt.Errorf("count chain exits of user %d: %w", p.UserID, err)
		}
		if count >= MaxChainExits {
			return newError(CodeInvalidArgument, msgExitTooMany)
		}
		id, err := q.InsertChainExit(ctx, sqlcgen.InsertChainExitParams{
			UserID: pgtype.Int8{Int64: p.UserID, Valid: true}, Name: name, Protocol: parsed.node.Protocol,
			Address: parsed.node.Address, Port: int32(parsed.node.Port), IsEnabled: true, AllowServer: true, AllowClient: true,
		})
		if isUniqueViolation(err) {
			return newError(CodeConflict, msgExitDuplicate)
		}
		if err != nil {
			return fmt.Errorf("insert chain exit: %w", err)
		}
		if err := s.sealExitLink(ctx, q, id, parsed); err != nil {
			return err
		}
		created, err = getChainExit(ctx, q, p.UserID, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// prepareExitRelink 是换链接在事务外的准备：出口被服务端规则引用时计频率，并按服务端链式的要求校验、固定新地址。
// 没被服务端规则引用时返回零值（不固定）。
func (s *ChainService) prepareExitRelink(ctx context.Context, userID, exitID int64, parsed parsedExitLink) (netip.Addr, bool, error) {
	if _, err := getChainExit(ctx, s.q, userID, exitID); err != nil {
		return netip.Addr{}, false, err
	}
	refs, err := s.q.CountServerRoutesByExit(ctx, pgtype.Int8{Int64: exitID, Valid: true})
	if err != nil {
		return netip.Addr{}, false, fmt.Errorf("count server routes of chain exit %d: %w", exitID, err)
	}
	if refs == 0 {
		return netip.Addr{}, false, nil
	}
	if err := s.allowServerWrite(userID); err != nil {
		return netip.Addr{}, false, err
	}
	ip, err := s.pinExitNode(ctx, parsed.node, nil)
	if err != nil {
		return netip.Addr{}, false, err
	}
	return ip, true, nil
}

// UpdateExit 改名称，link 非空时一并换链接；他人的返回 404（R8）。
// 出口被服务端规则引用时，新链接要重新通过地址限制（docs/03 §5.19）。
func (s *ChainService) UpdateExit(ctx context.Context, p *UserPrincipal, id int64, name, link string) (*ChainExit, error) {
	name, err := validChainName(name)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, newError(CodeInvalidArgument, msgExitNameEmpty)
	}
	var parsed *parsedExitLink
	var pinned netip.Addr
	isPinned := false
	if strings.TrimSpace(link) != "" {
		pl, err := parseExitLink(link)
		if err != nil {
			return nil, err
		}
		parsed = &pl
		if pinned, isPinned, err = s.prepareExitRelink(ctx, p.UserID, id, pl); err != nil {
			return nil, err
		}
	}
	var updated *ChainExit
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := lockChainOwner(ctx, q, p.UserID); err != nil {
			return err
		}
		flags, err := chainFlags(ctx, q, p.UserID)
		if err != nil {
			return err
		}
		if !flags.CustomNode {
			return newError(CodeFeatureDisabled, msgExitNotAllowed)
		}
		n, err := q.RenameChainExit(ctx, sqlcgen.RenameChainExitParams{ID: id, UserID: p.UserID, Name: name})
		if isUniqueViolation(err) {
			return newError(CodeConflict, msgExitDuplicate)
		}
		if err != nil {
			return fmt.Errorf("rename chain exit %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgExitNotFound)
		}
		if parsed != nil {
			if err := s.relinkExit(ctx, q, id, *parsed, pinned, isPinned); err != nil {
				return err
			}
		}
		updated, err = getChainExit(ctx, q, p.UserID, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	if parsed != nil {
		s.kickApplier()
	}
	return updated, nil
}

// relinkExit 在事务内换链接。事务外校验之后才被服务端规则引用上的（没固定地址），让用户重试，不留一个没校验过的出口给服务端规则用。
func (s *ChainService) relinkExit(ctx context.Context, q *sqlcgen.Queries, id int64, parsed parsedExitLink, pinned netip.Addr, isPinned bool) error {
	refs, err := q.CountServerRoutesByExit(ctx, pgtype.Int8{Int64: id, Valid: true})
	if err != nil {
		return fmt.Errorf("count server routes of chain exit %d: %w", id, err)
	}
	if refs > 0 && !isPinned {
		return newError(CodeConflict, msgChainExitChanged)
	}
	if err := s.sealExitLink(ctx, q, id, parsed); err != nil {
		return err
	}
	if !isPinned {
		return nil
	}
	err = q.SetChainExitResolved(ctx, sqlcgen.SetChainExitResolvedParams{ID: id, ResolvedIp: &pinned, ResolvedAt: timestamptz(s.now())})
	if err != nil {
		return fmt.Errorf("pin chain exit %d: %w", id, err)
	}
	return nil
}

// DeleteExit 删除用户出口，引用它的规则随外键一并删除；他人的返回 404（R8）。
func (s *ChainService) DeleteExit(ctx context.Context, p *UserPrincipal, id int64) error {
	n, err := s.q.DeleteChainExit(ctx, sqlcgen.DeleteChainExitParams{ID: id, UserID: p.UserID})
	if err != nil {
		return fmt.Errorf("delete chain exit %d: %w", id, err)
	}
	if n == 0 {
		return newError(CodeNotFound, msgExitNotFound)
	}
	s.kickApplier()
	return nil
}

// ExitLink 返回用户出口的原链接（凭据，不得写日志）；他人的与平台出口返回 404（R8）。
func (s *ChainService) ExitLink(ctx context.Context, p *UserPrincipal, id int64) (string, error) {
	sealed, err := s.q.GetChainExitLink(ctx, sqlcgen.GetChainExitLinkParams{ID: id, UserID: p.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", newError(CodeNotFound, msgExitNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("get link of chain exit %d: %w", id, err)
	}
	return s.openExitLink(id, sealed)
}

func (s *ChainService) openExitLink(id int64, sealed []byte) (string, error) {
	plain, err := s.sealer.Open(sealed, exitLinkAAD(id))
	if err != nil {
		return "", fmt.Errorf("open link of chain exit %d: %w", id, err)
	}
	return string(plain), nil
}

// 管理端（node:read / node:write，Ruling-50）-----------------------------------------------------

func adminExitFromRow(r sqlcgen.ListAdminChainExitsRow) AdminChainExit {
	return AdminChainExit{
		ID: r.ID, UserID: r.OwnerID, Username: r.Username, Name: r.Name, Protocol: r.Protocol, Address: r.Address, Port: int(r.Port),
		Remark: r.Remark, Enabled: r.IsEnabled, AllowServer: r.AllowServer, AllowClient: r.AllowClient, AssignMode: r.AssignMode,
		NodeGroups: []ExitNodeGroup{}, Users: []ExitUser{},
		RouteCount: int(r.RouteCount), CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
}

// attachExitGroups 给这些出口补上所属的节点组与指定的用户。
func attachExitGroups(ctx context.Context, q *sqlcgen.Queries, items []AdminChainExit) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(items))
	index := make(map[int64]int, len(items))
	for i, it := range items {
		ids = append(ids, it.ID)
		index[it.ID] = i
	}
	rows, err := q.ListExitNodeGroups(ctx, ids)
	if err != nil {
		return fmt.Errorf("list node groups of chain exits: %w", err)
	}
	for _, r := range rows {
		i := index[r.ExitID]
		items[i].NodeGroups = append(items[i].NodeGroups, ExitNodeGroup{ID: r.ID, Name: r.Name})
	}
	users, err := q.ListExitUsers(ctx, ids)
	if err != nil {
		return fmt.Errorf("list users of chain exits: %w", err)
	}
	for _, r := range users {
		i := index[r.ExitID]
		items[i].Users = append(items[i].Users, ExitUser{ID: r.ID, Username: r.Username})
	}
	return nil
}

func getAdminExit(ctx context.Context, q *sqlcgen.Queries, id int64) (*AdminChainExit, error) {
	rows, err := q.ListAdminChainExits(ctx, sqlcgen.ListAdminChainExitsParams{ExitID: pgtype.Int8{Int64: id, Valid: true}, RowLimit: 1})
	if err != nil {
		return nil, fmt.Errorf("get chain exit %d: %w", id, err)
	}
	if len(rows) == 0 {
		return nil, newError(CodeNotFound, msgExitNotFound)
	}
	items := []AdminChainExit{adminExitFromRow(rows[0])}
	if err := attachExitGroups(ctx, q, items); err != nil {
		return nil, err
	}
	return &items[0], nil
}

// ListExits 分页列出全部出口（平台出口与用户出口），按 id 倒序。
func (s *ChainService) ListExits(ctx context.Context, f ChainExitFilter, pq PageQuery) (*Page[AdminChainExit], error) {
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	keyword := strings.TrimSpace(f.Keyword)
	if utf8.RuneCountInString(keyword) > maxExitKeywordLen {
		return nil, newError(CodeInvalidArgument, msgExitBadKeyword)
	}
	var isPlatform pgtype.Bool
	switch f.Owner {
	case "":
		// 不限归属
	case ExitOwnerPlatform, ExitOwnerUser:
		isPlatform = pgtype.Bool{Bool: f.Owner == ExitOwnerPlatform, Valid: true}
	default:
		return nil, newError(CodeInvalidArgument, msgExitBadOwner)
	}
	userArg := pgtype.Int8{Int64: f.UserID, Valid: f.UserID > 0}
	keywordArg := pgtype.Text{String: keyword, Valid: keyword != ""}
	total, err := s.q.CountAdminChainExits(ctx, sqlcgen.CountAdminChainExitsParams{IsPlatform: isPlatform, UserID: userArg, Keyword: keywordArg})
	if err != nil {
		return nil, fmt.Errorf("count chain exits: %w", err)
	}
	if total == 0 {
		return &Page[AdminChainExit]{Items: []AdminChainExit{}}, nil
	}
	rows, err := s.q.ListAdminChainExits(ctx, sqlcgen.ListAdminChainExitsParams{
		IsPlatform: isPlatform, UserID: userArg, Keyword: keywordArg, RowLimit: limit, RowOffset: offset,
	})
	if err != nil {
		return nil, fmt.Errorf("list chain exits: %w", err)
	}
	items := make([]AdminChainExit, 0, len(rows))
	for _, r := range rows {
		items = append(items, adminExitFromRow(r))
	}
	if err := attachExitGroups(ctx, s.q, items); err != nil {
		return nil, err
	}
	return &Page[AdminChainExit]{Items: items, Total: total}, nil
}

// platformExitSpec 是校验过的平台出口输入。
type platformExitSpec struct {
	in     PlatformExitInput
	parsed *parsedExitLink
}

// normalizePlatformExit 校验平台出口的输入；isLinkRequired 为真时必须带链接（新建）。
func normalizePlatformExit(in PlatformExitInput, isLinkRequired bool) (platformExitSpec, error) {
	name, err := validChainName(in.Name)
	if err != nil {
		return platformExitSpec{}, err
	}
	if name == "" {
		return platformExitSpec{}, newError(CodeInvalidArgument, msgExitNameEmpty)
	}
	in.Name = name
	in.Remark = strings.TrimSpace(in.Remark)
	if utf8.RuneCountInString(in.Remark) > maxExitRemarkLen || strings.ContainsFunc(in.Remark, unicode.IsControl) {
		return platformExitSpec{}, newError(CodeInvalidArgument, msgExitBadRemark)
	}
	if !in.AllowServer && !in.AllowClient {
		return platformExitSpec{}, newError(CodeInvalidArgument, msgExitNoUse)
	}
	if in, err = normalizeExitAssign(in); err != nil {
		return platformExitSpec{}, err
	}
	spec := platformExitSpec{in: in}
	hasLink := strings.TrimSpace(in.Link) != ""
	if !hasLink && isLinkRequired {
		return platformExitSpec{}, newError(CodeInvalidArgument, msgExitLinkLen)
	}
	if hasLink {
		parsed, err := parseExitLink(in.Link)
		if err != nil {
			return platformExitSpec{}, err
		}
		spec.parsed = &parsed
	}
	return spec, nil
}

// normalizeExitAssign 校验分配方式，只留所选方式那一组 id（去重），另一组清空（Ruling-64）。
func normalizeExitAssign(in PlatformExitInput) (PlatformExitInput, error) {
	switch in.AssignMode {
	case "", ExitAssignGroup:
		in.AssignMode, in.UserIDs = ExitAssignGroup, nil
		in.NodeGroupIDs = dedupeIDs(in.NodeGroupIDs)
		if len(in.NodeGroupIDs) > maxExitNodeGroups {
			return in, newError(CodeInvalidArgument, msgExitBadGroups)
		}
	case ExitAssignUser:
		in.NodeGroupIDs = nil
		in.UserIDs = dedupeIDs(in.UserIDs)
		if len(in.UserIDs) > maxExitUsers {
			return in, newError(CodeInvalidArgument, msgExitBadUsers)
		}
	default:
		return in, newError(CodeInvalidArgument, msgExitBadAssign)
	}
	return in, nil
}

// checkPlatformServerUse 校验允许用于服务端链式的平台出口确实写得进 Xray 配置（平台出口不受地址限制，Ruling-48）。
func checkPlatformServerUse(node render.ProxyNode, isServerAllowed bool) error {
	if !isServerAllowed {
		return nil
	}
	if _, ok := render.XrayOutbound(node, ""); !ok {
		return newError(CodeInvalidArgument, msgExitServerSupport)
	}
	return nil
}

// setExitGroups 把平台出口的节点组整体换成 ids；有不存在的节点组返回 400。
func setExitGroups(ctx context.Context, q *sqlcgen.Queries, exitID int64, ids []int64) error {
	if err := q.DeleteExitNodeGroups(ctx, exitID); err != nil {
		return fmt.Errorf("clear node groups of chain exit %d: %w", exitID, err)
	}
	if len(ids) == 0 {
		return nil
	}
	found, err := q.CountNodeGroupsByIDs(ctx, ids)
	if err != nil {
		return fmt.Errorf("count node groups: %w", err)
	}
	if int(found) != len(ids) {
		return newError(CodeInvalidArgument, msgExitBadGroups)
	}
	if err := q.AddExitNodeGroups(ctx, sqlcgen.AddExitNodeGroupsParams{NodeGroupIds: ids, ExitID: exitID}); err != nil {
		return fmt.Errorf("set node groups of chain exit %d: %w", exitID, err)
	}
	return nil
}

// setExitUsers 把平台出口指定的用户整体换成 ids；有不存在或已注销的用户返回 400。
func setExitUsers(ctx context.Context, q *sqlcgen.Queries, exitID int64, ids []int64) error {
	if err := q.DeleteExitUsers(ctx, exitID); err != nil {
		return fmt.Errorf("clear users of chain exit %d: %w", exitID, err)
	}
	if len(ids) == 0 {
		return nil
	}
	found, err := q.CountLiveUsersByIDs(ctx, ids)
	if err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if int(found) != len(ids) {
		return newError(CodeInvalidArgument, msgExitBadUsers)
	}
	if err := q.AddExitUsers(ctx, sqlcgen.AddExitUsersParams{ExitID: exitID, UserIds: ids}); err != nil {
		return fmt.Errorf("set users of chain exit %d: %w", exitID, err)
	}
	return nil
}

// setExitAssignment 写平台出口的分配方式与对应名单（Ruling-64）。
func setExitAssignment(ctx context.Context, q *sqlcgen.Queries, exitID int64, in PlatformExitInput) error {
	if err := q.SetChainExitAssignMode(ctx, sqlcgen.SetChainExitAssignModeParams{ID: exitID, AssignMode: in.AssignMode}); err != nil {
		return fmt.Errorf("set assign mode of chain exit %d: %w", exitID, err)
	}
	if err := setExitGroups(ctx, q, exitID, in.NodeGroupIDs); err != nil {
		return err
	}
	return setExitUsers(ctx, q, exitID, in.UserIDs)
}

// exitAuditDetail 是出口审计的详情；不含链接（凭据）。
func exitAuditDetail(e *AdminChainExit) map[string]any {
	return map[string]any{
		"name": e.Name, "protocol": e.Protocol, "userId": e.UserID,
		"assignMode": e.AssignMode, "nodeGroups": len(e.NodeGroups), "users": len(e.Users),
	}
}

// CreatePlatformExit 添加一个平台出口并写审计。
func (s *ChainService) CreatePlatformExit(ctx context.Context, actor *AdminPrincipal, in PlatformExitInput, ip *netip.Addr) (*AdminChainExit, error) {
	spec, err := normalizePlatformExit(in, true)
	if err != nil {
		return nil, err
	}
	if err := checkPlatformServerUse(spec.parsed.node, spec.in.AllowServer); err != nil {
		return nil, err
	}
	var created *AdminChainExit
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		node := spec.parsed.node
		id, err := q.InsertChainExit(ctx, sqlcgen.InsertChainExitParams{
			Name: spec.in.Name, Protocol: node.Protocol, Address: node.Address, Port: int32(node.Port), Remark: spec.in.Remark,
			IsEnabled: spec.in.Enabled, AllowServer: spec.in.AllowServer, AllowClient: spec.in.AllowClient,
		})
		if isUniqueViolation(err) {
			return newError(CodeConflict, msgExitDuplicate)
		}
		if err != nil {
			return fmt.Errorf("insert platform chain exit: %w", err)
		}
		if err := s.sealExitLink(ctx, q, id, *spec.parsed); err != nil {
			return err
		}
		if err := setExitAssignment(ctx, q, id, spec.in); err != nil {
			return err
		}
		if created, err = getAdminExit(ctx, q, id); err != nil {
			return err
		}
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionExitCreate,
			TargetType: targetChainExit, TargetID: fmt.Sprint(id), Detail: exitAuditDetail(created), ClientIP: ip})
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// UpdatePlatformExit 改平台出口并写审计；对用户出口调用返回 400（Ruling-50）。
func (s *ChainService) UpdatePlatformExit(ctx context.Context, actor *AdminPrincipal, id int64, in PlatformExitInput, ip *netip.Addr) (*AdminChainExit, error) {
	spec, err := normalizePlatformExit(in, false)
	if err != nil {
		return nil, err
	}
	var updated *AdminChainExit
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		current, err := getAdminExit(ctx, q, id)
		if err != nil {
			return err
		}
		if current.UserID != 0 {
			return newError(CodeInvalidArgument, msgExitUserReadOnly)
		}
		if err := s.checkPlatformUpdateUse(ctx, q, id, spec); err != nil {
			return err
		}
		_, err = q.UpdatePlatformChainExit(ctx, sqlcgen.UpdatePlatformChainExitParams{
			ID: id, Name: spec.in.Name, Remark: spec.in.Remark, IsEnabled: spec.in.Enabled,
			AllowServer: spec.in.AllowServer, AllowClient: spec.in.AllowClient,
		})
		if isUniqueViolation(err) {
			return newError(CodeConflict, msgExitDuplicate)
		}
		if err != nil {
			return fmt.Errorf("update platform chain exit %d: %w", id, err)
		}
		if spec.parsed != nil {
			if err := s.sealExitLink(ctx, q, id, *spec.parsed); err != nil {
				return err
			}
		}
		if err := setExitAssignment(ctx, q, id, spec.in); err != nil {
			return err
		}
		if updated, err = getAdminExit(ctx, q, id); err != nil {
			return err
		}
		detail := exitAuditDetail(updated)
		detail["linkChanged"] = spec.parsed != nil
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionExitUpdate,
			TargetType: targetChainExit, TargetID: fmt.Sprint(id), Detail: detail, ClientIP: ip})
	})
	if err != nil {
		return nil, err
	}
	s.kickApplier()
	return updated, nil
}

// checkPlatformUpdateUse 校验改后的平台出口若允许用于服务端链式，其链接（新给的或现有的）写得进 Xray 配置。
func (s *ChainService) checkPlatformUpdateUse(ctx context.Context, q *sqlcgen.Queries, id int64, spec platformExitSpec) error {
	if spec.parsed != nil {
		return checkPlatformServerUse(spec.parsed.node, spec.in.AllowServer)
	}
	secrets, err := loadExitSecrets(ctx, q, s.sealer, []int64{id})
	if err != nil {
		return err
	}
	sec := secrets[id]
	if !sec.isParsed {
		return newError(CodeInvalidArgument, msgExitBadLink)
	}
	return checkPlatformServerUse(sec.node, spec.in.AllowServer)
}

// SetExitEnabled 停用或启用出口（平台出口与用户出口都可以）并写审计；停用后引用它的规则阻断。
func (s *ChainService) SetExitEnabled(ctx context.Context, actor *AdminPrincipal, id int64, enabled bool, ip *netip.Addr) (*AdminChainExit, error) {
	var updated *AdminChainExit
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.SetChainExitEnabled(ctx, sqlcgen.SetChainExitEnabledParams{ID: id, IsEnabled: enabled})
		if err != nil {
			return fmt.Errorf("set chain exit %d enabled: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgExitNotFound)
		}
		if updated, err = getAdminExit(ctx, q, id); err != nil {
			return err
		}
		action := ActionExitDisable
		if enabled {
			action = ActionExitEnable
		}
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: action,
			TargetType: targetChainExit, TargetID: fmt.Sprint(id), Detail: exitAuditDetail(updated), ClientIP: ip})
	})
	if err != nil {
		return nil, err
	}
	s.kickApplier()
	return updated, nil
}

// DeleteExitAsAdmin 删除出口（平台出口或用户出口），引用它的规则随外键一并删除；写审计。
func (s *ChainService) DeleteExitAsAdmin(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) error {
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		current, err := getAdminExit(ctx, q, id)
		if err != nil {
			return err
		}
		if _, err := q.DeleteChainExitByID(ctx, id); err != nil {
			return fmt.Errorf("delete chain exit %d: %w", id, err)
		}
		action := ActionExitDelete
		if current.UserID != 0 {
			action = ActionExitDeleteUser
		}
		detail := exitAuditDetail(current)
		detail["routeCount"] = current.RouteCount
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: action,
			TargetType: targetChainExit, TargetID: fmt.Sprint(id), Detail: detail, ClientIP: ip})
	})
	if err != nil {
		return err
	}
	s.kickApplier()
	return nil
}

// PlatformExitLink 返回平台出口的原链接（凭据，不得写日志）并写审计；用户出口的凭据管理员不能查看，返回 404（Ruling-50）。
func (s *ChainService) PlatformExitLink(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) (string, error) {
	sealed, err := s.q.GetPlatformChainExitLink(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", newError(CodeNotFound, msgExitNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("get link of platform chain exit %d: %w", id, err)
	}
	link, err := s.openExitLink(id, sealed)
	if err != nil {
		return "", err
	}
	err = writeAudit(ctx, s.q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionExitLinkView,
		TargetType: targetChainExit, TargetID: fmt.Sprint(id), ClientIP: ip})
	if err != nil {
		return "", err
	}
	return link, nil
}
