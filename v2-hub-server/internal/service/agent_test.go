package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"
)

// 二级代理的业务层用例（docs/03 §5.23，Ruling-101 – Ruling-104）：扣减量计算、调低判定、并发配额、注册归属与超管侧管理。
// 接口层的越权矩阵见 api/agent_test.go。

type agentFixture struct {
	*accountFixture
	agents *AgentService
}

const testAgentWriteRate = 10000

func newAgentFixture(t *testing.T) *agentFixture {
	t.Helper()
	af := newAccountFixture(t)
	tickets := NewTicketService(af.pool, af.settings, af.mail, nil, af.clock.now)
	agents := NewAgentService(af.pool, af.users, tickets, AgentServiceConfig{WriteRatePerHour: testAgentWriteRate, Now: af.clock.now})
	return &agentFixture{accountFixture: af, agents: agents}
}

// newAgent 由超管建一个代理并登录，返回代理与它的会话身份。
func (f *agentFixture) newAgent(t *testing.T, name string, maxUsers int, pool int64, planIDs ...int64) (*Agent, *AdminPrincipal) {
	t.Helper()
	in := AgentCreateInput{Username: name, AgentQuotaSpec: AgentQuotaSpec{MaxUsers: maxUsers, TrafficPoolTotal: pool, PlanIDs: planIDs}}
	in.Password = cred
	a, err := f.agents.CreateAgent(context.Background(), f.actor, in, nil)
	if err != nil {
		t.Fatalf("CreateAgent(%s): %v", name, err)
	}
	return a, f.adminLogin(t, name).Principal
}

// limitedPlan 建一个流量与时长都有限的套餐。
func (f *agentFixture) limitedPlan(t *testing.T, name string, traffic int64, days int, reset string) *Plan {
	t.Helper()
	return f.plan(t, name, PlanQuota{TrafficLimit: traffic, DurationDays: days, TrafficReset: reset, TrafficResetDay: 1})
}

// adminLogin 以管理端账号（员工或代理）登录。
func (f *agentFixture) adminLogin(t *testing.T, username string) *LoginResult {
	t.Helper()
	res, err := f.auth.Login(context.Background(), LoginInput{Username: username, Password: cred})
	if err != nil {
		t.Fatalf("Login(%s): %v", username, err)
	}
	return res
}

func (f *agentFixture) poolUsed(t *testing.T, agentID int64) int64 {
	t.Helper()
	var used int64
	if err := f.pool.QueryRow(context.Background(), `SELECT traffic_pool_used FROM agent_profile WHERE admin_id = $1`, agentID).Scan(&used); err != nil {
		t.Fatalf("read pool: %v", err)
	}
	return used
}

func expectCode(t *testing.T, err error, want Code) {
	t.Helper()
	var se *Error
	if !errors.As(err, &se) || se.Code != want {
		t.Fatalf("err = %v, want %s", err, want)
	}
}

