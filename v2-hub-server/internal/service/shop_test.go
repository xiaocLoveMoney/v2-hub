package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

const (
	priceMonth   = 990  // 分
	priceQuarter = 2700 // 分
	day          = 24 * time.Hour
)

type shopFixture struct {
	*walletFixture
	shop *ShopService
}

func newShopFixture(t *testing.T) *shopFixture {
	t.Helper()
	wf := newWalletFixture(t)
	return &shopFixture{walletFixture: wf, shop: NewShopService(wf.pool, wf.settings, wf.users, wf.mail, wf.clock.now)}
}

// salePlan 建一个上架的套餐，带 30 天与 90 天两个时长选项，返回套餐与两个选项的 id。
func (f *shopFixture) salePlan(t *testing.T, name string, limit int64, trial bool) (plan *Plan, monthID, quarterID int64) {
	t.Helper()
	in := PlanInput{
		Name: name, NodeGroupIDs: []int64{f.group.ID}, OnSale: true, Trial: trial,
		PlanQuota: PlanQuota{TrafficLimit: limit, DurationDays: 30, TrafficReset: TrafficResetNever, TrafficResetDay: 1},
		Prices:    []PlanPriceInput{{DurationDays: 30, Price: priceMonth}, {DurationDays: 90, Price: priceQuarter}},
	}
	if trial {
		in.Prices = []PlanPriceInput{{DurationDays: 3, Price: 0}}
	}
	p, err := f.users.CreatePlan(context.Background(), f.actor, in, nil)
	if err != nil {
		t.Fatalf("CreatePlan %s: %v", name, err)
	}
	if trial {
		return p, p.SavedPrices[0].ID, 0
	}
	return p, p.SavedPrices[0].ID, p.SavedPrices[1].ID
}

func (f *shopFixture) funded(t *testing.T, username string, balance int64) *UserPrincipal {
	t.Helper()
	p := f.principal(t, username)
	if balance > 0 {
		if err := f.wallet.AdjustBalance(context.Background(), f.actor, p.UserID, balance, "测试预存", nil); err != nil {
			t.Fatalf("fund %s: %v", username, err)
		}
	}
	return p
}

func (f *shopFixture) buyPlan(t *testing.T, p *UserPrincipal, priceID int64, coupon string) *Order {
	t.Helper()
	o, err := f.shop.CreateOrder(context.Background(), p, OrderInput{Kind: OrderPlan, PlanPriceID: priceID, CouponCode: coupon}, nil)
	if err != nil {
		t.Fatalf("CreateOrder(price %d, coupon %q): %v", priceID, coupon, err)
	}
	return o
}

func (f *shopFixture) userOf(t *testing.T, id int64) *User {
	t.Helper()
	u, err := f.users.GetUser(context.Background(), id)
	if err != nil {
		t.Fatalf("GetUser %d: %v", id, err)
	}
	return u
}

func (f *shopFixture) coupon(t *testing.T, in CouponInput) *Coupon {
	t.Helper()
	c, err := f.shop.CreateCoupon(context.Background(), f.actor, in, nil)
	if err != nil {
		t.Fatalf("CreateCoupon %s: %v", in.Code, err)
	}
	return c
}

