package service

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

const topupAmount = 1000 // 分

type walletFixture struct {
	*accountFixture
	wallet *WalletService
}

func newWalletFixture(t *testing.T) *walletFixture {
	t.Helper()
	af := newAccountFixture(t)
	f := &walletFixture{accountFixture: af, wallet: NewWalletService(af.pool, testSealer(t), af.settings, af.clock.now)}
	if _, err := f.update(t, map[string]any{"shop.enabled": true}); err != nil {
		t.Fatalf("enable shop: %v", err)
	}
	return f
}

func (f *walletFixture) principal(t *testing.T, username string) *UserPrincipal {
	t.Helper()
	u := f.newUser(t, username, 0)
	return &UserPrincipal{UserID: u.ID, Username: u.Username, Status: u.Status}
}

func (f *walletFixture) balanceOf(t *testing.T, userID int64) int64 {
	t.Helper()
	var balance int64
	if err := f.pool.QueryRow(context.Background(), `SELECT balance FROM end_user WHERE id = $1`, userID).Scan(&balance); err != nil {
		t.Fatalf("read balance: %v", err)
	}
	return balance
}

func (f *walletFixture) codes(t *testing.T, count int, expiresAt *time.Time) []string {
	t.Helper()
	codes, err := f.wallet.CreateTopupCodes(context.Background(), f.actor, TopupBatchInput{Amount: topupAmount, Count: count, ExpiresAt: expiresAt, Batch: "it"}, nil)
	if err != nil || len(codes) != count {
		t.Fatalf("CreateTopupCodes = %d codes, %v", len(codes), err)
	}
	return codes
}

// 充值码全程：库里不存明文；兑换入账并记流水；一码一次；停用、过期、已用的提示不区分；已用的不能改删（docs/03 §5.15）。
func TestTopupCodeLifecycle(t *testing.T) {
	f := newWalletFixture(t)
	ctx := context.Background()
	alice := f.principal(t, "alice")
	codes := f.codes(t, 3, nil)

	// 库里只有 HMAC 与密文
	var hash, enc []byte
	var tail string
	if err := f.pool.QueryRow(ctx, `SELECT code_hash, code_enc, code_tail FROM topup_code ORDER BY id LIMIT 1`).Scan(&hash, &enc, &tail); err != nil {
		t.Fatalf("read topup_code: %v", err)
	}
	if bytes.Contains(enc, []byte(codes[0])) || bytes.Contains(hash, []byte(codes[0])) || tail != codes[0][topupCodeLen-topupCodeTailLen:] {
		t.Fatal("topup code must be stored as HMAC + ciphertext only, with the last 4 chars as tail")
	}
	page, err := f.wallet.ListTopupCodes(ctx, TopupFilter{Status: TopupUnused, Batch: "it"}, PageQuery{})
	if err != nil || page.Total != 3 || page.Items[0].Status != TopupUnused {
		t.Fatalf("ListTopupCodes = %+v, %v", page, err)
	}

	// 用户输入可以带分组空格、连字符与小写
	typed := strings.ToLower(codes[0][:4] + " " + codes[0][4:8] + "-" + codes[0][8:])
	res, err := f.wallet.Redeem(ctx, alice, typed, nil)
	if err != nil || res.Amount != topupAmount || res.Balance != topupAmount || f.balanceOf(t, alice.UserID) != topupAmount {
		t.Fatalf("Redeem = %+v, %v", res, err)
	}
	if _, err := f.wallet.Redeem(ctx, alice, codes[0], nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("redeeming a used code = %v, want invalid_argument", err)
	}
	ledger, err := f.wallet.ListOwnLedger(ctx, alice, PageQuery{})
	if err != nil || ledger.Total != 1 || ledger.Items[0].Kind != LedgerTopupCode || ledger.Items[0].Amount != topupAmount || ledger.Items[0].BalanceAfter != topupAmount {
		t.Fatalf("ledger = %+v, %v", ledger, err)
	}

	used, unused := page.Items[2], page.Items[1] // 列表按 id 倒序，codes[0] 是最早的那条
	if _, err := f.wallet.SetTopupCodeDisabled(ctx, f.actor, used.ID, true, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("disabling a used code = %v, want conflict", err)
	}
	if err := f.wallet.DeleteTopupCode(ctx, f.actor, used.ID, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleting a used code = %v, want conflict", err)
	}
	plain, err := f.wallet.RevealTopupCode(ctx, f.actor, unused.ID, nil)
	if err != nil || plain != codes[1] {
		t.Fatalf("RevealTopupCode = %v, want the generated code back", err)
	}
	if c, err := f.wallet.SetTopupCodeDisabled(ctx, f.actor, unused.ID, true, nil); err != nil || c.Status != TopupDisabled {
		t.Fatalf("SetTopupCodeDisabled = %+v, %v", c, err)
	}
	if _, err := f.wallet.Redeem(ctx, alice, codes[1], nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("redeeming a disabled code = %v, want invalid_argument", err)
	}
	if err := f.wallet.DeleteTopupCode(ctx, f.actor, unused.ID, nil); err != nil {
		t.Fatalf("DeleteTopupCode: %v", err)
	}

	// 过期的码不能兑换
	soon := f.clock.now().Add(time.Hour)
	expiring := f.codes(t, 1, &soon)
	f.clock.advance(2 * time.Hour)
	if _, err := f.wallet.Redeem(ctx, alice, expiring[0], nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("redeeming an expired code = %v, want invalid_argument", err)
	}
	if got := f.balanceOf(t, alice.UserID); got != topupAmount {
		t.Fatalf("balance after failed redeems = %d, want %d", got, topupAmount)
	}

	// 商店关闭时兑换入口整体关闭
	if _, err := f.update(t, map[string]any{"shop.enabled": false}); err != nil {
		t.Fatalf("disable shop: %v", err)
	}
	if _, err := f.wallet.Redeem(ctx, alice, codes[2], nil); !errors.Is(err, ErrFeatureDisabled) {
		t.Fatalf("redeem with shop disabled = %v, want feature_disabled", err)
	}
}

