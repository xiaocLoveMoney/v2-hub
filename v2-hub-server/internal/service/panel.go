package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/crypto"
	"v2-hub-server/internal/manager/geoip"
	"v2-hub-server/internal/manager/panel"
)

// 服务器状态，与 docs/03 §2 一致。
const (
	ServerStatusUnknown    = "unknown"
	ServerStatusOnline     = "online"
	ServerStatusOffline    = "offline"
	ServerStatusAuthFailed = "auth_failed"
)

const (
	maxServerNameLen      = 64
	maxRemarkLen          = 255
	maxTokenLen           = 512
	maxLastErrorLen       = 255
	msgServerNotFound     = "服务器不存在"
	msgServerDisabled     = "服务器已停用，请先启用"
	msgWebCertUnsupported = "这台服务器的面板不支持读取证书路径"

	ActionServerCreate = "server.create"
	ActionServerUpdate = "server.update"
	ActionServerDelete = "server.delete"
	ActionServerSync   = "server.sync"
	targetServer       = "server"
)

var certPinPattern = regexp.MustCompile(`^[A-Za-z0-9+/]{43}=$`)

// statusByErrorKind 是转移表 T2、T3 的表驱动实现；探测成功走 T1（online）。
var statusByErrorKind = map[panel.ErrorKind]string{
	panel.KindAuth:        ServerStatusAuthFailed,
	panel.KindUnreachable: ServerStatusOffline,
	panel.KindNotFound:    ServerStatusOffline,
	panel.KindRejected:    ServerStatusOffline,
}

// PanelConfig 是面板相关业务的参数。
type PanelConfig struct {
	// RequestTimeout 是单次面板请求超时。
	RequestTimeout time.Duration
	// Locator 把服务器公网 IP 换成国家码（Ruling-65）；nil 表示位置扫描已关闭。
	Locator geoip.Locator
	// SiteLocation 返回站点时区，流量周期与到期日按它取“今天”（Ruling-84）；nil 按 UTC。
	SiteLocation func(ctx context.Context) *time.Location
	// LatencyEnabled 报告三网延迟检测是否开着（Ruling-86）；关着时趋势不带延迟数据。nil 按关着。
	LatencyEnabled func(ctx context.Context) (bool, error)
}

// PanelService 负责服务器接入、探测、入站同步与增删改、对外地址、节点与节点组（docs/03 §5.1）。
// 只经 panel.Provider 访问面板（Ruling-8）。
type PanelService struct {
	store
	sealer   *crypto.Sealer
	registry *panel.Registry
	cfg      PanelConfig
	now      func() time.Time
}

// NewPanelService 创建面板业务服务；now 为 nil 时用 time.Now。
func NewPanelService(pool *pgxpool.Pool, sealer *crypto.Sealer, registry *panel.Registry, cfg PanelConfig, now func() time.Time) *PanelService {
	if now == nil {
		now = time.Now
	}
	return &PanelService{store: newStore(pool), sealer: sealer, registry: registry, cfg: cfg, now: now}
}

// ServerConnInput 是接入一台面板的连接信息。APIToken 为空表示沿用原值（仅更新时）。
type ServerConnInput struct {
	ProviderType  string
	BaseURL       string
	APIToken      string
	TLSMode       string
	CertPinSHA256 string
}

// ServerInput 是新建 / 修改服务器的输入。
type ServerInput struct {
	ServerConnInput
	Name       string
	Region     string
	Remark     string
	IsDisabled bool
}

// Server 是服务器视图（不含 token）。
type Server struct {
	ID            int64
	Name          string
	ProviderType  string
	BaseURL       string
	TLSMode       string
	CertPinSHA256 *string
	Region        string
	Remark        string
	IsDisabled    bool
	Status        string
	LastError     string
	LastSeenAt    *time.Time
	LastSyncedAt  *time.Time
	PanelVersion  string
	InboundCount  int
	CreatedAt     time.Time
	// HostID 是关联的托管主机（docs/03 §5.22）；nil 表示没有绑定 SSH。
	HostID *int64
	// ClientFlow 是 VLESS 流控（Ruling-77），空串为不带。
	ClientFlow string
	// Resources 是最近一次探测成功的资源快照（Ruling-83）；nil 表示还没探测成功过。
	Resources *panel.Resources
	// PublicIPv4 / PublicIPv6 是面板自报的公网地址，没报为空串。
	PublicIPv4    string
	PublicIPv6    string
	OnlineClients int
	// Billing 与 Traffic 是账单、流量额度与本周期用量（Ruling-84）。
	Billing ServerBilling
	Traffic ServerCycleTraffic
	// Host 是关联的托管主机的系统信息；nil 表示没有关联。
	Host *ServerHostBrief

	// trafficAdjust 是“本周期已用”的校准值，只在 trafficAdjustCycle 等于当前周期起始日时有效。
	trafficAdjust      int64
	trafficAdjustCycle *time.Time
}

