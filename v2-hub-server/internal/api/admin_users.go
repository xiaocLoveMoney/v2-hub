package api

import (
	"context"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"v2-hub-server/internal/service"
)

const (
	queryKeyword  = "keyword"
	queryStatus   = "status"
	queryPlanID   = "planId"
	msgPickPlan   = "请选择套餐"
	defaultDayOne = 1
)

// UserManager 是套餐与用户接口需要的业务能力（由 service.UserService 实现，docs/03 §5.2）。
type UserManager interface {
	ListPlans(ctx context.Context) ([]service.Plan, error)
	CreatePlan(ctx context.Context, actor *service.AdminPrincipal, in service.PlanInput, ip *netip.Addr) (*service.Plan, error)
	UpdatePlan(ctx context.Context, actor *service.AdminPrincipal, id int64, in service.PlanInput, ip *netip.Addr) (*service.Plan, error)
	DeletePlan(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) error
	ListUsers(ctx context.Context, f service.UserFilter, page, size int) ([]service.User, int64, error)
	GetUser(ctx context.Context, id int64) (*service.User, error)
	CreateUser(ctx context.Context, actor *service.AdminPrincipal, in service.UserCreateInput, ip *netip.Addr) (*service.User, error)
	UpdateUser(ctx context.Context, actor *service.AdminPrincipal, id int64, in service.UserUpdateInput, ip *netip.Addr) (*service.User, error)
	DeleteUser(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) error
	AssignPlan(ctx context.Context, actor *service.AdminPrincipal, id, planID int64, ip *netip.Addr) (*service.User, error)
	UpdateQuota(ctx context.Context, actor *service.AdminPrincipal, id int64, in service.UserQuotaInput, ip *netip.Addr) (*service.User, error)
	ResetUserPassword(ctx context.Context, actor *service.AdminPrincipal, id int64, pw string, ip *netip.Addr) error
	ListUserProvisions(ctx context.Context, id int64) ([]service.Provision, error)
	ReconcileUser(ctx context.Context, id int64) error
	ClearUserTOTP(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) error
	ResetUserSubscription(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) error
	ListSubscribeLogs(ctx context.Context, userID int64, pq service.PageQuery) (*service.Page[service.SubscribeLogItem], error)
}

// planBody 对应 hub-api.yaml PlanInput。
type planBody struct {
	Name             string  `json:"name"`
	TrafficLimit     int64   `json:"trafficLimit"`
	DurationDays     int     `json:"durationDays"`
	StartOnFirstUse  bool    `json:"startOnFirstUse"`
	LimitIP          int     `json:"limitIp"`
	RenewEveryDays   int     `json:"renewEveryDays"`
	RenewMonthDay    int     `json:"renewMonthDay"`
	RenewWeekday     int     `json:"renewWeekday"`
	RenewMax         int     `json:"renewMax"`
	TrafficReset     string  `json:"trafficReset"`
	TrafficResetDay  int     `json:"trafficResetDay"`
	AllowClientChain bool    `json:"allowClientChain"`
	AllowCustomNode  bool    `json:"allowCustomNode"`
	Remark           string  `json:"remark"`
	NodeGroupIDs     []int64 `json:"nodeGroupIds"`
}

// input 转成业务输入；trafficReset / trafficResetDay 省略时取契约默认值（never / 1）。
func (b planBody) input() service.PlanInput {
	reset, day := b.TrafficReset, b.TrafficResetDay
	if reset == "" {
		reset = service.TrafficResetNever
	}
	if day == 0 {
		day = defaultDayOne
	}
	return service.PlanInput{
		Name: b.Name, AllowClientChain: b.AllowClientChain, AllowCustomNode: b.AllowCustomNode, Remark: b.Remark, NodeGroupIDs: b.NodeGroupIDs,
		PlanQuota: service.PlanQuota{
			TrafficLimit: b.TrafficLimit, DurationDays: b.DurationDays, StartOnFirstUse: b.StartOnFirstUse, LimitIP: b.LimitIP,
			RenewEveryDays: b.RenewEveryDays, RenewMonthDay: b.RenewMonthDay, RenewWeekday: b.RenewWeekday, RenewMax: b.RenewMax,
			TrafficReset: reset, TrafficResetDay: day,
		},
	}
}

// planVO 对应 hub-api.yaml Plan。
type planVO struct {
	planBody
	ID        int64     `json:"id"`
	UserCount int       `json:"userCount"`
	CreatedAt time.Time `json:"createdAt"`
}