func TestAgentPoolCost(t *testing.T) {
	cases := []struct {
		name    string
		traffic int64
		days    int
		reset   string
		want    int64
		ok      bool
	}{
		{"never 按 1 期", gib, 30, TrafficResetNever, gib, true},
		{"hourly 按小时折算", gib, 2, TrafficResetHourly, 48 * gib, true},
		{"daily 按天", gib, 30, TrafficResetDaily, 30 * gib, true},
		{"weekly 恰好整周", gib, 14, TrafficResetWeekly, 2 * gib, true},
		{"weekly 向上取整", gib, 8, TrafficResetWeekly, 2 * gib, true},
		{"weekly 不足一周按一周", gib, 1, TrafficResetWeekly, gib, true},
		{"monthly 恰好 30 天", gib, 30, TrafficResetMonthly, gib, true},
		{"monthly 31 天算两期", gib, 31, TrafficResetMonthly, 2 * gib, true},
		{"monthly 一年 13 期", gib, 365, TrafficResetMonthly, 13 * gib, true},
		{"不限流量", 0, 30, TrafficResetNever, 0, false},
		{"不限时长", gib, 0, TrafficResetNever, 0, false},
		{"周期不认识", gib, 30, "yearly", 0, false},
		{"恰好不溢出", math.MaxInt64 / 2, 2, TrafficResetDaily, math.MaxInt64 / 2 * 2, true},
		{"溢出", math.MaxInt64/2 + 1, 2, TrafficResetDaily, 0, false},
		{"hourly 大时长溢出", math.MaxInt64 / 24, 36500, TrafficResetHourly, 0, false},
		{"最大值 never", math.MaxInt64, 1, TrafficResetNever, math.MaxInt64, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := agentPoolCost(c.traffic, c.days, c.reset)
			if ok != c.ok || got != c.want {
				t.Fatalf("agentPoolCost(%d, %d, %s) = (%d, %v), want (%d, %v)", c.traffic, c.days, c.reset, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestApplyAgentQuota(t *testing.T) {
	now := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	later, earlier, past := now.Add(48*time.Hour), now.Add(24*time.Hour), now.Add(-time.Hour)
	i64 := func(v int64) *int64 { return &v }
	tm := func(v time.Time) *time.Time { return &v }
	cases := []struct {
		name    string
		cur     Quota
		in      AgentQuotaInput
		wantErr bool
		limit   int64
		expires *time.Time
	}{
		{"至少给一个", Quota{TrafficLimit: gib}, AgentQuotaInput{}, true, 0, nil},
		{"调低额度", Quota{TrafficLimit: 10 * gib}, AgentQuotaInput{TrafficLimit: i64(gib)}, false, gib, nil},
		{"相等视为不变", Quota{TrafficLimit: gib}, AgentQuotaInput{TrafficLimit: i64(gib)}, false, gib, nil},
		{"不能调高", Quota{TrafficLimit: gib}, AgentQuotaInput{TrafficLimit: i64(gib + 1)}, true, 0, nil},
		{"不能为 0（不限）", Quota{TrafficLimit: gib}, AgentQuotaInput{TrafficLimit: i64(0)}, true, 0, nil},
		{"不能为负", Quota{TrafficLimit: gib}, AgentQuotaInput{TrafficLimit: i64(-1)}, true, 0, nil},
		{"当前不限时任意正数都算调低", Quota{TrafficLimit: 0}, AgentQuotaInput{TrafficLimit: i64(math.MaxInt64)}, false, math.MaxInt64, nil},
		{"到期提前", Quota{ExpiresAt: tm(later)}, AgentQuotaInput{ExpiresAt: tm(earlier)}, false, 0, tm(earlier)},
		{"到期相等", Quota{ExpiresAt: tm(later)}, AgentQuotaInput{ExpiresAt: tm(later)}, false, 0, tm(later)},
		{"到期推后", Quota{ExpiresAt: tm(earlier)}, AgentQuotaInput{ExpiresAt: tm(later)}, true, 0, nil},
		{"当前不过期时任何时间都算提前，可为过去", Quota{}, AgentQuotaInput{ExpiresAt: tm(past)}, false, 0, tm(past)},
		{"等待首次使用时不能改到期", Quota{PendingDays: 30}, AgentQuotaInput{ExpiresAt: tm(past)}, true, 0, nil},
		{"等待首次使用时可以调低额度", Quota{PendingDays: 30, TrafficLimit: gib}, AgentQuotaInput{TrafficLimit: i64(1)}, false, 1, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			qt := c.cur
			err := applyAgentQuota(&qt, c.in)
			if c.wantErr {
				expectCode(t, err, CodeInvalidArgument)
				return
			}
			if err != nil {
				t.Fatalf("applyAgentQuota: %v", err)
			}
			if c.in.TrafficLimit != nil && qt.TrafficLimit != c.limit {
				t.Fatalf("limit = %d, want %d", qt.TrafficLimit, c.limit)
			}
			if c.expires != nil && (qt.ExpiresAt == nil || !qt.ExpiresAt.Equal(*c.expires) || qt.PendingDays != 0) {
				t.Fatalf("expires = %v pending %d, want %v", qt.ExpiresAt, qt.PendingDays, c.expires)
			}
		})
	}
}

// 代理的权限集合恒为空、不是超管：库里即使有 super_admin 的角色关联行也不读（Ruling-101）。
func TestAgentPrincipalIgnoresRoleRows(t *testing.T) {
	f := newAgentFixture(t)
	ctx := context.Background()
	a, _ := f.newAgent(t, "agent-r", 1, gib)
	if _, err := f.pool.Exec(ctx, `INSERT INTO admin_role (admin_id, role_id) SELECT $1, id FROM role WHERE code = 'super_admin'`, a.ID); err != nil {
		t.Fatalf("insert role row: %v", err)
	}
	p := f.adminLogin(t, "agent-r").Principal
	if !p.IsAgent() || p.IsSuperAdmin || len(p.Permissions) != 0 || p.Can(PermUserRead) || p.TOTPSetupRequired() {
		t.Fatalf("agent principal = %+v", p)
	}
	auth, err := f.auth.Authenticate(ctx, f.adminLogin(t, "agent-r").Token)
	if err != nil || !auth.IsAgent() || auth.IsSuperAdmin || len(auth.Permissions) != 0 {
		t.Fatalf("Authenticate = %+v, %v", auth, err)
	}
}

// 并发开通不超卖流量池：池里只够 3 次，6 个用户同时开通只成功 3 个（Ruling-103）。
func TestAgentConcurrentAssignDoesNotOversellPool(t *testing.T) {
	f := newAgentFixture(t)
	ctx := context.Background()
	plan := f.limitedPlan(t, "p1", gib, 30, TrafficResetNever)
	a, p := f.newAgent(t, "agent-c", 100, 3*gib, plan.ID)
	const users = 6
	ids := make([]int64, 0, users)
	for i := range users {
		u := f.newUser(t, fmt.Sprintf("cu%d", i), 0)
		if _, err := f.agents.TransferUser(ctx, f.actor, u.ID, &a.ID, nil); err != nil {
			t.Fatalf("TransferUser: %v", err)
		}
		ids = append(ids, u.ID)
	}
	var wg sync.WaitGroup
	errs := make([]error, users)
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.agents.AssignPlan(ctx, p, id, plan.ID, nil)
		}()
	}
	wg.Wait()
	ok, short := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, &Error{Code: CodeAgentPoolInsufficient}):
			short++
		default:
			t.Fatalf("AssignPlan: %v", err)
		}
	}
	if ok != 3 || short != 3 || f.poolUsed(t, a.ID) != 3*gib {
		t.Fatalf("ok=%d short=%d used=%d", ok, short, f.poolUsed(t, a.ID))
	}
	page, err := f.agents.ListAgentAllocations(ctx, a.ID, nil, nil, PageQuery{})
	if err != nil || page.Total != 3 {
		t.Fatalf("allocations = %+v, %v", page, err)
	}
}

