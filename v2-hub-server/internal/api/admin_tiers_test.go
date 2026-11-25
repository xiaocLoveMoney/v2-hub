package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"v2-hub-server/internal/manager/totp"
	"v2-hub-server/internal/service"
)

// pw2 是改密码用例里的新密码（仅测试库内有效）。
const pw2 = "another-horse-battery"

// 超管强制两步验证（R10）：未开启时只有豁免接口可用；开启后放行；超管不能关闭。
func TestSuperAdminMustBindTOTP(t *testing.T) {
	srv, rbac := newAPIServer(t)
	for _, name := range []string{"root", "root2"} {
		if _, err := rbac.BootstrapSuperAdmin(context.Background(), name, cred); err != nil {
			t.Fatalf("bootstrap %s: %v", name, err)
		}
	}
	c := newClient(t, srv.URL)
	s := c.login("root")
	if !s.TOTPSetupRequired || !s.IsSuperAdmin || !s.IsOwner || s.TOTPEnabled {
		t.Fatalf("session before binding = %+v", s)
	}
	setupRequired := string(service.CodeTOTPSetupRequired)
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/api/admin/servers"},
		{http.MethodGet, "/api/admin/dashboard"},
		{http.MethodGet, "/api/admin/roles"},
		{http.MethodPost, "/api/admin/auth/reauth"},
		{http.MethodPost, "/api/admin/auth/totp/disable"},
		{http.MethodPost, "/api/admin/roles"},
	} {
		expectStatus(t, c.do(r.method, r.path, map[string]any{}), http.StatusForbidden, setupRequired)
	}
	// 豁免：查看会话、改自己密码（这里旧密码错误，走到了 handler）、生成与确认 TOTP、登出
	expectStatus(t, c.do(http.MethodGet, "/api/admin/auth/me", nil), http.StatusOK, codeOK)
	expectStatus(t, c.do(http.MethodPut, "/api/admin/auth/password", map[string]string{"oldPassword": "wrong-guess-123", "newPassword": pw2}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))
	c.bindTOTP()
	if s = c.me(); s.TOTPSetupRequired || !s.TOTPEnabled {
		t.Fatalf("session after binding = %+v", s)
	}
	expectStatus(t, c.do(http.MethodGet, "/api/admin/servers", nil), http.StatusOK, codeOK)
	// 超管不能关闭两步验证（R10）
	c.clock.advance(totp.Period)
	expectStatus(t, c.do(http.MethodPost, "/api/admin/auth/totp/disable", map[string]string{"code": "123456"}), http.StatusConflict, string(service.CodeConflict))

	other := newClient(t, srv.URL)
	other.login("root2")
	expectStatus(t, other.do(http.MethodPost, "/api/admin/auth/logout", nil), http.StatusOK, codeOK)

	// 非超管不受 R10 约束
	expectStatus(t, c.do(http.MethodPost, "/api/admin/admins", map[string]any{"username": "agent", "password": cred, "roles": []string{service.RoleOperator}}),
		http.StatusOK, codeOK)
	agent := newClient(t, srv.URL)
	if s := agent.login("agent"); s.TOTPSetupRequired || s.IsSuperAdmin {
		t.Fatalf("operator session = %+v", s)
	}
	expectStatus(t, agent.do(http.MethodGet, "/api/admin/users", nil), http.StatusOK, codeOK)
}