func toPlanVO(p service.Plan) planVO {
	q := p.PlanQuota
	groups := p.NodeGroupIDs
	if groups == nil {
		groups = []int64{}
	}
	return planVO{
		ID: p.ID, UserCount: p.UserCount, CreatedAt: p.CreatedAt,
		planBody: planBody{
			Name: p.Name, TrafficLimit: q.TrafficLimit, DurationDays: q.DurationDays, StartOnFirstUse: q.StartOnFirstUse, LimitIP: q.LimitIP,
			RenewEveryDays: q.RenewEveryDays, RenewMonthDay: q.RenewMonthDay, RenewWeekday: q.RenewWeekday, RenewMax: q.RenewMax,
			TrafficReset: q.TrafficReset, TrafficResetDay: q.TrafficResetDay, AllowClientChain: p.AllowClientChain,
			AllowCustomNode: p.AllowCustomNode, Remark: p.Remark, NodeGroupIDs: groups,
		},
	}
}

// quotaBody 对应 hub-api.yaml UserQuotaInput。
type quotaBody struct {
	TrafficLimit    int64      `json:"trafficLimit"`
	TrafficUsed     int64      `json:"trafficUsed"`
	ExpiresAt       *time.Time `json:"expiresAt"`
	PendingDays     int        `json:"pendingDays"`
	LimitIP         int        `json:"limitIp"`
	RenewEveryDays  int        `json:"renewEveryDays"`
	RenewMonthDay   int        `json:"renewMonthDay"`
	RenewWeekday    int        `json:"renewWeekday"`
	RenewMax        int        `json:"renewMax"`
	TrafficReset    string     `json:"trafficReset"`
	TrafficResetDay int        `json:"trafficResetDay"`
}

type provisionCountsVO struct {
	Synced  int `json:"synced"`
	Pending int `json:"pending"`
	Failed  int `json:"failed"`
}

// userVO 对应 hub-api.yaml User。
type userVO struct {
	quotaBody
	ID         int64             `json:"id"`
	Username   string            `json:"username"`
	Remark     string            `json:"remark"`
	PlanID     *int64            `json:"planId"`
	PlanName   string            `json:"planName"`
	Status     string            `json:"status"`
	IsDisabled bool              `json:"isDisabled"`
	RenewCount int               `json:"renewCount"`
	Provisions provisionCountsVO `json:"provisions"`
	CreatedAt  time.Time         `json:"createdAt"`
	// 0.2.6 起
	TOTPEnabled bool       `json:"totpEnabled"`
	LastUsedAt  *time.Time `json:"lastUsedAt"`
	// 0.3.0 起
	Email             *string `json:"email"`
	IsEmailVerified   bool    `json:"isEmailVerified"`
	InvitedBy         *int64  `json:"invitedBy"`
	InvitedByUsername string  `json:"invitedByUsername"`
}

func toUserVO(u service.User) userVO {
	q := u.Quota
	return userVO{
		ID: u.ID, Username: u.Username, Remark: u.Remark, PlanID: u.PlanID, PlanName: u.PlanName, Status: u.Status,
		IsDisabled: q.Disabled, RenewCount: q.RenewCount, CreatedAt: u.CreatedAt, TOTPEnabled: u.TOTPEnabled, LastUsedAt: u.LastUsedAt,
		Email: optionalText(u.Email), IsEmailVerified: u.EmailVerified, InvitedBy: u.InvitedBy, InvitedByUsername: u.InvitedByUsername,
		Provisions: provisionCountsVO{Synced: u.Provisions.Synced, Pending: u.Provisions.Pending, Failed: u.Provisions.Failed},
		quotaBody: quotaBody{
			TrafficLimit: q.TrafficLimit, TrafficUsed: q.TrafficUsed, ExpiresAt: q.ExpiresAt, PendingDays: q.PendingDays, LimitIP: q.LimitIP,
			RenewEveryDays: q.RenewEveryDays, RenewMonthDay: q.RenewMonthDay, RenewWeekday: q.RenewWeekday, RenewMax: q.RenewMax,
			TrafficReset: q.TrafficReset, TrafficResetDay: q.TrafficResetDay,
		},
	}
}

