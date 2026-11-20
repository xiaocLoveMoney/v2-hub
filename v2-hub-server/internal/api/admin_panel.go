package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"time"

	"v2-hub-server/internal/manager/inboundtpl"
	"v2-hub-server/internal/manager/panel"
	"v2-hub-server/internal/service"
)

// PanelManager 是 Web 层需要的服务器 / 入站 / 节点能力（由 service.PanelService 实现）。
type PanelManager interface {
	TestServer(ctx context.Context, in service.ServerConnInput) (*panel.PanelInfo, error)
	CertFingerprint(ctx context.Context, baseURL string) (string, error)
	ListServers(ctx context.Context) ([]service.Server, error)
	GetServer(ctx context.Context, id int64) (*service.Server, error)
	CreateServer(ctx context.Context, actor *service.AdminPrincipal, in service.ServerInput, ip *netip.Addr) (*service.Server, error)
	UpdateServer(ctx context.Context, actor *service.AdminPrincipal, id int64, in service.ServerInput, ip *netip.Addr) (*service.Server, error)
	DeleteServer(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) error
	SyncServer(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) (*service.SyncResult, error)
	SetServerBilling(ctx context.Context, actor *service.AdminPrincipal, id int64, in service.ServerBillingInput, ip *netip.Addr) (*service.Server, error)
	GetServerMetrics(ctx context.Context, id int64, rng string, withLatency bool) (*service.ServerMetrics, error)
	ListServerTrends(ctx context.Context, withLatency bool) ([]service.ServerTrend, error)

	ListServerInbounds(ctx context.Context, serverID int64) ([]service.Inbound, error)
	GetInbound(ctx context.Context, id int64) (*service.Inbound, error)
	CreateInbound(ctx context.Context, actor *service.AdminPrincipal, serverID int64, in service.InboundCreateInput, ip *netip.Addr) (*service.Inbound, error)
	UpdateInbound(ctx context.Context, actor *service.AdminPrincipal, id int64, in service.InboundUpdateInput, ip *netip.Addr) (*service.Inbound, error)
	SetInboundEnabled(ctx context.Context, actor *service.AdminPrincipal, id int64, enabled bool, ip *netip.Addr) (*service.Inbound, error)
	AckInboundDrift(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) error
	GetInboundPanelConfig(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) (*service.InboundPanelConfig, error)
	GeneratePanelKeys(ctx context.Context, actor *service.AdminPrincipal, serverID int64, kind string, ip *netip.Addr) (json.RawMessage, error)
	WebCertFiles(ctx context.Context, serverID int64) (panel.CertFiles, error)
	ScanRealityTarget(ctx context.Context, serverID int64, target, sni string, xver int) (*panel.RealityScan, error)
	LocateServer(ctx context.Context, id int64) (*service.ServerLocation, error)
	ScanRealityTargets(ctx context.Context, serverID int64, targets []string) ([]panel.RealityScan, error)
	DeleteInbound(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) error
	CreateEndpoints(ctx context.Context, actor *service.AdminPrincipal, serverID int64, in service.EndpointInput, ip *netip.Addr) error
	DeleteEndpoint(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) error

	ListNodes(ctx context.Context, f service.NodeFilter, pq service.PageQuery) (*service.Page[service.Node], error)
	UpdateNode(ctx context.Context, actor *service.AdminPrincipal, id int64, in service.NodeInput, ip *netip.Addr) error
	AffixNodeNames(ctx context.Context, actor *service.AdminPrincipal, in service.NodeAffixInput, ip *netip.Addr) (int64, error)
	ListNodeGroups(ctx context.Context) ([]service.NodeGroup, error)
	CreateNodeGroup(ctx context.Context, actor *service.AdminPrincipal, in service.NodeGroupInput, ip *netip.Addr) (*service.NodeGroup, error)
	UpdateNodeGroup(ctx context.Context, actor *service.AdminPrincipal, id int64, in service.NodeGroupInput, ip *netip.Addr) error
	DeleteNodeGroup(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) error
}

// ---- VO：与 hub-api.yaml 的 schema 一致 ----

