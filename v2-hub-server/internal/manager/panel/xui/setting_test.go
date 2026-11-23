package xui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"v2-hub-server/internal/manager/panel"
)

// settingPanel 按 3x-ui v3.11.0 实测形状模拟面板设置接口：POST /setting/all 的 obj 是全部设置，POST /setting/update 收整份 JSON。
type settingPanel struct {
	mu       sync.Mutex
	settings string
	writes   []string
}

func (p *settingPanel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+testToken || r.Method != http.MethodPost {
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
	case "/setting/all":
		reply(json.RawMessage(p.settings))
	case "/setting/update":
		body, err := io.ReadAll(r.Body)
		if err != nil || r.Header.Get("Content-Type") != contentTypeJSON {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		p.writes = append(p.writes, string(body))
		p.settings = string(body)
		reply(nil)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newCoreKeeper(t *testing.T, fp *settingPanel) panel.CoreKeeper {
	t.Helper()
	srv := httptest.NewServer(fp)
	t.Cleanup(srv.Close)
	prov, err := New(panel.ConnConfig{BaseURL: srv.URL + "/base", Token: testToken, TLSMode: panel.TLSVerify})
	if err != nil {
		t.Fatal(err)
	}
	keeper, ok := prov.(panel.CoreKeeper)
	if !ok {
		t.Fatal("3x-ui 客户端应实现 CoreKeeper")
	}
	return keeper
}

// 设置开着：只改这一项、其它键原样写回（数字不丢精度、脱敏成空串的密钥仍是空串）；再调一次不再写（Ruling-94）。
func TestKeepCoreOnClientDrop(t *testing.T) {
	fp := &settingPanel{settings: `{"webPort":2053,"tgBotToken":"","expireDiff":12345678901234567890,` +
		`"restartXrayOnClientDisable":true,"subEnable":true,"nested":{"a":[1,2]}}`}
	keeper := newCoreKeeper(t, fp)
	ctx := context.Background()

	changed, err := keeper.KeepCoreOnClientDrop(ctx)
	if err != nil || !changed || len(fp.writes) != 1 {
		t.Fatalf("开着时应改一次：changed=%v writes=%d err=%v", changed, len(fp.writes), err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(fp.writes[0]), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"webPort": "2053", "tgBotToken": `""`, "expireDiff": "12345678901234567890",
		"restartXrayOnClientDisable": "false", "subEnable": "true", "nested": `{"a":[1,2]}`,
	}
	if len(got) != len(want) {
		t.Fatalf("写回的键数不对：%s", fp.writes[0])
	}
	for key, value := range want {
		if string(got[key]) != value {
			t.Errorf("键 %s 写回成 %s，应为 %s", key, got[key], value)
		}
	}

	changed, err = keeper.KeepCoreOnClientDrop(ctx)
	if err != nil || changed || len(fp.writes) != 1 {
		t.Fatalf("已经关着时不应再写：changed=%v writes=%d err=%v", changed, len(fp.writes), err)
	}
}

// 面板没有这一项（旧版本）或值不是布尔：不写面板。
func TestKeepCoreOnClientDropSkips(t *testing.T) {
	ctx := context.Background()
	old := &settingPanel{settings: `{"webPort":2053}`}
	if changed, err := newCoreKeeper(t, old).KeepCoreOnClientDrop(ctx); err != nil || changed || len(old.writes) != 0 {
		t.Fatalf("没有这一项时应跳过：changed=%v writes=%d err=%v", changed, len(old.writes), err)
	}
	odd := &settingPanel{settings: `{"restartXrayOnClientDisable":"yes"}`}
	if changed, err := newCoreKeeper(t, odd).KeepCoreOnClientDrop(ctx); err == nil || changed || len(odd.writes) != 0 {
		t.Fatalf("值不是布尔时应报错且不写：changed=%v writes=%d err=%v", changed, len(odd.writes), err)
	}
}