// provisionVO 对应 hub-api.yaml Provision。
type provisionVO struct {
	ServerID   int64      `json:"serverId"`
	ServerName string     `json:"serverName"`
	Email      string     `json:"email"`
	Status     string     `json:"status"`
	LastError  string     `json:"lastError"`
	RetryCount int        `json:"retryCount"`
	SyncedAt   *time.Time `json:"syncedAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

func (h *handlers) listPlans(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	list, err := h.users.ListPlans(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, mapSlice(list, toPlanVO))
}

func (h *handlers) createPlan(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req planBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	plan, err := h.users.CreatePlan(r.Context(), p, req.input(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toPlanVO(*plan))
}

func (h *handlers) updatePlan(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req planBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	plan, err := h.users.UpdatePlan(r.Context(), p, id, req.input(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toPlanVO(*plan))
}

func (h *handlers) deletePlan(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.users.DeletePlan(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) listUsers(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
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
	f := service.UserFilter{Keyword: r.URL.Query().Get(queryKeyword), Status: r.URL.Query().Get(queryStatus), PlanID: planID}
	list, total, err := h.users.ListUsers(r.Context(), f, pq.Page, pq.PageSize)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, pageVO[userVO]{Items: mapSlice(list, toUserVO), Total: total})
}

// userCreateBody 对应 hub-api.yaml UserCreateInput（JSON 字段名 password）。
type userCreateBody struct {
	Username   string `json:"username"`
	Credential string `json:"password"`
	Remark     string `json:"remark"`
	PlanID     int64  `json:"planId"`
	Email      string `json:"email"`
}

func (h *handlers) createUser(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req userCreateBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	in := service.UserCreateInput{Username: req.Username, Remark: req.Remark, PlanID: req.PlanID, Email: req.Email}
	pw := req.Credential
	in.Password = pw
	u, err := h.users.CreateUser(r.Context(), p, in, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toUserVO(*u))
}

func (h *handlers) getUser(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	u, err := h.users.GetUser(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toUserVO(*u))
}

// userUpdateBody 对应 hub-api.yaml UserUpdateInput。
type userUpdateBody struct {
	Remark     string `json:"remark"`
	IsDisabled bool   `json:"isDisabled"`
	// Email 省略不改，空串清除（0.3.0）
	Email *string `json:"email"`
}

func (h *handlers) updateUser(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req userUpdateBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	u, err := h.users.UpdateUser(r.Context(), p, id, service.UserUpdateInput{Remark: req.Remark, IsDisabled: req.IsDisabled, Email: req.Email}, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toUserVO(*u))
}

func (h *handlers) deleteUser(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.users.DeleteUser(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

type assignPlanBody struct {
	PlanID int64 `json:"planId"`
}

func (h *handlers) assignUserPlan(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req assignPlanBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.PlanID < 1 {
		writeError(w, r, badRequest(msgPickPlan))
		return
	}
	u, err := h.users.AssignPlan(r.Context(), p, id, req.PlanID, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toUserVO(*u))
}

func (h *handlers) updateUserQuota(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req quotaBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	u, err := h.users.UpdateQuota(r.Context(), p, id, service.UserQuotaInput{
		TrafficLimit: req.TrafficLimit, TrafficUsed: req.TrafficUsed, ExpiresAt: req.ExpiresAt, PendingDays: req.PendingDays, LimitIP: req.LimitIP,
		RenewEveryDays: req.RenewEveryDays, RenewMonthDay: req.RenewMonthDay, RenewWeekday: req.RenewWeekday, RenewMax: req.RenewMax,
		TrafficReset: req.TrafficReset, TrafficResetDay: req.TrafficResetDay,
	}, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toUserVO(*u))
}

func (h *handlers) resetUserPassword(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req credentialRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.users.ResetUserPassword(r.Context(), p, id, req.Credential, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) listUserProvisions(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	list, err := h.users.ListUserProvisions(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, mapSlice(list, func(v service.Provision) provisionVO {
		return provisionVO{ServerID: v.ServerID, ServerName: v.ServerName, Email: v.Email, Status: v.Status, LastError: v.LastError,
			RetryCount: v.RetryCount, SyncedAt: v.SyncedAt, UpdatedAt: v.UpdatedAt}
	}))
}

func (h *handlers) reconcileUser(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.users.ReconcileUser(r.Context(), id); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) clearUserTOTP(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.users.ClearUserTOTP(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) resetUserSubscription(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.users.ResetUserSubscription(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

// subscribeLogVO 对应 hub-api.yaml SubscribeLog。
type subscribeLogVO struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"userId"`
	Username  string    `json:"username"`
	Format    string    `json:"format"`
	ClientIP  string    `json:"clientIp"`
	UserAgent string    `json:"userAgent"`
	CreatedAt time.Time `json:"createdAt"`
}

type subscribeLogPageVO struct {
	Items []subscribeLogVO `json:"items"`
	Total int64            `json:"total"`
}

func toSubscribeLogPageVO(page *service.Page[service.SubscribeLogItem]) subscribeLogPageVO {
	items := make([]subscribeLogVO, 0, len(page.Items))
	for _, l := range page.Items {
		items = append(items, subscribeLogVO{ID: l.ID, UserID: l.UserID, Username: l.Username, Format: l.Format, ClientIP: l.ClientIP, UserAgent: l.UserAgent, CreatedAt: l.CreatedAt})
	}
	return subscribeLogPageVO{Items: items, Total: page.Total}
}

const queryUserID = "userId"

func (h *handlers) listSubscribeLogs(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var userID int64
	if raw := r.URL.Query().Get(queryUserID); raw != "" {
		if userID, err = strconv.ParseInt(raw, 10, 64); err != nil || userID < 1 {
			writeError(w, r, badRequest(msgBadID))
			return
		}
	}
	page, err := h.users.ListSubscribeLogs(r.Context(), userID, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toSubscribeLogPageVO(page))
}