// 购买、续费与换购（U11，docs/03 §5.13）：同套餐从原到期日顺延并清零已用；换购立即生效、不叠加剩余时长；余额不足不留任何痕迹。
func TestBuyRenewAndSwitchPlan(t *testing.T) {
	f := newShopFixture(t)
	ctx := context.Background()
	basic, basicMonth, basicQuarter := f.salePlan(t, "basic", gib, false)
	pro, proMonth, _ := f.salePlan(t, "pro", 2*gib, false)
	alice := f.funded(t, "alice", 10000)
	now := f.clock.now()

	quote, err := f.shop.QuoteOrder(ctx, alice, OrderInput{Kind: OrderPlan, PlanPriceID: basicMonth})
	if err != nil || quote.AmountPayable != priceMonth || quote.Shortfall != 0 || quote.Renewal || quote.TrafficLimit != gib ||
		quote.ExpiresAt == nil || !quote.ExpiresAt.Equal(now.AddDate(0, 0, 30)) {
		t.Fatalf("quote = %+v, %v", quote, err)
	}
	if got := f.balanceOf(t, alice.UserID); got != 10000 {
		t.Fatalf("quote must not charge: balance = %d", got)
	}

	order := f.buyPlan(t, alice, basicMonth, "")
	u := f.userOf(t, alice.UserID)
	if order.AmountPaid != priceMonth || order.ItemName != "basic" || order.DurationDays != 30 || u.Balance != 10000-priceMonth ||
		u.PlanID == nil || *u.PlanID != basic.ID || !u.Quota.ExpiresAt.Equal(now.AddDate(0, 0, 30)) || u.Status != UserStatusActive {
		t.Fatalf("after first purchase: order = %+v, user = %+v", order, u)
	}
	f.reconcile(t)
	f.panelClient(t, f.emailOf(t, alice.UserID)) // 买完即下发到面板

	// 用掉一些流量后续费 90 天：从原到期日顺延，已用清零
	if _, err := f.pool.Exec(ctx, `UPDATE end_user SET traffic_used = $2 WHERE id = $1`, alice.UserID, gib/2); err != nil {
		t.Fatalf("seed usage: %v", err)
	}
	f.clock.advance(10 * day)
	renew := f.buyPlan(t, alice, basicQuarter, "")
	u = f.userOf(t, alice.UserID)
	if renew.AmountPaid != priceQuarter || u.Quota.TrafficUsed != 0 || !u.Quota.ExpiresAt.Equal(now.AddDate(0, 0, 120)) || u.Balance != 10000-priceMonth-priceQuarter {
		t.Fatalf("after renewal: order = %+v, user = %+v", renew, u)
	}

	// 换购 pro：立即生效，到期 = 现在 + 30 天，原来剩的 110 天作废
	f.buyPlan(t, alice, proMonth, "")
	u = f.userOf(t, alice.UserID)
	if *u.PlanID != pro.ID || u.Quota.TrafficLimit != 2*gib || !u.Quota.ExpiresAt.Equal(f.clock.now().AddDate(0, 0, 30)) {
		t.Fatalf("after switching plan: user = %+v", u)
	}

	// 余额不足：返回差额，不扣款、不留订单
	poor := f.funded(t, "poor", 500)
	_, err = f.shop.CreateOrder(ctx, poor, OrderInput{Kind: OrderPlan, PlanPriceID: basicMonth}, nil)
	var se *Error
	if !errors.As(err, &se) || se.Code != CodeInsufficientBalance || se.Data.(map[string]int64)["shortfall"] != priceMonth-500 {
		t.Fatalf("insufficient balance = %v", err)
	}
	if own, err := f.shop.ListOwnOrders(ctx, poor, PageQuery{}); err != nil || own.Total != 0 || f.balanceOf(t, poor.UserID) != 500 {
		t.Fatalf("failed order must leave nothing behind: %+v, %v", own, err)
	}

	// 不可买的情况
	bad := []struct {
		in   OrderInput
		want error
	}{
		{OrderInput{Kind: "gift", PlanPriceID: basicMonth}, ErrInvalidArgument},
		{OrderInput{Kind: OrderPlan}, ErrInvalidArgument},
		{OrderInput{Kind: OrderPlan, PlanPriceID: 999999}, ErrConflict},
	}
	for i, c := range bad {
		if _, err := f.shop.QuoteOrder(ctx, alice, c.in); !errors.Is(err, c.want) {
			t.Errorf("bad order %d = %v, want %v", i, err, c.want)
		}
	}
	in := PlanInput{Name: "basic", PlanQuota: basic.PlanQuota, NodeGroupIDs: basic.NodeGroupIDs, OnSale: false}
	if _, err := f.users.UpdatePlan(ctx, f.actor, basic.ID, in, nil); err != nil {
		t.Fatalf("take plan off sale: %v", err)
	}
	if _, err := f.shop.QuoteOrder(ctx, alice, OrderInput{Kind: OrderPlan, PlanPriceID: basicMonth}); !errors.Is(err, ErrConflict) {
		t.Fatalf("off-sale plan = %v, want conflict", err)
	}

	// 订单：本人只看到自己的三张；流水里三笔购买都指向订单
	own, err := f.shop.ListOwnOrders(ctx, alice, PageQuery{})
	if err != nil || own.Total != 3 || own.Items[0].ItemName != "pro" {
		t.Fatalf("own orders = %+v, %v", own, err)
	}
	all, err := f.shop.ListOrders(ctx, OrderFilter{Kind: OrderPlan}, PageQuery{})
	if err != nil || all.Total != 3 || all.Items[0].Username != "alice" {
		t.Fatalf("admin orders = %+v, %v", all, err)
	}
	ledger, err := f.wallet.ListLedger(ctx, LedgerFilter{UserID: alice.UserID, Kind: LedgerPurchase}, PageQuery{})
	if err != nil || ledger.Total != 3 || ledger.Items[0].Amount != -priceMonth {
		t.Fatalf("purchase ledger = %+v, %v", ledger, err)
	}

	if _, err := f.update(t, map[string]any{"shop.enabled": false}); err != nil {
		t.Fatalf("disable shop: %v", err)
	}
	if _, err := f.shop.CreateOrder(ctx, alice, OrderInput{Kind: OrderPlan, PlanPriceID: proMonth}, nil); !errors.Is(err, ErrFeatureDisabled) {
		t.Fatalf("order with shop disabled = %v, want feature_disabled", err)
	}
	if w, err := f.shop.GetWallet(ctx, alice); err != nil || w.Balance != f.balanceOf(t, alice.UserID) {
		t.Fatalf("wallet must stay readable with shop disabled: %+v, %v", w, err)
	}
}