type panelInfoVO struct {
	Version string `json:"version"`
	GUID    string `json:"guid"`
	// Resources 是资源快照（Ruling-81），面板没给为 null。
	Resources *panel.Resources `json:"resources"`
}

type serverVO struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	ProviderType  string     `json:"providerType"`
	BaseURL       string     `json:"baseUrl"`
	TLSMode       string     `json:"tlsMode"`
	CertPinSHA256 *string    `json:"certPinSha256"`
	Region        string     `json:"region"`
	Remark        string     `json:"remark"`
	IsDisabled    bool       `json:"isDisabled"`
	Status        string     `json:"status"`
	LastError     string     `json:"lastError"`
	LastSeenAt    *time.Time `json:"lastSeenAt"`
	LastSyncedAt  *time.Time `json:"lastSyncedAt"`
	PanelVersion  string     `json:"panelVersion"`
	InboundCount  int        `json:"inboundCount"`
	CreatedAt     time.Time  `json:"createdAt"`
	// HostID 是关联的托管主机（docs/03 §5.22）；没有为 null。
	HostID *int64 `json:"hostId"`
	// ClientFlow 是服务器的 VLESS 流控（Ruling-77），空串为不带。
	ClientFlow string `json:"clientFlow"`
	// Reach 是这台服务器上大陆连通检测的目标数（Ruling-67）；功能没开为 null。
	Reach *serverReachVO `json:"reach"`
	// Resources 是最近一次探测成功的资源快照（Ruling-81）；还没探测成功过为 null。
	Resources     *panel.Resources `json:"resources"`
	PublicIPv4    string           `json:"publicIpv4"`
	PublicIPv6    string           `json:"publicIpv6"`
	OnlineClients int              `json:"onlineClients"`
	// Billing、Traffic 是账单、流量额度与本周期用量（Ruling-82）；Host 是托管主机的系统信息，没有关联为 null。
	Billing serverBillingVO      `json:"billing"`
	Traffic serverCycleTrafficVO `json:"traffic"`
	Host    *serverHostBriefVO   `json:"host"`
}

// toServerVO 把服务器转成 VO；reach 为 nil 表示大陆连通检测没开。
func toServerVO(s service.Server, reach map[int64]service.ReachCount) serverVO {
	vo := serverVO{
		ID: s.ID, Name: s.Name, ProviderType: s.ProviderType, BaseURL: s.BaseURL, TLSMode: s.TLSMode,
		CertPinSHA256: s.CertPinSHA256, Region: s.Region, Remark: s.Remark, IsDisabled: s.IsDisabled,
		Status: s.Status, LastError: s.LastError, LastSeenAt: s.LastSeenAt, LastSyncedAt: s.LastSyncedAt,
		PanelVersion: s.PanelVersion, InboundCount: s.InboundCount, CreatedAt: s.CreatedAt, HostID: s.HostID,
		ClientFlow: s.ClientFlow, Resources: s.Resources, PublicIPv4: s.PublicIPv4, PublicIPv6: s.PublicIPv6,
		OnlineClients: s.OnlineClients, Billing: toServerBillingVO(s.Billing), Traffic: toServerCycleTrafficVO(s.Traffic),
		Host: toServerHostBriefVO(s.Host),
	}
	if reach != nil {
		c := reach[s.ID]
		vo.Reach = &serverReachVO{Total: c.Total, OK: c.OK, Blocked: c.Blocked, Partial: c.Partial}
	}
	return vo
}

// writeServer 输出一台服务器（带大陆连通的目标数）。
func (h *handlers) writeServer(w http.ResponseWriter, r *http.Request, srv *service.Server) {
	reach, err := h.serverReach(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toServerVO(*srv, reach))
}

type endpointVO struct {
	ID         int64  `json:"id"`
	Address    string `json:"address"`
	Port       int    `json:"port"`
	Remark     string `json:"remark"`
	IsDisabled bool   `json:"isDisabled"`
	IsMissing  bool   `json:"isMissing"`
}