// ServerHostBrief 是服务器所在托管主机的系统信息（docs/03 §5.22）。CertNotAfter 是平台申请的面板证书的到期时间，没有为 nil。
type ServerHostBrief struct {
	OSID         string
	Arch         string
	CertNotAfter *time.Time
}

func (s *PanelService) validateConn(in ServerConnInput, requireToken bool) (ServerConnInput, error) {
	if !s.registry.Has(in.ProviderType) {
		return in, newError(CodeInvalidArgument, "不支持的面板类型："+in.ProviderType)
	}
	u, err := panel.ParseBaseURL(in.BaseURL)
	if err != nil {
		return in, newError(CodeInvalidArgument, "面板地址须为 http(s)://主机[:端口][/base path]，不含查询串")
	}
	in.BaseURL = u.String()
	if requireToken && in.APIToken == "" {
		return in, newError(CodeInvalidArgument, "API token 不能为空")
	}
	if len(in.APIToken) > maxTokenLen {
		return in, newError(CodeInvalidArgument, "API token 过长")
	}
	switch in.TLSMode {
	case panel.TLSVerify, panel.TLSSkip:
		in.CertPinSHA256 = ""
	case panel.TLSPin:
		if !certPinPattern.MatchString(in.CertPinSHA256) {
			return in, newError(CodeInvalidArgument, "证书指纹须为 SHA-256 的 Base64（44 个字符）")
		}
	default:
		return in, newError(CodeInvalidArgument, "TLS 校验模式只能是 verify / pin / skip")
	}
	return in, nil
}

func validateServerFields(in ServerInput) error {
	if n := utf8.RuneCountInString(strings.TrimSpace(in.Name)); n < 1 || n > maxServerNameLen {
		return newError(CodeInvalidArgument, fmt.Sprintf("名称长度须为 1–%d", maxServerNameLen))
	}
	if utf8.RuneCountInString(in.Remark) > maxRemarkLen {
		return newError(CodeInvalidArgument, "备注过长")
	}
	return validateServerRegion(in.Region)
}

// panelError 把面板错误转成对外错误：面板拒绝执行 → 400，其余 → 502（docs/03 §5）。
func panelError(err error) error {
	pe := panel.AsError(err)
	if pe == nil {
		return err
	}
	if pe.Kind == panel.KindRejected {
		return newError(CodeInvalidArgument, pe.Message)
	}
	return newError(CodeUpstream, pe.Message)
}

func (s *PanelService) connConfig(in ServerConnInput) panel.ConnConfig {
	return panel.ConnConfig{
		BaseURL: in.BaseURL, Token: in.APIToken, TLSMode: in.TLSMode,
		CertPinSHA256: in.CertPinSHA256, Timeout: s.cfg.RequestTimeout,
	}
}

func tokenAAD(serverID int64) []byte {
	return fmt.Appendf(nil, "server:%d:token", serverID)
}

// openToken 解密一台服务器的 API token。
func (s *PanelService) openToken(serverID int64, sealed []byte) (string, error) {
	token, err := s.sealer.Open(sealed, tokenAAD(serverID))
	if err != nil {
		return "", fmt.Errorf("open token of server %d: %w", serverID, err)
	}
	return string(token), nil
}

// saveToken 加密并保存一台服务器的 API token（在调用方的事务里）。
func (s *PanelService) saveToken(ctx context.Context, q *sqlcgen.Queries, serverID int64, token string) error {
	sealed, err := s.sealer.Seal([]byte(token), tokenAAD(serverID))
	if err != nil {
		return fmt.Errorf("seal server token: %w", err)
	}
	if err := q.SetServerToken(ctx, sqlcgen.SetServerTokenParams{ID: serverID, ApiTokenEnc: sealed}); err != nil {
		return fmt.Errorf("save server token: %w", err)
	}
	return nil
}

