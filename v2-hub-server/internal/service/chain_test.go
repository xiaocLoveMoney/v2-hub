package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"v2-hub-server/internal/manager/render"
)

// 出口的示例链接；用户名、密码与 UUID 都是示例值，不对应任何真实节点。
const (
	chainSocksLink    = "socks5://sample-user:sample-auth-0001@home.example.net:1080#home-socks"
	chainVLESSLink    = "vless://11111111-1111-4111-8111-111111111111@exit.example.net:443?type=tcp&security=tls&sni=exit.example.net#own-vps"
	chainHy2Link      = "hysteria2://sample-auth-0001@quic.example.net:8443?sni=quic.example.net#hy2"
	chainExitName     = "家宽落地"
	chainPlatformName = "平台出口-东京"
	sqlSetPlanFlags   = `UPDATE plan SET allow_client_chain = $2, allow_custom_node = $3 WHERE id = (SELECT plan_id FROM end_user WHERE id = $1)`
	sqlSetNodeEnable  = `UPDATE node SET is_enabled = $2 WHERE id = $1`
	sqlDeleteNode     = `DELETE FROM node WHERE id = $1`
	sqlCountChain     = `SELECT (SELECT COUNT(*) FROM chain_route WHERE user_id = $1) + (SELECT COUNT(*) FROM chain_exit WHERE user_id = $1)`
)

// chainResolver 是测试用的域名解析表：出口的域名解析到公网地址，inner 解析到内网。
func chainResolver() fakeResolver {
	return fakeResolver{
		"home.example.net": {guardPublicV4}, "exit.example.net": {"1.0.0.1"}, "inner.example.net": {"10.0.0.8"},
		"panel-a.example.com": {"8.8.4.4"},
	}
}

type chainFixture struct {
	*subFixture
	chain *ChainService
}

func newChainFixture(t *testing.T) *chainFixture {
	t.Helper()
	sf := newSubFixture(t)
	opts := ChainOptions{Resolver: chainResolver(), Now: sf.clock.now}
	return &chainFixture{subFixture: sf, chain: NewChainService(sf.pool, testSealer(t), nil, opts)}
}

// setFlags 直接改用户当前套餐的两个开关。
func (f *chainFixture) setFlags(t *testing.T, chain, customNode bool) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), sqlSetPlanFlags, f.user.ID, chain, customNode); err != nil {
		t.Fatalf("set plan flags: %v", err)
	}
}

func (f *chainFixture) overview(t *testing.T) *ChainOverview {
	t.Helper()
	o, err := f.chain.Overview(context.Background(), f.principal)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	return o
}

// clientIn 造一条客户端规则的输入。
func clientIn(exitKind string, exitID int64, entries ...int64) ChainRouteInput {
	return ChainRouteInput{Kind: ChainKindClient, EntryNodeIDs: entries, ExitKind: exitKind, ExitID: exitID, Enabled: true}
}

func (f *chainFixture) mustRoute(t *testing.T, in ChainRouteInput) *ChainRoute {
	t.Helper()
	r, err := f.chain.CreateRoute(context.Background(), f.principal, in)
	if err != nil {
		t.Fatalf("CreateRoute(%+v): %v", in, err)
	}
	return r
}

func (f *chainFixture) mustExit(t *testing.T, link, name string) *ChainExit {
	t.Helper()
	e, err := f.chain.CreateExit(context.Background(), f.principal, link, name)
	if err != nil {
		t.Fatalf("CreateExit: %v", err)
	}
	return e
}

// platformExit 建一个挂在夹具节点组上的平台出口。
func (f *chainFixture) platformExit(t *testing.T, name string, allowServer, allowClient bool) *AdminChainExit {
	t.Helper()
	e, err := f.chain.CreatePlatformExit(context.Background(), f.actor, PlatformExitInput{
		Name: name, Link: chainVLESSLink, Enabled: true, AllowServer: allowServer, AllowClient: allowClient, NodeGroupIDs: []int64{f.group.ID},
	}, nil)
	if err != nil {
		t.Fatalf("CreatePlatformExit: %v", err)
	}
	return e
}