// 并发兑换同一个码只入账一次（行锁，docs/03 §5.15 兑换）。
func TestRedeemSameCodeConcurrentlyCreditsOnce(t *testing.T) {
	f := newWalletFixture(t)
	ctx := context.Background()
	const racers = 8
	users := make([]*UserPrincipal, racers)
	for i := range users {
		users[i] = f.principal(t, "racer"+string(rune('a'+i)))
	}
	code := f.codes(t, 1, nil)[0]

	var wg sync.WaitGroup
	results := make([]error, racers)
	for i := range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = f.wallet.Redeem(ctx, users[i], code, nil)
		}()
	}
	wg.Wait()

	okCount := 0
	var total int64
	for i, err := range results {
		if err == nil {
			okCount++
		} else if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("racer %d: unexpected error %v", i, err)
		}
		total += f.balanceOf(t, users[i].UserID)
	}
	if okCount != 1 || total != topupAmount {
		t.Fatalf("successes = %d, credited total = %d; want exactly one success and %d", okCount, total, topupAmount)
	}
}

// 兑换失败满 10 次后一小时内拒绝，哪怕换成有效的码（防猜，docs/03 §5.15）。
func TestRedeemLocksAfterRepeatedFailures(t *testing.T) {
	f := newWalletFixture(t)
	ctx := context.Background()
	alice := f.principal(t, "alice")
	valid := f.codes(t, 1, nil)[0]
	for i := range redeemFailsPerUser {
		if _, err := f.wallet.Redeem(ctx, alice, "2222333344445555", nil); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("attempt %d = %v, want invalid_argument", i, err)
		}
	}
	if _, err := f.wallet.Redeem(ctx, alice, valid, nil); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("redeem after %d failures = %v, want rate_limited", redeemFailsPerUser, err)
	}
	f.clock.advance(redeemGuardWindow + time.Minute)
	if _, err := f.wallet.Redeem(ctx, alice, valid, nil); err != nil {
		t.Fatalf("redeem after lockout expired: %v", err)
	}
}

// 管理员调整余额：须写备注、不能为 0、结果不得为负或超上限；每次一条带操作人的流水（docs/03 §5.14）。
func TestAdjustBalanceKeepsLedgerAndBounds(t *testing.T) {
	f := newWalletFixture(t)
	ctx := context.Background()
	alice := f.principal(t, "alice")

	bad := []struct {
		amount int64
		remark string
	}{{0, "补偿"}, {500, "  "}, {maxBalance + 1, "太多"}}
	for i, c := range bad {
		if err := f.wallet.AdjustBalance(ctx, f.actor, alice.UserID, c.amount, c.remark, nil); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("bad adjust %d = %v, want invalid_argument", i, err)
		}
	}
	if err := f.wallet.AdjustBalance(ctx, f.actor, alice.UserID, 500, "活动补偿", nil); err != nil {
		t.Fatalf("AdjustBalance: %v", err)
	}
	if err := f.wallet.AdjustBalance(ctx, f.actor, alice.UserID, -600, "扣回", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("adjust below zero = %v, want conflict", err)
	}
	if err := f.wallet.AdjustBalance(ctx, f.actor, alice.UserID, maxBalance, "超上限", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("adjust above max = %v, want conflict", err)
	}
	if err := f.wallet.AdjustBalance(ctx, f.actor, 999999, 100, "不存在的用户", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("adjust unknown user = %v, want not_found", err)
	}
	if got := f.balanceOf(t, alice.UserID); got != 500 {
		t.Fatalf("balance = %d, want 500", got)
	}
	page, err := f.wallet.ListLedger(ctx, LedgerFilter{UserID: alice.UserID, Kind: LedgerAdminAdjust}, PageQuery{})
	if err != nil || page.Total != 1 || page.Items[0].Amount != 500 || page.Items[0].BalanceAfter != 500 ||
		page.Items[0].AdminUsername != f.actor.Username || page.Items[0].Remark != "活动补偿" {
		t.Fatalf("ledger = %+v, %v", page, err)
	}
	if _, err := f.wallet.ListLedger(ctx, LedgerFilter{Kind: "gift"}, PageQuery{}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unknown ledger kind = %v, want invalid_argument", err)
	}
}

