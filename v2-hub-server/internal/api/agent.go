package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"time"

	"v2-hub-server/internal/service"
)

// 二级代理接口（docs/03 §5.23，Ruling-101 – Ruling-104）：代理专用的 /api/admin/agent/*（tag agent，经 requireAgent）
// 与超管侧的 /api/admin/agents*、PUT /api/admin/users/{id}/agent（tag admin-agents，经 requireAdmin）。
// 代理侧处理函数不读取请求里的任何代理 id，身份只取自会话。

const (
	msgAgentQuotaNull = "expiresAt 不能为 null"
	msgBadAgentID     = "agentId 须为正整数"
	queryAgentID      = "agentId"
)

// jsonNull 是 JSON 的 null 字面量。
var jsonNull = []byte("null")

// AgentManager 是二级代理接口需要的业务能力（由 service.AgentService 实现）。
type AgentManager interface {
	AllowWrite(p *service.AdminPrincipal) error
	GetSummary(ctx context.Context, p *service.AdminPrincipal) (*service.AgentSummary, error)
	ListPlans(ctx context.Context, p *service.AdminPrincipal) ([]service.AgentPlan, error)
	ListUsers(ctx context.Context, p *service.AdminPrincipal, f service.AgentUserFilter, pq service.PageQuery) (*service.Page[service.AgentUser], error)
	GetUser(ctx context.Context, p *service.AdminPrincipal, id int64) (*service.AgentUser, error)
	CreateUser(ctx context.Context, p *service.AdminPrincipal, in service.AgentUserCreateInput, ip *netip.Addr) (*service.AgentUser, error)
	UpdateUser(ctx context.Context, p *service.AdminPrincipal, id int64, in service.AgentUserUpdateInput, ip *netip.Addr) (*service.AgentUser, error)
	DeleteUser(ctx context.Context, p *service.AdminPrincipal, id int64, ip *netip.Addr) error
	AssignPlan(ctx context.Context, p *service.AdminPrincipal, id, planID int64, ip *netip.Addr) (*service.AgentUser, error)
	UpdateQuota(ctx context.Context, p *service.AdminPrincipal, id int64, in service.AgentQuotaInput, ip *netip.Addr) (*service.AgentUser, error)
	ResetUserPassword(ctx context.Context, p *service.AdminPrincipal, id int64, pw string, ip *netip.Addr) error
	ClearUserTOTP(ctx context.Context, p *service.AdminPrincipal, id int64, ip *netip.Addr) error
	ResetUserSubscription(ctx context.Context, p *service.AdminPrincipal, id int64, ip *netip.Addr) error
	ListInviteCodes(ctx context.Context, p *service.AdminPrincipal, pq service.PageQuery) (*service.Page[service.InviteCode], error)
	CreateInviteCode(ctx context.Context, p *service.AdminPrincipal, in service.InviteCodeInput, ip *netip.Addr) (*service.InviteCode, error)
	UpdateInviteCode(ctx context.Context, p *service.AdminPrincipal, id int64, in service.InviteCodeInput, ip *netip.Addr) (*service.InviteCode, error)
	DeleteInviteCode(ctx context.Context, p *service.AdminPrincipal, id int64, ip *netip.Addr) error
	ListTickets(ctx context.Context, p *service.AdminPrincipal, f service.TicketFilter, pq service.PageQuery) (*service.Page[service.Ticket], error)
	GetTicket(ctx context.Context, p *service.AdminPrincipal, id int64) (*service.TicketDetail, error)
	ReplyTicket(ctx context.Context, p *service.AdminPrincipal, id int64, content string, ip *netip.Addr) (*service.TicketDetail, error)
	CloseTicket(ctx context.Context, p *service.AdminPrincipal, id int64, ip *netip.Addr) (*service.TicketDetail, error)
	ListOrders(ctx context.Context, p *service.AdminPrincipal, f service.OrderFilter, pq service.PageQuery) (*service.Page[service.Order], error)
	ListLedger(ctx context.Context, p *service.AdminPrincipal, f service.LedgerFilter, pq service.PageQuery) (*service.Page[service.LedgerEntry], error)
	ListOwnAllocations(ctx context.Context, p *service.AdminPrincipal, from, to *time.Time, pq service.PageQuery) (*service.Page[service.AgentAllocation], error)

	// 超管侧（admin:read / admin:write）
	ListAgents(ctx context.Context, keyword string, pq service.PageQuery) (*service.Page[service.Agent], error)
	GetAgent(ctx context.Context, id int64) (*service.Agent, error)
	CreateAgent(ctx context.Context, actor *service.AdminPrincipal, in service.AgentCreateInput, ip *netip.Addr) (*service.Agent, error)
	UpdateAgent(ctx context.Context, actor *service.AdminPrincipal, id int64, in service.AgentUpdateInput, ip *netip.Addr) (*service.Agent, error)
	ListAgentAllocations(ctx context.Context, id int64, from, to *time.Time, pq service.PageQuery) (*service.Page[service.AgentAllocation], error)
	TransferUser(ctx context.Context, actor *service.AdminPrincipal, userID int64, agentID *int64, ip *netip.Addr) (*service.User, error)
}

