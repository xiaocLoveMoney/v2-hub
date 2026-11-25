package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/crypto"
	"v2-hub-server/internal/manager/ratelimit"
)

// 二级代理的代理侧业务（docs/03 §5.23，Ruling-101 – Ruling-104）。
// 隔离靠两层：Web 层 requireAgent 只放行代理会话；这里每个方法先经 agentScope 取出会话里的代理 id，
// 每个查询都在 SQL 里带 agent_id = 当前代理 的条件（dao/queries/agent.sql 的 Agent* 查询），按 id 的写操作在同一事务里
// 先用 AgentLockUser / AgentLockTicket / AgentLockInviteCode 锁定并确认归属，查不到一律 404，不暴露对象是否存在。

const (
	// 开通记录的类型（agent_allocation.kind）
	AgentAllocationCreate = "create"
	AgentAllocationAssign = "assign"

	// 流量池按周期折算：周按 7 天（daysPerWeek 在 user_quota.go）、月按 30 天（Ruling-103）
	hoursPerDay       = 24
	agentDaysPerMonth = 30

	agentWriteWindow   = time.Hour
	agentGuardMaxKeys  = 1024
	maxAgentKeywordLen = 64

	msgAgentOnly           = "该接口只供代理账号使用"
	msgAgentWriteTooOften  = "操作过于频繁，请稍后再试"
	msgAgentPlanNotAllowed = "该套餐不在可分配范围内"
	msgAgentPlanUnlimited  = "该套餐的流量或时长为不限，不能由代理分配"
	msgAgentPlanTooLarge   = "该套餐的流量过大，无法计算流量池扣减量"
	msgAgentPoolShort      = "流量池剩余不足，不能开通该套餐"
	msgAgentUserLimit      = "名下用户数已达上限，不能再新建"
	msgAgentQuotaEmpty     = "请至少修改一项：流量额度或到期时间"
	msgAgentLimitNotLower  = "流量额度只能调低，且须大于 0"
	msgAgentExpiryNotEarly = "到期时间只能提前，不能推后"
	msgAgentExpiryPending  = "该用户还在等待首次使用，尚未开始计时，不能修改到期时间"
	msgAgentBadStatus      = "状态取值不合法"
	msgAgentKeywordLong    = "关键字过长"
	msgRemarkTooLong       = "备注过长"
)

// AgentServiceConfig 是代理侧的运行参数。
type AgentServiceConfig struct {
	// WriteRatePerHour 是每个代理账号的写操作每小时合计上限（配置 agent.writeRatePerHour，Ruling-104）。
	WriteRatePerHour int
	// Now 为 nil 时用 time.Now。
	Now func() time.Time
}

// AgentService 负责二级代理：代理侧的名下用户、配额、邀请码、工单、订单与流水，以及超管侧的代理管理与用户划转。
type AgentService struct {
	store
	users   *UserService
	tickets *TicketService
	writes  *ratelimit.FailureGuard
	now     func() time.Time
}

// NewAgentService 创建代理服务；users 提供与员工共用的用户事务内逻辑，tickets 提供工单提醒邮件。
func NewAgentService(pool *pgxpool.Pool, users *UserService, tickets *TicketService, cfg AgentServiceConfig) *AgentService {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	guard := ratelimit.NewFailureGuard(ratelimit.FailureGuardConfig{
		MaxFailures: cfg.WriteRatePerHour, Window: agentWriteWindow, Lockout: agentWriteWindow, MaxKeys: agentGuardMaxKeys,
	}, now)
	return &AgentService{store: newStore(pool), users: users, tickets: tickets, writes: guard, now: now}
}

// agentScope 返回会话里的代理 id；不是代理会话一律 CodePermissionDenied（Web 层已拦一次，这里是纵深防御）。
func agentScope(p *AdminPrincipal) (int64, error) {
	if p == nil || !p.IsAgent() || p.AdminID <= 0 {
		return 0, newError(CodePermissionDenied, msgAgentOnly)
	}
	return p.AdminID, nil
}

// agentKey 把代理 id 转成查询参数；agent_id 列可空，参数为 NULL 时查询匹配不到任何行（默认拒绝）。
func agentKey(agentID int64) pgtype.Int8 {
	return pgtype.Int8{Int64: agentID, Valid: agentID > 0}
}

