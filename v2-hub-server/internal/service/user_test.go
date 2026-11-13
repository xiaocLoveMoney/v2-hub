package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"v2-hub-server/internal/manager/panel"
)

type userFixture struct {
	*panelFixture
	users  *UserService
	server *Server
	group  *NodeGroup
	nodes  []Node
}

// newUserFixture：一台内存面板上两个入站（1 vless、2 trojan），一个包含两者直连节点的节点组。
func newUserFixture(t *testing.T) *userFixture {
	t.Helper()
	f := newPanelFixture(t)
	ctx := context.Background()
	fake := f.fleet.get(fakeBase)
	seedInbound(fake, "1", 443, "a")
	fake.PutInbound(panel.Inbound{Ref: "2", Protocol: "trojan", Port: 8443, Remark: "b", Enabled: true, CoreType: panel.CoreXray, Network: "tcp", Config: json.RawMessage(`{"settings":{}}`)})
	srv, err := f.panels.CreateServer(ctx, f.actor, serverInput("hk-1"), nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	nodes, err := f.panels.ListNodes(ctx, NodeFilter{ServerID: srv.ID}, PageQuery{})
	if err != nil || nodes.Total != 2 {
		t.Fatalf("nodes = %+v, %v", nodes, err)
	}
	ids := []int64{nodes.Items[0].ID, nodes.Items[1].ID}
	g, err := f.panels.CreateNodeGroup(ctx, f.actor, NodeGroupInput{Name: "basic", NodeIDs: ids}, nil)
	if err != nil {
		t.Fatalf("CreateNodeGroup: %v", err)
	}
	users := NewUserService(f.pool, testSealer(t), f.panels, UserConfig{Location: time.UTC, Subscription: SubscriptionConfig{InfoNodes: true}}, f.clock.now)
	return &userFixture{panelFixture: f, users: users, server: srv, group: g, nodes: nodes.Items}
}

func (f *userFixture) plan(t *testing.T, name string, q PlanQuota) *Plan {
	t.Helper()
	if q.TrafficReset == "" {
		q.TrafficReset, q.TrafficResetDay = TrafficResetNever, 1
	}
	p, err := f.users.CreatePlan(context.Background(), f.actor, PlanInput{Name: name, PlanQuota: q, NodeGroupIDs: []int64{f.group.ID}}, nil)
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	return p
}

func (f *userFixture) newUser(t *testing.T, name string, planID int64) *User {
	t.Helper()
	u, err := f.users.CreateUser(context.Background(), f.actor, UserCreateInput{Username: name, Password: cred, PlanID: planID}, nil)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return u
}

func (f *userFixture) reconcile(t *testing.T) {
	t.Helper()
	if err := f.users.ReconcileServer(context.Background(), f.server.ID); err != nil {
		t.Fatalf("ReconcileServer: %v", err)
	}
}

func (f *userFixture) panelClient(t *testing.T, email string) panel.Client {
	t.Helper()
	c, ok := f.fleet.get(fakeBase).ClientSnapshot(email)
	if !ok {
		t.Fatalf("client %s not on panel", email)
	}
	return c
}

func (f *userFixture) emailOf(t *testing.T, userID int64) string {
	t.Helper()
	ps, err := f.users.ListUserProvisions(context.Background(), userID)
	if err != nil || len(ps) != 1 {
		t.Fatalf("provisions = %+v, %v", ps, err)
	}
	return ps[0].Email
}

// TestUserProvisioningLifecycle 覆盖 M2 DoD：建用户后面板出现 client，超额自动禁用；管理员改用量、改节点组、停用、删除都能对账到面板。
func TestUserProvisioningLifecycle(t *testing.T) {
	f := newUserFixture(t)
	ctx := context.Background()
	fake := f.fleet.get(fakeBase)
	plan := f.plan(t, "basic", PlanQuota{TrafficLimit: gib, DurationDays: 30, LimitIP: 2})

	u := f.newUser(t, "alice.example", plan.ID)
	if u.Status != UserStatusActive || u.Quota.TrafficLimit != gib || u.Quota.ExpiresAt == nil || u.PlanName != "basic" {
		t.Fatalf("user = %+v", u)
	}
	f.reconcile(t)
	email := f.emailOf(t, u.ID)
	c := f.panelClient(t, email)
	if !c.Enabled || c.LimitIP != 2 || !slices.Equal(c.InboundRefs, []string{"1", "2"}) {
		t.Fatalf("client after create = %+v", c)
	}
	if got, _ := f.users.GetUser(ctx, u.ID); got.Provisions.Synced != 1 {
		t.Fatalf("provisions = %+v", got.Provisions)
	}

	// 用量超额 → limited → 面板禁用（U2）
	fake.AddTraffic(email, 600<<20, 500<<20)
	if err := f.users.CollectServerTraffic(ctx, f.server.ID); err != nil {
		t.Fatalf("CollectServerTraffic: %v", err)
	}
	if n, err := f.users.AdvanceUsers(ctx); err != nil || n != 1 {
		t.Fatalf("AdvanceUsers = %d, %v", n, err)
	}
	got, _ := f.users.GetUser(ctx, u.ID)
	if got.Status != UserStatusLimited || got.Quota.TrafficUsed != 1100<<20 {
		t.Fatalf("after traffic = %+v", got)
	}
	f.reconcile(t)
	if f.panelClient(t, email).Enabled {
		t.Fatal("limited user must be disabled on panel")
	}

	// 管理员直接清零已用 → active → 面板启用（U7）
	in := UserQuotaInput{TrafficLimit: gib, TrafficUsed: 0, ExpiresAt: got.Quota.ExpiresAt, LimitIP: 3, TrafficReset: TrafficResetNever, TrafficResetDay: 1}
	got, err := f.users.UpdateQuota(ctx, f.actor, u.ID, in, nil)
	if err != nil || got.Status != UserStatusActive {
		t.Fatalf("UpdateQuota = %+v, %v", got, err)
	}
	f.reconcile(t)
	if c := f.panelClient(t, email); !c.Enabled || c.LimitIP != 3 {
		t.Fatalf("client after quota = %+v", c)
	}

	// 面板侧计数清零：增量取当前读数
	fake.ResetTraffic(email)
	fake.AddTraffic(email, 100, 0)
	if err := f.users.CollectServerTraffic(ctx, f.server.ID); err != nil {
		t.Fatalf("CollectServerTraffic: %v", err)
	}
	if got, _ = f.users.GetUser(ctx, u.ID); got.Quota.TrafficUsed != 100 {
		t.Fatalf("used after panel reset = %d", got.Quota.TrafficUsed)
	}

	// 节点组去掉 trojan 节点 → 卸载入站 2
	var vlessNode int64
	for _, n := range f.nodes {
		if n.Protocol == "vless" {
			vlessNode = n.ID
		}
	}
	if err := f.panels.UpdateNodeGroup(ctx, f.actor, f.group.ID, NodeGroupInput{Name: "basic", NodeIDs: []int64{vlessNode}}, nil); err != nil {
		t.Fatalf("UpdateNodeGroup: %v", err)
	}
	f.reconcile(t)
	if c := f.panelClient(t, email); !slices.Equal(c.InboundRefs, []string{"1"}) {
		t.Fatalf("refs after group change = %v", c.InboundRefs)
	}

	// 遗留的平台格式 client 被清理，其它 client 不碰
	if err := fake.AddClient(ctx, panel.ClientSpec{Email: "u999-zzzzzz", InboundRefs: []string{"1"}}); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}
	if err := fake.AddClient(ctx, panel.ClientSpec{Email: "manual-user", InboundRefs: []string{"1"}}); err != nil {
		t.Fatalf("seed manual: %v", err)
	}
	f.reconcile(t)
	if _, ok := fake.ClientSnapshot("u999-zzzzzz"); ok {
		t.Fatal("orphan platform client should be deleted")
	}
	if _, ok := fake.ClientSnapshot("manual-user"); !ok {
		t.Fatal("non-platform client must be left alone")
	}

	// 停用（U8）
	if _, err := f.users.UpdateUser(ctx, f.actor, u.ID, UserUpdateInput{IsDisabled: true}, nil); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	f.reconcile(t)
	if f.panelClient(t, email).Enabled {
		t.Fatal("disabled user must be disabled on panel")
	}
	if _, err := f.users.AssignPlan(ctx, f.actor, u.ID, plan.ID, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("renew disabled user err = %v", err)
	}

	// 删除：面板 client 删掉后记录也删掉
	if err := f.users.DeleteUser(ctx, f.actor, u.ID, nil); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, ok := fake.ClientSnapshot(email); ok {
		t.Fatal("deleted user's client must be removed from panel")
	}
	if _, err := f.users.GetUser(ctx, u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted user err = %v", err)
	}
	var n int
	if err := f.pool.QueryRow(ctx, "SELECT COUNT(*) FROM end_user WHERE id = $1", u.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("user row should be purged: %d, %v", n, err)
	}
}

