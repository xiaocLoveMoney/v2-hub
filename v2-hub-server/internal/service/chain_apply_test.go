package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"v2-hub-server/internal/manager/chain"
	"v2-hub-server/internal/manager/panel"
	"v2-hub-server/internal/manager/panel/paneltest"
)

const (
	sqlSetServerChainFlag = `UPDATE plan SET allow_server_chain = $2, allow_custom_node = TRUE WHERE id = (SELECT plan_id FROM end_user WHERE id = $1)`
	sqlTagInbounds        = `UPDATE inbound SET tag = 'in-' || port || '-tcp' WHERE server_id = $1`
	sqlCountAudit         = `SELECT COUNT(*) FROM audit_log WHERE action = $1`
	sqlExitPin            = `SELECT coalesce(host(resolved_ip), '') FROM chain_exit WHERE id = $1`

	tagFirst  = "in-443-tcp"
	tagSecond = "in-8443-tcp"
	// 用户出口的示例链接：IP 字面量（公网、回环）与解析到内网的域名
	ownPublicLink = "socks5://sample-user:sample-auth-0001@203.0.114.20:1080#public-socks"
	ownLoopLink   = "socks5://127.0.0.1:1080#loop"
	ownInnerLink  = "socks5://inner.example.net:1080#inner"
	ownGoneLink   = "socks5://gone.example.net:1080#gone"
	ownPanelLink  = "socks5://8.8.4.4:1080#panel"
	testWriteMax  = 1000
)

type chainApplyFixture struct {
	*chainFixture
	applier  *ChainApplier
	fake     *paneltest.Fake
	resolver fakeResolver
}