// AllowWrite 记一次代理的写操作并判断是否超出每小时上限（Ruling-104）。错误：CodePermissionDenied、CodeRateLimited。
func (s *AgentService) AllowWrite(p *AdminPrincipal) error {
	agentID, err := agentScope(p)
	if err != nil {
		return err
	}
	key := strconv.FormatInt(agentID, 10)
	if s.writes.IsLocked(key) {
		return newError(CodeRateLimited, msgAgentWriteTooOften)
	}
	s.writes.RecordFailure(key)
	return nil
}

// ---- 流量池 ----------------------------------------------------------------------

// agentPoolCost 算开通或续期一次从流量池扣减的字节数 = 套餐流量 × 时长内的重置周期数（Ruling-103）：
// never 按 1 期，hourly / daily 按时长折算，weekly 按 7 天、monthly 按 30 天向上取整。
// 流量或时长不限（≤ 0）、重置周期不认识、乘积溢出 int64 时 ok 为假。
func agentPoolCost(trafficLimit int64, durationDays int, reset string) (cost int64, ok bool) {
	if trafficLimit <= 0 || durationDays <= 0 {
		return 0, false
	}
	days := int64(durationDays)
	var periods int64
	switch reset {
	case TrafficResetNever:
		periods = 1
	case TrafficResetHourly:
		periods = days * hoursPerDay
	case TrafficResetDaily:
		periods = days
	case TrafficResetWeekly:
		periods = ceilDiv(days, daysPerWeek)
	case TrafficResetMonthly:
		periods = ceilDiv(days, agentDaysPerMonth)
	default:
		return 0, false
	}
	if trafficLimit > math.MaxInt64/periods {
		return 0, false
	}
	return trafficLimit * periods, true
}

// ceilDiv 是正整数的向上取整除法（调用方保证 a > 0、b > 0）。
func ceilDiv(a, b int64) int64 {
	return (a + b - 1) / b
}

// agentPlanCost 校验白名单套餐可由代理分配并返回扣减量：流量与时长任一为不限返回 400（套餐后来被改成不限也拒，Ruling-103）。
func agentPlanCost(p sqlcgen.GetPlanRow) (int64, error) {
	if p.TrafficLimit <= 0 || p.DurationDays <= 0 {
		return 0, newError(CodeInvalidArgument, msgAgentPlanUnlimited)
	}
	cost, ok := agentPoolCost(p.TrafficLimit, int(p.DurationDays), p.TrafficReset)
	if !ok {
		return 0, newError(CodeInvalidArgument, msgAgentPlanTooLarge)
	}
	return cost, nil
}

// AgentPoolShortage 是 agent_pool_insufficient 错误的 data：本次要扣的量与池里还剩的量（字节）。
type AgentPoolShortage struct {
	PoolCost      int64 `json:"poolCost"`
	PoolRemaining int64 `json:"poolRemaining"`
}

// lockAgentProfile 锁住代理的配额行（锁顺序：配额行 → 用户行，Ruling-103）；代理已停用视为会话失效。
func lockAgentProfile(ctx context.Context, q *sqlcgen.Queries, agentID int64) (sqlcgen.LockAgentProfileRow, error) {
	profile, err := q.LockAgentProfile(ctx, agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return profile, newError(CodePermissionDenied, msgAgentOnly)
	}
	if err != nil {
		return profile, fmt.Errorf("lock agent profile %d: %w", agentID, err)
	}
	if profile.IsDisabled {
		return profile, newError(CodeUnauthenticated, msgLoginRequired)
	}
	return profile, nil
}

// lockAgentPlan 取白名单里的套餐并加共享锁；不在白名单（含套餐已删除）返回 400。
func lockAgentPlan(ctx context.Context, q *sqlcgen.Queries, agentID, planID int64) (sqlcgen.GetPlanRow, error) {
	row, err := q.AgentLockPlan(ctx, sqlcgen.AgentLockPlanParams{AgentID: agentID, PlanID: planID})
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlcgen.GetPlanRow{}, newError(CodeInvalidArgument, msgAgentPlanNotAllowed)
	}
	if err != nil {
		return sqlcgen.GetPlanRow{}, fmt.Errorf("lock plan %d of agent %d: %w", planID, agentID, err)
	}
	// 两条查询的行结构逐字段相同，可直接转换
	return sqlcgen.GetPlanRow(row), nil
}