// 并发新建不突破用户数上限（Ruling-103）。
func TestAgentConcurrentCreateRespectsUserLimit(t *testing.T) {
	f := newAgentFixture(t)
	ctx := context.Background()
	plan := f.limitedPlan(t, "p1", gib, 30, TrafficResetNever)
	a, p := f.newAgent(t, "agent-l", 2, 100*gib, plan.ID)
	const n = 5
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := AgentUserCreateInput{Username: fmt.Sprintf("lu%d", i), PlanID: plan.ID}
			in.Password = cred
			_, errs[i] = f.agents.CreateUser(ctx, p, in, nil)
		}()
	}
	wg.Wait()
	ok, limited := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, &Error{Code: CodeAgentUserLimit}):
			limited++
		default:
			t.Fatalf("CreateUser: %v", err)
		}
	}
	got, err := f.agents.GetAgent(ctx, a.ID)
	if err != nil || ok != 2 || limited != 3 || got.UserCount != 2 || got.TrafficPoolUsed != 2*gib {
		t.Fatalf("ok=%d limited=%d agent=%+v err=%v", ok, limited, got, err)
	}
}

// 并发注册不突破用户数上限：达上限后代理码按邀请码不可用处理（docs/03 §5.23 注册）。
func TestAgentConcurrentRegisterRespectsUserLimit(t *testing.T) {
	f := newAgentFixture(t)
	ctx := context.Background()
	f.openRegistration(t)
	_, p := f.newAgent(t, "agent-g", 2, gib)
	code, err := f.agents.CreateInviteCode(ctx, p, InviteCodeInput{}, nil)
	if err != nil {
		t.Fatalf("CreateInviteCode: %v", err)
	}
	const n = 5
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := registerInput(fmt.Sprintf("ru%d", i))
			in.InviteCode = code.Code
			_, errs[i] = f.account.Register(ctx, in)
		}()
	}
	wg.Wait()
	ok, invalid := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrInvalidArgument):
			invalid++
		default:
			t.Fatalf("Register: %v", err)
		}
	}
	sum, err := f.agents.GetSummary(ctx, p)
	if err != nil || ok != 2 || invalid != 3 || sum.UserCount != 2 {
		t.Fatalf("ok=%d invalid=%d summary=%+v err=%v", ok, invalid, sum, err)
	}
}