// newChainApplyFixture 在订阅夹具上建下发器：两个节点在同一台假面板上，入站带上 tag，确认等待全部为 0。
// 域名解析走同一张假表（用例可以改它）；写操作频率放得很宽，专门的用例另建服务。
func newChainApplyFixture(t *testing.T) *chainApplyFixture {
	t.Helper()
	cf := newChainFixture(t)
	resolver := chainResolver()
	applier := NewChainApplier(cf.pool, testSealer(t), cf.users, cf.panels,
		ChainApplyConfig{ConfirmTimeout: time.Millisecond, PollInterval: time.Millisecond, Resolver: resolver}, cf.clock.now)
	cf.chain = NewChainService(cf.pool, testSealer(t), applier, ChainOptions{Resolver: resolver, Now: cf.clock.now, ServerWriteLimit: testWriteMax})
	if _, err := cf.pool.Exec(context.Background(), sqlTagInbounds, cf.server.ID); err != nil {
		t.Fatalf("tag inbounds: %v", err)
	}
	f := &chainApplyFixture{chainFixture: cf, applier: applier, fake: cf.fleet.get(fakeBase), resolver: resolver}
	f.setServerChain(t, true)
	return f
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

// applyLater 把时钟拨过最小下发间隔再下发。
func (f *chainApplyFixture) applyLater(t *testing.T) {
	t.Helper()
	f.clock.advance(defaultChainMinInterval + time.Second)
	f.apply(t)
}

func (f *chainApplyFixture) status(t *testing.T) *ChainApply {
	t.Helper()
	s, err := f.applier.Status(context.Background(), f.server.ID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	return s
}

// hubRules 返回假面板模板里的平台路由：“出站 tag → 逗号连接的入站 tag”。
func (f *chainApplyFixture) hubRules(t *testing.T) map[string]string {
	t.Helper()
	tpl, _, _, _ := f.fake.CoreSnapshot()
	sec, err := chain.Extract(tpl)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	out := make(map[string]string, len(sec.Rules))
	for _, raw := range sec.Rules {
		var v struct {
			OutboundTag string   `json:"outboundTag"`
			InboundTag  []string `json:"inboundTag"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		out[v.OutboundTag] = strings.Join(v.InboundTag, ",")
	}
	if len(sec.Outbounds) != len(sec.Rules) {
		t.Fatalf("outbounds and rules should pair up: %d vs %d", len(sec.Outbounds), len(sec.Rules))
	}
	return out
}

func (f *chainApplyFixture) template(t *testing.T) string {
	t.Helper()
	tpl, _, _, _ := f.fake.CoreSnapshot()
	return string(tpl)
}

// serverIn 造一条服务端规则的输入。
func serverIn(exitKind string, exitID int64, entries ...int64) ChainRouteInput {
	return ChainRouteInput{Kind: ChainKindServer, EntryNodeIDs: entries, ExitKind: exitKind, ExitID: exitID, Enabled: true}
}

func (f *chainApplyFixture) serverRoute(t *testing.T, id int64) ChainRoute {
	t.Helper()
	for _, r := range f.overview(t).ServerRoutes {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("server route %d not found", id)
	return ChainRoute{}
}

func (f *chainApplyFixture) pinOf(t *testing.T, exitID int64) string {
	t.Helper()
	var ip string
	if err := f.pool.QueryRow(context.Background(), sqlExitPin, exitID).Scan(&ip); err != nil {
		t.Fatalf("read pin of exit %d: %v", exitID, err)
	}
	return ip
}

func TestServerRoutes(t *testing.T) {
	f := newChainApplyFixture(t)
	ctx := context.Background()
	first, second := f.nodes[0], f.nodes[1]
	in := serverIn(ChainExitNode, second.ID, first.ID)

	f.setServerChain(t, false)
	o := f.overview(t)
	if o.Flags.ServerChain || !o.Nodes[0].CanBeServerEntry {
		t.Fatalf("overview = %+v", o)
	}
	_, err := f.chain.CreateRoute(ctx, f.principal, in)
	wantErr(t, "create without plan flag", err, ErrFeatureDisabled)

	f.setServerChain(t, true)
	route := f.mustRoute(t, in)
	if route.Status != ChainOK || route.Kind != ChainKindServer || route.Entries[0].State != EntryPending {
		t.Fatalf("create = %+v", route)
	}
	// 防环（Ruling-49）：落地节点不能再作起飞，起飞节点不能再作落地；落地也不能是本条的起飞节点
	for what, bad := range map[string]ChainRouteInput{
		"exit of another route as entry": serverIn(ChainExitOwn, 1, second.ID),
		"entry of another route as exit": serverIn(ChainExitNode, first.ID, second.ID),
		"exit is an entry":               serverIn(ChainExitNode, second.ID, second.ID),
		"unknown exit":                   serverIn(ChainExitNode, second.ID+999, first.ID),
		"unknown entry":                  serverIn(ChainExitNode, second.ID, second.ID+999),
	} {
		_, err := f.chain.CreateRoute(ctx, f.principal, bad)
		wantErr(t, what, err, ErrInvalidArgument)
	}
	// 同一起飞节点可以再建一条（靠后的被覆盖），客户端规则不受防环限制
	later := f.mustRoute(t, serverIn(ChainExitNode, second.ID, first.ID))
	if later.Entries[0].State != EntryShadowed || later.Entries[0].CoveredBy != 1 || later.Position != 2 {
		t.Fatalf("later route should be shadowed: %+v", later)
	}
	f.setFlags(t, true, true)
	f.mustRoute(t, clientIn(ChainExitNode, first.ID, second.ID))

	// 别人的规则一律 404（R8）
	bob := f.newUser(t, "bob", *f.user.PlanID)
	other := &UserPrincipal{UserID: bob.ID, Username: bob.Username, Status: UserStatusActive}
	_, err = f.chain.UpdateRoute(ctx, other, route.ID, in)
	wantErr(t, "update others", err, ErrNotFound)
	wantErr(t, "delete others", f.chain.DeleteRoute(ctx, other, route.ID), ErrNotFound)

	// 套餐关掉：阻断（plan），改动 403，删除仍可
	f.setServerChain(t, false)
	if r := f.serverRoute(t, route.ID); r.Status != ChainBlocked || r.BlockedReason != ChainReasonPlan || r.Entries[0].State != EntryInactive {
		t.Fatalf("blocked route = %+v", r)
	}
	_, err = f.chain.UpdateRoute(ctx, f.principal, route.ID, in)
	wantErr(t, "update without plan flag", err, ErrFeatureDisabled)
	if err := f.chain.DeleteRoute(ctx, f.principal, route.ID); err != nil {
		t.Fatalf("delete without plan flag: %v", err)
	}
}

func TestChainApplyLifecycle(t *testing.T) {
	f := newChainApplyFixture(t)
	ctx := context.Background()
	original, testURL, _, _ := f.fake.CoreSnapshot()
	exit := f.platformExit(t, chainPlatformName, true, true)
	// 一条规则带两个起飞节点（同一台服务器上的两个入站）：一个出站、一条带两个入站的路由
	route := f.mustRoute(t, serverIn(ChainExitPlatform, exit.ID, f.nodes[0].ID, f.nodes[1].ID))
	tag := chain.Tag(route.ID)

	// C1 → C2：写入、重启、确认；路由按起飞入站与本人 email 匹配，出站是平台出口的参数
	f.apply(t)
	tpl, url, running, restarts := f.fake.CoreSnapshot()
	email := f.emailOf(t, f.user.ID)
	rules := f.hubRules(t)
	if !running || restarts != 1 || url != testURL || len(rules) != 1 || rules[tag] != tagFirst+","+tagSecond {
		t.Fatalf("after apply: running=%v restarts=%d url=%q rules=%v", running, restarts, url, rules)
	}
	if !strings.Contains(string(tpl), `"user":["`+email+`"]`) || !strings.Contains(string(tpl), "exit.example.net") {
		t.Fatalf("routing rule should match the user's email and use the platform exit: %s", tpl)
	}
	st := f.status(t)
	if st.Status != chainApplyApplied || st.RuleCount != 1 || st.AppliedRuleCount != 1 || !st.Supported || st.AppliedAt == nil {
		t.Fatalf("status after apply = %+v", st)
	}
	if r := f.serverRoute(t, route.ID); r.Entries[0].State != EntryApplied || r.Entries[1].State != EntryApplied {
		t.Fatalf("entries should be applied: %+v", r.Entries)
	}

	// C5：一致时不重启
	f.apply(t)
	if _, _, _, n := f.fake.CoreSnapshot(); n != 1 {
		t.Fatalf("consistent check should not restart, restarts=%d", n)
	}

	// 只改名称：条目不变、不重启，但状态要回到“生效”（不能一直显示下发中）
	renamed := serverIn(ChainExitPlatform, exit.ID, f.nodes[0].ID, f.nodes[1].ID)
	renamed.Name = "东京线"
	if _, err := f.chain.UpdateRoute(ctx, f.principal, route.ID, renamed); err != nil {
		t.Fatal(err)
	}
	if r := f.serverRoute(t, route.ID); r.Entries[0].State != EntryPending {
		t.Fatalf("a just-changed route should show pending: %+v", r.Entries)
	}
	f.apply(t)
	if _, _, _, n := f.fake.CoreSnapshot(); n != 1 || f.serverRoute(t, route.ID).Entries[0].State != EntryApplied {
		t.Fatalf("rename should not restart and should settle as applied, restarts=%d", n)
	}

	// 面板上被人删掉平台条目：下一轮补回（距上次下发不足最小间隔时先记待下发）
	f.fake.PutCoreTemplate(original)
	f.apply(t)
	if st := f.status(t); st.Status != chainApplyPending || len(f.hubRules(t)) != 0 {
		t.Fatalf("within min interval should stay pending: %+v", st)
	}
	f.applyLater(t)
	if rules := f.hubRules(t); rules[tag] == "" {
		t.Fatalf("entries should be restored: %v", rules)
	}

	// 去掉一个起飞节点：路由只剩一个入站
	if _, err := f.chain.UpdateRoute(ctx, f.principal, route.ID, serverIn(ChainExitPlatform, exit.ID, f.nodes[1].ID)); err != nil {
		t.Fatal(err)
	}
	f.applyLater(t)
	if rules := f.hubRules(t); rules[tag] != tagSecond {
		t.Fatalf("route should keep only the second inbound: %v", rules)
	}

	// 停用：条目被清掉，模板其它部分回到原样
	off := serverIn(ChainExitPlatform, exit.ID, f.nodes[1].ID)
	off.Enabled = false
	if _, err := f.chain.UpdateRoute(ctx, f.principal, route.ID, off); err != nil {
		t.Fatal(err)
	}
	f.applyLater(t)
	cleared, err := chain.Merge(original, chain.Section{})
	if err != nil || f.template(t) != string(cleared) || f.status(t).AppliedRuleCount != 0 {
		t.Fatalf("disabled route should be removed: %s", f.template(t))
	}
	var n int
	if err := f.pool.QueryRow(ctx, sqlCountAudit, ActionChainApply).Scan(&n); err != nil || n != 4 {
		t.Fatalf("apply audit count = %d, %v", n, err)
	}
}

func TestChainApplyPriority(t *testing.T) {
	f := newChainApplyFixture(t)
	ctx := context.Background()
	exit := f.platformExit(t, chainPlatformName, true, true)
	own := f.mustExit(t, ownPublicLink, "")
	top := f.mustRoute(t, serverIn(ChainExitPlatform, exit.ID, f.nodes[0].ID))
	wide := f.mustRoute(t, serverIn(ChainExitOwn, own.ID, f.nodes[0].ID, f.nodes[1].ID))
	topTag, wideTag := chain.Tag(top.ID), chain.Tag(wide.ID)

	// 第 1 条占了第一个入站；第 2 条只在第二个入站上生效
	f.apply(t)
	if rules := f.hubRules(t); len(rules) != 2 || rules[topTag] != tagFirst || rules[wideTag] != tagSecond {
		t.Fatalf("rules by priority = %v", rules)
	}
	if r := f.serverRoute(t, wide.ID); r.Entries[0].State != EntryShadowed || r.Entries[1].State != EntryApplied {
		t.Fatalf("wide route entries = %+v", r.Entries)
	}

	// 调顺序：第 2 条提到最前后两个入站都归它，原第 1 条被完全覆盖、不再下发
	if _, err := f.chain.ReorderRoutes(ctx, f.principal, ChainKindServer, []int64{wide.ID, top.ID}); err != nil {
		t.Fatal(err)
	}
	f.applyLater(t)
	if rules := f.hubRules(t); len(rules) != 1 || rules[wideTag] != tagFirst+","+tagSecond {
		t.Fatalf("rules after reorder = %v", rules)
	}
	if r := f.serverRoute(t, top.ID); r.Entries[0].State != EntryShadowed || r.Entries[0].CoveredBy != 1 || r.Position != 2 {
		t.Fatalf("top route after reorder = %+v", r)
	}

	// 靠前的规则停用后，自动落到后面的规则
	off := serverIn(ChainExitOwn, own.ID, f.nodes[0].ID, f.nodes[1].ID)
	off.Enabled = false
	if _, err := f.chain.UpdateRoute(ctx, f.principal, wide.ID, off); err != nil {
		t.Fatal(err)
	}
	f.applyLater(t)
	if rules := f.hubRules(t); len(rules) != 1 || rules[topTag] != tagFirst {
		t.Fatalf("rules after disabling the leading route = %v", rules)
	}
}

func TestServerChainOwnExitGuard(t *testing.T) {
	f := newChainApplyFixture(t)
	ctx := context.Background()
	first := f.nodes[0]

	// 用于服务端链式：回环地址、解析到内网的域名、解析不了的域名、指向平台自己服务器的地址一律拒绝（Ruling-48、Ruling-49）
	for what, link := range map[string]string{"loopback": ownLoopLink, "inner domain": ownInnerLink, "unresolvable": ownGoneLink, "platform server": ownPanelLink} {
		exit := f.mustExit(t, link, what)
		_, err := f.chain.CreateRoute(ctx, f.principal, serverIn(ChainExitOwn, exit.ID, first.ID))
		wantErr(t, what+" for server chain", err, ErrInvalidArgument)
		// 客户端链式不限制：客户端自己去连，平台服务器不参与
		f.setFlags(t, true, true)
		f.mustRoute(t, clientIn(ChainExitOwn, exit.ID, first.ID))
	}
	f.setServerChain(t, true)

	// 域名出口：解析后固定成 IP 下发，域名不进出站地址
	home := f.mustExit(t, chainSocksLink, chainExitName)
	route := f.mustRoute(t, serverIn(ChainExitOwn, home.ID, first.ID))
	if f.pinOf(t, home.ID) != guardPublicV4 {
		t.Fatalf("pin = %q, want %s", f.pinOf(t, home.ID), guardPublicV4)
	}
	f.apply(t)
	if tpl := f.template(t); !strings.Contains(tpl, `"address":"`+guardPublicV4+`"`) || strings.Contains(tpl, "home.example.net") {
		t.Fatalf("outbound should use the pinned IP only: %s", tpl)
	}

	// 被服务端规则引用的出口换链接：新地址同样要过限制，不通过时链接保持不变
	_, err := f.chain.UpdateExit(ctx, f.principal, home.ID, chainExitName, ownLoopLink)
	wantErr(t, "relink to loopback while used by a server route", err, ErrInvalidArgument)
	if link, err := f.chain.ExitLink(ctx, f.principal, home.ID); err != nil || link != chainSocksLink {
		t.Fatalf("link must stay unchanged after a rejected relink, err = %v", err)
	}
	if _, err := f.chain.UpdateExit(ctx, f.principal, home.ID, chainExitName, ownPublicLink); err != nil {
		t.Fatalf("relink to a public address: %v", err)
	}
	if f.pinOf(t, home.ID) != "203.0.114.20" {
		t.Fatalf("pin after relink = %q", f.pinOf(t, home.ID))
	}
	if _, err := f.chain.UpdateExit(ctx, f.principal, home.ID, chainExitName, chainSocksLink); err != nil {
		t.Fatalf("relink back to the domain: %v", err)
	}

	// 定期复查：解析结果换了公网地址就跟着换；出现内网地址就作废，规则阻断并从面板撤下
	f.resolver["home.example.net"] = []string{"1.0.0.1"}
	f.applyLater(t)
	if f.pinOf(t, home.ID) != guardPublicV4 {
		t.Fatalf("pin should not be rechecked before it is stale: %q", f.pinOf(t, home.ID))
	}
	f.clock.advance(chainRepinAfter)
	f.apply(t)
	if f.pinOf(t, home.ID) != "1.0.0.1" || !strings.Contains(f.template(t), `"address":"1.0.0.1"`) {
		t.Fatalf("pin should follow the new public address: %q", f.pinOf(t, home.ID))
	}
	f.resolver["home.example.net"] = []string{"1.0.0.1", "192.168.1.1"}
	f.clock.advance(chainRepinAfter + defaultChainMinInterval)
	f.apply(t)
	if r := f.serverRoute(t, route.ID); r.BlockedReason != ChainReasonUnsafe || f.pinOf(t, home.ID) != "" || len(f.hubRules(t)) != 0 {
		t.Fatalf("a rebound-to-private exit must be blocked and removed: %+v, rules=%v", r, f.hubRules(t))
	}
	// 暂时解析不了：保持原样，不作废
	if _, err := f.chain.UpdateExit(ctx, f.principal, home.ID, chainExitName, chainVLESSLink); err != nil {
		t.Fatalf("relink: %v", err)
	}
	delete(f.resolver, "exit.example.net")
	f.clock.advance(chainRepinAfter + defaultChainMinInterval)
	f.apply(t)
	if f.pinOf(t, home.ID) != "1.0.0.1" || f.serverRoute(t, route.ID).Status != ChainOK {
		t.Fatalf("a temporary resolve failure must keep the pin: %q", f.pinOf(t, home.ID))
	}
}

func TestChainApplyQuarantine(t *testing.T) {
	f := newChainApplyFixture(t)
	ctx := context.Background()
	platform := f.platformExit(t, chainPlatformName, true, true)
	own := f.mustExit(t, ownPublicLink, "")
	good := f.mustRoute(t, serverIn(ChainExitPlatform, platform.ID, f.nodes[0].ID))
	f.apply(t)
	if len(f.hubRules(t)) != 1 {
		t.Fatal("the platform route should be applied first")
	}

	// 新加一条落地为用户出口的规则，它的配置让内核起不来：整台服务器回滚，但只隔离这条新规则（Ruling-49）
	bad := f.mustRoute(t, serverIn(ChainExitOwn, own.ID, f.nodes[1].ID))
	f.fake.BreakCoreOn("203.0.114.20")
	f.applyLater(t)
	st := f.status(t)
	if st.Status != chainApplyFailed || len(f.hubRules(t)) != 1 {
		t.Fatalf("failed apply should roll back to the previous entries: %+v, rules=%v", st, f.hubRules(t))
	}
	if r := f.serverRoute(t, bad.ID); r.BlockedReason != ChainReasonQuarantined || r.Entries[0].State != EntryInactive {
		t.Fatalf("the new own-exit route should be quarantined: %+v", r)
	}
	if r := f.serverRoute(t, good.ID); r.Status != ChainOK || r.Entries[0].State != EntryApplied {
		t.Fatalf("the platform route must stay applied: %+v", r)
	}
	// 隔离后期望回到只有平台规则：与面板现状一致，不再重启，服务器状态恢复
	_, _, _, restartsBefore := f.fake.CoreSnapshot()
	f.apply(t)
	if _, _, _, n := f.fake.CoreSnapshot(); n != restartsBefore || f.status(t).Status != chainApplyApplied {
		t.Fatalf("after quarantine the server should settle without restarting: %+v", f.status(t))
	}

	// 用户改了出口（内容指纹变了）：隔离自动解除，重新下发
	f.fake.BreakCoreOn("")
	if _, err := f.chain.UpdateExit(ctx, f.principal, own.ID, "public-socks", "socks5://203.0.114.21:1080"); err != nil {
		t.Fatalf("relink: %v", err)
	}
	if r := f.serverRoute(t, bad.ID); r.Status != ChainOK {
		t.Fatalf("quarantine should be released after the exit changed: %+v", r)
	}
	f.applyLater(t)
	if rules := f.hubRules(t); len(rules) != 2 || rules[chain.Tag(bad.ID)] != tagSecond {
		t.Fatalf("released route should be applied: %v", rules)
	}
}

func TestChainApplyRollback(t *testing.T) {
	f := newChainApplyFixture(t)
	ctx := context.Background()
	before, _, _, _ := f.fake.CoreSnapshot()
	route := f.mustRoute(t, serverIn(ChainExitNode, f.nodes[1].ID, f.nodes[0].ID))
	f.fake.BreakCoreOn(chain.TagPrefix)

	// C3：重启后内核没起来 → 写回原模板（逐字节一致）并再次重启，记失败与退避；落地是平台节点的规则不隔离
	f.apply(t)
	after, _, running, restarts := f.fake.CoreSnapshot()
	if string(after) != string(before) || !running || restarts != 2 {
		t.Fatalf("rollback: running=%v restarts=%d template changed=%v", running, restarts, string(after) != string(before))
	}
	st := f.status(t)
	if st.Status != chainApplyFailed || st.FailCount != 1 || st.NextAttemptAt == nil || !strings.Contains(st.LastError, "回滚") || st.RetryStopped {
		t.Fatalf("status after failure = %+v", st)
	}
	if r := f.serverRoute(t, route.ID); r.Status != ChainOK || r.Entries[0].State != EntryFailed {
		t.Fatalf("route should show failed: %+v", r)
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

	// 管理端全站规则：状态与用户看到的一致，可按类型、用户、服务器筛选
	page, err := f.chain.ListRoutes(ctx, ChainRouteFilter{ServerID: f.server.ID, Kind: ChainKindServer}, PageQuery{})
	isListed := err == nil && page.Total == 1 && page.Items[0].Username == f.user.Username && page.Items[0].Entries[0].State == EntryApplied &&
		page.Items[0].Entries[0].ServerName == f.server.Name
	if !isListed {
		t.Fatalf("admin list = %+v, %v", page, err)
	}
	if page, err := f.chain.ListRoutes(ctx, ChainRouteFilter{Kind: ChainKindClient}, PageQuery{}); err != nil || page.Total != 0 {
		t.Fatalf("admin list by kind = %+v, %v", page, err)
	}
	if page, err := f.chain.ListRoutes(ctx, ChainRouteFilter{UserID: f.user.ID + 999}, PageQuery{}); err != nil || page.Total != 0 {
		t.Fatalf("admin list by user = %+v, %v", page, err)
	}
	_, err = f.chain.ListRoutes(ctx, ChainRouteFilter{Kind: "relay"}, PageQuery{})
	wantErr(t, "admin list with unknown kind", err, ErrInvalidArgument)
}

func TestChainApplyPlanRevoked(t *testing.T) {
	f := newChainApplyFixture(t)
	ctx := context.Background()
	f.mustRoute(t, serverIn(ChainExitNode, f.nodes[1].ID, f.nodes[0].ID))
	f.apply(t)
	if len(f.hubRules(t)) != 1 {
		t.Fatal("route should be applied first")
	}
	f.setServerChain(t, false)
	f.applyLater(t)
	if len(f.hubRules(t)) != 0 {
		t.Fatalf("revoked plan should clear entries: %v", f.hubRules(t))
	}
	ids, err := f.applier.ListServerIDs(ctx)
	if err != nil || len(ids) != 1 {
		t.Fatalf("server with a (blocked) route should still be checked: %v, %v", ids, err)
	}
}

const sqlSetBlockRule = `INSERT INTO setting (key, value) VALUES ('routing.blockRule', $1)
ON CONFLICT (key) DO UPDATE SET value = excluded.value`

// 平台拦截规则（Ruling-55）：默认开，没有链式规则的服务器也下发；排在链式条目之前；关掉后撤掉，链式条目不受影响。
func TestChainApplyBlockRule(t *testing.T) {
	f := newChainApplyFixture(t)
	ctx := context.Background()
	f.applier.cfg.Settings = NewSettingsService(f.pool, SubscriptionConfig{}, f.clock.now)

	ids, err := f.applier.ListServerIDs(ctx)
	if err != nil || len(ids) != 1 || ids[0] != f.server.ID {
		t.Fatalf("with the block rule on every enabled server should be checked: %v, %v", ids, err)
	}
	f.apply(t)
	if _, ok := f.hubRules(t)[chain.BlockTag]; !ok || len(f.hubRules(t)) != 1 {
		t.Fatalf("block rule should be applied without any chain route: %v", f.hubRules(t))
	}
	if st := f.status(t); st.Status != chainApplyApplied || st.RuleCount != 0 {
		t.Fatalf("status with only the block rule = %+v", st)
	}

	route := f.mustRoute(t, serverIn(ChainExitNode, f.nodes[1].ID, f.nodes[0].ID))
	tag := chain.Tag(route.ID)
	f.applyLater(t)
	tpl := f.template(t)
	if rules := f.hubRules(t); len(rules) != 2 || rules[tag] == "" {
		t.Fatalf("chain entry should be added next to the block rule: %v", rules)
	}
	if strings.Index(tpl, `"ruleTag":"`+chain.BlockTag+`"`) > strings.Index(tpl, `"ruleTag":"`+tag+`"`) {
		t.Fatalf("block rule should come before chain rules: %s", tpl)
	}

	// 关掉设置（拨过设置缓存与最小间隔）：只撤掉拦截条目
	if _, err := f.pool.Exec(ctx, sqlSetBlockRule, []byte("false")); err != nil {
		t.Fatal(err)
	}
	f.applyLater(t)
	if rules := f.hubRules(t); len(rules) != 1 || rules[tag] == "" {
		t.Fatalf("turning off should remove only the block rule: %v", rules)
	}
	if strings.Contains(f.template(t), chain.BlockTag) {
		t.Fatal("block outbound should be removed too")
	}
}

func TestChainApplyRejectedOnSave(t *testing.T) {
	f := newChainApplyFixture(t)
	before, _, _, _ := f.fake.CoreSnapshot()
	f.mustRoute(t, serverIn(ChainExitNode, f.nodes[1].ID, f.nodes[0].ID))
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

// 面板保存模板即生效时（3x-ui ≥ 3.3.1，Ruling-93）：下发、改动、撤掉都不重启内核。
func TestChainApplyLiveNoRestart(t *testing.T) {
	f := newChainApplyFixture(t)
	ctx := context.Background()
	f.fake.SetCoreLiveApply(true)
	exit := f.platformExit(t, chainPlatformName, true, true)
	route := f.mustRoute(t, serverIn(ChainExitPlatform, exit.ID, f.nodes[0].ID, f.nodes[1].ID))
	tag := chain.Tag(route.ID)

	f.apply(t)
	if _, _, running, restarts := f.fake.CoreSnapshot(); !running || restarts != 0 || f.hubRules(t)[tag] != tagFirst+","+tagSecond {
		t.Fatalf("live apply must not restart: running=%v restarts=%d rules=%v", running, restarts, f.hubRules(t))
	}
	if st := f.status(t); st.Status != chainApplyApplied || st.AppliedRuleCount != 1 {
		t.Fatalf("status after live apply = %+v", st)
	}

	if _, err := f.chain.UpdateRoute(ctx, f.principal, route.ID, serverIn(ChainExitPlatform, exit.ID, f.nodes[1].ID)); err != nil {
		t.Fatal(err)
	}
	f.applyLater(t)
	if err := f.chain.DeleteRoute(ctx, f.principal, route.ID); err != nil {
		t.Fatal(err)
	}
	f.applyLater(t)
	if _, _, running, restarts := f.fake.CoreSnapshot(); !running || restarts != 0 || len(f.hubRules(t)) != 0 {
		t.Fatalf("changing and removing a route must not restart: running=%v restarts=%d rules=%v", running, restarts, f.hubRules(t))
	}
}

// 保存即生效的面板上新模板让内核起不来：写回备份即恢复，同样不重启。
func TestChainApplyLiveRollback(t *testing.T) {
	f := newChainApplyFixture(t)
	f.fake.SetCoreLiveApply(true)
	before, _, _, _ := f.fake.CoreSnapshot()
	route := f.mustRoute(t, serverIn(ChainExitNode, f.nodes[1].ID, f.nodes[0].ID))
	f.fake.BreakCoreOn(chain.TagPrefix)

	f.apply(t)
	after, _, running, restarts := f.fake.CoreSnapshot()
	if string(after) != string(before) || !running || restarts != 0 {
		t.Fatalf("live rollback: running=%v restarts=%d template changed=%v", running, restarts, string(after) != string(before))
	}
	st := f.status(t)
	if st.Status != chainApplyFailed || st.FailCount != 1 || !strings.Contains(st.LastError, "回滚") {
		t.Fatalf("status after live failure = %+v", st)
	}
	if r := f.serverRoute(t, route.ID); r.Entries[0].State != EntryFailed {
		t.Fatalf("route should show failed: %+v", r)
	}
}

// 面板先保存、再应用，应用出错时保存接口报错但模板已经变了：读回核对后照样回滚（不能当成“被拒绝、模板没变”）。
func TestChainApplySavedButApplyFailed(t *testing.T) {
	for name, isLive := range map[string]bool{"live": true, "restart": false} {
		t.Run(name, func(t *testing.T) {
			f := newChainApplyFixture(t)
			f.fake.SetCoreLiveApply(isLive)
			before, _, _, _ := f.fake.CoreSnapshot()
			f.mustRoute(t, serverIn(ChainExitNode, f.nodes[1].ID, f.nodes[0].ID))
			f.fake.FailApplyOn(chain.TagPrefix)

			f.apply(t)
			after, _, running, restarts := f.fake.CoreSnapshot()
			wantRestarts := 1
			if isLive {
				wantRestarts = 0
			}
			if string(after) != string(before) || !running || restarts != wantRestarts {
				t.Fatalf("saved-but-failed must roll back: running=%v restarts=%d template changed=%v", running, restarts, string(after) != string(before))
			}
			if st := f.status(t); st.Status != chainApplyFailed || st.FailCount != 1 || !strings.Contains(st.LastError, "xray apply failed") {
				t.Fatalf("status = %+v", st)
			}
		})
	}
}

// 问不到面板会不会自己应用改动时不下发也不重启，记待下发、下一轮再来（重启会断开全部连接，不能当兜底）。
func TestChainApplyLiveUnknownStaysPending(t *testing.T) {
	f := newChainApplyFixture(t)
	f.mustRoute(t, serverIn(ChainExitNode, f.nodes[1].ID, f.nodes[0].ID))
	f.fake.FailOn("AppliesCoreTemplateLive", &panel.Error{Kind: panel.KindUnreachable, Message: "面板不可达"})

	f.apply(t)
	if _, _, _, restarts := f.fake.CoreSnapshot(); restarts != 0 || len(f.hubRules(t)) != 0 {
		t.Fatalf("unknown live-apply support must not write or restart: restarts=%d rules=%v", restarts, f.hubRules(t))
	}
	if st := f.status(t); st.Status != chainApplyPending || st.FailCount != 0 {
		t.Fatalf("status = %+v", st)
	}
	f.fake.FailOn("AppliesCoreTemplateLive", nil)
	f.apply(t)
	if st := f.status(t); st.Status != chainApplyApplied {
		t.Fatalf("status after recovery = %+v", st)
	}
}

func TestServerChainWriteLimit(t *testing.T) {
	f := newChainApplyFixture(t)
	ctx := context.Background()
	const limit = 3
	f.chain = NewChainService(f.pool, testSealer(t), f.applier, ChainOptions{
		Resolver: f.resolver, Now: f.clock.now, ServerWriteLimit: limit, ServerWriteWindow: time.Hour,
	})
	in := serverIn(ChainExitNode, f.nodes[1].ID, f.nodes[0].ID)

	// 新建、修改、调顺序各算一次；第 4 次被拒（Ruling-49），删除同样计入
	route := f.mustRoute(t, in)
	if _, err := f.chain.UpdateRoute(ctx, f.principal, route.ID, in); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if _, err := f.chain.ReorderRoutes(ctx, f.principal, ChainKindServer, []int64{route.ID}); err != nil {
		t.Fatalf("third write: %v", err)
	}
	_, err := f.chain.UpdateRoute(ctx, f.principal, route.ID, in)
	wantErr(t, "write over the limit", err, ErrRateLimited)
	wantErr(t, "delete over the limit", f.chain.DeleteRoute(ctx, f.principal, route.ID), ErrRateLimited)
	// 客户端规则与别的用户不受影响
	f.setFlags(t, true, true)
	f.mustRoute(t, clientIn(ChainExitNode, f.nodes[1].ID, f.nodes[0].ID))
	bob := f.newUser(t, "bob", *f.user.PlanID)
	if _, err := f.chain.CreateRoute(ctx, &UserPrincipal{UserID: bob.ID, Username: bob.Username, Status: UserStatusActive}, in); err != nil {
		t.Fatalf("another user's write: %v", err)
	}
	// 时间窗过去后恢复
	f.clock.advance(time.Hour + time.Second)
	if err := f.chain.DeleteRoute(ctx, f.principal, route.ID); err != nil {
		t.Fatalf("write after the window: %v", err)
	}
}

func TestAdminChainExits(t *testing.T) {
	f := newChainApplyFixture(t)
	ctx := context.Background()
	base := PlatformExitInput{Name: chainPlatformName, Link: chainVLESSLink, Enabled: true, AllowServer: true, AllowClient: true, NodeGroupIDs: []int64{f.group.ID}}
	with := func(mutate func(in *PlatformExitInput)) PlatformExitInput {
		in := base
		mutate(&in)
		return in
	}
	for what, in := range map[string]PlatformExitInput{
		"empty name":         with(func(in *PlatformExitInput) { in.Name = " " }),
		"no link":            with(func(in *PlatformExitInput) { in.Link = "" }),
		"bad link":           with(func(in *PlatformExitInput) { in.Link = "not a link" }),
		"no use":             with(func(in *PlatformExitInput) { in.AllowServer, in.AllowClient = false, false }),
		"unknown node group": with(func(in *PlatformExitInput) { in.NodeGroupIDs = []int64{f.group.ID + 999} }),
		"remark too long":    with(func(in *PlatformExitInput) { in.Remark = strings.Repeat("备", maxExitRemarkLen+1) }),
	} {
		_, err := f.chain.CreatePlatformExit(ctx, f.actor, in, nil)
		wantErr(t, "create platform exit: "+what, err, ErrInvalidArgument)
	}
	platform, err := f.chain.CreatePlatformExit(ctx, f.actor, base, nil)
	isCreated := err == nil && platform.UserID == 0 && platform.Address == "exit.example.net" && len(platform.NodeGroups) == 1 &&
		platform.NodeGroups[0].ID == f.group.ID
	if !isCreated {
		t.Fatalf("create platform exit = %+v, %v", platform, err)
	}
	_, err = f.chain.CreatePlatformExit(ctx, f.actor, base, nil)
	wantErr(t, "duplicate platform exit name", err, ErrConflict)
	if link, err := f.chain.PlatformExitLink(ctx, f.actor, platform.ID, nil); err != nil || link != chainVLESSLink {
		t.Fatalf("platform exit link mismatch, err = %v", err)
	}

	// 用户出口：管理员能看到归属与地址，能停用、启用、删除，但不能改、不能看链接（Ruling-50）
	own := f.mustExit(t, ownPublicLink, "")
	route := f.mustRoute(t, serverIn(ChainExitOwn, own.ID, f.nodes[0].ID))
	f.apply(t)
	if len(f.hubRules(t)) != 1 {
		t.Fatal("the own-exit route should be applied first")
	}
	_, err = f.chain.UpdatePlatformExit(ctx, f.actor, own.ID, base, nil)
	wantErr(t, "admin edits a user exit", err, ErrInvalidArgument)
	_, err = f.chain.PlatformExitLink(ctx, f.actor, own.ID, nil)
	wantErr(t, "admin reads a user exit's link", err, ErrNotFound)

	all, err := f.chain.ListExits(ctx, ChainExitFilter{}, PageQuery{})
	if err != nil || all.Total != 2 || all.Items[0].ID != own.ID || all.Items[0].Username != f.user.Username || all.Items[0].RouteCount != 1 {
		t.Fatalf("list all exits = %+v, %v", all, err)
	}
	for what, c := range map[string]struct {
		filter ChainExitFilter
		wantID int64
	}{
		"platform only": {ChainExitFilter{Owner: ExitOwnerPlatform}, platform.ID},
		"user only":     {ChainExitFilter{Owner: ExitOwnerUser}, own.ID},
		"by user":       {ChainExitFilter{UserID: f.user.ID}, own.ID},
		"by address":    {ChainExitFilter{Keyword: "EXIT.example"}, platform.ID},
		"by name":       {ChainExitFilter{Keyword: "public-"}, own.ID},
	} {
		page, err := f.chain.ListExits(ctx, c.filter, PageQuery{})
		if err != nil || page.Total != 1 || page.Items[0].ID != c.wantID {
			t.Fatalf("list exits %s = %+v, %v", what, page, err)
		}
	}
	// 关键字里的通配符按字面匹配，不当通配符解释
	if page, err := f.chain.ListExits(ctx, ChainExitFilter{Keyword: "%"}, PageQuery{}); err != nil || page.Total != 0 {
		t.Fatalf("wildcard keyword should match nothing: %+v, %v", page, err)
	}
	_, err = f.chain.ListExits(ctx, ChainExitFilter{Owner: "nobody"}, PageQuery{})
	wantErr(t, "list exits with unknown owner", err, ErrInvalidArgument)

	// 停用用户出口：规则阻断并从面板撤下；用户看得到已被停用，新规则不能再选它
	if e, err := f.chain.SetExitEnabled(ctx, f.actor, own.ID, false, nil); err != nil || e.Enabled {
		t.Fatalf("disable user exit = %+v, %v", e, err)
	}
	if r := f.serverRoute(t, route.ID); r.BlockedReason != ChainReasonExit {
		t.Fatalf("route with a disabled exit = %+v", r)
	}
	if exits := f.overview(t).Exits; len(exits) != 1 || exits[0].Enabled {
		t.Fatalf("user should see the exit as disabled: %+v", exits)
	}
	_, err = f.chain.CreateRoute(ctx, f.principal, serverIn(ChainExitOwn, own.ID, f.nodes[1].ID))
	wantErr(t, "use a disabled exit", err, ErrInvalidArgument)
	f.applyLater(t)
	if len(f.hubRules(t)) != 0 {
		t.Fatalf("disabled exit should clear the entries: %v", f.hubRules(t))
	}
	// 删除用户出口：引用它的规则一并删除
	if err := f.chain.DeleteExitAsAdmin(ctx, f.actor, own.ID, nil); err != nil {
		t.Fatalf("delete user exit: %v", err)
	}
	if o := f.overview(t); len(o.Exits) != 0 || len(o.ServerRoutes) != 0 {
		t.Fatalf("exit and its routes should be gone: %+v", o)
	}
	wantErr(t, "delete a missing exit", f.chain.DeleteExitAsAdmin(ctx, f.actor, own.ID, nil), ErrNotFound)
	_, err = f.chain.SetExitEnabled(ctx, f.actor, own.ID, true, nil)
	wantErr(t, "enable a missing exit", err, ErrNotFound)

	for action, want := range map[string]int{
		ActionExitCreate: 1, ActionExitLinkView: 1, ActionExitDisable: 1, ActionExitDeleteUser: 1,
	} {
		var n int
		if err := f.pool.QueryRow(ctx, sqlCountAudit, action).Scan(&n); err != nil || n != want {
			t.Fatalf("audit count of %s = %d, %v, want %d", action, n, err, want)
		}
	}
}
