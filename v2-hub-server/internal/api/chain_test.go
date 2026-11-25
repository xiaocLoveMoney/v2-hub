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

// 示例链接里的用户名与密码是示例值，不对应任何真实节点。
const (
	chainTestLink     = "socks5://sample-user:sample-auth-0001@home.example.net:1080#home-socks"
	chainPath         = "/api/user/chain"
	chainRoutesPath   = "/api/user/chain/routes"
	chainExitsPath    = "/api/user/chain/exits"
	chainTestNodeName = "家宽落地"
)

// 链式代理接口走 HTTP（docs/03 §5.18、§5.21）：会话里的功能开关随套餐变化、套餐没开时写入 403、我的出口的增改删与取原链接、
// 规则与调顺序的请求校验、CSRF、他人资源 404（R8）。规则与订阅输出的细节由 service 层的用例覆盖（那里有带节点的面板夹具）。
func TestChainEndpoints(t *testing.T) {
	srv, rbac := newAPIServer(t)
	if _, err := rbac.BootstrapSuperAdmin(context.Background(), "root", cred); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	root := newClient(t, srv.URL)
	root.loginSuper("root")
	expectStatus(t, root.do(http.MethodPut, "/api/admin/settings", map[string]any{"register": map[string]any{"mode": service.RegisterOpen}}), http.StatusOK, codeOK)

	register := func(name string) (*apiClient, userSessionVO) {
		c := newClient(t, srv.URL)
		res := c.do(http.MethodPost, "/api/user/auth/register", map[string]any{"username": name, "email": name + "@example.com", "password": cred})
		expectStatus(t, res, http.StatusOK, codeOK)
		var s userSessionVO
		if err := json.Unmarshal(res.Data, &s); err != nil {
			t.Fatalf("decode session: %v", err)
		}
		return c, s
	}
	alice, session := register("alice")
	if session.Features.ClientChain || session.Features.CustomNode {
		t.Fatalf("a user without a plan must have no chain features: %+v", session.Features)
	}

	// 没有套餐：总览照常返回，写入 403；不带 CSRF 头先被拦下
	overview := func(c *apiClient) chainOverviewVO {
		res := c.do(http.MethodGet, chainPath, nil)
		expectStatus(t, res, http.StatusOK, codeOK)
		var o chainOverviewVO
		if err := json.Unmarshal(res.Data, &o); err != nil {
			t.Fatalf("decode overview: %v", err)
		}
		return o
	}
	o := overview(alice)
	isEmptyOverview := !o.Allowed && !o.ExitAllowed && o.MaxClientRoutes == service.MaxClientRoutes && o.MaxExits == service.MaxChainExits &&
		o.MaxRouteEntries == service.MaxRouteEntries && o.ClientRoutes != nil && o.ServerRoutes != nil && o.Exits != nil &&
		o.PlatformExits != nil && o.Nodes != nil
	if !isEmptyOverview {
		t.Fatalf("overview without plan = %+v", o)
	}
	routeBody := map[string]any{"kind": service.ChainKindClient, "entryNodeIds": []int64{1}, "exitKind": service.ChainExitNode, "exitId": 2}
	expectStatus(t, alice.do(http.MethodPost, chainExitsPath, map[string]any{"link": chainTestLink}), http.StatusForbidden, string(service.CodePermissionDenied))
	alice.csrf = session.CSRFToken
	expectStatus(t, alice.do(http.MethodPost, chainExitsPath, map[string]any{"link": chainTestLink}), http.StatusForbidden, string(service.CodeFeatureDisabled))
	expectStatus(t, alice.do(http.MethodPost, chainRoutesPath, routeBody), http.StatusForbidden, string(service.CodeFeatureDisabled))

	// 管理员建一个两个开关都开的套餐并分给 alice：会话里的开关随之变化
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
	me := alice.do(http.MethodGet, "/api/user/auth/me", nil)
	expectStatus(t, me, http.StatusOK, codeOK)
	if err := json.Unmarshal(me.Data, &session); err != nil || !session.Features.ClientChain || !session.Features.CustomNode {
		t.Fatalf("features after assigning the plan = %+v, %v", session.Features, err)
	}

	// 我的出口：增、改、取原链接、同名 409、坏链接 400
	expectStatus(t, alice.do(http.MethodPost, chainExitsPath, map[string]any{"link": "not a link"}), http.StatusBadRequest, string(service.CodeInvalidArgument))
	created := alice.do(http.MethodPost, chainExitsPath, map[string]any{"link": chainTestLink})
	expectStatus(t, created, http.StatusOK, codeOK)
	var node chainExitVO
	if err := json.Unmarshal(created.Data, &node); err != nil || node.Name != "home-socks" || node.Protocol != "socks" || node.Port != 1080 {
		t.Fatalf("exit = %+v, %v", node, err)
	}
	if raw := string(created.Data); strings.Contains(raw, "sample-auth-0001") || strings.Contains(raw, `"link"`) {
		t.Fatalf("exit response must not carry the link or its credential: %s", raw)
	}
	expectStatus(t, alice.do(http.MethodPost, chainExitsPath, map[string]any{"link": chainTestLink, "name": "home-socks"}), http.StatusConflict, string(service.CodeConflict))
	nodePath := fmt.Sprintf("%s/%d", chainExitsPath, node.ID)
	renamed := alice.do(http.MethodPut, nodePath, map[string]any{"name": chainTestNodeName})
	expectStatus(t, renamed, http.StatusOK, codeOK)
	if err := json.Unmarshal(renamed.Data, &node); err != nil || node.Name != chainTestNodeName || node.Protocol != "socks" {
		t.Fatalf("renamed exit = %+v, %v", node, err)
	}
	linkRes := alice.do(http.MethodGet, nodePath+"/link", nil)
	expectStatus(t, linkRes, http.StatusOK, codeOK)
	var link nodeLinkVO
	if err := json.Unmarshal(linkRes.Data, &link); err != nil || link.Link != chainTestLink {
		t.Fatalf("exit link mismatch: %v", err)
	}
	if o := overview(alice); !o.Allowed || !o.ExitAllowed || len(o.Exits) != 1 || o.Exits[0].RouteCount != 0 || !o.Exits[0].IsEnabled {
		t.Fatalf("overview with plan = %+v", o)
	}
	// 没有可用节点（套餐没绑节点组）：起飞节点不存在按 400；请求本身不合要求也是 400
	ownBody := map[string]any{"kind": service.ChainKindClient, "entryNodeIds": []int64{1}, "exitKind": service.ChainExitOwn, "exitId": node.ID}
	expectStatus(t, alice.do(http.MethodPost, chainRoutesPath, ownBody), http.StatusBadRequest, string(service.CodeInvalidArgument))
	for _, bad := range []map[string]any{
		{"kind": service.ChainKindClient, "entryNodeIds": []int64{}, "exitKind": service.ChainExitOwn, "exitId": node.ID},
		{"kind": "relay", "entryNodeIds": []int64{1}, "exitKind": service.ChainExitOwn, "exitId": node.ID},
		{"kind": service.ChainKindClient, "entryNodeIds": []int64{1}, "exitKind": "custom", "exitId": node.ID},
		{"kind": service.ChainKindClient, "entryNodeIds": []int64{1}},
	} {
		expectStatus(t, alice.do(http.MethodPost, chainRoutesPath, bad), http.StatusBadRequest, string(service.CodeInvalidArgument))
	}
	// 调顺序：ids 必须恰好是本人该类型的全部规则；不带 CSRF 被拦下
	orderPath := chainRoutesPath + "/order"
	expectStatus(t, alice.do(http.MethodPut, orderPath, map[string]any{"kind": service.ChainKindClient, "ids": []int64{1}}), http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, alice.do(http.MethodPut, orderPath, map[string]any{"kind": "relay", "ids": []int64{}}), http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, alice.do(http.MethodPut, chainRoutesPath+"/999999", routeBody), http.StatusNotFound, string(service.CodeNotFound))

	// 归属（R8）：bob 碰不到 alice 的出口
	bob, bobSession := register("bob")
	bob.csrf = bobSession.CSRFToken
	expectStatus(t, root.do(http.MethodPost, fmt.Sprintf("/api/admin/users/%d/plan", bobSession.User.ID), map[string]any{"planId": plan.ID}), http.StatusOK, codeOK)
	expectStatus(t, bob.do(http.MethodGet, nodePath+"/link", nil), http.StatusNotFound, string(service.CodeNotFound))
	expectStatus(t, bob.do(http.MethodPut, nodePath, map[string]any{"name": "x"}), http.StatusNotFound, string(service.CodeNotFound))
	expectStatus(t, bob.do(http.MethodDelete, nodePath, nil), http.StatusNotFound, string(service.CodeNotFound))
	expectStatus(t, bob.do(http.MethodDelete, chainRoutesPath+"/1", nil), http.StatusNotFound, string(service.CodeNotFound))
	if o := overview(bob); len(o.Exits) != 0 {
		t.Fatalf("bob must not see alice's exits: %+v", o.Exits)
	}

	expectStatus(t, alice.do(http.MethodDelete, nodePath, nil), http.StatusOK, codeOK)
	expectStatus(t, alice.do(http.MethodGet, nodePath+"/link", nil), http.StatusNotFound, string(service.CodeNotFound))
}
