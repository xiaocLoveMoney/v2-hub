package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"v2-hub-server/internal/service"
)

// 用户侧链式代理接口（docs/03 §5.18、§5.19、§5.21）：有序的链式规则与我的出口。身份只取自会话（R8）。

// ChainManager 是链式代理接口需要的能力（由 service.ChainService 实现）。
type ChainManager interface {
	Features(ctx context.Context, userID int64) (service.ChainFlags, error)
	Overview(ctx context.Context, p *service.UserPrincipal) (*service.ChainOverview, error)
	CreateRoute(ctx context.Context, p *service.UserPrincipal, in service.ChainRouteInput) (*service.ChainRoute, error)
	UpdateRoute(ctx context.Context, p *service.UserPrincipal, id int64, in service.ChainRouteInput) (*service.ChainRoute, error)
	DeleteRoute(ctx context.Context, p *service.UserPrincipal, id int64) error
	ReorderRoutes(ctx context.Context, p *service.UserPrincipal, kind string, ids []int64) ([]service.ChainRoute, error)
	CreateExit(ctx context.Context, p *service.UserPrincipal, link, name string) (*service.ChainExit, error)
	UpdateExit(ctx context.Context, p *service.UserPrincipal, id int64, name, link string) (*service.ChainExit, error)
	DeleteExit(ctx context.Context, p *service.UserPrincipal, id int64) error
	ExitLink(ctx context.Context, p *service.UserPrincipal, id int64) (string, error)
	TestExit(ctx context.Context, p *service.UserPrincipal, exitID, nodeID int64, mode string) (*service.ExitTestResult, error)
}

// userFeaturesVO 对应 hub-api.yaml UserSession.features：当前套餐开了哪些功能，决定菜单显隐。
type userFeaturesVO struct {
	ClientChain bool `json:"clientChain"`
	CustomNode  bool `json:"customNode"`
	ServerChain bool `json:"serverChain"`
}

// sessionVO 组装会话信息并带上套餐的功能开关。开关读不到时按都未开返回并留痕：
// 它只影响菜单显隐，后端各接口仍做最终校验，不能因此让登录失败。
func (h *handlers) sessionVO(r *http.Request, p *service.UserPrincipal) userSessionVO {
	vo := toUserSessionVO(p)
	flags, err := h.chain.Features(r.Context(), p.UserID)
	if err != nil {
		slog.Error("load chain features", "user_id", p.UserID, "err", err)
		return vo
	}
	vo.Features = userFeaturesVO{ClientChain: flags.Chain, CustomNode: flags.CustomNode, ServerChain: flags.ServerChain}
	return vo
}

// chainNodeOptionVO 对应 hub-api.yaml ChainNodeOption。
type chainNodeOptionVO struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	Region           string `json:"region"`
	Protocol         string `json:"protocol"`
	CanBeExit        bool   `json:"canBeExit"`
	CanBeServerEntry bool   `json:"canBeServerEntry"`
}

// chainPlatformExitOptionVO 对应 hub-api.yaml ChainPlatformExitOption；地址与链接不对用户返回。
type chainPlatformExitOptionVO struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Protocol    string `json:"protocol"`
	AllowServer bool   `json:"allowServer"`
	AllowClient bool   `json:"allowClient"`
}

// chainRouteEntryVO 对应 hub-api.yaml ChainRouteEntry。
type chainRouteEntryVO struct {
	NodeID    int64  `json:"nodeId"`
	Name      string `json:"name"`
	State     string `json:"state"`
	CoveredBy int    `json:"coveredBy"`
}

// chainRouteExitVO 对应 hub-api.yaml ChainRouteExit。
type chainRouteExitVO struct {
	Kind     string `json:"kind"`
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
}

// chainRouteVO 对应 hub-api.yaml ChainRoute。
type chainRouteVO struct {
	ID            int64               `json:"id"`
	Kind          string              `json:"kind"`
	Position      int                 `json:"position"`
	Name          string              `json:"name"`
	DisplayName   string              `json:"displayName"`
	Entries       []chainRouteEntryVO `json:"entries"`
	Exit          chainRouteExitVO    `json:"exit"`
	IsEnabled     bool                `json:"isEnabled"`
	Status        string              `json:"status"`
	BlockedReason string              `json:"blockedReason"`
	CreatedAt     time.Time           `json:"createdAt"`
}

