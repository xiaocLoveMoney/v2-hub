package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"v2-hub-server/internal/service"
)

// 运营接口走 HTTP（docs/03 §5.20）：总览按权限出区块、Telegram 测试未配置时 403、设置与站点信息的新分组、
// 公开套餐默认为空、收支按月与 CSV 导出。告警与日报的判定由 service 层用例覆盖。
func TestOpsEndpoints(t *testing.T) {
	srv, rbac := newAPIServer(t)
	if _, err := rbac.BootstrapSuperAdmin(context.Background(), "root", cred); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	root := newClient(t, srv.URL)
	root.login("root")

	res := root.do(http.MethodGet, "/api/admin/dashboard", nil)
	expectStatus(t, res, http.StatusOK, codeOK)
	var d adminDashboardVO
	if err := json.Unmarshal(res.Data, &d); err != nil || d.Users == nil || d.Traffic == nil || len(d.Traffic.Days) != 7 || d.Servers == nil || d.Finance == nil || d.Tickets == nil {
		t.Fatalf("super admin dashboard = %+v, %v", d, err)
	}

	expectStatus(t, root.do(http.MethodPost, "/api/admin/settings/test-telegram", nil), http.StatusForbidden, string(service.CodeFeatureDisabled))
	res = root.do(http.MethodPut, "/api/admin/settings", map[string]any{
		"landing": map[string]any{"enabled": true, "headline": "快速稳定", "intro": "欢迎"},
		"support": map[string]any{"url": "https://chat.example.com"},
		"alert":   map[string]any{"emails": []string{"ops@example.com"}},
	})
	expectStatus(t, res, http.StatusOK, codeOK)
	var settings settingsVO
	if err := json.Unmarshal(res.Data, &settings); err != nil || settings.TelegramEnabled || !settings.Landing.Enabled || settings.Alert.OfflineMinutes != 5 || settings.Support.Label != "在线客服" {
		t.Fatalf("settings = %+v, %v", settings, err)
	}
	expectStatus(t, root.do(http.MethodPut, "/api/admin/settings", map[string]any{"support": map[string]any{"url": "javascript:alert(1)"}}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))

	anon := newClient(t, srv.URL)
	res = anon.do(http.MethodGet, "/api/user/site", nil)
	var site siteInfoVO
	if err := json.Unmarshal(res.Data, &site); err != nil || site.Landing.Headline != "快速稳定" || site.Support.URL != "https://chat.example.com" {
		t.Fatalf("site = %+v, %v", site, err)
	}
	res = anon.do(http.MethodGet, "/api/user/site/plans", nil)
	expectStatus(t, res, http.StatusOK, codeOK)
	if strings.TrimSpace(string(res.Data)) != "[]" {
		t.Fatalf("shop is off, public plans should be empty: %s", res.Data)
	}

	res = root.do(http.MethodGet, "/api/admin/finance/stats?granularity=month&months=3", nil)
	expectStatus(t, res, http.StatusOK, codeOK)
	var stats financeStatsVO
	if err := json.Unmarshal(res.Data, &stats); err != nil || stats.Granularity != service.StatsByMonth || len(stats.Days) != 3 {
		t.Fatalf("monthly stats = %+v, %v", stats, err)
	}
	expectStatus(t, root.do(http.MethodGet, "/api/admin/finance/stats?granularity=week", nil), http.StatusBadRequest, string(service.CodeInvalidArgument))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/api/admin/finance/stats.csv?days=2", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := root.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "finance-day-") || len(strings.Split(strings.TrimSpace(string(body)), "\n")) != 4 {
		t.Fatalf("csv export = %d %v %q, %v", resp.StatusCode, resp.Header, body, err)
	}
}