// reasons 返回某类规则的“状态:原因”，按 id 索引。
func (f *chainFixture) reasons(t *testing.T, kind string) map[int64]string {
	t.Helper()
	o := f.overview(t)
	routes := o.ClientRoutes
	if kind == ChainKindServer {
		routes = o.ServerRoutes
	}
	out := make(map[int64]string, len(routes))
	for _, r := range routes {
		out[r.ID] = r.Status + ":" + r.BlockedReason
	}
	return out
}

// dialers 渲染 Mihomo 订阅，返回“链路名 → 起飞节点名”。
func (f *chainFixture) dialers(t *testing.T, token string) map[string]string {
	t.Helper()
	var cfg struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(f.render(t, token, render.FormatMihomo, "").Body, &cfg); err != nil {
		t.Fatalf("mihomo output is not valid YAML: %v", err)
	}
	out := make(map[string]string, len(cfg.Proxies))
	for _, p := range cfg.Proxies {
		if d, ok := p["dialer-proxy"].(string); ok {
			out[fmt.Sprint(p["name"])] = d
		}
	}
	return out
}

func wantErr(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: err = %v, want %v", what, err, want)
	}
}

func TestChainRoutesAndExits(t *testing.T) {
	f := newChainFixture(t)
	ctx := context.Background()
	first, second := f.nodes[0], f.nodes[1]

	// 套餐没开：总览照常返回，新建被拒
	o := f.overview(t)
	if o.Flags.Chain || o.Flags.CustomNode || len(o.Nodes) != 2 || !o.Nodes[0].CanBeExit || len(o.ClientRoutes) != 0 {
		t.Fatalf("overview before enabling = %+v", o)
	}
	_, err := f.chain.CreateRoute(ctx, f.principal, clientIn(ChainExitNode, second.ID, first.ID))
	wantErr(t, "create route without plan flag", err, ErrFeatureDisabled)
	_, err = f.chain.CreateExit(ctx, f.principal, chainSocksLink, "")
	wantErr(t, "create exit without plan flag", err, ErrFeatureDisabled)

	// 只开链式：平台节点作落地；没起名时显示落地的名称
	f.setFlags(t, true, false)
	route := f.mustRoute(t, clientIn(ChainExitNode, second.ID, first.ID))
	isFirstOK := route.Status == ChainOK && route.Exit.Kind == ChainExitNode && route.DisplayName == second.DisplayName &&
		route.Position == 1 && len(route.Entries) == 1 && route.Entries[0].State == EntryOK
	if !isFirstOK {
		t.Fatalf("create route = %+v", route)
	}
	for what, in := range map[string]ChainRouteInput{
		"exit is an entry":   clientIn(ChainExitNode, first.ID, first.ID, second.ID),
		"no entries":         clientIn(ChainExitNode, second.ID),
		"duplicate entries":  clientIn(ChainExitNode, second.ID, first.ID, first.ID),
		"negative entry":     clientIn(ChainExitNode, second.ID, -1),
		"unknown entry":      clientIn(ChainExitNode, second.ID, second.ID+999),
		"unknown exit":       clientIn(ChainExitNode, second.ID+999, first.ID),
		"unknown exit kind":  clientIn("custom", second.ID, first.ID),
		"missing exit id":    clientIn(ChainExitNode, 0, first.ID),
		"unknown platform":   clientIn(ChainExitPlatform, 999, first.ID),
		"name with newline":  {Kind: ChainKindClient, EntryNodeIDs: []int64{first.ID}, ExitKind: ChainExitNode, ExitID: second.ID, Name: "a\nb"},
		"name too long":      {Kind: ChainKindClient, EntryNodeIDs: []int64{first.ID}, ExitKind: ChainExitNode, ExitID: second.ID, Name: strings.Repeat("长", maxChainNameLen+1)},
		"unknown route kind": {Kind: "relay", EntryNodeIDs: []int64{first.ID}, ExitKind: ChainExitNode, ExitID: second.ID},
		"too many entries":   clientIn(ChainExitNode, second.ID, make([]int64, MaxRouteEntries+1)...),
	} {
		_, err := f.chain.CreateRoute(ctx, f.principal, in)
		wantErr(t, what, err, ErrInvalidArgument)
	}

	// 自己的出口要另一个开关
	_, err = f.chain.CreateExit(ctx, f.principal, chainSocksLink, "")
	wantErr(t, "own exit needs its own flag", err, ErrFeatureDisabled)
	f.setFlags(t, true, true)
	for what, link := range map[string]string{
		"empty": "  ", "garbage": "not a link", "hysteria2": chainHy2Link, "too long": chainSocksLink + strings.Repeat("x", maxExitLinkLen),
	} {
		_, err := f.chain.CreateExit(ctx, f.principal, link, "")
		wantErr(t, "exit link "+what, err, ErrInvalidArgument)
	}
	socks := f.mustExit(t, chainSocksLink, "")
	isSocksOK := socks.Name == "home-socks" && socks.Protocol == render.ProtoSocks && socks.Address == "home.example.net" &&
		socks.Port == 1080 && socks.RouteCount == 0 && socks.Enabled
	if !isSocksOK {
		t.Fatalf("create exit = %+v", socks)
	}
	_, err = f.chain.CreateExit(ctx, f.principal, chainVLESSLink, "home-socks")
	wantErr(t, "duplicate exit name", err, ErrConflict)
	if link, err := f.chain.ExitLink(ctx, f.principal, socks.ID); err != nil || link != chainSocksLink {
		t.Fatalf("ExitLink mismatch, err = %v", err)
	}
	renamed, err := f.chain.UpdateExit(ctx, f.principal, socks.ID, chainExitName, chainVLESSLink)
	if err != nil || renamed.Name != chainExitName || renamed.Protocol != render.ProtoVLESS || renamed.Address != "exit.example.net" || renamed.Port != 443 {
		t.Fatalf("update exit = %+v, %v", renamed, err)
	}
	if kept, err := f.chain.UpdateExit(ctx, f.principal, socks.ID, chainExitName, ""); err != nil || kept.Protocol != render.ProtoVLESS {
		t.Fatalf("update without link should keep the link: %+v, %v", kept, err)
	}
	_, err = f.chain.UpdateExit(ctx, f.principal, socks.ID, " ", "")
	wantErr(t, "empty exit name", err, ErrInvalidArgument)
	_, err = f.chain.CreateRoute(ctx, f.principal, clientIn(ChainExitOwn, socks.ID+999, first.ID))
	wantErr(t, "unknown own exit", err, ErrInvalidArgument)

	// 一条规则带两个起飞节点，落地是自己的出口；客户端链式不限制出口地址
	in := clientIn(ChainExitOwn, socks.ID, first.ID, second.ID)
	in.Name = " 回家 "
	ownRoute := f.mustRoute(t, in)
	isOwnOK := ownRoute.Status == ChainOK && ownRoute.Exit.Kind == ChainExitOwn && ownRoute.Name == "回家" &&
		ownRoute.Exit.Name == chainExitName && ownRoute.Position == 2 && len(ownRoute.Entries) == 2
	if !isOwnOK {
		t.Fatalf("create route with own exit = %+v", ownRoute)
	}

	// 修改：换起飞节点与落地，规则还在（起飞节点先加后删，不会被“删光即删规则”的触发器误删）
	updated, err := f.chain.UpdateRoute(ctx, f.principal, ownRoute.ID, ChainRouteInput{
		EntryNodeIDs: []int64{second.ID}, ExitKind: ChainExitOwn, ExitID: socks.ID, Name: "回家", Enabled: true,
	})
	if err != nil || len(updated.Entries) != 1 || updated.Entries[0].NodeID != second.ID || updated.Kind != ChainKindClient {
		t.Fatalf("update route = %+v, %v", updated, err)
	}

	// 归属（R8）：别人的规则与出口一律 404；引用别人的出口与引用不存在的出口同一个说明
	bob := f.newUser(t, "bob", *f.user.PlanID)
	other := &UserPrincipal{UserID: bob.ID, Username: bob.Username, Status: UserStatusActive}
	_, err = f.chain.UpdateRoute(ctx, other, route.ID, clientIn(ChainExitNode, second.ID, first.ID))
	wantErr(t, "update other's route", err, ErrNotFound)
	wantErr(t, "delete other's route", f.chain.DeleteRoute(ctx, other, route.ID), ErrNotFound)
	_, err = f.chain.ReorderRoutes(ctx, other, ChainKindClient, []int64{route.ID})
	wantErr(t, "reorder other's routes", err, ErrInvalidArgument)
	_, err = f.chain.ExitLink(ctx, other, socks.ID)
	wantErr(t, "read other's exit link", err, ErrNotFound)
	_, err = f.chain.UpdateExit(ctx, other, socks.ID, "x", "")
	wantErr(t, "update other's exit", err, ErrNotFound)
	wantErr(t, "delete other's exit", f.chain.DeleteExit(ctx, other, socks.ID), ErrNotFound)
	_, err = f.chain.CreateRoute(ctx, other, clientIn(ChainExitOwn, socks.ID, first.ID))
	wantErr(t, "use other's exit", err, ErrInvalidArgument)
	if o, err := f.chain.Overview(ctx, other); err != nil || len(o.ClientRoutes) != 0 || len(o.Exits) != 0 {
		t.Fatalf("other user's overview = %+v, %v", o, err)
	}

	// 阻断原因：出口开关关掉 → plan；落地节点停用 → exit（起飞节点停用只影响那个节点）；全关 → 都是 plan
	reversed := f.mustRoute(t, clientIn(ChainExitNode, first.ID, second.ID))
	f.setFlags(t, true, false)
	if got := f.reasons(t, ChainKindClient); got[route.ID] != "ok:" || got[ownRoute.ID] != "blocked:plan" {
		t.Fatalf("reasons without own-exit flag = %v", got)
	}
	f.setFlags(t, true, true)
	if _, err := f.pool.Exec(ctx, sqlSetNodeEnable, first.ID, false); err != nil {
		t.Fatalf("disable node: %v", err)
	}
	o = f.overview(t)
	got := f.reasons(t, ChainKindClient)
	if got[route.ID] != "ok:" || got[reversed.ID] != "blocked:exit" || got[ownRoute.ID] != "ok:" || o.ClientRoutes[0].Entries[0].State != EntryUnavailable {
		t.Fatalf("reasons with a node disabled = %v, first route entries = %+v", got, o.ClientRoutes[0].Entries)
	}
	if _, err := f.pool.Exec(ctx, sqlSetNodeEnable, first.ID, true); err != nil {
		t.Fatalf("enable node: %v", err)
	}
	f.setFlags(t, false, false)
	if got := f.reasons(t, ChainKindClient); got[route.ID] != "blocked:plan" || got[reversed.ID] != "blocked:plan" {
		t.Fatalf("reasons without chain flag = %v", got)
	}
	_, err = f.chain.UpdateRoute(ctx, f.principal, route.ID, clientIn(ChainExitNode, second.ID, first.ID))
	wantErr(t, "update route without plan flag", err, ErrFeatureDisabled)
	_, err = f.chain.ReorderRoutes(ctx, f.principal, ChainKindClient, []int64{reversed.ID, ownRoute.ID, route.ID})
	wantErr(t, "reorder without plan flag", err, ErrFeatureDisabled)
	// 套餐没开时仍然可以删
	if err := f.chain.DeleteRoute(ctx, f.principal, reversed.ID); err != nil {
		t.Fatalf("delete route without plan flag: %v", err)
	}

	// 删出口连带删引用它的规则；删用户清掉剩下的
	if exits, err := listChainExits(ctx, f.chain.q, f.user.ID, socks.ID); err != nil || exits[0].RouteCount != 1 {
		t.Fatalf("route count of exit = %+v, %v", exits, err)
	}
	if err := f.chain.DeleteExit(ctx, f.principal, socks.ID); err != nil {
		t.Fatalf("DeleteExit: %v", err)
	}
	if left := f.overview(t).ClientRoutes; len(left) != 1 || left[0].ID != route.ID {
		t.Fatalf("routes after deleting exit = %+v", left)
	}
	if err := f.users.DeleteUser(ctx, f.actor, f.user.ID, nil); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	var left int
	if err := f.pool.QueryRow(ctx, sqlCountChain, f.user.ID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("chain rows left after deleting user = %d, %v", left, err)
	}
}

