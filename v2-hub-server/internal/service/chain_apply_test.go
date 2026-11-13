package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"v2-hub-server/internal/manager/chain"
	"v2-hub-server/internal/manager/panel/paneltest"
)

const (
	sqlSetServerChainFlag = `UPDATE plan SET allow_server_chain = $2 WHERE id = (SELECT plan_id FROM end_user WHERE id = $1)`
	sqlTagInbounds        = `UPDATE inbound SET tag = 'in-' || port || '-tcp' WHERE server_id = $1`
	sqlCountAudit         = `SELECT COUNT(*) FROM audit_log WHERE action = $1`
)

type chainApplyFixture struct {
	*chainFixture
	applier *ChainApplier
	fake    *paneltest.Fake
}

// newChainApplyFixture 在订阅夹具上建下发器：两个节点在同一台假面板上，入站带上 tag，确认等待全部为 0。
func newChainApplyFixture(t *testing.T) *chainApplyFixture {
	t.Helper()
	cf := newChainFixture(t)
	applier := NewChainApplier(cf.pool, testSealer(t), cf.users, cf.panels,
		ChainApplyConfig{ConfirmTimeout: time.Millisecond, PollInterval: time.Millisecond}, cf.clock.now)
	cf.chain = NewChainService(cf.pool, testSealer(t), applier)
	if _, err := cf.pool.Exec(context.Background(), sqlTagInbounds, cf.server.ID); err != nil {
		t.Fatalf("tag inbounds: %v", err)
	}
	return &chainApplyFixture{chainFixture: cf, applier: applier, fake: cf.fleet.get(fakeBase)}
}

func (f *chainApplyFixture) setServerChain(t *testing.T, on bool) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), sqlSetServerChainFlag, f.user.ID, on); err != nil {
		t.Fatalf("set server chain flag: %v", err)
	}
}

func (f *chainApplyFixture) apply(t *testing.T) {
	t.Helper()
	if err := f.applier.ApplyServer(context.Background(), f.server.ID); err != nil {
		t.Fatalf("ApplyServer: %v", err)
	}
}

