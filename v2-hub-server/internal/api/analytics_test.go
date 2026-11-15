package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"v2-hub-server/internal/service"
)

// 总览分析与大陆连通检测接口走 HTTP（docs/03 §5.20，Ruling-66、Ruling-67）：权限码、days / range 的取值校验、
// 待处理分区按权限出块、连通检测没开时的 403。指标口径由 service 层用例覆盖。
func TestAnalyticsEndpoints(t *testing.T) {
	srv, rbac := newAPIServer(t)
	if _, err := rbac.BootstrapSuperAdmin(context.Background(), "root", cred); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	anon := newClient(t, srv.URL)
	expectStatus(t, anon.do(http.MethodGet, "/api/admin/analytics/users", nil), http.StatusUnauthorized, string(service.CodeUnauthenticated))
	expectStatus(t, anon.do(http.MethodGet, "/api/admin/analytics/backlog", nil), http.StatusUnauthorized, string(service.CodeUnauthenticated))

	root := newClient(t, srv.URL)
	root.login("root")
	res := root.do(http.MethodGet, "/api/admin/analytics/users?days=7", nil)
	expectStatus(t, res, http.StatusOK, codeOK)
	var users analyticsUsersVO
	if err := json.Unmarshal(res.Data, &users); err != nil || len(users.Growth) != 7 || len(users.ActiveDays) != 7 ||
		users.Plans == nil || users.TopUsers == nil || users.Subscription.Clients == nil || len(users.Subscription.Days) != 7 {
		t.Fatalf("users analytics = %+v, %v", users, err)
	}
	for _, path := range []string{
		"/api/admin/analytics/users?days=8", "/api/admin/analytics/users?days=abc", "/api/admin/analytics/users?days=365",
		"/api/admin/analytics/traffic?days=1", "/api/admin/analytics/finance?days=91", "/api/admin/analytics/servers?range=30d",
	} {
		expectStatus(t, root.do(http.MethodGet, path, nil), http.StatusBadRequest, string(service.CodeInvalidArgument))
	}

	res = root.do(http.MethodGet, "/api/admin/analytics/traffic", nil)
	expectStatus(t, res, http.StatusOK, codeOK)
	var traffic analyticsTrafficVO
	if err := json.Unmarshal(res.Data, &traffic); err != nil || len(traffic.Days) != 30 || len(traffic.Hours) != 24 || traffic.Servers == nil {
		t.Fatalf("traffic analytics = %+v, %v", traffic, err)
	}
	res = root.do(http.MethodGet, "/api/admin/analytics/servers?range=7d", nil)
	expectStatus(t, res, http.StatusOK, codeOK)
	var servers analyticsServersVO
	if err := json.Unmarshal(res.Data, &servers); err != nil || servers.Availability == nil || servers.Online == nil || servers.TopCPU == nil || servers.Reach != nil {
		t.Fatalf("servers analytics = %+v, %v", servers, err)
	}
	res = root.do(http.MethodGet, "/api/admin/analytics/finance?days=90", nil)
	expectStatus(t, res, http.StatusOK, codeOK)
	var finance analyticsFinanceVO
	if err := json.Unmarshal(res.Data, &finance); err != nil || len(finance.Days) != 90 || len(finance.OrderKinds) != 3 || finance.Plans == nil || finance.Coupons == nil {
		t.Fatalf("finance analytics = %+v, %v", finance, err)
	}
	res = root.do(http.MethodGet, "/api/admin/analytics/backlog", nil)
	expectStatus(t, res, http.StatusOK, codeOK)
	var backlog analyticsBacklogVO
	if err := json.Unmarshal(res.Data, &backlog); err != nil || backlog.Tickets == nil || backlog.Provisions == nil || backlog.Panels == nil || backlog.Outbox == nil {
		t.Fatalf("backlog = %+v, %v", backlog, err)
	}

	// 只有 user:read 的管理员：用户 / 流量分区可进且不带按服务器的流量，服务器 / 财务分区 403，待处理只出下发那一块
	expectStatus(t, root.do(http.MethodPost, "/api/admin/roles",
		map[string]any{"code": "support", "name": "客服", "permissions": []string{service.PermUserRead}}), http.StatusOK, codeOK)
	expectStatus(t, root.do(http.MethodPost, "/api/admin/admins",
		map[string]any{"username": "support1", "password": cred, "roles": []string{"support"}}), http.StatusOK, codeOK)
	support := newClient(t, srv.URL)
	support.login("support1")
	expectStatus(t, support.do(http.MethodGet, "/api/admin/analytics/users", nil), http.StatusOK, codeOK)
	res = support.do(http.MethodGet, "/api/admin/analytics/traffic?days=7", nil)
	expectStatus(t, res, http.StatusOK, codeOK)
	var limited analyticsTrafficVO
	if err := json.Unmarshal(res.Data, &limited); err != nil || limited.Servers != nil {
		t.Fatalf("traffic without server:read must not carry servers: %+v, %v", limited.Servers, err)
	}
	expectStatus(t, support.do(http.MethodGet, "/api/admin/analytics/servers", nil), http.StatusForbidden, string(service.CodePermissionDenied))
	expectStatus(t, support.do(http.MethodGet, "/api/admin/analytics/finance", nil), http.StatusForbidden, string(service.CodePermissionDenied))
	res = support.do(http.MethodGet, "/api/admin/analytics/backlog", nil)
	expectStatus(t, res, http.StatusOK, codeOK)
	var part analyticsBacklogVO
	if err := json.Unmarshal(res.Data, &part); err != nil || part.Provisions == nil || part.Tickets != nil || part.Panels != nil || part.Outbox != nil {
		t.Fatalf("user-only backlog = %+v, %v", part, err)
	}
}

