package api

import (
	"context"
	"net/http"
	"net/netip"
	"time"

	"v2-hub-server/internal/manager/deploy"
	"v2-hub-server/internal/service"
)

// HostManager 是 SSH 托管接口需要的能力（docs/03 §5.22，由 service.HostService 实现）。
type HostManager interface {
	ListCredentials(ctx context.Context) ([]service.SSHCredential, error)
	CreateCredential(ctx context.Context, actor *service.AdminPrincipal, in service.SSHCredentialInput, ip *netip.Addr) (*service.SSHCredential, error)
	UpdateCredential(ctx context.Context, actor *service.AdminPrincipal, id int64, in service.SSHCredentialInput, ip *netip.Addr) (*service.SSHCredential, error)
	DeleteCredential(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) error
	ListDNSAccounts(ctx context.Context) ([]service.DNSAccount, error)
	CreateDNSAccount(ctx context.Context, actor *service.AdminPrincipal, in service.DNSAccountInput, ip *netip.Addr) (*service.DNSAccount, error)
	UpdateDNSAccount(ctx context.Context, actor *service.AdminPrincipal, id int64, in service.DNSAccountInput, ip *netip.Addr) (*service.DNSAccount, error)
	DeleteDNSAccount(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) error
	ListHosts(ctx context.Context) ([]service.ServerHost, error)
	GetHost(ctx context.Context, id int64) (*service.ServerHost, error)
	CreateHosts(ctx context.Context, actor *service.AdminPrincipal, in service.HostBatchInput, ip *netip.Addr) ([]service.ServerHost, error)
	UpdateHost(ctx context.Context, actor *service.AdminPrincipal, id int64, in service.ServerHostInput, ip *netip.Addr) (*service.ServerHost, error)
	DeleteHost(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) error
	ResetHostKey(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) (*service.ServerHost, error)
	BindServer(ctx context.Context, actor *service.AdminPrincipal, serverID int64, in service.HostBindInput, ip *netip.Addr) (*service.ServerHost, error)
	TestHost(ctx context.Context, actor *service.AdminPrincipal, in service.HostTestInput) (*service.HostDetection, error)
	ListHostTasks(ctx context.Context, hostID int64) ([]service.HostTask, error)
	GetTask(ctx context.Context, id int64) (*service.HostTaskDetail, error)
	CreateAction(ctx context.Context, actor *service.AdminPrincipal, hostID int64, kind string, ip *netip.Addr) (*service.HostTask, error)
	CancelTask(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) (*service.HostTask, error)
	ResetPanelLogin(ctx context.Context, actor *service.AdminPrincipal, hostID int64, ip *netip.Addr) (*deploy.PanelLogin, error)
	PreviewInboundPresets(ctx context.Context, actor *service.AdminPrincipal, serverID int64) (*service.InboundPresetPreview, error)
	// CreateInbound / UpdateInbound / SetInboundEnabled 是带主机端口检查的入站写入（Ruling-78）。
	CreateInbound(ctx context.Context, actor *service.AdminPrincipal, serverID int64, in service.InboundCreateInput, ip *netip.Addr) (*service.Inbound, error)
	UpdateInbound(ctx context.Context, actor *service.AdminPrincipal, id int64, in service.InboundUpdateInput, ip *netip.Addr) (*service.Inbound, error)
	SetInboundEnabled(ctx context.Context, actor *service.AdminPrincipal, id int64, enabled bool, ip *netip.Addr) (*service.Inbound, error)
	DeployInboundPresets(ctx context.Context, actor *service.AdminPrincipal, serverID int64, in service.InboundPresetDeployInput, ip *netip.Addr) (*service.HostTask, error)
}

type sshCredentialVO struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	Username       string    `json:"username"`
	AuthType       string    `json:"authType"`
	KeyFingerprint string    `json:"keyFingerprint"`
	Remark         string    `json:"remark"`
	HostCount      int       `json:"hostCount"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func toSSHCredentialVO(c service.SSHCredential) sshCredentialVO {
	return sshCredentialVO{
		ID: c.ID, Name: c.Name, Username: c.Username, AuthType: c.AuthType, KeyFingerprint: c.KeyFingerprint,
		Remark: c.Remark, HostCount: c.HostCount, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

// sshCredentialRequest 的 secret、passphrase 只写：响应里没有对应字段。
type sshCredentialRequest struct {
	Name       string `json:"name"`
	Username   string `json:"username"`
	AuthType   string `json:"authType"`
	Secret     string `json:"secret"`
	Passphrase string `json:"passphrase"`
	Remark     string `json:"remark"`
}

func (r sshCredentialRequest) toInput() service.SSHCredentialInput {
	return service.SSHCredentialInput{Name: r.Name, Username: r.Username, AuthType: r.AuthType, Secret: r.Secret, Passphrase: r.Passphrase, Remark: r.Remark}
}

type dnsAccountVO struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	ProviderType string    `json:"providerType"`
	Zone         string    `json:"zone"`
	Remark       string    `json:"remark"`
	HostCount    int       `json:"hostCount"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