// 套餐不在白名单、不限流量 / 不限时长（含事后被改成不限）一律 400；池不足返回 409 且 data 正确（Ruling-103）。
func TestAgentPlanRulesAndPoolShortage(t *testing.T) {
	f := newAgentFixture(t)
	ctx := context.Background()
	daily := f.limitedPlan(t, "daily", gib, 10, TrafficResetDaily) // 扣 10 GiB
	other := f.limitedPlan(t, "other", gib, 30, TrafficResetNever)
	unlimited := f.plan(t, "unl", PlanQuota{TrafficLimit: 0, DurationDays: 30})
	if _, err := f.agents.CreateAgent(ctx, f.actor, AgentCreateInput{Username: "agent-x", Password: cred,
		AgentQuotaSpec: AgentQuotaSpec{MaxUsers: 1, PlanIDs: []int64{unlimited.ID}}}, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unlimited plan in whitelist = %v, want invalid_argument", err)
	}
	if _, err := f.agents.CreateAgent(ctx, f.actor, AgentCreateInput{Username: "agent-x", Password: cred,
		AgentQuotaSpec: AgentQuotaSpec{MaxUsers: 1, PlanIDs: []int64{999999}}}, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("missing plan in whitelist = %v, want invalid_argument", err)
	}
	a, p := f.newAgent(t, "agent-p", 10, 15*gib, daily.ID)
	in := AgentUserCreateInput{Username: "pu1", PlanID: other.ID}
	in.Password = cred
	if _, err := f.agents.CreateUser(ctx, p, in, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("plan not in whitelist = %v, want invalid_argument", err)
	}
	in.PlanID = daily.ID
	u, err := f.agents.CreateUser(ctx, p, in, nil)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.PlanID == nil || *u.PlanID != daily.ID || u.TrafficLimit != gib || f.poolUsed(t, a.ID) != 10*gib {
		t.Fatalf("created user = %+v used=%d", u, f.poolUsed(t, a.ID))
	}
	_, err = f.agents.AssignPlan(ctx, p, u.ID, daily.ID, nil)
	var se *Error
	if !errors.As(err, &se) || se.Code != CodeAgentPoolInsufficient {
		t.Fatalf("assign beyond pool = %v", err)
	}
	if d, ok := se.Data.(AgentPoolShortage); !ok || d.PoolCost != 10*gib || d.PoolRemaining != 5*gib {
		t.Fatalf("shortage data = %#v", se.Data)
	}
	// 套餐事后被改成不限时长：仍在白名单，但不能再开通
	if _, err := f.users.UpdatePlan(ctx, f.actor, daily.ID, PlanInput{Name: "daily", NodeGroupIDs: []int64{f.group.ID},
		PlanQuota: PlanQuota{TrafficLimit: gib, DurationDays: 0, TrafficReset: TrafficResetDaily, TrafficResetDay: 1}}, nil); err != nil {
		t.Fatalf("UpdatePlan: %v", err)
	}
	if _, err := f.agents.AssignPlan(ctx, p, u.ID, daily.ID, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("assign plan turned unlimited = %v, want invalid_argument", err)
	}
	if f.poolUsed(t, a.ID) != 10*gib {
		t.Fatalf("pool changed on rejected assign: %d", f.poolUsed(t, a.ID))
	}
}

