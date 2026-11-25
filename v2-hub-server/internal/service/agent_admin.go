package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/crypto"
)

// 超管侧的代理管理与用户划转（docs/03 §5.23 超管侧管理，Ruling-101 – Ruling-104）。路由层要求 admin:read / admin:write，写操作须二次验证。

const (
	ActionAgentCreate       = "agent.create"
	ActionAgentUpdate       = "agent.update"
	ActionUserAgentTransfer = "user.agent_transfer"

	maxAgentRemarkLen = 255
	maxAgentUsers     = 1000000
	maxAgentPlans     = 200

	msgAgentNotFound      = "代理不存在"
	msgAgentBadMaxUsers   = "用户数上限须在 0–1000000 之间"
	msgAgentBadPool       = "流量池总量不能为负"
	msgAgentTooManyPlans  = "白名单套餐最多 200 个"
	msgAgentBadPlanIDs    = "白名单含不存在的套餐"
	msgAgentPlanLimited   = "白名单只收流量与时长都有限的套餐"
	msgAgentPoolBelowUsed = "流量池总量不能低于已用量"
	msgAgentTransferBad   = "目标不是代理账号"
	msgAdminNameInvalid   = "用户名须为 3–64 位字母、数字或 _ . -"
)

// Agent 是超管看到的一个代理：账号、配额、白名单与名下未删除用户数。
type Agent struct {
	ID               int64
	Username         string
	IsDisabled       bool
	TOTPEnabled      bool
	LastLoginAt      *time.Time
	CreatedAt        time.Time
	Remark           string
	MaxUsers         int
	TrafficPoolTotal int64
	TrafficPoolUsed  int64
	PlanIDs          []int64
	UserCount        int
}

// AgentQuotaSpec 是代理的配额（新建与修改共用，修改为整体替换）。
type AgentQuotaSpec struct {
	Remark           string
	MaxUsers         int
	TrafficPoolTotal int64
	PlanIDs          []int64
}

func (in AgentQuotaSpec) validate() error {
	if utf8.RuneCountInString(in.Remark) > maxAgentRemarkLen {
		return newError(CodeInvalidArgument, msgRemarkTooLong)
	}
	if in.MaxUsers < 0 || in.MaxUsers > maxAgentUsers {
		return newError(CodeInvalidArgument, msgAgentBadMaxUsers)
	}
	if in.TrafficPoolTotal < 0 {
		return newError(CodeInvalidArgument, msgAgentBadPool)
	}
	if len(in.PlanIDs) > maxAgentPlans {
		return newError(CodeInvalidArgument, msgAgentTooManyPlans)
	}
	return nil
}

// AgentCreateInput 是新建代理的输入。
type AgentCreateInput struct {
	Username string
	Password string
	AgentQuotaSpec
}

// AgentUpdateInput 是修改代理的输入；IsDisabled 为真时吊销其全部会话。
type AgentUpdateInput struct {
	AgentQuotaSpec
	IsDisabled bool
}