// 流量包（U12）：只加额度不改到期；没有套餐不能买；因此回到额度以内的用户恢复正常。
func TestTrafficPackAddsQuotaOnly(t *testing.T) {
	f := newShopFixture(t)
	ctx := context.Background()
	_, month, _ := f.salePlan(t, "basic", gib, false)
	pack, err := f.shop.CreateTrafficPack(ctx, f.actor, TrafficPackInput{Name: "加油包", TrafficBytes: gib, Price: 300, OnSale: true}, nil)
	if err != nil {
		t.Fatalf("CreateTrafficPack: %v", err)
	}
	if _, err := f.shop.CreateTrafficPack(ctx, f.actor, TrafficPackInput{Name: "加油包", TrafficBytes: gib, Price: 300}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate pack name = %v, want conflict", err)
	}
	alice := f.funded(t, "alice", 5000)
	in := OrderInput{Kind: OrderTrafficPack, TrafficPackID: pack.ID}
	if _, err := f.shop.CreateOrder(ctx, alice, in, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("pack without plan = %v, want conflict", err)
	}
	f.buyPlan(t, alice, month, "")
	expires := *f.userOf(t, alice.UserID).Quota.ExpiresAt
	if _, err := f.pool.Exec(ctx, `UPDATE end_user SET traffic_used = traffic_limit, status = 'limited' WHERE id = $1`, alice.UserID); err != nil {
		t.Fatalf("exhaust traffic: %v", err)
	}
	order, err := f.shop.CreateOrder(ctx, alice, in, nil)
	u := f.userOf(t, alice.UserID)
	if err != nil || order.Kind != OrderTrafficPack || order.TrafficBytes != gib || u.Quota.TrafficLimit != 2*gib || !u.Quota.ExpiresAt.Equal(expires) ||
		u.Status != UserStatusActive || u.Balance != 5000-priceMonth-300 {
		t.Fatalf("after buying pack: order = %+v, user = %+v, err = %v", order, u, err)
	}
	if _, err := f.shop.UpdateTrafficPack(ctx, f.actor, pack.ID, TrafficPackInput{Name: "加油包", TrafficBytes: gib, Price: 300, OnSale: false}, nil); err != nil {
		t.Fatalf("take pack off sale: %v", err)
	}
	if _, err := f.shop.CreateOrder(ctx, alice, in, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("off-sale pack = %v, want conflict", err)
	}
}

