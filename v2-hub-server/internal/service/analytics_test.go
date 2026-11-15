package service

import (
	"context"
	"testing"
	"time"
)

const (
	sqlAddTraffic     = `INSERT INTO user_traffic_hourly (user_id, hour, up, down) VALUES ($1, $2, $3, $4)`
	sqlSetUserCreated = `UPDATE end_user SET created_at = $2 WHERE id = $1`
	sqlSetUserExpiry  = `UPDATE end_user SET expires_at = $2, auto_renew = $3 WHERE id = $1`
	sqlSetUserStatus  = `UPDATE end_user SET status = $2, last_used_at = $3 WHERE id = $1`
	sqlAddSubscribe   = `INSERT INTO subscribe_log (user_id, format, user_agent, created_at) VALUES ($1, $2, $3, $4)`
	sqlAddOrder       = `INSERT INTO shop_order (order_no, user_id, username, kind, plan_id, item, is_trial, amount_original, amount_discount, amount_paid, coupon_code, created_at)
		VALUES ($1, $2, $3, $4, $5, '{}', $6, $7::bigint, $8::bigint, $7::bigint - $8::bigint, $9, $10)`
	sqlAddPayment   = `INSERT INTO payment (trade_no, username, gateway, amount, status, expires_at) VALUES ($1, 'alice', 'test', 100, $2, now())`
	sqlAddTicket    = `INSERT INTO ticket (user_id, subject, status, created_at, last_reply_at) VALUES ($1, 's', $2, $3, $3) RETURNING id`
	sqlAddTicketMsg = `INSERT INTO ticket_message (ticket_id, author_kind, content, created_at) VALUES ($1, 'admin', 'ok', $2)`
	sqlFailProv     = `UPDATE provision SET status = 'failed' WHERE user_id = $1`
	sqlAddMetric    = `INSERT INTO server_metric (server_id, bucket_at, cpu, online_clients) VALUES ($1, $2, $3, $4)`
	sqlAddMailRow   = `INSERT INTO mail_outbox (aad_key, recipient, subject, status) VALUES ('k', 'a@example.com', 's', $1)`
	sqlSumServerTr  = `SELECT COALESCE(SUM(up + down), 0) FROM server_traffic_hourly WHERE server_id = $1`
	analyticsDays   = 7
)

type analyticsFixture struct {
	*opsFixture
	analytics *AnalyticsService
}

func newAnalyticsFixture(t *testing.T) *analyticsFixture {
	t.Helper()
	of := newOpsFixture(t)
	return &analyticsFixture{opsFixture: of, analytics: NewAnalyticsService(of.pool, of.settings, nil, 0, of.clock.now)}
}

func TestAnalyticsDaysValidation(t *testing.T) {
	f := newAnalyticsFixture(t)
	ctx := context.Background()
	for _, days := range []int{1, 8, 31, 91, 365} {
		_, err := f.analytics.Users(ctx, days)
		wantErr(t, "users days", err, ErrInvalidArgument)
		_, err = f.analytics.Traffic(ctx, days, true)
		wantErr(t, "traffic days", err, ErrInvalidArgument)
		_, err = f.analytics.Finance(ctx, days)
		wantErr(t, "finance days", err, ErrInvalidArgument)
	}
	_, err := f.analytics.Servers(ctx, "30d")
	wantErr(t, "servers range", err, ErrInvalidArgument)
	// 缺省取 30 天
	u, err := f.analytics.Users(ctx, 0)
	if err != nil || len(u.Growth) != analyticsDaysMonth || len(u.ActiveDays) != analyticsDaysMonth {
		t.Fatalf("default window = %d days, %v", len(u.Growth), err)
	}
}

