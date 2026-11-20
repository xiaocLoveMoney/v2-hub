package xui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"v2-hub-server/internal/manager/panel"
)

const testToken = "test-token-value"

// fakePanel 按 3x-ui v3.11.0 的真实响应形状（本地容器实测）模拟面板 API。
type fakePanel struct {
	mu        sync.Mutex
	inbounds  map[int64]map[string]any
	hosts     []map[string]any // Host 组对象，形状同 /hosts/get
	clients   map[string]map[string]any
	lastBody  map[string]any
	nextID    int64
	nextGroup int
	status    int // 非 0 时所有请求返回该状态码
}

func newFakePanel() *fakePanel {
	return &fakePanel{inbounds: map[int64]map[string]any{}, clients: map[string]map[string]any{}, nextID: 1}
}

func (f *fakePanel) reply(w http.ResponseWriter, success bool, msg string, obj any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"success": success, "msg": msg, "obj": obj}); err != nil {
		panic(err)
	}
}

func (f *fakePanel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/base/panel/api")
	var body map[string]any
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	idFrom := func(prefix string) int64 {
		id, _ := strconv.ParseInt(strings.TrimPrefix(path, prefix), 10, 64)
		return id
	}
	switch {
	case path == "/server/status":
		f.reply(w, true, "", map[string]any{"panelVersion": "3.11.0", "panelGuid": "guid-1", "cpu": 3.2, "cpuCores": 2,
			"mem": map[string]any{"current": 300, "total": 1000}, "swap": map[string]any{"current": 1, "total": 2},
			"disk": map[string]any{"current": 30, "total": 200}, "uptime": 86400, "loads": []float64{0.5, 0.3},
			"tcpCount": 42, "udpCount": 7, "netIO": map[string]any{"up": 2900, "down": 2950},
			"netTraffic": map[string]any{"sent": 833, "recv": 895},
			"publicIP":   map[string]any{"ipv4": "203.0.113.1", "ipv6": "N/A"},
			"xray":       map[string]any{"state": "error", "errorMsg": "listen tcp :443: bind: address already in use", "version": "26.10.21"}})
	case path == "/clients/list":
		rows := make([]map[string]any, 0, len(f.clients))
		for _, c := range f.clients {
			rows = append(rows, c)
		}
		f.reply(w, true, "", rows)
	case path == "/clients/add":
		f.lastBody = body
		cl := body["client"].(map[string]any)
		cl["inboundIds"] = body["inboundIds"]
		cl["traffic"] = map[string]any{"up": 0, "down": 0}
		f.clients[cl["email"].(string)] = cl
		f.reply(w, true, "Inbound client(s) have been added.", nil)
	case strings.HasPrefix(path, "/clients/update/"):
		cl := f.clients[strings.TrimPrefix(path, "/clients/update/")]
		cl["limitIp"], cl["enable"], cl["flow"] = body["limitIp"], body["enable"], body["flow"]
		if id, ok := body["id"]; ok {
			cl["id"], cl["password"] = id, body["password"]
		}
		f.reply(w, true, "", nil)
	case strings.HasPrefix(path, "/clients/links/"):
		cl, ok := f.clients[strings.TrimPrefix(path, "/clients/links/")]
		if !ok {
			f.reply(w, false, "client not found", nil)
			return
		}
		f.reply(w, true, "", []string{fmt.Sprintf("vless://%v@host.test:443?type=tcp#%v", cl["id"], cl["email"])})
	case path == "/clients/onlines":
		f.reply(w, true, "", []string{"u1-abc123"})
	case strings.HasPrefix(path, "/clients/del/"):
		email := strings.TrimPrefix(path, "/clients/del/")
		if _, ok := f.clients[email]; !ok {
			f.reply(w, false, "client not found", nil)
			return
		}
		delete(f.clients, email)
		f.reply(w, true, "", nil)
	case path == "/clients/bulkEnable", path == "/clients/bulkDisable":
		for _, e := range body["emails"].([]any) {
			f.clients[e.(string)]["enable"] = path == "/clients/bulkEnable"
		}
		f.reply(w, true, "", nil)
	case strings.HasSuffix(path, "/attach"), strings.HasSuffix(path, "/detach"):
		parts := strings.Split(strings.TrimPrefix(path, "/clients/"), "/")
		cl := f.clients[parts[0]]
		cur := cl["inboundIds"].([]any)
		for _, id := range body["inboundIds"].([]any) {
			if parts[1] == "attach" {
				cur = append(cur, id)
				continue
			}
			cur = slices.DeleteFunc(cur, func(x any) bool { return x == id })
		}
		cl["inboundIds"] = cur
		f.reply(w, true, "", nil)
	case path == "/server/getNewX25519Cert":
		f.reply(w, true, "", map[string]any{"privateKey": "NEW-PRIV", "publicKey": "NEW-PUB"})
	case path == "/inbounds/list":
		list := make([]map[string]any, 0, len(f.inbounds))
		for id := int64(1); id < f.nextID; id++ {
			if in, ok := f.inbounds[id]; ok {
				list = append(list, in)
			}
		}
		f.reply(w, true, "", list)
	case strings.HasPrefix(path, "/inbounds/get/"):
		in, ok := f.inbounds[idFrom("/inbounds/get/")]
		if !ok {
			f.reply(w, false, "record not found", nil)
			return
		}
		f.reply(w, true, "", in)
	case path == "/inbounds/add":
		for _, in := range f.inbounds {
			if in["port"] == body["port"] {
				f.reply(w, false, "Port already exists", nil)
				return
			}
		}
		body["id"] = float64(f.nextID)
		body["tag"] = "in-" + strconv.Itoa(int(body["port"].(float64))) + "-tcp"
		body["up"], body["down"] = 0, 0
		f.inbounds[f.nextID] = body
		f.nextID++
		f.reply(w, true, "Inbound has been successfully created.", body)
	case strings.HasPrefix(path, "/inbounds/update/"):
		id := idFrom("/inbounds/update/")
		body["id"] = float64(id)
		f.inbounds[id] = body
		f.reply(w, true, "Inbound has been successfully updated.", body)
	case strings.HasPrefix(path, "/inbounds/setEnable/"):
		in := f.inbounds[idFrom("/inbounds/setEnable/")]
		in["enable"] = body["enable"]
		f.reply(w, true, "", nil)
	case strings.HasPrefix(path, "/inbounds/del/"):
		delete(f.inbounds, idFrom("/inbounds/del/"))
		f.reply(w, true, "Inbound has been successfully deleted.", nil)
	case path == "/hosts/raw":
		rows := make([]map[string]any, 0, len(f.hosts))
		for _, g := range f.hosts {
			rows = append(rows, hostRowsOf(g)...)
		}
		f.reply(w, true, "", rows)
	case path == "/hosts/add":
		f.nextGroup++
		body["groupId"] = "g" + strconv.Itoa(f.nextGroup)
		f.hosts = append(f.hosts, body)
		f.reply(w, true, "Host added successfully", hostRowsOf(body))
	case strings.HasPrefix(path, "/hosts/get/"):
		g := f.group(strings.TrimPrefix(path, "/hosts/get/"))
		if g == nil {
			f.reply(w, false, "Failed to load host (host not found\n)", nil)
			return
		}
		f.reply(w, true, "", g)
	case strings.HasPrefix(path, "/hosts/update/"):
		id := strings.TrimPrefix(path, "/hosts/update/")
		for i, g := range f.hosts {
			if g["groupId"] == id {
				body["groupId"] = id
				f.hosts[i] = body
			}
		}
		f.reply(w, true, "Host updated successfully", hostRowsOf(body))
	case strings.HasPrefix(path, "/hosts/del/"):
		id := strings.TrimPrefix(path, "/hosts/del/")
		f.hosts = slices.DeleteFunc(f.hosts, func(g map[string]any) bool { return g["groupId"] == id })
		f.reply(w, true, "Host deleted successfully", nil)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakePanel) group(id string) map[string]any {
	for _, g := range f.hosts {
		if g["groupId"] == id {
			return g
		}
	}
	return nil
}

// hostRowsOf 按 3x-ui 的存法把组展开成“入站 × 地址”的行；地址可带 :port，覆盖组端口。
func hostRowsOf(g map[string]any) []map[string]any {
	var rows []map[string]any
	for _, h := range g["hosts"].([]any) {
		addr, port := h.(string), g["port"]
		if host, p, err := net.SplitHostPort(addr); err == nil {
			n, _ := strconv.Atoi(p)
			addr, port = host, float64(n)
		}
		for _, id := range g["inboundIds"].([]any) {
			rows = append(rows, map[string]any{"groupId": g["groupId"], "inboundId": id, "address": addr, "port": port, "remark": g["remark"], "isDisabled": false})
		}
	}
	return rows
}

func newTestClient(t *testing.T, fp *fakePanel, token string) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(fp)
	t.Cleanup(srv.Close)
	p, err := New(panel.ConnConfig{BaseURL: srv.URL + "/base/", Token: token, TLSMode: panel.TLSVerify})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p.(*Client), srv
}