// chargePool 判断流量池够不够并扣减（调用方已锁配额行）：不够返回 409 agent_pool_insufficient，data 带扣减量与剩余量。
func chargePool(ctx context.Context, q *sqlcgen.Queries, profile sqlcgen.LockAgentProfileRow, cost int64) error {
	remaining := profile.TrafficPoolTotal - profile.TrafficPoolUsed
	shortage := &Error{Code: CodeAgentPoolInsufficient, Message: msgAgentPoolShort, Data: AgentPoolShortage{PoolCost: cost, PoolRemaining: max(remaining, 0)}}
	if cost > remaining {
		return shortage
	}
	n, err := q.AddAgentPoolUsed(ctx, sqlcgen.AddAgentPoolUsedParams{AdminID: profile.AdminID, Amount: cost})
	if err != nil {
		return fmt.Errorf("charge pool of agent %d: %w", profile.AdminID, err)
	}
	if n == 0 {
		return shortage
	}
	return nil
}

// recordAllocation 写一条只追加的开通记录（Ruling-103）。
func recordAllocation(ctx context.Context, q *sqlcgen.Queries, agentID, userID int64, username string, p sqlcgen.GetPlanRow, cost int64, kind string) error {
	err := q.InsertAgentAllocation(ctx, sqlcgen.InsertAgentAllocationParams{
		AgentID: agentID, UserID: int8Arg(userID), Username: username, PlanID: int8Arg(p.ID), PlanName: p.Name,
		TrafficBytes: cost, DurationDays: p.DurationDays, Kind: kind,
	})
	if err != nil {
		return fmt.Errorf("insert allocation of agent %d: %w", agentID, err)
	}
	return nil
}

// ---- 配额概览与套餐 --------------------------------------------------------------

// AgentSummary 是代理自己的配额与用量。
type AgentSummary struct {
	MaxUsers         int
	UserCount        int
	TrafficPoolTotal int64
	TrafficPoolUsed  int64
}

// GetSummary 返回当前代理的配额与用量。
func (s *AgentService) GetSummary(ctx context.Context, p *AdminPrincipal) (*AgentSummary, error) {
	agentID, err := agentScope(p)
	if err != nil {
		return nil, err
	}
	row, err := s.q.AgentGetSummary(ctx, agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, newError(CodePermissionDenied, msgAgentOnly)
	}
	if err != nil {
		return nil, fmt.Errorf("get summary of agent %d: %w", agentID, err)
	}
	return &AgentSummary{MaxUsers: int(row.MaxUsers), UserCount: int(row.UserCount), TrafficPoolTotal: row.TrafficPoolTotal, TrafficPoolUsed: row.TrafficPoolUsed}, nil
}

// AgentPlan 是代理可分配的一个白名单套餐；PoolCost 是开通一次的扣减量（算不出时为 math.MaxInt64，表示无法开通）。
type AgentPlan struct {
	ID           int64
	Name         string
	TrafficLimit int64
	DurationDays int
	TrafficReset string
	LimitIP      int
	PoolCost     int64
}

// ListPlans 返回当前代理的白名单套餐，按套餐排序字段升序。
func (s *AgentService) ListPlans(ctx context.Context, p *AdminPrincipal) ([]AgentPlan, error) {
	agentID, err := agentScope(p)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.AgentListPlans(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("list plans of agent %d: %w", agentID, err)
	}
	out := make([]AgentPlan, 0, len(rows))
	for _, r := range rows {
		cost, ok := agentPoolCost(r.TrafficLimit, int(r.DurationDays), r.TrafficReset)
		if !ok {
			cost = math.MaxInt64
		}
		out = append(out, AgentPlan{
			ID: r.ID, Name: r.Name, TrafficLimit: r.TrafficLimit, DurationDays: int(r.DurationDays), TrafficReset: r.TrafficReset,
			LimitIP: int(r.LimitIp), PoolCost: cost,
		})
	}
	return out, nil
}

