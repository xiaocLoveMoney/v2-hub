package service

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"v2-hub-server/internal/manager/deploy"
	"v2-hub-server/internal/manager/inboundtpl"
	"v2-hub-server/internal/manager/panel"
	"v2-hub-server/internal/manager/panel/paneltest"
)

const (
	presetPortBase = 41001
	testCertPath   = "/root/cert/node-a.example.com/fullchain.pem"
	testKeyPath    = "/root/cert/node-a.example.com/privkey.pem"
)

// presetFixture 是一台装好面板、带 DNS 账号与公开证书、已按域名接入的主机。
type presetFixture struct {
	*hostFixture
	host     ServerHost
	serverID int64
	fake     *paneltest.Fake
}

func newPresetFixture(t *testing.T, withDNS bool) *presetFixture {
	t.Helper()
	f := newHostFixture(t)
	f.freshInstall()
	var host ServerHost
	if withDNS {
		host = f.createHostWithDNS(t, hostAddrA, "node-a", f.createDNSAccount(t).ID)
	} else {
		host = f.createHost(t, hostAddrA, "node-a")
	}
	wantTask(t, f.runNext(t), HostTaskSucceeded, deploy.ReasonNone)
	f.installed()
	got, err := f.hosts.GetHost(context.Background(), host.ID)
	if err != nil || got.ServerID == nil {
		t.Fatalf("host after provision = %+v, %v", got, err)
	}
	srv, err := f.panels.GetServer(context.Background(), *got.ServerID)
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	fake := f.fleet.get(srv.BaseURL)
	fake.SetRealityScan(panel.RealityScan{Target: paneltest.FakeRealityCandidate, Host: "www.example.org", Port: 443, Feasible: true, LatencyMs: 12})
	fake.SetWebCert(panel.CertFiles{CertFile: testCertPath, KeyFile: testKeyPath})
	return &presetFixture{hostFixture: f, host: *got, serverID: srv.ID, fake: fake}
}

func allPresetItems() []InboundPresetItem {
	items := make([]InboundPresetItem, 0, len(inboundtpl.Presets))
	for i, preset := range inboundtpl.Presets {
		items = append(items, InboundPresetItem{Key: preset.Key, Port: presetPortBase + i})
	}
	return items
}

func (f *presetFixture) deploy(t *testing.T, in InboundPresetDeployInput) *HostTaskDetail {
	t.Helper()
	if _, err := f.hosts.DeployInboundPresets(context.Background(), f.actor, f.serverID, in, nil); err != nil {
		t.Fatalf("DeployInboundPresets: %v", err)
	}
	return f.runNext(t)
}

func (f *presetFixture) inboundsByPort(t *testing.T) map[int]Inbound {
	t.Helper()
	list, err := f.panels.ListServerInbounds(context.Background(), f.serverID)
	if err != nil {
		t.Fatalf("ListServerInbounds: %v", err)
	}
	out := make(map[int]Inbound, len(list))
	for _, in := range list {
		out[in.Port] = in
	}
	return out
}

// panelConfig 取面板上该端口入站的完整配置（假面板不删敏感字段）。
func (f *presetFixture) panelConfig(t *testing.T, port int) json.RawMessage {
	t.Helper()
	in, ok := f.inboundsByPort(t)[port]
	if !ok {
		t.Fatalf("no inbound on port %d", port)
	}
	got, err := f.fake.GetInbound(context.Background(), in.RemoteRef)
	if err != nil {
		t.Fatalf("GetInbound: %v", err)
	}
	return got.Config
}

