package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/inboundtpl"
	"v2-hub-server/internal/manager/panel"
)

const (
	minPort            = 1
	maxPort            = 65535
	maxListenLen       = 64
	maxConfigBytes     = 64 << 10
	maxAddressLen      = 255
	maxEndpointAddrs   = 32
	maxEndpointInbound = 64
	msgInboundNotFound = "入站不存在"
	nodeNameSeparator  = " · "

	ActionInboundCreate   = "inbound.create"
	ActionInboundUpdate   = "inbound.update"
	ActionInboundEnable   = "inbound.enable"
	ActionInboundDelete   = "inbound.delete"
	ActionInboundDriftAck = "inbound.drift_ack"
	ActionEndpointCreate  = "endpoint.create"
	ActionEndpointDelete  = "endpoint.delete"
	targetInbound         = "inbound"
	targetEndpoint        = "endpoint"
)

var (
	protocolPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9\-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9\-]{0,61}[A-Za-z0-9])?)*$`)
	// advancedConfigKeys 是高级 JSON 允许的顶层键（Xray 入站的三段配置）。
	advancedConfigKeys = map[string]bool{"settings": true, "streamSettings": true, "sniffing": true}
)

// Endpoint 是对外地址视图。
type Endpoint struct {
	ID         int64
	Address    string
	Port       int
	Remark     string
	IsDisabled bool
	IsMissing  bool
}

// Inbound 是入站视图（缓存副本，敏感字段已删除）。
type Inbound struct {
	ID              int64
	ServerID        int64
	RemoteRef       string
	Protocol        string
	Port            int
	Listen          string
	Tag             string
	Remark          string
	IsEnabled       bool
	Network         string
	Security        string
	CoreType        string
	Config          json.RawMessage
	ClientCount     int
	DriftDetectedAt *time.Time
	IsMissing       bool
	Endpoints       []Endpoint
	CreatedAt       time.Time
}

// SyncResult 是一次同步的统计。
type SyncResult struct {
	Inbounds int
	Added    int
	Updated  int
	Missing  int
	Drifted  int
}

// InboundCreateInput 是新建入站：Template 与 Config 二选一。
type InboundCreateInput struct {
	Remark         string
	Port           int
	Listen         string
	Enabled        bool
	Template       string
	TemplateParams inboundtpl.Params
	Protocol       string
	Config         json.RawMessage
}

// InboundUpdateInput 是修改入站；Config 为空表示不改配置。
type InboundUpdateInput struct {
	Remark string
	Port   int
	Listen string
	Config json.RawMessage
}

// EndpointInput 是为入站添加对外地址。
type EndpointInput struct {
	InboundIDs []int64
	Addresses  []string
	Port       int
	Remark     string
}

func validateInboundBase(remark string, port int, listen string) error {
	if n := utf8.RuneCountInString(strings.TrimSpace(remark)); n < 1 || n > maxRemarkLen {
		return newError(CodeInvalidArgument, fmt.Sprintf("备注长度须为 1–%d", maxRemarkLen))
	}
	if port < minPort || port > maxPort {
		return newError(CodeInvalidArgument, "端口须为 1–65535")
	}
	if len(listen) > maxListenLen {
		return newError(CodeInvalidArgument, "监听地址过长")
	}
	if listen != "" {
		if _, err := netip.ParseAddr(listen); err != nil {
			return newError(CodeInvalidArgument, "监听地址须为 IP，留空表示全部地址")
		}
	}
	return nil
}

// validateAdvancedConfig 校验高级 JSON：对象，只含 settings / streamSettings / sniffing，且各段为对象。
func validateAdvancedConfig(raw json.RawMessage) error {
	if len(raw) > maxConfigBytes {
		return newError(CodeInvalidArgument, "配置过大")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return newError(CodeInvalidArgument, "配置须为 JSON 对象")
	}
	for k, v := range top {
		if !advancedConfigKeys[k] {
			return newError(CodeInvalidArgument, "配置只能包含 settings、streamSettings、sniffing，出现了："+k)
		}
		var section map[string]any
		if err := json.Unmarshal(v, &section); err != nil {
			return newError(CodeInvalidArgument, k+" 须为 JSON 对象")
		}
	}
	return nil
}

func (s *PanelService) buildCreateSpec(in InboundCreateInput) (panel.InboundSpec, error) {
	if err := validateInboundBase(in.Remark, in.Port, in.Listen); err != nil {
		return panel.InboundSpec{}, err
	}
	base := inboundtpl.Base{Remark: strings.TrimSpace(in.Remark), Port: in.Port, Listen: in.Listen, Enabled: in.Enabled}
	hasConfig := len(in.Config) > 0
	if (in.Template == "") == !hasConfig {
		return panel.InboundSpec{}, newError(CodeInvalidArgument, "模板与高级 JSON 须二选一")
	}
	if in.Template != "" {
		spec, err := inboundtpl.Build(in.Template, base, in.TemplateParams)
		var pe *inboundtpl.ParamError
		switch {
		case errors.As(err, &pe):
			return panel.InboundSpec{}, newError(CodeInvalidArgument, pe.Message)
		case errors.Is(err, inboundtpl.ErrUnknownTemplate):
			return panel.InboundSpec{}, newError(CodeInvalidArgument, "未知的入站模板："+in.Template)
		case err != nil:
			return panel.InboundSpec{}, err
		}
		return spec, nil
	}
	if !protocolPattern.MatchString(in.Protocol) {
		return panel.InboundSpec{}, newError(CodeInvalidArgument, "协议名不合法")
	}
	if err := validateAdvancedConfig(in.Config); err != nil {
		return panel.InboundSpec{}, err
	}
	return panel.InboundSpec{Protocol: in.Protocol, Port: base.Port, Listen: base.Listen, Remark: base.Remark, Enabled: base.Enabled, Config: in.Config}, nil
}

// openEnabledProvider 打开 Provider，并拒绝对已停用服务器的写操作（T4）。
func (s *PanelService) openEnabledProvider(ctx context.Context, serverID int64) (panel.Provider, sqlcgen.GetServerConnRow, error) {
	p, row, err := s.openProvider(ctx, serverID)
	if err != nil {
		return nil, row, err
	}
	if row.IsDisabled {
		return nil, row, newError(CodeConflict, msgServerDisabled)
	}
	return p, row, nil
}

func upsertParams(serverID int64, in panel.Inbound) sqlcgen.UpsertInboundFromPlatformParams {
	return sqlcgen.UpsertInboundFromPlatformParams{
		ServerID: serverID, RemoteRef: in.Ref, Protocol: in.Protocol, Port: int32(in.Port), Listen: in.Listen,
		Tag: in.Tag, Remark: in.Remark, IsEnabled: in.Enabled, Network: in.Network, Security: in.Security,
		CoreType: in.CoreType, Config: in.Config, ClientCount: int32(in.ClientCount), SyncedHash: in.ConfigHash,
	}
}

func directNodeName(serverName string, in panel.Inbound) string {
	label := in.Remark
	if label == "" {
		label = fmt.Sprint(in.Port)
	}
	return truncate(serverName+nodeNameSeparator+label, maxNodeNameLen)
}

// savePlatformWrite 落库一次平台发起的写入结果：入站即“已确认”，并保证有直连节点。
func savePlatformWrite(ctx context.Context, q *sqlcgen.Queries, serverID int64, serverName string, in panel.Inbound) (int64, error) {
	id, err := q.UpsertInboundFromPlatform(ctx, upsertParams(serverID, in))
	if err != nil {
		return 0, fmt.Errorf("save inbound %s of server %d: %w", in.Ref, serverID, err)
	}
	if err := q.EnsureNode(ctx, sqlcgen.EnsureNodeParams{InboundID: id, DisplayName: directNodeName(serverName, in)}); err != nil {
		return 0, fmt.Errorf("ensure direct node of inbound %d: %w", id, err)
	}
	return id, nil
}

// SyncServer 探测并同步一台服务器（actor 为 nil 表示定时任务）。面板为事实来源（02 篇铁律二）。
func (s *PanelService) SyncServer(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) (*SyncResult, error) {
	p, row, err := s.openProvider(ctx, id)
	if err != nil {
		return nil, err
	}
	if row.IsDisabled {
		return nil, newError(CodeConflict, msgServerDisabled)
	}
	info, probeErr := p.Probe(ctx)
	if err := s.q.SetServerProbeResult(ctx, probeParams(id, &info, probeErr)); err != nil {
		return nil, fmt.Errorf("save probe result of server %d: %w", id, err)
	}
	if probeErr != nil {
		return nil, panelError(probeErr)
	}
	inbounds, err := p.ListInbounds(ctx)
	if err != nil {
		return nil, panelError(err)
	}
	var endpoints []panel.Endpoint
	if p.Capabilities().Endpoints {
		if endpoints, err = p.ListEndpoints(ctx); err != nil {
			return nil, panelError(err)
		}
	}
	var result *SyncResult
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		if result, err = s.applySync(ctx, q, id, row.Name, inbounds, endpoints); err != nil {
			return err
		}
		if actor == nil {
			return nil
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionServerSync, TargetType: targetServer,
			TargetID: fmt.Sprint(id), ClientIP: ip,
			Detail: map[string]any{"added": result.Added, "updated": result.Updated, "missing": result.Missing, "drifted": result.Drifted},
		})
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// applySync 把面板现状写入缓存：新增入站与节点、刷新已有入站、按摘要判定漂移、标记缺失（docs/03 §5.1）。
func (s *PanelService) applySync(ctx context.Context, q *sqlcgen.Queries, serverID int64, serverName string,
	inbounds []panel.Inbound, endpoints []panel.Endpoint) (*SyncResult, error) {
	existing, err := q.ListInboundsByServer(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("list cached inbounds of server %d: %w", serverID, err)
	}
	byRef := make(map[string]sqlcgen.ListInboundsByServerRow, len(existing))
	for _, r := range existing {
		byRef[r.RemoteRef] = r
	}
	now := s.now()
	result := &SyncResult{Inbounds: len(inbounds)}
	idByRef := make(map[string]int64, len(inbounds))
	presentRefs := make([]string, 0, len(inbounds))
	nameByRef := make(map[string]string, len(inbounds))
	for _, in := range inbounds {
		presentRefs = append(presentRefs, in.Ref)
		nameByRef[in.Ref] = in.Remark
		row, ok := byRef[in.Ref]
		if !ok {
			id, err := savePlatformWrite(ctx, q, serverID, serverName, in)
			if err != nil {
				return nil, err
			}
			idByRef[in.Ref] = id
			result.Added++
			continue
		}
		idByRef[in.Ref] = row.ID
		drift := row.DriftDetectedAt
		hashChanged := row.SyncedHash != in.ConfigHash
		switch {
		case hashChanged && !drift.Valid:
			drift = pgtype.Timestamptz{Time: now, Valid: true}
			result.Drifted++
		case !hashChanged && drift.Valid:
			// 面板侧又改回了已确认的配置，漂移自然消除
			drift = pgtype.Timestamptz{}
		}
		if hashChanged || row.IsMissing {
			result.Updated++
		}
		if err := q.UpdateInboundFromSync(ctx, sqlcgen.UpdateInboundFromSyncParams{
			ID: row.ID, Protocol: in.Protocol, Port: int32(in.Port), Listen: in.Listen, Tag: in.Tag, Remark: in.Remark,
			IsEnabled: in.Enabled, Network: in.Network, Security: in.Security, CoreType: in.CoreType, Config: in.Config,
			ClientCount: int32(in.ClientCount), DriftDetectedAt: drift,
		}); err != nil {
			return nil, fmt.Errorf("refresh inbound %d: %w", row.ID, err)
		}
		if err := q.EnsureNode(ctx, sqlcgen.EnsureNodeParams{InboundID: row.ID, DisplayName: directNodeName(serverName, in)}); err != nil {
			return nil, fmt.Errorf("ensure direct node of inbound %d: %w", row.ID, err)
		}
	}
	missing, err := q.MarkInboundsMissing(ctx, sqlcgen.MarkInboundsMissingParams{ServerID: serverID, PresentRefs: presentRefs})
	if err != nil {
		return nil, fmt.Errorf("mark missing inbounds of server %d: %w", serverID, err)
	}
	result.Missing = int(missing)

	presentEndpoints := make([]int64, 0, len(endpoints))
	for _, ep := range endpoints {
		inboundID, ok := idByRef[ep.InboundRef]
		if !ok {
			continue // 挂在面板其它节点或已删入站上的 Host，与本服务器无关
		}
		epID, err := q.UpsertEndpoint(ctx, sqlcgen.UpsertEndpointParams{
			InboundID: inboundID, RemoteGroupRef: ep.GroupRef, Address: ep.Address, Port: int32(ep.Port),
			Remark: ep.Remark, IsDisabled: ep.Disabled,
		})
		if err != nil {
			return nil, fmt.Errorf("save endpoint %s of inbound %d: %w", ep.Address, inboundID, err)
		}
		presentEndpoints = append(presentEndpoints, epID)
		label := nameByRef[ep.InboundRef]
		if err := q.EnsureNode(ctx, sqlcgen.EnsureNodeParams{
			InboundID: inboundID, EndpointID: pgtype.Int8{Int64: epID, Valid: true},
			DisplayName: truncate(label+nodeNameSeparator+ep.Address, maxNodeNameLen),
		}); err != nil {
			return nil, fmt.Errorf("ensure endpoint node %d: %w", epID, err)
		}
	}
	if _, err := q.MarkEndpointsMissing(ctx, sqlcgen.MarkEndpointsMissingParams{ServerID: serverID, PresentIds: presentEndpoints}); err != nil {
		return nil, fmt.Errorf("mark missing endpoints of server %d: %w", serverID, err)
	}
	if err := q.SetServerSynced(ctx, serverID); err != nil {
		return nil, fmt.Errorf("mark server %d synced: %w", serverID, err)
	}
	return result, nil
}

// ListServerInbounds 返回服务器的入站及其对外地址。
func (s *PanelService) ListServerInbounds(ctx context.Context, serverID int64) ([]Inbound, error) {
	if _, err := s.GetServer(ctx, serverID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListInboundsByServer(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("list inbounds of server %d: %w", serverID, err)
	}
	eps, err := s.q.ListEndpointsByServer(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("list endpoints of server %d: %w", serverID, err)
	}
	byInbound := make(map[int64][]Endpoint, len(rows))
	for _, e := range eps {
		byInbound[e.InboundID] = append(byInbound[e.InboundID], Endpoint{
			ID: e.ID, Address: e.Address, Port: int(e.Port), Remark: e.Remark, IsDisabled: e.IsDisabled, IsMissing: e.IsMissing,
		})
	}
	out := make([]Inbound, 0, len(rows))
	for _, r := range rows {
		in := inboundFromRow(sqlcgen.GetInboundRow(r))
		in.Endpoints = byInbound[r.ID]
		if in.Endpoints == nil {
			in.Endpoints = []Endpoint{}
		}
		out = append(out, in)
	}
	return out, nil
}

func inboundFromRow(r sqlcgen.GetInboundRow) Inbound {
	return Inbound{
		ID: r.ID, ServerID: r.ServerID, RemoteRef: r.RemoteRef, Protocol: r.Protocol, Port: int(r.Port),
		Listen: r.Listen, Tag: r.Tag, Remark: r.Remark, IsEnabled: r.IsEnabled, Network: r.Network,
		Security: r.Security, CoreType: r.CoreType, Config: r.Config, ClientCount: int(r.ClientCount),
		DriftDetectedAt: timePtr(r.DriftDetectedAt), IsMissing: r.IsMissing, CreatedAt: r.CreatedAt.Time,
		Endpoints: []Endpoint{},
	}
}

// GetInbound 返回入站详情（含对外地址）。
func (s *PanelService) GetInbound(ctx context.Context, id int64) (*Inbound, error) {
	row, err := s.q.GetInbound(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, newError(CodeNotFound, msgInboundNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get inbound %d: %w", id, err)
	}
	list, err := s.ListServerInbounds(ctx, row.ServerID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i], nil
		}
	}
	return nil, newError(CodeNotFound, msgInboundNotFound)
}

func (s *PanelService) loadInbound(ctx context.Context, id int64) (sqlcgen.GetInboundRow, error) {
	row, err := s.q.GetInbound(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return row, newError(CodeNotFound, msgInboundNotFound)
	}
	if err != nil {
		return row, fmt.Errorf("get inbound %d: %w", id, err)
	}
	return row, nil
}

// writeInbound 执行一次面板写入并落库、记审计。
func (s *PanelService) writeInbound(ctx context.Context, actor *AdminPrincipal, serverID int64, serverName, action string,
	ip *netip.Addr, detail map[string]any, write func() (panel.Inbound, error)) (*Inbound, error) {
	in, err := write()
	if err != nil {
		return nil, panelError(err)
	}
	var id int64
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		if id, err = savePlatformWrite(ctx, q, serverID, serverName, in); err != nil {
			return err
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: action, TargetType: targetInbound,
			TargetID: fmt.Sprint(id), ClientIP: ip, Detail: detail,
		})
	})
	if err != nil {
		return nil, err
	}
	return s.GetInbound(ctx, id)
}

// CreateInbound 在服务器上新建入站（模板或高级 JSON）。
func (s *PanelService) CreateInbound(ctx context.Context, actor *AdminPrincipal, serverID int64, in InboundCreateInput, ip *netip.Addr) (*Inbound, error) {
	spec, err := s.buildCreateSpec(in)
	if err != nil {
		return nil, err
	}
	p, row, err := s.openEnabledProvider(ctx, serverID)
	if err != nil {
		return nil, err
	}
	detail := map[string]any{"serverId": serverID, "protocol": spec.Protocol, "port": spec.Port, "template": in.Template}
	return s.writeInbound(ctx, actor, serverID, row.Name, ActionInboundCreate, ip, detail, func() (panel.Inbound, error) {
		return p.CreateInbound(ctx, spec)
	})
}

// UpdateInbound 修改入站；省略的敏感字段与现有 clients 由 Provider 从面板补回（Ruling-14）。
func (s *PanelService) UpdateInbound(ctx context.Context, actor *AdminPrincipal, id int64, in InboundUpdateInput, ip *netip.Addr) (*Inbound, error) {
	if err := validateInboundBase(in.Remark, in.Port, in.Listen); err != nil {
		return nil, err
	}
	if len(in.Config) > 0 {
		if err := validateAdvancedConfig(in.Config); err != nil {
			return nil, err
		}
	}
	row, err := s.loadInbound(ctx, id)
	if err != nil {
		return nil, err
	}
	p, srv, err := s.openEnabledProvider(ctx, row.ServerID)
	if err != nil {
		return nil, err
	}
	spec := panel.InboundSpec{
		Protocol: row.Protocol, Port: in.Port, Listen: in.Listen, Remark: strings.TrimSpace(in.Remark),
		Enabled: row.IsEnabled, Config: in.Config,
	}
	detail := map[string]any{"port": in.Port, "configChanged": len(in.Config) > 0}
	return s.writeInbound(ctx, actor, row.ServerID, srv.Name, ActionInboundUpdate, ip, detail, func() (panel.Inbound, error) {
		return p.UpdateInbound(ctx, row.RemoteRef, spec)
	})
}

// SetInboundEnabled 启用或停用入站。
func (s *PanelService) SetInboundEnabled(ctx context.Context, actor *AdminPrincipal, id int64, enabled bool, ip *netip.Addr) (*Inbound, error) {
	row, err := s.loadInbound(ctx, id)
	if err != nil {
		return nil, err
	}
	p, srv, err := s.openEnabledProvider(ctx, row.ServerID)
	if err != nil {
		return nil, err
	}
	return s.writeInbound(ctx, actor, row.ServerID, srv.Name, ActionInboundEnable, ip, map[string]any{"enabled": enabled},
		func() (panel.Inbound, error) {
			if err := p.SetInboundEnabled(ctx, row.RemoteRef, enabled); err != nil {
				return panel.Inbound{}, err
			}
			return p.GetInbound(ctx, row.RemoteRef)
		})
}

// AckInboundDrift 以面板当前配置为准，清除漂移标记。
func (s *PanelService) AckInboundDrift(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) error {
	row, err := s.loadInbound(ctx, id)
	if err != nil {
		return err
	}
	p, srv, err := s.openProvider(ctx, row.ServerID)
	if err != nil {
		return err
	}
	_, err = s.writeInbound(ctx, actor, row.ServerID, srv.Name, ActionInboundDriftAck, ip, nil, func() (panel.Inbound, error) {
		return p.GetInbound(ctx, row.RemoteRef)
	})
	return err
}

// DeleteInbound 先删面板再删平台记录（节点级联删除）；面板上已不存在也视为成功。
func (s *PanelService) DeleteInbound(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) error {
	row, err := s.loadInbound(ctx, id)
	if err != nil {
		return err
	}
	if !row.IsMissing {
		p, _, err := s.openEnabledProvider(ctx, row.ServerID)
		if err != nil {
			return err
		}
		if err := p.DeleteInbound(ctx, row.RemoteRef); err != nil && !errors.Is(err, panel.ErrNotFound) {
			return panelError(err)
		}
	}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.DeleteInbound(ctx, id); err != nil {
			return fmt.Errorf("delete inbound %d: %w", id, err)
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionInboundDelete, TargetType: targetInbound,
			TargetID: fmt.Sprint(id), ClientIP: ip,
			Detail: map[string]any{"serverId": row.ServerID, "port": row.Port, "protocol": row.Protocol},
		})
	})
}

func validEndpointAddress(addr string) bool {
	if addr == "" || len(addr) > maxAddressLen {
		return false
	}
	if _, err := netip.ParseAddr(addr); err == nil {
		return true
	}
	return hostnamePattern.MatchString(addr)
}

// CreateEndpoints 为同一服务器上的入站添加对外地址，完成后重新同步以落库地址与节点。
func (s *PanelService) CreateEndpoints(ctx context.Context, actor *AdminPrincipal, serverID int64, in EndpointInput, ip *netip.Addr) error {
	if len(in.InboundIDs) == 0 || len(in.InboundIDs) > maxEndpointInbound {
		return newError(CodeInvalidArgument, "须选择 1–64 个入站")
	}
	if len(in.Addresses) == 0 || len(in.Addresses) > maxEndpointAddrs {
		return newError(CodeInvalidArgument, "须填写 1–32 个地址")
	}
	for i, a := range in.Addresses {
		in.Addresses[i] = strings.TrimSpace(a)
		if !validEndpointAddress(in.Addresses[i]) {
			return newError(CodeInvalidArgument, "地址须为域名或 IP："+a)
		}
	}
	if in.Port < 0 || in.Port > maxPort || utf8.RuneCountInString(in.Remark) > maxRemarkLen {
		return newError(CodeInvalidArgument, "端口或备注不合法")
	}
	rows, err := s.q.ListInboundsByIDs(ctx, in.InboundIDs)
	if err != nil {
		return fmt.Errorf("list inbounds: %w", err)
	}
	refs := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.ServerID != serverID {
			return newError(CodeInvalidArgument, "所选入站不属于该服务器")
		}
		refs = append(refs, r.RemoteRef)
	}
	unique := make(map[int64]struct{}, len(in.InboundIDs))
	for _, id := range in.InboundIDs {
		unique[id] = struct{}{}
	}
	if len(refs) != len(unique) {
		return newError(CodeInvalidArgument, "所选入站不存在")
	}
	p, _, err := s.openEnabledProvider(ctx, serverID)
	if err != nil {
		return err
	}
	if !p.Capabilities().Endpoints {
		return newError(CodeInvalidArgument, "该面板不支持对外地址")
	}
	if err := p.CreateEndpoints(ctx, panel.EndpointSpec{InboundRefs: refs, Addresses: in.Addresses, Port: in.Port, Remark: in.Remark}); err != nil {
		return panelError(err)
	}
	if err := s.writeAuditOnly(ctx, AuditEntry{
		ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionEndpointCreate, TargetType: targetServer,
		TargetID: fmt.Sprint(serverID), ClientIP: ip, Detail: map[string]any{"inboundIds": in.InboundIDs, "addresses": in.Addresses},
	}); err != nil {
		return err
	}
	_, err = s.SyncServer(ctx, nil, serverID, nil)
	return err
}

func (s *PanelService) writeAuditOnly(ctx context.Context, e AuditEntry) error {
	return writeAudit(ctx, s.q, e)
}

// DeleteEndpoint 删除地址所在的整个 Host 组（面板与平台），对应节点级联删除。
func (s *PanelService) DeleteEndpoint(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) error {
	ep, err := s.q.GetEndpoint(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return newError(CodeNotFound, "对外地址不存在")
	}
	if err != nil {
		return fmt.Errorf("get endpoint %d: %w", id, err)
	}
	p, _, err := s.openEnabledProvider(ctx, ep.ServerID)
	if err != nil {
		return err
	}
	if err := p.DeleteEndpointGroup(ctx, ep.RemoteGroupRef); err != nil && !errors.Is(err, panel.ErrNotFound) {
		return panelError(err)
	}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.DeleteEndpointGroupLocal(ctx, sqlcgen.DeleteEndpointGroupLocalParams{ServerID: ep.ServerID, GroupRef: ep.RemoteGroupRef}); err != nil {
			return fmt.Errorf("delete endpoint group %s: %w", ep.RemoteGroupRef, err)
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionEndpointDelete, TargetType: targetEndpoint,
			TargetID: fmt.Sprint(id), ClientIP: ip, Detail: map[string]any{"address": ep.Address},
		})
	})
}

// serverHost 取面板地址的主机名，作为直连节点的对外地址。
func serverHost(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
