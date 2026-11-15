package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"v2-hub-server/internal/manager/pay"
	"v2-hub-server/internal/manager/pay/paytest"
)

const payAmount = 5000 // 分

type payFixture struct {
	*shopFixture
	gateway *paytest.Fake
	pay     *PayService
}

func newPayFixture(t *testing.T) *payFixture {
	t.Helper()
	sf := newShopFixture(t)
	gw := &paytest.Fake{}
	reg, err := pay.NewRegistry(gw)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return &payFixture{shopFixture: sf, gateway: gw, pay: NewPayService(sf.pool, sf.settings, reg, nil, sf.clock.now)}
}

func (f *payFixture) notify(tradeNo, gatewayTradeNo string, amount int64, sign, status string) (string, error) {
	return f.pay.HandleNotify(context.Background(), paytest.Code, pay.NotifyRequest{Form: paytest.Notify(tradeNo, gatewayTradeNo, amount, sign, status)})
}

// 在线充值（docs/03 §5.15）：发起后落一张待支付的充值单；通知验签通过才入账，重复通知只入账一次；
// 金额、网关或交易号对不上都拒绝；过期后晚到的成功通知仍入账。
func TestPaymentNotifyCreditsOnce(t *testing.T) {
	f := newPayFixture(t)
	ctx := context.Background()
	alice := f.principal(t, "alice")
	if _, err := f.update(t, map[string]any{"site.portalUrl": "https://user.example.com"}); err != nil {
		t.Fatalf("set portal url: %v", err)
	}

	for i, c := range []struct {
		gateway string
		amount  int64
	}{{"nosuch", payAmount}, {paytest.Code, minPaymentAmount - 1}, {paytest.Code, maxMoney + 1}} {
		if _, err := f.pay.CreatePayment(ctx, alice, c.gateway, c.amount, nil); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("bad payment %d = %v, want invalid_argument", i, err)
		}
	}
	created, err := f.pay.CreatePayment(ctx, alice, paytest.Code, payAmount, nil)
	if err != nil || created.PayURL == "" || !created.ExpiresAt.Equal(f.clock.now().Add(paymentTTL)) {
		t.Fatalf("CreatePayment = %+v, %v", created, err)
	}
	sent := f.gateway.Created()
	if len(sent) != 1 || sent[0].TradeNo != created.TradeNo || sent[0].Amount != payAmount || sent[0].NotifyURL != "https://user.example.com/api/pay/notify/fake" {
		t.Fatalf("gateway received = %+v", sent)
	}
	if got := f.balanceOf(t, alice.UserID); got != 0 {
		t.Fatalf("balance before payment = %d", got)
	}

	// 验签失败、金额不符、单号不存在：都不入账
	if _, err := f.notify(created.TradeNo, "G1", payAmount, "forged", ""); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("forged notification = %v, want invalid_argument", err)
	}
	if _, err := f.notify(created.TradeNo, "G1", payAmount-1, paytest.GoodSign, ""); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("amount mismatch = %v, want invalid_argument", err)
	}
	if _, err := f.notify("P00000000000000000000", "G1", payAmount, paytest.GoodSign, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown trade no = %v, want not_found", err)
	}
	// “不是支付成功”的通知：应答但不入账
	if ack, err := f.notify(created.TradeNo, "G1", payAmount, paytest.GoodSign, paytest.StatusClosed); err != nil || ack != paytest.Ack {
		t.Fatalf("closed notification = %q, %v", ack, err)
	}
	if got := f.balanceOf(t, alice.UserID); got != 0 {
		t.Fatalf("balance after rejected notifications = %d", got)
	}

	// 并发收到同一条成功通知：只入账一次
	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.notify(created.TradeNo, "G1", payAmount, paytest.GoodSign, "")
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("notification %d: %v (a repeated notification must still be acknowledged)", i, err)
		}
	}
	if got := f.balanceOf(t, alice.UserID); got != payAmount {
		t.Fatalf("balance after repeated notifications = %d, want %d", got, payAmount)
	}
	ledger, err := f.wallet.ListLedger(ctx, LedgerFilter{UserID: alice.UserID, Kind: LedgerTopupOnline}, PageQuery{})
	if err != nil || ledger.Total != 1 {
		t.Fatalf("online topup ledger = %+v, %v", ledger, err)
	}
	page, err := f.pay.ListPayments(ctx, PaymentFilter{Status: PaymentPaid}, PageQuery{})
	if err != nil || page.Total != 1 || page.Items[0].TradeNo != created.TradeNo || page.Items[0].PaidAt == nil || page.Items[0].Username != "alice" {
		t.Fatalf("paid payments = %+v, %v", page, err)
	}

	// 同一个网关交易号不能再用在另一张充值单上
	second, err := f.pay.CreatePayment(ctx, alice, paytest.Code, payAmount, nil)
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if _, err := f.notify(second.TradeNo, "G1", payAmount, paytest.GoodSign, ""); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("reused gateway trade no = %v, want invalid_argument", err)
	}

	// 30 分钟未支付即过期；过期后晚到的成功通知仍然入账
	f.clock.advance(paymentTTL + time.Minute)
	if n, err := f.pay.ExpirePayments(ctx); err != nil || n != 1 {
		t.Fatalf("ExpirePayments = %d, %v", n, err)
	}
	if expired, err := f.pay.ListPayments(ctx, PaymentFilter{UserID: alice.UserID, Status: PaymentExpired}, PageQuery{}); err != nil || expired.Total != 1 {
		t.Fatalf("expired payments = %+v, %v", expired, err)
	}
	if _, err := f.notify(second.TradeNo, "G2", payAmount, paytest.GoodSign, ""); err != nil {
		t.Fatalf("late notification for an expired payment: %v", err)
	}
	if got := f.balanceOf(t, alice.UserID); got != 2*payAmount {
		t.Fatalf("balance after late notification = %d, want %d", got, 2*payAmount)
	}
}

