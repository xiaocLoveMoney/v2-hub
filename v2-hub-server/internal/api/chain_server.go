package api

import (
	"context"
	"net/http"
	"net/netip"
	"time"

	"v2-hub-server/internal/service"
)

// 管理端的链式接口：服务端链式的下发状态与重试（docs/03 §5.19）、全站规则列表、出口管理（docs/03 §5.21，Ruling-50）。

// ChainApplyManager 是下发状态与重试需要的能力（由 service.ChainApplier 实现）。
type ChainApplyManager interface {
	Status(ctx context.Context, serverID int64) (*service.ChainApply, error)
	Retry(ctx context.Context, actor *service.AdminPrincipal, serverID int64, ip *netip.Addr) (*service.ChainApply, error)
}

// ChainAdminManager 是管理端规则列表与出口管理需要的能力（由 service.ChainService 实现）。
type ChainAdminManager interface {
	ListRoutes(ctx context.Context, f service.ChainRouteFilter, pq service.PageQuery) (*service.Page[service.AdminChainRoute], error)
	ListExits(ctx context.Context, f service.ChainExitFilter, pq service.PageQuery) (*service.Page[service.AdminChainExit], error)
	CreatePlatformExit(ctx context.Context, actor *service.AdminPrincipal, in service.PlatformExitInput, ip *netip.Addr) (*service.AdminChainExit, error)
	UpdatePlatformExit(ctx context.Context, actor *service.AdminPrincipal, id int64, in service.PlatformExitInput, ip *netip.Addr) (*service.AdminChainExit, error)
	SetExitEnabled(ctx context.Context, actor *service.AdminPrincipal, id int64, enabled bool, ip *netip.Addr) (*service.AdminChainExit, error)
	DeleteExitAsAdmin(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) error
	PlatformExitLink(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) (string, error)
	TestExitAsAdmin(ctx context.Context, exitID, serverID int64, mode string) (*service.ExitTestResult, error)
}

const (
	queryOwner = "owner"
)

// chainApplyVO 对应 hub-api.yaml ChainApply。
type chainApplyVO struct {
	ServerID         int64      `json:"serverId"`
	IsSupported      bool       `json:"isSupported"`
	Status           string     `json:"status"`
	RuleCount        int        `json:"ruleCount"`
	AppliedRuleCount int        `json:"appliedRuleCount"`
	FailCount        int        `json:"failCount"`
	IsRetryStopped   bool       `json:"isRetryStopped"`
	LastError        string     `json:"lastError"`
	LastAttemptAt    *time.Time `json:"lastAttemptAt"`
	NextAttemptAt    *time.Time `json:"nextAttemptAt"`
	AppliedAt        *time.Time `json:"appliedAt"`
}

func toChainApplyVO(a *service.ChainApply) chainApplyVO {
	return chainApplyVO{
		ServerID: a.ServerID, IsSupported: a.Supported, Status: a.Status, RuleCount: a.RuleCount, AppliedRuleCount: a.AppliedRuleCount,
		FailCount: a.FailCount, IsRetryStopped: a.RetryStopped, LastError: a.LastError,
		LastAttemptAt: a.LastAttemptAt, NextAttemptAt: a.NextAttemptAt, AppliedAt: a.AppliedAt,
	}
}

func (h *handlers) getServerChainApply(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a, err := h.chainApply.Status(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toChainApplyVO(a))
}

func (h *handlers) retryServerChainApply(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a, err := h.chainApply.Retry(r.Context(), p, id, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toChainApplyVO(a))
}

type adminChainUserVO struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// adminChainRouteEntryVO 对应 hub-api.yaml AdminChainRouteEntry。
type adminChainRouteEntryVO struct {
	NodeID     int64  `json:"nodeId"`
	Name       string `json:"name"`
	ServerID   int64  `json:"serverId"`
	ServerName string `json:"serverName"`
	State      string `json:"state"`
	CoveredBy  int    `json:"coveredBy"`
}

// adminChainRouteVO 对应 hub-api.yaml AdminChainRoute。
type adminChainRouteVO struct {
	ID            int64                    `json:"id"`
	User          adminChainUserVO         `json:"user"`
	Kind          string                   `json:"kind"`
	Position      int                      `json:"position"`
	Name          string                   `json:"name"`
	Entries       []adminChainRouteEntryVO `json:"entries"`
	Exit          chainRouteExitVO         `json:"exit"`
	IsEnabled     bool                     `json:"isEnabled"`
	Status        string                   `json:"status"`
	BlockedReason string                   `json:"blockedReason"`
	CreatedAt     time.Time                `json:"createdAt"`
}

func toAdminChainRouteVO(r service.AdminChainRoute) adminChainRouteVO {
	return adminChainRouteVO{
		ID: r.ID, Kind: r.Kind, Position: r.Position, Name: r.Name, IsEnabled: r.Enabled, Status: r.Status,
		BlockedReason: r.BlockedReason, CreatedAt: r.CreatedAt, Exit: toChainRouteExitVO(r.Exit),
		User: adminChainUserVO{ID: r.UserID, Username: r.Username},
		Entries: mapSlice(r.Entries, func(e service.ChainRouteEntry) adminChainRouteEntryVO {
			return adminChainRouteEntryVO{
				NodeID: e.NodeID, Name: e.Name, ServerID: e.ServerID, ServerName: e.ServerName, State: e.State, CoveredBy: e.CoveredBy,
			}
		}),
	}
}

