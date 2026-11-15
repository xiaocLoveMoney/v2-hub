package service

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/crypto"
	"v2-hub-server/internal/manager/ratelimit"
	"v2-hub-server/internal/manager/render"
)

// 订阅（docs/03 §5.4）：token、节点与分享链接的对应、各格式输出、频控与记录。

const (
	subTokenBytes = 24 // 192 bit，URL 安全 Base64 后 32 个字符
	queryFormat   = "format"
	pathSep       = "/"

	// 连接密码（docs/03 §5.4 连接密码，Ruling-58）：每用户每小时最多改 5 次
	connPasswordPerHour = 5
	connPasswordWindow  = time.Hour
	connPasswordMaxKeys = 10000

	ActionPortalConnPassword = "portal.connection_password"
	msgConnPasswordBad       = "连接密码须为 8–64 位字母、数字或 - _ . ~"
	msgConnPasswordRate      = "修改太频繁，请稍后再试"

	// 频控（docs/03 §5.4）：同一 token 每分钟 30 次、同一 IP 每分钟 60 次；token 不存在按 IP 计失败，15 分钟 20 次即锁 15 分钟
	subTokenRatePerMinute = 30
	subIPRatePerMinute    = 60
	subMissMaxFailures    = 20
	subRateWindow         = time.Minute
	subMissWindow         = 15 * time.Minute
	subGuardMaxKeys       = 50000

	defaultSubscriptionTitle         = "v2-hub"
	defaultSubscriptionIntervalHours = 12
	defaultSubscribeLogRetention     = 30 * 24 * time.Hour

	ActionUserSubReset   = "user.subscription_reset"
	ActionPortalSubReset = "portal.subscription_reset"

	msgSubNotFound    = "订阅不存在"
	msgSubRateLimited = "请求过于频繁，请稍后再试"
	msgSubBadFormat   = "format 只能是 base64、mihomo、singbox、xray"

	bytesPerGB       = 1 << 30
	dateLayout       = "2006-01-02"
	headerUserinfo   = "Subscription-Userinfo"
	headerInterval   = "Profile-Update-Interval"
	headerTitle      = "Profile-Title"
	headerDispostion = "Content-Disposition"
	contentTypeText  = "text/plain; charset=utf-8"
	contentTypeYAML  = "application/yaml; charset=utf-8"
	contentTypeJSONU = "application/json; charset=utf-8"
)

// SubscriptionConfig 是订阅的系统级设置（M3 放配置文件，M4 起移入系统设置表）。
type SubscriptionConfig struct {
	// BaseURL 是对外的用户域名根地址（如 https://user.example.com）；为空时用请求自身的地址。
	BaseURL string
	// Title 是客户端里显示的订阅名称。
	Title string
	// UpdateIntervalHours 是建议客户端自动更新的间隔。
	UpdateIntervalHours int
	// InfoNodes 为 true 时在节点前附“剩余流量”“到期时间”两条说明节点。
	InfoNodes bool
	// LogRetention 是订阅记录的保留时长。
	LogRetention time.Duration
	// Path 是订阅地址的路径前缀，空 = sub；OldPath / OldPathUntil 是过渡中的旧前缀（Ruling-59）。
	Path         string
	OldPath      string
	OldPathUntil time.Time
}

// subPath 返回当前路径前缀；没配置时用 sub（M10 之前的固定前缀）。
func (c SubscriptionConfig) subPath() string {
	if c.Path == "" {
		return LegacySubPath
	}
	return c.Path
}

// acceptsPath 报告 path 能否用来拉订阅（docs/03 §5.4 订阅路径）。
func (c SubscriptionConfig) acceptsPath(path string, now time.Time) bool {
	return SubscriptionSettings{Path: c.subPath(), OldPath: c.OldPath, OldPathUntil: c.OldPathUntil}.AcceptsSubPath(path, now)
}