// 发起充值的前提：商店开着、配了网关；每个用户每小时 10 次；网关下单失败不向用户透出原始报错。
func TestPaymentCreateGuards(t *testing.T) {
	f := newPayFixture(t)
	ctx := context.Background()
	alice := f.principal(t, "alice")

	empty, err := pay.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	none := NewPayService(f.pool, f.settings, empty, nil, f.clock.now)
	if _, err := none.CreatePayment(ctx, alice, paytest.Code, payAmount, nil); !errors.Is(err, ErrFeatureDisabled) || len(none.Gateways()) != 0 {
		t.Fatalf("payment without any gateway = %v, want feature_disabled", err)
	}

	f.gateway.FailWith(errors.New("upstream said: secret-key-mismatch"))
	_, err = f.pay.CreatePayment(ctx, alice, paytest.Code, payAmount, nil)
	var se *Error
	if !errors.As(err, &se) || se.Code != CodeUpstream || se.Message == "" || strings.Contains(se.Message, "secret") {
		t.Fatalf("gateway failure = %v, want upstream_unavailable without the raw error", err)
	}
	f.gateway.FailWith(nil)
	for i := 1; i < paymentsPerHour; i++ {
		if _, err := f.pay.CreatePayment(ctx, alice, paytest.Code, payAmount, nil); err != nil {
			t.Fatalf("payment %d: %v", i, err)
		}
	}
	if _, err := f.pay.CreatePayment(ctx, alice, paytest.Code, payAmount, nil); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("payment %d within an hour = %v, want rate_limited", paymentsPerHour+1, err)
	}

	if _, err := f.update(t, map[string]any{"shop.enabled": false}); err != nil {
		t.Fatalf("disable shop: %v", err)
	}
	f.clock.advance(2 * time.Hour)
	if _, err := f.pay.CreatePayment(ctx, alice, paytest.Code, payAmount, nil); !errors.Is(err, ErrFeatureDisabled) {
		t.Fatalf("payment with shop disabled = %v, want feature_disabled", err)
	}
	if _, err := pay.NewRegistry(&paytest.Fake{}, &paytest.Fake{}); err == nil {
		t.Fatal("duplicate gateway codes must be rejected")
	}
}