func TestPreviewInboundPresets(t *testing.T) {
	f := newPresetFixture(t, true)
	ctx := context.Background()
	f.deployer.Listening = []deploy.Port{{Number: 22}, {Number: 443}}
	preview, err := f.hosts.PreviewInboundPresets(ctx, f.actor, f.serverID)
	if err != nil {
		t.Fatalf("PreviewInboundPresets: %v", err)
	}
	if preview.HostID != f.host.ID || preview.Domain != domainNodeA || !preview.CertReady || len(preview.Items) != 7 {
		t.Fatalf("preview = %+v", preview)
	}
	ports := make(map[int]bool, 7)
	for _, it := range preview.Items {
		if !it.Available || it.Port < presetPortMin || it.Port > presetPortMax || ports[it.Port] {
			t.Fatalf("item = %+v", it)
		}
		ports[it.Port] = true
		if it.NeedsCert != presetOf(InboundPresetItem{Key: it.Key}).NeedsCert {
			t.Fatalf("needsCert of %s", it.Key)
		}
	}

	// 没有 DNS 账号：要证书的四项不可用
	plain := newPresetFixture(t, false)
	preview, err = plain.hosts.PreviewInboundPresets(ctx, plain.actor, plain.serverID)
	if err != nil {
		t.Fatalf("PreviewInboundPresets: %v", err)
	}
	for _, it := range preview.Items {
		if it.Available == presetOf(InboundPresetItem{Key: it.Key}).NeedsCert || (!it.Available && it.Reason != msgPresetNoDNS) {
			t.Fatalf("item without dns = %+v", it)
		}
	}
	// 不能部署要证书的预设
	_, err = plain.hosts.DeployInboundPresets(ctx, plain.actor, plain.serverID, InboundPresetDeployInput{Items: []InboundPresetItem{{Key: inboundtpl.PresetTrojanTLS, Port: presetPortBase}}}, nil)
	wantCode(t, err, CodeInvalidArgument)
}