const realityConfig = `{
  "settings": {"clients": [{"id": "uuid-1", "email": "u1"}], "decryption": "none"},
  "streamSettings": {"network": "tcp", "security": "reality",
    "realitySettings": {"target": "www.cloudflare.com:443", "privateKey": "PRIV", "settings": {"publicKey": "PUB"}}},
  "sniffing": {"enabled": true}
}`

func TestProbe(t *testing.T) {
	c, _ := newTestClient(t, newFakePanel(), testToken)
	info, err := c.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.Version != "3.11.0" || info.GUID != "guid-1" {
		t.Fatalf("info = %+v", info)
	}
	if info.CoreState != "error" || !strings.Contains(info.CoreError, "address already in use") {
		t.Fatalf("core state = %q, %q", info.CoreState, info.CoreError)
	}
	if info.PublicIP() != "203.0.113.1" || info.PublicIPv4 != "203.0.113.1" || info.PublicIPv6 != "" {
		t.Fatalf("public ip = %q / %q / %q", info.PublicIP(), info.PublicIPv4, info.PublicIPv6)
	}
	res := info.Resources
	if res == nil || res.CPU == nil {
		t.Fatalf("resources = %+v", res)
	}
	want := panel.Resources{
		CPU: res.CPU, CPUCores: 2, MemUsed: 300, MemTotal: 1000, SwapUsed: 1, SwapTotal: 2, DiskUsed: 30, DiskTotal: 200,
		Load1: 0.5, Load5: 0.3, UptimeSeconds: 86400, TCPCount: 42, UDPCount: 7, NetUpRate: 2900, NetDownRate: 2950,
		NetSent: 833, NetRecv: 895, CoreState: "error", CoreVersion: "26.10.21",
	}
	if *res != want {
		t.Fatalf("resources = %+v, want %+v", *res, want)
	}
}

