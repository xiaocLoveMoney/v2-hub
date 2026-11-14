package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"v2-hub-server/internal/service"
)

// 示例链接里的 UUID 是示例值，不对应任何真实节点。
const (
	adminExitsPath  = "/api/admin/chain-exits"
	adminRoutesPath = "/api/admin/chain-routes"
	platformLink    = "vless://11111111-1111-4111-8111-111111111111@exit.example.net:443?type=tcp&security=tls&sni=exit.example.net#tokyo"
	platformName    = "平台出口-东京"
)

// 服务端链式与管理端链式接口走 HTTP（docs/03 §5.19、§5.21）：会话开关、套餐没开时写入 403、CSRF、不存在的资源 404、
// 管理端下发状态 / 重试 / 全站规则 / 出口管理的权限与校验。下发本身由 service 层的假面板用例覆盖。
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
	body := map[string]any{"kind": service.ChainKindServer, "entryNodeIds": []int64{1}, "exitKind": service.ChainExitNode, "exitId": 2}
	expectStatus(t, alice.do(http.MethodPost, chainRoutesPath, body), http.StatusForbidden, string(service.CodePermissionDenied))
	alice.csrf = session.CSRFToken
	expectStatus(t, alice.do(http.MethodPost, chainRoutesPath, body), http.StatusForbidden, string(service.CodeFeatureDisabled))
	expectStatus(t, alice.do(http.MethodDelete, chainRoutesPath+"/999999", nil), http.StatusNotFound, string(service.CodeNotFound))

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
	expectStatus(t, alice.do(http.MethodPost, chainRoutesPath, body), http.StatusBadRequest, string(service.CodeInvalidArgument))
	ov := alice.do(http.MethodGet, chainPath, nil)
	expectStatus(t, ov, http.StatusOK, codeOK)
	var o chainOverviewVO
	if err := json.Unmarshal(ov.Data, &o); err != nil || !o.ServerAllowed || o.MaxServerRoutes != service.MaxServerRoutes || o.ServerRoutes == nil {
		t.Fatalf("overview = %+v, %v", o, err)
	}

	// 管理端：不存在的服务器 404；全站规则为空；普通用户会话访问不了
	expectStatus(t, root.do(http.MethodGet, "/api/admin/servers/999999/chain-apply", nil), http.StatusNotFound, string(service.CodeNotFound))
	expectStatus(t, root.do(http.MethodPost, "/api/admin/servers/999999/chain-apply/retry", nil), http.StatusNotFound, string(service.CodeNotFound))
	list := root.do(http.MethodGet, adminRoutesPath+"?pageSize=10&kind=server", nil)
	expectStatus(t, list, http.StatusOK, codeOK)
	var page struct {
		Items []adminChainRouteVO `json:"items"`
		Total int64               `json:"total"`
	}
	if err := json.Unmarshal(list.Data, &page); err != nil || page.Total != 0 || page.Items == nil {
		t.Fatalf("admin list = %+v, %v", page, err)
	}
	expectStatus(t, root.do(http.MethodGet, adminRoutesPath+"?serverId=abc", nil), http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, root.do(http.MethodGet, adminRoutesPath+"?kind=relay", nil), http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, alice.do(http.MethodGet, adminRoutesPath, nil), http.StatusUnauthorized, string(service.CodeUnauthenticated))
}