// connPasswordPattern 是自设连接密码的写法：8–64 位，分享链接里不用转义的字符（Ruling-58）。
var connPasswordPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{8,64}$`)

// subGuards 是订阅端点的三个频控器。
type subGuards struct {
	token *ratelimit.FailureGuard
	ip    *ratelimit.FailureGuard
	miss  *ratelimit.FailureGuard
}

func newSubGuards(now func() time.Time) subGuards {
	rate := func(perMinute int) *ratelimit.FailureGuard {
		return ratelimit.NewFailureGuard(ratelimit.FailureGuardConfig{
			MaxFailures: perMinute, Window: subRateWindow, Lockout: subRateWindow, MaxKeys: subGuardMaxKeys,
		}, now)
	}
	return subGuards{
		token: rate(subTokenRatePerMinute),
		ip:    rate(subIPRatePerMinute),
		miss: ratelimit.NewFailureGuard(ratelimit.FailureGuardConfig{
			MaxFailures: subMissMaxFailures, Window: subMissWindow, Lockout: subMissWindow, MaxKeys: subGuardMaxKeys,
		}, now),
	}
}

func subTokenAAD(userID int64) []byte {
	return fmt.Appendf(nil, "end_user:%d:sub", userID)
}

// SubscriptionFormatLink 是某一格式的订阅地址。
type SubscriptionFormatLink struct {
	Format string
	URL    string
}

// SubscriptionInfo 是用户的订阅地址；URL 属于凭据，不得写日志。HasCustomPassword 表示自设了连接密码（不回传密码本身）。
type SubscriptionInfo struct {
	URL                 string
	Formats             []SubscriptionFormatLink
	UpdateIntervalHours int
	HasCustomPassword   bool
}

var subscriptionFormats = []string{render.FormatBase64, render.FormatMihomo, render.FormatSingBox, render.FormatXray}

// subConfig 返回当前生效的订阅设置：接了系统设置就以设置表为准（docs/03 §5.6 订阅设置），否则用构造时给的值。
// 读设置失败时退回构造时的值并留痕——订阅端点不能因为设置读不到而整体不可用。
func (s *UserService) subConfig(ctx context.Context) SubscriptionConfig {
	if s.cfg.Settings == nil {
		return s.cfg.Subscription
	}
	cur, err := s.cfg.Settings.Current(ctx)
	if err != nil {
		slog.Error("load subscription settings, falling back to config file", "err", err)
		return s.cfg.Subscription
	}
	return cur.subscriptionConfig()
}

func (s *UserService) subscriptionInfo(ctx context.Context, token, requestBase string) *SubscriptionInfo {
	cfg := s.subConfig(ctx)
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = strings.TrimRight(requestBase, "/")
	}
	u := base + pathSep + cfg.subPath() + pathSep + token
	info := &SubscriptionInfo{URL: u, UpdateIntervalHours: cfg.UpdateIntervalHours, Formats: make([]SubscriptionFormatLink, 0, len(subscriptionFormats))}
	for _, f := range subscriptionFormats {
		info.Formats = append(info.Formats, SubscriptionFormatLink{Format: f, URL: u + "?" + queryFormat + "=" + f})
	}
	return info
}

// GetSubscription 返回本人的订阅地址，尚无 token 时生成（docs/03 §5.4 地址）。
func (s *UserService) GetSubscription(ctx context.Context, p *UserPrincipal, requestBase string) (*SubscriptionInfo, error) {
	token, err := s.ensureSubToken(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	return s.subscriptionInfoOf(ctx, p.UserID, token, requestBase)
}

// subscriptionInfoOf 是带“是否自设连接密码”的订阅信息。
func (s *UserService) subscriptionInfoOf(ctx context.Context, userID int64, token, requestBase string) (*SubscriptionInfo, error) {
	sealed, err := s.q.GetUserCredential(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, newError(CodeNotFound, msgUserNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get credential of user %d: %w", userID, err)
	}
	cred, err := s.openCredential(userID, sealed)
	if err != nil {
		return nil, err
	}
	info := s.subscriptionInfo(ctx, token, requestBase)
	info.HasCustomPassword = cred.Password != ""
	return info, nil
}

// SetConnectionPassword 由用户本人自设连接密码，空串恢复为随机（docs/03 §5.4 连接密码，Ruling-58）。
// 只改面板 client 的 password 字段：凭据版本 +1 后由对账写到各面板，UUID、auth 与订阅地址不变。
// 错误：CodeInvalidArgument（写法不对）、CodeRateLimited（每小时超过 5 次）。
func (s *UserService) SetConnectionPassword(ctx context.Context, p *UserPrincipal, password, requestBase string, ip *netip.Addr) (*SubscriptionInfo, error) {
	if password != "" && !connPasswordPattern.MatchString(password) {
		return nil, newError(CodeInvalidArgument, msgConnPasswordBad)
	}
	key := strconv.FormatInt(p.UserID, 10)
	if s.connPasswordGuard.IsLocked(key) {
		return nil, newError(CodeRateLimited, msgConnPasswordRate)
	}
	token, err := s.ensureSubToken(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		sealed, err := q.GetUserCredentialForUpdate(ctx, p.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return newError(CodeNotFound, msgUserNotFound)
		}
		if err != nil {
			return fmt.Errorf("lock credential of user %d: %w", p.UserID, err)
		}
		cred, err := s.openCredential(p.UserID, sealed)
		if err != nil {
			return err
		}
		cred.Password = password
		resealed, err := s.sealCredential(p.UserID, cred)
		if err != nil {
			return err
		}
		if _, err := q.UpdateUserCredential(ctx, sqlcgen.UpdateUserCredentialParams{ID: p.UserID, ClientCredEnc: resealed}); err != nil {
			return fmt.Errorf("update credential of user %d: %w", p.UserID, err)
		}
		if err := q.MarkUserProvisionsPending(ctx, p.UserID); err != nil {
			return fmt.Errorf("mark provisions of user %d pending: %w", p.UserID, err)
		}
		// 审计只记动作与是否自设，不记密码
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorUser, ActorID: p.UserID, Action: ActionPortalConnPassword, TargetType: targetUser,
			TargetID: key, Detail: map[string]any{"custom": password != ""}, ClientIP: ip})
	})
	if err != nil {
		return nil, err
	}
	s.connPasswordGuard.RecordFailure(key)
	s.TriggerReconcile()
	info := s.subscriptionInfo(ctx, token, requestBase)
	info.HasCustomPassword = password != ""
	return info, nil
}

// sealCredential 把连接凭据加密（关联数据 end_user:<id>:client）。
func (s *UserService) sealCredential(userID int64, cred clientCredential) ([]byte, error) {
	plain, err := json.Marshal(cred)
	if err != nil {
		return nil, fmt.Errorf("encode credential: %w", err)
	}
	sealed, err := s.sealer.Seal(plain, credentialAAD(userID))
	if err != nil {
		return nil, fmt.Errorf("seal credential of user %d: %w", userID, err)
	}
	return sealed, nil
}

func (s *UserService) ensureSubToken(ctx context.Context, userID int64) (string, error) {
	// 并发的首次请求可能同时生成：只有一个能写入，另一个回读
	for range 2 {
		sealed, err := s.q.GetUserSubToken(ctx, userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", newError(CodeNotFound, msgUserNotFound)
		}
		if err != nil {
			return "", fmt.Errorf("get sub token of user %d: %w", userID, err)
		}
		if sealed != nil {
			plain, err := s.sealer.Open(sealed, subTokenAAD(userID))
			if err != nil {
				return "", fmt.Errorf("open sub token of user %d: %w", userID, err)
			}
			return string(plain), nil
		}
		token, hash, enc, err := s.newSubToken(userID)
		if err != nil {
			return "", err
		}
		n, err := s.q.SetUserSubToken(ctx, sqlcgen.SetUserSubTokenParams{ID: userID, SubTokenHash: hash, SubTokenEnc: enc})
		if err != nil {
			return "", fmt.Errorf("save sub token of user %d: %w", userID, err)
		}
		if n == 1 {
			return token, nil
		}
	}
	return "", fmt.Errorf("ensure sub token of user %d: lost the race twice", userID)
}

func (s *UserService) newSubToken(userID int64) (token string, hash, sealed []byte, err error) {
	token, err = crypto.RandomToken(subTokenBytes)
	if err != nil {
		return "", nil, nil, err
	}
	sealed, err = s.sealer.Seal([]byte(token), subTokenAAD(userID))
	if err != nil {
		return "", nil, nil, fmt.Errorf("seal sub token of user %d: %w", userID, err)
	}
	return token, crypto.SHA256(token), sealed, nil
}

// rotateSubscription 换新 token 并轮换连接凭据，把下发标为待同步（docs/03 §5.4 重置）。audit 的动作与操作者由调用方给出。
func (s *UserService) rotateSubscription(ctx context.Context, userID int64, audit AuditEntry) (string, error) {
	token, hash, sealedToken, err := s.newSubToken(userID)
	if err != nil {
		return "", err
	}
	// 新凭据不带自设的连接密码：重置即一并改回随机（Ruling-58）
	cred, err := newClientCredential()
	if err != nil {
		return "", err
	}
	sealedCred, err := s.sealCredential(userID, cred)
	if err != nil {
		return "", err
	}
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.RotateUserSubscription(ctx, sqlcgen.RotateUserSubscriptionParams{ID: userID, SubTokenHash: hash, SubTokenEnc: sealedToken, ClientCredEnc: sealedCred})
		if err != nil {
			return fmt.Errorf("rotate subscription of user %d: %w", userID, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgUserNotFound)
		}
		if err := q.MarkUserProvisionsPending(ctx, userID); err != nil {
			return fmt.Errorf("mark provisions of user %d pending: %w", userID, err)
		}
		audit.TargetType, audit.TargetID = targetUser, fmt.Sprint(userID)
		return writeAudit(ctx, q, audit)
	})
	if err != nil {
		return "", err
	}
	s.TriggerReconcile()
	return token, nil
}

// ResetOwnSubscription 由用户本人重置订阅，返回新地址。
func (s *UserService) ResetOwnSubscription(ctx context.Context, p *UserPrincipal, requestBase string, ip *netip.Addr) (*SubscriptionInfo, error) {
	token, err := s.rotateSubscription(ctx, p.UserID, AuditEntry{ActorType: ActorUser, ActorID: p.UserID, Action: ActionPortalSubReset, ClientIP: ip})
	if err != nil {
		return nil, err
	}
	return s.subscriptionInfo(ctx, token, requestBase), nil
}

// ResetUserSubscription 由管理员重置某用户的订阅（user:write）。
func (s *UserService) ResetUserSubscription(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) error {
	_, err := s.rotateSubscription(ctx, id, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionUserSubReset, ClientIP: ip})
	return err
}

// SubscriptionRequest 是一次订阅拉取。
type SubscriptionRequest struct {
	// Path 是地址里的路径前缀，只认当前与未过期的旧前缀（Ruling-59）。
	Path      string
	Token     string
	Format    string // 空表示按 User-Agent 判断
	UserAgent string
	ClientIP  *netip.Addr
}

// SubscriptionResult 是渲染好的订阅内容与响应头。
type SubscriptionResult struct {
	Body        []byte
	ContentType string
	Headers     map[string]string
}

// formatByUserAgent 按客户端标识猜格式（docs/03 §5.4 格式）。
func formatByUserAgent(ua string) string {
	ua = strings.ToLower(ua)
	for _, kw := range []string{"clash", "mihomo", "stash"} {
		if strings.Contains(ua, kw) {
			return render.FormatMihomo
		}
	}
	for _, kw := range []string{"sing-box", "sfa", "sfi", "sfm"} {
		if strings.Contains(ua, kw) {
			return render.FormatSingBox
		}
	}
	return render.FormatBase64
}

func resolveFormat(explicit, ua string) (string, error) {
	if explicit == "" {
		return formatByUserAgent(ua), nil
	}
	for _, f := range subscriptionFormats {
		if explicit == f {
			return f, nil
		}
	}
	return "", newError(CodeInvalidArgument, msgSubBadFormat)
}

// RenderSubscription 处理一次 /{path}/{token} 请求：频控 → 校验前缀 → 找用户 → 取缓存的链接 → 渲染 → 记录。不回源面板（铁律四）。
func (s *UserService) RenderSubscription(ctx context.Context, req SubscriptionRequest) (*SubscriptionResult, error) {
	ipKey := ""
	if req.ClientIP != nil {
		ipKey = req.ClientIP.String()
	}
	if ipKey != "" && (s.subGuards.miss.IsLocked(ipKey) || s.subGuards.ip.IsLocked(ipKey)) {
		return nil, newError(CodeRateLimited, msgSubRateLimited)
	}
	if ipKey != "" {
		s.subGuards.ip.RecordFailure(ipKey)
	}
	cfg := s.subConfig(ctx)
	// 前缀不对与 token 不存在同样对待：404 并计入失败频控，防枚举
	if !cfg.acceptsPath(req.Path, s.now()) {
		if ipKey != "" {
			s.subGuards.miss.RecordFailure(ipKey)
		}
		return nil, newError(CodeNotFound, msgSubNotFound)
	}
	format, err := resolveFormat(req.Format, req.UserAgent)
	if err != nil {
		return nil, err
	}
	hash := crypto.SHA256(req.Token)
	user, err := s.q.GetUserBySubTokenHash(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		if ipKey != "" {
			s.subGuards.miss.RecordFailure(ipKey)
		}
		return nil, newError(CodeNotFound, msgSubNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get user by sub token: %w", err)
	}
	tokenKey := hex.EncodeToString(hash)
	if s.subGuards.token.IsLocked(tokenKey) {
		return nil, newError(CodeRateLimited, msgSubRateLimited)
	}
	s.subGuards.token.RecordFailure(tokenKey)

	var items []subItem
	var chains []subChain
	if user.Status == UserStatusActive {
		var nodes []sqlcgen.ListUserSubNodesRow
		if items, nodes, err = s.loadSubItemsWithNodes(ctx, user.ID); err != nil {
			return nil, err
		}
		// Base64 表达不了链路（Ruling-36），不必为它读规则
		if format != render.FormatBase64 {
			if chains, err = s.loadSubChains(ctx, user.ID, items, nodes); err != nil {
				return nil, err
			}
		}
	}
	body, contentType, err := renderSubscription(format, s.subInfoTexts(cfg, user), items, chains)
	if err != nil {
		return nil, err
	}
	if err := s.q.InsertSubscribeLog(ctx, sqlcgen.InsertSubscribeLogParams{
		UserID: user.ID, Format: format, ClientIp: req.ClientIP, UserAgent: truncate(req.UserAgent, maxUserAgentLen),
	}); err != nil {
		// 记录写失败不影响订阅下发，但必须留痕
		slog.Error("insert subscribe log", "user_id", user.ID, "err", err)
	}
	return &SubscriptionResult{Body: body, ContentType: contentType, Headers: subHeaders(cfg, user)}, nil
}

// subInfoTexts 返回说明节点的文字：非 active 只有一条原因；active 且开启信息节点时是剩余流量与到期时间。
func (s *UserService) subInfoTexts(cfg SubscriptionConfig, u sqlcgen.GetUserBySubTokenHashRow) []string {
	switch u.Status {
	case UserStatusExpired:
		return []string{"套餐已到期，请续费"}
	case UserStatusLimited:
		return []string{"流量已用尽"}
	case UserStatusDisabled:
		return []string{"账号已停用，请联系管理员"}
	default:
	}
	if !cfg.InfoNodes {
		return nil
	}
	traffic := "流量：不限"
	if u.TrafficLimit > 0 {
		left := max(u.TrafficLimit-u.TrafficUsed, 0)
		traffic = fmt.Sprintf("剩余流量：%.2f GB", float64(left)/bytesPerGB)
	}
	expiry := "到期：不限"
	switch {
	case u.ExpiresAt.Valid:
		expiry = "到期：" + u.ExpiresAt.Time.In(s.cfg.Location).Format(dateLayout)
	case u.PendingDays > 0:
		expiry = fmt.Sprintf("到期：首次使用后 %d 天", u.PendingDays)
	default:
	}
	return []string{traffic, expiry}
}

func subHeaders(cfg SubscriptionConfig, u sqlcgen.GetUserBySubTokenHashRow) map[string]string {
	info := fmt.Sprintf("upload=0; download=%d", u.TrafficUsed)
	if u.TrafficLimit > 0 {
		info += fmt.Sprintf("; total=%d", u.TrafficLimit)
	}
	if u.ExpiresAt.Valid {
		info += fmt.Sprintf("; expire=%d", u.ExpiresAt.Time.Unix())
	}
	title := cfg.Title
	return map[string]string{
		headerUserinfo:   info,
		headerInterval:   fmt.Sprint(cfg.UpdateIntervalHours),
		headerTitle:      "base64:" + base64.StdEncoding.EncodeToString([]byte(title)),
		headerDispostion: "attachment; filename*=UTF-8''" + url.PathEscape(title),
	}
}

func renderSubscription(format string, infoTexts []string, items []subItem, chains []subChain) ([]byte, string, error) {
	if format == render.FormatBase64 {
		links := make([]string, 0, len(infoTexts)+len(items))
		for _, t := range infoTexts {
			links = append(links, render.InfoLink(t))
		}
		for _, it := range items {
			links = append(links, it.link)
		}
		return render.Base64(links), contentTypeText, nil
	}
	nodes := make([]render.ProxyNode, 0, len(infoTexts)+len(items))
	for _, t := range infoTexts {
		nodes = append(nodes, render.InfoNode(t))
	}
	for _, it := range items {
		nodes = append(nodes, it.node)
	}
	// 说明节点排在前面，链路的入口下标要相应后移
	renderChains := make([]render.Chain, 0, len(chains))
	for _, c := range chains {
		renderChains = append(renderChains, render.Chain{Name: c.name, Entry: len(infoTexts) + c.entryItem, Exit: c.exit})
	}
	switch format {
	case render.FormatMihomo:
		out, err := render.Mihomo(nodes, renderChains)
		return out, contentTypeYAML, err
	case render.FormatSingBox:
		out, err := render.SingBox(nodes, renderChains)
		return out, contentTypeJSONU, err
	default:
		out, err := render.Xray(nodes, renderChains)
		return out, contentTypeJSONU, err
	}
}

// subChain 是一条可输出的链路：entryItem 是起飞节点在 items 里的下标，exit 是落地，name 为空表示用默认名。
type subChain struct {
	name      string
	entryItem int
	exit      render.ProxyNode
}

// chainNameSep 连接规则名与起飞节点名，作为起了名的规则在订阅里各条链路的名称。
const chainNameSep = " · "

// loadSubChains 取用户启用且通畅的客户端规则，把每个生效的（起飞，落地）组合对到本次要输出的节点上（docs/03 §5.18 订阅输出）。
// 起飞或落地这次没有可输出的链接（如面板还没同步到）的组合跳过；出口的链接解不开或解析不了时跳过（读取处已留痕），
// 不让一条坏规则拖垮整份订阅。nodes 是 loadSubItemsWithNodes 一并返回的可用节点。
func (s *UserService) loadSubChains(ctx context.Context, userID int64, items []subItem, nodes []sqlcgen.ListUserSubNodesRow) ([]subChain, error) {
	if len(items) == 0 {
		return nil, nil
	}
	// 订阅只关心客户端规则，不需要各面板能否改内核配置
	st, err := loadClientChainState(ctx, s.q, s.sealer, userID, nodes)
	if err != nil {
		return nil, err
	}
	itemIndex := make(map[int64]int, len(items))
	for i, it := range items {
		itemIndex[it.nodeID] = i
	}
	routes := st.routesOf(ChainKindClient)
	out := make([]subChain, 0, len(routes))
	for _, r := range routes {
		exit, ok := subChainExit(st, r, items, itemIndex)
		if !ok {
			continue
		}
		for _, e := range r.Entries {
			entry, hasEntry := itemIndex[e.NodeID]
			if e.State != EntryOK || !hasEntry {
				continue
			}
			name := ""
			if r.Name != "" {
				name = r.Name + chainNameSep + e.Name
			}
			out = append(out, subChain{name: name, entryItem: entry, exit: exit})
		}
	}
	return out, nil
}

// subChainExit 返回一条客户端规则的落地参数：平台节点取本次输出的节点，出口取解析好的链接（客户端自己连，不用固定的地址）。
func subChainExit(st *chainState, r ChainRoute, items []subItem, itemIndex map[int64]int) (render.ProxyNode, bool) {
	if r.Exit.Kind == ChainExitNode {
		i, ok := itemIndex[r.Exit.ID]
		if !ok {
			return render.ProxyNode{}, false
		}
		return items[i].node, true
	}
	sec := st.exits[r.Exit.ID]
	return sec.node, sec.isParsed
}

// subItem 是一条可输出的节点：nodeID 对应节点目录，link 是改好名称与地址的分享链接，node 是解析后的模型。
type subItem struct {
	nodeID int64
	link   string
	node   render.ProxyNode
}

// loadSubItems 把用户在各面板上缓存的分享链接对应到节点目录（docs/03 §5.4 节点与链接对应），按节点排序输出。
func (s *UserService) loadSubItems(ctx context.Context, userID int64) ([]subItem, error) {
	items, _, err := s.loadSubItemsWithNodes(ctx, userID)
	return items, err
}

// loadSubItemsWithNodes 同 loadSubItems，另返回查到的可用节点，供随后算链路时复用。
func (s *UserService) loadSubItemsWithNodes(ctx context.Context, userID int64) ([]subItem, []sqlcgen.ListUserSubNodesRow, error) {
	nodes, err := s.q.ListUserSubNodes(ctx, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("list sub nodes of user %d: %w", userID, err)
	}
	if len(nodes) == 0 {
		return nil, nodes, nil
	}
	serverIDs := make([]int64, 0, 4)
	seen := make(map[int64]bool, 4)
	for _, n := range nodes {
		if !seen[n.ServerID] {
			seen[n.ServerID] = true
			serverIDs = append(serverIDs, n.ServerID)
		}
	}
	epRows, err := s.q.ListServerEndpointAddrs(ctx, serverIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("list endpoint addrs: %w", err)
	}
	provs, err := s.q.ListUserProvisionLinks(ctx, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("list provision links of user %d: %w", userID, err)
	}
	linksByServer := make(map[int64][]string, len(provs))
	for _, v := range provs {
		plain, err := s.sealer.Open(v.LinksEnc, linksAAD(v.ID))
		if err != nil {
			return nil, nil, fmt.Errorf("open links of provision %d: %w", v.ID, err)
		}
		var links []string
		if err := json.Unmarshal(plain, &links); err != nil {
			return nil, nil, fmt.Errorf("decode links of provision %d: %w", v.ID, err)
		}
		linksByServer[v.ServerID] = links
	}
	return matchLinks(nodes, epRows, linksByServer), nodes, nil
}

type endpointKey struct {
	inboundID int64
	address   string
	port      int
}

// matchLinks 先按（地址，端口）把链接对到对外地址节点；对不上的，端口等于入站端口且地址不是该入站任何对外地址时，
// 归该入站的直连节点。直连节点的地址改用平台登记的服务器地址（面板给出的是它自己认定的主机名）。
func matchLinks(nodes []sqlcgen.ListUserSubNodesRow, endpoints []sqlcgen.ListServerEndpointAddrsRow, linksByServer map[int64][]string) []subItem {
	inboundPort := make(map[int64]int, len(nodes))
	for _, n := range nodes {
		inboundPort[n.InboundID] = int(n.InboundPort)
	}
	known := make(map[endpointKey]bool, len(endpoints))
	for _, e := range endpoints {
		port := int(e.Port)
		if port == 0 {
			port = inboundPort[e.InboundID]
		}
		known[endpointKey{e.InboundID, strings.ToLower(e.Address), port}] = true
	}
	type parsed struct {
		link string
		node render.ProxyNode
		used bool
	}
	parsedByServer := make(map[int64][]*parsed, len(linksByServer))
	for sid, links := range linksByServer {
		for _, l := range links {
			pn, err := render.ParseLink(l)
			if err != nil {
				slog.Warn("skip unparsable share link", "server_id", sid)
				continue
			}
			parsedByServer[sid] = append(parsedByServer[sid], &parsed{link: l, node: pn})
		}
	}
	out := make([]subItem, 0, len(nodes))
	for _, n := range nodes {
		wantPort := int(n.EndpointPort)
		if n.IsDirect || wantPort == 0 {
			wantPort = int(n.InboundPort)
		}
		var hit *parsed
		for _, c := range parsedByServer[n.ServerID] {
			if c.used || c.node.Port != wantPort {
				continue
			}
			addr := strings.ToLower(c.node.Address)
			isEndpointLink := known[endpointKey{n.InboundID, addr, c.node.Port}]
			if (n.IsDirect && !isEndpointLink) || (!n.IsDirect && addr == strings.ToLower(n.EndpointAddress)) {
				hit = c
				break
			}
		}
		if hit == nil {
			continue
		}
		hit.used = true
		address := ""
		if n.IsDirect {
			address = serverHost(n.BaseUrl)
		}
		link, err := render.Rewrite(hit.link, n.DisplayName, address)
		if err != nil {
			slog.Warn("skip unrewritable share link", "node_id", n.ID)
			continue
		}
		node := hit.node
		node.Name = n.DisplayName
		if address != "" {
			node.Address = address
		}
		out = append(out, subItem{nodeID: n.ID, link: link, node: node})
	}
	return out
}

// SubscribeLogItem 是一条订阅拉取记录。
type SubscribeLogItem struct {
	ID        int64
	UserID    int64
	Username  string
	Format    string
	ClientIP  string
	UserAgent string
	CreatedAt time.Time
}

// ListSubscribeLogs 分页列出订阅记录；userID 为 0 表示全部（管理员），否则只看该用户。
func (s *UserService) ListSubscribeLogs(ctx context.Context, userID int64, pq PageQuery) (*Page[SubscribeLogItem], error) {
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	uid := pgtype.Int8{Int64: userID, Valid: userID != 0}
	total, err := s.q.CountSubscribeLogs(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("count subscribe logs: %w", err)
	}
	rows, err := s.q.ListSubscribeLogs(ctx, sqlcgen.ListSubscribeLogsParams{UserID: uid, PageLimit: limit, PageOffset: offset})
	if err != nil {
		return nil, fmt.Errorf("list subscribe logs: %w", err)
	}
	items := make([]SubscribeLogItem, 0, len(rows))
	for _, r := range rows {
		it := SubscribeLogItem{ID: r.ID, UserID: r.UserID, Username: r.Username, Format: r.Format, UserAgent: r.UserAgent, CreatedAt: r.CreatedAt.Time}
		if r.ClientIp != nil {
			it.ClientIP = r.ClientIp.String()
		}
		items = append(items, it)
	}
	return &Page[SubscribeLogItem]{Items: items, Total: total}, nil
}

// PurgeSubscribeLogs 删除超出保留期的订阅记录，返回删除条数（定时任务调用）。
func (s *UserService) PurgeSubscribeLogs(ctx context.Context) (int64, error) {
	cutoff := pgtype.Timestamptz{Time: s.now().Add(-s.subConfig(ctx).LogRetention), Valid: true}
	n, err := s.q.DeleteOldSubscribeLogs(ctx, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete old subscribe logs: %w", err)
	}
	return n, nil
}
