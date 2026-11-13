package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"v2-hub-server/internal/manager/panel"
)

type contentFixture struct {
	*accountFixture
	content *ContentService
	tickets *TicketService
}

func newContentFixture(t *testing.T) *contentFixture {
	t.Helper()
	af := newAccountFixture(t)
	af.users.cfg.Settings = af.settings
	return &contentFixture{
		accountFixture: af,
		content:        NewContentService(af.pool, testSealer(t), af.settings, af.mail, af.clock.now),
		tickets:        NewTicketService(af.pool, af.settings, af.mail, af.clock.now),
	}
}

func (f *contentFixture) principal(t *testing.T, username string, planID int64) *UserPrincipal {
	t.Helper()
	u := f.newUser(t, username, planID)
	return &UserPrincipal{UserID: u.ID, Username: u.Username, Status: u.Status}
}

func TestAnnouncementsPublishOrderAndHighlights(t *testing.T) {
	f := newContentFixture(t)
	ctx := context.Background()
	draft, err := f.content.CreateAnnouncement(ctx, f.actor, AnnouncementInput{Title: " 草稿 ", Content: "# 还没写完"}, nil)
	if err != nil || draft.Title != "草稿" || draft.PublishedAt != nil {
		t.Fatalf("draft = %+v, %v", draft, err)
	}
	old, err := f.content.CreateAnnouncement(ctx, f.actor, AnnouncementInput{Title: "置顶的旧公告", Content: "x", IsPinned: true, IsPublished: true}, nil)
	if err != nil || old.PublishedAt == nil {
		t.Fatalf("pinned = %+v, %v", old, err)
	}
	f.clock.advance(time.Hour)
	popup, err := f.content.CreateAnnouncement(ctx, f.actor, AnnouncementInput{Title: "弹窗", Content: "y", IsPopup: true, IsPublished: true}, nil)
	if err != nil {
		t.Fatalf("popup: %v", err)
	}
	f.clock.advance(time.Hour)
	if _, err := f.content.CreateAnnouncement(ctx, f.actor, AnnouncementInput{Title: "最新", Content: "z", IsPublished: true}, nil); err != nil {
		t.Fatalf("latest: %v", err)
	}

	page, err := f.content.ListPublishedAnnouncements(ctx, PageQuery{})
	if err != nil || page.Total != 3 {
		t.Fatalf("published = %+v, %v", page, err)
	}
	titles := []string{page.Items[0].Title, page.Items[1].Title, page.Items[2].Title}
	if strings.Join(titles, ",") != "置顶的旧公告,最新,弹窗" {
		t.Fatalf("order = %v, want pinned first then newest first, drafts hidden", titles)
	}
	latest, pop, err := f.content.Highlights(ctx)
	if err != nil || latest == nil || latest.ID != old.ID || pop == nil || pop.ID != popup.ID {
		t.Fatalf("highlights = %+v / %+v, %v", latest, pop, err)
	}
	all, err := f.content.ListAnnouncements(ctx, PageQuery{})
	if err != nil || all.Total != 4 {
		t.Fatalf("admin list = %+v, %v", all, err)
	}

	// 已发布的再保存：发布时间不变；改回草稿后用户端看不到
	republished, err := f.content.UpdateAnnouncement(ctx, f.actor, old.ID, AnnouncementInput{Title: "置顶的旧公告", Content: "改了", IsPinned: true, IsPublished: true}, nil)
	if err != nil || !republished.PublishedAt.Equal(*old.PublishedAt) || republished.Content != "改了" {
		t.Fatalf("re-save published = %+v, %v", republished, err)
	}
	if _, err := f.content.UpdateAnnouncement(ctx, f.actor, old.ID, AnnouncementInput{Title: "置顶的旧公告", Content: "改了"}, nil); err != nil {
		t.Fatalf("unpublish: %v", err)
	}
	if page, err := f.content.ListPublishedAnnouncements(ctx, PageQuery{}); err != nil || page.Total != 2 {
		t.Fatalf("after unpublish = %+v, %v", page, err)
	}
	for name, in := range map[string]AnnouncementInput{
		"empty title": {Title: "  ", Content: "x"},
		"long title":  {Title: strings.Repeat("长", maxAnnouncementTitle+1), Content: "x"},
		"long body":   {Title: "t", Content: strings.Repeat("a", maxMarkdownLen+1)},
	} {
		if _, err := f.content.CreateAnnouncement(ctx, f.actor, in, nil); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("%s = %v, want invalid_argument", name, err)
		}
	}
	if err := f.content.DeleteAnnouncement(ctx, f.actor, draft.ID, nil); err != nil {
		t.Fatalf("DeleteAnnouncement: %v", err)
	}
	if err := f.content.DeleteAnnouncement(ctx, f.actor, draft.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice = %v", err)
	}
}