func toChainRouteExitVO(e service.ChainRouteExit) chainRouteExitVO {
	return chainRouteExitVO{Kind: e.Kind, ID: e.ID, Name: e.Name, Protocol: e.Protocol}
}

func toChainRouteVO(r service.ChainRoute) chainRouteVO {
	return chainRouteVO{
		ID: r.ID, Kind: r.Kind, Position: r.Position, Name: r.Name, DisplayName: r.DisplayName, IsEnabled: r.Enabled,
		Status: r.Status, BlockedReason: r.BlockedReason, CreatedAt: r.CreatedAt, Exit: toChainRouteExitVO(r.Exit),
		Entries: mapSlice(r.Entries, func(e service.ChainRouteEntry) chainRouteEntryVO {
			return chainRouteEntryVO{NodeID: e.NodeID, Name: e.Name, State: e.State, CoveredBy: e.CoveredBy}
		}),
	}
}

// chainExitVO 对应 hub-api.yaml ChainExit；原链接属于凭据，不在这里返回。
type chainExitVO struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Protocol   string    `json:"protocol"`
	Address    string    `json:"address"`
	Port       int       `json:"port"`
	IsEnabled  bool      `json:"isEnabled"`
	RouteCount int       `json:"routeCount"`
	CreatedAt  time.Time `json:"createdAt"`
}

func toChainExitVO(e service.ChainExit) chainExitVO {
	return chainExitVO{
		ID: e.ID, Name: e.Name, Protocol: e.Protocol, Address: e.Address, Port: e.Port, IsEnabled: e.Enabled,
		RouteCount: e.RouteCount, CreatedAt: e.CreatedAt,
	}
}

// chainOverviewVO 对应 hub-api.yaml ChainOverview。
type chainOverviewVO struct {
	Allowed         bool                        `json:"allowed"`
	ExitAllowed     bool                        `json:"exitAllowed"`
	ServerAllowed   bool                        `json:"serverAllowed"`
	MaxClientRoutes int                         `json:"maxClientRoutes"`
	MaxServerRoutes int                         `json:"maxServerRoutes"`
	MaxRouteEntries int                         `json:"maxRouteEntries"`
	MaxExits        int                         `json:"maxExits"`
	ClientRoutes    []chainRouteVO              `json:"clientRoutes"`
	ServerRoutes    []chainRouteVO              `json:"serverRoutes"`
	Exits           []chainExitVO               `json:"exits"`
	PlatformExits   []chainPlatformExitOptionVO `json:"platformExits"`
	Nodes           []chainNodeOptionVO         `json:"nodes"`
}

// chainRouteRequest 对应 hub-api.yaml ChainRouteInput / ChainRouteUpdateInput；新建时 isEnabled 省略按启用，修改时 kind 被忽略。
type chainRouteRequest struct {
	Kind         string  `json:"kind"`
	EntryNodeIDs []int64 `json:"entryNodeIds"`
	ExitKind     string  `json:"exitKind"`
	ExitID       int64   `json:"exitId"`
	Name         string  `json:"name"`
	IsEnabled    *bool   `json:"isEnabled"`
}

func (req chainRouteRequest) toInput() service.ChainRouteInput {
	in := service.ChainRouteInput{
		Kind: req.Kind, EntryNodeIDs: req.EntryNodeIDs, ExitKind: req.ExitKind, ExitID: req.ExitID, Name: req.Name, Enabled: true,
	}
	if req.IsEnabled != nil {
		in.Enabled = *req.IsEnabled
	}
	return in
}

// chainRouteOrderRequest 对应 hub-api.yaml ChainRouteOrderInput。
type chainRouteOrderRequest struct {
	Kind string  `json:"kind"`
	IDs  []int64 `json:"ids"`
}

// chainExitRequest 对应 hub-api.yaml ChainExitInput / ChainExitUpdateInput；link 属于凭据，不得写日志。
type chainExitRequest struct {
	Link string `json:"link"`
	Name string `json:"name"`
}