func TestChainPlatformExitGrant(t *testing.T) {
	f := newChainFixture(t)
	ctx := context.Background()
	f.setFlags(t, true, false)
	serverOnly := f.platformExit(t, "只给服务端", true, false)
	exit := f.platformExit(t, chainPlatformName, true, true)

	// 用户只看到名称与协议；不允许客户端的不能用于客户端规则；不受“自己的出口”开关影响
	o := f.overview(t)
	if len(o.PlatformExits) != 2 || o.PlatformExits[1].Name != chainPlatformName || o.PlatformExits[1].Protocol != render.ProtoVLESS {
		t.Fatalf("platform exits = %+v", o.PlatformExits)
	}
	_, err := f.chain.CreateRoute(ctx, f.principal, clientIn(ChainExitPlatform, serverOnly.ID, f.nodes[0].ID))
	wantErr(t, "platform exit not for client", err, ErrInvalidArgument)
	route := f.mustRoute(t, clientIn(ChainExitPlatform, exit.ID, f.nodes[0].ID, f.nodes[1].ID))
	if route.Status != ChainOK || route.Exit.Kind != ChainExitPlatform || route.DisplayName != chainPlatformName {
		t.Fatalf("route with platform exit = %+v", route)
	}
	// 用户拿不到平台出口的链接
	_, err = f.chain.ExitLink(ctx, f.principal, exit.ID)
	wantErr(t, "user reads platform exit link", err, ErrNotFound)
	wantErr(t, "user deletes platform exit", f.chain.DeleteExit(ctx, f.principal, exit.ID), ErrNotFound)

	// 停用、移出节点组：规则保留但阻断；恢复后自动通畅
	if _, err := f.chain.SetExitEnabled(ctx, f.actor, exit.ID, false, nil); err != nil {
		t.Fatalf("SetExitEnabled: %v", err)
	}
	if got := f.reasons(t, ChainKindClient); got[route.ID] != "blocked:exit" || len(f.overview(t).PlatformExits) != 1 {
		t.Fatalf("disabled platform exit: reasons = %v", got)
	}
	in := PlatformExitInput{Name: chainPlatformName, Enabled: true, AllowServer: true, AllowClient: true}
	if _, err := f.chain.UpdatePlatformExit(ctx, f.actor, exit.ID, in, nil); err != nil {
		t.Fatalf("UpdatePlatformExit: %v", err)
	}
	if got := f.reasons(t, ChainKindClient); got[route.ID] != "blocked:exit" {
		t.Fatalf("platform exit without a node group should not be granted: %v", got)
	}
	in.NodeGroupIDs = []int64{f.group.ID}
	if _, err := f.chain.UpdatePlatformExit(ctx, f.actor, exit.ID, in, nil); err != nil {
		t.Fatalf("UpdatePlatformExit: %v", err)
	}
	if got := f.reasons(t, ChainKindClient); got[route.ID] != "ok:" {
		t.Fatalf("platform exit granted again: %v", got)
	}
	// 改成不允许客户端：阻断
	in.AllowClient = false
	if _, err := f.chain.UpdatePlatformExit(ctx, f.actor, exit.ID, in, nil); err != nil {
		t.Fatalf("UpdatePlatformExit: %v", err)
	}
	if got := f.reasons(t, ChainKindClient); got[route.ID] != "blocked:exit" {
		t.Fatalf("platform exit no longer for client: %v", got)
	}
}