// openProvider 解密 token 并按类型创建 Provider。
func (s *PanelService) openProvider(ctx context.Context, serverID int64) (panel.Provider, sqlcgen.GetServerConnRow, error) {
	row, err := s.q.GetServerConn(ctx, serverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, row, newError(CodeNotFound, msgServerNotFound)
	}
	if err != nil {
		return nil, row, fmt.Errorf("get server %d: %w", serverID, err)
	}
	token, err := s.openToken(serverID, row.ApiTokenEnc)
	if err != nil {
		return nil, row, err
	}
	p, err := s.registry.Open(row.ProviderType, panel.ConnConfig{
		BaseURL: row.BaseUrl, Token: token, TLSMode: row.TlsMode,
		CertPinSHA256: row.CertPinSha256.String, Timeout: s.cfg.RequestTimeout,
	})
	if err != nil {
		return nil, row, fmt.Errorf("open provider of server %d: %w", serverID, err)
	}
	return p, row, nil
}

// TestServer 用未保存的连接信息探测面板。
func (s *PanelService) TestServer(ctx context.Context, in ServerConnInput) (*panel.PanelInfo, error) {
	conn, err := s.validateConn(in, true)
	if err != nil {
		return nil, err
	}
	info, err := s.probeConn(ctx, conn)
	if err != nil {
		return nil, panelError(err)
	}
	return info, nil
}

// probeConn 用已校验的连接信息探测面板，返回 Provider 层的原始错误（带 ErrorKind），
// 供要按“连不上 / 令牌被拒”分支的调用方使用；对外接口一律走 TestServer。
func (s *PanelService) probeConn(ctx context.Context, conn ServerConnInput) (*panel.PanelInfo, error) {
	p, err := s.registry.Open(conn.ProviderType, s.connConfig(conn))
	if err != nil {
		return nil, newError(CodeInvalidArgument, "连接信息不合法")
	}
	info, err := p.Probe(ctx)
	if err != nil {
		return nil, err
	}
	return &info, nil
}

// CertFingerprint 读取 https 面板的叶子证书指纹。
func (s *PanelService) CertFingerprint(ctx context.Context, baseURL string) (string, error) {
	fp, err := panel.CertFingerprint(ctx, baseURL)
	if err != nil {
		if pe := panel.AsError(err); pe != nil {
			return "", newError(CodeUpstream, pe.Message)
		}
		return "", newError(CodeInvalidArgument, "需要 https 面板地址")
	}
	return fp, nil
}

