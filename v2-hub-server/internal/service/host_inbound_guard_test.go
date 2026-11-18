package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"v2-hub-server/internal/manager/deploy"
	"v2-hub-server/internal/manager/remote"
)

const (
	guardBusyPort = 443
	guardFreePort = 41443
	vlessTCPJSON  = `{"settings":{"clients":[],"decryption":"none"},"streamSettings":{"network":"tcp","security":"none"}}`
	hysteriaJSON  = `{"settings":{"clients":[]},"streamSettings":{"network":"hysteria","security":"tls"}}`
)

func guardInput(protocol, config string, port int, enabled bool) InboundCreateInput {
	return InboundCreateInput{Remark: "guard", Port: port, Enabled: enabled, Protocol: protocol, Config: json.RawMessage(config)}
}

func TestListenTransports(t *testing.T) {
	cases := []struct {
		protocol, config string
		tcp, udp         bool
	}{
		{"vless", vlessTCPJSON, true, false},
		{"vmess", `{"streamSettings":{"network":"kcp"}}`, false, true},
		{"hysteria", hysteriaJSON, false, true},
		{"wireguard", `{}`, false, true},
		{"shadowsocks", `{"settings":{"network":"tcp,udp"}}`, true, true},
		{"shadowsocks", `{"settings":{"network":"udp"}}`, false, true},
		{"trojan", ``, true, false},
		{"trojan", `not json`, true, false},
	}
	for _, c := range cases {
		tcp, udp := listenTransports(c.protocol, json.RawMessage(c.config))
		if tcp != c.tcp || udp != c.udp {
			t.Errorf("listenTransports(%s, %s) = %v/%v, want %v/%v", c.protocol, c.config, tcp, udp, c.tcp, c.udp)
		}
	}
}

// 绑了 SSH 的服务器：要监听的端口已被主机上的程序占用就拒绝，面板上什么都不建（Ruling-78）。
func TestCreateInboundRefusesBusyHostPort(t *testing.T) {
	f := newPresetFixture(t, false)
	ctx := context.Background()
	f.deployer.Listening = []deploy.Port{{Number: guardBusyPort}, {Number: guardBusyPort, UDP: true}, {Number: guardFreePort, UDP: true}}

	_, err := f.hosts.CreateInbound(ctx, f.actor, f.serverID, guardInput("vless", vlessTCPJSON, guardBusyPort, true), nil)
	wantCode(t, err, CodeInvalidArgument)
	if !strings.Contains(err.Error(), "443/TCP") {
		t.Fatalf("message should name the port and protocol: %v", err)
	}
	// 只被 UDP 占着的端口：TCP 入站可以建，UDP 的 Hysteria 不行
	if _, err := f.hosts.CreateInbound(ctx, f.actor, f.serverID, guardInput("vless", vlessTCPJSON, guardFreePort, true), nil); err != nil {
		t.Fatalf("a tcp inbound on a udp-only busy port: %v", err)
	}
	_, err = f.hosts.CreateInbound(ctx, f.actor, f.serverID, guardInput("hysteria", hysteriaJSON, guardFreePort+1, true), nil)
	if err != nil {
		t.Fatalf("hysteria on a free port: %v", err)
	}
	f.deployer.Listening = append(f.deployer.Listening, deploy.Port{Number: guardFreePort + 2, UDP: true})
	_, err = f.hosts.CreateInbound(ctx, f.actor, f.serverID, guardInput("hysteria", hysteriaJSON, guardFreePort+2, true), nil)
	wantCode(t, err, CodeInvalidArgument)
	if ports := f.inboundsByPort(t); len(ports) != 2 {
		t.Fatalf("only the two allowed inbounds should exist, got %v", ports)
	}
	// 新建时就停用：不监听端口，不查
	if _, err := f.hosts.CreateInbound(ctx, f.actor, f.serverID, guardInput("vless", vlessTCPJSON, guardBusyPort, false), nil); err != nil {
		t.Fatalf("a disabled inbound need not check ports: %v", err)
	}
}

func TestUpdateAndEnableInboundCheckHostPort(t *testing.T) {
	f := newPresetFixture(t, false)
	ctx := context.Background()
	created, err := f.hosts.CreateInbound(ctx, f.actor, f.serverID, guardInput("vless", vlessTCPJSON, guardFreePort, true), nil)
	if err != nil {
		t.Fatalf("CreateInbound: %v", err)
	}
	// 这个入站自己占着的端口出现在主机监听里：端口不变的修改照常
	f.deployer.Listening = []deploy.Port{{Number: guardFreePort}, {Number: guardBusyPort}}
	if _, err := f.hosts.UpdateInbound(ctx, f.actor, created.ID, InboundUpdateInput{Remark: "renamed", Port: guardFreePort}, nil); err != nil {
		t.Fatalf("an update keeping the port: %v", err)
	}
	// 改到被占的端口：拒绝，面板上的端口不变
	_, err = f.hosts.UpdateInbound(ctx, f.actor, created.ID, InboundUpdateInput{Remark: "renamed", Port: guardBusyPort}, nil)
	wantCode(t, err, CodeInvalidArgument)
	if _, ok := f.inboundsByPort(t)[guardFreePort]; !ok {
		t.Fatal("a refused update must not reach the panel")
	}
	// 停用后改端口不查，启用时再查
	if _, err := f.hosts.SetInboundEnabled(ctx, f.actor, created.ID, false, nil); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := f.hosts.UpdateInbound(ctx, f.actor, created.ID, InboundUpdateInput{Remark: "renamed", Port: guardBusyPort}, nil); err != nil {
		t.Fatalf("a disabled inbound may move to a busy port: %v", err)
	}
	_, err = f.hosts.SetInboundEnabled(ctx, f.actor, created.ID, true, nil)
	wantCode(t, err, CodeInvalidArgument)
	f.deployer.Listening = []deploy.Port{{Number: guardFreePort}}
	if got, err := f.hosts.SetInboundEnabled(ctx, f.actor, created.ID, true, nil); err != nil || !got.IsEnabled {
		t.Fatalf("enable once the port is free: %+v, %v", got, err)
	}
}

// 读不到主机端口（SSH 连不上）不挡入站管理；没绑 SSH 的服务器不查。
func TestInboundPortCheckIsBestEffort(t *testing.T) {
	f := newPresetFixture(t, false)
	ctx := context.Background()
	f.deployer.Listening = []deploy.Port{{Number: guardBusyPort}}
	f.dialer.Err = &remote.Error{Kind: remote.KindUnreachable, Message: "连不上"}
	if _, err := f.hosts.CreateInbound(ctx, f.actor, f.serverID, guardInput("vless", vlessTCPJSON, guardBusyPort, true), nil); err != nil {
		t.Fatalf("an unreadable host must not block the write: %v", err)
	}
	f.dialer.Err = nil

	plain := newHostFixture(t)
	srv, err := plain.panels.CreateServer(ctx, plain.actor, serverInput("no-ssh"), nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	calls := len(plain.deployer.Calls())
	plain.deployer.Listening = []deploy.Port{{Number: guardBusyPort}}
	if _, err := plain.hosts.CreateInbound(ctx, plain.actor, srv.ID, guardInput("vless", vlessTCPJSON, guardBusyPort, true), nil); err != nil {
		t.Fatalf("a server without ssh is not checked: %v", err)
	}
	if len(plain.deployer.Calls()) != calls {
		t.Fatal("no ssh call expected for a server without a host")
	}
}