// ---- VO ----

// agentSummaryVO 对应 hub-api.yaml AgentSummary。
type agentSummaryVO struct {
	MaxUsers         int   `json:"maxUsers"`
	UserCount        int   `json:"userCount"`
	TrafficPoolTotal int64 `json:"trafficPoolTotal"`
	TrafficPoolUsed  int64 `json:"trafficPoolUsed"`
}

// agentPlanVO 对应 hub-api.yaml AgentPlan。
type agentPlanVO struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	TrafficLimit int64  `json:"trafficLimit"`
	DurationDays int    `json:"durationDays"`
	TrafficReset string `json:"trafficReset"`
	LimitIP      int    `json:"limitIp"`
	PoolCost     int64  `json:"poolCost"`
}

func toAgentPlanVO(p service.AgentPlan) agentPlanVO {
	return agentPlanVO{ID: p.ID, Name: p.Name, TrafficLimit: p.TrafficLimit, DurationDays: p.DurationDays, TrafficReset: p.TrafficReset,
		LimitIP: p.LimitIP, PoolCost: p.PoolCost}
}

// agentUserVO 对应 hub-api.yaml AgentUser：只有这些字段，不含下发状态、订阅凭据、邀请关系、免费续期（Ruling-104）。
type agentUserVO struct {
	ID           int64      `json:"id"`
	Username     string     `json:"username"`
	Email        *string    `json:"email"`
	Remark       string     `json:"remark"`
	Status       string     `json:"status"`
	IsDisabled   bool       `json:"isDisabled"`
	PlanID       *int64     `json:"planId"`
	PlanName     string     `json:"planName"`
	TrafficLimit int64      `json:"trafficLimit"`
	TrafficUsed  int64      `json:"trafficUsed"`
	ExpiresAt    *time.Time `json:"expiresAt"`
	PendingDays  int        `json:"pendingDays"`
	Balance      int64      `json:"balance"`
	TOTPEnabled  bool       `json:"totpEnabled"`
	CreatedAt    time.Time  `json:"createdAt"`
	LastUsedAt   *time.Time `json:"lastUsedAt"`
}

func toAgentUserVO(u service.AgentUser) agentUserVO {
	return agentUserVO{
		ID: u.ID, Username: u.Username, Email: optionalText(u.Email), Remark: u.Remark, Status: u.Status, IsDisabled: u.IsDisabled,
		PlanID: u.PlanID, PlanName: u.PlanName, TrafficLimit: u.TrafficLimit, TrafficUsed: u.TrafficUsed, ExpiresAt: u.ExpiresAt,
		PendingDays: u.PendingDays, Balance: u.Balance, TOTPEnabled: u.TOTPEnabled, CreatedAt: u.CreatedAt, LastUsedAt: u.LastUsedAt,
	}
}

