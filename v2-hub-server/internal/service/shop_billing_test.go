package service

import (
	"context"
	"strings"
	"testing"
	"time"
)

// verify 给用户绑一个已验证的邮箱，提醒与续费邮件才会发给他。
func (f *shopFixture) verify(t *testing.T, p *UserPrincipal) string {
	t.Helper()
	email := p.Username + "@example.com"
	if _, err := f.pool.Exec(context.Background(), `UPDATE end_user SET email = $2, email_verified_at = now() WHERE id = $1`, p.UserID, email); err != nil {
		t.Fatalf("verify email: %v", err)
	}
	return email
}

func (f *shopFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func (f *shopFixture) count(t *testing.T, sql string, args ...any) (n int) {
	t.Helper()
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

func (f *shopFixture) advance(t *testing.T) {
	t.Helper()
	if _, err := f.shop.AdvanceUsers(context.Background()); err != nil {
		t.Fatalf("AdvanceUsers: %v", err)
	}
}

// subjectsTo 发出全部待发邮件，返回发给某个地址的主题（按发送顺序）。
func (f *shopFixture) subjectsTo(t *testing.T, to string) []string {
	t.Helper()
	f.flush(t)
	var out []string
	for _, m := range f.sender.messages() {
		if m.To == to {
			out = append(out, m.Subject)
		}
	}
	return out
}

func lastContains(subjects []string, want string) bool {
	return len(subjects) > 0 && strings.Contains(subjects[len(subjects)-1], want)
}

// 到期自动续费（U10，docs/03 §5.16）：余额够就扣款、从原到期日顺延，面板上的 client 不被来回启停；
// 余额不够只试一次，关闭开关、到期并发信，之后充值不补扣。
func TestAutoRenewAtExpiry(t *testing.T) {
	f := newShopFixture(t)
	ctx := context.Background()
	_, month, _ := f.salePlan(t, "basic", gib, false)
	alice := f.funded(t, "alice", 2*priceMonth+100) // 够首购 + 续一次
	email := f.verify(t, alice)
	f.buyPlan(t, alice, month, "")
	if _, err := f.shop.SetAutoRenew(ctx, alice, true, nil); err != nil {
		t.Fatalf("SetAutoRenew: %v", err)
	}
	f.reconcile(t)
	firstExpiry := *f.userOf(t, alice.UserID).Quota.ExpiresAt

	// 还没到期：什么都不发生
	f.advance(t)
	if got := f.balanceOf(t, alice.UserID); got != priceMonth+100 {
		t.Fatalf("balance before expiry = %d", got)
	}

	f.clock.t = firstExpiry.Add(40 * time.Second)
	f.advance(t)
	u := f.userOf(t, alice.UserID)
	if u.Status != UserStatusActive || !u.Quota.ExpiresAt.Equal(firstExpiry.AddDate(0, 0, 30)) || u.Balance != 100 || !u.AutoRenew {
		t.Fatalf("after auto renew: %+v", u)
	}
	if n := f.count(t, `SELECT count(*) FROM provision WHERE user_id = $1 AND status <> 'synced'`, alice.UserID); n != 0 {
		t.Fatalf("a renewed user must not be re-provisioned: %d provisions left synced state", n)
	}
	orders, err := f.shop.ListOrders(ctx, OrderFilter{UserID: alice.UserID, Kind: OrderRenew}, PageQuery{})
	if err != nil || orders.Total != 1 || orders.Items[0].AmountPaid != priceMonth || orders.Items[0].ItemName != "basic" {
		t.Fatalf("renew orders = %+v, %v", orders, err)
	}
	ledger, err := f.wallet.ListLedger(ctx, LedgerFilter{UserID: alice.UserID, Kind: LedgerRenew}, PageQuery{})
	if err != nil || ledger.Total != 1 || ledger.Items[0].Amount != -priceMonth || ledger.Items[0].BalanceAfter != 100 {
		t.Fatalf("renew ledger = %+v, %v", ledger, err)
	}
	if subjects := f.subjectsTo(t, email); !lastContains(subjects, "已自动续费") {
		t.Fatalf("mails after renewal = %v", subjects)
	}

	// 第二次到期：余额不够 → 只试这一次，关闭自动续费，用户到期并收到通知
	f.clock.t = u.Quota.ExpiresAt.Add(40 * time.Second)
	f.advance(t)
	u = f.userOf(t, alice.UserID)
	if u.Status != UserStatusExpired || u.AutoRenew || u.Balance != 100 {
		t.Fatalf("after failed renewal: %+v", u)
	}
	subjects := f.subjectsTo(t, email)
	if !lastContains(subjects, "自动续费失败") {
		t.Fatalf("mails after failed renewal = %v", subjects)
	}
	// 之后充值不会补扣
	if err := f.wallet.AdjustBalance(ctx, f.actor, alice.UserID, 5000, "充值", nil); err != nil {
		t.Fatalf("AdjustBalance: %v", err)
	}
	f.advance(t)
	if u = f.userOf(t, alice.UserID); u.Status != UserStatusExpired || u.Balance != 5100 || len(f.subjectsTo(t, email)) != len(subjects) {
		t.Fatalf("topping up after a failed renewal must not charge: %+v", u)
	}
}

// 自动续费的边界：套餐下架即失败；商店关闭期间不处理也不关开关，重开后补上；管理员设的免费续期优先，不扣钱。
func TestAutoRenewEdgeCases(t *testing.T) {
	f := newShopFixture(t)
	ctx := context.Background()
	basic, month, _ := f.salePlan(t, "basic", gib, false)
	enable := func(name string) *UserPrincipal {
		p := f.funded(t, name, 5000)
		f.buyPlan(t, p, month, "")
		if _, err := f.shop.SetAutoRenew(ctx, p, true, nil); err != nil {
			t.Fatalf("SetAutoRenew %s: %v", name, err)
		}
		return p
	}
	paused, free := enable("paused"), enable("free")
	f.exec(t, `UPDATE end_user SET renew_every_days = 30 WHERE id = $1`, free.UserID)
	expiry := *f.userOf(t, paused.UserID).Quota.ExpiresAt

	if _, err := f.update(t, map[string]any{"shop.enabled": false}); err != nil {
		t.Fatalf("disable shop: %v", err)
	}
	f.clock.t = expiry.Add(time.Minute)
	f.advance(t)
	u := f.userOf(t, paused.UserID)
	if u.Status != UserStatusExpired || !u.AutoRenew || u.Balance != 5000-priceMonth {
		t.Fatalf("shop closed: auto renew must pause, not fail: %+v", u)
	}
	// 免费续期照常生效，不扣余额、不产生订单
	fu := f.userOf(t, free.UserID)
	if fu.Status != UserStatusActive || fu.Balance != 5000-priceMonth || fu.Quota.RenewCount != 1 {
		t.Fatalf("free renewal must win over paid renewal: %+v", fu)
	}

	if _, err := f.update(t, map[string]any{"shop.enabled": true}); err != nil {
		t.Fatalf("enable shop: %v", err)
	}
	f.advance(t)
	if u = f.userOf(t, paused.UserID); u.Status != UserStatusActive || u.Balance != 5000-2*priceMonth {
		t.Fatalf("after shop reopened: %+v", u)
	}
	if n := f.count(t, `SELECT count(*) FROM shop_order WHERE kind = 'renew'`); n != 1 {
		t.Fatalf("renew orders = %d, want 1 (only the paused user, once)", n)
	}

	// 套餐下架：下一次到期时续费失败并关闭
	in := PlanInput{Name: "basic", PlanQuota: basic.PlanQuota, NodeGroupIDs: basic.NodeGroupIDs, OnSale: false}
	if _, err := f.users.UpdatePlan(ctx, f.actor, basic.ID, in, nil); err != nil {
		t.Fatalf("take plan off sale: %v", err)
	}
	f.clock.t = f.userOf(t, paused.UserID).Quota.ExpiresAt.Add(time.Minute)
	f.advance(t)
	if u = f.userOf(t, paused.UserID); u.Status != UserStatusExpired || u.AutoRenew || u.Balance != 5000-2*priceMonth {
		t.Fatalf("off-sale plan must fail the renewal without charging: %+v", u)
	}
}

// 邀请返利（Ruling-34）：按实付 × 比例向下取整，只进邀请人余额；只返首笔 / 每笔都返；实付为 0、试用、邀请人停用时不返。
func TestInviteRebate(t *testing.T) {
	f := newShopFixture(t)
	ctx := context.Background()
	_, month, _ := f.salePlan(t, "basic", gib, false)
	_, trial, _ := f.salePlan(t, "试用", gib, true)
	f.coupon(t, CouponInput{Code: "FREE", Kind: CouponFixed, Value: 99999, AllPlans: true, MaxUsesPerUser: 0})
	ivy := f.principal(t, "ivy")
	invite := func(name string) *UserPrincipal {
		p := f.funded(t, name, 10000)
		f.exec(t, `UPDATE end_user SET invited_by = $2 WHERE id = $1`, p.UserID, ivy.UserID)
		return p
	}
	bob, carol := invite("bob"), invite("carol")

	// 没开返利：不返
	f.buyPlan(t, carol, month, "")
	if got := f.balanceOf(t, ivy.UserID); got != 0 {
		t.Fatalf("rebate while disabled: inviter balance = %d", got)
	}
	if _, err := f.update(t, map[string]any{"rebate.percent": 10}); err != nil {
		t.Fatalf("enable rebate: %v", err)
	}
	// carol 之前已经有过实付订单，“只返首笔”下不再返
	f.buyPlan(t, carol, month, "")
	// 试用与实付为 0 的订单不返，也不算“首笔”
	f.buyPlan(t, bob, trial, "")
	f.clock.advance(4 * day)
	f.buyPlan(t, bob, month, "FREE")
	if got := f.balanceOf(t, ivy.UserID); got != 0 {
		t.Fatalf("no rebate expected yet: inviter balance = %d", got)
	}
	// bob 的第一笔实付：990 × 10% = 99
	f.buyPlan(t, bob, month, "")
	if got := f.balanceOf(t, ivy.UserID); got != 99 {
		t.Fatalf("first paid order: inviter balance = %d, want 99", got)
	}
	f.buyPlan(t, bob, month, "")
	if got := f.balanceOf(t, ivy.UserID); got != 99 {
		t.Fatalf("mode first must rebate once: inviter balance = %d", got)
	}
	ledger, err := f.wallet.ListLedger(ctx, LedgerFilter{UserID: ivy.UserID, Kind: LedgerRebate}, PageQuery{})
	if err != nil || ledger.Total != 1 || ledger.Items[0].Amount != 99 {
		t.Fatalf("rebate ledger = %+v, %v", ledger, err)
	}
	w, err := f.shop.GetWallet(ctx, ivy)
	if err != nil || w.RebateTotal != 99 {
		t.Fatalf("wallet rebate total = %+v, %v", w, err)
	}
	// 邀请页：比例、累计与记录，被邀请人用户名打码
	info, err := f.account.GetInvite(ctx, ivy)
	if err != nil || info.RebatePercent != 10 || info.RebateTotal != 99 || len(info.Rebates) != 1 || info.Rebates[0].Amount != 99 || info.Rebates[0].Username == "bob" {
		t.Fatalf("invite info = %+v, %v", info, err)
	}

	if _, err := f.update(t, map[string]any{"rebate.mode": RebateAlways}); err != nil {
		t.Fatalf("set rebate mode: %v", err)
	}
	f.buyPlan(t, bob, month, "")
	if got := f.balanceOf(t, ivy.UserID); got != 198 {
		t.Fatalf("mode always: inviter balance = %d, want 198", got)
	}
	// 邀请人停用后不再返
	f.exec(t, `UPDATE end_user SET is_disabled = TRUE WHERE id = $1`, ivy.UserID)
	f.buyPlan(t, bob, month, "")
	if got := f.balanceOf(t, ivy.UserID); got != 198 {
		t.Fatalf("disabled inviter must not get rebates: balance = %d", got)
	}
	if n := f.count(t, `SELECT count(*) FROM rebate`); n != 2 {
		t.Fatalf("rebate rows = %d, want 2", n)
	}
}

// 提醒邮件（docs/03 §5.16）：每个到期时间只提醒一次；流量回到阈值以上后才会再提醒；免费续期的用户不提醒到期。
func TestExpiryAndTrafficReminders(t *testing.T) {
	f := newShopFixture(t)
	ctx := context.Background()
	_, month, _ := f.salePlan(t, "basic", gib, false)
	alice := f.funded(t, "alice", 10000)
	email := f.verify(t, alice)
	f.buyPlan(t, alice, month, "")
	gifted := f.funded(t, "gifted", 10000) // 管理员给了免费续期：不发到期提醒
	giftedEmail := f.verify(t, gifted)
	f.buyPlan(t, gifted, month, "")
	f.exec(t, `UPDATE end_user SET renew_every_days = 30 WHERE id = $1`, gifted.UserID)
	silent := f.funded(t, "silent", 10000) // 没有已验证邮箱：不发
	f.buyPlan(t, silent, month, "")

	remind := func() {
		t.Helper()
		if err := f.shop.SendReminders(ctx); err != nil {
			t.Fatalf("SendReminders: %v", err)
		}
	}
	remind()
	if got := f.subjectsTo(t, email); len(got) != 0 {
		t.Fatalf("reminder sent 30 days ahead: %v", got)
	}
	// 默认提前 3 天
	f.clock.advance(28 * day)
	remind()
	remind()
	got := f.subjectsTo(t, email)
	if len(got) != 1 || !strings.Contains(got[0], "即将到期") || len(f.subjectsTo(t, giftedEmail)) != 0 {
		t.Fatalf("expiry reminders = %v (gifted: %v)", got, f.subjectsTo(t, giftedEmail))
	}
	// 续费后到期时间变了：临近新的到期时间时再提醒一次
	f.buyPlan(t, alice, month, "")
	f.clock.advance(31 * day)
	remind()
	if got = f.subjectsTo(t, email); len(got) != 2 {
		t.Fatalf("after renewal, reminders = %v, want one more", got)
	}

	// 流量：剩余不足 10% 时提醒一次
	f.exec(t, `UPDATE end_user SET traffic_used = traffic_limit - traffic_limit / 20 WHERE id = $1`, alice.UserID)
	remind()
	remind()
	if got = f.subjectsTo(t, email); len(got) != 3 || !strings.Contains(got[2], "流量即将用完") {
		t.Fatalf("traffic reminders = %v", got)
	}
	// 清零后再次低于阈值：可以再提醒
	f.exec(t, `UPDATE end_user SET traffic_used = 0 WHERE id = $1`, alice.UserID)
	remind()
	f.exec(t, `UPDATE end_user SET traffic_used = traffic_limit - 1 WHERE id = $1`, alice.UserID)
	remind()
	if got = f.subjectsTo(t, email); len(got) != 4 {
		t.Fatalf("after usage reset, reminders = %v, want one more", got)
	}

	// 关掉两项提醒后不再发
	if _, err := f.update(t, map[string]any{"notify.expireDays": 0, "notify.trafficPercent": 0}); err != nil {
		t.Fatalf("disable reminders: %v", err)
	}
	f.exec(t, `UPDATE end_user SET traffic_notified = FALSE, expire_notified_for = NULL WHERE id = $1`, alice.UserID)
	remind()
	if got = f.subjectsTo(t, email); len(got) != 4 {
		t.Fatalf("reminders while disabled = %v", got)
	}
}

// 邮件不可用时不发提醒，也不记“已提醒”，等邮件配好后还能补上（docs/03 §5.16 邮件条件）。
func TestRemindersWaitForMail(t *testing.T) {
	f := newShopFixture(t)
	ctx := context.Background()
	_, month, _ := f.salePlan(t, "basic", gib, false)
	alice := f.funded(t, "alice", 10000)
	email := f.verify(t, alice)
	f.buyPlan(t, alice, month, "")
	f.clock.advance(28 * day)

	noMail := NewShopService(f.pool, f.settings, f.users, NewMailService(f.pool, testSealer(t), nil, f.settings, f.clock.now), f.clock.now)
	if err := noMail.SendReminders(ctx); err != nil {
		t.Fatalf("SendReminders without mail: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM end_user WHERE id = $1 AND expire_notified_for IS NOT NULL`, alice.UserID); n != 0 {
		t.Fatal("reminder must not be marked as sent while mail is unavailable")
	}
	if err := f.shop.SendReminders(ctx); err != nil {
		t.Fatalf("SendReminders: %v", err)
	}
	if got := f.subjectsTo(t, email); len(got) != 1 {
		t.Fatalf("reminder after mail became available = %v", got)
	}
}