// ---- 名下用户 --------------------------------------------------------------------

// AgentUser 是代理看到的名下用户：不含下发状态、订阅凭据、邀请关系、免费续期等字段（Ruling-104）。
type AgentUser struct {
	ID           int64
	Username     string
	Email        string // 空串 = 未绑定
	Remark       string
	Status       string
	IsDisabled   bool
	PlanID       *int64
	PlanName     string
	TrafficLimit int64
	TrafficUsed  int64
	ExpiresAt    *time.Time
	PendingDays  int
	Balance      int64
	TOTPEnabled  bool
	CreatedAt    time.Time
	LastUsedAt   *time.Time
}

func agentUserFromRow(r sqlcgen.AgentListUsersRow) AgentUser {
	u := AgentUser{
		ID: r.ID, Username: r.Username, Email: r.Email.String, Remark: r.Remark, Status: r.Status, IsDisabled: r.IsDisabled,
		PlanName: r.PlanName, TrafficLimit: r.TrafficLimit, TrafficUsed: r.TrafficUsed, ExpiresAt: timePtr(r.ExpiresAt),
		PendingDays: int(r.PendingDays), Balance: r.Balance, TOTPEnabled: r.TotpEnabled, CreatedAt: r.CreatedAt.Time, LastUsedAt: timePtr(r.LastUsedAt),
	}
	if r.PlanID.Valid {
		id := r.PlanID.Int64
		u.PlanID = &id
	}
	return u
}

// AgentUserFilter 是名下用户列表的筛选；零值表示不限。
type AgentUserFilter struct {
	Keyword string
	Status  string
	PlanID  int64
}

// ListUsers 分页列出名下未删除的用户，按创建时间倒序。错误：CodeInvalidArgument。
func (s *AgentService) ListUsers(ctx context.Context, p *AdminPrincipal, f AgentUserFilter, pq PageQuery) (*Page[AgentUser], error) {
	agentID, err := agentScope(p)
	if err != nil {
		return nil, err
	}
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	if f.Status != "" && !isUserStatus(f.Status) {
		return nil, newError(CodeInvalidArgument, msgAgentBadStatus)
	}
	if utf8.RuneCountInString(f.Keyword) > maxAgentKeywordLen {
		return nil, newError(CodeInvalidArgument, msgAgentKeywordLong)
	}
	if f.PlanID < 0 {
		return nil, newError(CodeInvalidArgument, msgPlanNotFound)
	}
	total, err := s.q.AgentCountUsers(ctx, sqlcgen.AgentCountUsersParams{
		AgentID: agentKey(agentID), Keyword: textArg(f.Keyword), Status: textArg(f.Status), PlanID: int8Arg(f.PlanID),
	})
	if err != nil {
		return nil, fmt.Errorf("count users of agent %d: %w", agentID, err)
	}
	if total == 0 {
		return &Page[AgentUser]{Items: []AgentUser{}, Total: 0}, nil
	}
	rows, err := s.q.AgentListUsers(ctx, sqlcgen.AgentListUsersParams{
		AgentID: agentKey(agentID), Keyword: textArg(f.Keyword), Status: textArg(f.Status), PlanID: int8Arg(f.PlanID),
		RowLimit: limit, RowOffset: offset,
	})
	if err != nil {
		return nil, fmt.Errorf("list users of agent %d: %w", agentID, err)
	}
	items := make([]AgentUser, 0, len(rows))
	for _, r := range rows {
		items = append(items, agentUserFromRow(r))
	}
	return &Page[AgentUser]{Items: items, Total: total}, nil
}

// GetUser 返回一个名下用户；不在名下或待删除一律 CodeNotFound。
func (s *AgentService) GetUser(ctx context.Context, p *AdminPrincipal, id int64) (*AgentUser, error) {
	agentID, err := agentScope(p)
	if err != nil {
		return nil, err
	}
	return s.getUser(ctx, agentID, id)
}