// 指定用户（Ruling-64）：只按名单授权，另一组清空；仍要求套餐开链式；名单里不能有不存在的用户。
func TestChainPlatformExitAssignUsers(t *testing.T) {
	f := newChainFixture(t)
	ctx := context.Background()
	f.setFlags(t, true, false)
	bob := f.newUser(t, "bob", *f.user.PlanID)
	bobPrincipal := &UserPrincipal{UserID: bob.ID, Username: bob.Username, Status: UserStatusActive}
	exit := f.platformExit(t, chainPlatformName, true, true)
	route := f.mustRoute(t, clientIn(ChainExitPlatform, exit.ID, f.nodes[0].ID))

	// 只指定 bob：节点组被清空，本人的规则阻断，bob 看得到
	in := PlatformExitInput{
		Name: chainPlatformName, Enabled: true, AllowServer: true, AllowClient: true,
		AssignMode: ExitAssignUser, NodeGroupIDs: []int64{f.group.ID}, UserIDs: []int64{bob.ID, bob.ID},
	}
	got, err := f.chain.UpdatePlatformExit(ctx, f.actor, exit.ID, in, nil)
	if err != nil || got.AssignMode != ExitAssignUser || len(got.NodeGroups) != 0 || len(got.Users) != 1 || got.Users[0].Username != "bob" {
		t.Fatalf("assign to bob = %+v, %v", got, err)
	}
	if r := f.reasons(t, ChainKindClient); r[route.ID] != "blocked:exit" {
		t.Fatalf("unassigned user should be blocked: %v", r)
	}
	bobView, err := f.chain.Overview(ctx, bobPrincipal)
	if err != nil || len(bobView.PlatformExits) != 1 {
		t.Fatalf("bob overview = %+v, %v", bobView, err)
	}

	// 加上本人：恢复通畅；套餐关掉链式开关仍然阻断
	in.UserIDs = []int64{f.user.ID, bob.ID}
	if _, err := f.chain.UpdatePlatformExit(ctx, f.actor, exit.ID, in, nil); err != nil {
		t.Fatalf("assign to both: %v", err)
	}
	if r := f.reasons(t, ChainKindClient); r[route.ID] != "ok:" {
		t.Fatalf("assigned user should be ok: %v", r)
	}
	f.setFlags(t, false, false)
	if r := f.reasons(t, ChainKindClient); r[route.ID] != "blocked:plan" {
		t.Fatalf("plan switch still required: %v", r)
	}
	f.setFlags(t, true, false)

	// 非法输入：不存在的用户、未知的分配方式；失败不改动现状
	in.UserIDs = []int64{f.user.ID, 999999999}
	_, err = f.chain.UpdatePlatformExit(ctx, f.actor, exit.ID, in, nil)
	wantErr(t, "unknown user", err, ErrInvalidArgument)
	in.AssignMode = "nobody"
	_, err = f.chain.UpdatePlatformExit(ctx, f.actor, exit.ID, in, nil)
	wantErr(t, "bad assign mode", err, ErrInvalidArgument)
	if r := f.reasons(t, ChainKindClient); r[route.ID] != "ok:" {
		t.Fatalf("failed update must not change the grant: %v", r)
	}

	// 改回按节点组（省略方式 = group）：用户名单清空，按套餐的节点组授权
	back, err := f.chain.UpdatePlatformExit(ctx, f.actor, exit.ID, PlatformExitInput{
		Name: chainPlatformName, Enabled: true, AllowServer: true, AllowClient: true, NodeGroupIDs: []int64{f.group.ID}, UserIDs: []int64{bob.ID},
	}, nil)
	if err != nil || back.AssignMode != ExitAssignGroup || len(back.Users) != 0 || len(back.NodeGroups) != 1 {
		t.Fatalf("back to group = %+v, %v", back, err)
	}
	if r := f.reasons(t, ChainKindClient); r[route.ID] != "ok:" {
		t.Fatalf("group grant: %v", r)
	}
}