type inboundVO struct {
	ID              int64           `json:"id"`
	ServerID        int64           `json:"serverId"`
	RemoteRef       string          `json:"remoteRef"`
	Protocol        string          `json:"protocol"`
	Port            int             `json:"port"`
	Listen          string          `json:"listen"`
	Tag             string          `json:"tag"`
	Remark          string          `json:"remark"`
	IsEnabled       bool            `json:"isEnabled"`
	Network         string          `json:"network"`
	Security        string          `json:"security"`
	CoreType        string          `json:"coreType"`
	Config          json.RawMessage `json:"config"`
	ClientCount     int             `json:"clientCount"`
	DriftDetectedAt *time.Time      `json:"driftDetectedAt"`
	IsMissing       bool            `json:"isMissing"`
	Endpoints       []endpointVO    `json:"endpoints"`
	CreatedAt       time.Time       `json:"createdAt"`
}

func toInboundVO(in service.Inbound) inboundVO {
	return inboundVO{
		ID: in.ID, ServerID: in.ServerID, RemoteRef: in.RemoteRef, Protocol: in.Protocol, Port: in.Port,
		Listen: in.Listen, Tag: in.Tag, Remark: in.Remark, IsEnabled: in.IsEnabled, Network: in.Network,
		Security: in.Security, CoreType: in.CoreType, Config: in.Config, ClientCount: in.ClientCount,
		DriftDetectedAt: in.DriftDetectedAt, IsMissing: in.IsMissing, CreatedAt: in.CreatedAt,
		Endpoints: mapSlice(in.Endpoints, func(e service.Endpoint) endpointVO {
			return endpointVO{ID: e.ID, Address: e.Address, Port: e.Port, Remark: e.Remark, IsDisabled: e.IsDisabled, IsMissing: e.IsMissing}
		}),
	}
}

type syncResultVO struct {
	Inbounds int `json:"inbounds"`
	Added    int `json:"added"`
	Updated  int `json:"updated"`
	Missing  int `json:"missing"`
	Drifted  int `json:"drifted"`
}

type nodeVO struct {
	ID          int64    `json:"id"`
	InboundID   int64    `json:"inboundId"`
	EndpointID  *int64   `json:"endpointId"`
	ServerID    int64    `json:"serverId"`
	ServerName  string   `json:"serverName"`
	Protocol    string   `json:"protocol"`
	Address     string   `json:"address"`
	Port        int      `json:"port"`
	DisplayName string   `json:"displayName"`
	Region      string   `json:"region"`
	Tags        []string `json:"tags"`
	SortOrder   int      `json:"sortOrder"`
	IsEnabled   bool     `json:"isEnabled"`
	IsMissing   bool     `json:"isMissing"`
	GroupIDs    []int64  `json:"groupIds"`
	// Reach 是大陆连通结论（Ruling-67）；功能没开或节点不在检测范围为 null。
	Reach *nodeReachVO `json:"reach"`
}

type nodeGroupVO struct {
	ID      int64   `json:"id"`
	Name    string  `json:"name"`
	Remark  string  `json:"remark"`
	NodeIDs []int64 `json:"nodeIds"`
}

// ---- 请求体 ----

type serverConnRequest struct {
	ProviderType  string `json:"providerType"`
	BaseURL       string `json:"baseUrl"`
	APIToken      string `json:"apiToken"`
	TLSMode       string `json:"tlsMode"`
	CertPinSHA256 string `json:"certPinSha256"`
}

func (r serverConnRequest) toInput() service.ServerConnInput {
	return service.ServerConnInput{ProviderType: r.ProviderType, BaseURL: r.BaseURL, APIToken: r.APIToken, TLSMode: r.TLSMode, CertPinSHA256: r.CertPinSHA256}
}

type serverRequest struct {
	serverConnRequest
	Name       string `json:"name"`
	Region     string `json:"region"`
	Remark     string `json:"remark"`
	IsDisabled bool   `json:"isDisabled"`
}

func (r serverRequest) toInput() service.ServerInput {
	return service.ServerInput{ServerConnInput: r.serverConnRequest.toInput(), Name: r.Name, Region: r.Region, Remark: r.Remark, IsDisabled: r.IsDisabled}
}

type inboundCreateRequest struct {
	Remark         string            `json:"remark"`
	Port           int               `json:"port"`
	Listen         string            `json:"listen"`
	Enabled        *bool             `json:"enabled"`
	Template       string            `json:"template"`
	TemplateParams inboundtpl.Params `json:"templateParams"`
	Protocol       string            `json:"protocol"`
	Config         json.RawMessage   `json:"config"`
}

