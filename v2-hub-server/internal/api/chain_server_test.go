package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"v2-hub-server/internal/service"
)

const serverRulesPath = "/api/user/chain/server-rules"

// 服务端链式接口走 HTTP（docs/03 §5.19）：会话开关、套餐没开时写入 403、CSRF、他人与不存在的资源 404、
// 管理端下发状态 / 重试 / 全站规则的权限与 404。下发本身由 service 层的假面板用例覆盖。
func TestServerChainEndpoints(t *testing.T) {
	srv, rbac := newAPIServer(t)
	if _, err := rbac.BootstrapSuperAdmin(context.Background(), "root", cred); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	root := newClient(t, srv.URL)
	root.login("root")
	expectStatus(t, root.do(http.MethodPut, "/api/admin/settings", map[string]any{"register": map[string]any{"mode": service.RegisterOpen}}), http.StatusOK, codeOK)

	alice := newClient(t, srv.URL)
	res := alice.do(http.MethodPost, "/api/user/auth/register", map[string]any{"username": "alice", "email": "alice@example.com", "password": cred})
	expectStatus(t, res, http.StatusOK, codeOK)
	var session userSessionVO
	if err := json.Unmarshal(res.Data, &session); err != nil || session.Features.ServerChain {
		t.Fatalf("a user without a plan must not have server chain: %+v, %v", session.Features, err)
	}

	// 没开：写入 403 feature_disabled；不带 CSRF 先被拦下；改删不存在的规则 404
	body := map[string]any{"entryNodeId": 1, "exitNodeId": 2}
	expectStatus(t, alice.do(http.MethodPost, serverRulesPath, body), http.StatusForbidden, string(service.CodePermissionDenied))
	alice.csrf = session.CSRFToken
	expectStatus(t, alice.do(http.MethodPost, serverRulesPath, body), http.StatusForbidden, string(service.CodeFeatureDisabled))
	expectStatus(t, alice.do(http.MethodDelete, serverRulesPath+"/999999", nil), http.StatusNotFound, string(service.CodeNotFound))

	// 开了服务端链式的套餐：会话开关打开；没有可用节点时新建 400
	planRes := root.do(http.MethodPost, "/api/admin/plans", map[string]any{
		"name": "中转版", "trafficLimit": 1 << 30, "durationDays": 30, "nodeGroupIds": []int64{}, "allowServerChain": true,
	})
	expectStatus(t, planRes, http.StatusOK, codeOK)
	var plan struct {
		ID               int64 `json:"id"`
		AllowServerChain bool  `json:"allowServerChain"`
	}
	if err := json.Unmarshal(planRes.Data, &plan); err != nil || !plan.AllowServerChain {
		t.Fatalf("plan should echo allowServerChain: %+v, %v", plan, err)
	}
	expectStatus(t, root.do(http.MethodPost, fmt.Sprintf("/api/admin/users/%d/plan", session.User.ID), map[string]any{"planId": plan.ID}), http.StatusOK, codeOK)
	me := alice.do(http.MethodGet, "/api/user/auth/me", nil)
	if err := json.Unmarshal(me.Data, &session); err != nil || !session.Features.ServerChain {
		t.Fatalf("features after assigning the plan = %+v, %v", session.Features, err)
	}
	expectStatus(t, alice.do(http.MethodPost, serverRulesPath, body), http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, alice.do(http.MethodPut, serverRulesPath+"/999999", map[string]any{"isEnabled": false}), http.StatusNotFound, string(service.CodeNotFound))
	ov := alice.do(http.MethodGet, chainPath, nil)
	expectStatus(t, ov, http.StatusOK, codeOK)
	var o chainOverviewVO
	if err := json.Unmarshal(ov.Data, &o); err != nil || !o.ServerAllowed || o.MaxServerRules != service.MaxServerChainRules || o.ServerRules == nil {
		t.Fatalf("overview = %+v, %v", o, err)
	}

	// 管理端：不存在的服务器 404；全站列表为空；普通用户会话访问不了
	expectStatus(t, root.do(http.MethodGet, "/api/admin/servers/999999/chain-apply", nil), http.StatusNotFound, string(service.CodeNotFound))
	expectStatus(t, root.do(http.MethodPost, "/api/admin/servers/999999/chain-apply/retry", nil), http.StatusNotFound, string(service.CodeNotFound))
	list := root.do(http.MethodGet, "/api/admin/server-chain-rules?pageSize=10", nil)
	expectStatus(t, list, http.StatusOK, codeOK)
	var page struct {
		Items []adminServerChainRuleVO `json:"items"`
		Total int64                    `json:"total"`
	}
	if err := json.Unmarshal(list.Data, &page); err != nil || page.Total != 0 || page.Items == nil {
		t.Fatalf("admin list = %+v, %v", page, err)
	}
	expectStatus(t, root.do(http.MethodGet, "/api/admin/server-chain-rules?serverId=abc", nil), http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, alice.do(http.MethodGet, "/api/admin/server-chain-rules", nil), http.StatusUnauthorized, string(service.CodeUnauthenticated))
}