func (s *AgentService) getUser(ctx context.Context, agentID, id int64) (*AgentUser, error) {
	rows, err := s.q.AgentListUsers(ctx, sqlcgen.AgentListUsersParams{AgentID: agentKey(agentID), UserID: int8Arg(id), RowLimit: 1})
	if err != nil {
		return nil, fmt.Errorf("get user %d of agent %d: %w", id, agentID, err)
	}
	if len(rows) == 0 {
		return nil, newError(CodeNotFound, msgUserNotFound)
	}
	u := agentUserFromRow(rows[0])
	return &u, nil
}

// lockAgentUser 锁住名下用户行；不在名下或待删除一律 CodeNotFound（代理侧按 id 写操作的唯一归属入口）。
func lockAgentUser(ctx context.Context, q *sqlcgen.Queries, agentID, id int64) (sqlcgen.GetUserQuotaRow, error) {
	row, err := q.AgentLockUser(ctx, sqlcgen.AgentLockUserParams{ID: id, AgentID: agentKey(agentID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlcgen.GetUserQuotaRow{}, newError(CodeNotFound, msgUserNotFound)
	}
	if err != nil {
		return sqlcgen.GetUserQuotaRow{}, fmt.Errorf("lock user %d of agent %d: %w", id, agentID, err)
	}
	// 两条查询的行结构逐字段相同，可直接转换
	return sqlcgen.GetUserQuotaRow(row), nil
}

// AgentUserCreateInput 是代理新建名下用户的输入；PlanID 必填且须在白名单内。
type AgentUserCreateInput struct {
	Username string
	Password string
	Email    string
	Remark   string
	PlanID   int64
}

// CreateUser 新建名下用户并同时开通一个白名单套餐（扣流量池，开通记录 kind = create，Ruling-103）。
// 错误：CodeInvalidArgument（字段不合法、套餐不在白名单或为不限）、CodeAgentUserLimit、CodeAgentPoolInsufficient、CodeConflict（用户名或邮箱已占用）。
func (s *AgentService) CreateUser(ctx context.Context, p *AdminPrincipal, in AgentUserCreateInput, ip *netip.Addr) (*AgentUser, error) {
	agentID, err := agentScope(p)
	if err != nil {
		return nil, err
	}
	in.Username = strings.TrimSpace(in.Username)
	if !endUserNamePattern.MatchString(in.Username) {
		return nil, newError(CodeInvalidArgument, msgBadUsername)
	}
	if err := validateUserLoginSecret(in.Password); err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(in.Remark) > maxUserRemarkLen {
		return nil, newError(CodeInvalidArgument, msgRemarkTooLong)
	}
	if in.PlanID < 1 {
		return nil, newError(CodeInvalidArgument, msgAgentPlanNotAllowed)
	}
	email := ""
	if strings.TrimSpace(in.Email) != "" {
		if email = normalizeEmail(in.Email); email == "" {
			return nil, newError(CodeInvalidArgument, msgMailBadAddress)
		}
	}
	hash, err := crypto.HashPassword(in.Password)
	if err != nil {
		return nil, fmt.Errorf("hash user password: %w", err)
	}
	now := s.now()
	var id int64
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		profile, err := lockAgentProfile(ctx, q, agentID)
		if err != nil {
			return err
		}
		plan, err := lockAgentPlan(ctx, q, agentID, in.PlanID)
		if err != nil {
			return err
		}
		cost, err := agentPlanCost(plan)
		if err != nil {
			return err
		}
		n, err := q.CountAgentUsers(ctx, agentKey(agentID))
		if err != nil {
			return fmt.Errorf("count users of agent %d: %w", agentID, err)
		}
		if n >= int64(profile.MaxUsers) {
			return newError(CodeAgentUserLimit, msgAgentUserLimit)
		}
		if err := chargePool(ctx, q, profile, cost); err != nil {
			return err
		}
		spec := newUserSpec{Username: in.Username, LoginHash: hash, Remark: in.Remark, Email: email, EmailVerified: true, PlanID: plan.ID, AgentID: agentID}
		if id, err = s.users.createUserTx(ctx, q, spec, now); err != nil {
			return err
		}
		if err := recordAllocation(ctx, q, agentID, id, in.Username, plan, cost, AgentAllocationCreate); err != nil {
			return err
		}
		return writeAudit(ctx, q, adminAudit(p, ActionUserCreate, targetUser, id, ip,
			map[string]any{"username": in.Username, "planId": plan.ID, "poolCost": cost}))
	})
	if err != nil {
		return nil, err
	}
	s.users.TriggerReconcile()
	return s.getUser(ctx, agentID, id)
}

