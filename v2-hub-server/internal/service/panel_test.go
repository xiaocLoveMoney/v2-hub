package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"v2-hub-server/internal/manager/inboundtpl"
	"v2-hub-server/internal/manager/panel"
	"v2-hub-server/internal/manager/panel/paneltest"
)

const (
	fakeType  = "fake"
	fakeBase  = "https://panel-a.example.com:2053/secret-path"
	fakeToken = "panel-token-123"
)

// fakeFleet 按面板地址返回同一台内存面板，模拟“同一台服务器”的多次连接。
type fakeFleet struct {
	mu     sync.Mutex
	panels map[string]*paneltest.Fake
	tokens map[string]string
}

func (f *fakeFleet) get(base string) *paneltest.Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.panels[base]
	if !ok {
		p = paneltest.New()
		f.panels[base] = p
	}
	return p
}

func (f *fakeFleet) factory(cfg panel.ConnConfig) (panel.Provider, error) {
	f.mu.Lock()
	f.tokens[cfg.BaseURL] = cfg.Token
	f.mu.Unlock()
	return f.get(cfg.BaseURL), nil
}

type panelFixture struct {
	*fixture
	panels *PanelService
	fleet  *fakeFleet
	actor  *AdminPrincipal
}

func newPanelFixture(t *testing.T) *panelFixture {
	t.Helper()
	f := newFixture(t)
	f.bootstrap(t, "root")
	fleet := &fakeFleet{panels: map[string]*paneltest.Fake{}, tokens: map[string]string{}}
	reg := panel.NewRegistry(map[string]panel.Factory{fakeType: fleet.factory})
	ps := NewPanelService(f.pool, testSealer(t), reg, PanelConfig{RequestTimeout: time.Second}, f.clock.now)
	return &panelFixture{fixture: f, panels: ps, fleet: fleet, actor: f.login(t, "root").Principal}
}

func serverInput(name string) ServerInput {
	in := ServerInput{Name: name, Region: "HK"}
	in.ProviderType, in.BaseURL, in.TLSMode = fakeType, fakeBase, panel.TLSVerify
	tok := fakeToken
	in.APIToken = tok
	return in
}

func seedInbound(p *paneltest.Fake, ref string, port int, remark string) {
	p.PutInbound(panel.Inbound{
		Ref: ref, Protocol: "vless", Port: port, Remark: remark, Enabled: true, CoreType: panel.CoreXray,
		Network: "tcp", Config: json.RawMessage(`{"settings":{}}`),
	})
}