func TestAnalyticsUsers(t *testing.T) {
	f := newAnalyticsFixture(t)
	ctx := context.Background()
	now := f.clock.now()
	plan := f.plan(t, "basic", PlanQuota{})
	alice := f.newUser(t, "alice", plan.ID)
	bob := f.newUser(t, "bob", plan.ID)
	carol := f.newUser(t, "carol", 0)
	// alice 是窗口之前注册的老用户；bob 7 日内到期且没开自动续费；carol 已过期
	f.exec(t, sqlSetUserCreated, alice.ID, now.AddDate(0, 0, -20))
	f.exec(t, sqlSetUserExpiry, bob.ID, now.Add(48*time.Hour), false)
	f.exec(t, sqlSetUserExpiry, alice.ID, now.Add(72*time.Hour), true)
	f.exec(t, sqlSetUserExpiry, carol.ID, now.Add(-time.Hour), false)
	f.exec(t, sqlSetUserStatus, carol.ID, UserStatusExpired, nil)
	f.exec(t, sqlSetUserStatus, alice.ID, UserStatusActive, now)
	// 流量：alice 今天与昨天，bob 只有 10 天前
	f.exec(t, sqlAddTraffic, alice.ID, hourOf(now), 100, 400)
	f.exec(t, sqlAddTraffic, alice.ID, hourOf(now.Add(-24*time.Hour)), 10, 20)
	f.exec(t, sqlAddTraffic, bob.ID, hourOf(now.AddDate(0, 0, -10)), 5, 5)
	f.exec(t, sqlAddSubscribe, alice.ID, "mihomo", "clash-verge/v2.0.0", now)
	f.exec(t, sqlAddSubscribe, alice.ID, "mihomo", "FlClash/0.8", now)
	f.exec(t, sqlAddSubscribe, bob.ID, "base64", "v2rayNG/1.9.0", now)
	f.exec(t, sqlAddSubscribe, bob.ID, "singbox", "SFA/1.10 sing-box", now)
	f.exec(t, sqlAddSubscribe, carol.ID, "base64", "", now)

	u, err := f.analytics.Users(ctx, analyticsDays)
	if err != nil {
		t.Fatalf("Users: %v", err)
	}
	last := u.Growth[len(u.Growth)-1]
	if len(u.Growth) != analyticsDays || last.NewUsers != 2 || last.TotalUsers != 3 || u.Growth[0].TotalUsers != 1 {
		t.Fatalf("growth = %+v", u.Growth)
	}
	if u.DAU != 1 || u.WAU != 1 || u.MAU != 2 {
		t.Fatalf("dau/wau/mau = %d/%d/%d", u.DAU, u.WAU, u.MAU)
	}
	if u.ActiveDays[len(u.ActiveDays)-1].Count != 1 {
		t.Fatalf("active days = %+v", u.ActiveDays)
	}
	if u.Status.Active != 2 || u.Status.Expired != 1 {
		t.Fatalf("status = %+v", u.Status)
	}
	if u.Expiry.Within7d != 2 || u.Expiry.Within7dNoAutoRenew != 1 || u.Expiry.Expired != 1 {
		t.Fatalf("expiry = %+v", u.Expiry)
	}
	// bob 有效但从未使用：沉默；alice 刚用过
	if u.SilentUsers != 1 {
		t.Fatalf("silent users = %d", u.SilentUsers)
	}
	if len(u.Plans) != 2 || u.Plans[0].Name != "basic" || u.Plans[0].Users != 2 || u.Plans[1].PlanID != nil || u.Plans[1].Users != 1 {
		t.Fatalf("plans = %+v", u.Plans)
	}
	if len(u.TopUsers) != 1 || u.TopUsers[0].Username != "alice" || u.TopUsers[0].Bytes != 530 || u.TopUsers[0].PlanName != "basic" {
		t.Fatalf("top users = %+v", u.TopUsers)
	}
	clients := make(map[string]SubscribeClient, len(u.SubscribeClients))
	for _, c := range u.SubscribeClients {
		clients[c.Client] = c
	}
	if clients["clash"].Pulls != 2 || clients["clash"].Users != 1 || clients["v2rayng"].Pulls != 1 || clients["singbox"].Pulls != 1 || clients["other"].Pulls != 1 {
		t.Fatalf("subscribe clients = %+v", u.SubscribeClients)
	}
	if today := u.SubscribeDays[len(u.SubscribeDays)-1]; today.Pulls != 5 || today.Users != 3 {
		t.Fatalf("subscribe today = %+v", today)
	}
	if len(u.SubscribeFormats) != 3 || u.SubscribeFormats[0].Pulls != 2 {
		t.Fatalf("subscribe formats = %+v", u.SubscribeFormats)
	}
}

