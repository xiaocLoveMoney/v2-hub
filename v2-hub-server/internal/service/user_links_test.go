package service

import (
	"context"
	"slices"
	"testing"
	"time"
)

// countCalls 数 calls 里某个方法被调用的次数。
func countCalls(calls []string, method string) int {
	n := 0
	for _, c := range calls {
		if c == method {
			n++
		}
	}
	return n
}

// 取链接的摘要按用户存库（Ruling-92）：hub-server 重启后不再全量重取；摘要对不上（如升级前的旧记录）时只重取一次。
func TestLinksStampSurvivesRestart(t *testing.T) {
	f := newUserFixture(t)
	ctx := context.Background()
	fake := f.fleet.get(fakeBase)
	plan := f.plan(t, "basic", PlanQuota{TrafficLimit: gib, DurationDays: 30})
	f.newUser(t, "alice.example", plan.ID)
	f.newUser(t, "bob.example", plan.ID)
	f.reconcile(t)
	if n := countCalls(fake.Calls, "ClientLinks"); n != 2 {
		t.Fatalf("首次对账应给两个用户各取一次链接，实际 %d 次", n)
	}

	// 模拟进程重启：新的服务实例，内存里什么都没有
	f.users = NewUserService(f.pool, testSealer(t), f.panels, UserConfig{Location: time.UTC}, f.clock.now)
	before := len(fake.Calls)
	f.reconcile(t)
	if slices.Contains(fake.Calls[before:], "ClientLinks") {
		t.Fatalf("重启后对账不应重取链接；calls = %v", fake.Calls[before:])
	}

	// 升级前的记录摘要为空串：重取一次，之后不再取
	if _, err := f.pool.Exec(ctx, "UPDATE provision SET links_stamp = ''"); err != nil {
		t.Fatalf("clear links_stamp: %v", err)
	}
	before = len(fake.Calls)
	f.reconcile(t)
	if n := countCalls(fake.Calls[before:], "ClientLinks"); n != 2 {
		t.Fatalf("摘要对不上时应各重取一次，实际 %d 次", n)
	}
	before = len(fake.Calls)
	f.reconcile(t)
	if slices.Contains(fake.Calls[before:], "ClientLinks") {
		t.Fatalf("重取后摘要已存，再对账不应取链接；calls = %v", fake.Calls[before:])
	}
}