func TestMailAnnouncementOnceToVerifiedEnabledUsers(t *testing.T) {
	f := newContentFixture(t)
	ctx := context.Background()
	for _, row := range []struct {
		name, email      string
		verified, banned bool
	}{
		{"alice", "alice@example.com", true, false},
		{"bob", "bob@example.com", false, false},   // 未验证
		{"carol", "carol@example.com", true, true}, // 已停用
		{"dave", "", false, false},                 // 没有邮箱
	} {
		u := f.newUser(t, row.name, 0)
		if _, err := f.pool.Exec(ctx, `UPDATE end_user SET email = NULLIF($2, ''), email_verified_at = CASE WHEN $3 THEN now() END, is_disabled = $4 WHERE id = $1`,
			u.ID, row.email, row.verified, row.banned); err != nil {
			t.Fatalf("seed %s: %v", row.name, err)
		}
	}
	draft, err := f.content.CreateAnnouncement(ctx, f.actor, AnnouncementInput{Title: "维护通知", Content: "今晚 **维护**"}, nil)
	if err != nil {
		t.Fatalf("CreateAnnouncement: %v", err)
	}
	if _, err := f.content.MailAnnouncement(ctx, f.actor, draft.ID, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("mail a draft = %v, want conflict", err)
	}
	if _, err := f.content.UpdateAnnouncement(ctx, f.actor, draft.ID, AnnouncementInput{Title: "维护通知", Content: "今晚 **维护**", IsPublished: true}, nil); err != nil {
		t.Fatalf("publish: %v", err)
	}
	queued, err := f.content.MailAnnouncement(ctx, f.actor, draft.ID, nil)
	if err != nil || queued != 1 {
		t.Fatalf("MailAnnouncement = %d, %v; want exactly the one verified, enabled user", queued, err)
	}
	if _, err := f.content.MailAnnouncement(ctx, f.actor, draft.ID, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("mail twice = %v, want conflict", err)
	}
	f.flush(t)
	msgs := f.sender.messages()
	if len(msgs) != 1 || msgs[0].To != "alice@example.com" || !strings.Contains(msgs[0].Subject, "维护通知") || !strings.Contains(msgs[0].Text, "今晚 **维护**") {
		t.Fatalf("mails = %+v", msgs)
	}
	if _, err := f.content.MailAnnouncement(ctx, f.actor, 9999, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mail missing announcement = %v", err)
	}
}