// 优惠码（docs/03 §5.15）：百分比向下取整、固定面额不超过原价；范围、最低消费、时间窗、每人次数；询价不占次数；不可用的提示不区分原因。
func TestCouponRules(t *testing.T) {
	f := newShopFixture(t)
	ctx := context.Background()
	basic, basicMonth, _ := f.salePlan(t, "basic", gib, false)
	_, proMonth, _ := f.salePlan(t, "pro", 2*gib, false)
	alice := f.funded(t, "alice", 10000)
	now := f.clock.now()
	later, earlier := now.Add(day), now.Add(-day)

	off20 := f.coupon(t, CouponInput{Code: "off20", Kind: CouponPercent, Value: 20, AllPlans: true, MaxUsesPerUser: 1})
	f.coupon(t, CouponInput{Code: "FREE", Kind: CouponFixed, Value: 5000, AllPlans: true, MaxUsesPerUser: 0})
	f.coupon(t, CouponInput{Code: "BASICONLY", Kind: CouponFixed, Value: 100, PlanIDs: []int64{basic.ID}})
	f.coupon(t, CouponInput{Code: "BIGSPEND", Kind: CouponFixed, Value: 100, AllPlans: true, MinAmount: priceMonth + 1})
	f.coupon(t, CouponInput{Code: "NOTYET", Kind: CouponFixed, Value: 100, AllPlans: true, StartsAt: &later})
	f.coupon(t, CouponInput{Code: "EXPIRED", Kind: CouponFixed, Value: 100, AllPlans: true, EndsAt: &earlier})
	f.coupon(t, CouponInput{Code: "STOPPED", Kind: CouponFixed, Value: 100, AllPlans: true, Disabled: true})
	if _, err := f.shop.CreateCoupon(ctx, f.actor, CouponInput{Code: "OFF20", Kind: CouponFixed, Value: 1, AllPlans: true}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate coupon code = %v, want conflict", err)
	}
	if _, err := f.shop.CreateCoupon(ctx, f.actor, CouponInput{Code: "HALF", Kind: CouponPercent, Value: 101, AllPlans: true}, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("percent over 100 = %v, want invalid_argument", err)
	}

	// 询价：990 × 20% = 198，不占次数
	quote, err := f.shop.QuoteOrder(ctx, alice, OrderInput{Kind: OrderPlan, PlanPriceID: basicMonth, CouponCode: " off20 "})
	if err != nil || quote.AmountDiscount != 198 || quote.AmountPayable != priceMonth-198 || quote.CouponCode != "OFF20" {
		t.Fatalf("quote with coupon = %+v, %v", quote, err)
	}
	for _, code := range []string{"BIGSPEND", "NOTYET", "EXPIRED", "STOPPED", "NOSUCH", "bad code!"} {
		if _, err := f.shop.QuoteOrder(ctx, alice, OrderInput{Kind: OrderPlan, PlanPriceID: basicMonth, CouponCode: code}); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("coupon %s = %v, want invalid_argument", code, err)
		}
	}
	if _, err := f.shop.QuoteOrder(ctx, alice, OrderInput{Kind: OrderPlan, PlanPriceID: proMonth, CouponCode: "BASICONLY"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("plan-scoped coupon on another plan = %v, want invalid_argument", err)
	}

	order := f.buyPlan(t, alice, basicMonth, "OFF20")
	if order.AmountDiscount != 198 || order.AmountPaid != priceMonth-198 || order.CouponCode != "OFF20" || f.balanceOf(t, alice.UserID) != 10000-(priceMonth-198) {
		t.Fatalf("order with coupon = %+v", order)
	}
	// 每人一次：第二次不可用
	if _, err := f.shop.CreateOrder(ctx, alice, OrderInput{Kind: OrderPlan, PlanPriceID: basicMonth, CouponCode: "OFF20"}, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("reusing a once-per-user coupon = %v, want invalid_argument", err)
	}
	// 用过的优惠码不能删
	if err := f.shop.DeleteCoupon(ctx, f.actor, off20.ID, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleting a used coupon = %v, want conflict", err)
	}

	// 固定面额大于原价：实付为 0，不产生流水
	before := f.balanceOf(t, alice.UserID)
	free := f.buyPlan(t, alice, basicMonth, "FREE")
	if free.AmountDiscount != priceMonth || free.AmountPaid != 0 || f.balanceOf(t, alice.UserID) != before {
		t.Fatalf("fully discounted order = %+v", free)
	}
	ledger, err := f.wallet.ListLedger(ctx, LedgerFilter{UserID: alice.UserID, Kind: LedgerPurchase}, PageQuery{})
	if err != nil || ledger.Total != 1 {
		t.Fatalf("zero-amount order must not write a ledger entry: %+v, %v", ledger, err)
	}

	// 流量包默认不适用
	pack, err := f.shop.CreateTrafficPack(ctx, f.actor, TrafficPackInput{Name: "加油包", TrafficBytes: gib, Price: 300, OnSale: true}, nil)
	if err != nil {
		t.Fatalf("CreateTrafficPack: %v", err)
	}
	if _, err := f.shop.QuoteOrder(ctx, alice, OrderInput{Kind: OrderTrafficPack, TrafficPackID: pack.ID, CouponCode: "FREE"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("plan coupon on a traffic pack = %v, want invalid_argument", err)
	}
}

// 优惠码总次数在并发下单时不超发（优惠码行加锁，docs/03 §5.15 限制）。
func TestCouponMaxUsesHoldsUnderConcurrency(t *testing.T) {
	f := newShopFixture(t)
	ctx := context.Background()
	_, month, _ := f.salePlan(t, "basic", gib, false)
	f.coupon(t, CouponInput{Code: "ONCE", Kind: CouponFixed, Value: 100, AllPlans: true, MaxUses: 1, MaxUsesPerUser: 1})
	const racers = 6
	users := make([]*UserPrincipal, racers)
	for i := range users {
		users[i] = f.funded(t, fmt.Sprintf("racer%d", i), 5000)
	}
	var wg sync.WaitGroup
	results := make([]error, racers)
	for i := range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = f.shop.CreateOrder(ctx, users[i], OrderInput{Kind: OrderPlan, PlanPriceID: month, CouponCode: "ONCE"}, nil)
		}()
	}
	wg.Wait()
	okCount := 0
	for i, err := range results {
		if err == nil {
			okCount++
		} else if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("racer %d: unexpected error %v", i, err)
		}
	}
	var used, orders int
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT used_count FROM coupon WHERE code = 'ONCE'), (SELECT count(*) FROM shop_order)`).Scan(&used, &orders); err != nil {
		t.Fatalf("read coupon: %v", err)
	}
	if okCount != 1 || used != 1 || orders != 1 {
		t.Fatalf("successes = %d, used_count = %d, orders = %d; want 1 / 1 / 1", okCount, used, orders)
	}
}

// 试用（docs/03 §5.12）：每人终身一次（并发也只成功一次）；持有未到期的其它套餐不能领；不能用优惠码；价格为 0 不产生流水。
func TestTrialOncePerUser(t *testing.T) {
	f := newShopFixture(t)
	ctx := context.Background()
	_, trialID, _ := f.salePlan(t, "试用", gib, true)
	_, month, _ := f.salePlan(t, "basic", gib, false)
	f.coupon(t, CouponInput{Code: "ANYONE", Kind: CouponFixed, Value: 1, AllPlans: true})
	alice := f.principal(t, "alice")
	in := OrderInput{Kind: OrderPlan, PlanPriceID: trialID}

	if _, err := f.shop.QuoteOrder(ctx, alice, OrderInput{Kind: OrderPlan, PlanPriceID: trialID, CouponCode: "ANYONE"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("coupon on trial = %v, want invalid_argument", err)
	}
	var wg sync.WaitGroup
	results := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = f.shop.CreateOrder(ctx, alice, in, nil)
		}()
	}
	wg.Wait()
	okCount := 0
	for i, err := range results {
		if err == nil {
			okCount++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatalf("attempt %d: unexpected error %v", i, err)
		}
	}
	u := f.userOf(t, alice.UserID)
	if okCount != 1 || u.Quota.ExpiresAt == nil || !u.Quota.ExpiresAt.Equal(f.clock.now().AddDate(0, 0, 3)) {
		t.Fatalf("trial successes = %d, user = %+v", okCount, u)
	}
	if ledger, err := f.wallet.ListLedger(ctx, LedgerFilter{UserID: alice.UserID}, PageQuery{}); err != nil || ledger.Total != 0 {
		t.Fatalf("free trial must not write ledger entries: %+v, %v", ledger, err)
	}
	// 试用到期后也不能再领
	f.clock.advance(4 * day)
	if _, err := f.shop.CreateOrder(ctx, alice, in, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("second trial = %v, want conflict", err)
	}

	// 已有未到期的正式套餐：不能领试用
	bob := f.funded(t, "bob", 5000)
	f.buyPlan(t, bob, month, "")
	if _, err := f.shop.CreateOrder(ctx, bob, in, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("trial while holding a paid plan = %v, want conflict", err)
	}
	shop, err := f.shop.GetShop(ctx, alice)
	if err != nil || len(shop.Plans) != 2 {
		t.Fatalf("GetShop = %+v, %v", shop, err)
	}
	for _, p := range shop.Plans {
		if p.Trial && (p.CanBuy || p.Reason != msgTrialUsed) {
			t.Fatalf("used trial must be marked unavailable in the shop: %+v", p)
		}
	}
}

// 防猜与频控：优惠码无效 20 次后一小时内拒绝带码的请求；下单每分钟 10 次（docs/03 §5.13、§5.15）。
func TestShopGuards(t *testing.T) {
	f := newShopFixture(t)
	ctx := context.Background()
	_, month, _ := f.salePlan(t, "basic", gib, false)
	alice := f.funded(t, "alice", 100)
	in := OrderInput{Kind: OrderPlan, PlanPriceID: month, CouponCode: "NOSUCH"}
	for i := range couponFailsPerHour {
		if _, err := f.shop.QuoteOrder(ctx, alice, in); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("guess %d = %v, want invalid_argument", i, err)
		}
	}
	if _, err := f.shop.QuoteOrder(ctx, alice, in); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("after %d bad coupons = %v, want rate_limited", couponFailsPerHour, err)
	}
	// 不带优惠码的询价不受影响
	if _, err := f.shop.QuoteOrder(ctx, alice, OrderInput{Kind: OrderPlan, PlanPriceID: month}); err != nil {
		t.Fatalf("quote without coupon while coupon guard is locked: %v", err)
	}

	f.clock.advance(2 * time.Minute)
	plain := OrderInput{Kind: OrderPlan, PlanPriceID: month}
	for i := range ordersPerMinute {
		if _, err := f.shop.CreateOrder(ctx, alice, plain, nil); !errors.Is(err, ErrInsufficient) {
			t.Fatalf("order %d = %v, want insufficient_balance", i, err)
		}
	}
	if _, err := f.shop.CreateOrder(ctx, alice, plain, nil); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("order %d within a minute = %v, want rate_limited", ordersPerMinute+1, err)
	}
}

// 自动续费开关（docs/03 §5.16）：买过在售的时长选项才能开；换购后续费对象跟着换；试用不能开；选项被删后不可用。
func TestAutoRenewSwitch(t *testing.T) {
	f := newShopFixture(t)
	ctx := context.Background()
	basic, basicMonth, _ := f.salePlan(t, "basic", gib, false)
	_, proMonth, _ := f.salePlan(t, "pro", 2*gib, false)
	alice := f.funded(t, "alice", 10000)

	w, err := f.shop.GetWallet(ctx, alice)
	if err != nil || w.AutoRenew.Available || w.AutoRenew.Reason != msgAutoRenewNoOption || w.Balance != 10000 {
		t.Fatalf("wallet before any purchase = %+v, %v", w, err)
	}
	if _, err := f.shop.SetAutoRenew(ctx, alice, true, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("enabling auto renew without a purchase = %v, want conflict", err)
	}
	f.buyPlan(t, alice, basicMonth, "")
	w, err = f.shop.SetAutoRenew(ctx, alice, true, nil)
	if err != nil || !w.AutoRenew.Enabled || !w.AutoRenew.Available || w.AutoRenew.PlanName != "basic" || w.AutoRenew.DurationDays != 30 ||
		w.AutoRenew.Price == nil || *w.AutoRenew.Price != priceMonth {
		t.Fatalf("SetAutoRenew = %+v, %v", w, err)
	}

	// 换购后仍然开着，续费对象换成新买的选项
	f.buyPlan(t, alice, proMonth, "")
	w, err = f.shop.GetWallet(ctx, alice)
	if err != nil || !w.AutoRenew.Enabled || w.AutoRenew.PlanName != "pro" || !f.userOf(t, alice.UserID).AutoRenew {
		t.Fatalf("wallet after switching plan = %+v, %v", w, err)
	}

	// 管理员删掉这个时长选项：不能再开，价格为 nil
	in := PlanInput{Name: "pro", PlanQuota: basic.PlanQuota, NodeGroupIDs: basic.NodeGroupIDs, OnSale: true, Prices: []PlanPriceInput{{DurationDays: 365, Price: 9900}}}
	proID := *f.userOf(t, alice.UserID).PlanID
	if _, err := f.users.UpdatePlan(ctx, f.actor, proID, in, nil); err != nil {
		t.Fatalf("replace pro prices: %v", err)
	}
	w, err = f.shop.GetWallet(ctx, alice)
	if err != nil || w.AutoRenew.Available || w.AutoRenew.Price != nil {
		t.Fatalf("wallet after the option was removed = %+v, %v", w, err)
	}
	if w, err = f.shop.SetAutoRenew(ctx, alice, false, nil); err != nil || w.AutoRenew.Enabled {
		t.Fatalf("turning auto renew off must always work: %+v, %v", w, err)
	}
}

func TestPublicPlans(t *testing.T) {
	f := newShopFixture(t)
	ctx := context.Background()
	f.salePlan(t, "标准版", 0, false)
	f.salePlan(t, "试用", 0, true)
	set := func(patch map[string]any) {
		t.Helper()
		if _, err := updateSettings(t, f.settings, f.actor, patch); err != nil {
			t.Fatal(err)
		}
	}
	set(map[string]any{"shop.enabled": true})
	if plans, err := f.shop.PublicPlans(ctx); err != nil || len(plans) != 0 {
		t.Fatalf("landing off should hide plans: %+v, %v", plans, err)
	}
	set(map[string]any{"landing.enabled": true})
	plans, err := f.shop.PublicPlans(ctx)
	if err != nil || len(plans) != 1 || plans[0].Name != "标准版" || len(plans[0].Prices) != 2 || plans[0].Current || plans[0].CanBuy {
		t.Fatalf("public plans = %+v, %v", plans, err)
	}
	set(map[string]any{"shop.enabled": false})
	if plans, err = f.shop.PublicPlans(ctx); err != nil || len(plans) != 0 {
		t.Fatalf("shop off should hide plans: %+v, %v", plans, err)
	}
}