// 管理端出口管理走 HTTP（docs/03 §5.21，Ruling-50）：平台出口增改删与取链接、用户出口只能停用 / 删除且不能看链接、
// 响应不带凭据、node:read 能看不能改、普通用户会话访问不了。
func TestAdminChainExitEndpoints(t *testing.T) {
	srv, rbac := newAPIServer(t)
	if _, err := rbac.BootstrapSuperAdmin(context.Background(), "root", cred); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	root := newClient(t, srv.URL)
	root.login("root")
	expectStatus(t, root.do(http.MethodPut, "/api/admin/settings", map[string]any{"register": map[string]any{"mode": service.RegisterOpen}}), http.StatusOK, codeOK)

	// 平台出口：坏输入 400、新建、同名 409、响应不带链接、取链接要单独请求
	input := map[string]any{"name": platformName, "link": platformLink, "allowServer": true, "allowClient": false, "nodeGroupIds": []int64{}}
	expectStatus(t, root.do(http.MethodPost, adminExitsPath, map[string]any{"name": platformName, "link": "not a link", "allowServer": true, "allowClient": true, "nodeGroupIds": []int64{}}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, root.do(http.MethodPost, adminExitsPath, map[string]any{"name": platformName, "link": platformLink, "allowServer": false, "allowClient": false, "nodeGroupIds": []int64{}}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))
	created := root.do(http.MethodPost, adminExitsPath, input)
	expectStatus(t, created, http.StatusOK, codeOK)
	var exit adminChainExitVO
	if err := json.Unmarshal(created.Data, &exit); err != nil {
		t.Fatalf("decode exit: %v", err)
	}
	isPlatform := exit.Owner == service.ExitOwnerPlatform && exit.User == nil && exit.Address == "exit.example.net" && exit.Port == 443 &&
		exit.IsEnabled && exit.AllowServer && !exit.AllowClient && exit.NodeGroups != nil
	if !isPlatform {
		t.Fatalf("platform exit = %+v", exit)
	}
	if raw := string(created.Data); strings.Contains(raw, "11111111-1111") || strings.Contains(raw, `"link"`) {
		t.Fatalf("exit response must not carry the link: %s", raw)
	}
	expectStatus(t, root.do(http.MethodPost, adminExitsPath, input), http.StatusConflict, string(service.CodeConflict))
	exitPath := fmt.Sprintf("%s/%d", adminExitsPath, exit.ID)
	linkRes := root.do(http.MethodGet, exitPath+"/link", nil)
	expectStatus(t, linkRes, http.StatusOK, codeOK)
	var link nodeLinkVO
	if err := json.Unmarshal(linkRes.Data, &link); err != nil || link.Link != platformLink {
		t.Fatalf("platform exit link mismatch: %v", err)
	}
	updated := root.do(http.MethodPut, exitPath, map[string]any{
		"name": platformName, "remark": "备用", "isEnabled": true, "allowServer": true, "allowClient": true, "nodeGroupIds": []int64{},
	})
	expectStatus(t, updated, http.StatusOK, codeOK)
	if err := json.Unmarshal(updated.Data, &exit); err != nil || exit.Remark != "备用" || !exit.AllowClient || exit.Address != "exit.example.net" {
		t.Fatalf("updated platform exit = %+v, %v", exit, err)
	}
	expectStatus(t, root.do(http.MethodPut, exitPath, map[string]any{
		"name": platformName, "remark": "", "isEnabled": true, "allowServer": true, "allowClient": true, "nodeGroupIds": []int64{999999},
	}), http.StatusBadRequest, string(service.CodeInvalidArgument))

	// 用户出口：管理员能停用 / 启用 / 删除，不能改、不能看链接
	alice := newClient(t, srv.URL)
	res := alice.do(http.MethodPost, "/api/user/auth/register", map[string]any{"username": "alice", "email": "alice@example.com", "password": cred})
	expectStatus(t, res, http.StatusOK, codeOK)
	var session userSessionVO
	if err := json.Unmarshal(res.Data, &session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	alice.csrf = session.CSRFToken
	planRes := root.do(http.MethodPost, "/api/admin/plans", map[string]any{
		"name": "链式版", "trafficLimit": 1 << 30, "durationDays": 30, "nodeGroupIds": []int64{}, "allowClientChain": true, "allowCustomNode": true,
	})
	expectStatus(t, planRes, http.StatusOK, codeOK)
	var plan struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(planRes.Data, &plan); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	expectStatus(t, root.do(http.MethodPost, fmt.Sprintf("/api/admin/users/%d/plan", session.User.ID), map[string]any{"planId": plan.ID}), http.StatusOK, codeOK)
	ownRes := alice.do(http.MethodPost, chainExitsPath, map[string]any{"link": chainTestLink})
	expectStatus(t, ownRes, http.StatusOK, codeOK)
	var own chainExitVO
	if err := json.Unmarshal(ownRes.Data, &own); err != nil {
		t.Fatalf("decode own exit: %v", err)
	}
	ownPath := fmt.Sprintf("%s/%d", adminExitsPath, own.ID)

	// 延迟测试（Ruling-52）：这里只验校验、归属与权限，真正跑测试由 service 层的假面板用例覆盖
	const missingID = 999999
	ownTestPath := fmt.Sprintf("%s/%d/test", chainExitsPath, own.ID)
	expectStatus(t, alice.do(http.MethodPost, ownTestPath, map[string]any{"nodeId": 1, "mode": "icmp"}), http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, alice.do(http.MethodPost, ownTestPath, map[string]any{"nodeId": missingID, "mode": "tcp"}), http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, alice.do(http.MethodPost, fmt.Sprintf("%s/%d/test", chainExitsPath, exit.ID), map[string]any{"nodeId": 1, "mode": "tcp"}),
		http.StatusNotFound, string(service.CodeNotFound))
	expectStatus(t, root.do(http.MethodPost, exitPath+"/test", map[string]any{"serverId": missingID, "mode": "icmp"}), http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, root.do(http.MethodPost, exitPath+"/test", map[string]any{"serverId": missingID, "mode": "tcp"}), http.StatusNotFound, string(service.CodeNotFound))
	expectStatus(t, root.do(http.MethodPost, fmt.Sprintf("%s/%d/test", adminExitsPath, missingID), map[string]any{"serverId": 1, "mode": "tcp"}),
		http.StatusNotFound, string(service.CodeNotFound))
	expectStatus(t, alice.do(http.MethodPost, exitPath+"/test", map[string]any{"serverId": 1, "mode": "tcp"}), http.StatusUnauthorized, string(service.CodeUnauthenticated))
	expectStatus(t, root.do(http.MethodGet, ownPath+"/link", nil), http.StatusNotFound, string(service.CodeNotFound))
	expectStatus(t, root.do(http.MethodPut, ownPath, map[string]any{
		"name": "x", "remark": "", "isEnabled": true, "allowServer": true, "allowClient": true, "nodeGroupIds": []int64{},
	}), http.StatusBadRequest, string(service.CodeInvalidArgument))
	disabled := root.do(http.MethodPut, ownPath+"/enabled", map[string]any{"isEnabled": false})
	expectStatus(t, disabled, http.StatusOK, codeOK)
	var ownAdmin adminChainExitVO
	if err := json.Unmarshal(disabled.Data, &ownAdmin); err != nil || ownAdmin.IsEnabled || ownAdmin.Owner != service.ExitOwnerUser || ownAdmin.User == nil || ownAdmin.User.Username != "alice" {
		t.Fatalf("disabled user exit = %+v, %v", ownAdmin, err)
	}
	// 用户不能碰平台出口
	expectStatus(t, alice.do(http.MethodGet, fmt.Sprintf("%s/%d/link", chainExitsPath, exit.ID), nil), http.StatusNotFound, string(service.CodeNotFound))
	expectStatus(t, alice.do(http.MethodDelete, fmt.Sprintf("%s/%d", chainExitsPath, exit.ID), nil), http.StatusNotFound, string(service.CodeNotFound))
	expectStatus(t, alice.do(http.MethodGet, adminExitsPath, nil), http.StatusUnauthorized, string(service.CodeUnauthenticated))

	// 列表与筛选
	var page struct {
		Items []adminChainExitVO `json:"items"`
		Total int64              `json:"total"`
	}
	list := root.do(http.MethodGet, adminExitsPath+"?owner=user", nil)
	expectStatus(t, list, http.StatusOK, codeOK)
	if err := json.Unmarshal(list.Data, &page); err != nil || page.Total != 1 || page.Items[0].ID != own.ID {
		t.Fatalf("list user exits = %+v, %v", page, err)
	}
	expectStatus(t, root.do(http.MethodGet, adminExitsPath+"?owner=nobody", nil), http.StatusBadRequest, string(service.CodeInvalidArgument))

	// node:read 能看不能改
	expectStatus(t, root.do(http.MethodPost, "/api/admin/roles",
		map[string]any{"code": "node_viewer", "name": "节点只读", "permissions": []string{service.PermNodeRead}}), http.StatusOK, codeOK)
	expectStatus(t, root.do(http.MethodPost, "/api/admin/admins",
		map[string]any{"username": "viewer", "password": cred, "roles": []string{"node_viewer"}}), http.StatusOK, codeOK)
	viewer := newClient(t, srv.URL)
	viewer.login("viewer")
	expectStatus(t, viewer.do(http.MethodGet, adminExitsPath, nil), http.StatusOK, codeOK)
	expectStatus(t, viewer.do(http.MethodGet, exitPath+"/link", nil), http.StatusForbidden, string(service.CodePermissionDenied))
	expectStatus(t, viewer.do(http.MethodPut, ownPath+"/enabled", map[string]any{"isEnabled": true}), http.StatusForbidden, string(service.CodePermissionDenied))
	expectStatus(t, viewer.do(http.MethodDelete, exitPath, nil), http.StatusForbidden, string(service.CodePermissionDenied))
	expectStatus(t, viewer.do(http.MethodPost, exitPath+"/test", map[string]any{"serverId": 1, "mode": "tcp"}), http.StatusForbidden, string(service.CodePermissionDenied))
	expectStatus(t, viewer.do(http.MethodGet, adminRoutesPath, nil), http.StatusForbidden, string(service.CodePermissionDenied))

	// 删除：用户出口与平台出口都可以
	expectStatus(t, root.do(http.MethodDelete, ownPath, nil), http.StatusOK, codeOK)
	expectStatus(t, root.do(http.MethodDelete, exitPath, nil), http.StatusOK, codeOK)
	expectStatus(t, root.do(http.MethodDelete, exitPath, nil), http.StatusNotFound, string(service.CodeNotFound))
}