func toDNSAccountVO(a service.DNSAccount) dnsAccountVO {
	return dnsAccountVO{
		ID: a.ID, Name: a.Name, ProviderType: a.ProviderType, Zone: a.Zone, Remark: a.Remark,
		HostCount: a.HostCount, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
}

// dnsAccountRequest 的 token 只写：响应里没有对应字段。
type dnsAccountRequest struct {
	Name         string `json:"name"`
	ProviderType string `json:"providerType"`
	Zone         string `json:"zone"`
	Token        string `json:"token"`
	Remark       string `json:"remark"`
}

func (r dnsAccountRequest) toInput() service.DNSAccountInput {
	return service.DNSAccountInput{Name: r.Name, ProviderType: r.ProviderType, Zone: r.Zone, Token: r.Token, Remark: r.Remark}
}

type hostTaskVO struct {
	ID            int64      `json:"id"`
	HostID        int64      `json:"hostId"`
	Kind          string     `json:"kind"`
	Status        string     `json:"status"`
	Step          string     `json:"step"`
	ErrorCode     string     `json:"errorCode"`
	LastError     string     `json:"lastError"`
	CreatedByName string     `json:"createdByName"`
	CreatedAt     time.Time  `json:"createdAt"`
	StartedAt     *time.Time `json:"startedAt"`
	FinishedAt    *time.Time `json:"finishedAt"`
}

func toHostTaskVO(t service.HostTask) hostTaskVO {
	return hostTaskVO{
		ID: t.ID, HostID: t.HostID, Kind: t.Kind, Status: t.Status, Step: t.Step, ErrorCode: t.ErrorCode, LastError: t.LastError,
		CreatedByName: t.CreatedByName, CreatedAt: t.CreatedAt, StartedAt: t.StartedAt, FinishedAt: t.FinishedAt,
	}
}

type hostTaskDetailVO struct {
	hostTaskVO
	Log string `json:"log"`
}

type serverHostVO struct {
	ID             int64       `json:"id"`
	Name           string      `json:"name"`
	Address        string      `json:"address"`
	SSHPort        int         `json:"sshPort"`
	CredentialID   int64       `json:"credentialId"`
	CredentialName string      `json:"credentialName"`
	ProviderType   string      `json:"providerType"`
	Region         string      `json:"region"`
	PanelPort      *int        `json:"panelPort"`
	HostKeySHA256  string      `json:"hostKeySha256"`
	OSID           string      `json:"osId"`
	Arch           string      `json:"arch"`
	ServerID       *int64      `json:"serverId"`
	ServerName     string      `json:"serverName"`
	DNSAccountID   *int64      `json:"dnsAccountId"`
	DNSAccountName string      `json:"dnsAccountName"`
	Domain         string      `json:"domain"`
	CertNotAfter   *time.Time  `json:"certNotAfter"`
	LastTask       *hostTaskVO `json:"lastTask"`
	CreatedAt      time.Time   `json:"createdAt"`
}

func toServerHostVO(h service.ServerHost) serverHostVO {
	vo := serverHostVO{
		ID: h.ID, Name: h.Name, Address: h.Address, SSHPort: h.SSHPort, CredentialID: h.CredentialID, CredentialName: h.CredentialName,
		ProviderType: h.ProviderType, Region: h.Region, PanelPort: h.PanelPort, HostKeySHA256: h.HostKeySHA256, OSID: h.OSID, Arch: h.Arch,
		ServerID: h.ServerID, ServerName: h.ServerName, CreatedAt: h.CreatedAt,
		DNSAccountID: h.DNSAccountID, DNSAccountName: h.DNSAccountName, Domain: h.Domain, CertNotAfter: h.CertNotAfter,
	}
	if h.LastTask != nil {
		task := toHostTaskVO(*h.LastTask)
		vo.LastTask = &task
	}
	return vo
}

type serverHostRequest struct {
	Name         string `json:"name"`
	Address      string `json:"address"`
	SSHPort      int    `json:"sshPort"`
	CredentialID int64  `json:"credentialId"`
	Region       string `json:"region"`
	PanelPort    *int   `json:"panelPort"`
	DNSAccountID *int64 `json:"dnsAccountId"`
	Domain       string `json:"domain"`
}

type serverHostBatchItem struct {
	Address string `json:"address"`
	Name    string `json:"name"`
	Region  string `json:"region"`
	Domain  string `json:"domain"`
}

type serverHostBatchRequest struct {
	CredentialID int64                 `json:"credentialId"`
	SSHPort      int                   `json:"sshPort"`
	ProviderType string                `json:"providerType"`
	PanelPort    *int                  `json:"panelPort"`
	DNSAccountID *int64                `json:"dnsAccountId"`
	Hosts        []serverHostBatchItem `json:"hosts"`
}

func (r serverHostBatchRequest) toInput() service.HostBatchInput {
	return service.HostBatchInput{
		CredentialID: r.CredentialID, SSHPort: r.SSHPort, ProviderType: r.ProviderType, PanelPort: r.PanelPort, DNSAccountID: r.DNSAccountID,
		Hosts: mapSlice(r.Hosts, func(i serverHostBatchItem) service.HostBatchItem {
			return service.HostBatchItem{Address: i.Address, Name: i.Name, Region: i.Region, Domain: i.Domain}
		}),
	}
}

type serverHostTestRequest struct {
	Address      string `json:"address"`
	SSHPort      int    `json:"sshPort"`
	CredentialID int64  `json:"credentialId"`
	ProviderType string `json:"providerType"`
}

type serverHostBindRequest struct {
	Address      string `json:"address"`
	SSHPort      int    `json:"sshPort"`
	CredentialID int64  `json:"credentialId"`
}

type hostDetectionVO struct {
	HostKeySHA256 string `json:"hostKeySha256"`
	OSID          string `json:"osId"`
	Arch          string `json:"arch"`
	IsPrivileged  bool   `json:"isPrivileged"`
	PanelState    string `json:"panelState"`
	PanelVersion  string `json:"panelVersion"`
	Plan          string `json:"plan"`
	Reason        string `json:"reason"`
}

type hostActionRequest struct {
	Action string `json:"action"`
}

// panelLoginVO 只出现在重置口令的那一次响应里。
type panelLoginVO struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *handlers) listSSHCredentials(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	list, err := h.hosts.ListCredentials(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, mapSlice(list, toSSHCredentialVO))
}