func TestTutorialsValidateLinksAndFilterPublished(t *testing.T) {
	f := newContentFixture(t)
	ctx := context.Background()
	in := TutorialInput{Platform: PlatformIOS, ClientName: "Shadowrocket", Summary: "iOS 客户端", Content: "1. 安装",
		Links: []TutorialLink{{Label: "App Store", URL: "https://apps.apple.com/app/id932747118"}}, SortOrder: 2, IsPublished: true}
	tut, err := f.content.CreateTutorial(ctx, f.actor, in, nil)
	if err != nil || len(tut.Links) != 1 || tut.Links[0].Label != "App Store" {
		t.Fatalf("CreateTutorial = %+v, %v", tut, err)
	}
	hidden := in
	hidden.ClientName, hidden.IsPublished, hidden.Platform, hidden.Links = "草稿客户端", false, PlatformAndroid, nil
	if _, err := f.content.CreateTutorial(ctx, f.actor, hidden, nil); err != nil {
		t.Fatalf("create hidden: %v", err)
	}
	if all, err := f.content.ListTutorials(ctx, false); err != nil || len(all) != 2 {
		t.Fatalf("admin tutorials = %+v, %v", all, err)
	}
	pub, err := f.content.ListTutorials(ctx, true)
	if err != nil || len(pub) != 1 || pub[0].ClientName != "Shadowrocket" {
		t.Fatalf("published tutorials = %+v, %v", pub, err)
	}
	bad := map[string]func(*TutorialInput){
		"javascript link": func(i *TutorialInput) { i.Links = []TutorialLink{{Label: "x", URL: "javascript:alert(1)"}} },
		"http link":       func(i *TutorialInput) { i.Links = []TutorialLink{{Label: "x", URL: "http://example.com/a.apk"}} },
		"empty label":     func(i *TutorialInput) { i.Links = []TutorialLink{{Label: " ", URL: "https://example.com"}} },
		"too many links":  func(i *TutorialInput) { i.Links = make([]TutorialLink, maxTutorialLinks+1) },
		"bad platform":    func(i *TutorialInput) { i.Platform = "symbian" },
		"empty name":      func(i *TutorialInput) { i.ClientName = "" },
		"bad sort":        func(i *TutorialInput) { i.SortOrder = -1 },
	}
	for name, mutate := range bad {
		cp := in
		mutate(&cp)
		if _, err := f.content.CreateTutorial(ctx, f.actor, cp, nil); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("%s = %v, want invalid_argument", name, err)
		}
	}
	in.ClientName = "Shadowrocket 2"
	if got, err := f.content.UpdateTutorial(ctx, f.actor, tut.ID, in, nil); err != nil || got.ClientName != "Shadowrocket 2" {
		t.Fatalf("UpdateTutorial = %+v, %v", got, err)
	}
	if _, err := f.content.UpdateTutorial(ctx, f.actor, 9999, in, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing = %v", err)
	}
	if err := f.content.DeleteTutorial(ctx, f.actor, tut.ID, nil); err != nil {
		t.Fatalf("DeleteTutorial: %v", err)
	}
}

