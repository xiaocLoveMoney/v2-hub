package xui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"v2-hub-server/internal/manager/panel"
)

// corePanel 按 3x-ui v3.11.0 实测形状模拟内核配置相关接口：POST /xray/ 的 obj 是 JSON 字符串，
// 里面的 xraySetting 是对象；/xray/update 只收表单字段。
type corePanel struct {
	mu         sync.Mutex
	template   string
	testURL    string
	state      string
	restarts   int
	lastFormCT string
}

func (p *corePanel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	reply := func(obj any) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": obj}); err != nil {
			panic(err)
		}
	}
	switch strings.TrimPrefix(r.URL.Path, "/base/panel/api") {
	case "/xray/":
		inner, err := json.Marshal(map[string]any{
			"xraySetting": json.RawMessage(p.template), "inboundTags": []string{"in-443-tcp"}, "outboundTestUrl": p.testURL,
		})
		if err != nil {
			panic(err)
		}
		reply(string(inner))
	case "/xray/update":
		p.lastFormCT = r.Header.Get("Content-Type")
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		p.template = r.PostForm.Get("xraySetting")
		p.testURL = r.PostForm.Get("outboundTestUrl")
		reply(nil)
	case "/server/restartXrayService":
		p.restarts++
		reply(nil)
	case "/server/status":
		reply(map[string]any{"xray": map[string]any{"state": p.state, "errorMsg": "", "version": "26.10.21"}})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestCoreTemplateRoundTrip(t *testing.T) {
	fp := &corePanel{template: `{"outbounds":[{"tag":"direct","protocol":"freedom"}]}`, testURL: "https://example.test/204", state: "running"}
	srv := httptest.NewServer(fp)
	defer srv.Close()
	prov, err := New(panel.ConnConfig{BaseURL: srv.URL + "/base", Token: testToken, TLSMode: panel.TLSVerify})
	if err != nil {
		t.Fatal(err)
	}
	if !prov.Capabilities().CoreConfig {
		t.Fatal("3x-ui 应声明 CoreConfig 能力")
	}
	cc, ok := prov.(panel.CoreConfigurator)
	if !ok {
		t.Fatal("3x-ui 客户端应实现 CoreConfigurator")
	}
	ctx := context.Background()
	tpl, err := cc.GetCoreTemplate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(tpl.Config) != fp.template || tpl.Extra[extraOutboundTestURL] != fp.testURL {
		t.Fatalf("读出的模板不对：%s %v", tpl.Config, tpl.Extra)
	}

	next := `{"outbounds":[{"tag":"direct","protocol":"freedom"},{"tag":"hub-r1","protocol":"vless"}]}`
	tpl.Config = json.RawMessage(next)
	if err := cc.SetCoreTemplate(ctx, tpl); err != nil {
		t.Fatal(err)
	}
	if fp.template != next || fp.testURL != "https://example.test/204" || fp.lastFormCT != contentTypeForm {
		t.Fatalf("写回应是表单且带回测试地址：%q %q %q", fp.template, fp.testURL, fp.lastFormCT)
	}
	if err := cc.RestartCore(ctx); err != nil || fp.restarts != 1 {
		t.Fatalf("重启：%v %d", err, fp.restarts)
	}
	state, err := cc.CoreState(ctx)
	if err != nil || !state.Running {
		t.Fatalf("内核应在运行：%+v %v", state, err)
	}
	fp.state = "error"
	if state, err = cc.CoreState(ctx); err != nil || state.Running || state.State != "error" {
		t.Fatalf("内核应不在运行：%+v %v", state, err)
	}
}

func TestUnwrapJSONString(t *testing.T) {
	obj := json.RawMessage(`{"a":1}`)
	got, err := unwrapJSONString(obj)
	if err != nil || string(got) != `{"a":1}` {
		t.Fatalf("对象应原样返回：%s %v", got, err)
	}
	got, err = unwrapJSONString(json.RawMessage(`"{\"a\":1}"`))
	if err != nil || string(got) != `{"a":1}` {
		t.Fatalf("字符串里的 JSON 应解开：%s %v", got, err)
	}
	if _, err = unwrapJSONString(json.RawMessage(`"not json"`)); err == nil {
		t.Fatal("字符串里不是 JSON 应报错")
	}
}

// 保存模板即生效的版本门槛是 3.3.1（Ruling-93）；认不出来的版本串按不支持处理，由调用方退回重启。
func TestSupportsLiveApply(t *testing.T) {
	cases := map[string]bool{
		"3.11.0": true, "v3.11.0": true, "3.3.1": true, "3.4.0": true, "4.0.0": true, " 3.10.2 ": true,
		"3.3.0": false, "3.2.8": false, "2.9.4": false, "": false, "dev-latest": false, "3.11": false, "3.x.1": false, "3.-1.5": false,
	}
	for version, want := range cases {
		if got := supportsLiveApply(version); got != want {
			t.Errorf("supportsLiveApply(%q) = %v, want %v", version, got, want)
		}
	}
}