func (h *handlers) createSSHCredential(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req sshCredentialRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	c, err := h.hosts.CreateCredential(r.Context(), p, req.toInput(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toSSHCredentialVO(*c))
}

func (h *handlers) updateSSHCredential(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req sshCredentialRequest
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	c, err := h.hosts.UpdateCredential(r.Context(), p, id, req.toInput(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toSSHCredentialVO(*c))
}

func (h *handlers) deleteSSHCredential(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.hosts.DeleteCredential(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) listDNSAccounts(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	list, err := h.hosts.ListDNSAccounts(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, mapSlice(list, toDNSAccountVO))
}

func (h *handlers) createDNSAccount(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req dnsAccountRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	a, err := h.hosts.CreateDNSAccount(r.Context(), p, req.toInput(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toDNSAccountVO(*a))
}

func (h *handlers) updateDNSAccount(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req dnsAccountRequest
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a, err := h.hosts.UpdateDNSAccount(r.Context(), p, id, req.toInput(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toDNSAccountVO(*a))
}

func (h *handlers) deleteDNSAccount(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.hosts.DeleteDNSAccount(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) listServerHosts(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	list, err := h.hosts.ListHosts(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, mapSlice(list, toServerHostVO))
}

func (h *handlers) createServerHosts(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req serverHostBatchRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	list, err := h.hosts.CreateHosts(r.Context(), p, req.toInput(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, mapSlice(list, toServerHostVO))
}

func (h *handlers) testServerHost(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req serverHostTestRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	det, err := h.hosts.TestHost(r.Context(), p, service.HostTestInput{
		Address: req.Address, SSHPort: req.SSHPort, CredentialID: req.CredentialID, ProviderType: req.ProviderType,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, hostDetectionVO{
		HostKeySHA256: det.HostKeySHA256, OSID: det.OSID, Arch: det.Arch, IsPrivileged: det.IsPrivileged,
		PanelState: string(det.PanelState), PanelVersion: det.PanelVersion, Plan: string(det.Plan), Reason: string(det.Reason),
	})
}

// writeHost 输出一台主机；err 非空时输出错误。
func writeHost(w http.ResponseWriter, r *http.Request, host *service.ServerHost, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toServerHostVO(*host))
}

// writeTask 输出一条任务；err 非空时输出错误。
func writeTask(w http.ResponseWriter, r *http.Request, task *service.HostTask, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toHostTaskVO(*task))
}

func (h *handlers) getServerHost(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	host, err := h.hosts.GetHost(r.Context(), id)
	writeHost(w, r, host, err)
}

func (h *handlers) updateServerHost(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req serverHostRequest
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	host, err := h.hosts.UpdateHost(r.Context(), p, id, service.ServerHostInput{
		Name: req.Name, Address: req.Address, SSHPort: req.SSHPort, CredentialID: req.CredentialID, Region: req.Region, PanelPort: req.PanelPort,
		DNSAccountID: req.DNSAccountID, Domain: req.Domain,
	}, clientIP(r, h.trustedProxies))
	writeHost(w, r, host, err)
}

func (h *handlers) deleteServerHost(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.hosts.DeleteHost(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) resetServerHostKey(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	host, err := h.hosts.ResetHostKey(r.Context(), p, id, clientIP(r, h.trustedProxies))
	writeHost(w, r, host, err)
}

func (h *handlers) listServerHostTasks(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	tasks, err := h.hosts.ListHostTasks(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, mapSlice(tasks, toHostTaskVO))
}

func (h *handlers) createServerHostAction(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req hostActionRequest
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	task, err := h.hosts.CreateAction(r.Context(), p, id, req.Action, clientIP(r, h.trustedProxies))
	writeTask(w, r, task, err)
}

func (h *handlers) resetServerHostPanelLogin(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	login, err := h.hosts.ResetPanelLogin(r.Context(), p, id, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, panelLoginVO{Username: login.Username, Password: login.Password})
}

type inboundPresetOptionVO struct {
	Key       string `json:"key"`
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
	Port      int    `json:"port"`
	NeedsCert bool   `json:"needsCert"`
}

type inboundPresetPreviewVO struct {
	HostID    int64                   `json:"hostId"`
	Domain    string                  `json:"domain"`
	CertReady bool                    `json:"certReady"`
	Items     []inboundPresetOptionVO `json:"items"`
}

// requireHostWrite 是一键部署在 node:write（路由已查）之外要求的 host:write（Ruling-76）。
func requireHostWrite(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) bool {
	if p.Can(service.PermHostWrite) {
		return true
	}
	writeError(w, r, service.ErrPermissionDenied)
	return false
}

// previewInboundPresets 一键部署入站的预览（docs/03 §5.1，Ruling-76）。
func (h *handlers) previewInboundPresets(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	if !requireHostWrite(w, r, p) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	preview, err := h.hosts.PreviewInboundPresets(r.Context(), p, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]inboundPresetOptionVO, 0, len(preview.Items))
	for _, it := range preview.Items {
		items = append(items, inboundPresetOptionVO{Key: it.Key, Available: it.Available, Reason: it.Reason, Port: it.Port, NeedsCert: it.NeedsCert})
	}
	writeOK(w, inboundPresetPreviewVO{HostID: preview.HostID, Domain: preview.Domain, CertReady: preview.CertReady, Items: items})
}

// deployInboundPresets 一键部署入站，排一条 deploy_inbounds 主机任务（Ruling-76）。
func (h *handlers) deployInboundPresets(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	if !requireHostWrite(w, r, p) {
		return
	}
	var req service.InboundPresetDeployInput
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if req.NodeGroupIDs == nil {
		req.NodeGroupIDs = []int64{}
	}
	task, err := h.hosts.DeployInboundPresets(r.Context(), p, id, req, clientIP(r, h.trustedProxies))
	writeTask(w, r, task, err)
}

func (h *handlers) getHostTask(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	task, err := h.hosts.GetTask(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, hostTaskDetailVO{hostTaskVO: toHostTaskVO(task.HostTask), Log: task.Log})
}

func (h *handlers) cancelHostTask(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	task, err := h.hosts.CancelTask(r.Context(), p, id, clientIP(r, h.trustedProxies))
	writeTask(w, r, task, err)
}

func (h *handlers) bindServerHost(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req serverHostBindRequest
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	host, err := h.hosts.BindServer(r.Context(), p, id, service.HostBindInput{Address: req.Address, SSHPort: req.SSHPort, CredentialID: req.CredentialID}, clientIP(r, h.trustedProxies))
	writeHost(w, r, host, err)
}