type inboundUpdateRequest struct {
	Remark string          `json:"remark"`
	Port   int             `json:"port"`
	Listen string          `json:"listen"`
	Config json.RawMessage `json:"config"`
}

type enabledRequest struct {
	Enabled bool `json:"enabled"`
}

type endpointRequest struct {
	InboundIDs []int64  `json:"inboundIds"`
	Addresses  []string `json:"addresses"`
	Port       int      `json:"port"`
	Remark     string   `json:"remark"`
}

type nodeRequest struct {
	DisplayName string   `json:"displayName"`
	Tags        []string `json:"tags"`
	SortOrder   int      `json:"sortOrder"`
	IsEnabled   bool     `json:"isEnabled"`
}

type nodeAffixRequest struct {
	NodeIDs []int64 `json:"nodeIds"`
	Prefix  string  `json:"prefix"`
	Suffix  string  `json:"suffix"`
}

type nodeAffixVO struct {
	Updated int64 `json:"updated"`
}

type nodeGroupRequest struct {
	Name    string  `json:"name"`
	Remark  string  `json:"remark"`
	NodeIDs []int64 `json:"nodeIds"`
}

type fingerprintRequest struct {
	BaseURL string `json:"baseUrl"`
}

const (
	queryServerID = "serverId"
	queryGroupID  = "groupId"
	msgBadFilter  = "过滤参数不合法"
)

// ---- 处理函数 ----

func (h *handlers) testServer(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	var req serverConnRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	info, err := h.panels.TestServer(r.Context(), req.toInput())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, panelInfoVO{Version: info.Version, GUID: info.GUID, Resources: info.Resources})
}

func (h *handlers) certFingerprint(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	var req fingerprintRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	fp, err := h.panels.CertFingerprint(r.Context(), req.BaseURL)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, map[string]string{"sha256": fp})
}

