package xui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"v2-hub-server/internal/manager/panel"
)

const testToken = "test-token-value"

// fakePanel 按 3x-ui v3.11.0 的真实响应形状（本地容器实测）模拟面板 API。
type fakePanel struct {
	mu       sync.Mutex
	inbounds map[int64]map[string]any
	hosts    []map[string]any
	nextID   int64
	status   int // 非 0 时所有请求返回该状态码
}

func newFakePanel() *fakePanel {
	return &fakePanel{inbounds: map[int64]map[string]any{}, nextID: 1}
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
		f.reply(w, true, "", map[string]any{"panelVersion": "3.11.0", "panelGuid": "guid-1", "cpu": 3.2, "publicIP": map[string]any{"ipv4": "203.0.113.1"}})
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
		f.reply(w, true, "", f.hosts)
	case path == "/hosts/add":
		for _, id := range body["inboundIds"].([]any) {
			for _, h := range body["hosts"].([]any) {
				f.hosts = append(f.hosts, map[string]any{"groupId": "g1", "inboundId": id, "address": h, "port": body["port"], "remark": body["remark"], "isDisabled": false})
			}
		}
		f.reply(w, true, "Host added successfully", nil)
	case strings.HasPrefix(path, "/hosts/del/"):
		f.hosts = nil
		f.reply(w, true, "Host deleted successfully", nil)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
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
	if strings.Contains(string(info.Status), "publicIP") || !strings.Contains(string(info.Status), "cpu") {
		t.Fatalf("snapshot should keep whitelisted fields only: %s", info.Status)
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
	if err := c.DeleteEndpointGroup(ctx, "g1"); err != nil {
		t.Fatalf("DeleteEndpointGroup: %v", err)
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
	if err := p.DeleteEndpointGroup(ctx, eps[0].GroupRef); err != nil {
		t.Fatalf("DeleteEndpointGroup: %v", err)
	}
	got, err := p.GetInbound(ctx, in.Ref)
	if err != nil || got.Remark != "live-test-2" || got.Enabled {
		t.Fatalf("GetInbound = %+v, %v", got, err)
	}
}