// TestUserProvisionFailureAndDeletePending：面板不可达时删除记为待删除，恢复后对账完成并清掉记录。
func TestUserProvisionFailureAndDeletePending(t *testing.T) {
	f := newUserFixture(t)
	ctx := context.Background()
	fake := f.fleet.get(fakeBase)
	plan := f.plan(t, "basic", PlanQuota{DurationDays: 30})
	u := f.newUser(t, "bob", plan.ID)
	f.reconcile(t)
	email := f.emailOf(t, u.ID)

	fake.FailWith(&panel.Error{Kind: panel.KindUnreachable, Message: "超时"})
	if err := f.users.DeleteUser(ctx, f.actor, u.ID, nil); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	var pending int
	if err := f.pool.QueryRow(ctx, "SELECT COUNT(*) FROM end_user WHERE id = $1 AND deleted_at IS NOT NULL", u.ID).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("user should stay as pending delete: %d, %v", pending, err)
	}
	fake.FailWith(nil)
	f.reconcile(t)
	if _, ok := fake.ClientSnapshot(email); ok {
		t.Fatal("client should be removed once the panel is back")
	}
	if err := f.pool.QueryRow(ctx, "SELECT COUNT(*) FROM end_user WHERE id = $1", u.ID).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("user row should be purged after panel recovers: %d, %v", pending, err)
	}
}