// agentAllocationVO 对应 hub-api.yaml AgentAllocation。
type agentAllocationVO struct {
	ID           int64     `json:"id"`
	AgentID      int64     `json:"agentId"`
	UserID       *int64    `json:"userId"`
	Username     string    `json:"username"`
	PlanID       *int64    `json:"planId"`
	PlanName     string    `json:"planName"`
	TrafficBytes int64     `json:"trafficBytes"`
	DurationDays int       `json:"durationDays"`
	Kind         string    `json:"kind"`
	CreatedAt    time.Time `json:"createdAt"`
}

func toAgentAllocationVO(a service.AgentAllocation) agentAllocationVO {
	return agentAllocationVO{ID: a.ID, AgentID: a.AgentID, UserID: a.UserID, Username: a.Username, PlanID: a.PlanID, PlanName: a.PlanName,
		TrafficBytes: a.TrafficBytes, DurationDays: a.DurationDays, Kind: a.Kind, CreatedAt: a.CreatedAt}
}

// ---- 请求体 ----

// agentUserCreateBody 对应 hub-api.yaml AgentUserCreateInput（JSON 字段名 password）。
type agentUserCreateBody struct {
	Username   string `json:"username"`
	Credential string `json:"password"`
	Email      string `json:"email"`
	Remark     string `json:"remark"`
	PlanID     int64  `json:"planId"`
}

// agentUserUpdateBody 对应 hub-api.yaml AgentUserUpdateInput：只收备注与启停，其余字段因 DisallowUnknownFields 一律 400。
type agentUserUpdateBody struct {
	Remark     string `json:"remark"`
	IsDisabled bool   `json:"isDisabled"`
}

// agentQuotaBody 对应 hub-api.yaml AgentUserQuotaInput；ExpiresAt 保留原文以区分“省略”与“显式 null”。
type agentQuotaBody struct {
	TrafficLimit *int64          `json:"trafficLimit"`
	ExpiresAt    json.RawMessage `json:"expiresAt"`
}

func (b agentQuotaBody) input() (service.AgentQuotaInput, error) {
	in := service.AgentQuotaInput{TrafficLimit: b.TrafficLimit}
	if b.ExpiresAt == nil {
		return in, nil
	}
	if bytes.Equal(bytes.TrimSpace(b.ExpiresAt), jsonNull) {
		return in, badRequest(msgAgentQuotaNull)
	}
	var t time.Time
	if err := json.Unmarshal(b.ExpiresAt, &t); err != nil {
		return in, badRequest(msgBadJSON)
	}
	in.ExpiresAt = &t
	return in, nil
}

// ---- 代理侧处理函数 ----

func (h *handlers) agentGetSummary(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	sum, err := h.agents.GetSummary(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, agentSummaryVO{MaxUsers: sum.MaxUsers, UserCount: sum.UserCount, TrafficPoolTotal: sum.TrafficPoolTotal, TrafficPoolUsed: sum.TrafficPoolUsed})
}

func (h *handlers) agentListPlans(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	list, err := h.agents.ListPlans(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, mapSlice(list, toAgentPlanVO))
}

