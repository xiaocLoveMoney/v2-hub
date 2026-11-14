package xui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"v2-hub-server/internal/manager/panel"
)

// probePanel 按 3x-ui v3.11.0 的形状模拟出站测试与网页证书路径两个接口。
type probePanel struct {
	// reply 是 /xray/testOutbound 返回的整个信封
	reply    map[string]any
	lastForm map[string]string
}

func (p *probePanel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	write := func(body map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(body); err != nil {
			panic(err)
		}
	}
	switch strings.TrimPrefix(r.URL.Path, "/base/panel/api") {
	case "/xray/testOutbound":
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		p.lastForm = map[string]string{"outbound": r.PostForm.Get("outbound"), "mode": r.PostForm.Get("mode")}
		write(p.reply)
	case "/server/getWebCertFiles":
		write(map[string]any{"success": true, "obj": map[string]any{"webCertFile": "/root/cert/fullchain.pem", "webKeyFile": "/root/cert/privkey.pem"}})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newProbeClient(t *testing.T, fp *probePanel) *Client {
	t.Helper()
	srv := httptest.NewServer(fp)
	t.Cleanup(srv.Close)
	p, err := New(panel.ConnConfig{BaseURL: srv.URL + "/base", Token: testToken})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c, ok := p.(*Client)
	if !ok {
		t.Fatalf("New returned %T", p)
	}
	return c
}

func TestTestOutbound(t *testing.T) {
	const outbound = `{"tag":"hub-test","protocol":"socks"}`
	ctx := context.Background()

	t.Run("http 方式：空 mode，带出口 IP", func(t *testing.T) {
		fp := &probePanel{reply: map[string]any{"success": true, "obj": map[string]any{
			"tag": "hub-test", "success": true, "delay": 123, "mode": "http", "httpStatus": 204,
			"egress": map[string]any{"ipv4": "198.51.100.7", "country": "JP"},
		}}}
		got, err := newProbeClient(t, fp).TestOutbound(ctx, json.RawMessage(outbound), panel.OutboundTestHTTP)
		if err != nil {
			t.Fatalf("TestOutbound: %v", err)
		}
		want := panel.OutboundTestResult{OK: true, Mode: panel.OutboundTestHTTP, DelayMs: 123, HTTPStatus: 204, EgressIP: "198.51.100.7", EgressRegion: "JP"}
		if got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
		if fp.lastForm["mode"] != "" || fp.lastForm["outbound"] != outbound {
			t.Errorf("form = %v", fp.lastForm)
		}
	})

	t.Run("tcp 与 real 方式原样传给面板；连不上是一次失败的结果", func(t *testing.T) {
		fp := &probePanel{reply: map[string]any{"success": true, "obj": map[string]any{"success": false, "delay": 0, "error": "dial timeout", "mode": "tcp"}}}
		c := newProbeClient(t, fp)
		got, err := c.TestOutbound(ctx, json.RawMessage(outbound), panel.OutboundTestTCP)
		if err != nil || got.OK || got.Message != "dial timeout" || got.Mode != panel.OutboundTestTCP || fp.lastForm["mode"] != "tcp" {
			t.Errorf("tcp: got %+v err %v form %v", got, err, fp.lastForm)
		}
		if _, err := c.TestOutbound(ctx, json.RawMessage(outbound), panel.OutboundTestReal); err != nil || fp.lastForm["mode"] != "real" {
			t.Errorf("real: err %v form %v", err, fp.lastForm)
		}
	})

	t.Run("面板拒绝请求（另一项测试在跑）也是失败的结果，不是错误", func(t *testing.T) {
		fp := &probePanel{reply: map[string]any{"success": false, "msg": "Another outbound test is already running, please wait"}}
		got, err := newProbeClient(t, fp).TestOutbound(ctx, json.RawMessage(outbound), panel.OutboundTestHTTP)
		if err != nil || got.OK || !strings.Contains(got.Message, "already running") {
			t.Errorf("got %+v err %v", got, err)
		}
	})

	t.Run("token 无效是错误", func(t *testing.T) {
		c := newProbeClient(t, &probePanel{})
		c.token = "wrong"
		if _, err := c.TestOutbound(ctx, json.RawMessage(outbound), panel.OutboundTestTCP); !errors.Is(err, panel.ErrAuth) {
			t.Errorf("err = %v, want ErrAuth", err)
		}
	})
}

func TestWebCertFiles(t *testing.T) {
	got, err := newProbeClient(t, &probePanel{}).WebCertFiles(context.Background())
	want := panel.CertFiles{CertFile: "/root/cert/fullchain.pem", KeyFile: "/root/cert/privkey.pem"}
	if err != nil || got != want {
		t.Errorf("got %+v err %v, want %+v", got, err, want)
	}
}
