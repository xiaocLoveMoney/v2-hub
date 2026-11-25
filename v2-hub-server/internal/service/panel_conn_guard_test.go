package service

import (
	"context"
	"testing"
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