func TestAnalyticsTrafficAndServers(t *testing.T) {
	f := newAnalyticsFixture(t)
	ctx := context.Background()
	now := f.clock.now()
	plan := f.plan(t, "basic", PlanQuota{})
	alice := f.newUser(t, "alice", plan.ID)
	f.reconcile(t)

	// 用量任务顺带按服务器累加（Ruling-66）
	f.fleet.get(fakeBase).AddTraffic(f.emailOf(t, alice.ID), 300, 700)
	if err := f.users.CollectServerTraffic(ctx, f.server.ID); err != nil {
		t.Fatalf("CollectServerTraffic: %v", err)
	}
	if n := f.count(t, sqlSumServerTr, f.server.ID); n != 1000 {
		t.Fatalf("server traffic = %d, want 1000", n)
	}

	tr, err := f.analytics.Traffic(ctx, analyticsDays, true)
	if err != nil {
		t.Fatalf("Traffic: %v", err)
	}
	today := tr.Days[len(tr.Days)-1]
	if len(tr.Days) != analyticsDays || today.Up != 300 || today.Down != 700 || len(tr.Hours) != analyticsHoursPerDay {
		t.Fatalf("traffic = %+v", tr)
	}
	var hourly int64
	for _, h := range tr.Hours {
		hourly += h.Bytes
	}
	if hourly != 1000/analyticsDays {
		t.Fatalf("hourly average total = %d", hourly)
	}
	if len(tr.Servers) != 1 || tr.Servers[0].ServerID != f.server.ID || tr.Servers[0].Up != 300 {
		t.Fatalf("traffic servers = %+v", tr.Servers)
	}
	// 没有 server:read：不带按服务器的那一块
	if tr, err = f.analytics.Traffic(ctx, analyticsDays, false); err != nil || tr.Servers != nil {
		t.Fatalf("traffic without servers = %+v, %v", tr.Servers, err)
	}

	// 服务器分区：近 15 分钟内两条采样，可用率按时段数算
	bucket := now.Truncate(metricBucket)
	f.exec(t, sqlAddMetric, f.server.ID, bucket, 42.5, 7)
	f.exec(t, sqlAddMetric, f.server.ID, bucket.Add(-metricBucket), 10.0, 3)
	sv, err := f.analytics.Servers(ctx, "")
	if err != nil {
		t.Fatalf("Servers: %v", err)
	}
	if len(sv.Availability) != 1 || sv.Availability[0].Samples < 1 || sv.Availability[0].Percent <= 0 || sv.Availability[0].Percent > analyticsFullPercent {
		t.Fatalf("availability = %+v", sv.Availability)
	}
	if len(sv.Online) != 2 || sv.Online[1].Connections != 7 {
		t.Fatalf("online series = %+v", sv.Online)
	}
	if len(sv.TopCPU) != 1 || sv.TopCPU[0].CPU == nil || *sv.TopCPU[0].CPU != 42.5 || sv.TopOnline[0].OnlineClients != 7 {
		t.Fatalf("top loads = %+v / %+v", sv.TopCPU, sv.TopOnline)
	}
	if sv.Reach != nil {
		t.Fatalf("reach summary must be nil without reach service: %+v", sv.Reach)
	}
	if sv, err = f.analytics.Servers(ctx, MetricRange7d); err != nil || len(sv.Online) == 0 {
		t.Fatalf("7d online series = %+v, %v", sv, err)
	}
}

func TestAnalyticsFinance(t *testing.T) {
	f := newAnalyticsFixture(t)
	ctx := context.Background()
	now := f.clock.now()
	plan := f.plan(t, "basic", PlanQuota{})
	alice := f.newUser(t, "alice", plan.ID)
	bob := f.newUser(t, "bob", plan.ID)
	f.exec(t, sqlAddOrder, "O1", alice.ID, "alice", "plan", plan.ID, true, 0, 0, "", now)
	f.exec(t, sqlAddOrder, "O2", alice.ID, "alice", "renew", plan.ID, false, 1000, 200, "SAVE20", now)
	f.exec(t, sqlAddOrder, "O3", alice.ID, "alice", "traffic_pack", nil, false, 500, 0, "", now)
	f.exec(t, sqlAddOrder, "O4", bob.ID, "bob", "plan", plan.ID, false, 3000, 0, "", now.AddDate(0, 0, -20))
	f.exec(t, sqlAddPayment, "P1", "paid")
	f.exec(t, sqlAddPayment, "P2", "expired")
	f.exec(t, sqlAddPayment, "P3", "pending")

	fin, err := f.analytics.Finance(ctx, analyticsDays)
	if err != nil {
		t.Fatalf("Finance: %v", err)
	}
	if today := fin.Days[len(fin.Days)-1]; len(fin.Days) != analyticsDays || today.OrderPaid != 1300 {
		t.Fatalf("finance days = %+v", fin.Days)
	}
	kinds := make(map[string]OrderKindStats, len(fin.OrderKinds))
	for _, k := range fin.OrderKinds {
		kinds[k.Kind] = k
	}
	if len(fin.OrderKinds) != 3 || kinds[orderKindPlan].Count != 1 || kinds[orderKindRenew].Paid != 800 || kinds[orderKindTrafficPack].Paid != 500 {
		t.Fatalf("order kinds = %+v", fin.OrderKinds)
	}
	if fin.TrialCount != 1 || fin.Payers != 1 || fin.PayersPaid != 1300 {
		t.Fatalf("trial / payers = %d / %d / %d", fin.TrialCount, fin.Payers, fin.PayersPaid)
	}
	if len(fin.Plans) != 1 || fin.Plans[0].Name != "basic" || fin.Plans[0].Count != 2 || fin.Plans[0].Paid != 800 {
		t.Fatalf("plan sales = %+v", fin.Plans)
	}
	// 两个用户都是窗口内注册的：alice 试用过且付过费，bob 付过费（订单在窗口外也算）
	if fin.FunnelRegistered != 2 || fin.FunnelTrial != 1 || fin.FunnelPaid != 2 {
		t.Fatalf("funnel = %d / %d / %d", fin.FunnelRegistered, fin.FunnelTrial, fin.FunnelPaid)
	}
	if fin.PaymentsPaid != 1 || fin.PaymentsExpired != 1 || fin.PaymentsPending != 1 {
		t.Fatalf("payments = %+v", fin)
	}
	if len(fin.Coupons) != 1 || fin.Coupons[0].Code != "SAVE20" || fin.Coupons[0].Discount != 200 || fin.Coupons[0].Paid != 800 {
		t.Fatalf("coupons = %+v", fin.Coupons)
	}
}