func (f *chainApplyFixture) status(t *testing.T) *ChainApply {
	t.Helper()
	s, err := f.applier.Status(context.Background(), f.server.ID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	return s
}

// hubTags 返回假面板模板里平台出站的 tag。
func (f *chainApplyFixture) hubTags(t *testing.T) []string {
	t.Helper()
	tpl, _, _, _ := f.fake.CoreSnapshot()
	sec, err := chain.Extract(tpl)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	out := make([]string, 0, len(sec.Outbounds))
	for _, o := range sec.Outbounds {
		var v struct {
			Tag string `json:"tag"`
		}
		if err := json.Unmarshal(o, &v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v.Tag)
	}
	return out
}

func (f *chainApplyFixture) serverRule(t *testing.T) ServerChainRule {
	t.Helper()
	o, err := f.chain.Overview(context.Background(), f.principal)
	if err != nil || len(o.ServerRules) != 1 {
		t.Fatalf("overview server rules = %+v, %v", o, err)
	}
	return o.ServerRules[0]
}

func TestServerChainRules(t *testing.T) {
	f := newChainApplyFixture(t)
	ctx := context.Background()
	entry, exit := f.nodes[0], f.nodes[1]
	in := ServerChainRuleInput{EntryNodeID: entry.ID, ExitNodeID: exit.ID, Enabled: true}

	o, err := f.chain.Overview(ctx, f.principal)
	if err != nil || o.Flags.ServerChain || !o.Nodes[0].CanBeServerEntry {
		t.Fatalf("overview = %+v, %v", o, err)
	}
	_, err = f.chain.CreateServerRule(ctx, f.principal, in)
	wantErr(t, "create without plan flag", err, ErrFeatureDisabled)

	f.setServerChain(t, true)
	rule, err := f.chain.CreateServerRule(ctx, f.principal, in)
	if err != nil || rule.Status != ChainOK || rule.ApplyStatus != ServerApplyPending {
		t.Fatalf("create = %+v, %v", rule, err)
	}
	_, err = f.chain.CreateServerRule(ctx, f.principal, ServerChainRuleInput{EntryNodeID: entry.ID, ExitNodeID: exit.ID})
	wantErr(t, "same entry twice", err, ErrConflict)
	for what, bad := range map[string]ServerChainRuleInput{
		"same node":     {EntryNodeID: exit.ID, ExitNodeID: exit.ID},
		"unknown exit":  {EntryNodeID: exit.ID, ExitNodeID: exit.ID + 999},
		"unknown entry": {EntryNodeID: exit.ID + 999, ExitNodeID: entry.ID},
		"missing exit":  {EntryNodeID: exit.ID},
	} {
		_, err := f.chain.CreateServerRule(ctx, f.principal, bad)
		wantErr(t, what, err, ErrInvalidArgument)
	}

	// 别人的规则一律 404（R8）
	bob := f.newUser(t, "bob", *f.user.PlanID)
	other := &UserPrincipal{UserID: bob.ID, Username: bob.Username, Status: UserStatusActive}
	_, err = f.chain.UpdateServerRule(ctx, other, rule.ID, false)
	wantErr(t, "update others", err, ErrNotFound)
	wantErr(t, "delete others", f.chain.DeleteServerRule(ctx, other, rule.ID), ErrNotFound)

	// 套餐关掉：阻断（plan），改动 403，删除仍可
	f.setServerChain(t, false)
	if r := f.serverRule(t); r.Status != ChainBlocked || r.BlockedReason != ChainReasonPlan || r.ApplyStatus != ServerApplyNone {
		t.Fatalf("blocked rule = %+v", r)
	}
	_, err = f.chain.UpdateServerRule(ctx, f.principal, rule.ID, false)
	wantErr(t, "update without plan flag", err, ErrFeatureDisabled)
	if err := f.chain.DeleteServerRule(ctx, f.principal, rule.ID); err != nil {
		t.Fatalf("delete without plan flag: %v", err)
	}
}

func TestChainApplyLifecycle(t *testing.T) {
	f := newChainApplyFixture(t)
	ctx := context.Background()
	f.setServerChain(t, true)
	original, testURL, _, _ := f.fake.CoreSnapshot()
	rule, err := f.chain.CreateServerRule(ctx, f.principal, ServerChainRuleInput{EntryNodeID: f.nodes[0].ID, ExitNodeID: f.nodes[1].ID, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	tag := chain.Tag(rule.ID)

	// C1 → C2：写入、重启、确认；出站用本人在落地面板上的凭据，路由按入口入站与本人 email 匹配
	f.apply(t)
	tpl, url, running, restarts := f.fake.CoreSnapshot()
	email := f.emailOf(t, f.user.ID)
	if !running || restarts != 1 || url != testURL || strings.Join(f.hubTags(t), ",") != tag {
		t.Fatalf("after apply: running=%v restarts=%d url=%q tags=%v", running, restarts, url, f.hubTags(t))
	}
	if !strings.Contains(string(tpl), `"user":["`+email+`"]`) || !strings.Contains(string(tpl), `"inboundTag":["in-443-tcp"]`) {
		t.Fatalf("routing rule should match entry inbound and user email: %s", tpl)
	}
	st := f.status(t)
	if st.Status != chainApplyApplied || st.RuleCount != 1 || st.AppliedRuleCount != 1 || !st.Supported || st.AppliedAt == nil {
		t.Fatalf("status after apply = %+v", st)
	}
	if r := f.serverRule(t); r.ApplyStatus != ServerApplyApplied {
		t.Fatalf("rule should be applied: %+v", r)
	}

	// C5：一致时不重启
	f.apply(t)
	if _, _, _, n := f.fake.CoreSnapshot(); n != 1 {
		t.Fatalf("consistent check should not restart, restarts=%d", n)
	}

	// 面板上被人删掉平台条目：下一轮补回（距上次下发不足最小间隔时先记待下发）
	f.fake.PutCoreTemplate(original)
	f.apply(t)
	if st := f.status(t); st.Status != chainApplyPending || len(f.hubTags(t)) != 0 {
		t.Fatalf("within min interval should stay pending: %+v", st)
	}
	f.clock.advance(defaultChainMinInterval + time.Second)
	f.apply(t)
	if strings.Join(f.hubTags(t), ",") != tag {
		t.Fatalf("entries should be restored: %v", f.hubTags(t))
	}

	// 停用：条目被清掉，模板其它部分回到原样
	if _, err := f.chain.UpdateServerRule(ctx, f.principal, rule.ID, false); err != nil {
		t.Fatal(err)
	}
	f.clock.advance(defaultChainMinInterval + time.Second)
	f.apply(t)
	tpl, _, _, _ = f.fake.CoreSnapshot()
	cleared, err := chain.Merge(original, chain.Section{})
	if err != nil || string(tpl) != string(cleared) || f.status(t).AppliedRuleCount != 0 {
		t.Fatalf("disabled rule should be removed: %s", tpl)
	}
	var n int
	if err := f.pool.QueryRow(ctx, sqlCountAudit, ActionChainApply).Scan(&n); err != nil || n != 3 {
		t.Fatalf("apply audit count = %d, %v", n, err)
	}
}

func TestChainApplyRollback(t *testing.T) {
	f := newChainApplyFixture(t)
	ctx := context.Background()
	f.setServerChain(t, true)
	before, _, _, _ := f.fake.CoreSnapshot()
	if _, err := f.chain.CreateServerRule(ctx, f.principal, ServerChainRuleInput{EntryNodeID: f.nodes[0].ID, ExitNodeID: f.nodes[1].ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	f.fake.BreakCoreOn(chain.TagPrefix)

	// C3：重启后内核没起来 → 写回原模板（逐字节一致）并再次重启，记失败与退避
	f.apply(t)
	after, _, running, restarts := f.fake.CoreSnapshot()
	if string(after) != string(before) || !running || restarts != 2 {
		t.Fatalf("rollback: running=%v restarts=%d template changed=%v", running, restarts, string(after) != string(before))
	}
	st := f.status(t)
	if st.Status != chainApplyFailed || st.FailCount != 1 || st.NextAttemptAt == nil || !strings.Contains(st.LastError, "回滚") || st.RetryStopped {
		t.Fatalf("status after failure = %+v", st)
	}
	if r := f.serverRule(t); r.ApplyStatus != ServerApplyFailed {
		t.Fatalf("rule should show failed: %+v", r)
	}

	// 退避期内不重试；到期后再失败，第三次失败后停止自动重试（C4）
	f.apply(t)
	if _, _, _, n := f.fake.CoreSnapshot(); n != 2 {
		t.Fatalf("should not retry during backoff, restarts=%d", n)
	}
	for i, wait := range []time.Duration{time.Minute, 5 * time.Minute} {
		f.clock.advance(wait + time.Second)
		f.apply(t)
		if st := f.status(t); st.FailCount != i+2 {
			t.Fatalf("fail count = %d, want %d", st.FailCount, i+2)
		}
	}
	f.clock.advance(time.Hour)
	f.apply(t)
	st = f.status(t)
	if _, _, _, n := f.fake.CoreSnapshot(); n != 6 || !st.RetryStopped {
		t.Fatalf("should stop after 3 failures: restarts=%d status=%+v", n, st)
	}

	// 管理员重试：清零后再下发，修好后成功
	f.fake.BreakCoreOn("")
	st, err := f.applier.Retry(ctx, f.actor, f.server.ID, nil)
	if err != nil || st.FailCount != 0 || st.RetryStopped {
		t.Fatalf("retry = %+v, %v", st, err)
	}
	f.apply(t)
	if st := f.status(t); st.Status != chainApplyApplied || st.LastError != "" {
		t.Fatalf("after retry = %+v", st)
	}
	page, err := f.applier.ListServerRules(ctx, ServerChainRuleFilter{ServerID: f.server.ID}, PageQuery{})
	if err != nil || page.Total != 1 || page.Items[0].Username != f.user.Username || page.Items[0].ApplyStatus != ServerApplyApplied {
		t.Fatalf("admin list = %+v, %v", page, err)
	}
}

func TestChainApplyPlanRevoked(t *testing.T) {
	f := newChainApplyFixture(t)
	ctx := context.Background()
	f.setServerChain(t, true)
	if _, err := f.chain.CreateServerRule(ctx, f.principal, ServerChainRuleInput{EntryNodeID: f.nodes[0].ID, ExitNodeID: f.nodes[1].ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	f.apply(t)
	if len(f.hubTags(t)) != 1 {
		t.Fatal("rule should be applied first")
	}
	f.setServerChain(t, false)
	f.clock.advance(defaultChainMinInterval + time.Second)
	f.apply(t)
	if len(f.hubTags(t)) != 0 {
		t.Fatalf("revoked plan should clear entries: %v", f.hubTags(t))
	}
	ids, err := f.applier.ListServerIDs(ctx)
	if err != nil || len(ids) != 1 {
		t.Fatalf("server with a (blocked) rule should still be checked: %v, %v", ids, err)
	}
}

func TestChainApplyRejectedOnSave(t *testing.T) {
	f := newChainApplyFixture(t)
	ctx := context.Background()
	f.setServerChain(t, true)
	before, _, _, _ := f.fake.CoreSnapshot()
	if _, err := f.chain.CreateServerRule(ctx, f.principal, ServerChainRuleInput{EntryNodeID: f.nodes[0].ID, ExitNodeID: f.nodes[1].ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	f.fake.RejectTemplateOn(chain.TagPrefix)
	f.apply(t)
	after, _, running, restarts := f.fake.CoreSnapshot()
	st := f.status(t)
	if string(after) != string(before) || !running || restarts != 0 {
		t.Fatalf("rejected save must not touch or restart the core: restarts=%d", restarts)
	}
	if st.Status != chainApplyFailed || st.FailCount != 1 || !strings.Contains(st.LastError, "xray core rejects") {
		t.Fatalf("status after rejection = %+v", st)
	}
}