func TestSharedAccountsVisibilityAndEncryption(t *testing.T) {
	f := newContentFixture(t)
	ctx := context.Background()
	basic := f.plan(t, "basic", PlanQuota{TrafficLimit: gib, DurationDays: 30})
	pro := f.plan(t, "pro", PlanQuota{TrafficLimit: 10 * gib, DurationDays: 30})
	basicUser := f.principal(t, "basicuser", basic.ID)
	proUser := f.principal(t, "prouser", pro.ID)
	noPlan := f.principal(t, "noplan", 0)

	in := SharedAccountInput{Name: "Netflix 合租", Category: "流媒体", Account: "share@example.com", Remark: "只用 2 号位", IsPublished: true}
	if _, err := f.content.CreateSharedAccount(ctx, f.actor, in, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("create without credential = %v, want invalid_argument", err)
	}
	in.Secret = "s3cret-value"
	forAll, err := f.content.CreateSharedAccount(ctx, f.actor, in, nil)
	if err != nil {
		t.Fatalf("CreateSharedAccount: %v", err)
	}
	proOnly := in
	proOnly.Name, proOnly.PlanIDs, proOnly.Secret = "Apple ID", []int64{pro.ID, pro.ID}, "pro-only-value"
	restricted, err := f.content.CreateSharedAccount(ctx, f.actor, proOnly, nil)
	if err != nil || len(restricted.PlanIDs) != 1 {
		t.Fatalf("restricted = %+v, %v", restricted, err)
	}
	hidden := in
	hidden.Name, hidden.IsPublished = "未发布", false
	unpublished, err := f.content.CreateSharedAccount(ctx, f.actor, hidden, nil)
	if err != nil {
		t.Fatalf("create unpublished: %v", err)
	}
	bad := in
	bad.PlanIDs = []int64{9999}
	if _, err := f.content.CreateSharedAccount(ctx, f.actor, bad, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unknown plan = %v, want invalid_argument", err)
	}

	var stored []byte
	if err := f.pool.QueryRow(ctx, "SELECT credential_enc FROM shared_account WHERE id = $1", forAll.ID).Scan(&stored); err != nil {
		t.Fatalf("read credential: %v", err)
	}
	if strings.Contains(string(stored), "s3cret-value") {
		t.Fatal("shared account credential must be encrypted at rest")
	}

	// 设置默认关闭：用户侧整体不可用
	if _, err := f.content.ListVisibleSharedAccounts(ctx, proUser); !errors.Is(err, ErrFeatureDisabled) {
		t.Fatalf("shared page off = %v, want feature_disabled", err)
	}
	if _, err := f.update(t, map[string]any{"shared.enabled": true}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	count := func(p *UserPrincipal) int {
		list, err := f.content.ListVisibleSharedAccounts(ctx, p)
		if err != nil {
			t.Fatalf("ListVisibleSharedAccounts(%s): %v", p.Username, err)
		}
		return len(list)
	}
	if count(proUser) != 2 || count(basicUser) != 1 || count(noPlan) != 0 {
		t.Fatalf("visible = pro %d, basic %d, no plan %d; want 2, 1, 0", count(proUser), count(basicUser), count(noPlan))
	}
	if got, err := f.content.RevealVisibleSharedAccount(ctx, proUser, restricted.ID, nil); err != nil || got != "pro-only-value" {
		t.Fatalf("pro reveal = %q, %v", got, err)
	}
	// 凭 id 也读不到不可见的条目（R8）
	for name, c := range map[string]struct {
		p  *UserPrincipal
		id int64
	}{
		"other plan":  {basicUser, restricted.ID},
		"unpublished": {proUser, unpublished.ID},
		"no plan":     {noPlan, forAll.ID},
		"missing":     {proUser, 9999},
	} {
		if _, err := f.content.RevealVisibleSharedAccount(ctx, c.p, c.id, nil); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s reveal = %v, want not_found", name, err)
		}
	}
	// 修改时不带口令 = 沿用原值
	in.Secret, in.Remark = "", "换了备注"
	if _, err := f.content.UpdateSharedAccount(ctx, f.actor, forAll.ID, in, nil); err != nil {
		t.Fatalf("UpdateSharedAccount: %v", err)
	}
	if got, err := f.content.RevealSharedAccount(ctx, f.actor, forAll.ID, nil); err != nil || got != "s3cret-value" {
		t.Fatalf("admin reveal after update without credential = %q, %v", got, err)
	}
	var reveals int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE action IN ($1, $2)", ActionSharedReveal, ActionPortalSharedReveal).Scan(&reveals); err != nil || reveals != 2 {
		t.Fatalf("reveal audit rows = %d, %v; want 2", reveals, err)
	}
}