func TestDeployInboundPresetsValidation(t *testing.T) {
	f := newPresetFixture(t, true)
	ctx := context.Background()
	bad := []InboundPresetDeployInput{
		{},
		{Items: []InboundPresetItem{{Key: "nope", Port: presetPortBase}}},
		{Items: []InboundPresetItem{{Key: inboundtpl.PresetTrojanTLS, Port: 0}}},
		{Items: []InboundPresetItem{{Key: inboundtpl.PresetTrojanTLS, Port: presetPortBase}, {Key: inboundtpl.PresetVMessWSTLS, Port: presetPortBase}}},
		{Items: []InboundPresetItem{{Key: inboundtpl.PresetTrojanTLS, Port: presetPortBase}, {Key: inboundtpl.PresetTrojanTLS, Port: presetPortBase + 1}}},
		{Items: []InboundPresetItem{{Key: inboundtpl.PresetTrojanTLS, Port: presetPortBase}}, NodeGroupIDs: []int64{999999}},
		{Items: []InboundPresetItem{{Key: inboundtpl.PresetTrojanTLS, Port: presetPortBase}}, NamePrefix: strings.Repeat("x", maxNodeAffixLen+1)},
		{Items: []InboundPresetItem{{Key: inboundtpl.PresetTrojanTLS, Port: presetPortBase}}, NameSuffix: "a\tb"},
	}
	for i, in := range bad {
		_, err := f.hosts.DeployInboundPresets(ctx, f.actor, f.serverID, in, nil)
		if err == nil {
			t.Fatalf("case %d must be rejected", i)
		}
		wantCode(t, err, CodeInvalidArgument)
	}
	// 服务器没绑 SSH：409
	srv, err := f.panels.CreateServer(ctx, f.actor, serverInput("plain-server"), nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	_, err = f.hosts.PreviewInboundPresets(ctx, f.actor, srv.ID)
	wantCode(t, err, CodeConflict)
	_, err = f.hosts.DeployInboundPresets(ctx, f.actor, srv.ID, InboundPresetDeployInput{Items: allPresetItems()[:1]}, nil)
	wantCode(t, err, CodeConflict)
}

func TestDeployAllInboundPresets(t *testing.T) {
	f := newPresetFixture(t, true)
	ctx := context.Background()
	// 面板上先有一个入站，节点组里先有它的直连节点：部署后原有成员要保留
	seedInbound(f.fake, "900", 2096, "existing")
	if _, err := f.panels.SyncServer(ctx, f.actor, f.serverID, nil); err != nil {
		t.Fatalf("SyncServer: %v", err)
	}
	before := f.inboundsByPort(t)
	nodes, err := f.panels.ListNodes(ctx, NodeFilter{ServerID: f.serverID}, PageQuery{PageSize: 100})
	if err != nil || nodes.Total == 0 {
		t.Fatalf("nodes = %+v, %v", nodes, err)
	}
	group, err := f.panels.CreateNodeGroup(ctx, f.actor, NodeGroupInput{Name: "all", NodeIDs: []int64{nodes.Items[0].ID}}, nil)
	if err != nil {
		t.Fatalf("CreateNodeGroup: %v", err)
	}
	reconcilesBefore := f.reconciles.n.Load()

	task := f.deploy(t, InboundPresetDeployInput{Items: allPresetItems(), NodeGroupIDs: []int64{group.ID}})
	wantTask(t, task, HostTaskSucceeded, deploy.ReasonNone)
	after := f.inboundsByPort(t)
	if len(after) != len(before)+7 {
		t.Fatalf("want 7 new inbounds, got %d → %d\n%s", len(before), len(after), task.Log)
	}
	wantProto := map[string]string{
		inboundtpl.PresetVLESSRealityVision: "vless", inboundtpl.PresetVLESSRealityMLKEM: "vless", inboundtpl.PresetVLESSXHTTPTLS: "vless",
		inboundtpl.PresetVMessWSTLS: "vmess", inboundtpl.PresetTrojanTLS: "trojan", inboundtpl.PresetShadowsocks2022: "shadowsocks",
		inboundtpl.PresetHysteria2Salamander: "hysteria",
	}
	for _, it := range allPresetItems() {
		in, ok := after[it.Port]
		if !ok || in.Protocol != wantProto[it.Key] || in.Remark != presetOf(it).Remark {
			t.Fatalf("inbound for %s = %+v", it.Key, in)
		}
	}
	// TLS 入站引用主机证书；REALITY 用扫描出的目标
	raw := f.panelConfig(t, presetPortBase+4)
	if !strings.Contains(string(raw), testCertPath) {
		t.Fatalf("trojan inbound must reference the host certificate: %s", raw)
	}
	raw = f.panelConfig(t, presetPortBase)
	if !strings.Contains(string(raw), paneltest.FakeRealityCandidate) {
		t.Fatalf("reality inbound must use the scanned target: %s", raw)
	}
	raw = f.panelConfig(t, presetPortBase+1)
	if !strings.Contains(string(raw), "fake-pq") || strings.Contains(string(raw), "fake-x\"") {
		t.Fatalf("mlkem inbound must use the ML-KEM-768 pair: %s", raw)
	}
	// 放行防火墙：Hysteria2 放 UDP，SS 两种都放
	opened := f.deployer.OpenedPorts()
	if len(opened) == 0 {
		t.Fatal("firewall not opened")
	}
	last := opened[len(opened)-1]
	for _, want := range []deploy.Port{{Number: presetPortBase + 6, UDP: true}, {Number: presetPortBase + 5}, {Number: presetPortBase + 5, UDP: true}, {Number: presetPortBase}} {
		if !slices.Contains(last, want) {
			t.Fatalf("firewall misses %+v: %+v", want, last)
		}
	}
	if slices.Contains(last, deploy.Port{Number: presetPortBase + 6}) {
		t.Fatal("hysteria2 must not open tcp")
	}
	// VLESS 流控打开；节点组带上 7 个新直连节点与原有成员；触发对账
	srv, err := f.panels.GetServer(ctx, f.serverID)
	if err != nil || srv.ClientFlow != ClientFlowVision {
		t.Fatalf("server flow = %+v, %v", srv, err)
	}
	groups, err := f.panels.ListNodeGroups(ctx)
	if err != nil {
		t.Fatalf("ListNodeGroups: %v", err)
	}
	if g := groups[slices.IndexFunc(groups, func(g NodeGroup) bool { return g.ID == group.ID })]; len(g.NodeIDs) != 8 || !slices.Contains(g.NodeIDs, nodes.Items[0].ID) {
		t.Fatalf("group members = %v", g.NodeIDs)
	}
	if f.reconciles.n.Load() <= reconcilesBefore {
		t.Fatal("deploy must request a reconcile")
	}
	if !strings.Contains(task.Log, "✓ Hysteria2 端口") || task.Step != hostStepNodeGroups {
		t.Fatalf("log / step: %s\n%s", task.Step, task.Log)
	}

	// 再部署一次同样的预设：端口已被面板上的入站占着，一个都不建
	again := f.deploy(t, InboundPresetDeployInput{Items: allPresetItems()[:2]})
	wantTask(t, again, HostTaskFailed, deploy.ReasonPortInUse)
	if len(f.inboundsByPort(t)) != len(after) {
		t.Fatal("nothing may be created when a port is busy")
	}
}

// TestDeployInboundPresetsAffixesNodeNames：填了前后缀时新入站的直连节点显示名带上它们，入站备注与原有节点不变（Ruling-80）。
func TestDeployInboundPresetsAffixesNodeNames(t *testing.T) {
	f := newPresetFixture(t, true)
	ctx := context.Background()
	seedInbound(f.fake, "900", 2096, "existing")
	if _, err := f.panels.SyncServer(ctx, f.actor, f.serverID, nil); err != nil {
		t.Fatalf("SyncServer: %v", err)
	}
	items := allPresetItems()[:2]
	task := f.deploy(t, InboundPresetDeployInput{Items: items, NamePrefix: " 香港 ", NameSuffix: " | 01 "})
	wantTask(t, task, HostTaskSucceeded, deploy.ReasonNone)
	if !strings.Contains(task.Log, "前缀「香港 」、后缀「 | 01」") {
		t.Fatalf("log must mention the affix:\n%s", task.Log)
	}
	inbounds := f.inboundsByPort(t)
	nodes, err := f.panels.ListNodes(ctx, NodeFilter{ServerID: f.serverID}, PageQuery{PageSize: maxPageSize})
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	names := make(map[int64]string, len(nodes.Items))
	for _, n := range nodes.Items {
		names[n.InboundID] = n.DisplayName
	}
	for _, it := range items {
		in := inbounds[it.Port]
		if remark := presetOf(it).Remark; in.Remark != remark || names[in.ID] != "香港 "+remark+" | 01" {
			t.Fatalf("%s: remark %q, node name %q", it.Key, in.Remark, names[in.ID])
		}
	}
	if got := names[inbounds[2096].ID]; got != "existing" {
		t.Fatalf("existing node renamed to %q", got)
	}
}

func TestDeployInboundPresetsRefusesHostPorts(t *testing.T) {
	f := newPresetFixture(t, true)
	f.deployer.Listening = []deploy.Port{{Number: presetPortBase + 6, UDP: true}}
	before := len(f.inboundsByPort(t))
	task := f.deploy(t, InboundPresetDeployInput{Items: allPresetItems()})
	wantTask(t, task, HostTaskFailed, deploy.ReasonPortInUse)
	if !strings.Contains(task.LastError, "41007/udp") || len(f.inboundsByPort(t)) != before {
		t.Fatalf("lastError = %q", task.LastError)
	}
	// 同一端口只被占了 TCP：只用 UDP 的 Hysteria2 照样能建
	f.deployer.Listening = []deploy.Port{{Number: presetPortBase + 6}}
	task = f.deploy(t, InboundPresetDeployInput{Items: allPresetItems()[6:]})
	wantTask(t, task, HostTaskSucceeded, deploy.ReasonNone)
}

func TestDeployInboundPresetsPartialFailure(t *testing.T) {
	f := newPresetFixture(t, true)
	f.fake.SetRealityScan(panel.RealityScan{Target: paneltest.FakeRealityCandidate, Reason: "no tls 1.3"})
	task := f.deploy(t, InboundPresetDeployInput{Items: allPresetItems()})
	wantTask(t, task, HostTaskFailed, deploy.ReasonCommandFailed)
	if !strings.Contains(task.LastError, "7 项里有 2 项没建成") || !strings.Contains(task.LastError, msgNoRealityTarget) {
		t.Fatalf("lastError = %q", task.LastError)
	}
	after := f.inboundsByPort(t)
	if _, ok := after[presetPortBase]; ok {
		t.Fatal("reality must not be created without a target")
	}
	if _, ok := after[presetPortBase+4]; !ok {
		t.Fatal("other presets must still be created")
	}
	if srv, _ := f.panels.GetServer(context.Background(), f.serverID); srv.ClientFlow != "" {
		t.Fatal("vision must stay off when no REALITY inbound was created")
	}
}

func TestDeployInboundPresetsXrayDown(t *testing.T) {
	f := newPresetFixture(t, true)
	group, err := f.panels.CreateNodeGroup(context.Background(), f.actor, NodeGroupInput{Name: "g"}, nil)
	if err != nil {
		t.Fatalf("CreateNodeGroup: %v", err)
	}
	f.fake.SetCoreState("error", "listen tcp :41001: bind: address already in use")
	task := f.deploy(t, InboundPresetDeployInput{Items: allPresetItems()[:1], NodeGroupIDs: []int64{group.ID}})
	wantTask(t, task, HostTaskFailed, deploy.ReasonXrayDown)
	if !strings.Contains(task.LastError, "address already in use") {
		t.Fatalf("lastError = %q", task.LastError)
	}
	groups, _ := f.panels.ListNodeGroups(context.Background())
	if g := groups[slices.IndexFunc(groups, func(g NodeGroup) bool { return g.ID == group.ID })]; len(g.NodeIDs) != 0 {
		t.Fatal("broken nodes must not be handed to users")
	}
}

func TestDeployInboundPresetsIssuesCertFirst(t *testing.T) {
	f := newHostFixture(t)
	f.freshInstall()
	host := f.createHostWithDNS(t, hostAddrA, "node-a", f.createDNSAccount(t).ID)
	// 部署时证书没签成：服务器按自签证书接入，主机还没记下证书
	f.deployer.Errors["IssueCert"] = deploy.NewError(deploy.ReasonCertFailed, "限流")
	wantTask(t, f.runNext(t), HostTaskFailed, deploy.ReasonCertFailed)
	f.deployer.Errors["IssueCert"] = nil
	f.installed()
	got, _ := f.hosts.GetHost(context.Background(), host.ID)
	srv, _ := f.panels.GetServer(context.Background(), *got.ServerID)
	fake := f.fleet.get(srv.BaseURL)
	fake.SetWebCert(panel.CertFiles{CertFile: testCertPath, KeyFile: testKeyPath})
	// 证书装好后服务器换成按域名接入，那台面板也要有证书路径
	f.fleet.get(urlDomainNodeA).SetWebCert(panel.CertFiles{CertFile: testCertPath, KeyFile: testKeyPath})

	issuedBefore := len(f.deployer.CertRequests())
	if _, err := f.hosts.DeployInboundPresets(context.Background(), f.actor, srv.ID, InboundPresetDeployInput{Items: []InboundPresetItem{{Key: inboundtpl.PresetTrojanTLS, Port: presetPortBase}}}, nil); err != nil {
		t.Fatalf("DeployInboundPresets: %v", err)
	}
	task := f.runNext(t)
	wantTask(t, task, HostTaskSucceeded, deploy.ReasonNone)
	if len(f.deployer.CertRequests()) != issuedBefore+1 || !strings.Contains(task.Log, "先配置证书") {
		t.Fatalf("certificate must be issued first:\n%s", task.Log)
	}
	if got, _ := f.hosts.GetHost(context.Background(), host.ID); got.CertNotAfter == nil {
		t.Fatal("host must have a certificate afterwards")
	}
}
