package service

import (
	"context"
	"testing"

	"v2-hub-server/internal/manager/panel"
)

// 改面板地址或面板类型时必须重新提交 token，不拿已存 token 去连新地址；与是否停用无关（Ruling-97）。
func TestUpdateServerRequiresTokenOnMove(t *testing.T) {
	f := newPanelFixture(t)
	ctx := context.Background()
	srv, err := f.panels.CreateServer(ctx, f.actor, serverInput("hk-1"), nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	keep := serverInput("hk-1")
	keep.APIToken = ""

	// 地址只差规范化（末尾斜杠）：视为没变，沿用 token
	same := keep
	same.BaseURL = fakeBase + "/"
	same.Remark = "renamed"
	if _, err := f.panels.UpdateServer(ctx, f.actor, srv.ID, same, nil); err != nil {
		t.Fatalf("update without moving: %v", err)
	}

	moved := keep
	moved.BaseURL = "https://evil.example.net:2053/secret-path"
	_, err = f.panels.UpdateServer(ctx, f.actor, srv.ID, moved, nil)
	wantCode(t, err, CodeInvalidArgument)

	// 先停用（不改地址）再改地址：同样拒绝，堵住“停用不探测、轮询带出旧 token”的路径
	disabled := keep
	disabled.IsDisabled = true
	if _, err := f.panels.UpdateServer(ctx, f.actor, srv.ID, disabled, nil); err != nil {
		t.Fatalf("disable: %v", err)
	}
	moved.IsDisabled = true
	_, err = f.panels.UpdateServer(ctx, f.actor, srv.ID, moved, nil)
	wantCode(t, err, CodeInvalidArgument)
	got, err := f.panels.GetServer(ctx, srv.ID)
	if err != nil || got.BaseURL != fakeBase {
		t.Fatalf("base url after rejected move = %q, %v", got.BaseURL, err)
	}

	// 带上新 token 就可以改（停用中不探测）
	moved.APIToken = fakeToken
	if _, err := f.panels.UpdateServer(ctx, f.actor, srv.ID, moved, nil); err != nil {
		t.Fatalf("move with a new token: %v", err)
	}
}

// TLS 校验方式或指定的证书指纹有变化时同样必须重新提交 token（Ruling-107）：把校验改松、或把指纹换成别人的证书，
// 都不能带着已存 token 继续连；与是否停用无关。TLS 设置不变时沿用 token。
func TestUpdateServerRequiresTokenOnTLSChange(t *testing.T) {
	f := newPanelFixture(t)
	ctx := context.Background()
	srv, err := f.panels.CreateServer(ctx, f.actor, serverInput("hk-1"), nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	keep := serverInput("hk-1")
	keep.APIToken = ""
	keep.Remark = "unchanged tls"
	if _, err := f.panels.UpdateServer(ctx, f.actor, srv.ID, keep, nil); err != nil {
		t.Fatalf("update without touching tls: %v", err)
	}

	const pinA = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	const pinB = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="
	skip := keep
	skip.TLSMode = panel.TLSSkip
	pinned := keep
	pinned.TLSMode, pinned.CertPinSHA256 = panel.TLSPin, pinA
	for _, in := range []ServerInput{skip, pinned} {
		for _, disabled := range []bool{false, true} {
			in.IsDisabled = disabled
			_, err = f.panels.UpdateServer(ctx, f.actor, srv.ID, in, nil)
			wantCode(t, err, CodeInvalidArgument)
		}
	}
	got, err := f.panels.GetServer(ctx, srv.ID)
	if err != nil || got.TLSMode != panel.TLSVerify {
		t.Fatalf("tls mode after rejected changes = %q, %v", got.TLSMode, err)
	}

	// 带新 token 切到 pin（停用中不探测）；之后换指纹仍要 token，原样提交同一指纹沿用
	pinned.IsDisabled = true
	pinned.APIToken = fakeToken
	if _, err := f.panels.UpdateServer(ctx, f.actor, srv.ID, pinned, nil); err != nil {
		t.Fatalf("switch to pin with a new token: %v", err)
	}
	pinned.APIToken = ""
	if _, err := f.panels.UpdateServer(ctx, f.actor, srv.ID, pinned, nil); err != nil {
		t.Fatalf("resubmit the same pin: %v", err)
	}
	other := pinned
	other.CertPinSHA256 = pinB
	_, err = f.panels.UpdateServer(ctx, f.actor, srv.ID, other, nil)
	wantCode(t, err, CodeInvalidArgument)
}