// checkWhitelist 确认白名单里的套餐都存在、且流量与时长都有限（Ruling-103），返回去重后的 id。
func checkWhitelist(ctx context.Context, q *sqlcgen.Queries, ids []int64) ([]int64, error) {
	ids = dedupeIDs(ids)
	if len(ids) == 0 {
		return ids, nil
	}
	rows, err := q.ListPlanLimitsByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list plan limits: %w", err)
	}
	if len(rows) != len(ids) {
		return nil, newError(CodeInvalidArgument, msgAgentBadPlanIDs)
	}
	for _, r := range rows {
		if r.TrafficLimit <= 0 || r.DurationDays <= 0 {
			return nil, newError(CodeInvalidArgument, msgAgentPlanLimited)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func replaceAgentPlans(ctx context.Context, q *sqlcgen.Queries, agentID int64, ids []int64) error {
	if err := q.DeleteAgentPlans(ctx, agentID); err != nil {
		return fmt.Errorf("clear plans of agent %d: %w", agentID, err)
	}
	if len(ids) == 0 {
		return nil
	}
	if err := q.InsertAgentPlans(ctx, sqlcgen.InsertAgentPlansParams{AdminID: agentID, PlanIds: ids}); err != nil {
		return fmt.Errorf("insert plans of agent %d: %w", agentID, err)
	}
	return nil
}

// CreateAgent 新建代理账号，与配额、白名单同一事务建出（Ruling-103、Ruling-104）。
// 错误：CodeInvalidArgument、CodeConflict（用户名与管理员共用命名空间，重名）。
func (s *AgentService) CreateAgent(ctx context.Context, actor *AdminPrincipal, in AgentCreateInput, ip *netip.Addr) (*Agent, error) {
	if !usernamePattern.MatchString(in.Username) {
		return nil, newError(CodeInvalidArgument, msgAdminNameInvalid)
	}
	if err := validatePassword(in.Password); err != nil {
		return nil, err
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	hash, err := crypto.HashPassword(in.Password)
	if err != nil {
		return nil, fmt.Errorf("hash agent password: %w", err)
	}
	var id int64
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		plans, err := checkWhitelist(ctx, q, in.PlanIDs)
		if err != nil {
			return err
		}
		row, err := q.CreateAgentAdmin(ctx, sqlcgen.CreateAgentAdminParams{Username: in.Username, PasswordHash: hash})
		if isUniqueViolation(err) {
			return newError(CodeConflict, "用户名已存在")
		}
		if err != nil {
			return fmt.Errorf("create agent %s: %w", in.Username, err)
		}
		id = row.ID
		if err := q.CreateAgentProfile(ctx, sqlcgen.CreateAgentProfileParams{
			AdminID: id, MaxUsers: int32(in.MaxUsers), TrafficPoolTotal: in.TrafficPoolTotal, Remark: in.Remark,
		}); err != nil {
			return fmt.Errorf("create profile of agent %d: %w", id, err)
		}
		if err := replaceAgentPlans(ctx, q, id, plans); err != nil {
			return err
		}
		return writeAudit(ctx, q, adminAudit(actor, ActionAgentCreate, targetAdmin, id, ip, map[string]any{
			"username": in.Username, "maxUsers": in.MaxUsers, "trafficPoolTotal": in.TrafficPoolTotal, "planIds": plans,
		}))
	})
	if err != nil {
		return nil, err
	}
	return s.GetAgent(ctx, id)
}

// UpdateAgent 整体替换代理的备注、配额、白名单与停用状态（Ruling-103、Ruling-104）。
// 流量池总量低于已用量 409；停用时吊销其全部会话（邀请码随之不可用，名下用户不受影响）。错误：CodeInvalidArgument、CodeNotFound、CodeConflict。
func (s *AgentService) UpdateAgent(ctx context.Context, actor *AdminPrincipal, id int64, in AgentUpdateInput, ip *netip.Addr) (*Agent, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		profile, err := q.LockAgentProfile(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return newError(CodeNotFound, msgAgentNotFound)
		}
		if err != nil {
			return fmt.Errorf("lock agent profile %d: %w", id, err)
		}
		if in.TrafficPoolTotal < profile.TrafficPoolUsed {
			return newError(CodeConflict, msgAgentPoolBelowUsed)
		}
		plans, err := checkWhitelist(ctx, q, in.PlanIDs)
		if err != nil {
			return err
		}
		before, err := agentPlanIDs(ctx, q, []int64{id})
		if err != nil {
			return err
		}
		if err := q.UpdateAgentProfile(ctx, sqlcgen.UpdateAgentProfileParams{
			AdminID: id, MaxUsers: int32(in.MaxUsers), TrafficPoolTotal: in.TrafficPoolTotal, Remark: in.Remark,
		}); err != nil {
			return fmt.Errorf("update profile of agent %d: %w", id, err)
		}
		if err := replaceAgentPlans(ctx, q, id, plans); err != nil {
			return err
		}
		if err := q.UpdateAdminDisabled(ctx, sqlcgen.UpdateAdminDisabledParams{ID: id, IsDisabled: in.IsDisabled}); err != nil {
			return fmt.Errorf("update agent %d: %w", id, err)
		}
		if in.IsDisabled {
			if err := q.DeleteAdminSessionsByAdmin(ctx, id); err != nil {
				return fmt.Errorf("revoke sessions of agent %d: %w", id, err)
			}
		}
		return writeAudit(ctx, q, adminAudit(actor, ActionAgentUpdate, targetAdmin, id, ip, map[string]any{
			"trafficPoolTotal": [2]int64{profile.TrafficPoolTotal, in.TrafficPoolTotal},
			"maxUsers":         [2]int32{profile.MaxUsers, int32(in.MaxUsers)},
			"plansAdded":       idsMissing(plans, before[id]),
			"plansRemoved":     idsMissing(before[id], plans),
			"isDisabled":       in.IsDisabled,
		}))
	})
	if err != nil {
		return nil, err
	}
	return s.GetAgent(ctx, id)
}

// idsMissing 返回 a 中不在 b 里的 id（审计记白名单变化用）。
func idsMissing(a, b []int64) []int64 {
	out := make([]int64, 0, len(a))
	for _, id := range a {
		if !slices.Contains(b, id) {
			out = append(out, id)
		}
	}
	return out
}

func agentPlanIDs(ctx context.Context, q *sqlcgen.Queries, agentIDs []int64) (map[int64][]int64, error) {
	rows, err := q.ListAgentPlanIDs(ctx, agentIDs)
	if err != nil {
		return nil, fmt.Errorf("list agent plans: %w", err)
	}
	out := make(map[int64][]int64, len(agentIDs))
	for _, r := range rows {
		out[r.AdminID] = append(out[r.AdminID], r.PlanID)
	}
	return out, nil
}