// 删除用户不退回流量池，开通记录保留并在用户清除后 userId 变空（Ruling-103）。
func TestAgentDeleteUserDoesNotRefundPool(t *testing.T) {
	f := newAgentFixture(t)
	ctx := context.Background()
	plan := f.limitedPlan(t, "p1", gib, 30, TrafficResetNever)
	a, p := f.newAgent(t, "agent-d", 10, 10*gib, plan.ID)
	in := AgentUserCreateInput{Username: "du1", PlanID: plan.ID}
	in.Password = cred
	u, err := f.agents.CreateUser(ctx, p, in, nil)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := f.agents.DeleteUser(ctx, p, u.ID, nil); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := f.agents.GetUser(ctx, p, u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetUser after delete = %v, want not_found", err)
	}
	if f.poolUsed(t, a.ID) != gib {
		t.Fatalf("pool refunded: %d", f.poolUsed(t, a.ID))
	}
	page, err := f.agents.ListOwnAllocations(ctx, p, nil, nil, PageQuery{})
	if err != nil || page.Total != 1 || page.Items[0].Username != "du1" || page.Items[0].Kind != AgentAllocationCreate ||
		page.Items[0].TrafficBytes != gib || page.Items[0].DurationDays != 30 {
		t.Fatalf("allocations = %+v, %v", page, err)
	}
	sum, err := f.agents.GetSummary(ctx, p)
	if err != nil || sum.UserCount != 0 {
		t.Fatalf("summary = %+v, %v", sum, err)
	}
}

// 代理调额只能收紧，判定见 docs/03 §5.23“调低的判定”。
func TestAgentUpdateQuotaOnlyTightens(t *testing.T) {
	f := newAgentFixture(t)
	ctx := context.Background()
	plan := f.limitedPlan(t, "p1", 10*gib, 30, TrafficResetNever)
	_, p := f.newAgent(t, "agent-q", 10, 100*gib, plan.ID)
	in := AgentUserCreateInput{Username: "qu1", PlanID: plan.ID}
	in.Password = cred
	u, err := f.agents.CreateUser(ctx, p, in, nil)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	higher := 11 * gib
	if _, err := f.agents.UpdateQuota(ctx, p, u.ID, AgentQuotaInput{TrafficLimit: &higher}, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("raise limit = %v", err)
	}
	later := u.ExpiresAt.Add(time.Hour)
	if _, err := f.agents.UpdateQuota(ctx, p, u.ID, AgentQuotaInput{ExpiresAt: &later}, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("postpone expiry = %v", err)
	}
	lower, past := gib, f.clock.now().Add(-time.Hour)
	got, err := f.agents.UpdateQuota(ctx, p, u.ID, AgentQuotaInput{TrafficLimit: &lower, ExpiresAt: &past}, nil)
	if err != nil {
		t.Fatalf("UpdateQuota: %v", err)
	}
	if got.TrafficLimit != gib || got.ExpiresAt == nil || !got.ExpiresAt.Equal(past) || got.Status != UserStatusExpired {
		t.Fatalf("after tighten = %+v", got)
	}
}

