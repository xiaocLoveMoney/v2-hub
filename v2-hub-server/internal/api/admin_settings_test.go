package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"v2-hub-server/internal/service"
)

// publicSiteKeys 是 /api/user/site 允许暴露的字段数，多一个都算泄露设置。
const publicSiteKeys = 9

func TestSettingsEndpoints(t *testing.T) {
	srv, rbac := newAPIServer(t)
	if _, err := rbac.BootstrapSuperAdmin(context.Background(), "root", cred); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	root := newClient(t, srv.URL)
	root.login("root")

	got := root.do(http.MethodGet, "/api/admin/settings", nil)
	expectStatus(t, got, http.StatusOK, codeOK)
	var cur settingsVO
	if err := json.Unmarshal(got.Data, &cur); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if cur.MailEnabled || cur.Register.Mode != service.RegisterInvite || cur.Register.EmailAllow == nil {
		t.Fatalf("default settings = %+v", cur)
	}

	put := root.do(http.MethodPut, "/api/admin/settings", map[string]any{
		"register": map[string]any{"mode": service.RegisterOpen}, "site": map[string]any{"name": "测试站"},
	})
	expectStatus(t, put, http.StatusOK, codeOK)
	if err := json.Unmarshal(put.Data, &cur); err != nil || cur.Register.Mode != service.RegisterOpen || cur.Site.Name != "测试站" {
		t.Fatalf("updated settings = %+v, %v", cur, err)
	}
	expectStatus(t, root.do(http.MethodPut, "/api/admin/settings", map[string]any{"register": map[string]any{"mode": "anyone"}}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, root.do(http.MethodPut, "/api/admin/settings", map[string]any{"register": "open"}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))
	// 不认识的键：凭据类设置不在清单里，不能经接口写进设置表（Ruling-24）
	expectStatus(t, root.do(http.MethodPut, "/api/admin/settings", map[string]any{"mail": map[string]any{"smtpHost": "smtp.example.com"}}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))

	// 邮件未配置：测试邮件返回 feature_disabled，发件箱可查且为空
	expectStatus(t, root.do(http.MethodPost, "/api/admin/settings/test-mail", map[string]any{"to": "a@example.com"}),
		http.StatusForbidden, string(service.CodeFeatureDisabled))
	expectStatus(t, root.do(http.MethodGet, "/api/admin/mail-outbox?status=sent", nil), http.StatusOK, codeOK)
	expectStatus(t, root.do(http.MethodGet, "/api/admin/mail-outbox?status=nope", nil), http.StatusBadRequest, string(service.CodeInvalidArgument))

	// 只有 node:read 的管理员读写设置都应 403（R1）
	expectStatus(t, root.do(http.MethodPost, "/api/admin/roles",
		map[string]any{"code": "viewer", "name": "只读", "permissions": []string{service.PermNodeRead}}), http.StatusOK, codeOK)
	expectStatus(t, root.do(http.MethodPost, "/api/admin/admins",
		map[string]any{"username": "viewer1", "password": cred, "roles": []string{"viewer"}}), http.StatusOK, codeOK)
	viewer := newClient(t, srv.URL)
	viewer.login("viewer1")
	expectStatus(t, viewer.do(http.MethodGet, "/api/admin/settings", nil), http.StatusForbidden, string(service.CodePermissionDenied))
	expectStatus(t, viewer.do(http.MethodPut, "/api/admin/settings", map[string]any{"site": map[string]any{"name": "x"}}),
		http.StatusForbidden, string(service.CodePermissionDenied))
	expectStatus(t, viewer.do(http.MethodGet, "/api/admin/mail-outbox", nil), http.StatusForbidden, string(service.CodePermissionDenied))
}

func TestPublicSiteInfoAndEmailCode(t *testing.T) {
	srv, rbac := newAPIServer(t)
	if _, err := rbac.BootstrapSuperAdmin(context.Background(), "root", cred); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	anon := newClient(t, srv.URL)
	got := anon.do(http.MethodGet, "/api/user/site", nil)
	expectStatus(t, got, http.StatusOK, codeOK)
	var site siteInfoVO
	if err := json.Unmarshal(got.Data, &site); err != nil {
		t.Fatalf("decode site: %v", err)
	}
	// 设置里 emailVerify 默认为真，但邮件未配置，所以注册不要求验证码
	if site.SiteName == "" || site.RegisterMode != service.RegisterInvite || site.MailEnabled || site.EmailVerifyRequired {
		t.Fatalf("site info = %+v", site)
	}
	// 公开子集之外的设置不出现在响应里（docs/03 §5.6 公开子集：0.4.0 起多了商店开关与显示币种）
	var raw map[string]any
	if err := json.Unmarshal(got.Data, &raw); err != nil || len(raw) != publicSiteKeys {
		t.Fatalf("site info keys = %v, %v; want exactly the %d public ones", raw, err, publicSiteKeys)
	}
	expectStatus(t, anon.do(http.MethodPost, "/api/user/auth/email-code", map[string]any{"email": "a@example.com", "purpose": "register"}),
		http.StatusForbidden, string(service.CodeFeatureDisabled))
	expectStatus(t, anon.do(http.MethodPost, "/api/user/auth/email-code", map[string]any{"email": "a@example.com", "purpose": "bind"}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))
}