func (h *handlers) listChainRoutes(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	userID, err := optionalUserID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	serverID, err := optionalQueryID(r, queryServerID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	f := service.ChainRouteFilter{Kind: r.URL.Query().Get(queryKind), UserID: userID, ServerID: serverID}
	page, err := h.chainAdmin.ListRoutes(r.Context(), f, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, pageVO[adminChainRouteVO]{Items: mapSlice(page.Items, toAdminChainRouteVO), Total: page.Total})
}

type exitNodeGroupVO struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// adminChainExitVO 对应 hub-api.yaml AdminChainExit；原链接不在这里返回。
type adminChainExitVO struct {
	ID          int64             `json:"id"`
	Owner       string            `json:"owner"`
	User        *adminChainUserVO `json:"user"`
	Name        string            `json:"name"`
	Protocol    string            `json:"protocol"`
	Address     string            `json:"address"`
	Port        int               `json:"port"`
	Remark      string            `json:"remark"`
	IsEnabled   bool              `json:"isEnabled"`
	AllowServer bool              `json:"allowServer"`
	AllowClient bool              `json:"allowClient"`
	NodeGroups  []exitNodeGroupVO `json:"nodeGroups"`
	RouteCount  int               `json:"routeCount"`
	CreatedAt   time.Time         `json:"createdAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
}

func toAdminChainExitVO(e service.AdminChainExit) adminChainExitVO {
	vo := adminChainExitVO{
		ID: e.ID, Owner: service.ExitOwnerPlatform, Name: e.Name, Protocol: e.Protocol, Address: e.Address, Port: e.Port,
		Remark: e.Remark, IsEnabled: e.Enabled, AllowServer: e.AllowServer, AllowClient: e.AllowClient,
		RouteCount: e.RouteCount, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
		NodeGroups: mapSlice(e.NodeGroups, func(g service.ExitNodeGroup) exitNodeGroupVO { return exitNodeGroupVO{ID: g.ID, Name: g.Name} }),
	}
	if e.UserID != 0 {
		vo.Owner = service.ExitOwnerUser
		vo.User = &adminChainUserVO{ID: e.UserID, Username: e.Username}
	}
	return vo
}

// platformExitRequest 对应 hub-api.yaml AdminChainExitInput / AdminChainExitUpdateInput；link 属于凭据，不得写日志。
// 新建时 isEnabled 省略按启用。
type platformExitRequest struct {
	Name         string  `json:"name"`
	Link         string  `json:"link"`
	Remark       string  `json:"remark"`
	IsEnabled    *bool   `json:"isEnabled"`
	AllowServer  bool    `json:"allowServer"`
	AllowClient  bool    `json:"allowClient"`
	NodeGroupIDs []int64 `json:"nodeGroupIds"`
}

func (req platformExitRequest) toInput() service.PlatformExitInput {
	in := service.PlatformExitInput{
		Name: req.Name, Link: req.Link, Remark: req.Remark, Enabled: true,
		AllowServer: req.AllowServer, AllowClient: req.AllowClient, NodeGroupIDs: req.NodeGroupIDs,
	}
	if req.IsEnabled != nil {
		in.Enabled = *req.IsEnabled
	}
	return in
}

// chainExitEnabledRequest 对应 hub-api.yaml ChainExitEnabledInput。
type chainExitEnabledRequest struct {
	IsEnabled bool `json:"isEnabled"`
}

func (h *handlers) listChainExits(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	userID, err := optionalUserID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	f := service.ChainExitFilter{Owner: r.URL.Query().Get(queryOwner), UserID: userID, Keyword: r.URL.Query().Get(queryKeyword)}
	page, err := h.chainAdmin.ListExits(r.Context(), f, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, pageVO[adminChainExitVO]{Items: mapSlice(page.Items, toAdminChainExitVO), Total: page.Total})
}

func (h *handlers) createPlatformChainExit(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req platformExitRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	e, err := h.chainAdmin.CreatePlatformExit(r.Context(), p, req.toInput(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toAdminChainExitVO(*e))
}

func (h *handlers) updatePlatformChainExit(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req platformExitRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	e, err := h.chainAdmin.UpdatePlatformExit(r.Context(), p, id, req.toInput(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toAdminChainExitVO(*e))
}

func (h *handlers) setChainExitEnabled(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req chainExitEnabledRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	e, err := h.chainAdmin.SetExitEnabled(r.Context(), p, id, req.IsEnabled, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toAdminChainExitVO(*e))
}

func (h *handlers) deleteChainExitAsAdmin(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.chainAdmin.DeleteExitAsAdmin(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) getPlatformChainExitLink(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	link, err := h.chainAdmin.PlatformExitLink(r.Context(), p, id, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nodeLinkVO{Link: link})
}

// adminChainExitTestRequest 对应 hub-api.yaml AdminChainExitTestInput。
type adminChainExitTestRequest struct {
	ServerID int64  `json:"serverId"`
	Mode     string `json:"mode"`
}

// adminChainExitTestVO 对应 hub-api.yaml AdminChainExitTestResult：比用户侧多面板的错误原文。
type adminChainExitTestVO struct {
	chainExitTestVO
	Error string `json:"error"`
}

func (h *handlers) testChainExitAsAdmin(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req adminChainExitTestRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	res, err := h.chainAdmin.TestExitAsAdmin(r.Context(), id, req.ServerID, req.Mode)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, adminChainExitTestVO{chainExitTestVO: toChainExitTestVO(res), Error: res.Error})
}