// TestUserExpiryAndRenewal：到期后面板禁用（U3），续费顺延并清零（U6）。
func TestUserExpiryAndRenewal(t *testing.T) {
	f := newUserFixture(t)
	ctx := context.Background()
	plan := f.plan(t, "month", PlanQuota{TrafficLimit: gib, DurationDays: 30})
	u := f.newUser(t, "carol", plan.ID)
	f.reconcile(t)
	email := f.emailOf(t, u.ID)
	firstExpiry := *u.Quota.ExpiresAt

	f.clock.advance(31 * 24 * time.Hour)
	if n, err := f.users.AdvanceUsers(ctx); err != nil || n != 1 {
		t.Fatalf("AdvanceUsers = %d, %v", n, err)
	}
	f.reconcile(t)
	if f.panelClient(t, email).Enabled {
		t.Fatal("expired user must be disabled on panel")
	}
	got, err := f.users.AssignPlan(ctx, f.actor, u.ID, plan.ID, nil)
	if err != nil || got.Status != UserStatusActive || !got.Quota.ExpiresAt.After(firstExpiry) {
		t.Fatalf("AssignPlan = %+v, %v", got, err)
	}
	f.reconcile(t)
	if !f.panelClient(t, email).Enabled {
		t.Fatal("renewed user must be enabled on panel")
	}
}

func TestPlanAndUserValidation(t *testing.T) {
	f := newUserFixture(t)
	ctx := context.Background()
	bad := []PlanQuota{
		{TrafficLimit: -1, TrafficReset: TrafficResetNever, TrafficResetDay: 1},
		{TrafficReset: "yearly", TrafficResetDay: 1},
		{TrafficReset: TrafficResetMonthly, TrafficResetDay: 32},
	}
	for i, q := range bad {
		if _, err := f.users.CreatePlan(ctx, f.actor, PlanInput{Name: "x", PlanQuota: q}, nil); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("bad plan %d err = %v", i, err)
		}
	}
	if _, err := f.users.CreatePlan(ctx, f.actor, PlanInput{Name: "x", PlanQuota: PlanQuota{TrafficReset: TrafficResetNever, TrafficResetDay: 1}, NodeGroupIDs: []int64{99999}}, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("unknown group err = %v", err)
	}
	f.plan(t, "dup", PlanQuota{})
	if _, err := f.users.CreatePlan(ctx, f.actor, PlanInput{Name: "dup", PlanQuota: PlanQuota{TrafficReset: TrafficResetNever, TrafficResetDay: 1}}, nil); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate plan err = %v", err)
	}
	if _, err := f.users.CreateUser(ctx, f.actor, UserCreateInput{Username: "a b", Password: cred}, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("bad username err = %v", err)
	}
	if _, err := f.users.CreateUser(ctx, f.actor, UserCreateInput{Username: "dave", Password: "short"}, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("short password err = %v", err)
	}
	if _, err := f.users.CreateUser(ctx, f.actor, UserCreateInput{Username: "dave", Password: cred, PlanID: 99999}, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("unknown plan err = %v", err)
	}
	if _, _, err := f.users.ListUsers(ctx, UserFilter{Status: "weird"}, 1, 20); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("bad status filter err = %v", err)
	}
	if _, _, err := f.users.ListUsers(ctx, UserFilter{}, 1, 1000); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("huge page err = %v", err)
	}
	// 没有套餐的用户不下发
	u := f.newUser(t, "erin", 0)
	f.reconcile(t)
	if ps, _ := f.users.ListUserProvisions(ctx, u.ID); len(ps) != 0 {
		t.Fatalf("user without plan should not be provisioned: %+v", ps)
	}
	list, total, err := f.users.ListUsers(ctx, UserFilter{Keyword: "eri"}, 1, 20)
	if err != nil || total != 1 || list[0].Username != "erin" {
		t.Fatalf("ListUsers keyword = %+v, %d, %v", list, total, err)
	}
}