func agentFromRow(r sqlcgen.ListAgentsRow, plans []int64) Agent {
	if plans == nil {
		plans = []int64{}
	}
	return Agent{
		ID: r.ID, Username: r.Username, IsDisabled: r.IsDisabled, TOTPEnabled: r.TotpEnabled, LastLoginAt: timePtr(r.LastLoginAt),
		CreatedAt: r.CreatedAt.Time, Remark: r.Remark, MaxUsers: int(r.MaxUsers), TrafficPoolTotal: r.TrafficPoolTotal,
		TrafficPoolUsed: r.TrafficPoolUsed, PlanIDs: plans, UserCount: int(r.UserCount),
	}
}

func (s *AgentService) agentsFromRows(ctx context.Context, rows []sqlcgen.ListAgentsRow) ([]Agent, error) {
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	plans, err := agentPlanIDs(ctx, s.q, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Agent, 0, len(rows))
	for _, r := range rows {
		out = append(out, agentFromRow(r, plans[r.ID]))
	}
	return out, nil
}

// ListAgents 分页列出代理，按创建时间倒序；keyword 按用户名模糊匹配。错误：CodeInvalidArgument。
func (s *AgentService) ListAgents(ctx context.Context, keyword string, pq PageQuery) (*Page[Agent], error) {
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(keyword) > maxAgentKeywordLen {
		return nil, newError(CodeInvalidArgument, msgAgentKeywordLong)
	}
	total, err := s.q.CountAgents(ctx, textArg(keyword))
	if err != nil {
		return nil, fmt.Errorf("count agents: %w", err)
	}
	if total == 0 {
		return &Page[Agent]{Items: []Agent{}, Total: 0}, nil
	}
	rows, err := s.q.ListAgents(ctx, sqlcgen.ListAgentsParams{Keyword: textArg(keyword), RowLimit: limit, RowOffset: offset})
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	items, err := s.agentsFromRows(ctx, rows)
	if err != nil {
		return nil, err
	}
	return &Page[Agent]{Items: items, Total: total}, nil
}

// GetAgent 返回一个代理；id 不是代理账号时 CodeNotFound。
func (s *AgentService) GetAgent(ctx context.Context, id int64) (*Agent, error) {
	rows, err := s.q.ListAgents(ctx, sqlcgen.ListAgentsParams{AgentID: int8Arg(id), RowLimit: 1})
	if err != nil {
		return nil, fmt.Errorf("get agent %d: %w", id, err)
	}
	if len(rows) == 0 {
		return nil, newError(CodeNotFound, msgAgentNotFound)
	}
	items, err := s.agentsFromRows(ctx, rows)
	if err != nil {
		return nil, err
	}
	return &items[0], nil
}

// ListAgentAllocations 列出某个代理的开通记录（线下结算的依据）。id 不是代理账号时 CodeNotFound。
func (s *AgentService) ListAgentAllocations(ctx context.Context, id int64, from, to *time.Time, pq PageQuery) (*Page[AgentAllocation], error) {
	ok, err := s.q.ExistsAgent(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("check agent %d: %w", id, err)
	}
	if !ok {
		return nil, newError(CodeNotFound, msgAgentNotFound)
	}
	return s.listAllocations(ctx, id, from, to, pq)
}

// TransferUser 把用户划给某个代理或收回为平台用户（agentID 为 nil，docs/03 §5.23 名下 ④）：不受用户数上限限制，
// 可以划给已停用的代理；只改归属，不动额度、邀请关系与流量池。错误：CodeInvalidArgument（目标不是代理）、CodeNotFound。
func (s *AgentService) TransferUser(ctx context.Context, actor *AdminPrincipal, userID int64, agentID *int64, ip *netip.Addr) (*User, error) {
	var target int64
	if agentID != nil {
		if *agentID < 1 {
			return nil, newError(CodeInvalidArgument, msgAgentTransferBad)
		}
		target = *agentID
	}
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := lockUser(ctx, q, userID); err != nil {
			return err
		}
		if target != 0 {
			ok, err := q.ExistsAgent(ctx, target)
			if err != nil {
				return fmt.Errorf("check agent %d: %w", target, err)
			}
			if !ok {
				return newError(CodeInvalidArgument, msgAgentTransferBad)
			}
		}
		// 行已锁，读到的就是划转前的归属
		current, err := q.ListUsers(ctx, sqlcgen.ListUsersParams{UserID: int8Arg(userID), RowLimit: 1})
		if err != nil {
			return fmt.Errorf("get user %d: %w", userID, err)
		}
		if len(current) == 0 {
			return newError(CodeNotFound, msgUserNotFound)
		}
		before := userFromRow(current[0])
		n, err := q.SetUserAgent(ctx, sqlcgen.SetUserAgentParams{ID: userID, AgentID: int8Arg(target)})
		if err != nil {
			return fmt.Errorf("transfer user %d: %w", userID, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgUserNotFound)
		}
		return writeAudit(ctx, q, adminAudit(actor, ActionUserAgentTransfer, targetUser, userID, ip,
			map[string]any{"fromAgentId": before.AgentID, "toAgentId": agentID}))
	})
	if err != nil {
		return nil, err
	}
	return s.users.GetUser(ctx, userID)
}
