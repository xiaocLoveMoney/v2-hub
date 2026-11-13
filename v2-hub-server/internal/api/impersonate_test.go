package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"v2-hub-server/internal/service"
)

// 切换为用户身份（docs/03 §5.17）：没有独立权限码签不了票；票据一次性；代入会话只读，退出时记审计。
func TestImpersonateEndpoints(t *testing.T) {
	srv, rbac := newAPIServer(t)
	ctx := context.Background()
	if _, err := rbac.BootstrapSuperAdmin(ctx, "root", cred); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	root := newClient(t, srv.URL)
	root.login("root")
	expectStatus(t, root.do(http.MethodPut, "/api/admin/settings", map[string]any{
		"register": map[string]any{"mode": service.RegisterOpen}, "site": map[string]any{"portalUrl": "https://user.example.com/"},
	}), http.StatusOK, codeOK)

	alice := newClient(t, srv.URL)
	reg := alice.do(http.MethodPost, "/api/user/auth/register", map[string]any{"username": "alice", "email": "alice@example.com", "password": cred})
	expectStatus(t, reg, http.StatusOK, codeOK)
	var own userSessionVO
	if err := json.Unmarshal(reg.Data, &own); err != nil || own.Impersonated {
		t.Fatalf("own session = %+v, %v", own, err)
	}
	userID := own.User.ID

	// 只有 user:write 的管理员签不了票：切换身份是独立权限码
	role := root.do(http.MethodPost, "/api/admin/roles", map[string]any{"code": "support", "name": "客服", "permissions": []string{service.PermUserRead, service.PermUserWrite}})
	expectStatus(t, role, http.StatusOK, codeOK)
	expectStatus(t, root.do(http.MethodPost, "/api/admin/admins", map[string]any{"username": "support1", "password": cred, "roles": []string{"support"}}), http.StatusOK, codeOK)
	support := newClient(t, srv.URL)
	support.login("support1")
	impersonatePath := fmt.Sprintf("/api/admin/users/%d/impersonate", userID)
	expectStatus(t, support.do(http.MethodPost, impersonatePath, nil), http.StatusForbidden, string(service.CodePermissionDenied))
	expectStatus(t, root.do(http.MethodPost, "/api/admin/users/999999/impersonate", nil), http.StatusNotFound, string(service.CodeNotFound))

	issued := root.do(http.MethodPost, impersonatePath, nil)
	expectStatus(t, issued, http.StatusOK, codeOK)
	var ticket impersonateTicketVO
	if err := json.Unmarshal(issued.Data, &ticket); err != nil || len(ticket.Ticket) < 32 || ticket.PortalURL != "https://user.example.com" {
		t.Fatalf("ticket = portalUrl %q, %d chars, %v", ticket.PortalURL, len(ticket.Ticket), err)
	}

	viewer := newClient(t, srv.URL)
	expectStatus(t, viewer.do(http.MethodPost, "/api/user/auth/impersonate", map[string]any{"ticket": "0123456789012345678901234567890123456789"}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))
	redeemed := viewer.do(http.MethodPost, "/api/user/auth/impersonate", map[string]any{"ticket": ticket.Ticket})
	expectStatus(t, redeemed, http.StatusOK, codeOK)
	var session userSessionVO
	if err := json.Unmarshal(redeemed.Data, &session); err != nil || !session.Impersonated || session.User.ID != userID || session.User.Username != "alice" {
		t.Fatalf("impersonated session = %+v, %v", session, err)
	}
	viewer.csrf = session.CSRFToken
	// 票据只能用一次
	expectStatus(t, newClient(t, srv.URL).do(http.MethodPost, "/api/user/auth/impersonate", map[string]any{"ticket": ticket.Ticket}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))

	// 只读：能看，不能写
	expectStatus(t, viewer.do(http.MethodGet, "/api/user/dashboard", nil), http.StatusOK, codeOK)
	me := viewer.do(http.MethodGet, "/api/user/auth/me", nil)
	if err := json.Unmarshal(me.Data, &session); err != nil || !session.Impersonated {
		t.Fatalf("me under impersonation = %+v, %v", session, err)
	}
	for _, write := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPut, "/api/user/auth/password", map[string]any{"oldPassword": cred, "newPassword": cred + "x"}},
		{http.MethodPost, "/api/user/subscription/reset", nil},
		{http.MethodPost, "/api/user/account/delete", map[string]any{"currentPassword": cred}},
		{http.MethodPost, "/api/user/tickets", map[string]any{"subject": "s", "content": "c"}},
		{http.MethodPost, "/api/user/wallet/redeem", map[string]any{"code": "2222333344445555"}},
	} {
		expectStatus(t, viewer.do(write.method, write.path, write.body), http.StatusForbidden, string(service.CodePermissionDenied))
	}
	// 用户自己的会话不受影响，仍可写
	alice.csrf = own.CSRFToken
	expectStatus(t, alice.do(http.MethodPost, "/api/user/tickets", map[string]any{"subject": "自己提的", "content": "正文"}), http.StatusOK, codeOK)

	// 代入会话可以退出；开始与结束各有一条审计，且不出现在用户的“最近登录”里
	expectStatus(t, viewer.do(http.MethodPost, "/api/user/auth/logout", nil), http.StatusOK, codeOK)
	expectStatus(t, viewer.do(http.MethodGet, "/api/user/dashboard", nil), http.StatusUnauthorized, string(service.CodeUnauthenticated))
	logs := root.do(http.MethodGet, "/api/admin/audit-logs?pageSize=100", nil)
	expectStatus(t, logs, http.StatusOK, codeOK)
	var page struct {
		Items []struct {
			Action   string `json:"action"`
			TargetID string `json:"targetId"`
		} `json:"items"`
	}
	if err := json.Unmarshal(logs.Data, &page); err != nil {
		t.Fatalf("decode audit logs: %v", err)
	}
	seen := map[string]int{}
	for _, l := range page.Items {
		if l.TargetID == fmt.Sprint(userID) {
			seen[l.Action]++
		}
	}
	if seen[service.ActionImpersonateStart] != 1 || seen[service.ActionImpersonateEnd] != 1 {
		t.Fatalf("impersonation audit = %v, want one start and one end", seen)
	}
	history := alice.do(http.MethodGet, "/api/user/login-logs", nil)
	var logins []map[string]any
	if err := json.Unmarshal(history.Data, &logins); err != nil || len(logins) != 0 {
		t.Fatalf("user login logs = %v, %v; impersonation must not appear there", logins, err)
	}

	// 停用的用户不能被代入
	expectStatus(t, root.do(http.MethodPut, fmt.Sprintf("/api/admin/users/%d", userID), map[string]any{"remark": "", "isDisabled": true}), http.StatusOK, codeOK)
	expectStatus(t, root.do(http.MethodPost, impersonatePath, nil), http.StatusConflict, string(service.CodeConflict))
}
