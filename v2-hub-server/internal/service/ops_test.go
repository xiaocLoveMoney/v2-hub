package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"v2-hub-server/internal/manager/telegram"
)

const (
	sqlSetServerOffline = `UPDATE server SET status = 'offline', last_error = '连不上', offline_since = $2 WHERE id = $1`
	sqlSetServerOnline  = `UPDATE server SET status = 'online', offline_since = NULL WHERE id = $1`
	sqlDisableServer    = `UPDATE server SET is_disabled = TRUE WHERE id = $1`
	sqlCountMailSubject = `SELECT COUNT(*) FROM mail_outbox WHERE subject LIKE $1`
	sqlCountTelegram    = `SELECT COUNT(*) FROM telegram_outbox WHERE status = $1`
	opsAlertEmail       = "ops@example.com"
	opsChatID           = "-1001234567890"
)

// fakeTelegram 记下发出的消息；fail 非空时每次发送都返回它。
type fakeTelegram struct {
	mu   sync.Mutex
	sent []string
	fail error
}

func (f *fakeTelegram) SendMessage(_ context.Context, chatID, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.sent = append(f.sent, chatID+"|"+text)
	return nil
}

func (f *fakeTelegram) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

type opsFixture struct {
	*accountFixture
	tg       *fakeTelegram
	telegram *TelegramService
	ops      *OpsService
}

func newOpsFixture(t *testing.T) *opsFixture {
	t.Helper()
	af := newAccountFixture(t)
	tg := &fakeTelegram{}
	tgSvc := NewTelegramService(af.pool, tg, af.settings, af.clock.now)
	ops := NewOpsService(af.pool, af.settings, af.mail, tgSvc, af.clock.now)
	f := &opsFixture{accountFixture: af, tg: tg, telegram: tgSvc, ops: ops}
	if _, err := f.set(t, map[string]map[string]any{
		"alert":    {"emails": []string{opsAlertEmail}, "offlineMinutes": 5},
		"telegram": {"chatId": opsChatID},
	}); err != nil {
		t.Fatalf("configure ops settings: %v", err)
	}
	return f
}

// set 用分组形式的补丁改设置（与接口一致），内部摊平成“分组.键”。
func (f *opsFixture) set(t *testing.T, groups map[string]map[string]any) (Settings, error) {
	t.Helper()
	flat := make(map[string]any, len(groups))
	for g, items := range groups {
		for k, v := range items {
			flat[g+"."+k] = v
		}
	}
	return f.update(t, flat)
}

func (f *opsFixture) count(t *testing.T, sql string, arg any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), sql, arg).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func (f *opsFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

func (f *opsFixture) checkAlerts(t *testing.T) {
	t.Helper()
	if err := f.ops.CheckAlerts(context.Background()); err != nil {
		t.Fatalf("CheckAlerts: %v", err)
	}
}

func TestServerAlerts(t *testing.T) {
	f := newOpsFixture(t)
	ctx := context.Background()
	now := f.clock.now()

	// 不在线未满阈值：不告警
	f.exec(t, sqlSetServerOffline, f.server.ID, now.Add(-3*time.Minute))
	f.checkAlerts(t)
	if n := f.count(t, sqlCountMailSubject, "%不在线%"); n != 0 {
		t.Fatalf("should not alert before the threshold, mails = %d", n)
	}

	// 满 5 分钟：邮件与 Telegram 各一条；再检查不重复
	f.clock.advance(3 * time.Minute)
	f.checkAlerts(t)
	f.checkAlerts(t)
	if n := f.count(t, sqlCountMailSubject, "%"+f.server.Name+" 不在线%"); n != 1 {
		t.Fatalf("alert mails = %d, want 1", n)
	}
	if n := f.count(t, sqlCountTelegram, "pending"); n != 1 {
		t.Fatalf("pending telegram = %d, want 1", n)
	}
	if _, err := f.telegram.ProcessOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	msgs := f.tg.messages()
	if len(msgs) != 1 || !strings.HasPrefix(msgs[0], opsChatID+"|") || !strings.Contains(msgs[0], "已持续：6 分钟") || !strings.Contains(msgs[0], "连不上") {
		t.Fatalf("telegram messages = %v", msgs)
	}

	// 恢复：补一条恢复通知，标记清掉
	f.exec(t, sqlSetServerOnline, f.server.ID)
	f.checkAlerts(t)
	f.checkAlerts(t)
	if n := f.count(t, sqlCountMailSubject, "%已恢复在线%"); n != 1 {
		t.Fatalf("recovery mails = %d, want 1", n)
	}

	// 停用的服务器：不告警
	f.exec(t, sqlSetServerOffline, f.server.ID, f.clock.now().Add(-time.Hour))
	f.exec(t, sqlDisableServer, f.server.ID)
	f.checkAlerts(t)
	if n := f.count(t, sqlCountMailSubject, "%不在线%"); n != 1 {
		t.Fatalf("disabled server must not alert, mails = %d", n)
	}
}

func TestTelegramOutbox(t *testing.T) {
	f := newOpsFixture(t)
	ctx := context.Background()
	f.telegram.Notify(ctx, f.telegram.q, TelegramEventTicket, "工单待处理：测试")
	f.telegram.Notify(ctx, f.telegram.q, TelegramEventPayment, "默认不发")
	if n := f.count(t, sqlCountTelegram, "pending"); n != 1 {
		t.Fatalf("payments are off by default, pending = %d", n)
	}
	// 被拒绝：直接放弃
	f.tg.fail = fmt.Errorf("%w: chat not found", telegram.ErrRejected)
	if _, err := f.telegram.ProcessOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, sqlCountTelegram, "failed"); n != 1 {
		t.Fatalf("rejected message should fail at once, failed = %d", n)
	}
	// 连不上：按退避重试，到时间后发出
	f.telegram.Notify(ctx, f.telegram.q, TelegramEventTicket, "第二条")
	f.tg.fail = telegram.ErrUnreachable
	if _, err := f.telegram.ProcessOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	f.tg.fail = nil
	if n, _ := f.telegram.ProcessOutbox(ctx); n != 0 {
		t.Fatalf("should wait for backoff, processed %d", n)
	}
	f.clock.advance(2 * time.Minute)
	if _, err := f.telegram.ProcessOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, sqlCountTelegram, "sent"); n != 1 {
		t.Fatalf("sent = %d, want 1", n)
	}

	// 测试消息：没配会话时 403
	if err := f.telegram.SendTest(ctx, f.actor, nil); err != nil {
		t.Fatalf("SendTest: %v", err)
	}
	if _, err := f.set(t, map[string]map[string]any{"telegram": {"chatId": ""}}); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "test without chat id", f.telegram.SendTest(ctx, f.actor, nil), ErrFeatureDisabled)
	disabled := NewTelegramService(f.pool, nil, f.settings, f.clock.now)
	wantErr(t, "test without token", disabled.SendTest(ctx, f.actor, nil), ErrFeatureDisabled)
}