func TestChainSubscriptionOutput(t *testing.T) {
	f := newChainFixture(t)
	ctx := context.Background()
	first, second := f.nodes[0], f.nodes[1]
	token := f.token(t)
	f.setFlags(t, true, true)
	socks := f.mustExit(t, chainSocksLink, chainExitName)
	nodeRoute := f.mustRoute(t, clientIn(ChainExitNode, second.ID, first.ID))
	named := clientIn(ChainExitOwn, socks.ID, first.ID, second.ID)
	named.Name = "回家"
	ownRoute := f.mustRoute(t, named)
	// 重复的（起飞，落地）组合只输出第一次
	dup := f.mustRoute(t, clientIn(ChainExitOwn, socks.ID, second.ID))
	if dup.Entries[0].State != EntryDuplicate || dup.Entries[0].CoveredBy != ownRoute.Position {
		t.Fatalf("duplicate pair = %+v", dup.Entries)
	}

	// Mihomo：没起名的规则用“起飞名 → 落地名”，起了名的每个起飞节点一条“规则名 · 起飞名”
	defaultName := render.ChainDefaultName(first.DisplayName, second.DisplayName)
	want := map[string]string{
		defaultName: first.DisplayName, "回家 · " + first.DisplayName: first.DisplayName, "回家 · " + second.DisplayName: second.DisplayName,
	}
	got := f.dialers(t, token)
	if len(got) != len(want) {
		t.Fatalf("dialer-proxy = %v, want %v", got, want)
	}
	for name, dialer := range want {
		if got[name] != dialer {
			t.Fatalf("dialer-proxy = %v, want %v", got, want)
		}
	}
	// sing-box：三个带 detour 的出站；Xray：两个节点 + 三条链路各一份配置；Base64 不带链路
	var sb struct {
		Outbounds []struct {
			Type   string `json:"type"`
			Detour string `json:"detour"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(f.render(t, token, render.FormatSingBox, "").Body, &sb); err != nil {
		t.Fatalf("sing-box output: %v", err)
	}
	detours, socksOut := 0, 0
	for _, o := range sb.Outbounds {
		if o.Detour != "" {
			detours++
			if o.Type == "socks" {
				socksOut++
			}
		}
	}
	if detours != 3 || socksOut != 2 {
		t.Fatalf("sing-box detours = %d (socks %d), want 3 (2)", detours, socksOut)
	}
	var xr []struct {
		Remarks   string `json:"remarks"`
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(f.render(t, token, render.FormatXray, "").Body, &xr); err != nil || len(xr) != 5 || len(xr[4].Outbounds) != 3 {
		t.Fatalf("xray configs = %+v, %v", xr, err)
	}
	if nodes := decodeBase64Sub(t, f.render(t, token, render.FormatBase64, "").Body); len(nodes) != 4 {
		t.Fatalf("base64 should carry no chains: %q", nodeNames(nodes))
	}

	// 用户自己关掉的规则不输出；原先被它盖住的重复组合随之生效
	if _, err := f.chain.UpdateRoute(ctx, f.principal, ownRoute.ID, ChainRouteInput{
		EntryNodeIDs: []int64{first.ID, second.ID}, ExitKind: ChainExitOwn, ExitID: socks.ID, Name: "回家", Enabled: false,
	}); err != nil {
		t.Fatalf("UpdateRoute: %v", err)
	}
	dupName := render.ChainDefaultName(second.DisplayName, chainExitName)
	if got := f.dialers(t, token); len(got) != 2 || got[defaultName] == "" || got[dupName] != second.DisplayName {
		t.Fatalf("dialers after disabling the named route = %v", got)
	}
	// 阻断的规则保留但不输出；条件恢复后自动回来
	f.setFlags(t, false, true)
	if got := f.dialers(t, token); len(got) != 0 {
		t.Fatalf("blocked routes must not be rendered: %v", got)
	}
	if routes := f.overview(t).ClientRoutes; len(routes) != 3 {
		t.Fatalf("blocked routes must be kept: %+v", routes)
	}
	f.setFlags(t, true, true)
	if got := f.dialers(t, token); len(got) != 2 || got[nodeRoute.DisplayName] != "" {
		t.Fatalf("routes should come back once allowed again: %v", got)
	}
}

func TestChainReorder(t *testing.T) {
	f := newChainFixture(t)
	ctx := context.Background()
	f.setFlags(t, true, true)
	socks := f.mustExit(t, chainSocksLink, chainExitName)
	a := f.mustRoute(t, clientIn(ChainExitOwn, socks.ID, f.nodes[0].ID))
	b := f.mustRoute(t, clientIn(ChainExitOwn, socks.ID, f.nodes[0].ID, f.nodes[1].ID))
	c := f.mustRoute(t, clientIn(ChainExitNode, f.nodes[1].ID, f.nodes[0].ID))
	if a.Position != 1 || b.Position != 2 || c.Position != 3 || b.Entries[0].State != EntryDuplicate || b.Entries[1].State != EntryOK {
		t.Fatalf("initial order: %+v / %+v / %+v", a, b, c)
	}
	for what, ids := range map[string][]int64{
		"missing one": {a.ID, b.ID}, "unknown id": {a.ID, b.ID, c.ID + 999}, "duplicate": {a.ID, a.ID, b.ID}, "extra": {a.ID, b.ID, c.ID, c.ID + 1}, "empty": {},
	} {
		_, err := f.chain.ReorderRoutes(ctx, f.principal, ChainKindClient, ids)
		wantErr(t, "reorder "+what, err, ErrInvalidArgument)
	}
	_, err := f.chain.ReorderRoutes(ctx, f.principal, "relay", []int64{a.ID, b.ID, c.ID})
	wantErr(t, "reorder unknown kind", err, ErrInvalidArgument)

	// 把第 2 条提到最前：它的两个起飞节点都生效，原第 1 条变成重复
	routes, err := f.chain.ReorderRoutes(ctx, f.principal, ChainKindClient, []int64{b.ID, c.ID, a.ID})
	if err != nil || len(routes) != 3 || routes[0].ID != b.ID || routes[1].ID != c.ID || routes[2].ID != a.ID {
		t.Fatalf("reorder = %+v, %v", routes, err)
	}
	isMoved := routes[0].Position == 1 && routes[0].Entries[0].State == EntryOK && routes[2].Position == 3 &&
		routes[2].Entries[0].State == EntryDuplicate && routes[2].Entries[0].CoveredBy == 1
	if !isMoved {
		t.Fatalf("states after reorder = %+v", routes)
	}
	// 删掉中间一条后序号仍然连续，新建的排在最后
	if err := f.chain.DeleteRoute(ctx, f.principal, c.ID); err != nil {
		t.Fatalf("DeleteRoute: %v", err)
	}
	d := f.mustRoute(t, clientIn(ChainExitNode, f.nodes[0].ID, f.nodes[1].ID))
	left := f.overview(t).ClientRoutes
	if len(left) != 3 || left[1].ID != a.ID || left[1].Position != 2 || left[2].ID != d.ID || d.Position != 3 {
		t.Fatalf("positions after delete and create = %+v", left)
	}
}

func TestChainLimits(t *testing.T) {
	f := newChainFixture(t)
	ctx := context.Background()
	f.setFlags(t, true, true)
	exitIDs := make([]int64, 0, MaxChainExits)
	for i := range MaxChainExits {
		exitIDs = append(exitIDs, f.mustExit(t, chainSocksLink, fmt.Sprintf("落地 %d", i)).ID)
	}
	_, err := f.chain.CreateExit(ctx, f.principal, chainSocksLink, "多出来的")
	wantErr(t, "exit over limit", err, ErrInvalidArgument)

	// 同一组起飞节点配不同的出口各算一条；到 20 条为止
	for i := range MaxClientRoutes {
		f.mustRoute(t, clientIn(ChainExitOwn, exitIDs[i%MaxChainExits], f.nodes[i/MaxChainExits].ID))
	}
	if n := len(f.overview(t).ClientRoutes); n != MaxClientRoutes {
		t.Fatalf("routes = %d, want %d", n, MaxClientRoutes)
	}
	_, err = f.chain.CreateRoute(ctx, f.principal, clientIn(ChainExitNode, f.nodes[1].ID, f.nodes[0].ID))
	wantErr(t, "route over limit", err, ErrInvalidArgument)
}

func TestChainNodeDeletion(t *testing.T) {
	f := newChainFixture(t)
	ctx := context.Background()
	f.setFlags(t, true, true)
	socks := f.mustExit(t, chainSocksLink, chainExitName)
	both := f.mustRoute(t, clientIn(ChainExitOwn, socks.ID, f.nodes[0].ID, f.nodes[1].ID))
	onlyFirst := f.mustRoute(t, clientIn(ChainExitOwn, socks.ID, f.nodes[0].ID))
	exitIsFirst := f.mustRoute(t, clientIn(ChainExitNode, f.nodes[0].ID, f.nodes[1].ID))

	// 删节点：作落地的规则一并删除；作起飞的从规则里移除，规则不剩起飞节点时一并删除（docs/03 §5.18 阻断与删除）
	if _, err := f.pool.Exec(ctx, sqlDeleteNode, f.nodes[0].ID); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	left := f.overview(t).ClientRoutes
	if len(left) != 1 || left[0].ID != both.ID || len(left[0].Entries) != 1 || left[0].Entries[0].NodeID != f.nodes[1].ID {
		t.Fatalf("routes after deleting a node = %+v (dropped %d and %d expected)", left, onlyFirst.ID, exitIsFirst.ID)
	}
}