// ListServers 返回全部服务器。
func (s *PanelService) ListServers(ctx context.Context) ([]Server, error) {
	rows, err := s.q.ListServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	out := make([]Server, 0, len(rows))
	for _, r := range rows {
		out = append(out, serverFromRow(sqlcgen.GetServerRow(r)))
	}
	if err := s.attachTraffic(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetServer 返回一台服务器，带本周期的流量用量；不存在返回 404。
func (s *PanelService) GetServer(ctx context.Context, id int64) (*Server, error) {
	srv, err := s.loadServer(ctx, id)
	if err != nil {
		return nil, err
	}
	today := s.today(ctx)
	start, _ := trafficCycle(today, srv.Billing.TrafficResetDay)
	sent, recv, err := s.nicTrafficSince(ctx, id, start)
	if err != nil {
		return nil, err
	}
	srv.Traffic = trafficOf(srv, today, sent, recv)
	return srv, nil
}

// loadServer 读一台服务器，不算流量用量（Traffic 为零值）；不存在返回 404。
func (s *PanelService) loadServer(ctx context.Context, id int64) (*Server, error) {
	row, err := s.q.GetServer(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, newError(CodeNotFound, msgServerNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get server %d: %w", id, err)
	}
	srv := serverFromRow(row)
	return &srv, nil
}

func serverFromRow(r sqlcgen.GetServerRow) Server {
	srv := Server{
		ID: r.ID, Name: r.Name, ProviderType: r.ProviderType, BaseURL: r.BaseUrl, TLSMode: r.TlsMode,
		Region: r.Region, Remark: r.Remark, IsDisabled: r.IsDisabled, Status: r.Status, LastError: r.LastError,
		PanelVersion: r.PanelVersion, InboundCount: int(r.InboundCount),
		CreatedAt: r.CreatedAt.Time, LastSeenAt: timePtr(r.LastSeenAt), LastSyncedAt: timePtr(r.LastSyncedAt),
		ClientFlow: r.ClientFlow, Resources: resourcesFromSnapshot(r.ID, r.StatusSnapshot),
		PublicIPv4: r.PublicIpv4, PublicIPv6: r.PublicIpv6, OnlineClients: int(r.OnlineClients),
		Billing: billingFromRow(r), trafficAdjust: r.TrafficAdjustBytes, trafficAdjustCycle: datePtr(r.TrafficAdjustCycle),
	}
	if r.CertPinSha256.Valid {
		v := r.CertPinSha256.String
		srv.CertPinSHA256 = &v
	}
	if r.HostID != 0 {
		hostID := r.HostID
		srv.HostID = &hostID
		srv.Host = &ServerHostBrief{OSID: r.HostOsID, Arch: r.HostArch, CertNotAfter: timePtr(r.HostCertNotAfter)}
	}
	return srv
}

// resourcesFromSnapshot 解出快照里的资源（Ruling-83）；没有快照返回 nil，解不出来记日志后同样返回 nil（下一次探测会写回）。
func resourcesFromSnapshot(serverID int64, raw []byte) *panel.Resources {
	if len(raw) == 0 {
		return nil
	}
	var res panel.Resources
	if err := json.Unmarshal(raw, &res); err != nil {
		slog.Warn("decode server status snapshot", "server_id", serverID, "err", err)
		return nil
	}
	return &res
}

// snapshotOf 把资源编码成落库的快照；nil 资源或编码失败返回 nil（快照列置空）。
func snapshotOf(serverID int64, res *panel.Resources) []byte {
	if res == nil {
		return nil
	}
	raw, err := json.Marshal(res)
	if err != nil {
		slog.Warn("encode server status snapshot", "server_id", serverID, "err", err)
		return nil
	}
	return raw
}

// SupportsCoreConfig 报告某类面板能否改内核配置（服务端链式的入口，docs/03 §5.19）。
func (s *PanelService) SupportsCoreConfig(providerType string) bool {
	return s.registry.Capabilities(providerType).CoreConfig
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

func textOrNull(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

// CreateServer 接入面板：先探测，通过后加密保存 token、记录探测结果，再做一次同步。
// 同步失败不回滚接入，错误记在服务器的 last_error 上。
func (s *PanelService) CreateServer(ctx context.Context, actor *AdminPrincipal, in ServerInput, ip *netip.Addr) (*Server, error) {
	if err := validateServerFields(in); err != nil {
		return nil, err
	}
	conn, err := s.validateConn(in.ServerConnInput, true)
	if err != nil {
		return nil, err
	}
	in.ServerConnInput = conn
	info, err := s.TestServer(ctx, conn)
	if err != nil {
		return nil, err
	}
	var id int64
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		id, err = q.CreateServer(ctx, sqlcgen.CreateServerParams{
			Name: strings.TrimSpace(in.Name), ProviderType: conn.ProviderType, BaseUrl: conn.BaseURL,
			TlsMode: conn.TLSMode, CertPinSha256: textOrNull(conn.CertPinSHA256),
			Region: in.Region, Remark: in.Remark, IsDisabled: in.IsDisabled,
		})
		if isUniqueViolation(err) {
			return newError(CodeConflict, "服务器名称已存在")
		}
		if err != nil {
			return fmt.Errorf("create server: %w", err)
		}
		if err := s.saveToken(ctx, q, id, conn.APIToken); err != nil {
			return err
		}
		if err := q.SetServerProbeResult(ctx, probeParams(id, info, nil, s.now())); err != nil {
			return fmt.Errorf("save probe result: %w", err)
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionServerCreate, TargetType: targetServer,
			TargetID: fmt.Sprint(id), ClientIP: ip,
			Detail: map[string]any{"name": in.Name, "providerType": conn.ProviderType, "baseUrl": redactURL(conn.BaseURL), "tlsMode": conn.TLSMode},
		})
	})
	if err != nil {
		return nil, err
	}
	if in.Region == "" {
		s.fillRegionFromIP(ctx, id, info.PublicIP())
	}
	if !in.IsDisabled {
		if _, err := s.SyncServer(ctx, nil, id, nil); err != nil {
			s.recordProbeError(ctx, id, err)
		}
	}
	return s.GetServer(ctx, id)
}

// redactURL 只保留 scheme 与主机端口，base path 可能是面板的隐藏入口，不进审计详情。
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func probeParams(id int64, info *panel.PanelInfo, probeErr error, probedAt time.Time) sqlcgen.SetServerProbeResultParams {
	if probeErr == nil && info != nil {
		return sqlcgen.SetServerProbeResultParams{
			ID: id, Status: ServerStatusOnline, IsOnline: true, PanelVersion: info.Version, PanelGuid: info.GUID,
			StatusSnapshot: snapshotOf(id, info.Resources), PublicIpv4: info.PublicIPv4, PublicIpv6: info.PublicIPv6,
			ProbedAt: pgtype.Timestamptz{Time: probedAt, Valid: true},
		}
	}
	status := ServerStatusOffline
	msg := "未知错误"
	var se *Error
	if pe := panel.AsError(probeErr); pe != nil {
		if st, ok := statusByErrorKind[pe.Kind]; ok {
			status = st
		}
		msg = pe.Message
	} else if errors.As(probeErr, &se) {
		msg = se.Message
	}
	return sqlcgen.SetServerProbeResultParams{ID: id, Status: status, LastError: truncate(msg, maxLastErrorLen), ProbedAt: pgtype.Timestamptz{Time: probedAt, Valid: true}}
}

func (s *PanelService) recordProbeError(ctx context.Context, id int64, err error) {
	if perr := s.q.SetServerProbeResult(ctx, probeParams(id, nil, err, s.now())); perr != nil {
		// 只是状态回写失败，不影响主流程；原错误已在调用处返回或记录
		slog.Warn("record probe error", "server_id", id, "err", perr)
	}
}

// UpdateServer 修改服务器；连接信息变化或换了 token 时先探测。
func (s *PanelService) UpdateServer(ctx context.Context, actor *AdminPrincipal, id int64, in ServerInput, ip *netip.Addr) (*Server, error) {
	if err := validateServerFields(in); err != nil {
		return nil, err
	}
	conn, err := s.validateConn(in.ServerConnInput, false)
	if err != nil {
		return nil, err
	}
	current, err := s.q.GetServerConn(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, newError(CodeNotFound, msgServerNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get server %d: %w", id, err)
	}
	tokenChanged := conn.APIToken != ""
	if !tokenChanged {
		if conn.APIToken, err = s.openToken(id, current.ApiTokenEnc); err != nil {
			return nil, err
		}
	}
	connChanged := tokenChanged || conn.ProviderType != current.ProviderType || conn.BaseURL != current.BaseUrl ||
		conn.TLSMode != current.TlsMode || conn.CertPinSHA256 != current.CertPinSha256.String
	var info *panel.PanelInfo
	if connChanged && !in.IsDisabled {
		if info, err = s.TestServer(ctx, conn); err != nil {
			return nil, err
		}
	}
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		_, err := q.UpdateServer(ctx, sqlcgen.UpdateServerParams{
			ID: id, Name: strings.TrimSpace(in.Name), ProviderType: conn.ProviderType, BaseUrl: conn.BaseURL,
			TlsMode: conn.TLSMode, CertPinSha256: textOrNull(conn.CertPinSHA256),
			Region: in.Region, Remark: in.Remark, IsDisabled: in.IsDisabled,
		})
		if isUniqueViolation(err) {
			return newError(CodeConflict, "服务器名称已存在")
		}
		if err != nil {
			return fmt.Errorf("update server %d: %w", id, err)
		}
		if tokenChanged {
			if err := s.saveToken(ctx, q, id, conn.APIToken); err != nil {
				return err
			}
		}
		if info != nil {
			if err := q.SetServerProbeResult(ctx, probeParams(id, info, nil, s.now())); err != nil {
				return fmt.Errorf("save probe result: %w", err)
			}
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionServerUpdate, TargetType: targetServer,
			TargetID: fmt.Sprint(id), ClientIP: ip,
			Detail: map[string]any{"name": in.Name, "tokenChanged": tokenChanged, "connChanged": connChanged, "isDisabled": in.IsDisabled},
		})
	})
	if err != nil {
		return nil, err
	}
	return s.GetServer(ctx, id)
}

