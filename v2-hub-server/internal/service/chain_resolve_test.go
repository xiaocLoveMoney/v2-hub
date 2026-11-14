package service

import (
	"testing"
	"time"

	"v2-hub-server/internal/dao/sqlcgen"
)

// 生效计算的纯函数测试（docs/03 §5.18、§5.19，Ruling-46）：不连库。

const (
	testServerA int64 = 1
	testServerB int64 = 2
)

// resolveEnv 给出一个全部放行的环境：节点 1–4 可用（4 是 hysteria2），两台服务器都支持改内核配置，平台出口 7 已授权。
func resolveEnv() chainEnv {
	return chainEnv{
		flags: ChainFlags{Chain: true, CustomNode: true, ServerChain: true},
		available: map[int64]sqlcgen.ListUserSubNodesRow{
			1: {ID: 1, Protocol: "vless", ServerID: testServerA}, 2: {ID: 2, Protocol: "trojan", ServerID: testServerA},
			3: {ID: 3, Protocol: "vmess", ServerID: testServerB}, 4: {ID: 4, Protocol: "hysteria2", ServerID: testServerB},
		},
		platformExits: map[int64]bool{7: true},
		coreServers:   map[int64]bool{testServerA: true, testServerB: true},
		unsafeExits:   map[int64]bool{}, fingerprints: map[int64]string{}, views: map[int64]applyView{},
	}
}

// entry 造一个起飞节点：入站 id 取节点 id × 10，方便写“两个节点共用一个入站”的用例时单独指定。
func entry(nodeID, serverID int64) ChainRouteEntry {
	return ChainRouteEntry{NodeID: nodeID, InboundID: nodeID * 10, InboundTag: "in", ServerID: serverID}
}

func route(id int64, kind string, exit ChainRouteExit, entries ...ChainRouteEntry) ChainRoute {
	return ChainRoute{ID: id, Kind: kind, Enabled: true, Exit: exit, Entries: entries, exitEnabled: true, exitAllowServer: true, exitAllowClient: true}
}

func ownExit(id int64) ChainRouteExit      { return ChainRouteExit{Kind: ChainExitOwn, ID: id} }
func platformExit(id int64) ChainRouteExit { return ChainRouteExit{Kind: ChainExitPlatform, ID: id} }
func nodeExit(id int64) ChainRouteExit     { return ChainRouteExit{Kind: ChainExitNode, ID: id} }

func states(r ChainRoute) []string {
	out := make([]string, 0, len(r.Entries))
	for _, e := range r.Entries {
		out = append(out, e.State)
	}
	return out
}

func wantStates(t *testing.T, what string, r ChainRoute, want ...string) {
	t.Helper()
	got := states(r)
	if len(got) != len(want) {
		t.Fatalf("%s: states = %v, want %v", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: states = %v, want %v", what, got, want)
		}
	}
}

func TestResolveServerPriority(t *testing.T) {
	// 第 1 条占了节点 1；第 2 条的节点 1 被覆盖、节点 2 生效；第 3 条全被覆盖
	routes := []ChainRoute{
		route(11, ChainKindServer, ownExit(5), entry(1, testServerA)),
		route(12, ChainKindServer, platformExit(7), entry(1, testServerA), entry(2, testServerA)),
		route(13, ChainKindServer, ownExit(6), entry(2, testServerA)),
	}
	resolveRoutes(routes, resolveEnv())
	wantStates(t, "first", routes[0], EntryPending)
	wantStates(t, "second", routes[1], EntryShadowed, EntryPending)
	wantStates(t, "third", routes[2], EntryShadowed)
	if routes[1].Entries[0].CoveredBy != 1 || routes[2].Entries[0].CoveredBy != 2 || routes[2].Position != 3 {
		t.Fatalf("coveredBy / position wrong: %+v / %+v", routes[1].Entries, routes[2])
	}

	// 靠前的规则停用或阻断时不占位，自动落到后面的规则
	for what, mutate := range map[string]func(r *ChainRoute){
		"disabled": func(r *ChainRoute) { r.Enabled = false },
		"blocked":  func(r *ChainRoute) { r.exitEnabled = false },
	} {
		routes := []ChainRoute{
			route(11, ChainKindServer, ownExit(5), entry(1, testServerA)),
			route(12, ChainKindServer, platformExit(7), entry(1, testServerA)),
		}
		mutate(&routes[0])
		resolveRoutes(routes, resolveEnv())
		wantStates(t, what+" first", routes[0], EntryInactive)
		wantStates(t, what+" second takes over", routes[1], EntryPending)
	}
}

func TestResolveServerEntries(t *testing.T) {
	env := resolveEnv()
	env.coreServers[testServerB] = false
	sameInbound := entry(2, testServerA)
	sameInbound.InboundID = 10
	routes := []ChainRoute{
		// 节点 1 与“共用入站的节点 2”在同一条规则里都生效；节点 3 的面板不支持；节点 9 不可用
		route(11, ChainKindServer, platformExit(7), entry(1, testServerA), sameInbound, entry(3, testServerB), entry(9, testServerA)),
		// 另一条规则用到同一个入站（节点 2）时算被覆盖：按入站判断，不按节点
		route(12, ChainKindServer, platformExit(7), sameInbound),
	}
	resolveRoutes(routes, env)
	wantStates(t, "mixed entries", routes[0], EntryPending, EntryPending, EntryUnavailable, EntryUnavailable)
	wantStates(t, "same inbound in a later route", routes[1], EntryShadowed)

	// 客户端与服务端各排各的序号，互不覆盖
	routes = []ChainRoute{
		route(21, ChainKindClient, platformExit(7), entry(1, testServerA)),
		route(22, ChainKindServer, platformExit(7), entry(1, testServerA)),
	}
	resolveRoutes(routes, resolveEnv())
	if routes[0].Position != 1 || routes[1].Position != 1 {
		t.Fatalf("positions should restart per kind: %d, %d", routes[0].Position, routes[1].Position)
	}
	wantStates(t, "client", routes[0], EntryOK)
	wantStates(t, "server", routes[1], EntryPending)
}