// 二次验证（R12）：过期后高危接口 reauth_required，验证后恢复；没有权限的人先得到 permission_denied。
func TestReauthEndpoints(t *testing.T) {
	srv, rbac := newAPIServer(t)
	root := bootstrapRoot(t, srv, rbac)
	expectStatus(t, root.do(http.MethodPost, "/api/admin/admins", map[string]any{"username": "partner1", "password": cred, "roles": []string{service.RolePartner}}),
		http.StatusOK, codeOK)
	partner := newClient(t, srv.URL)
	partner.login("partner1")
	root.clock.advance(testReauthWindow + time.Second)

	role := map[string]any{"code": "viewer", "name": "只读", "permissions": []string{service.PermNodeRead}}
	expectStatus(t, root.do(http.MethodPost, "/api/admin/roles", role), http.StatusForbidden, string(service.CodeReauthRequired))
	expectStatus(t, root.do(http.MethodGet, "/api/admin/roles", nil), http.StatusOK, codeOK)
	expectStatus(t, partner.do(http.MethodPost, "/api/admin/roles", role), http.StatusForbidden, string(service.CodePermissionDenied))

	// 超管已开启两步验证：密码不行、错码不行、正确的码通过
	expectStatus(t, root.do(http.MethodPost, "/api/admin/auth/reauth", map[string]string{}), http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, root.do(http.MethodPost, "/api/admin/auth/reauth", map[string]string{"password": cred}), http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, root.do(http.MethodPost, "/api/admin/auth/reauth", map[string]string{"totpCode": "12345"}), http.StatusBadRequest, string(service.CodeInvalidArgument))
	v, _ := apiTOTPSeeds.Load(root.base + "|root")
	res := root.do(http.MethodPost, "/api/admin/auth/reauth", map[string]string{"totpCode": totp.CodeAt(v.([]byte), totp.Step(root.clock.now()))})
	expectStatus(t, res, http.StatusOK, codeOK)
	var vo adminReauthVO
	if err := json.Unmarshal(res.Data, &vo); err != nil || !vo.ReauthUntil.After(root.clock.now()) {
		t.Fatalf("reauth result = %+v, %v", vo, err)
	}
	expectStatus(t, root.do(http.MethodPost, "/api/admin/roles", role), http.StatusOK, codeOK)

	// 没开两步验证的非超管用密码验证；调余额属于高危接口
	balance := map[string]any{"amount": 100, "remark": "x"}
	expectStatus(t, partner.do(http.MethodPost, "/api/admin/users/999999/balance", balance), http.StatusForbidden, string(service.CodeReauthRequired))
	expectStatus(t, partner.do(http.MethodPost, "/api/admin/auth/reauth", map[string]string{"password": "wrong-guess-123"}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, partner.do(http.MethodPost, "/api/admin/auth/reauth", map[string]string{"password": cred}), http.StatusOK, codeOK)
	if res := partner.do(http.MethodPost, "/api/admin/users/999999/balance", balance); res.Body.Code == string(service.CodeReauthRequired) {
		t.Fatalf("still reauth_required after reauth: %+v", res.Body)
	}
}

// 本人改密码（R9）与管理员管理接口上的 R4 / R11，以及会话、管理员列表、权限码列表的新字段。
func TestOwnPasswordAndOwnerEndpoints(t *testing.T) {
	srv, rbac := newAPIServer(t)
	root := bootstrapRoot(t, srv, rbac)
	if _, err := rbac.BootstrapSuperAdmin(context.Background(), "root2", cred); err != nil {
		t.Fatalf("bootstrap root2: %v", err)
	}
	root2 := newClient(t, srv.URL)
	if s := root2.loginSuper("root2"); s.IsOwner {
		t.Fatal("root2 must not be the owner")
	}
	ownerID := root.me().Admin.ID

	// 所有者保护（R11）与不能经管理接口改自己的密码（R4）
	expectStatus(t, root2.do(http.MethodPut, fmt.Sprintf("/api/admin/admins/%d", ownerID), map[string]any{"roles": []string{service.RoleSuperAdmin}, "isDisabled": true}),
		http.StatusForbidden, string(service.CodePermissionDenied))
	expectStatus(t, root2.do(http.MethodPut, fmt.Sprintf("/api/admin/admins/%d/password", ownerID), map[string]string{"password": pw2}),
		http.StatusForbidden, string(service.CodePermissionDenied))
	expectStatus(t, root2.do(http.MethodDelete, fmt.Sprintf("/api/admin/admins/%d/totp", ownerID), nil),
		http.StatusForbidden, string(service.CodePermissionDenied))
	expectStatus(t, root.do(http.MethodPut, fmt.Sprintf("/api/admin/admins/%d/password", ownerID), map[string]string{"password": pw2}),
		http.StatusConflict, string(service.CodeConflict))

	list := root.do(http.MethodGet, "/api/admin/admins", nil)
	var page pageVO[adminVO]
	if err := json.Unmarshal(list.Data, &page); err != nil {
		t.Fatalf("decode admins: %v", err)
	}
	for _, a := range page.Items {
		if a.IsOwner != (a.ID == ownerID) {
			t.Fatalf("admin %s isOwner = %v", a.Username, a.IsOwner)
		}
	}
	perms := root.do(http.MethodGet, "/api/admin/permissions", nil)
	var pv []permissionVO
	if err := json.Unmarshal(perms.Data, &pv); err != nil {
		t.Fatalf("decode permissions: %v", err)
	}
	superOnly := 0
	for _, p := range pv {
		if p.SuperOnly != service.IsSuperOnlyPermission(p.Code) {
			t.Fatalf("permission %s superOnly = %v", p.Code, p.SuperOnly)
		}
		if p.SuperOnly {
			superOnly++
		}
	}
	if superOnly != len(service.SuperOnlyPermissions) {
		t.Fatalf("superOnly count = %d", superOnly)
	}

	// 本人改密码：缺旧密码 / 旧密码错 400；成功后当前会话保留、其它会话失效
	second := newClient(t, srv.URL)
	second.loginSuper("root2")
	expectStatus(t, root2.do(http.MethodPut, "/api/admin/auth/password", map[string]string{"newPassword": pw2}), http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, root2.do(http.MethodPut, "/api/admin/auth/password", map[string]string{"oldPassword": "wrong-guess-123", "newPassword": pw2}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, root2.do(http.MethodPut, "/api/admin/auth/password", map[string]string{"oldPassword": cred, "newPassword": pw2}), http.StatusOK, codeOK)
	expectStatus(t, root2.do(http.MethodGet, "/api/admin/auth/me", nil), http.StatusOK, codeOK)
	expectStatus(t, second.do(http.MethodGet, "/api/admin/auth/me", nil), http.StatusUnauthorized, string(service.CodeUnauthenticated))
}
