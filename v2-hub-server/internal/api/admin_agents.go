package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"v2-hub-server/internal/service"
)

// 超管侧的代理管理与用户划转（docs/03 §5.23 超管侧管理），与 hub-api.yaml tag admin-agents 逐条对应。

// agentVO 对应 hub-api.yaml Agent。
type agentVO struct {
	ID               int64      `json:"id"`
	Username         string     `json:"username"`
	IsDisabled       bool       `json:"isDisabled"`
	TOTPEnabled      bool       `json:"totpEnabled"`
	LastLoginAt      *time.Time `json:"lastLoginAt"`
	CreatedAt        time.Time  `json:"createdAt"`
	Remark           string     `json:"remark"`
	MaxUsers         int        `json:"maxUsers"`
	TrafficPoolTotal int64      `json:"trafficPoolTotal"`
	TrafficPoolUsed  int64      `json:"trafficPoolUsed"`
	PlanIDs          []int64    `json:"planIds"`
	UserCount        int        `json:"userCount"`
}

func toAgentVO(a service.Agent) agentVO {
	return agentVO{
		ID: a.ID, Username: a.Username, IsDisabled: a.IsDisabled, TOTPEnabled: a.TOTPEnabled, LastLoginAt: a.LastLoginAt, CreatedAt: a.CreatedAt,
		Remark: a.Remark, MaxUsers: a.MaxUsers, TrafficPoolTotal: a.TrafficPoolTotal, TrafficPoolUsed: a.TrafficPoolUsed,
		PlanIDs: a.PlanIDs, UserCount: a.UserCount,
	}
}

// agentBaseBody 对应 hub-api.yaml AgentBase。
type agentBaseBody struct {
	Remark           string  `json:"remark"`
	MaxUsers         int     `json:"maxUsers"`
	TrafficPoolTotal int64   `json:"trafficPoolTotal"`
	PlanIDs          []int64 `json:"planIds"`
}

func (b agentBaseBody) spec() service.AgentQuotaSpec {
	return service.AgentQuotaSpec{Remark: b.Remark, MaxUsers: b.MaxUsers, TrafficPoolTotal: b.TrafficPoolTotal, PlanIDs: b.PlanIDs}
}

// agentCreateBody 对应 hub-api.yaml AgentCreateInput（JSON 字段名 password）。
type agentCreateBody struct {
	agentBaseBody
	Username   string `json:"username"`
	Credential string `json:"password"`
}

// agentUpdateBody 对应 hub-api.yaml AgentUpdateInput。
type agentUpdateBody struct {
	agentBaseBody
	IsDisabled bool `json:"isDisabled"`
}

// userAgentBody 对应 hub-api.yaml UserAgentInput：agentId 必填，null 表示收回为平台用户；保留原文以区分“省略”与“null”。
type userAgentBody struct {
	AgentID json.RawMessage `json:"agentId"`
}

// target 解析目标代理：省略或不是正整数返回 400，null 返回 nil（收回）。
func (b userAgentBody) target() (*int64, error) {
	if b.AgentID == nil {
		return nil, badRequest(msgBadAgentID)
	}
	if bytes.Equal(bytes.TrimSpace(b.AgentID), jsonNull) {
		return nil, nil
	}
	var id int64
	if err := json.Unmarshal(b.AgentID, &id); err != nil || id < 1 {
		return nil, badRequest(msgBadAgentID)
	}
	return &id, nil
}

func (h *handlers) listAgents(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.agents.ListAgents(r.Context(), r.URL.Query().Get(queryKeyword), pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toPageVO(page, toAgentVO))
}

func (h *handlers) createAgent(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req agentCreateBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	in := service.AgentCreateInput{Username: req.Username, AgentQuotaSpec: req.spec()}
	pw := req.Credential
	in.Password = pw
	a, err := h.agents.CreateAgent(r.Context(), p, in, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toAgentVO(*a))
}

func (h *handlers) getAgent(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a, err := h.agents.GetAgent(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toAgentVO(*a))
}

func (h *handlers) updateAgent(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req agentUpdateBody
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a, err := h.agents.UpdateAgent(r.Context(), p, id, service.AgentUpdateInput{AgentQuotaSpec: req.spec(), IsDisabled: req.IsDisabled}, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toAgentVO(*a))
}

func (h *handlers) listAgentAllocations(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
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
	from, to, err := timeRangeQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.agents.ListAgentAllocations(r.Context(), id, from, to, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toPageVO(page, toAgentAllocationVO))
}

func (h *handlers) transferUserAgent(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req userAgentBody
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	target, err := req.target()
	if err != nil {
		writeError(w, r, err)
		return
	}
	u, err := h.agents.TransferUser(r.Context(), p, id, target, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toUserVO(*u))
}