func TestReportedIP(t *testing.T) {
	cases := map[string]string{"203.0.113.1": "203.0.113.1", "N/A": "", "": ""}
	for raw, want := range cases {
		if got := reportedIP(raw); got != want {
			t.Errorf("reportedIP(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestInboundLifecycleRedactsAndRestoresSecrets(t *testing.T) {
	fp := newFakePanel()
	c, _ := newTestClient(t, fp, testToken)
	ctx := context.Background()

	created, err := c.CreateInbound(ctx, panel.InboundSpec{Protocol: "vless", Port: 24443, Remark: "r1", Enabled: true, Config: json.RawMessage(realityConfig)})
	if err != nil {
		t.Fatalf("CreateInbound: %v", err)
	}
	if created.Ref != "1" || created.Network != "tcp" || created.Security != "reality" || created.ClientCount != 1 {
		t.Fatalf("created = %+v", created)
	}
	if strings.Contains(string(created.Config), "PRIV") || strings.Contains(string(created.Config), "uuid-1") {
		t.Fatalf("cached config leaks secrets or clients: %s", created.Config)
	}
	if !strings.Contains(string(created.Config), "PUB") {
		t.Fatalf("public key should stay: %s", created.Config)
	}

	// 面板侧只改 clients：摘要不变（clients 不算漂移）
	fp.mu.Lock()
	fp.inbounds[1]["settings"].(map[string]any)["clients"] = []any{}
	fp.mu.Unlock()
	same, err := c.GetInbound(ctx, "1")
	if err != nil || same.ConfigHash != created.ConfigHash {
		t.Fatalf("hash changed on clients-only edit: %v", err)
	}
	fp.mu.Lock()
	fp.inbounds[1]["settings"].(map[string]any)["clients"] = []any{map[string]any{"id": "uuid-2"}}
	fp.mu.Unlock()

	// 用缓存副本（已删私钥）回写并改备注：私钥与 clients 从面板原值补回
	updated, err := c.UpdateInbound(ctx, "1", panel.InboundSpec{Protocol: "vless", Port: 24443, Remark: "r2", Enabled: true, Config: created.Config})
	if err != nil {
		t.Fatalf("UpdateInbound: %v", err)
	}
	stored := fp.inbounds[1]
	reality := stored["streamSettings"].(map[string]any)["realitySettings"].(map[string]any)
	if reality["privateKey"] != "PRIV" {
		t.Fatalf("private key not restored: %v", reality)
	}
	if clients := stored["settings"].(map[string]any)["clients"].([]any); len(clients) != 1 {
		t.Fatalf("clients not preserved: %v", clients)
	}
	if updated.Remark != "r2" || updated.ConfigHash == created.ConfigHash {
		t.Fatalf("updated = %+v", updated)
	}

	if err := c.SetInboundEnabled(ctx, "1", false); err != nil {
		t.Fatalf("SetInboundEnabled: %v", err)
	}
	list, err := c.ListInbounds(ctx)
	if err != nil || len(list) != 1 || list[0].Enabled {
		t.Fatalf("ListInbounds = %+v, %v", list, err)
	}

	if _, err := c.CreateInbound(ctx, panel.InboundSpec{Protocol: "vless", Port: 24443, Remark: "dup", Config: json.RawMessage(realityConfig)}); !errors.Is(err, panel.ErrRejected) {
		t.Fatalf("duplicate port err = %v", err)
	}
	if err := c.DeleteInbound(ctx, "1"); err != nil {
		t.Fatalf("DeleteInbound: %v", err)
	}
	if _, err := c.GetInbound(ctx, "1"); !errors.Is(err, panel.ErrRejected) {
		t.Fatalf("get deleted err = %v", err)
	}
	if _, err := c.GetInbound(ctx, "x"); !errors.Is(err, panel.ErrNotFound) {
		t.Fatalf("bad ref err = %v", err)
	}
}

func TestEndpoints(t *testing.T) {
	fp := newFakePanel()
	c, _ := newTestClient(t, fp, testToken)
	ctx := context.Background()
	if err := c.CreateEndpoints(ctx, panel.EndpointSpec{InboundRefs: []string{"1"}, Addresses: []string{"cdn.example.com"}, Port: 8443, Remark: "cdn"}); err != nil {
		t.Fatalf("CreateEndpoints: %v", err)
	}
	eps, err := c.ListEndpoints(ctx)
	if err != nil || len(eps) != 1 || eps[0].GroupRef != "g1" || eps[0].InboundRef != "1" || eps[0].Port != 8443 {
		t.Fatalf("ListEndpoints = %+v, %v", eps, err)
	}
	if moves, err := c.RemoveEndpoint(ctx, panel.EndpointKey{GroupRef: "g1", InboundRef: "1", Address: "cdn.example.com"}); err != nil || len(moves) != 0 {
		t.Fatalf("RemoveEndpoint last pair = %v, %v", moves, err)
	}
	if len(fp.hosts) != 0 {
		t.Fatalf("group with its last pair removed should be deleted: %v", fp.hosts)
	}
	// 平台侧不填备注时用第一个地址（3x-ui 拒绝空备注）
	if err := c.CreateEndpoints(ctx, panel.EndpointSpec{InboundRefs: []string{"1"}, Addresses: []string{"relay.example.com"}}); err != nil {
		t.Fatalf("CreateEndpoints without remark: %v", err)
	}
	if len(fp.hosts) != 1 || fp.hosts[0]["remark"] != "relay.example.com" {
		t.Fatalf("empty remark should default to the first address, hosts = %v", fp.hosts)
	}
	if _, err := c.RemoveEndpoint(ctx, panel.EndpointKey{GroupRef: "g1", InboundRef: "1", Address: "cdn.example.com"}); !errors.Is(err, panel.ErrNotFound) {
		t.Fatalf("remove twice err = %v", err)
	}
}

// pairsOf 把面板现有的 Host 行整理成“组|入站|地址”集合，便于断言。
func pairsOf(t *testing.T, c *Client) map[string]bool {
	t.Helper()
	eps, err := c.ListEndpoints(context.Background())
	if err != nil {
		t.Fatalf("ListEndpoints: %v", err)
	}
	out := make(map[string]bool, len(eps))
	for _, e := range eps {
		out[e.GroupRef+"|"+e.InboundRef+"|"+e.Address] = true
	}
	return out
}

// TestRemoveEndpointKeepsTheRest 覆盖 docs/03 §5.1 删除对外地址的几种组形状：只删所点的一对，其余保留、设置不丢。
func TestRemoveEndpointKeepsTheRest(t *testing.T) {
	ctx := context.Background()
	newGroup := func(t *testing.T, refs []string, hosts []string) (*fakePanel, *Client) {
		fp := newFakePanel()
		c, _ := newTestClient(t, fp, testToken)
		if err := c.CreateEndpoints(ctx, panel.EndpointSpec{InboundRefs: refs, Addresses: hosts, Port: 443, Remark: "cdn"}); err != nil {
			t.Fatalf("CreateEndpoints: %v", err)
		}
		fp.mu.Lock()
		fp.hosts[0]["sni"] = "keep.example.com"
		fp.mu.Unlock()
		return fp, c
	}
	key := func(group, ref, addr string) panel.EndpointKey {
		return panel.EndpointKey{GroupRef: group, InboundRef: ref, Address: addr}
	}

	t.Run("one inbound, two addresses: shrink hosts", func(t *testing.T) {
		_, c := newGroup(t, []string{"1"}, []string{"a.example.com", "b.example.com"})
		if moves, err := c.RemoveEndpoint(ctx, key("g1", "1", "a.example.com")); err != nil || len(moves) != 0 {
			t.Fatalf("RemoveEndpoint = %v, %v", moves, err)
		}
		if got := pairsOf(t, c); len(got) != 1 || !got["g1|1|b.example.com"] {
			t.Fatalf("pairs = %v", got)
		}
	})

	t.Run("two inbounds, one address: shrink inbounds", func(t *testing.T) {
		_, c := newGroup(t, []string{"1", "2"}, []string{"a.example.com:8443"})
		if moves, err := c.RemoveEndpoint(ctx, key("g1", "1", "a.example.com")); err != nil || len(moves) != 0 {
			t.Fatalf("RemoveEndpoint = %v, %v", moves, err)
		}
		if got := pairsOf(t, c); len(got) != 1 || !got["g1|2|a.example.com"] {
			t.Fatalf("pairs = %v", got)
		}
	})

	t.Run("2x2: split, moving the fewer pairs and copying settings", func(t *testing.T) {
		fp, c := newGroup(t, []string{"1", "2"}, []string{"a.example.com", "b.example.com"})
		moves, err := c.RemoveEndpoint(ctx, key("g1", "1", "a.example.com"))
		if err != nil || len(moves) != 1 || moves[0].NewGroupRef != "g2" {
			t.Fatalf("RemoveEndpoint = %v, %v", moves, err)
		}
		want := map[string]bool{"g1|2|a.example.com": true, "g1|2|b.example.com": true, "g2|1|b.example.com": true}
		if got := pairsOf(t, c); len(got) != len(want) || !got["g1|2|a.example.com"] || !got["g1|2|b.example.com"] || !got["g2|1|b.example.com"] {
			t.Fatalf("pairs = %v, want %v", got, want)
		}
		if moves[0].InboundRef != "1" || moves[0].Address != "b.example.com" {
			t.Fatalf("moves = %+v", moves)
		}
		if fp.group("g2")["sni"] != "keep.example.com" {
			t.Fatalf("split group must copy settings: %v", fp.group("g2"))
		}
	})

	t.Run("2 inbounds x 3 addresses: move the other inbound's copy of the address", func(t *testing.T) {
		_, c := newGroup(t, []string{"1", "2"}, []string{"a.example.com", "b.example.com", "c.example.com"})
		moves, err := c.RemoveEndpoint(ctx, key("g1", "1", "a.example.com"))
		if err != nil || len(moves) != 1 || moves[0].InboundRef != "2" || moves[0].Address != "a.example.com" {
			t.Fatalf("RemoveEndpoint = %v, %v", moves, err)
		}
		got := pairsOf(t, c)
		if len(got) != 5 || got["g1|1|a.example.com"] || !got["g2|2|a.example.com"] || !got["g1|1|c.example.com"] {
			t.Fatalf("pairs = %v", got)
		}
	})
}

// TestInboundConfigAndKeys 覆盖 Ruling-16：编辑表单拿到含密钥、不含 clients 的完整配置；密钥由面板生成。
func TestInboundConfigAndKeys(t *testing.T) {
	fp := newFakePanel()
	c, _ := newTestClient(t, fp, testToken)
	ctx := context.Background()
	created, err := c.CreateInbound(ctx, panel.InboundSpec{Protocol: "vless", Port: 24443, Remark: "r1", Enabled: true, Config: json.RawMessage(realityConfig)})
	if err != nil {
		t.Fatalf("CreateInbound: %v", err)
	}
	cfg, err := c.GetInboundConfig(ctx, created.Ref)
	if err != nil {
		t.Fatalf("GetInboundConfig: %v", err)
	}
	if !strings.Contains(string(cfg.Config), "PRIV") || strings.Contains(string(cfg.Config), "uuid-1") {
		t.Fatalf("config must keep secrets and drop clients: %s", cfg.Config)
	}
	if cfg.Protocol != "vless" || cfg.Port != 24443 || cfg.Remark != "r1" || !cfg.Enabled {
		t.Fatalf("cfg = %+v", cfg)
	}
	if strings.Contains(string(created.Config), "decryption") {
		t.Fatalf("cached copy must not keep vless decryption: %s", created.Config)
	}
	keys, err := c.GenerateKeys(ctx, panel.KeyX25519)
	if err != nil || !strings.Contains(string(keys), "NEW-PUB") {
		t.Fatalf("GenerateKeys = %s, %v", keys, err)
	}
	if _, err := c.GenerateKeys(ctx, panel.KeyKind("rsa")); !errors.Is(err, panel.ErrRejected) {
		t.Fatalf("unknown kind err = %v", err)
	}
}

// TestClientOps 覆盖 M2 下发用到的 client 操作：新建时额度与到期为 0、凭据各协议共用、挂载 / 启停 / 删除。
func TestClientOps(t *testing.T) {
	fp := newFakePanel()
	c, _ := newTestClient(t, fp, testToken)
	ctx := context.Background()
	spec := panel.ClientSpec{Email: "u1-abc123", UUID: "uuid-1", Auth: "sec-1", LimitIP: 2, Enabled: true, InboundRefs: []string{"1", "2"}, Flow: "xtls-rprx-vision"}
	if err := c.AddClient(ctx, spec); err != nil {
		t.Fatalf("AddClient: %v", err)
	}
	sent := fp.lastBody["client"].(map[string]any)
	if sent["totalGB"] != float64(0) || sent["expiryTime"] != float64(0) || sent["id"] != "uuid-1" || sent["password"] != "sec-1" || sent["auth"] != "sec-1" ||
		sent["flow"] != "xtls-rprx-vision" {
		t.Fatalf("add body = %v", sent)
	}
	if err := c.AttachClient(ctx, spec.Email, []string{"3"}); err != nil {
		t.Fatalf("AttachClient: %v", err)
	}
	if err := c.DetachClient(ctx, spec.Email, []string{"1"}); err != nil {
		t.Fatalf("DetachClient: %v", err)
	}
	if err := c.SetClientsEnabled(ctx, []string{spec.Email}, false); err != nil {
		t.Fatalf("SetClientsEnabled: %v", err)
	}
	spec.LimitIP = 5
	spec.Enabled = false
	spec.Flow = "" // 去掉流控：flow 总是随更新给出
	if err := c.UpdateClient(ctx, spec); err != nil {
		t.Fatalf("UpdateClient: %v", err)
	}
	fp.mu.Lock()
	fp.clients[spec.Email]["traffic"] = map[string]any{"up": 100, "down": 250}
	fp.mu.Unlock()
	list, err := c.ListClients(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListClients = %+v, %v", list, err)
	}
	got := list[0]
	if got.Enabled || got.LimitIP != 5 || got.Flow != "" || got.Up != 100 || got.Down != 250 || strings.Join(got.InboundRefs, ",") != "2,3" {
		t.Fatalf("client = %+v", got)
	}
	spec.UUID = "22222222-2222-4222-8222-222222222222"
	if err := c.SetClientCredential(ctx, spec); err != nil {
		t.Fatalf("SetClientCredential: %v", err)
	}
	links, err := c.ClientLinks(ctx, spec.Email)
	if err != nil || len(links) != 1 || !strings.Contains(links[0], spec.UUID) {
		t.Fatalf("ClientLinks = %v, %v; want one link carrying the rotated UUID", links, err)
	}
	if online, err := c.OnlineClients(ctx); err != nil || len(online) != 1 || online[0] != "u1-abc123" {
		t.Fatalf("OnlineClients = %v, %v", online, err)
	}
	if !c.Capabilities().OnlineClients {
		t.Fatal("xui should report OnlineClients capability")
	}
	if err := c.DeleteClient(ctx, spec.Email); err != nil {
		t.Fatalf("DeleteClient: %v", err)
	}
	if err := c.DeleteClient(ctx, spec.Email); !errors.Is(err, panel.ErrRejected) {
		t.Fatalf("delete twice err = %v", err)
	}
	if err := c.AddClient(ctx, panel.ClientSpec{Email: "x", InboundRefs: []string{"bad"}}); !errors.Is(err, panel.ErrRejected) {
		t.Fatalf("bad ref err = %v", err)
	}
}

func TestErrorClassification(t *testing.T) {
	ctx := context.Background()
	c, _ := newTestClient(t, newFakePanel(), "wrong-token")
	if _, err := c.Probe(ctx); !errors.Is(err, panel.ErrAuth) {
		t.Fatalf("bad token err = %v", err)
	}
	for status, want := range map[int]error{
		http.StatusNotFound:            panel.ErrNotFound,
		http.StatusBadGateway:          panel.ErrUnreachable,
		http.StatusFound:               panel.ErrUnreachable,
		http.StatusUnprocessableEntity: panel.ErrRejected,
	} {
		fp := newFakePanel()
		fp.status = status
		c, _ := newTestClient(t, fp, testToken)
		if _, err := c.Probe(ctx); !errors.Is(err, want) {
			t.Fatalf("status %d err = %v, want %v", status, err, want)
		}
	}
	if strings.Contains((&panel.Error{Kind: panel.KindAuth, Message: "面板拒绝了 API token"}).Error(), testToken) {
		t.Fatal("error message must not contain token")
	}
	unreachable, err := New(panel.ConnConfig{BaseURL: "http://127.0.0.1:1", Token: testToken})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := unreachable.Probe(ctx); !errors.Is(err, panel.ErrUnreachable) {
		t.Fatalf("connection refused err = %v", err)
	}
}

// TestLive 对真实 3x-ui 面板跑一遍增删改查；设置 XUI_LIVE_URL 与 XUI_LIVE_TOKEN 时才执行（IT-3 前的手工验证）。
func TestLive(t *testing.T) {
	base, token := os.Getenv("XUI_LIVE_URL"), os.Getenv("XUI_LIVE_TOKEN")
	if base == "" || token == "" {
		t.Skip("XUI_LIVE_URL / XUI_LIVE_TOKEN 未设置，跳过真实面板用例")
	}
	p, err := New(panel.ConnConfig{BaseURL: base, Token: token, TLSMode: panel.TLSVerify})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if _, err := p.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	cfg := json.RawMessage(`{"settings":{"clients":[],"decryption":"none"},"streamSettings":{"network":"ws","security":"none","wsSettings":{"path":"/live"}},"sniffing":{"enabled":true,"destOverride":["http","tls"]}}`)
	in, err := p.CreateInbound(ctx, panel.InboundSpec{Protocol: "vless", Port: 34567, Remark: "live-test", Enabled: true, Config: cfg})
	if err != nil {
		t.Fatalf("CreateInbound: %v", err)
	}
	t.Cleanup(func() {
		if err := p.DeleteInbound(context.Background(), in.Ref); err != nil {
			t.Errorf("cleanup DeleteInbound: %v", err)
		}
	})
	if _, err := p.UpdateInbound(ctx, in.Ref, panel.InboundSpec{Protocol: "vless", Port: 34567, Remark: "live-test-2", Enabled: true, Config: in.Config}); err != nil {
		t.Fatalf("UpdateInbound: %v", err)
	}
	if err := p.SetInboundEnabled(ctx, in.Ref, false); err != nil {
		t.Fatalf("SetInboundEnabled: %v", err)
	}
	if err := p.CreateEndpoints(ctx, panel.EndpointSpec{InboundRefs: []string{in.Ref}, Addresses: []string{"live.example.com"}, Port: 443, Remark: "live"}); err != nil {
		t.Fatalf("CreateEndpoints: %v", err)
	}
	eps, err := p.ListEndpoints(ctx)
	if err != nil || len(eps) == 0 {
		t.Fatalf("ListEndpoints = %+v, %v", eps, err)
	}
	ep := eps[len(eps)-1]
	if _, err := p.RemoveEndpoint(ctx, panel.EndpointKey{GroupRef: ep.GroupRef, InboundRef: ep.InboundRef, Address: ep.Address}); err != nil {
		t.Fatalf("RemoveEndpoint: %v", err)
	}
	got, err := p.GetInbound(ctx, in.Ref)
	if err != nil || got.Remark != "live-test-2" || got.Enabled {
		t.Fatalf("GetInbound = %+v, %v", got, err)
	}
}