func TestTicketConversationAndOwnership(t *testing.T) {
	f := newContentFixture(t)
	ctx := context.Background()
	alice := f.principal(t, "alice", 0)
	bob := f.principal(t, "bob", 0)
	if _, err := f.pool.Exec(ctx, `UPDATE end_user SET email = 'alice@example.com', email_verified_at = now() WHERE id = $1`, alice.UserID); err != nil {
		t.Fatalf("seed email: %v", err)
	}
	if _, err := f.update(t, map[string]any{"ticket.notifyEmail": "ops@example.com"}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	d, err := f.tickets.Create(ctx, alice, " 连不上 ", "香港节点 <b>超时</b>\n第二行")
	if err != nil || d.Status != TicketOpen || d.Subject != "连不上" || len(d.Messages) != 1 || d.Messages[0].Content != "香港节点 <b>超时</b>\n第二行" {
		t.Fatalf("Create = %+v, %v", d, err)
	}
	// 他人的工单一律 404（R8）
	if _, err := f.tickets.GetOwn(ctx, bob, d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob reads alice's ticket = %v, want not_found", err)
	}
	if _, err := f.tickets.ReplyOwn(ctx, bob, d.ID, "插一句"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob replies to alice's ticket = %v, want not_found", err)
	}
	if _, err := f.tickets.CloseOwn(ctx, bob, d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob closes alice's ticket = %v, want not_found", err)
	}
	if page, err := f.tickets.ListOwn(ctx, bob, PageQuery{}); err != nil || page.Total != 0 {
		t.Fatalf("bob's tickets = %+v, %v", page, err)
	}

	f.clock.advance(time.Minute)
	d, err = f.tickets.Reply(ctx, f.actor, d.ID, "已处理，请重试", nil)
	if err != nil || d.Status != TicketAnswered || len(d.Messages) != 2 || d.Messages[1].AuthorKind != TicketAuthorAdmin || d.Messages[1].AuthorName != "root" {
		t.Fatalf("admin Reply = %+v, %v", d, err)
	}
	f.clock.advance(time.Minute)
	if d, err = f.tickets.CloseOwn(ctx, alice, d.ID); err != nil || d.Status != TicketClosed {
		t.Fatalf("CloseOwn = %+v, %v", d, err)
	}
	// 用户回复已关闭的工单即重新打开
	if d, err = f.tickets.ReplyOwn(ctx, alice, d.ID, "还是不行"); err != nil || d.Status != TicketOpen || len(d.Messages) != 3 {
		t.Fatalf("reply to a closed ticket = %+v, %v", d, err)
	}
	if d, err = f.tickets.Close(ctx, f.actor, d.ID, nil); err != nil || d.Status != TicketClosed {
		t.Fatalf("admin Close = %+v, %v", d, err)
	}

	// 通知：新建与用户回复各通知一次管理员邮箱，管理员回复通知用户；邮件里不带消息正文
	f.flush(t)
	var toOps, toUser int
	for _, m := range f.sender.messages() {
		if strings.Contains(m.Text, "超时") || strings.Contains(m.Text, "已处理") {
			t.Fatalf("notification must not carry the message body: %q", m.Text)
		}
		switch m.To {
		case "ops@example.com":
			toOps++
		case "alice@example.com":
			toUser++
		default:
			t.Fatalf("unexpected recipient %s", m.To)
		}
	}
	if toOps != 2 || toUser != 1 {
		t.Fatalf("notifications = ops %d, user %d; want 2 and 1", toOps, toUser)
	}

	page, err := f.tickets.List(ctx, TicketFilter{Status: TicketClosed}, PageQuery{})
	if err != nil || page.Total != 1 || page.Items[0].Username != "alice" {
		t.Fatalf("admin list closed = %+v, %v", page, err)
	}
	if _, err := f.tickets.List(ctx, TicketFilter{Status: "pending"}, PageQuery{}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("bad status = %v", err)
	}
	for name, c := range map[string][2]string{"empty subject": {" ", "x"}, "empty content": {"s", "  "}, "long content": {"s", strings.Repeat("字", maxTicketMessage+1)}} {
		if _, err := f.tickets.Create(ctx, alice, c[0], c[1]); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("%s = %v, want invalid_argument", name, err)
		}
	}
	if _, err := f.update(t, map[string]any{"ticket.enabled": false}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := f.tickets.Create(ctx, alice, "s", "c"); !errors.Is(err, ErrFeatureDisabled) {
		t.Fatalf("tickets off = %v, want feature_disabled", err)
	}
	if _, err := f.tickets.GetOwn(ctx, alice, d.ID); err != nil {
		t.Fatalf("existing tickets stay readable when the feature is off: %v", err)
	}
}

// 删除用户（docs/03 §5.2）：未关闭的工单当场删除；没有面板 client 的用户当场清除，已关闭的工单随之级联删除。
func TestDeleteUserRemovesTicketsAndPurgesUnprovisionedUser(t *testing.T) {
	f := newContentFixture(t)
	ctx := context.Background()
	plan := f.plan(t, "basic", PlanQuota{TrafficLimit: gib, DurationDays: 30})
	alice := f.principal(t, "alice", 0)   // 没有套餐，也就没有面板 client
	bob := f.principal(t, "bob", plan.ID) // 有套餐，对账后在面板上有 client
	f.reconcile(t)

	count := func(sql string, id int64) (n int) {
		t.Helper()
		if err := f.pool.QueryRow(ctx, sql, id).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}
	const ticketsOf, userRows = `SELECT count(*) FROM ticket WHERE user_id = $1`, `SELECT count(*) FROM end_user WHERE id = $1`
	for _, p := range []*UserPrincipal{alice, bob} {
		closed, err := f.tickets.Create(ctx, p, "已解决", "谢谢")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := f.tickets.CloseOwn(ctx, p, closed.ID); err != nil {
			t.Fatalf("CloseOwn: %v", err)
		}
		if _, err := f.tickets.Create(ctx, p, "还没解决", "在吗"); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	if err := f.users.DeleteUser(ctx, f.actor, alice.UserID, nil); err != nil {
		t.Fatalf("DeleteUser alice: %v", err)
	}
	if n := count(userRows, alice.UserID); n != 0 {
		t.Fatalf("user without panel clients must be purged at once, rows = %d", n)
	}
	if n := count(ticketsOf, alice.UserID); n != 0 {
		t.Fatalf("tickets of a purged user = %d, want 0", n)
	}

	// 面板不可达：bob 留在待删除状态，但未关闭的工单已经删掉，只剩已关闭的那张
	f.fleet.get(fakeBase).FailWith(&panel.Error{Kind: panel.KindUnreachable, Message: "超时"})
	if err := f.users.DeleteUser(ctx, f.actor, bob.UserID, nil); err != nil {
		t.Fatalf("DeleteUser bob: %v", err)
	}
	if n := count(userRows, bob.UserID); n != 1 {
		t.Fatalf("user with panel clients must wait for reconcile, rows = %d", n)
	}
	if n := count(ticketsOf, bob.UserID); n != 1 {
		t.Fatalf("tickets of a pending-delete user = %d, want only the closed one", n)
	}
	page, err := f.tickets.List(ctx, TicketFilter{}, PageQuery{Page: 1, PageSize: 20})
	if err != nil || page.Total != 1 {
		t.Fatalf("admin ticket list = %+v, %v", page, err)
	}
	f.fleet.get(fakeBase).FailWith(nil)
	f.reconcile(t)
	if n := count(userRows, bob.UserID); n != 0 {
		t.Fatalf("user must be purged after reconcile, rows = %d", n)
	}
}

func TestTicketOpenLimitHoldsUnderConcurrency(t *testing.T) {
	f := newContentFixture(t)
	alice := f.principal(t, "alice", 0)
	const racers = maxOpenTicketsPerUser + 4
	var wg sync.WaitGroup
	errs := make([]error, racers)
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.tickets.Create(context.Background(), alice, "并发工单", "内容")
		}()
	}
	wg.Wait()
	ok := 0
	for i, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrConflict), errors.Is(err, ErrRateLimited):
		default:
			t.Fatalf("racer %d: unexpected error %v", i, err)
		}
	}
	if ok != maxOpenTicketsPerUser {
		t.Fatalf("%d tickets created concurrently, limit is %d open tickets", ok, maxOpenTicketsPerUser)
	}
	// 上限已满：再建 409；过一小时关掉一张后可以再建
	if _, err := f.tickets.Create(context.Background(), alice, "第六张", "内容"); !errors.Is(err, ErrConflict) {
		t.Fatalf("over the open limit = %v, want conflict", err)
	}
}