func TestAnalyticsBacklog(t *testing.T) {
	f := newAnalyticsFixture(t)
	ctx := context.Background()
	now := f.clock.now()
	plan := f.plan(t, "basic", PlanQuota{})
	alice := f.newUser(t, "alice", plan.ID)
	f.reconcile(t)
	f.exec(t, sqlFailProv, alice.ID)
	var answered int64
	if err := f.pool.QueryRow(ctx, sqlAddTicket, alice.ID, "answered", now.Add(-2*time.Hour)).Scan(&answered); err != nil {
		t.Fatal(err)
	}
	f.exec(t, sqlAddTicketMsg, answered, now.Add(-90*time.Minute))
	var open int64
	if err := f.pool.QueryRow(ctx, sqlAddTicket, alice.ID, "open", now.Add(-3*time.Hour)).Scan(&open); err != nil {
		t.Fatal(err)
	}
	f.exec(t, sqlAddMailRow, "pending")
	f.exec(t, sqlAddMailRow, "failed")

	b, err := f.analytics.Backlog(ctx, f.actor)
	if err != nil {
		t.Fatalf("Backlog: %v", err)
	}
	if b.Tickets == nil || b.Tickets.Open != 1 || b.Tickets.OldestWaitingSince == nil || b.Tickets.AvgFirstReplyMinutes == nil || *b.Tickets.AvgFirstReplyMinutes != 30 {
		t.Fatalf("tickets = %+v", b.Tickets)
	}
	if b.Provisions == nil || b.Provisions.Failed != 1 || len(b.Provisions.Servers) != 1 || b.Provisions.Servers[0].ServerID != f.server.ID {
		t.Fatalf("provisions = %+v", b.Provisions)
	}
	if b.Panels == nil || len(b.Panels.ChainFailed) != 0 {
		t.Fatalf("panels = %+v", b.Panels)
	}
	if b.Outbox == nil || b.Outbox.MailPending != 1 || b.Outbox.MailFailed24h != 1 {
		t.Fatalf("outbox = %+v", b.Outbox)
	}
	// 只有 ticket:read：其余块为空
	limited := &AdminPrincipal{AdminID: f.actor.AdminID, Permissions: PermissionSet{PermTicketRead: {}}}
	part, err := f.analytics.Backlog(ctx, limited)
	if err != nil || part.Tickets == nil || part.Provisions != nil || part.Panels != nil || part.Outbox != nil {
		t.Fatalf("ticket-only backlog = %+v, %v", part, err)
	}
}

func TestAnalyticsCache(t *testing.T) {
	of := newOpsFixture(t)
	ctx := context.Background()
	cached := NewAnalyticsService(of.pool, of.settings, nil, time.Minute, of.clock.now)
	first, err := cached.Users(ctx, analyticsDays)
	if err != nil {
		t.Fatal(err)
	}
	of.newUser(t, "late", 0)
	again, err := cached.Users(ctx, analyticsDays)
	if err != nil || again != first {
		t.Fatalf("second call within ttl must hit the cache: %v", err)
	}
	// 参数不同不共用；过了 ttl 重新算
	other, err := cached.Users(ctx, analyticsDaysMonth)
	if err != nil || other.Growth[len(other.Growth)-1].TotalUsers != first.Growth[len(first.Growth)-1].TotalUsers+1 {
		t.Fatalf("different days must not share the cache: %v", err)
	}
	of.clock.advance(2 * time.Minute)
	fresh, err := cached.Users(ctx, analyticsDays)
	if err != nil || fresh == first || fresh.Growth[len(fresh.Growth)-1].NewUsers != first.Growth[len(first.Growth)-1].NewUsers+1 {
		t.Fatalf("expired cache must be reloaded: %v", err)
	}
}