func TestDailyReportAndDashboard(t *testing.T) {
	f := newOpsFixture(t)
	ctx := context.Background()
	sent, err := f.ops.DailyReport(ctx)
	if err != nil || sent {
		t.Fatalf("report is off by default: %v %v", sent, err)
	}
	if _, err := f.set(t, map[string]map[string]any{"report": {"dailyEnabled": true, "dailyHour": 0}}); err != nil {
		t.Fatal(err)
	}
	if sent, err = f.ops.DailyReport(ctx); err != nil || !sent {
		t.Fatalf("first report: %v %v", sent, err)
	}
	if sent, err = f.ops.DailyReport(ctx); err != nil || sent {
		t.Fatalf("report must be sent once a day: %v %v", sent, err)
	}
	if n := f.count(t, sqlCountMailSubject, "%运营日报%"); n != 1 {
		t.Fatalf("report mails = %d", n)
	}
	f.clock.advance(24 * time.Hour)
	if sent, err = f.ops.DailyReport(ctx); err != nil || !sent {
		t.Fatalf("next day report: %v %v", sent, err)
	}

	// 仪表盘按权限出区块
	full, err := f.ops.Dashboard(ctx, f.actor)
	if err != nil || full.Users == nil || full.Traffic == nil || len(full.Traffic.Days) != 7 || full.Servers == nil || full.Finance == nil || full.OpenTickets == nil {
		t.Fatalf("super admin dashboard = %+v, %v", full, err)
	}
	if full.Users.Total != f.count(t, `SELECT COUNT(*) FROM end_user WHERE deleted_at IS NULL AND id > $1`, 0) || full.Servers.Total != 1 {
		t.Fatalf("dashboard numbers = %+v %+v", full.Users, full.Servers)
	}
	limited := &AdminPrincipal{AdminID: f.actor.AdminID, Permissions: PermissionSet{PermServerRead: {}}}
	part, err := f.ops.Dashboard(ctx, limited)
	if err != nil || part.Users != nil || part.Traffic != nil || part.Finance != nil || part.OpenTickets != nil || part.Servers == nil {
		t.Fatalf("server-only dashboard = %+v, %v", part, err)
	}
}

func TestOpsSettingsValidation(t *testing.T) {
	f := newOpsFixture(t)
	for what, patch := range map[string]map[string]any{
		"bad chat id":       {"telegram.chatId": "not a chat"},
		"http support url":  {"support.url": "http://chat.example.com"},
		"too many emails":   {"alert.emails": manyEmails(11)},
		"offline minutes 0": {"alert.offlineMinutes": 0},
		"report hour 24":    {"report.dailyHour": 24},
		"empty label":       {"support.label": ""},
	} {
		if _, err := f.update(t, patch); err == nil {
			t.Fatalf("%s should be rejected", what)
		}
	}
	cur, err := f.set(t, map[string]map[string]any{
		"support": {"url": "https://chat.example.com/room", "label": "联系我们"},
		"landing": {"enabled": true, "headline": "快速稳定", "intro": "# 欢迎\n\n多线路"},
	})
	if err != nil || cur.Support.URL == "" || !cur.Landing.Enabled {
		t.Fatalf("valid ops settings = %+v, %v", cur, err)
	}
	site, err := f.account.Site(context.Background())
	if err != nil || site.Landing.Headline != "快速稳定" || site.Support.Label != "联系我们" {
		t.Fatalf("site info = %+v, %v", site, err)
	}
	if _, err := f.set(t, map[string]map[string]any{"landing": {"enabled": false}}); err != nil {
		t.Fatal(err)
	}
	if site, _ = f.account.Site(context.Background()); site.Landing.Intro != "" {
		t.Fatalf("disabled landing must not leak its content: %+v", site.Landing)
	}
}

func manyEmails(n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, fmt.Sprintf("ops%d@example.com", i))
	}
	return out
}