// AgentUserUpdateInput 是代理改名下用户的输入：只有备注与启用 / 停用。
type AgentUserUpdateInput struct {
	Remark     string
	IsDisabled bool
}

// UpdateUser 改名下用户的备注与启用 / 停用。错误：CodeInvalidArgument、CodeNotFound。
func (s *AgentService) UpdateUser(ctx context.Context, p *AdminPrincipal, id int64, in AgentUserUpdateInput, ip *netip.Addr) (*AgentUser, error) {
	agentID, err := agentScope(p)
	if err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(in.Remark) > maxUserRemarkLen {
		return nil, newError(CodeInvalidArgument, msgRemarkTooLong)
	}
	now := s.now()
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := lockAgentUser(ctx, q, agentID, id)
		if err != nil {
			return err
		}
		return s.users.updateUserTx(ctx, q, p, row, in.Remark, in.IsDisabled, now, ip)
	})
	if err != nil {
		return nil, err
	}
	return s.getUser(ctx, agentID, id)
}

// DeleteUser 删除名下用户（流程同员工删除；流量池不退回，开通记录保留，Ruling-103）。错误：CodeNotFound。
func (s *AgentService) DeleteUser(ctx context.Context, p *AdminPrincipal, id int64, ip *netip.Addr) error {
	agentID, err := agentScope(p)
	if err != nil {
		return err
	}
	var servers []int64
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := lockAgentUser(ctx, q, agentID, id); err != nil {
			return err
		}
		servers, err = s.users.deleteUserTx(ctx, q, id, adminAudit(p, ActionUserDelete, targetUser, id, ip, nil))
		return err
	})
	if err != nil {
		return err
	}
	s.users.reconcileServers(ctx, servers)
	return nil
}

// AssignPlan 给名下用户分配或续期白名单套餐（规则同员工分配，扣流量池，开通记录 kind = assign，Ruling-103）。
// 先锁配额行再锁用户行，顺序固定防死锁。错误：CodeInvalidArgument、CodeNotFound、CodeConflict（用户已停用）、CodeAgentPoolInsufficient。
func (s *AgentService) AssignPlan(ctx context.Context, p *AdminPrincipal, id, planID int64, ip *netip.Addr) (*AgentUser, error) {
	agentID, err := agentScope(p)
	if err != nil {
		return nil, err
	}
	if planID < 1 {
		return nil, newError(CodeInvalidArgument, msgAgentPlanNotAllowed)
	}
	now := s.now()
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		profile, err := lockAgentProfile(ctx, q, agentID)
		if err != nil {
			return err
		}
		row, err := lockAgentUser(ctx, q, agentID, id)
		if err != nil {
			return err
		}
		if row.IsDisabled {
			return newError(CodeConflict, msgUserDisabled)
		}
		plan, err := lockAgentPlan(ctx, q, agentID, planID)
		if err != nil {
			return err
		}
		cost, err := agentPlanCost(plan)
		if err != nil {
			return err
		}
		if err := chargePool(ctx, q, profile, cost); err != nil {
			return err
		}
		if err := recordAllocation(ctx, q, agentID, id, row.Username, plan, cost, AgentAllocationAssign); err != nil {
			return err
		}
		return s.users.assignPlanTx(ctx, q, p, row, plan, now, ip)
	})
	if err != nil {
		return nil, err
	}
	return s.getUser(ctx, agentID, id)
}

// AgentQuotaInput 是代理收紧名下用户额度的输入（Ruling-103）：nil 表示不改，至少给一个。
type AgentQuotaInput struct {
	TrafficLimit *int64
	ExpiresAt    *time.Time
}