func TestReachEndpoints(t *testing.T) {
	srv, rbac := newAPIServer(t)
	if _, err := rbac.BootstrapSuperAdmin(context.Background(), "root", cred); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	root := newClient(t, srv.URL)
	root.login("root")

	// 默认关：详情与手动检测 403，节点与服务器列表照常
	expectStatus(t, root.do(http.MethodGet, "/api/admin/nodes/1/reach", nil), http.StatusForbidden, string(service.CodeFeatureDisabled))
	expectStatus(t, root.do(http.MethodPost, "/api/admin/nodes/1/reach/check", nil), http.StatusForbidden, string(service.CodeFeatureDisabled))
	expectStatus(t, root.do(http.MethodGet, "/api/admin/nodes", nil), http.StatusOK, codeOK)
	expectStatus(t, root.do(http.MethodGet, "/api/admin/servers", nil), http.StatusOK, codeOK)

	res := root.do(http.MethodPut, "/api/admin/settings", map[string]any{"reach": map[string]any{"enabled": true, "intervalMinutes": 15}})
	expectStatus(t, res, http.StatusOK, codeOK)
	var settings settingsVO
	if err := json.Unmarshal(res.Data, &settings); err != nil || !settings.Reach.Enabled || settings.Reach.IntervalMinutes != 15 || !settings.Reach.Notify || settings.Reach.HasToken {
		t.Fatalf("reach settings = %+v, %v", settings.Reach, err)
	}
	expectStatus(t, root.do(http.MethodPut, "/api/admin/settings", map[string]any{"reach": map[string]any{"intervalMinutes": 5}}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))

	// 开了以后：不存在的节点 404，路径参数不合法 400
	expectStatus(t, root.do(http.MethodGet, "/api/admin/nodes/1/reach", nil), http.StatusNotFound, string(service.CodeNotFound))
	expectStatus(t, root.do(http.MethodPost, "/api/admin/nodes/1/reach/check", nil), http.StatusNotFound, string(service.CodeNotFound))
	expectStatus(t, root.do(http.MethodGet, "/api/admin/nodes/abc/reach", nil), http.StatusBadRequest, string(service.CodeInvalidArgument))
	res = root.do(http.MethodGet, "/api/admin/analytics/servers", nil)
	expectStatus(t, res, http.StatusOK, codeOK)
	var servers analyticsServersVO
	if err := json.Unmarshal(res.Data, &servers); err != nil || servers.Reach == nil || servers.Reach.Problems == nil {
		t.Fatalf("servers analytics with reach on = %+v, %v", servers.Reach, err)
	}

	// 只有 node:read：能看详情，不能发起检测
	expectStatus(t, root.do(http.MethodPost, "/api/admin/roles",
		map[string]any{"code": "viewer", "name": "只读", "permissions": []string{service.PermNodeRead}}), http.StatusOK, codeOK)
	expectStatus(t, root.do(http.MethodPost, "/api/admin/admins",
		map[string]any{"username": "viewer1", "password": cred, "roles": []string{"viewer"}}), http.StatusOK, codeOK)
	viewer := newClient(t, srv.URL)
	viewer.login("viewer1")
	expectStatus(t, viewer.do(http.MethodGet, "/api/admin/nodes/1/reach", nil), http.StatusNotFound, string(service.CodeNotFound))
	expectStatus(t, viewer.do(http.MethodPost, "/api/admin/nodes/1/reach/check", nil), http.StatusForbidden, string(service.CodePermissionDenied))
}
