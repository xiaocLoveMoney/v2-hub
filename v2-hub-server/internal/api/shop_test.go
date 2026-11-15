package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"v2-hub-server/internal/service"
)

// 管理员上架套餐与流量包、建优惠码 → 用户看商店、询价、余额不足、充值后下单、开自动续费 → 管理端看到订单，全程走 HTTP（docs/03 §5.12–§5.16）。
func TestShopEndpoints(t *testing.T) {
	const price, topup = 990, 1000
	srv, rbac := newAPIServer(t)
	if _, err := rbac.BootstrapSuperAdmin(context.Background(), "root", cred); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	root := newClient(t, srv.URL)
	root.login("root")
	expectStatus(t, root.do(http.MethodPut, "/api/admin/settings", map[string]any{
		"register": map[string]any{"mode": service.RegisterOpen}, "shop": map[string]any{"enabled": true},
	}), http.StatusOK, codeOK)

	planRes := root.do(http.MethodPost, "/api/admin/plans", map[string]any{
		"name": "标准版", "trafficLimit": 1 << 30, "durationDays": 30, "nodeGroupIds": []int64{}, "isOnSale": true, "description": "1GB / 月",
		"prices": []map[string]any{{"durationDays": 30, "price": price}},
	})
	expectStatus(t, planRes, http.StatusOK, codeOK)
	var plan planVO
	if err := json.Unmarshal(planRes.Data, &plan); err != nil || len(plan.Prices) != 1 {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	priceID := plan.Prices[0].ID
	expectStatus(t, root.do(http.MethodPost, "/api/admin/traffic-packs", map[string]any{"name": "加油包", "trafficBytes": 1 << 30, "price": 300, "isOnSale": true}),
		http.StatusOK, codeOK)
	expectStatus(t, root.do(http.MethodPost, "/api/admin/traffic-packs", map[string]any{"name": "坏包", "trafficBytes": 0, "price": 300, "isOnSale": true}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))
	couponRes := root.do(http.MethodPost, "/api/admin/coupons", map[string]any{"code": "welcome10", "kind": "percent", "value": 10, "allPlans": true, "isDisabled": false})
	expectStatus(t, couponRes, http.StatusOK, codeOK)
	var coupon couponVO
	if err := json.Unmarshal(couponRes.Data, &coupon); err != nil || coupon.Code != "WELCOME10" || coupon.MaxUsesPerUser == nil || *coupon.MaxUsesPerUser != 1 {
		t.Fatalf("coupon = %+v, %v", coupon, err)
	}

	alice := newClient(t, srv.URL)
	reg := alice.do(http.MethodPost, "/api/user/auth/register", map[string]any{"username": "alice", "email": "alice@example.com", "password": cred})
	expectStatus(t, reg, http.StatusOK, codeOK)
	var session userSessionVO
	if err := json.Unmarshal(reg.Data, &session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	alice.csrf = session.CSRFToken

	shopRes := alice.do(http.MethodGet, "/api/user/shop", nil)
	expectStatus(t, shopRes, http.StatusOK, codeOK)
	var shop shopVO
	if err := json.Unmarshal(shopRes.Data, &shop); err != nil || len(shop.Plans) != 1 || !shop.Plans[0].CanBuy || shop.Plans[0].Prices[0].Price != price ||
		len(shop.TrafficPacks) != 1 || shop.CanBuyTrafficPack || shop.TrafficPackReason == "" {
		t.Fatalf("shop = %+v, %v", shop, err)
	}

	order := map[string]any{"kind": "plan", "planPriceId": priceID, "couponCode": "welcome10"}
	quoteRes := alice.do(http.MethodPost, "/api/user/shop/quote", order)
	expectStatus(t, quoteRes, http.StatusOK, codeOK)
	var quote quoteVO
	if err := json.Unmarshal(quoteRes.Data, &quote); err != nil || quote.AmountDiscount != 99 || quote.AmountPayable != price-99 || quote.Shortfall != price-99 {
		t.Fatalf("quote = %+v, %v", quote, err)
	}
	// 余额不足：409 insufficient_balance，data 带差额
	short := alice.do(http.MethodPost, "/api/user/orders", order)
	expectStatus(t, short, http.StatusConflict, string(service.CodeInsufficientBalance))
	var lack struct {
		Shortfall int64 `json:"shortfall"`
	}
	if err := json.Unmarshal(short.Data, &lack); err != nil || lack.Shortfall != price-99 {
		t.Fatalf("insufficient balance data = %+v, %v", lack, err)
	}

	gen := root.do(http.MethodPost, "/api/admin/topup-codes", map[string]any{"amount": topup, "count": 1})
	expectStatus(t, gen, http.StatusOK, codeOK)
	var batch topupBatchVO
	if err := json.Unmarshal(gen.Data, &batch); err != nil || len(batch.Codes) != 1 {
		t.Fatalf("topup batch = %+v, %v", batch, err)
	}
	expectStatus(t, alice.do(http.MethodPost, "/api/user/wallet/redeem", map[string]any{"code": batch.Codes[0]}), http.StatusOK, codeOK)

	placed := alice.do(http.MethodPost, "/api/user/orders", order)
	expectStatus(t, placed, http.StatusOK, codeOK)
	var mine map[string]any
	if err := json.Unmarshal(placed.Data, &mine); err != nil || mine["itemName"] != "标准版" || mine["amountPaid"] != float64(price-99) || mine["couponCode"] != "WELCOME10" {
		t.Fatalf("order = %+v, %v", mine, err)
	}
	if _, has := mine["username"]; has {
		t.Fatal("user-side order must not expose username")
	}

	walletRes := alice.do(http.MethodPut, "/api/user/wallet/auto-renew", map[string]any{"enabled": true})
	expectStatus(t, walletRes, http.StatusOK, codeOK)
	var wallet walletVO
	if err := json.Unmarshal(walletRes.Data, &wallet); err != nil || wallet.Balance != topup-(price-99) || !wallet.AutoRenew.Enabled ||
		wallet.AutoRenew.Price == nil || *wallet.AutoRenew.Price != price || wallet.Gateways == nil {
		t.Fatalf("wallet = %+v, %v", wallet, err)
	}

	orders := root.do(http.MethodGet, "/api/admin/orders?kind=plan", nil)
	expectStatus(t, orders, http.StatusOK, codeOK)
	var page pageVO[orderVO]
	if err := json.Unmarshal(orders.Data, &page); err != nil || page.Total != 1 || page.Items[0].Username == nil || *page.Items[0].Username != "alice" {
		t.Fatalf("admin orders = %+v, %v", page, err)
	}
	expectStatus(t, root.do(http.MethodGet, "/api/admin/orders?from=yesterday", nil), http.StatusBadRequest, string(service.CodeInvalidArgument))
	// 用过的优惠码只能停用
	expectStatus(t, root.do(http.MethodDelete, "/api/admin/coupons/1", nil), http.StatusConflict, string(service.CodeConflict))

	// 商店关闭：商店与下单 403，订单与钱包仍可查看
	expectStatus(t, root.do(http.MethodPut, "/api/admin/settings", map[string]any{"shop": map[string]any{"enabled": false}}), http.StatusOK, codeOK)
	expectStatus(t, alice.do(http.MethodGet, "/api/user/shop", nil), http.StatusForbidden, string(service.CodeFeatureDisabled))
	expectStatus(t, alice.do(http.MethodPost, "/api/user/orders", order), http.StatusForbidden, string(service.CodeFeatureDisabled))
	expectStatus(t, alice.do(http.MethodGet, "/api/user/orders", nil), http.StatusOK, codeOK)
	expectStatus(t, alice.do(http.MethodGet, "/api/user/wallet", nil), http.StatusOK, codeOK)
}
