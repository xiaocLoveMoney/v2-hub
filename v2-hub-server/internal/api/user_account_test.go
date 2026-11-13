package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"v2-hub-server/internal/service"
)

// 邀请注册 → 会话可用 → 邀请页 → 注销，全程走 HTTP（邮件未配置，所以不要邮箱验证码）。
func TestRegisterInviteAndDeleteAccountEndpoints(t *testing.T) {
	srv, rbac := newAPIServer(t)
	if _, err := rbac.BootstrapSuperAdmin(context.Background(), "root", cred); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	root := newClient(t, srv.URL)
	root.login("root")
	created := root.do(http.MethodPost, "/api/admin/invite-codes", map[string]any{"maxUses": 1, "isDisabled": false, "remark": "给朋友"})
	expectStatus(t, created, http.StatusOK, codeOK)
	var invite inviteCodeVO
	if err := json.Unmarshal(created.Data, &invite); err != nil || invite.Code == "" || invite.Kind != service.InviteKindAdmin {
		t.Fatalf("invite = %+v, %v", invite, err)
	}
	expectStatus(t, root.do(http.MethodGet, "/api/admin/invite-codes?kind=admin", nil), http.StatusOK, codeOK)
	expectStatus(t, root.do(http.MethodGet, "/api/admin/invite-codes?kind=nope", nil), http.StatusBadRequest, string(service.CodeInvalidArgument))

	alice := newClient(t, srv.URL)
	body := map[string]any{"username": "alice", "email": "alice@example.com", "password": cred}
	expectStatus(t, alice.do(http.MethodPost, "/api/user/auth/register", body), http.StatusBadRequest, string(service.CodeInvalidArgument))
	body["inviteCode"] = invite.Code
	reg := alice.do(http.MethodPost, "/api/user/auth/register", body)
	expectStatus(t, reg, http.StatusOK, codeOK)
	var session userSessionVO
	if err := json.Unmarshal(reg.Data, &session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	// 邮件未配置时不要求验证码，邮箱记为未验证
	if session.User.Username != "alice" || session.User.Email == nil || *session.User.Email != "alice@example.com" || session.User.IsEmailVerified || session.CSRFToken == "" {
		t.Fatalf("session after register = %+v", session)
	}
	alice.csrf = session.CSRFToken
	expectStatus(t, alice.do(http.MethodGet, portalMePath, nil), http.StatusOK, codeOK)

	// 码只有一次：第二个人注册失败
	bob := newClient(t, srv.URL)
	expectStatus(t, bob.do(http.MethodPost, "/api/user/auth/register",
		map[string]any{"username": "bob", "email": "bob@example.com", "password": cred, "inviteCode": invite.Code}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))

	got := alice.do(http.MethodGet, "/api/user/invite", nil)
	expectStatus(t, got, http.StatusOK, codeOK)
	var info inviteInfoVO
	if err := json.Unmarshal(got.Data, &info); err != nil || !info.Enabled || info.Code == "" || info.Invitees == nil {
		t.Fatalf("invite info = %+v, %v", info, err)
	}
	// 重置要带 CSRF 头
	csrf := alice.csrf
	alice.csrf = ""
	expectStatus(t, alice.do(http.MethodPost, "/api/user/invite/reset", nil), http.StatusForbidden, string(service.CodePermissionDenied))
	alice.csrf = csrf
	reset := alice.do(http.MethodPost, "/api/user/invite/reset", nil)
	expectStatus(t, reset, http.StatusOK, codeOK)
	var after inviteInfoVO
	if err := json.Unmarshal(reset.Data, &after); err != nil || after.Code == info.Code {
		t.Fatalf("invite after reset = %+v, %v", after, err)
	}

	// 邮件未配置：换绑与找回口令整体不可用
	expectStatus(t, alice.do(http.MethodPost, "/api/user/account/email-code", map[string]any{"email": "new@example.com"}),
		http.StatusForbidden, string(service.CodeFeatureDisabled))
	expectStatus(t, alice.do(http.MethodPost, "/api/user/auth/password/reset",
		map[string]any{"email": "alice@example.com", "emailCode": "123456", "newPassword": cred}), http.StatusForbidden, string(service.CodeFeatureDisabled))

	// 管理端能看到邮箱与邀请码用量
	users := root.do(http.MethodGet, "/api/admin/users?keyword=alice@example", nil)
	expectStatus(t, users, http.StatusOK, codeOK)
	var page pageVO[userVO]
	if err := json.Unmarshal(users.Data, &page); err != nil || page.Total != 1 || page.Items[0].Email == nil || *page.Items[0].Email != "alice@example.com" {
		t.Fatalf("admin users by email keyword = %+v, %v", page, err)
	}

	// 注销：口令错 400；成功后会话失效、用户名可再注册
	expectStatus(t, alice.do(http.MethodPost, "/api/user/account/delete", map[string]any{"currentPassword": "wrong-guess-123"}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, alice.do(http.MethodPost, "/api/user/account/delete", map[string]any{"currentPassword": cred}), http.StatusOK, codeOK)
	expectStatus(t, alice.do(http.MethodGet, portalMePath, nil), http.StatusUnauthorized, string(service.CodeUnauthenticated))
	expectStatus(t, root.do(http.MethodPut, "/api/admin/settings", map[string]any{"register": map[string]any{"mode": service.RegisterOpen}}), http.StatusOK, codeOK)
	again := newClient(t, srv.URL)
	expectStatus(t, again.do(http.MethodPost, "/api/user/auth/register", map[string]any{"username": "alice", "email": "alice@example.com", "password": cred}),
		http.StatusOK, codeOK)
}
