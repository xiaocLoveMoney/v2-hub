package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"v2-hub-server/internal/service"
)

// ChainManager 是链式代理接口需要的能力（由 service.ChainService 实现）；身份只取自会话（R8）。
type ChainManager interface {
	Features(ctx context.Context, userID int64) (service.ChainFlags, error)
	Overview(ctx context.Context, p *service.UserPrincipal) (*service.ChainOverview, error)
	CreateRule(ctx context.Context, p *service.UserPrincipal, in service.ChainRuleInput) (*service.ChainRule, error)
	UpdateRule(ctx context.Context, p *service.UserPrincipal, id int64, name string, enabled bool) (*service.ChainRule, error)
	DeleteRule(ctx context.Context, p *service.UserPrincipal, id int64) error
	CreateCustomNode(ctx context.Context, p *service.UserPrincipal, link, name string) (*service.CustomNode, error)
	UpdateCustomNode(ctx context.Context, p *service.UserPrincipal, id int64, name, link string) (*service.CustomNode, error)
	DeleteCustomNode(ctx context.Context, p *service.UserPrincipal, id int64) error
	CustomNodeLink(ctx context.Context, p *service.UserPrincipal, id int64) (string, error)
}

// userFeaturesVO 对应 hub-api.yaml UserSession.features：当前套餐开了哪些功能，决定菜单显隐。
type userFeaturesVO struct {
	ClientChain bool `json:"clientChain"`
	CustomNode  bool `json:"customNode"`
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
	vo.Features = userFeaturesVO{ClientChain: flags.Chain, CustomNode: flags.CustomNode}
	return vo
}

// chainNodeOptionVO 对应 hub-api.yaml ChainNodeOption。
type chainNodeOptionVO struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Region    string `json:"region"`
	Protocol  string `json:"protocol"`
	CanBeExit bool   `json:"canBeExit"`
}

type chainEntryVO struct {
	NodeID int64  `json:"nodeId"`
	Name   string `json:"name"`
}

type chainExitVO struct {
	Kind     string `json:"kind"`
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
}

// chainRuleVO 对应 hub-api.yaml ChainRule。
type chainRuleVO struct {
	ID            int64        `json:"id"`
	Name          string       `json:"name"`
	DisplayName   string       `json:"displayName"`
	Entry         chainEntryVO `json:"entry"`
	Exit          chainExitVO  `json:"exit"`
	IsEnabled     bool         `json:"isEnabled"`
	Status        string       `json:"status"`
	BlockedReason string       `json:"blockedReason"`
	CreatedAt     time.Time    `json:"createdAt"`
}

func toChainRuleVO(r service.ChainRule) chainRuleVO {
	return chainRuleVO{
		ID: r.ID, Name: r.Name, DisplayName: r.DisplayName, IsEnabled: r.Enabled, Status: r.Status, BlockedReason: r.BlockedReason, CreatedAt: r.CreatedAt,
		Entry: chainEntryVO{NodeID: r.EntryNodeID, Name: r.EntryName},
		Exit:  chainExitVO{Kind: r.ExitKind, ID: r.ExitID, Name: r.ExitName, Protocol: r.ExitProtocol},
	}
}

// customNodeVO 对应 hub-api.yaml CustomNode；原链接属于凭据，不在这里返回。
type customNodeVO struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Protocol  string    `json:"protocol"`
	Address   string    `json:"address"`
	Port      int       `json:"port"`
	RuleCount int       `json:"ruleCount"`
	CreatedAt time.Time `json:"createdAt"`
}

func toCustomNodeVO(n service.CustomNode) customNodeVO {
	return customNodeVO{ID: n.ID, Name: n.Name, Protocol: n.Protocol, Address: n.Address, Port: n.Port, RuleCount: n.RuleCount, CreatedAt: n.CreatedAt}
}