// applyAgentQuota 按 docs/03 §5.23“调低的判定”把收紧后的值写进 qt；违反返回 400。与当前值相等视为不变。
func applyAgentQuota(qt *Quota, in AgentQuotaInput) error {
	if in.TrafficLimit == nil && in.ExpiresAt == nil {
		return newError(CodeInvalidArgument, msgAgentQuotaEmpty)
	}
	if in.TrafficLimit != nil {
		limit := *in.TrafficLimit
		lowered := limit > 0 && (qt.TrafficLimit == 0 || limit <= qt.TrafficLimit)
		if !lowered {
			return newError(CodeInvalidArgument, msgAgentLimitNotLower)
		}
		qt.TrafficLimit = limit
	}
	if in.ExpiresAt == nil {
		return nil
	}
	if qt.ExpiresAt == nil && qt.PendingDays > 0 {
		return newError(CodeInvalidArgument, msgAgentExpiryPending)
	}
	if qt.ExpiresAt != nil && in.ExpiresAt.After(*qt.ExpiresAt) {
		return newError(CodeInvalidArgument, msgAgentExpiryNotEarly)
	}
	t := *in.ExpiresAt
	qt.ExpiresAt, qt.PendingDays = &t, 0
	return nil
}

// UpdateQuota 调低名下用户的流量额度、把到期时间提前（只能收紧，不动流量池、不写开通记录）。
// 错误：CodeInvalidArgument、CodeNotFound。
func (s *AgentService) UpdateQuota(ctx context.Context, p *AdminPrincipal, id int64, in AgentQuotaInput, ip *netip.Addr) (*AgentUser, error) {
	agentID, err := agentScope(p)
	if err != nil {
		return nil, err
	}
	now := s.now()
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := lockAgentUser(ctx, q, agentID, id)
		if err != nil {
			return err
		}
		before := quotaFromRow(row)
		qt := before
		if err := applyAgentQuota(&qt, in); err != nil {
			return err
		}
		status := qt.Status(now)
		if err := saveQuota(ctx, q, id, row.PlanID, qt, status); err != nil {
			return err
		}
		detail := map[string]any{"trafficLimit": [2]int64{before.TrafficLimit, qt.TrafficLimit}, "expiresAt": qt.ExpiresAt}
		return s.users.afterQuotaChange(ctx, q, p, id, ActionUserQuota, ip, detail, row.Status != status)
	})
	if err != nil {
		return nil, err
	}
	return s.getUser(ctx, agentID, id)
}

// ResetUserPassword 重置名下用户的客户后台密码并吊销其会话。先锁定并确认归属。错误：CodeInvalidArgument、CodeNotFound。
func (s *AgentService) ResetUserPassword(ctx context.Context, p *AdminPrincipal, id int64, pw string, ip *netip.Addr) error {
	agentID, err := agentScope(p)
	if err != nil {
		return err
	}
	if err := validateUserLoginSecret(pw); err != nil {
		return err
	}
	hash, err := crypto.HashPassword(pw)
	if err != nil {
		return fmt.Errorf("hash user password: %w", err)
	}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := lockAgentUser(ctx, q, agentID, id); err != nil {
			return err
		}
		return resetUserPasswordTx(ctx, q, p, id, hash, ip)
	})
}

// ClearUserTOTP 清除名下用户的两步验证并吊销其会话。错误：CodeNotFound。
func (s *AgentService) ClearUserTOTP(ctx context.Context, p *AdminPrincipal, id int64, ip *netip.Addr) error {
	agentID, err := agentScope(p)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := lockAgentUser(ctx, q, agentID, id); err != nil {
			return err
		}
		return clearUserTOTPTx(ctx, q, p, id, ip)
	})
}

// ResetUserSubscription 重置名下用户的订阅地址并轮换连接凭据，随后触发对账。错误：CodeNotFound。
func (s *AgentService) ResetUserSubscription(ctx context.Context, p *AdminPrincipal, id int64, ip *netip.Addr) error {
	agentID, err := agentScope(p)
	if err != nil {
		return err
	}
	rot, err := s.users.newSubRotation(id)
	if err != nil {
		return err
	}
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := lockAgentUser(ctx, q, agentID, id); err != nil {
			return err
		}
		return rotateSubscriptionTx(ctx, q, id, rot, AuditEntry{ActorType: ActorAdmin, ActorID: p.AdminID, Action: ActionUserSubReset, ClientIP: ip})
	})
	if err != nil {
		return err
	}
	s.users.TriggerReconcile()
	return nil
}