// DeleteServer 移除服务器的平台记录（入站、地址、节点级联删除），不改动面板。
func (s *PanelService) DeleteServer(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) error {
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.DeleteServer(ctx, id)
		if err != nil {
			return fmt.Errorf("delete server %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgServerNotFound)
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionServerDelete,
			TargetType: targetServer, TargetID: fmt.Sprint(id), ClientIP: ip,
		})
	})
}

// ProbeServer 探测一台服务器并按 T1–T3 落库；返回探测错误（已落库）。
func (s *PanelService) ProbeServer(ctx context.Context, id int64) error {
	p, _, err := s.openProvider(ctx, id)
	if err != nil {
		return err
	}
	info, probeErr := p.Probe(ctx)
	params := probeParams(id, &info, probeErr, s.now())
	if err := s.q.SetServerProbeResult(ctx, params); err != nil {
		return fmt.Errorf("save probe result of server %d: %w", id, err)
	}
	if probeErr != nil {
		return probeErr
	}
	if err := s.recordMetric(ctx, id, info.Resources); err != nil {
		return err
	}
	return s.recordNicTraffic(ctx, id, info.Resources)
}

// ListEnabledServerIDs 返回需要探测与同步的服务器（T4：停用的跳过）。
func (s *PanelService) ListEnabledServerIDs(ctx context.Context) ([]int64, error) {
	ids, err := s.q.ListEnabledServerIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list enabled servers: %w", err)
	}
	return ids, nil
}