func TestResolveClientPairs(t *testing.T) {
	routes := []ChainRoute{
		route(11, ChainKindClient, ownExit(5), entry(1, testServerA), entry(2, testServerA)),
		// 同一起飞节点换一个落地：都输出；同一（起飞，落地）组合：只取第一次
		route(12, ChainKindClient, platformExit(7), entry(1, testServerA)),
		route(13, ChainKindClient, ownExit(5), entry(2, testServerA), entry(9, testServerA)),
	}
	resolveRoutes(routes, resolveEnv())
	wantStates(t, "first", routes[0], EntryOK, EntryOK)
	wantStates(t, "other exit", routes[1], EntryOK)
	wantStates(t, "duplicate pair", routes[2], EntryDuplicate, EntryUnavailable)
	if routes[2].Entries[0].CoveredBy != 1 {
		t.Fatalf("duplicate should point at the first route: %+v", routes[2].Entries[0])
	}
}

func TestRouteBlockedReason(t *testing.T) {
	const fingerprint = "abc"
	cases := []struct {
		name   string
		route  ChainRoute
		mutate func(env *chainEnv, r *ChainRoute)
		want   string
	}{
		{name: "ok node exit", route: route(1, ChainKindClient, nodeExit(2)), want: ChainReasonNone},
		{name: "client flag off", route: route(1, ChainKindClient, nodeExit(2)), mutate: func(env *chainEnv, _ *ChainRoute) { env.flags.Chain = false }, want: ChainReasonPlan},
		{name: "server flag off", route: route(1, ChainKindServer, nodeExit(2)), mutate: func(env *chainEnv, _ *ChainRoute) { env.flags.ServerChain = false }, want: ChainReasonPlan},
		{name: "node exit gone", route: route(1, ChainKindClient, nodeExit(9)), want: ChainReasonExit},
		{name: "node exit quic", route: route(1, ChainKindClient, nodeExit(4)), want: ChainReasonExit},
		{name: "platform exit ok", route: route(1, ChainKindServer, platformExit(7)), want: ChainReasonNone},
		{name: "platform exit not granted", route: route(1, ChainKindServer, platformExit(8)), want: ChainReasonExit},
		{name: "platform exit not for server", route: route(1, ChainKindServer, platformExit(7)), mutate: func(_ *chainEnv, r *ChainRoute) { r.exitAllowServer = false }, want: ChainReasonExit},
		{name: "platform exit not for client", route: route(1, ChainKindClient, platformExit(7)), mutate: func(_ *chainEnv, r *ChainRoute) { r.exitAllowClient = false }, want: ChainReasonExit},
		{name: "own exit without flag", route: route(1, ChainKindClient, ownExit(5)), mutate: func(env *chainEnv, _ *ChainRoute) { env.flags.CustomNode = false }, want: ChainReasonPlan},
		{name: "own exit disabled by admin", route: route(1, ChainKindClient, ownExit(5)), mutate: func(_ *chainEnv, r *ChainRoute) { r.exitEnabled = false }, want: ChainReasonExit},
		{name: "own exit unsafe for server", route: route(1, ChainKindServer, ownExit(5)), mutate: func(env *chainEnv, _ *ChainRoute) { env.unsafeExits[5] = true }, want: ChainReasonUnsafe},
		{name: "unsafe does not matter for client", route: route(1, ChainKindClient, ownExit(5)), mutate: func(env *chainEnv, _ *ChainRoute) { env.unsafeExits[5] = true }, want: ChainReasonNone},
		{name: "quarantined", route: route(1, ChainKindServer, ownExit(5)), mutate: func(env *chainEnv, r *ChainRoute) {
			env.fingerprints[1], r.quarantineHash = fingerprint, fingerprint
		}, want: ChainReasonQuarantined},
		{name: "quarantine released after change", route: route(1, ChainKindServer, ownExit(5)), mutate: func(env *chainEnv, r *ChainRoute) {
			env.fingerprints[1], r.quarantineHash = "changed", fingerprint
		}, want: ChainReasonNone},
	}
	for _, c := range cases {
		env := resolveEnv()
		r := c.route
		if c.mutate != nil {
			c.mutate(&env, &r)
		}
		if got := routeBlockedReason(r, env); got != c.want {
			t.Errorf("%s: reason = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestServerEntryApplyState(t *testing.T) {
	appliedAt := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	r := ChainRoute{ID: 5, changedAt: appliedAt.Add(-time.Minute)}
	view := applyView{status: chainApplyApplied, applied: []int64{5}, appliedAt: appliedAt}
	if got := serverEntryApplyState(r, view, true); got != EntryApplied {
		t.Fatalf("applied and unchanged = %q", got)
	}
	// 上次下发之后又改过：还没生效
	r.changedAt = appliedAt.Add(time.Second)
	if got := serverEntryApplyState(r, view, true); got != EntryPending {
		t.Fatalf("changed after apply = %q", got)
	}
	view.status = chainApplyFailed
	if got := serverEntryApplyState(r, view, true); got != EntryFailed {
		t.Fatalf("server failed = %q", got)
	}
	if got := serverEntryApplyState(ChainRoute{ID: 6}, applyView{}, false); got != EntryPending {
		t.Fatalf("no record yet = %q", got)
	}
}