func TestCreateServerProbesSealsAndSyncs(t *testing.T) {
	f := newPanelFixture(t)
	ctx := context.Background()
	seedInbound(f.fleet.get(fakeBase), "7", 443, "existing")

	srv, err := f.panels.CreateServer(ctx, f.actor, serverInput("hk-1"), nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	if srv.Status != ServerStatusOnline || srv.PanelVersion != "fake-1.0" || srv.InboundCount != 1 || srv.LastSyncedAt == nil {
		t.Fatalf("server = %+v", srv)
	}
	var enc []byte
	if err := f.pool.QueryRow(ctx, "SELECT api_token_enc FROM server WHERE id = $1", srv.ID).Scan(&enc); err != nil {
		t.Fatalf("read token: %v", err)
	}
	if bytes.Contains(enc, []byte(fakeToken)) {
		t.Fatal("api token must be stored encrypted")
	}
	nodes, err := f.panels.ListNodes(ctx, NodeFilter{ServerID: srv.ID}, PageQuery{})
	if err != nil || nodes.Total != 1 || nodes.Items[0].Address != "panel-a.example.com" || nodes.Items[0].Port != 443 {
		t.Fatalf("nodes = %+v, %v", nodes, err)
	}
	if _, err := f.panels.CreateServer(ctx, f.actor, serverInput("hk-1"), nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name err = %v", err)
	}
}

// 默认名不带服务器名，备注为空取“协议-端口”；旧默认名启动时去前缀，管理员改过的不动（Ruling-63）。
func TestNodeDefaultNames(t *testing.T) {
	f := newPanelFixture(t)
	ctx := context.Background()
	fake := f.fleet.get(fakeBase)
	seedInbound(fake, "1", 443, "【VLESS】洛杉矶")
	seedInbound(fake, "2", 8443, "")
	seedInbound(fake, "3", 9443, "custom")
	srv, err := f.panels.CreateServer(ctx, f.actor, serverInput("hk-1"), nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	names := func() map[int]string {
		nodes, err := f.panels.ListNodes(ctx, NodeFilter{ServerID: srv.ID}, PageQuery{})
		if err != nil {
			t.Fatalf("ListNodes: %v", err)
		}
		out := make(map[int]string, len(nodes.Items))
		for _, n := range nodes.Items {
			out[n.Port] = n.DisplayName
		}
		return out
	}
	if got := names(); got[443] != "【VLESS】洛杉矶" || got[8443] != "vless-8443" {
		t.Fatalf("default names = %v", got)
	}
	// 模拟老版本生成的名字，以及管理员改过的名字
	if _, err := f.pool.Exec(ctx, `UPDATE node SET display_name = CASE
		WHEN display_name = '【VLESS】洛杉矶' THEN 'hk-1 · 【VLESS】洛杉矶'
		WHEN display_name = 'vless-8443' THEN 'hk-1 · 8443'
		ELSE 'hk-1-手改' END`); err != nil {
		t.Fatalf("seed legacy names: %v", err)
	}
	n, err := f.panels.RenameLegacyNodes(ctx)
	if err != nil || n != 2 {
		t.Fatalf("RenameLegacyNodes = %d, %v", n, err)
	}
	if got := names(); got[443] != "【VLESS】洛杉矶" || got[8443] != "8443" || got[9443] != "hk-1-手改" {
		t.Fatalf("renamed = %v", got)
	}
}

func TestCreateServerRejectsUnreachablePanel(t *testing.T) {
	f := newPanelFixture(t)
	f.fleet.get(fakeBase).FailWith(&panel.Error{Kind: panel.KindAuth, Message: "面板拒绝了 API token"})
	_, err := f.panels.CreateServer(context.Background(), f.actor, serverInput("bad"), nil)
	var se *Error
	if !errors.As(err, &se) || se.Code != CodeUpstream || se.Message != "面板拒绝了 API token" {
		t.Fatalf("err = %v", err)
	}
	list, err := f.panels.ListServers(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("failed probe must not save server: %+v, %v", list, err)
	}

	bad := serverInput("x")
	bad.TLSMode, bad.CertPinSHA256 = panel.TLSPin, "short"
	if _, err := f.panels.CreateServer(context.Background(), f.actor, bad, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("bad pin err = %v", err)
	}
}

func TestProbeTransitions(t *testing.T) {
	f := newPanelFixture(t)
	ctx := context.Background()
	srv, err := f.panels.CreateServer(ctx, f.actor, serverInput("hk-1"), nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	fake := f.fleet.get(fakeBase)
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"T3 auth failed", &panel.Error{Kind: panel.KindAuth, Message: "token 被拒"}, ServerStatusAuthFailed},
		{"T2 unreachable", &panel.Error{Kind: panel.KindUnreachable, Message: "超时"}, ServerStatusOffline},
		{"T1 back online", nil, ServerStatusOnline},
	}
	for _, c := range cases {
		fake.FailWith(c.err)
		if err := f.panels.ProbeServer(ctx, srv.ID); (err == nil) != (c.err == nil) {
			t.Fatalf("%s: ProbeServer err = %v", c.name, err)
		}
		got, err := f.panels.GetServer(ctx, srv.ID)
		if err != nil || got.Status != c.want {
			t.Fatalf("%s: status = %+v, %v", c.name, got, err)
		}
		if c.err != nil && got.LastError == "" {
			t.Fatalf("%s: last error should be recorded", c.name)
		}
	}
}

func TestSyncDetectsDriftAndMissing(t *testing.T) {
	f := newPanelFixture(t)
	ctx := context.Background()
	fake := f.fleet.get(fakeBase)
	seedInbound(fake, "1", 443, "a")
	seedInbound(fake, "2", 8443, "b")
	srv, err := f.panels.CreateServer(ctx, f.actor, serverInput("hk-1"), nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}

	seedInbound(fake, "1", 443, "a-changed-on-panel")
	fake.RemoveInbound("2")
	seedInbound(fake, "3", 9443, "new")
	res, err := f.panels.SyncServer(ctx, f.actor, srv.ID, nil)
	if err != nil {
		t.Fatalf("SyncServer: %v", err)
	}
	if res.Added != 1 || res.Drifted != 1 || res.Missing != 1 || res.Inbounds != 2 {
		t.Fatalf("result = %+v", res)
	}
	list, err := f.panels.ListServerInbounds(ctx, srv.ID)
	if err != nil {
		t.Fatalf("ListServerInbounds: %v", err)
	}
	byRef := map[string]Inbound{}
	for _, in := range list {
		byRef[in.RemoteRef] = in
	}
	if byRef["1"].DriftDetectedAt == nil || byRef["1"].Remark != "a-changed-on-panel" {
		t.Fatalf("inbound 1 = %+v (panel is the source of truth, drift flagged)", byRef["1"])
	}
	if !byRef["2"].IsMissing {
		t.Fatal("inbound 2 should be marked missing")
	}

	// 再同步一次：已标记的漂移不重复计数
	res, err = f.panels.SyncServer(ctx, f.actor, srv.ID, nil)
	if err != nil || res.Drifted != 0 {
		t.Fatalf("second sync = %+v, %v", res, err)
	}
	if err := f.panels.AckInboundDrift(ctx, f.actor, byRef["1"].ID, nil); err != nil {
		t.Fatalf("AckInboundDrift: %v", err)
	}
	got, err := f.panels.GetInbound(ctx, byRef["1"].ID)
	if err != nil || got.DriftDetectedAt != nil {
		t.Fatalf("after ack = %+v, %v", got, err)
	}
}

func TestInboundCRUDThroughPanel(t *testing.T) {
	f := newPanelFixture(t)
	ctx := context.Background()
	srv, err := f.panels.CreateServer(ctx, f.actor, serverInput("hk-1"), nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	in, err := f.panels.CreateInbound(ctx, f.actor, srv.ID, InboundCreateInput{
		Remark: "reality", Port: 443, Enabled: true, Template: inboundtpl.VlessReality,
		TemplateParams: inboundtpl.Params{Target: "www.cloudflare.com:443"},
	}, nil)
	if err != nil {
		t.Fatalf("CreateInbound: %v", err)
	}
	if in.Protocol != "vless" || in.Port != 443 {
		t.Fatalf("inbound = %+v", in)
	}
	if _, err := f.panels.CreateInbound(ctx, f.actor, srv.ID, InboundCreateInput{Remark: "dup", Port: 443, Template: inboundtpl.VmessWS}, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("port conflict should surface as invalid_argument, err = %v", err)
	}
	if _, err := f.panels.CreateInbound(ctx, f.actor, srv.ID, InboundCreateInput{Remark: "x", Port: 1000, Protocol: "vless", Config: json.RawMessage(`{"evil":{}}`)}, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unknown config key err = %v", err)
	}
	if _, err := f.panels.CreateInbound(ctx, f.actor, srv.ID, InboundCreateInput{Remark: "x", Port: 1000}, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("neither template nor config err = %v", err)
	}

	updated, err := f.panels.UpdateInbound(ctx, f.actor, in.ID, InboundUpdateInput{Remark: "reality-2", Port: 2443}, nil)
	if err != nil || updated.Remark != "reality-2" || updated.Port != 2443 {
		t.Fatalf("UpdateInbound = %+v, %v", updated, err)
	}
	disabled, err := f.panels.SetInboundEnabled(ctx, f.actor, in.ID, false, nil)
	if err != nil || disabled.IsEnabled {
		t.Fatalf("SetInboundEnabled = %+v, %v", disabled, err)
	}

	if err := f.panels.CreateEndpoints(ctx, f.actor, srv.ID, EndpointInput{InboundIDs: []int64{in.ID}, Addresses: []string{"cdn.example.com"}, Port: 8443}, nil); err != nil {
		t.Fatalf("CreateEndpoints: %v", err)
	}
	if err := f.panels.CreateEndpoints(ctx, f.actor, srv.ID, EndpointInput{InboundIDs: []int64{in.ID}, Addresses: []string{"bad host!"}}, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("bad address err = %v", err)
	}
	nodes, err := f.panels.ListNodes(ctx, NodeFilter{ServerID: srv.ID}, PageQuery{})
	if err != nil || nodes.Total != 2 {
		t.Fatalf("nodes after endpoint = %+v, %v", nodes, err)
	}
	var endpointNode Node
	for _, n := range nodes.Items {
		if n.EndpointID != nil {
			endpointNode = n
		}
	}
	if endpointNode.Address != "cdn.example.com" || endpointNode.Port != 8443 {
		t.Fatalf("endpoint node = %+v", endpointNode)
	}
	if err := f.panels.DeleteEndpoint(ctx, f.actor, *endpointNode.EndpointID, nil); err != nil {
		t.Fatalf("DeleteEndpoint: %v", err)
	}

	if err := f.panels.DeleteInbound(ctx, f.actor, in.ID, nil); err != nil {
		t.Fatalf("DeleteInbound: %v", err)
	}
	if _, err := f.panels.GetInbound(ctx, in.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted inbound err = %v", err)
	}
	if nodes, err := f.panels.ListNodes(ctx, NodeFilter{ServerID: srv.ID}, PageQuery{}); err != nil || nodes.Total != 0 {
		t.Fatalf("nodes after delete = %+v, %v", nodes, err)
	}
}

// inboundsByID 列出服务器的全部入站（含缺失的），按平台 id 索引。
func inboundsByID(t *testing.T, f *panelFixture, serverID int64) map[int64]Inbound {
	t.Helper()
	list, err := f.panels.ListServerInbounds(context.Background(), serverID)
	if err != nil {
		t.Fatalf("ListServerInbounds: %v", err)
	}
	out := make(map[int64]Inbound, len(list))
	for _, in := range list {
		out[in.ID] = in
	}
	return out
}

func directNodeOf(t *testing.T, f *panelFixture, serverID, inboundID int64) Node {
	t.Helper()
	nodes, err := f.panels.ListNodes(context.Background(), NodeFilter{ServerID: serverID}, PageQuery{})
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	for _, n := range nodes.Items {
		if n.InboundID == inboundID && n.EndpointID == nil {
			return n
		}
	}
	t.Fatalf("no direct node for inbound %d in %+v", inboundID, nodes.Items)
	return Node{}
}

// TestPanelRefReuse 覆盖 docs/03 §5.1 面板标识复用与缺失入站：面板把已删入站的 id 分给新入站时，
// 旧记录标记缺失并保留节点与节点组，新入站另起一条记录；缺失入站不能再写，删除时不碰面板。
func TestPanelRefReuse(t *testing.T) {
	f := newPanelFixture(t)
	ctx := context.Background()
	fake := f.fleet.get(fakeBase)
	seedInbound(fake, "1", 45904, "ss-old")
	srv, err := f.panels.CreateServer(ctx, f.actor, serverInput("hk-1"), nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	var oldID int64
	for id := range inboundsByID(t, f, srv.ID) {
		oldID = id
	}
	oldNode := directNodeOf(t, f, srv.ID, oldID)
	if _, err := f.panels.CreateNodeGroup(ctx, f.actor, NodeGroupInput{Name: "basic", NodeIDs: []int64{oldNode.ID}}, nil); err != nil {
		t.Fatalf("CreateNodeGroup: %v", err)
	}

	// 有人在面板上删了 SS，再从平台新建入站：面板复用了 id 1
	fake.RemoveInbound("1")
	fake.SetNextRef(1)
	created, err := f.panels.CreateInbound(ctx, f.actor, srv.ID, InboundCreateInput{Remark: "new-vmess", Port: 2000, Enabled: true, Template: inboundtpl.VmessWS}, nil)
	if err != nil {
		t.Fatalf("CreateInbound: %v", err)
	}
	if created.ID == oldID || created.RemoteRef != "1" {
		t.Fatalf("new inbound must get its own record: created = %+v, old id %d", created, oldID)
	}
	all := inboundsByID(t, f, srv.ID)
	if old := all[oldID]; !old.IsMissing || old.Remark != "ss-old" || old.Protocol != "vless" {
		t.Fatalf("stale record must be kept as missing, unchanged: %+v", old)
	}
	groups, err := f.panels.ListNodeGroups(ctx)
	if err != nil || len(groups) != 1 || len(groups[0].NodeIDs) != 1 || groups[0].NodeIDs[0] != oldNode.ID {
		t.Fatalf("group membership must stay on the old node: %+v, %v", groups, err)
	}
	if directNodeOf(t, f, srv.ID, created.ID).ID == oldNode.ID {
		t.Fatal("new inbound must get a new direct node")
	}

	// 缺失入站只能删除：写操作返回 409，删除不调用面板（否则会删掉占用同一 id 的新入站）
	if _, err := f.panels.UpdateInbound(ctx, f.actor, oldID, InboundUpdateInput{Remark: "x", Port: 45904}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("update missing inbound err = %v", err)
	}
	if _, err := f.panels.SetInboundEnabled(ctx, f.actor, oldID, false, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("enable missing inbound err = %v", err)
	}
	if err := f.panels.AckInboundDrift(ctx, f.actor, oldID, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("ack drift on missing inbound err = %v", err)
	}
	if err := f.panels.CreateEndpoints(ctx, f.actor, srv.ID, EndpointInput{InboundIDs: []int64{oldID}, Addresses: []string{"cdn.example.com"}}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("endpoint on missing inbound err = %v", err)
	}
	if err := f.panels.DeleteInbound(ctx, f.actor, oldID, nil); err != nil {
		t.Fatalf("DeleteInbound missing: %v", err)
	}
	if _, err := fake.GetInbound(ctx, "1"); err != nil {
		t.Fatalf("deleting the missing record must not touch the panel inbound now holding id 1: %v", err)
	}

	// 面板侧发生同样的事（两次同步之间删了再建，协议不同）：同步按“旧的缺失 + 新的新增”处理
	fake.RemoveInbound("1")
	seedInbound(fake, "1", 3000, "made-on-panel")
	res, err := f.panels.SyncServer(ctx, f.actor, srv.ID, nil)
	if err != nil || res.Added != 1 || res.Missing != 1 {
		t.Fatalf("sync after reuse = %+v, %v", res, err)
	}
	all = inboundsByID(t, f, srv.ID)
	if !all[created.ID].IsMissing || all[created.ID].Remark != "new-vmess" || len(all) != 2 {
		t.Fatalf("after sync = %+v", all)
	}

	// 缺失是终态：同一标识再出现，也是新记录
	fake.RemoveInbound("1")
	if _, err := f.panels.SyncServer(ctx, f.actor, srv.ID, nil); err != nil {
		t.Fatalf("SyncServer: %v", err)
	}
	seedInbound(fake, "1", 3000, "made-on-panel")
	if res, err := f.panels.SyncServer(ctx, f.actor, srv.ID, nil); err != nil || res.Added != 1 {
		t.Fatalf("reappearing ref must be added as new: %+v, %v", res, err)
	}
	if all = inboundsByID(t, f, srv.ID); len(all) != 3 {
		t.Fatalf("inbounds = %+v", all)
	}
}

// TestDeleteEndpointRemovesOnlyThatAddress 覆盖 docs/03 §5.1 删除对外地址：2 个入站 × 2 个地址的组里删一个，
// 其余 3 个地址的记录、节点与节点组成员关系都不变，面板组被拆分后再同步也不产生缺失。
func TestDeleteEndpointRemovesOnlyThatAddress(t *testing.T) {
	f := newPanelFixture(t)
	ctx := context.Background()
	fake := f.fleet.get(fakeBase)
	seedInbound(fake, "1", 443, "a")
	seedInbound(fake, "2", 8443, "b")
	srv, err := f.panels.CreateServer(ctx, f.actor, serverInput("hk-1"), nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	ids := make([]int64, 0, 2)
	for id := range inboundsByID(t, f, srv.ID) {
		ids = append(ids, id)
	}
	if err := f.panels.CreateEndpoints(ctx, f.actor, srv.ID, EndpointInput{InboundIDs: ids, Addresses: []string{"cdn-a.example.com", "cdn-b.example.com"}, Port: 443}, nil); err != nil {
		t.Fatalf("CreateEndpoints: %v", err)
	}
	endpointNodes := func() map[int64]Node {
		nodes, err := f.panels.ListNodes(ctx, NodeFilter{ServerID: srv.ID}, PageQuery{})
		if err != nil {
			t.Fatalf("ListNodes: %v", err)
		}
		out := map[int64]Node{}
		for _, n := range nodes.Items {
			if n.EndpointID != nil {
				out[n.ID] = n
			}
		}
		return out
	}
	before := endpointNodes()
	if len(before) != 4 {
		t.Fatalf("endpoint nodes = %+v", before)
	}
	nodeIDs := make([]int64, 0, len(before))
	var target Node
	for id, n := range before {
		nodeIDs = append(nodeIDs, id)
		if n.InboundID == ids[0] && n.Address == "cdn-a.example.com" {
			target = n
		}
	}
	if _, err := f.panels.CreateNodeGroup(ctx, f.actor, NodeGroupInput{Name: "cdn", NodeIDs: nodeIDs}, nil); err != nil {
		t.Fatalf("CreateNodeGroup: %v", err)
	}

	if err := f.panels.DeleteEndpoint(ctx, f.actor, *target.EndpointID, nil); err != nil {
		t.Fatalf("DeleteEndpoint: %v", err)
	}
	if eps, _ := fake.ListEndpoints(ctx); len(eps) != 3 {
		t.Fatalf("panel should keep 3 addresses: %+v", eps)
	}
	check := func(stage string) {
		after := endpointNodes()
		if len(after) != 3 {
			t.Fatalf("%s: endpoint nodes = %+v", stage, after)
		}
		for id, n := range after {
			if _, ok := before[id]; !ok || n.IsMissing || id == target.ID {
				t.Fatalf("%s: node %d = %+v (remaining nodes must keep their identity)", stage, id, n)
			}
		}
		groups, err := f.panels.ListNodeGroups(ctx)
		if err != nil || len(groups) != 1 || len(groups[0].NodeIDs) != 3 {
			t.Fatalf("%s: groups = %+v, %v", stage, groups, err)
		}
	}
	check("after delete")
	res, err := f.panels.SyncServer(ctx, f.actor, srv.ID, nil)
	if err != nil {
		t.Fatalf("SyncServer: %v", err)
	}
	check("after sync")
	if res.Missing != 0 {
		t.Fatalf("sync after split = %+v", res)
	}
}

// TestInboundPanelConfigAndKeys 覆盖 Ruling-16：编辑表单从面板读完整配置（记审计），密钥由面板生成；缺失入站不可读。
func TestInboundPanelConfigAndKeys(t *testing.T) {
	f := newPanelFixture(t)
	ctx := context.Background()
	fake := f.fleet.get(fakeBase)
	fake.PutInbound(panel.Inbound{
		Ref: "1", Protocol: "vless", Port: 443, Remark: "r", Enabled: true, CoreType: panel.CoreXray, Network: "tcp",
		Config: json.RawMessage(`{"streamSettings":{"realitySettings":{"privateKey":"PRIV"}}}`),
	})
	srv, err := f.panels.CreateServer(ctx, f.actor, serverInput("hk-1"), nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	var id int64
	for k := range inboundsByID(t, f, srv.ID) {
		id = k
	}
	cfg, err := f.panels.GetInboundPanelConfig(ctx, f.actor, id, nil)
	if err != nil || !bytes.Contains(cfg.Config, []byte("PRIV")) || cfg.Port != 443 {
		t.Fatalf("GetInboundPanelConfig = %+v, %v", cfg, err)
	}
	var reads int
	if err := f.pool.QueryRow(ctx, "SELECT COUNT(*) FROM audit_log WHERE action = $1 AND target_id = $2", ActionInboundConfig, fmt.Sprint(id)).Scan(&reads); err != nil || reads != 1 {
		t.Fatalf("config read must be audited: %d, %v", reads, err)
	}
	keys, err := f.panels.GeneratePanelKeys(ctx, f.actor, srv.ID, string(panel.KeyX25519), nil)
	if err != nil || string(keys) != paneltest.FakeX25519 {
		t.Fatalf("GeneratePanelKeys = %s, %v", keys, err)
	}
	if _, err := f.panels.GeneratePanelKeys(ctx, f.actor, srv.ID, "rsa", nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unknown kind err = %v", err)
	}
	fake.RemoveInbound("1")
	if _, err := f.panels.SyncServer(ctx, f.actor, srv.ID, nil); err != nil {
		t.Fatalf("SyncServer: %v", err)
	}
	if _, err := f.panels.GetInboundPanelConfig(ctx, f.actor, id, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing inbound config err = %v", err)
	}
}

func TestDisabledServerRejectsWrites(t *testing.T) {
	f := newPanelFixture(t)
	ctx := context.Background()
	srv, err := f.panels.CreateServer(ctx, f.actor, serverInput("hk-1"), nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	in := serverInput("hk-1")
	in.APIToken = ""
	in.IsDisabled = true
	if _, err := f.panels.UpdateServer(ctx, f.actor, srv.ID, in, nil); err != nil {
		t.Fatalf("disable server: %v", err)
	}
	if _, err := f.panels.CreateInbound(ctx, f.actor, srv.ID, InboundCreateInput{Remark: "x", Port: 1000, Template: inboundtpl.VmessWS}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("write on disabled server err = %v (T4)", err)
	}
	ids, err := f.panels.ListEnabledServerIDs(ctx)
	if err != nil || len(ids) != 0 {
		t.Fatalf("disabled server must be skipped by jobs: %v, %v", ids, err)
	}
	if f.fleet.tokens[fakeBase] != fakeToken {
		t.Fatal("empty token on update must keep the stored token")
	}
}

func TestNodeGroups(t *testing.T) {
	f := newPanelFixture(t)
	ctx := context.Background()
	fake := f.fleet.get(fakeBase)
	seedInbound(fake, "1", 443, "a")
	seedInbound(fake, "2", 8443, "b")
	srv, err := f.panels.CreateServer(ctx, f.actor, serverInput("hk-1"), nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	nodes, err := f.panels.ListNodes(ctx, NodeFilter{ServerID: srv.ID}, PageQuery{})
	if err != nil || nodes.Total != 2 {
		t.Fatalf("nodes = %+v, %v", nodes, err)
	}
	first := nodes.Items[0].ID
	if err := f.panels.UpdateNode(ctx, f.actor, first, NodeInput{DisplayName: "香港 01", Tags: []string{"premium", " "}, IsEnabled: true}, nil); err != nil {
		t.Fatalf("UpdateNode: %v", err)
	}
	g, err := f.panels.CreateNodeGroup(ctx, f.actor, NodeGroupInput{Name: "basic", NodeIDs: []int64{first, first}}, nil)
	if err != nil || len(g.NodeIDs) != 1 {
		t.Fatalf("CreateNodeGroup = %+v, %v", g, err)
	}
	if _, err := f.panels.CreateNodeGroup(ctx, f.actor, NodeGroupInput{Name: "bad", NodeIDs: []int64{99999}}, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unknown node err = %v", err)
	}
	inGroup, err := f.panels.ListNodes(ctx, NodeFilter{GroupID: g.ID}, PageQuery{})
	if err != nil || inGroup.Total != 1 || inGroup.Items[0].DisplayName != "香港 01" || len(inGroup.Items[0].Tags) != 1 {
		t.Fatalf("group nodes = %+v, %v", inGroup, err)
	}
	// 同步不覆盖管理员改过的展示属性
	if _, err := f.panels.SyncServer(ctx, f.actor, srv.ID, nil); err != nil {
		t.Fatalf("SyncServer: %v", err)
	}
	if again, _ := f.panels.ListNodes(ctx, NodeFilter{GroupID: g.ID}, PageQuery{}); again.Items[0].DisplayName != "香港 01" {
		t.Fatal("sync must not overwrite node display name")
	}
	if err := f.panels.UpdateNodeGroup(ctx, f.actor, g.ID, NodeGroupInput{Name: "basic", NodeIDs: []int64{}}, nil); err != nil {
		t.Fatalf("UpdateNodeGroup: %v", err)
	}
	if err := f.panels.DeleteNodeGroup(ctx, f.actor, g.ID, nil); err != nil {
		t.Fatalf("DeleteNodeGroup: %v", err)
	}
	if err := f.panels.DeleteNodeGroup(ctx, f.actor, g.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice err = %v", err)
	}
}

// displayNamesByNode 取服务器下各节点的显示名（按节点 id）。
func displayNamesByNode(t *testing.T, f *panelFixture, serverID int64) map[int64]string {
	t.Helper()
	nodes, err := f.panels.ListNodes(context.Background(), NodeFilter{ServerID: serverID}, PageQuery{PageSize: maxPageSize})
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	out := make(map[int64]string, len(nodes.Items))
	for _, n := range nodes.Items {
		out[n.ID] = n.DisplayName
	}
	return out
}

// TestAffixNodeNames：批量给节点显示名加前缀 / 后缀（Ruling-80）；不合规或会超长时整批拒绝。
func TestAffixNodeNames(t *testing.T) {
	f := newPanelFixture(t)
	ctx := context.Background()
	fake := f.fleet.get(fakeBase)
	seedInbound(fake, "1", 443, "a")
	seedInbound(fake, "2", 8443, "b")
	srv, err := f.panels.CreateServer(ctx, f.actor, serverInput("hk-1"), nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	nodes, err := f.panels.ListNodes(ctx, NodeFilter{ServerID: srv.ID}, PageQuery{})
	if err != nil || nodes.Total != 2 {
		t.Fatalf("nodes = %+v, %v", nodes, err)
	}
	first, second := nodes.Items[0].ID, nodes.Items[1].ID

	// 前缀开头、后缀结尾的空白去掉，中间的分隔空格保留；重复的 id 只算一次；没选中的节点不动
	updated, err := f.panels.AffixNodeNames(ctx, f.actor, NodeAffixInput{NodeIDs: []int64{first, first}, Prefix: "  香港 ", Suffix: " | 01 "}, nil)
	if err != nil || updated != 1 {
		t.Fatalf("AffixNodeNames = %d, %v", updated, err)
	}
	names := displayNamesByNode(t, f, srv.ID)
	if names[first] != "香港 a | 01" || names[second] != "b" {
		t.Fatalf("names = %v", names)
	}
	// 只填后缀；同步不覆盖改过的显示名
	if _, err := f.panels.AffixNodeNames(ctx, f.actor, NodeAffixInput{NodeIDs: []int64{first, second}, Suffix: "-x"}, nil); err != nil {
		t.Fatalf("AffixNodeNames suffix only: %v", err)
	}
	if _, err := f.panels.SyncServer(ctx, f.actor, srv.ID, nil); err != nil {
		t.Fatalf("SyncServer: %v", err)
	}
	names = displayNamesByNode(t, f, srv.ID)
	if names[first] != "香港 a | 01-x" || names[second] != "b-x" {
		t.Fatalf("names after suffix = %v", names)
	}

	tooMany := make([]int64, maxAffixNodes+1)
	for i := range tooMany {
		tooMany[i] = int64(i + 1)
	}
	bad := []NodeAffixInput{
		{NodeIDs: []int64{first}},
		{NodeIDs: []int64{first}, Prefix: "  ", Suffix: " "},
		{Prefix: "x"},
		{NodeIDs: tooMany, Prefix: "x"},
		{NodeIDs: []int64{first, 999999}, Prefix: "x"},
		{NodeIDs: []int64{first}, Prefix: strings.Repeat("长", maxNodeAffixLen+1)},
		{NodeIDs: []int64{first}, Suffix: "a\nb"},
	}
	for i, in := range bad {
		if _, err := f.panels.AffixNodeNames(ctx, f.actor, in, nil); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("case %d err = %v", i, err)
		}
	}
	// 有一个节点加上后超长：整批拒绝，另一个也不改
	long := strings.Repeat("节", maxNodeNameLen-2)
	if err := f.panels.UpdateNode(ctx, f.actor, second, NodeInput{DisplayName: long, IsEnabled: true}, nil); err != nil {
		t.Fatalf("UpdateNode: %v", err)
	}
	if _, err := f.panels.AffixNodeNames(ctx, f.actor, NodeAffixInput{NodeIDs: []int64{first, second}, Prefix: "abc"}, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("overflow err = %v", err)
	}
	names = displayNamesByNode(t, f, srv.ID)
	if names[first] != "香港 a | 01-x" || names[second] != long {
		t.Fatalf("names after rejected batch = %v", names)
	}
	// 刚好到上限可以
	if _, err := f.panels.AffixNodeNames(ctx, f.actor, NodeAffixInput{NodeIDs: []int64{second}, Prefix: "ab"}, nil); err != nil {
		t.Fatalf("AffixNodeNames at limit: %v", err)
	}
	if names = displayNamesByNode(t, f, srv.ID); names[second] != "ab"+long {
		t.Fatalf("name at limit = %q", names[second])
	}
}

// TestPanelWebCertFiles：取面板自己的网页证书路径（Ruling-53）；没配时是两个空串，服务器不存在 404，面板连不上 502 类错误。
func TestPanelWebCertFiles(t *testing.T) {
	f := newPanelFixture(t)
	ctx := context.Background()
	fake := f.fleet.get(fakeBase)
	srv, err := f.panels.CreateServer(ctx, f.actor, serverInput("hk-1"), nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	if files, err := f.panels.WebCertFiles(ctx, srv.ID); err != nil || files != (panel.CertFiles{}) {
		t.Fatalf("no cert configured: %+v, %v", files, err)
	}
	want := panel.CertFiles{CertFile: "/root/cert/fullchain.pem", KeyFile: "/root/cert/privkey.pem"}
	fake.SetWebCert(want)
	if files, err := f.panels.WebCertFiles(ctx, srv.ID); err != nil || files != want {
		t.Fatalf("WebCertFiles = %+v, %v", files, err)
	}
	if _, err := f.panels.WebCertFiles(ctx, srv.ID+999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing server: err = %v, want ErrNotFound", err)
	}
	fake.FailWith(panel.ErrUnreachable)
	if _, err := f.panels.WebCertFiles(ctx, srv.ID); !errors.Is(err, ErrUpstream) {
		t.Errorf("unreachable panel: err = %v, want ErrUpstream", err)
	}
}

// TestPanelRealityScan：REALITY 目标扫描（Ruling-54）——目标形状校验（不收网段与路径）、数量上限、结果透传、面板不可达。
func TestPanelRealityScan(t *testing.T) {
	f := newPanelFixture(t)
	ctx := context.Background()
	fake := f.fleet.get(fakeBase)
	srv, err := f.panels.CreateServer(ctx, f.actor, serverInput("hk-1"), nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	const good = "www.example.com:443"
	fake.SetRealityScan(panel.RealityScan{Target: good, Host: "www.example.com", Port: 443, Feasible: true, LatencyMs: 42, ServerNames: []string{"www.example.com"}})

	res, err := f.panels.ScanRealityTarget(ctx, srv.ID, " "+good+" ", "www.example.com", 0)
	if err != nil || !res.Feasible || res.LatencyMs != 42 || res.Target != good {
		t.Fatalf("ScanRealityTarget = %+v, %v", res, err)
	}
	// 不可用的目标照常返回结果
	if res, err := f.panels.ScanRealityTarget(ctx, srv.ID, "plain.example.net", "", 1); err != nil || res.Feasible || res.Reason == "" {
		t.Fatalf("infeasible target = %+v, %v", res, err)
	}
	before := len(fake.RealityScanTargets())
	for what, target := range map[string]string{
		"empty": "", "cidr": "104.16.0.0/24", "url": "https://www.example.com", "path": "www.example.com/x", "space": "a b.example.com",
		"bad port": "www.example.com:70000", "comma list": "a.example.com,b.example.com", "too long": strings.Repeat("a", 256),
	} {
		if _, err := f.panels.ScanRealityTarget(ctx, srv.ID, target, "", 0); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("target %s: err = %v, want ErrInvalidArgument", what, err)
		}
	}
	if _, err := f.panels.ScanRealityTarget(ctx, srv.ID, good, "not a host", 0); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("bad sni: err = %v, want ErrInvalidArgument", err)
	}
	if _, err := f.panels.ScanRealityTarget(ctx, srv.ID, good, "", 3); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("bad xver: err = %v, want ErrInvalidArgument", err)
	}
	if _, err := f.panels.ScanRealityTargets(ctx, srv.ID, []string{good, "10.0.0.0/8"}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("cidr in list: err = %v, want ErrInvalidArgument", err)
	}
	if _, err := f.panels.ScanRealityTargets(ctx, srv.ID, make([]string, maxRealityTargets+1)); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("too many targets: err = %v, want ErrInvalidArgument", err)
	}
	if got := len(fake.RealityScanTargets()); got != before {
		t.Fatalf("rejected scans reached the panel: %d calls", got-before)
	}

	// 不带目标：探测面板自己的候选；带目标：原样交给面板（IPv6 带方括号也接受）
	list, err := f.panels.ScanRealityTargets(ctx, srv.ID, nil)
	if err != nil || len(list) != 1 || list[0].Target != paneltest.FakeRealityCandidate {
		t.Fatalf("candidate scan = %+v, %v", list, err)
	}
	list, err = f.panels.ScanRealityTargets(ctx, srv.ID, []string{good, "203.0.114.9", "[2606:4700::1111]:443"})
	if err != nil || len(list) != 3 || !list[0].Feasible {
		t.Fatalf("custom scan = %+v, %v", list, err)
	}
	if _, err := f.panels.ScanRealityTarget(ctx, srv.ID+999, good, "", 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing server: err = %v, want ErrNotFound", err)
	}
	fake.FailWith(panel.ErrUnreachable)
	if _, err := f.panels.ScanRealityTargets(ctx, srv.ID, nil); !errors.Is(err, ErrUpstream) {
		t.Errorf("unreachable panel: err = %v, want ErrUpstream", err)
	}
}