// 删除用户：剩余余额作废并留一条流水；流水保留，user_id 置空、用户名留快照（Ruling-32）。
func TestDeleteUserForfeitsBalanceAndKeepsLedger(t *testing.T) {
	f := newWalletFixture(t)
	ctx := context.Background()
	alice := f.principal(t, "alice")
	if err := f.wallet.AdjustBalance(ctx, f.actor, alice.UserID, 700, "预存", nil); err != nil {
		t.Fatalf("AdjustBalance: %v", err)
	}
	if err := f.users.DeleteUser(ctx, f.actor, alice.UserID, nil); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	rows, err := f.pool.Query(ctx, `SELECT kind, amount, balance_after, username, user_id IS NULL FROM wallet_ledger ORDER BY id`)
	if err != nil {
		t.Fatalf("query ledger: %v", err)
	}
	defer rows.Close()
	type entry struct {
		kind     string
		amount   int64
		after    int64
		username string
		orphaned bool
	}
	var got []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.kind, &e.amount, &e.after, &e.username, &e.orphaned); err != nil {
			t.Fatalf("scan ledger: %v", err)
		}
		got = append(got, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate ledger: %v", err)
	}
	want := []entry{{LedgerAdminAdjust, 700, 700, "alice", true}, {LedgerForfeit, -700, 0, "alice", true}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ledger after deletion = %+v, want %+v", got, want)
	}
}

// 套餐的时长选项：整体替换时保住留下来的 id（自动续费与订单快照引用它）；nil 表示不改；重复与超量被拒（docs/03 §5.12）。
func TestPlanPricesReplaceKeepsIDs(t *testing.T) {
	f := newUserFixture(t)
	ctx := context.Background()
	base := PlanInput{Name: "标准版", PlanQuota: PlanQuota{TrafficLimit: gib, DurationDays: 30, TrafficReset: TrafficResetMonthly, TrafficResetDay: 1},
		NodeGroupIDs: []int64{f.group.ID}, OnSale: true, SortOrder: 2, Description: "100GB / 月\n3 台设备"}

	in := base
	in.Prices = []PlanPriceInput{{DurationDays: 90, Price: 2700}, {DurationDays: 30, Price: 1000}}
	p, err := f.users.CreatePlan(ctx, f.actor, in, nil)
	if err != nil || len(p.SavedPrices) != 2 || p.SavedPrices[0].DurationDays != 30 || p.SavedPrices[1].Price != 2700 || !p.OnSale || p.Description != base.Description {
		t.Fatalf("CreatePlan = %+v, %v", p, err)
	}
	monthlyID := p.SavedPrices[0].ID

	in.Prices = []PlanPriceInput{{DurationDays: 30, Price: 1200}, {DurationDays: 365, Price: 9000}}
	p, err = f.users.UpdatePlan(ctx, f.actor, p.ID, in, nil)
	if err != nil || len(p.SavedPrices) != 2 || p.SavedPrices[0].ID != monthlyID || p.SavedPrices[0].Price != 1200 || p.SavedPrices[1].DurationDays != 365 {
		t.Fatalf("UpdatePlan with new prices = %+v, %v", p, err)
	}

	in.Prices = nil // 不带 prices：时长选项保持不变
	in.Description = ""
	p, err = f.users.UpdatePlan(ctx, f.actor, p.ID, in, nil)
	if err != nil || len(p.SavedPrices) != 2 || p.Description != "" {
		t.Fatalf("UpdatePlan without prices = %+v, %v", p, err)
	}

	bad := [][]PlanPriceInput{
		{{DurationDays: 30, Price: 1}, {DurationDays: 30, Price: 2}},
		{{DurationDays: 0, Price: 1}},
		{{DurationDays: 30, Price: -1}},
		{{DurationDays: 30, Price: maxMoney + 1}},
		{{DurationDays: 1}, {DurationDays: 2}, {DurationDays: 3}, {DurationDays: 4}, {DurationDays: 5}, {DurationDays: 6}, {DurationDays: 7}},
	}
	for i, prices := range bad {
		in.Prices = prices
		if _, err := f.users.UpdatePlan(ctx, f.actor, p.ID, in, nil); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("bad prices %d = %v, want invalid_argument", i, err)
		}
	}
}
