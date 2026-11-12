package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	if err := f.panels.UpdateNode(ctx, f.actor, first, NodeInput{DisplayName: "香港 01", Region: "HK", Tags: []string{"premium", " "}, IsEnabled: true}, nil); err != nil {
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