// chainOverviewVO 对应 hub-api.yaml ChainOverview。
type chainOverviewVO struct {
	Allowed           bool                `json:"allowed"`
	CustomNodeAllowed bool                `json:"customNodeAllowed"`
	MaxRules          int                 `json:"maxRules"`
	MaxCustomNodes    int                 `json:"maxCustomNodes"`
	Rules             []chainRuleVO       `json:"rules"`
	CustomNodes       []customNodeVO      `json:"customNodes"`
	Nodes             []chainNodeOptionVO `json:"nodes"`
}

// chainRuleCreateRequest 对应 hub-api.yaml ChainRuleInput；isEnabled 省略按启用。
type chainRuleCreateRequest struct {
	EntryNodeID      int64  `json:"entryNodeId"`
	ExitNodeID       int64  `json:"exitNodeId"`
	ExitCustomNodeID int64  `json:"exitCustomNodeId"`
	Name             string `json:"name"`
	IsEnabled        *bool  `json:"isEnabled"`
}

// chainRuleUpdateRequest 对应 hub-api.yaml ChainRuleUpdateInput。
type chainRuleUpdateRequest struct {
	Name      string `json:"name"`
	IsEnabled bool   `json:"isEnabled"`
}

// customNodeRequest 对应 hub-api.yaml CustomNodeInput / CustomNodeUpdateInput；link 属于凭据，不得写日志。
type customNodeRequest struct {
	Link string `json:"link"`
	Name string `json:"name"`
}

func (h *handlers) getChainOverview(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	o, err := h.chain.Overview(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	vo := chainOverviewVO{
		Allowed: o.Flags.Chain, CustomNodeAllowed: o.Flags.CustomNode, MaxRules: service.MaxChainRules, MaxCustomNodes: service.MaxCustomNodes,
		Rules: make([]chainRuleVO, 0, len(o.Rules)), CustomNodes: make([]customNodeVO, 0, len(o.CustomNodes)), Nodes: make([]chainNodeOptionVO, 0, len(o.Nodes)),
	}
	for _, rule := range o.Rules {
		vo.Rules = append(vo.Rules, toChainRuleVO(rule))
	}
	for _, n := range o.CustomNodes {
		vo.CustomNodes = append(vo.CustomNodes, toCustomNodeVO(n))
	}
	for _, n := range o.Nodes {
		vo.Nodes = append(vo.Nodes, chainNodeOptionVO{ID: n.ID, Name: n.Name, Region: n.Region, Protocol: n.Protocol, CanBeExit: n.CanBeExit})
	}
	writeOK(w, vo)
}

func (h *handlers) createChainRule(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	var req chainRuleCreateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	in := service.ChainRuleInput{EntryNodeID: req.EntryNodeID, ExitNodeID: req.ExitNodeID, ExitCustomNodeID: req.ExitCustomNodeID, Name: req.Name, Enabled: true}
	if req.IsEnabled != nil {
		in.Enabled = *req.IsEnabled
	}
	rule, err := h.chain.CreateRule(r.Context(), p, in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toChainRuleVO(*rule))
}

func (h *handlers) updateChainRule(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req chainRuleUpdateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	rule, err := h.chain.UpdateRule(r.Context(), p, id, req.Name, req.IsEnabled)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toChainRuleVO(*rule))
}

func (h *handlers) deleteChainRule(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.chain.DeleteRule(r.Context(), p, id); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) createCustomNode(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	var req customNodeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	n, err := h.chain.CreateCustomNode(r.Context(), p, req.Link, req.Name)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toCustomNodeVO(*n))
}

func (h *handlers) updateCustomNode(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req customNodeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	n, err := h.chain.UpdateCustomNode(r.Context(), p, id, req.Name, req.Link)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toCustomNodeVO(*n))
}

func (h *handlers) deleteCustomNode(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.chain.DeleteCustomNode(r.Context(), p, id); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) getCustomNodeLink(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	link, err := h.chain.CustomNodeLink(r.Context(), p, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nodeLinkVO{Link: link})
}
