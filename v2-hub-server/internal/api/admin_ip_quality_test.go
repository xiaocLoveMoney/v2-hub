package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"v2-hub-server/internal/manager/ipquality"
	"v2-hub-server/internal/service"
)

// testIPQSKey 是接口用例里 IPQS 的假 key：设置接口只能报“已配置”，不能回显它。
const testIPQSKey = "ipqs-test-key-not-real"

// IP 质量检测接口（docs/03 §5.23）：不存在的服务器 / 记录 404，没有 server:read 403，
// 设置里带 ipQuality 分组与只读的数据源目录、不回显 key，非法间隔 400。检测本身由 service 层用例覆盖。
func TestIPQualityEndpoints(t *testing.T) {
	srv, rbac := newAPIServer(t)
	if _, err := rbac.BootstrapSuperAdmin(context.Background(), "root", cred); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	root := newClient(t, srv.URL)
	root.login("root")

	expectStatus(t, root.do(http.MethodGet, "/api/admin/servers/999999/ip-checks", nil), http.StatusNotFound, string(service.CodeNotFound))
	expectStatus(t, root.do(http.MethodPost, "/api/admin/servers/999999/ip-checks", nil), http.StatusNotFound, string(service.CodeNotFound))
	expectStatus(t, root.do(http.MethodGet, "/api/admin/ip-checks/999999", nil), http.StatusNotFound, string(service.CodeNotFound))
	expectStatus(t, root.do(http.MethodGet, "/api/admin/servers/1/ip-checks?pageSize=1000", nil), http.StatusBadRequest, string(service.CodeInvalidArgument))

	got := root.do(http.MethodGet, "/api/admin/settings", nil)
	expectStatus(t, got, http.StatusOK, codeOK)
	var settings settingsVO
	if err := json.Unmarshal(got.Data, &settings); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if settings.IPQuality.AutoEnabled || settings.IPQuality.IntervalDays != 7 || len(settings.IPQuality.Sources) != 6 {
		t.Fatalf("ipQuality = %+v", settings.IPQuality)
	}
	for _, src := range settings.IPQuality.Sources {
		wantConfigured := !src.NeedsKey || src.Key == ipquality.SourceIPQS
		if src.IsConfigured != wantConfigured {
			t.Errorf("source %+v", src)
		}
	}
	if strings.Contains(string(got.Data), testIPQSKey) {
		t.Fatal("settings must not echo the data source key")
	}
	expectStatus(t, root.do(http.MethodPut, "/api/admin/settings", map[string]any{"ipQuality": map[string]any{"intervalDays": 0}}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))
	updated := root.do(http.MethodPut, "/api/admin/settings", map[string]any{"ipQuality": map[string]any{"autoEnabled": true, "intervalDays": 14}})
	expectStatus(t, updated, http.StatusOK, codeOK)
	if err := json.Unmarshal(updated.Data, &settings); err != nil || !settings.IPQuality.AutoEnabled || settings.IPQuality.IntervalDays != 14 || len(settings.IPQuality.Sources) != 6 {
		t.Fatalf("updated ipQuality = %+v, %v", settings.IPQuality, err)
	}
	// 只读的数据源目录不能经接口改
	expectStatus(t, root.do(http.MethodPut, "/api/admin/settings", map[string]any{"ipQuality": map[string]any{"sources": []any{}}}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))

	expectStatus(t, root.do(http.MethodPost, "/api/admin/roles", map[string]any{"code": "host_viewer", "name": "host_viewer", "permissions": []string{service.PermHostRead}}), http.StatusOK, codeOK)
	expectStatus(t, root.do(http.MethodPost, "/api/admin/admins", map[string]any{"username": "host_viewer", "password": cred, "roles": []string{"host_viewer"}}), http.StatusOK, codeOK)
	viewer := newClient(t, srv.URL)
	viewer.login("host_viewer")
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/admin/servers/1/ip-checks"}, {http.MethodPost, "/api/admin/servers/1/ip-checks"}, {http.MethodGet, "/api/admin/ip-checks/1"},
	} {
		expectStatus(t, viewer.do(c.method, c.path, nil), http.StatusForbidden, string(service.CodePermissionDenied))
	}
}