func TestCheckinOncePerDayAndRevivesLimitedUser(t *testing.T) {
	f := newContentFixture(t)
	ctx := context.Background()
	plan := f.plan(t, "basic", PlanQuota{TrafficLimit: gib, DurationDays: 30})
	alice := f.principal(t, "alice", plan.ID)
	f.reconcile(t)

	if _, err := f.users.Checkin(ctx, alice); !errors.Is(err, ErrFeatureDisabled) {
		t.Fatalf("checkin off = %v, want feature_disabled", err)
	}
	if info, err := f.users.GetCheckin(ctx, alice); err != nil || info.Enabled || info.CanCheckin {
		t.Fatalf("GetCheckin while off = %+v, %v", info, err)
	}
	if _, err := f.update(t, map[string]any{"checkin.enabled": true, "checkin.minMb": 10, "checkin.maxMb": 20, "site.timezone": "Asia/Shanghai"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// 把用量打满：limited，面板上的 client 被禁用
	if _, err := f.pool.Exec(ctx, "UPDATE end_user SET traffic_used = traffic_limit, status = 'limited' WHERE id = $1", alice.UserID); err != nil {
		t.Fatalf("exhaust quota: %v", err)
	}
	if err := f.users.q.MarkUserProvisionsPending(ctx, alice.UserID); err != nil {
		t.Fatalf("mark pending: %v", err)
	}
	f.reconcile(t)
	if c := f.panelClient(t, f.emailOf(t, alice.UserID)); c.Enabled {
		t.Fatal("client of a limited user should be disabled before checkin")
	}

	info, err := f.users.GetCheckin(ctx, alice)
	if err != nil || !info.Enabled || !info.CanCheckin || info.CheckedToday {
		t.Fatalf("GetCheckin = %+v, %v", info, err)
	}
	res, err := f.users.Checkin(ctx, alice)
	if err != nil {
		t.Fatalf("Checkin: %v", err)
	}
	if res.RewardBytes < 10*bytesPerMB || res.RewardBytes > 20*bytesPerMB || res.RewardBytes%bytesPerMB != 0 || res.TrafficLimit != gib+res.RewardBytes {
		t.Fatalf("reward = %+v, want a whole number of MB within [10, 20] added to the limit", res)
	}
	u, err := f.users.GetUser(ctx, alice.UserID)
	if err != nil || u.Status != UserStatusActive || u.Quota.TrafficLimit != res.TrafficLimit {
		t.Fatalf("user after checkin = %+v, %v; a limited user back under quota becomes active", u, err)
	}
	f.reconcile(t)
	if c := f.panelClient(t, f.emailOf(t, alice.UserID)); !c.Enabled {
		t.Fatal("client should be re-enabled after checkin brought the user back under quota")
	}
	if _, err := f.users.Checkin(ctx, alice); !errors.Is(err, ErrConflict) {
		t.Fatalf("second checkin the same day = %v, want conflict", err)
	}
	if info, err := f.users.GetCheckin(ctx, alice); err != nil || !info.CheckedToday || info.CanCheckin || len(info.Logs) != 1 {
		t.Fatalf("GetCheckin after = %+v, %v", info, err)
	}
	f.clock.advance(24 * time.Hour)
	if _, err := f.users.Checkin(ctx, alice); err != nil {
		t.Fatalf("checkin next day: %v", err)
	}
}

func TestCheckinConditionsAndConcurrency(t *testing.T) {
	f := newContentFixture(t)
	ctx := context.Background()
	if _, err := f.update(t, map[string]any{"checkin.enabled": true}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	limited := f.plan(t, "limited", PlanQuota{TrafficLimit: gib, DurationDays: 30})
	unlimited := f.plan(t, "unlimited", PlanQuota{DurationDays: 30})
	noPlan := f.principal(t, "noplan", 0)
	free := f.principal(t, "free", unlimited.ID)
	expired := f.principal(t, "expired", limited.ID)
	if _, err := f.pool.Exec(ctx, "UPDATE end_user SET expires_at = $2 WHERE id = $1", expired.UserID, f.clock.now().Add(-time.Hour)); err != nil {
		t.Fatalf("expire user: %v", err)
	}
	for name, p := range map[string]*UserPrincipal{"no plan": noPlan, "unlimited plan": free, "expired": expired} {
		if _, err := f.users.Checkin(ctx, p); !errors.Is(err, ErrConflict) {
			t.Fatalf("%s checkin = %v, want conflict", name, err)
		}
		if info, err := f.users.GetCheckin(ctx, p); err != nil || info.CanCheckin || info.Reason == "" {
			t.Fatalf("%s GetCheckin = %+v, %v", name, info, err)
		}
	}

	// 并发签到只成功一次（Ruling-26），额度只加一次
	alice := f.principal(t, "alice", limited.ID)
	const racers = 8
	var wg sync.WaitGroup
	results := make([]*CheckinResult, racers)
	errs := make([]error, racers)
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = f.users.Checkin(context.Background(), alice)
		}()
	}
	wg.Wait()
	var reward int64
	ok := 0
	for i, err := range errs {
		switch {
		case err == nil:
			ok++
			reward = results[i].RewardBytes
		case errors.Is(err, ErrConflict):
		default:
			t.Fatalf("racer %d: unexpected error %v", i, err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d concurrent checkins succeeded, want exactly 1", ok)
	}
	u, err := f.users.GetUser(ctx, alice.UserID)
	if err != nil || u.Quota.TrafficLimit != gib+reward {
		t.Fatalf("limit after concurrent checkins = %d, %v; want exactly one reward (%d) added", u.Quota.TrafficLimit, err, reward)
	}
}