// 注册归属：代理码写归属、无邀请人、不送注册赠送套餐；名下用户的个人码拉来的新用户随邀请人归属、邀请关系照旧；
// 代理停用时代理码不可用；注册关闭时代理码也不能注册（docs/03 §5.23 注册，Ruling-102）。
func TestAgentRegistrationAttribution(t *testing.T) {
	f := newAgentFixture(t)
	ctx := context.Background()
	f.openRegistration(t)
	gift := f.limitedPlan(t, "gift", gib, 7, TrafficResetNever)
	if _, err := f.update(t, map[string]any{"register.planId": gift.ID, "invite.userEnabled": true}); err != nil {
		t.Fatalf("settings: %v", err)
	}
	a, p := f.newAgent(t, "agent-i", 3, gib)
	code, err := f.agents.CreateInviteCode(ctx, p, InviteCodeInput{}, nil)
	if err != nil {
		t.Fatalf("CreateInviteCode: %v", err)
	}
	in := registerInput("reg1")
	in.InviteCode = code.Code
	res, err := f.account.Register(ctx, in)
	if err != nil {
		t.Fatalf("Register via agent code: %v", err)
	}
	reg1 := f.principalOf(t, res).UserID
	u, err := f.users.GetUser(ctx, reg1)
	if err != nil || u.AgentID == nil || *u.AgentID != a.ID || u.InvitedBy != nil || u.PlanID != nil {
		t.Fatalf("agent-code user = %+v, %v", u, err)
	}
	// 名下用户的个人码：新用户随邀请人归属，邀请关系照旧
	info, err := f.account.GetInvite(ctx, f.principalOf(t, res))
	if err != nil || info.Code == "" {
		t.Fatalf("GetInvite = %+v, %v", info, err)
	}
	in2 := registerInput("reg2")
	in2.InviteCode = info.Code
	res2, err := f.account.Register(ctx, in2)
	if err != nil {
		t.Fatalf("Register via personal code: %v", err)
	}
	u2, err := f.users.GetUser(ctx, f.principalOf(t, res2).UserID)
	if err != nil || u2.AgentID == nil || *u2.AgentID != a.ID || u2.InvitedBy == nil || *u2.InvitedBy != reg1 || u2.PlanID != nil {
		t.Fatalf("personal-code user = %+v, %v", u2, err)
	}
	// 平台用户注册照常送赠送套餐
	plain, err := f.account.Register(ctx, registerInput("reg3"))
	if err != nil {
		t.Fatalf("Register plain: %v", err)
	}
	u3, err := f.users.GetUser(ctx, f.principalOf(t, plain).UserID)
	if err != nil || u3.AgentID != nil || u3.PlanID == nil || *u3.PlanID != gift.ID {
		t.Fatalf("plain user = %+v, %v", u3, err)
	}
	// 停用代理：代理码不可用；会话立即失效
	if _, err := f.agents.UpdateAgent(ctx, f.actor, a.ID, AgentUpdateInput{AgentQuotaSpec: AgentQuotaSpec{MaxUsers: 3, TrafficPoolTotal: gib}, IsDisabled: true}, nil); err != nil {
		t.Fatalf("disable agent: %v", err)
	}
	in4 := registerInput("reg4")
	in4.InviteCode = code.Code
	if _, err := f.account.Register(ctx, in4); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("code of disabled agent = %v, want invalid_argument", err)
	}
	// 启用但达上限（已有 2 个，上限调到 2）：代理码与名下用户的个人码都不可用
	if _, err := f.agents.UpdateAgent(ctx, f.actor, a.ID, AgentUpdateInput{AgentQuotaSpec: AgentQuotaSpec{MaxUsers: 2, TrafficPoolTotal: gib}}, nil); err != nil {
		t.Fatalf("enable agent: %v", err)
	}
	if _, err := f.account.Register(ctx, in4); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("agent code at limit = %v, want invalid_argument", err)
	}
	in5 := registerInput("reg5")
	in5.InviteCode = info.Code
	if _, err := f.account.Register(ctx, in5); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("personal code at limit = %v, want invalid_argument", err)
	}
	// 注册关闭时代理码同样不能注册
	if _, err := f.agents.UpdateAgent(ctx, f.actor, a.ID, AgentUpdateInput{AgentQuotaSpec: AgentQuotaSpec{MaxUsers: 10, TrafficPoolTotal: gib}}, nil); err != nil {
		t.Fatalf("raise limit: %v", err)
	}
	if _, err := f.update(t, map[string]any{"register.mode": RegisterClosed}); err != nil {
		t.Fatalf("close registration: %v", err)
	}
	if _, err := f.account.Register(ctx, in4); !errors.Is(err, ErrFeatureDisabled) {
		t.Fatalf("closed registration with agent code = %v, want feature_disabled", err)
	}
}