func (h *handlers) agentListUsers(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	planID, err := optionalQueryID(r, queryPlanID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	f := service.AgentUserFilter{Keyword: r.URL.Query().Get(queryKeyword), Status: r.URL.Query().Get(queryStatus), PlanID: planID}
	page, err := h.agents.ListUsers(r.Context(), p, f, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toPageVO(page, toAgentUserVO))
}

func (h *handlers) agentCreateUser(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req agentUserCreateBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	in := service.AgentUserCreateInput{Username: req.Username, Email: req.Email, Remark: req.Remark, PlanID: req.PlanID}
	pw := req.Credential
	in.Password = pw
	u, err := h.agents.CreateUser(r.Context(), p, in, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toAgentUserVO(*u))
}

func (h *handlers) agentGetUser(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	u, err := h.agents.GetUser(r.Context(), p, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toAgentUserVO(*u))
}

func (h *handlers) agentUpdateUser(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req agentUserUpdateBody
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	u, err := h.agents.UpdateUser(r.Context(), p, id, service.AgentUserUpdateInput{Remark: req.Remark, IsDisabled: req.IsDisabled}, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toAgentUserVO(*u))
}

func (h *handlers) agentDeleteUser(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.agents.DeleteUser(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) agentAssignUserPlan(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req assignPlanBody
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if req.PlanID < 1 {
		writeError(w, r, badRequest(msgPickPlan))
		return
	}
	u, err := h.agents.AssignPlan(r.Context(), p, id, req.PlanID, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toAgentUserVO(*u))
}

func (h *handlers) agentUpdateUserQuota(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req agentQuotaBody
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	in, err := req.input()
	if err != nil {
		writeError(w, r, err)
		return
	}
	u, err := h.agents.UpdateQuota(r.Context(), p, id, in, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toAgentUserVO(*u))
}

func (h *handlers) agentResetUserPassword(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req credentialRequest
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.agents.ResetUserPassword(r.Context(), p, id, req.Credential, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) agentClearUserTOTP(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.agents.ClearUserTOTP(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) agentResetUserSubscription(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.agents.ResetUserSubscription(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) agentListInviteCodes(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.agents.ListInviteCodes(r.Context(), p, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toPageVO(page, toInviteCodeVO))
}

func (h *handlers) agentCreateInviteCode(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req inviteCodeBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	c, err := h.agents.CreateInviteCode(r.Context(), p, req.input(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toInviteCodeVO(*c))
}

func (h *handlers) agentUpdateInviteCode(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req inviteCodeBody
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	c, err := h.agents.UpdateInviteCode(r.Context(), p, id, req.input(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toInviteCodeVO(*c))
}

func (h *handlers) agentDeleteInviteCode(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.agents.DeleteInviteCode(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) agentListTickets(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	userID, err := optionalPositiveInt64(r, queryUserID, msgBadID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.agents.ListTickets(r.Context(), p, service.TicketFilter{Status: r.URL.Query().Get(queryStatus), UserID: userID}, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toTicketPageVO(page, true))
}

func (h *handlers) agentGetTicket(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	d, err := h.agents.GetTicket(r.Context(), p, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toTicketDetailVO(d, true))
}

func (h *handlers) agentReplyTicket(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req ticketMessageRequest
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	d, err := h.agents.ReplyTicket(r.Context(), p, id, req.Content, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toTicketDetailVO(d, true))
}

func (h *handlers) agentCloseTicket(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	d, err := h.agents.CloseTicket(r.Context(), p, id, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toTicketDetailVO(d, true))
}

// timeRangeQuery 读取可选的 from / to 查询参数。
func timeRangeQuery(r *http.Request) (*time.Time, *time.Time, error) {
	from, err := optionalTimeQuery(r, queryFrom, msgBadTimeFrom)
	if err != nil {
		return nil, nil, err
	}
	to, err := optionalTimeQuery(r, queryTo, msgBadTimeTo)
	if err != nil {
		return nil, nil, err
	}
	return from, to, nil
}

func (h *handlers) agentListOrders(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
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
	from, to, err := timeRangeQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.agents.ListOrders(r.Context(), p, service.OrderFilter{UserID: userID, Kind: r.URL.Query().Get(queryKind), From: from, To: to}, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toOrderPageVO(page, true))
}

func (h *handlers) agentListWalletLedger(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
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
	page, err := h.agents.ListLedger(r.Context(), p, service.LedgerFilter{UserID: userID, Kind: r.URL.Query().Get(queryKind)}, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toLedgerPageVO(page, true))
}

func (h *handlers) agentListAllocations(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
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
	page, err := h.agents.ListOwnAllocations(r.Context(), p, from, to, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toPageVO(page, toAgentAllocationVO))
}
