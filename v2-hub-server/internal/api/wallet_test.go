package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"v2-hub-server/internal/service"
)

// 管理员开商店、建带时长选项的套餐、生成充值码 → 用户注册并兑换 → 流水可见 → 管理员调整余额，全程走 HTTP（docs/03 §5.12–§5.15）。
func TestWalletEndpoints(t *testing.T) {
	const amount, adjust = 1000, 250
	srv, rbac := newAPIServer(t)
	if _, err := rbac.BootstrapSuperAdmin(context.Background(), "root", cred); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	root := newClient(t, srv.URL)
	root.login("root")
	alice := newClient(t, srv.URL)

	// 商店默认关闭：站点信息如实反映，兑换入口 403
	site := alice.do(http.MethodGet, "/api/user/site", nil)
	var info siteInfoVO
	if err := json.Unmarshal(site.Data, &info); err != nil || info.ShopEnabled || info.Currency != "CNY" {
		t.Fatalf("site before enabling shop = %+v, %v", info, err)
	}
	expectStatus(t, root.do(http.MethodPut, "/api/admin/settings", map[string]any{
		"register": map[string]any{"mode": service.RegisterOpen}, "shop": map[string]any{"enabled": true, "currency": "USD"},
	}), http.StatusOK, codeOK)
	expectStatus(t, root.do(http.MethodPut, "/api/admin/settings", map[string]any{"shop": map[string]any{"currency": "BTC"}}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))

	// 套餐：带时长选项，响应里有 prices、没有免费续期字段（Ruling-20）
	planRes := root.do(http.MethodPost, "/api/admin/plans", map[string]any{
		"name": "标准版", "trafficLimit": 1 << 30, "durationDays": 30, "nodeGroupIds": []int64{}, "isOnSale": true,
		"prices": []map[string]any{{"durationDays": 30, "price": 990}, {"durationDays": 365, "price": 9900}},
	})
	expectStatus(t, planRes, http.StatusOK, codeOK)
	var plan map[string]json.RawMessage
	if err := json.Unmarshal(planRes.Data, &plan); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	var prices []planPriceVO
	if err := json.Unmarshal(plan["prices"], &prices); err != nil || len(prices) != 2 || prices[0].Price != 990 || prices[1].DurationDays != 365 {
		t.Fatalf("plan prices = %+v, %v", prices, err)
	}
	if _, has := plan["renewEveryDays"]; has {
		t.Fatal("plan must no longer carry free-renewal fields")
	}

	// 充值码：明文只在生成时返回，列表里只有末 4 位
	gen := root.do(http.MethodPost, "/api/admin/topup-codes", map[string]any{"amount": amount, "count": 2, "batch": "api"})
	expectStatus(t, gen, http.StatusOK, codeOK)
	var batch topupBatchVO
	if err := json.Unmarshal(gen.Data, &batch); err != nil || len(batch.Codes) != 2 || batch.Amount != amount {
		t.Fatalf("topup batch = %+v, %v", batch, err)
	}
	list := root.do(http.MethodGet, "/api/admin/topup-codes?status=unused&batch=api", nil)
	expectStatus(t, list, http.StatusOK, codeOK)
	var codes topupCodePageVO
	if err := json.Unmarshal(list.Data, &codes); err != nil || codes.Total != 2 || len(codes.Items[0].CodeTail) != 4 {
		t.Fatalf("topup list = %+v, %v", codes, err)
	}
	expectStatus(t, root.do(http.MethodGet, "/api/admin/topup-codes?status=sold", nil), http.StatusBadRequest, string(service.CodeInvalidArgument))

	reg := alice.do(http.MethodPost, "/api/user/auth/register", map[string]any{"username": "alice", "email": "alice@example.com", "password": cred})
	expectStatus(t, reg, http.StatusOK, codeOK)
	var session userSessionVO
	if err := json.Unmarshal(reg.Data, &session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	// 不带 CSRF 头不能兑换
	expectStatus(t, alice.do(http.MethodPost, "/api/user/wallet/redeem", map[string]any{"code": batch.Codes[0]}),
		http.StatusForbidden, string(service.CodePermissionDenied))
	alice.csrf = session.CSRFToken
	expectStatus(t, alice.do(http.MethodPost, "/api/user/wallet/redeem", map[string]any{"code": "2222333344445555"}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))
	redeem := alice.do(http.MethodPost, "/api/user/wallet/redeem", map[string]any{"code": batch.Codes[0]})
	expectStatus(t, redeem, http.StatusOK, codeOK)
	var got redeemVO
	if err := json.Unmarshal(redeem.Data, &got); err != nil || got.Amount != amount || got.Balance != amount {
		t.Fatalf("redeem = %+v, %v", got, err)
	}

	// 管理员调整余额：结果不能为负；成功后返回的用户带新余额
	balancePath := fmt.Sprintf("/api/admin/users/%d/balance", session.User.ID)
	expectStatus(t, root.do(http.MethodPost, balancePath, map[string]any{"amount": -amount - 1, "remark": "扣过头"}), http.StatusConflict, string(service.CodeConflict))
	adjusted := root.do(http.MethodPost, balancePath, map[string]any{"amount": adjust, "remark": "活动补偿"})
	expectStatus(t, adjusted, http.StatusOK, codeOK)
	var user userVO
	if err := json.Unmarshal(adjusted.Data, &user); err != nil || user.Balance != amount+adjust {
		t.Fatalf("user after adjust = balance %d, %v", user.Balance, err)
	}

	// 用户侧流水：两条，不带管理端字段；仪表盘带余额
	ledger := alice.do(http.MethodGet, "/api/user/wallet/ledger", nil)
	expectStatus(t, ledger, http.StatusOK, codeOK)
	var own struct {
		Items []map[string]any `json:"items"`
		Total int64            `json:"total"`
	}
	if err := json.Unmarshal(ledger.Data, &own); err != nil || own.Total != 2 || own.Items[0]["kind"] != service.LedgerAdminAdjust {
		t.Fatalf("own ledger = %+v, %v", own, err)
	}
	for _, field := range []string{"userId", "username", "adminUsername"} {
		if _, has := own.Items[0][field]; has {
			t.Fatalf("user-side ledger must not expose %s", field)
		}
	}
	var dash dashboardVO
	if err := json.Unmarshal(alice.do(http.MethodGet, "/api/user/dashboard", nil).Data, &dash); err != nil || dash.Balance != amount+adjust {
		t.Fatalf("dashboard balance = %d, %v", dash.Balance, err)
	}
	all := root.do(http.MethodGet, fmt.Sprintf("/api/admin/wallet-ledger?userId=%d&kind=%s", session.User.ID, service.LedgerTopupCode), nil)
	expectStatus(t, all, http.StatusOK, codeOK)
	var admin ledgerPageVO
	if err := json.Unmarshal(all.Data, &admin); err != nil || admin.Total != 1 || admin.Items[0].Username == nil || *admin.Items[0].Username != "alice" {
		t.Fatalf("admin ledger = %+v, %v", admin, err)
	}

	// 查看明文须走单独接口；已使用的码不能删
	usedID := codes.Items[len(codes.Items)-1].ID
	reveal := root.do(http.MethodGet, fmt.Sprintf("/api/admin/topup-codes/%d/code", usedID), nil)
	expectStatus(t, reveal, http.StatusOK, codeOK)
	var secret topupSecretVO
	if err := json.Unmarshal(reveal.Data, &secret); err != nil || secret.Code != batch.Codes[0] {
		t.Fatalf("reveal = %v, want the first generated code", err)
	}
	expectStatus(t, root.do(http.MethodDelete, fmt.Sprintf("/api/admin/topup-codes/%d", usedID), nil), http.StatusConflict, string(service.CodeConflict))
}
