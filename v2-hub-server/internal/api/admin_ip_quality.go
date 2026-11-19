package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"time"

	"v2-hub-server/internal/manager/ipquality"
	"v2-hub-server/internal/service"
)

// IPQualityManager 是服务器 IP 质量检测接口需要的能力（docs/03 §5.23，由 service.IPQualityService 实现）。
type IPQualityManager interface {
	StartCheck(ctx context.Context, actor *service.AdminPrincipal, serverID int64, ip *netip.Addr) (*service.IPCheck, error)
	ListChecks(ctx context.Context, serverID int64, pq service.PageQuery) (*service.Page[service.IPCheck], error)
	GetCheckDetail(ctx context.Context, id int64) (*service.IPCheckDetail, error)
	Sources() []ipquality.SourceInfo
}

// ipCheckVO 对应 hub-api.yaml IpCheck。
type ipCheckVO struct {
	ID            int64      `json:"id"`
	ServerID      int64      `json:"serverId"`
	Status        string     `json:"status"`
	Origin        string     `json:"origin"`
	Scope         string     `json:"scope"`
	HostProbe     string     `json:"hostProbe"`
	IP            string     `json:"ip"`
	IPSource      string     `json:"ipSource"`
	Country       string     `json:"country"`
	RegCountry    string     `json:"regCountry"`
	IPKind        string     `json:"ipKind"`
	ASN           *int64     `json:"asn"`
	ASOrg         string     `json:"asOrg"`
	UsageType     string     `json:"usageType"`
	RiskLevel     string     `json:"riskLevel"`
	DNSBLListed   int        `json:"dnsblListed"`
	DNSBLTotal    int        `json:"dnsblTotal"`
	MediaOK       int        `json:"mediaOk"`
	MediaTotal    int        `json:"mediaTotal"`
	Port25        string     `json:"port25"`
	ErrorCode     string     `json:"errorCode"`
	LastError     string     `json:"lastError"`
	CreatedByName string     `json:"createdByName"`
	CreatedAt     time.Time  `json:"createdAt"`
	StartedAt     *time.Time `json:"startedAt"`
	FinishedAt    *time.Time `json:"finishedAt"`
}

// ipCheckDetailVO 对应 IpCheckDetail：报告原样透传（service 已按契约的形状编码），还没执行完为 null。
type ipCheckDetailVO struct {
	ipCheckVO
	Result json.RawMessage `json:"result"`
}

var jsonNull = json.RawMessage("null")

func toIPCheckVO(c service.IPCheck) ipCheckVO {
	return ipCheckVO{
		ID: c.ID, ServerID: c.ServerID, Status: c.Status, Origin: c.Origin, Scope: c.Scope, HostProbe: c.HostProbe,
		IP: c.IP, IPSource: c.IPSource, Country: c.Country, RegCountry: c.RegCountry, IPKind: c.IPKind, ASN: c.ASN,
		ASOrg: c.ASOrg, UsageType: c.UsageType, RiskLevel: c.RiskLevel, DNSBLListed: c.DNSBLListed, DNSBLTotal: c.DNSBLTotal,
		MediaOK: c.MediaOK, MediaTotal: c.MediaTotal, Port25: c.Port25, ErrorCode: c.ErrorCode, LastError: c.LastError,
		CreatedByName: c.CreatedByName, CreatedAt: c.CreatedAt, StartedAt: c.StartedAt, FinishedAt: c.FinishedAt,
	}
}

// ipQualityOn 在没接 IP 质量检测时回 404。
func (h *handlers) ipQualityOn(w http.ResponseWriter, r *http.Request) bool {
	if h.ipQuality != nil {
		return true
	}
	writeError(w, r, service.ErrNotFound)
	return false
}

func (h *handlers) listServerIPChecks(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	if !h.ipQualityOn(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.ipQuality.ListChecks(r.Context(), id, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, pageVO[ipCheckVO]{Items: mapSlice(page.Items, toIPCheckVO), Total: page.Total})
}

// startServerIPCheck 发起一次检测（server:write，路由已查）；有没有 host:write 由 service 决定检测范围（Ruling-81）。
func (h *handlers) startServerIPCheck(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	if !h.ipQualityOn(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	c, err := h.ipQuality.StartCheck(r.Context(), p, id, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toIPCheckVO(*c))
}

func (h *handlers) getIPCheck(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	if !h.ipQualityOn(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	d, err := h.ipQuality.GetCheckDetail(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	result := d.Result
	if len(result) == 0 {
		result = jsonNull
	}
	writeOK(w, ipCheckDetailVO{ipCheckVO: toIPCheckVO(d.IPCheck), Result: result})
}