func (h *handlers) getChainOverview(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	o, err := h.chain.Overview(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, chainOverviewVO{
		Allowed: o.Flags.Chain, ExitAllowed: o.Flags.CustomNode, ServerAllowed: o.Flags.ServerChain,
		MaxClientRoutes: service.MaxClientRoutes, MaxServerRoutes: service.MaxServerRoutes,
		MaxRouteEntries: service.MaxRouteEntries, MaxExits: service.MaxChainExits,
		ClientRoutes: mapSlice(o.ClientRoutes, toChainRouteVO), ServerRoutes: mapSlice(o.ServerRoutes, toChainRouteVO),
		Exits: mapSlice(o.Exits, toChainExitVO),
		PlatformExits: mapSlice(o.PlatformExits, func(x service.ChainPlatformExitOption) chainPlatformExitOptionVO {
			return chainPlatformExitOptionVO{ID: x.ID, Name: x.Name, Protocol: x.Protocol, AllowServer: x.AllowServer, AllowClient: x.AllowClient}
		}),
		Nodes: mapSlice(o.Nodes, func(n service.ChainNodeOption) chainNodeOptionVO {
			return chainNodeOptionVO{ID: n.ID, Name: n.Name, Region: n.Region, Protocol: n.Protocol, CanBeExit: n.CanBeExit, CanBeServerEntry: n.CanBeServerEntry}
		}),
	})
}

func (h *handlers) createChainRoute(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	var req chainRouteRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	route, err := h.chain.CreateRoute(r.Context(), p, req.toInput())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toChainRouteVO(*route))
}

func (h *handlers) updateChainRoute(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	var req chainRouteRequest
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	route, err := h.chain.UpdateRoute(r.Context(), p, id, req.toInput())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toChainRouteVO(*route))
}

func (h *handlers) deleteChainRoute(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.chain.DeleteRoute(r.Context(), p, id); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) reorderChainRoutes(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	var req chainRouteOrderRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	routes, err := h.chain.ReorderRoutes(r.Context(), p, req.Kind, req.IDs)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, mapSlice(routes, toChainRouteVO))
}

func (h *handlers) createChainExit(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	var req chainExitRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	e, err := h.chain.CreateExit(r.Context(), p, req.Link, req.Name)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toChainExitVO(*e))
}

func (h *handlers) updateChainExit(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	var req chainExitRequest
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	e, err := h.chain.UpdateExit(r.Context(), p, id, req.Name, req.Link)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toChainExitVO(*e))
}

func (h *handlers) deleteChainExit(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.chain.DeleteExit(r.Context(), p, id); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) getChainExitLink(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	link, err := h.chain.ExitLink(r.Context(), p, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nodeLinkVO{Link: link})
}

// chainExitTestRequest 对应 hub-api.yaml ChainExitTestInput。
type chainExitTestRequest struct {
	NodeID int64  `json:"nodeId"`
	Mode   string `json:"mode"`
}

// chainExitTestVO 对应 hub-api.yaml ChainExitTestResult：带不通的原因大类（Ruling-56），不带面板的错误原文（Ruling-52）。
type chainExitTestVO struct {
	OK           bool   `json:"ok"`
	Mode         string `json:"mode"`
	Reason       string `json:"reason"`
	DelayMs      int64  `json:"delayMs"`
	HTTPStatus   int    `json:"httpStatus"`
	EgressIP     string `json:"egressIp"`
	EgressRegion string `json:"egressRegion"`
}

func toChainExitTestVO(r *service.ExitTestResult) chainExitTestVO {
	return chainExitTestVO{
		OK: r.OK, Mode: r.Mode, Reason: r.Reason, DelayMs: r.DelayMs, HTTPStatus: r.HTTPStatus, EgressIP: r.EgressIP, EgressRegion: r.EgressRegion,
	}
}

func (h *handlers) testChainExit(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	var req chainExitTestRequest
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	res, err := h.chain.TestExit(r.Context(), p, id, req.NodeID, req.Mode)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toChainExitTestVO(res))
}