// 超管侧：流量池不能低于已用、白名单校验、划转与收回、管理员列表不含代理、不能经管理员接口改代理、代理不能成为所有者。
func TestAgentAdminSide(t *testing.T) {
	f := newAgentFixture(t)
	ctx := context.Background()
	plan := f.limitedPlan(t, "p1", gib, 30, TrafficResetNever)
	a, p := f.newAgent(t, "agent-s", 5, 5*gib, plan.ID)
	in := AgentUserCreateInput{Username: "su1", PlanID: plan.ID}
	in.Password = cred
	if _, err := f.agents.CreateUser(ctx, p, in, nil); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := f.agents.UpdateAgent(ctx, f.actor, a.ID, AgentUpdateInput{AgentQuotaSpec: AgentQuotaSpec{MaxUsers: 5, TrafficPoolTotal: gib - 1}}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("pool below used = %v, want conflict", err)
	}
	got, err := f.agents.UpdateAgent(ctx, f.actor, a.ID, AgentUpdateInput{AgentQuotaSpec: AgentQuotaSpec{Remark: "r", MaxUsers: 0, TrafficPoolTotal: gib}}, nil)
	if err != nil || got.TrafficPoolTotal != gib || got.MaxUsers != 0 || len(got.PlanIDs) != 0 || got.UserCount != 1 || got.Remark != "r" {
		t.Fatalf("UpdateAgent = %+v, %v", got, err)
	}
	if _, err := f.agents.GetAgent(ctx, f.actor.AdminID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetAgent(staff id) = %v, want not_found", err)
	}
	// 划转：不受上限限制（上限已是 0），目标须是代理，收回为平台用户
	platform := f.newUser(t, "plat1", 0)
	u, err := f.agents.TransferUser(ctx, f.actor, platform.ID, &a.ID, nil)
	if err != nil || u.AgentID == nil || *u.AgentID != a.ID {
		t.Fatalf("TransferUser = %+v, %v", u, err)
	}
	staffID := f.actor.AdminID
	if _, err := f.agents.TransferUser(ctx, f.actor, platform.ID, &staffID, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("transfer to staff = %v, want invalid_argument", err)
	}
	if u, err = f.agents.TransferUser(ctx, f.actor, platform.ID, nil, nil); err != nil || u.AgentID != nil {
		t.Fatalf("recall = %+v, %v", u, err)
	}
	// 管理员列表不含代理；管理员接口不能改代理（含分配角色）；重置代理密码照常可用
	admins, err := f.rbac.ListAdmins(ctx, PageQuery{})
	if err != nil {
		t.Fatalf("ListAdmins: %v", err)
	}
	for _, ad := range admins.Items {
		if ad.ID == a.ID {
			t.Fatalf("admin list contains agent %+v", ad)
		}
	}
	if _, err := f.rbac.UpdateAdmin(ctx, f.actor, a.ID, AdminUpdateInput{Roles: []string{RoleOperator}}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("UpdateAdmin(agent) = %v, want conflict", err)
	}
	if err := f.rbac.ResetAdminPassword(ctx, f.actor, a.ID, "brand-new-password-1", nil); err != nil {
		t.Fatalf("ResetAdminPassword(agent): %v", err)
	}
	if _, err := f.auth.Authenticate(ctx, f.adminLogin(t, "root").Token); err != nil {
		t.Fatalf("root session: %v", err)
	}
	if _, err := f.rbac.TransferOwner(ctx, "agent-s"); !errors.Is(err, ErrConflict) {
		t.Fatalf("TransferOwner(agent) = %v, want conflict", err)
	}
}

// 代理的写操作按账号限速：每小时合计上限用完即 429，员工会话一律拒绝（Ruling-104）。
func TestAgentWriteRateLimit(t *testing.T) {
	f := newAgentFixture(t)
	const limit = 2
	svc := NewAgentService(f.pool, f.users, nil, AgentServiceConfig{WriteRatePerHour: limit, Now: f.clock.now})
	_, p := f.newAgent(t, "agent-w", 1, gib)
	_, other := f.newAgent(t, "agent-w2", 1, gib)
	for range limit {
		if err := svc.AllowWrite(p); err != nil {
			t.Fatalf("AllowWrite: %v", err)
		}
	}
	expectCode(t, svc.AllowWrite(p), CodeRateLimited)
	if err := svc.AllowWrite(other); err != nil {
		t.Fatalf("other agent limited: %v", err)
	}
	expectCode(t, svc.AllowWrite(f.actor), CodePermissionDenied)
	f.clock.advance(time.Hour + time.Second)
	if err := svc.AllowWrite(p); err != nil {
		t.Fatalf("AllowWrite after window: %v", err)
	}
}