// 收支统计（finance:read）：按平台时区的自然日汇总充值、消费与返利，没有流水的日期给 0；余额是当前全部用户之和。
func TestFinanceStatsByDay(t *testing.T) {
	f := newPayFixture(t)
	ctx := context.Background()
	_, month, _ := f.salePlan(t, "basic", gib, false)
	if _, err := f.update(t, map[string]any{"rebate.percent": 10}); err != nil {
		t.Fatalf("enable rebate: %v", err)
	}
	ivy := f.principal(t, "ivy")
	bob := f.principal(t, "bob")
	f.exec(t, `UPDATE end_user SET invited_by = $2 WHERE id = $1`, bob.UserID, ivy.UserID)

	// 第一天：bob 兑换两个充值码
	codes := f.codes(t, 2, nil)
	for _, c := range codes {
		if _, err := f.wallet.Redeem(ctx, bob, c, nil); err != nil {
			t.Fatalf("Redeem: %v", err)
		}
	}
	// 第三天：下单（触发返利）；管理员调整不计入充值
	f.clock.advance(2 * day)
	f.buyPlan(t, bob, month, "")
	if err := f.wallet.AdjustBalance(ctx, f.actor, bob.UserID, 12345, "补偿", nil); err != nil {
		t.Fatalf("AdjustBalance: %v", err)
	}

	stats, err := f.shop.FinanceStatsBy(ctx, FinanceQuery{Granularity: StatsByDay, Days: 3})
	if err != nil || len(stats.Days) != 3 {
		t.Fatalf("FinanceStats = %+v, %v", stats, err)
	}
	first, middle, last := stats.Days[0], stats.Days[1], stats.Days[2]
	if first.Topup != 2*topupAmount || first.Spend != 0 || middle != (DayStats{Date: middle.Date}) ||
		last.Spend != priceMonth || last.OrderCount != 1 || last.Rebate != 99 || last.Topup != 0 {
		t.Fatalf("days = %+v", stats.Days)
	}
	wantBalance := int64(2*topupAmount-priceMonth+12345) + 99
	if stats.Topup != 2*topupAmount || stats.Spend != priceMonth || stats.Rebate != 99 || stats.Balance != wantBalance {
		t.Fatalf("totals = %+v, want balance %d", stats, wantBalance)
	}
	if only, err := f.shop.FinanceStatsBy(ctx, FinanceQuery{Granularity: StatsByDay, Days: 1}); err != nil || len(only.Days) != 1 || only.Topup != 0 || only.Spend != priceMonth {
		t.Fatalf("last day only = %+v, %v", only, err)
	}
	if _, err := f.shop.FinanceStatsBy(ctx, FinanceQuery{Granularity: StatsByDay, Days: maxStatsDays + 1}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("too many days = %v, want invalid_argument", err)
	}
	// 按月（docs/03 §5.20 财务报表）：本月一项合计等于按日合计；导出 CSV 带 BOM、元为单位、末行合计
	monthly, err := f.shop.FinanceStatsBy(ctx, FinanceQuery{Granularity: StatsByMonth, Months: 2})
	if err != nil || len(monthly.Days) != 2 || monthly.Granularity != StatsByMonth {
		t.Fatalf("monthly = %+v, %v", monthly, err)
	}
	var monthTopup, monthSpend int64
	for _, m := range monthly.Days {
		monthTopup, monthSpend = monthTopup+m.Topup, monthSpend+m.Spend
	}
	if monthTopup != 2*topupAmount || monthSpend != priceMonth || !strings.HasSuffix(monthly.Days[1].Date, "-01") {
		t.Fatalf("monthly days = %+v", monthly.Days)
	}
	for what, fq := range map[string]FinanceQuery{
		"bad granularity": {Granularity: "week"},
		"too many months": {Granularity: StatsByMonth, Months: maxStatsMonths + 1},
	} {
		if _, err := f.shop.FinanceStatsBy(ctx, fq); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("%s = %v, want invalid_argument", what, err)
		}
	}
	csvBody, name, err := f.shop.ExportFinanceCSV(ctx, f.actor, FinanceQuery{Granularity: StatsByDay, Days: 3}, nil)
	if err != nil || !strings.HasPrefix(string(csvBody), "\uFEFF日期,") || !strings.HasPrefix(name, "finance-day-") {
		t.Fatalf("csv = %q %q, %v", csvBody, name, err)
	}
	lines := strings.Split(strings.TrimSpace(string(csvBody)), "\n")
	if len(lines) != 5 || !strings.HasPrefix(lines[4], "合计,") || !strings.Contains(lines[1], formatCents(2*topupAmount)) {
		t.Fatalf("csv lines = %q", lines)
	}

	rebates, err := f.shop.ListRebates(ctx, ivy.UserID, PageQuery{})
	if err != nil || rebates.Total != 1 || rebates.Items[0].Amount != 99 || rebates.Items[0].InviteeUsername != "bob" || rebates.Items[0].OrderNo == "" {
		t.Fatalf("rebates = %+v, %v", rebates, err)
	}
	if none, err := f.shop.ListRebates(ctx, bob.UserID, PageQuery{}); err != nil || none.Total != 0 {
		t.Fatalf("rebates of a non-inviter = %+v, %v", none, err)
	}
}
