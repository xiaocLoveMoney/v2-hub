package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"v2-hub-server/internal/manager/pay/paytest"
	"v2-hub-server/internal/service"
)

// postForm 按支付网关的方式发一条表单通知（没有会话、没有 CSRF 头），返回状态码与应答正文。
func postForm(t *testing.T, target string, form map[string][]string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, target, strings.NewReader(url.Values(form).Encode()))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set(headerContent, contentTypeForm)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post notify: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("close body: %v", cerr)
		}
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read notify response: %v", err)
	}
	return resp.StatusCode, string(body)
}

// 在线充值走 HTTP：钱包列出网关 → 发起充值 → 网关回调（纯文本应答，重复通知只入账一次）→ 管理端看到充值单与统计（docs/03 §5.15）。
func TestPaymentEndpoints(t *testing.T) {
	const amount = 5000
	srv, rbac := newAPIServer(t)
	if _, err := rbac.BootstrapSuperAdmin(context.Background(), "root", cred); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	root := newClient(t, srv.URL)
	root.loginSuper("root")
	expectStatus(t, root.do(http.MethodPut, "/api/admin/settings", map[string]any{
		"register": map[string]any{"mode": service.RegisterOpen}, "shop": map[string]any{"enabled": true},
	}), http.StatusOK, codeOK)
	alice := newClient(t, srv.URL)
	reg := alice.do(http.MethodPost, "/api/user/auth/register", map[string]any{"username": "alice", "email": "alice@example.com", "password": cred})
	expectStatus(t, reg, http.StatusOK, codeOK)
	var session userSessionVO
	if err := json.Unmarshal(reg.Data, &session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	alice.csrf = session.CSRFToken

	var wallet walletVO
	if err := json.Unmarshal(alice.do(http.MethodGet, "/api/user/wallet", nil).Data, &wallet); err != nil || len(wallet.Gateways) != 1 || wallet.Gateways[0].Code != paytest.Code {
		t.Fatalf("wallet gateways = %+v, %v", wallet.Gateways, err)
	}
	expectStatus(t, alice.do(http.MethodPost, "/api/user/wallet/payments", map[string]any{"gateway": paytest.Code, "amount": 1}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))
	createdRes := alice.do(http.MethodPost, "/api/user/wallet/payments", map[string]any{"gateway": paytest.Code, "amount": amount})
	expectStatus(t, createdRes, http.StatusOK, codeOK)
	var created paymentCreatedVO
	if err := json.Unmarshal(createdRes.Data, &created); err != nil || created.TradeNo == "" || created.PayURL == "" {
		t.Fatalf("created payment = %+v, %v", created, err)
	}

	notifyURL := srv.URL + "/api/pay/notify/" + paytest.Code
	if status, _ := postForm(t, srv.URL+"/api/pay/notify/nosuch", paytest.Notify(created.TradeNo, "G1", amount, paytest.GoodSign, "")); status != http.StatusNotFound {
		t.Fatalf("unknown gateway = %d, want 404", status)
	}
	if status, _ := postForm(t, notifyURL, paytest.Notify(created.TradeNo, "G1", amount, "forged", "")); status != http.StatusBadRequest {
		t.Fatalf("forged notification = %d, want 400", status)
	}
	for i := range 2 {
		status, body := postForm(t, notifyURL, paytest.Notify(created.TradeNo, "G1", amount, paytest.GoodSign, ""))
		if status != http.StatusOK || body != paytest.Ack {
			t.Fatalf("notification %d = %d %q, want 200 %q", i, status, body, paytest.Ack)
		}
	}
	if err := json.Unmarshal(alice.do(http.MethodGet, "/api/user/wallet", nil).Data, &wallet); err != nil || wallet.Balance != amount {
		t.Fatalf("balance after two identical notifications = %d, %v; want %d", wallet.Balance, err, amount)
	}

	payments := root.do(http.MethodGet, "/api/admin/payments?status=paid", nil)
	expectStatus(t, payments, http.StatusOK, codeOK)
	var page pageVO[paymentVO]
	if err := json.Unmarshal(payments.Data, &page); err != nil || page.Total != 1 || page.Items[0].TradeNo != created.TradeNo || page.Items[0].Username != "alice" {
		t.Fatalf("admin payments = %+v, %v", page, err)
	}
	statsRes := root.do(http.MethodGet, "/api/admin/finance/stats?days=7", nil)
	expectStatus(t, statsRes, http.StatusOK, codeOK)
	var stats financeStatsVO
	if err := json.Unmarshal(statsRes.Data, &stats); err != nil || len(stats.Days) != 7 || stats.Totals.Topup != amount || stats.Totals.Balance != amount || stats.Days[6].Topup != amount {
		t.Fatalf("finance stats = %+v, %v", stats, err)
	}
	expectStatus(t, root.do(http.MethodGet, "/api/admin/finance/stats?days=0", nil), http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, root.do(http.MethodGet, "/api/admin/rebates?inviterId=x", nil), http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, root.do(http.MethodGet, "/api/admin/rebates", nil), http.StatusOK, codeOK)
}
