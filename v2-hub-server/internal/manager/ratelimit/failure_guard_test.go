package ratelimit

import (
	"sync"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newGuard(clock *fakeClock, maxKeys int) *FailureGuard {
	return NewFailureGuard(FailureGuardConfig{
		MaxFailures: 3,
		Window:      10 * time.Minute,
		Lockout:     15 * time.Minute,
		MaxKeys:     maxKeys,
	}, clock.now)
}

func TestFailureGuardLocksAfterMaxFailures(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	g := newGuard(clock, 10)
	for i := 0; i < 2; i++ {
		g.RecordFailure("alice")
	}
	if g.IsLocked("alice") {
		t.Fatal("locked too early")
	}
	g.RecordFailure("alice")
	if !g.IsLocked("alice") {
		t.Fatal("want locked after 3 failures")
	}
	if g.IsLocked("bob") {
		t.Fatal("other keys must not be locked")
	}
	clock.advance(15*time.Minute + time.Second)
	if g.IsLocked("alice") {
		t.Fatal("lock should expire")
	}
}

func TestFailureGuardWindowResets(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	g := newGuard(clock, 10)
	g.RecordFailure("alice")
	g.RecordFailure("alice")
	clock.advance(11 * time.Minute)
	g.RecordFailure("alice")
	if g.IsLocked("alice") {
		t.Fatal("failures outside the window must not accumulate")
	}
}

func TestFailureGuardReset(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	g := newGuard(clock, 10)
	g.RecordFailure("alice")
	g.RecordFailure("alice")
	g.Reset("alice")
	g.RecordFailure("alice")
	if g.IsLocked("alice") {
		t.Fatal("Reset must clear counted failures")
	}
}

func TestFailureGuardBoundedKeys(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	g := newGuard(clock, 2)
	g.RecordFailure("a")
	g.RecordFailure("b")
	g.RecordFailure("c")
	if len(g.entries) != 2 {
		t.Fatalf("entries = %d, want capped at 2", len(g.entries))
	}
	clock.advance(time.Hour)
	g.RecordFailure("c")
	if _, ok := g.entries["c"]; !ok {
		t.Fatal("expired entries should be evicted to make room")
	}
}

func TestFailureGuardAllow(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	g := newGuard(clock, 10)
	for i := 0; i < 3; i++ {
		if !g.Allow("alice") {
			t.Fatalf("call %d rejected, want allowed up to MaxFailures", i+1)
		}
	}
	if g.Allow("alice") {
		t.Fatal("want rejected after MaxFailures calls")
	}
	if !g.Allow("bob") {
		t.Fatal("other keys must not be limited")
	}
	// 被拒的调用不计数：锁定期一过就重新放行
	clock.advance(15*time.Minute + time.Second)
	if !g.Allow("alice") {
		t.Fatal("want allowed after lockout")
	}
}

// 并发占用：同时发起 N 次，放行数恰为上限，其余直接判为锁定（Ruling-105）。
func TestFailureGuardAcquireConcurrentCapped(t *testing.T) {
	const attempts = 200
	clock := &fakeClock{t: time.Unix(0, 0)}
	g := newGuard(clock, 10)
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		granted []*Attempt
	)
	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if a, ok := g.Acquire("u:alice", "ip:1.2.3.4"); ok {
				mu.Lock()
				granted = append(granted, a)
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if len(granted) != 3 {
		t.Fatalf("granted = %d, want exactly MaxFailures (3)", len(granted))
	}
	for _, a := range granted {
		a.Fail()
	}
	if !g.IsLocked("u:alice") || !g.IsLocked("ip:1.2.3.4") {
		t.Fatal("want both keys locked after the granted attempts all failed")
	}
}

// 顺序执行时与“先查锁定、失败后记一次”的行为一致：第 MaxFailures 次失败才锁定，期间的成功撤回不计数。
func TestFailureGuardAcquireSequential(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	g := newGuard(clock, 10)
	for i := 0; i < 2; i++ {
		a, ok := g.Acquire("alice")
		if !ok {
			t.Fatalf("attempt %d rejected too early", i+1)
		}
		a.Fail()
	}
	// 第 3 次正确：撤回，不锁定
	a, ok := g.Acquire("alice")
	if !ok {
		t.Fatal("3rd attempt rejected, want allowed")
	}
	a.Release()
	if g.IsLocked("alice") {
		t.Fatal("released attempt must not count")
	}
	a, ok = g.Acquire("alice")
	if !ok {
		t.Fatal("want allowed")
	}
	a.Fail()
	if !g.IsLocked("alice") {
		t.Fatal("want locked after 3 confirmed failures")
	}
	if _, ok := g.Acquire("alice"); ok {
		t.Fatal("want rejected while locked")
	}
	clock.advance(15*time.Minute + time.Second)
	if _, ok := g.Acquire("alice"); !ok {
		t.Fatal("want allowed after lockout")
	}
}

// 在途占用把名额占满时直接判为锁定；撤回后名额回来。Fail / Release 只有第一次生效，不会多撤。
func TestFailureGuardAcquireReleaseOnlyOwn(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	g := newGuard(clock, 10)
	first, _ := g.Acquire("alice")
	second, _ := g.Acquire("alice")
	third, _ := g.Acquire("alice")
	if _, ok := g.Acquire("alice"); ok {
		t.Fatal("want rejected while all slots are in flight")
	}
	first.Release()
	first.Release()
	first.Fail()
	fourth, ok := g.Acquire("alice")
	if !ok {
		t.Fatal("want one slot back after a release")
	}
	if _, ok := g.Acquire("alice"); ok {
		t.Fatal("a repeated Release must not free another slot")
	}
	second.Fail()
	third.Fail()
	fourth.Fail()
	if !g.IsLocked("alice") {
		t.Fatal("want locked")
	}
}

// 多个 key 一起占用：任一 key 已满则整体不占用，另一个 key 的名额不被泄漏。
func TestFailureGuardAcquireAllOrNothing(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	g := newGuard(clock, 10)
	for i := 0; i < 3; i++ {
		g.RecordFailure("ip:1")
	}
	if _, ok := g.Acquire("u:alice", "ip:1"); ok {
		t.Fatal("want rejected when one key is locked")
	}
	if _, ok := g.entries["u:alice"]; ok {
		t.Fatal("rejected acquire must not reserve the other key")
	}
	// 空 key 跳过
	a, ok := g.Acquire("u:bob", "")
	if !ok {
		t.Fatal("empty key must be ignored")
	}
	a.Release()
	if len(g.entries) != 1 {
		t.Fatalf("entries = %d, released empty entries should be dropped", len(g.entries))
	}
}

// 成功清零时其它在途占用仍可正常确认，不会因条目被删而错位。
func TestFailureGuardResetKeepsPending(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	g := newGuard(clock, 10)
	winner, _ := g.Acquire("alice")
	loser, _ := g.Acquire("alice")
	winner.Release()
	g.Reset("alice")
	loser.Fail()
	if g.entries["alice"].count != 1 || g.entries["alice"].pending != 0 {
		t.Fatalf("entry = %+v, want one failure and nothing pending", *g.entries["alice"])
	}
}