func (h *handlers) listServers(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	list, err := h.panels.ListServers(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	reach, err := h.serverReach(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, mapSlice(list, func(s service.Server) serverVO { return toServerVO(s, reach) }))
}

func (h *handlers) getServer(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	srv, err := h.panels.GetServer(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	h.writeServer(w, r, srv)
}

func (h *handlers) createServer(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req serverRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	srv, err := h.panels.CreateServer(r.Context(), p, req.toInput(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	h.writeServer(w, r, srv)
}

func (h *handlers) updateServer(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req serverRequest
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	srv, err := h.panels.UpdateServer(r.Context(), p, id, req.toInput(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	h.writeServer(w, r, srv)
}

type clientFlowRequest struct {
	ClientFlow string `json:"clientFlow"`
}

// setServerClientFlow 开关服务器的 VLESS 流控（Ruling-77）。
func (h *handlers) setServerClientFlow(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req clientFlowRequest
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	srv, err := h.users.SetServerClientFlow(r.Context(), p, id, req.ClientFlow, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	h.writeServer(w, r, srv)
}

func (h *handlers) deleteServer(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.panels.DeleteServer(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) syncServer(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	res, err := h.panels.SyncServer(r.Context(), p, id, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, syncResultVO{Inbounds: res.Inbounds, Added: res.Added, Updated: res.Updated, Missing: res.Missing, Drifted: res.Drifted})
}

func (h *handlers) listServerInbounds(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	list, err := h.panels.ListServerInbounds(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, mapSlice(list, toInboundVO))
}

func (h *handlers) createInbound(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req inboundCreateRequest
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	enabled := req.Enabled == nil || *req.Enabled
	in, err := h.hosts.CreateInbound(r.Context(), p, id, service.InboundCreateInput{
		Remark: req.Remark, Port: req.Port, Listen: req.Listen, Enabled: enabled, Template: req.Template,
		TemplateParams: req.TemplateParams, Protocol: req.Protocol, Config: req.Config,
	}, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toInboundVO(*in))
}

func (h *handlers) getInbound(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	in, err := h.panels.GetInbound(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toInboundVO(*in))
}

func (h *handlers) updateInbound(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req inboundUpdateRequest
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	in, err := h.hosts.UpdateInbound(r.Context(), p, id, service.InboundUpdateInput{
		Remark: req.Remark, Port: req.Port, Listen: req.Listen, Config: req.Config,
	}, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toInboundVO(*in))
}

func (h *handlers) setInboundEnabled(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req enabledRequest
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	in, err := h.hosts.SetInboundEnabled(r.Context(), p, id, req.Enabled, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toInboundVO(*in))
}

func (h *handlers) ackInboundDrift(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.panels.AckInboundDrift(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

// inboundPanelConfigVO 对应 hub-api.yaml InboundPanelConfig。
type inboundPanelConfigVO struct {
	Protocol string          `json:"protocol"`
	Port     int             `json:"port"`
	Listen   string          `json:"listen"`
	Remark   string          `json:"remark"`
	Enabled  bool            `json:"enabled"`
	Config   json.RawMessage `json:"config"`
}

func (h *handlers) getInboundPanelConfig(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	cfg, err := h.panels.GetInboundPanelConfig(r.Context(), p, id, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, inboundPanelConfigVO{Protocol: cfg.Protocol, Port: cfg.Port, Listen: cfg.Listen, Remark: cfg.Remark, Enabled: cfg.Enabled, Config: cfg.Config})
}

// panelKeysRequest 对应 generatePanelKeys 的请求体。
type panelKeysRequest struct {
	Kind string `json:"kind"`
}

func (h *handlers) generatePanelKeys(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req panelKeysRequest
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	keys, err := h.panels.GeneratePanelKeys(r.Context(), p, id, req.Kind, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, keys)
}

// serverWebCertVO 对应 hub-api.yaml ServerWebCert：只有路径，没有证书内容。
type serverWebCertVO struct {
	CertFile string `json:"certFile"`
	KeyFile  string `json:"keyFile"`
}

func (h *handlers) getServerWebCert(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	files, err := h.panels.WebCertFiles(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, serverWebCertVO{CertFile: files.CertFile, KeyFile: files.KeyFile})
}

// realityScanRequest 对应 hub-api.yaml RealityScanInput。
type realityScanRequest struct {
	Target string `json:"target"`
	SNI    string `json:"sni"`
	Xver   int    `json:"xver"`
}

// realityTargetsRequest 对应 hub-api.yaml RealityTargetsInput。
type realityTargetsRequest struct {
	Targets []string `json:"targets"`
}

// realityScanVO 对应 hub-api.yaml RealityScanResult。
type realityScanVO struct {
	Target        string   `json:"target"`
	Host          string   `json:"host"`
	Port          int      `json:"port"`
	IP            string   `json:"ip"`
	Feasible      bool     `json:"feasible"`
	Reason        string   `json:"reason"`
	LatencyMs     int64    `json:"latencyMs"`
	TLSVersion    string   `json:"tlsVersion"`
	ALPN          string   `json:"alpn"`
	CertValid     bool     `json:"certValid"`
	CertSubject   string   `json:"certSubject"`
	CertIssuer    string   `json:"certIssuer"`
	ServerNames   []string `json:"serverNames"`
	PrivateTarget bool     `json:"privateTarget"`
}

func toRealityScanVO(r panel.RealityScan) realityScanVO {
	names := r.ServerNames
	if names == nil {
		names = []string{}
	}
	return realityScanVO{
		Target: r.Target, Host: r.Host, Port: r.Port, IP: r.IP, Feasible: r.Feasible, Reason: r.Reason, LatencyMs: r.LatencyMs,
		TLSVersion: r.TLSVersion, ALPN: r.ALPN, CertValid: r.CertValid, CertSubject: r.CertSubject, CertIssuer: r.CertIssuer,
		ServerNames: names, PrivateTarget: r.PrivateTarget,
	}
}

func (h *handlers) scanRealityTarget(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	var req realityScanRequest
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	res, err := h.panels.ScanRealityTarget(r.Context(), id, req.Target, req.SNI, req.Xver)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toRealityScanVO(*res))
}

func (h *handlers) scanRealityTargets(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	var req realityTargetsRequest
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	res, err := h.panels.ScanRealityTargets(r.Context(), id, req.Targets)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]realityScanVO, 0, len(res))
	for _, item := range res {
		out = append(out, toRealityScanVO(item))
	}
	writeOK(w, out)
}

func (h *handlers) deleteInbound(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.panels.DeleteInbound(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) createEndpoints(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req endpointRequest
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.panels.CreateEndpoints(r.Context(), p, id, service.EndpointInput{
		InboundIDs: req.InboundIDs, Addresses: req.Addresses, Port: req.Port, Remark: req.Remark,
	}, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) deleteEndpoint(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.panels.DeleteEndpoint(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func optionalQueryID(r *http.Request, name string) (int64, error) {
	return optionalPositiveInt64(r, name, msgBadFilter)
}

func (h *handlers) listNodes(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	serverID, err := optionalQueryID(r, queryServerID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	groupID, err := optionalQueryID(r, queryGroupID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.panels.ListNodes(r.Context(), service.NodeFilter{ServerID: serverID, GroupID: groupID}, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	reach, err := h.nodeReach(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, pageVO[nodeVO]{Total: page.Total, Items: mapSlice(page.Items, func(n service.Node) nodeVO {
		vo := nodeVO{
			ID: n.ID, InboundID: n.InboundID, EndpointID: n.EndpointID, ServerID: n.ServerID, ServerName: n.ServerName,
			Protocol: n.Protocol, Address: n.Address, Port: n.Port, DisplayName: n.DisplayName, Region: n.Region,
			Tags: n.Tags, SortOrder: n.SortOrder, IsEnabled: n.IsEnabled, IsMissing: n.IsMissing, GroupIDs: n.GroupIDs,
		}
		if vo.Tags == nil {
			vo.Tags = []string{}
		}
		if vo.GroupIDs == nil {
			vo.GroupIDs = []int64{}
		}
		if b, ok := reach[n.ID]; ok {
			vo.Reach = &nodeReachVO{Verdict: b.Verdict, Scope: b.Scope, CnOK: b.CnOK, CnTotal: b.CnTotal, CheckedAt: b.CheckedAt}
		}
		return vo
	})})
}

func (h *handlers) updateNode(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req nodeRequest
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.panels.UpdateNode(r.Context(), p, id, service.NodeInput{
		DisplayName: req.DisplayName, Tags: req.Tags, SortOrder: req.SortOrder, IsEnabled: req.IsEnabled,
	}, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

// affixNodeNames 给一批节点的显示名加前缀 / 后缀（docs/03 §5.1，Ruling-80）。
func (h *handlers) affixNodeNames(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req nodeAffixRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	updated, err := h.panels.AffixNodeNames(r.Context(), p, service.NodeAffixInput{NodeIDs: req.NodeIDs, Prefix: req.Prefix, Suffix: req.Suffix}, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nodeAffixVO{Updated: updated})
}

func toNodeGroupVO(g service.NodeGroup) nodeGroupVO {
	vo := nodeGroupVO{ID: g.ID, Name: g.Name, Remark: g.Remark, NodeIDs: g.NodeIDs}
	if vo.NodeIDs == nil {
		vo.NodeIDs = []int64{}
	}
	return vo
}

func (h *handlers) listNodeGroups(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	list, err := h.panels.ListNodeGroups(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, mapSlice(list, toNodeGroupVO))
}

func (h *handlers) createNodeGroup(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req nodeGroupRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	g, err := h.panels.CreateNodeGroup(r.Context(), p, service.NodeGroupInput{Name: req.Name, Remark: req.Remark, NodeIDs: req.NodeIDs}, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toNodeGroupVO(*g))
}

func (h *handlers) updateNodeGroup(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req nodeGroupRequest
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.panels.UpdateNodeGroup(r.Context(), p, id, service.NodeGroupInput{Name: req.Name, Remark: req.Remark, NodeIDs: req.NodeIDs}, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) deleteNodeGroup(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.panels.DeleteNodeGroup(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

type serverLocationVO struct {
	Region   string `json:"region"`
	PublicIP string `json:"publicIp"`
}

// locateServer 扫描服务器位置，只返回结果不保存（Ruling-65）。
func (h *handlers) locateServer(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	loc, err := h.panels.LocateServer(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, serverLocationVO{Region: loc.Region, PublicIP: loc.PublicIP})
}
