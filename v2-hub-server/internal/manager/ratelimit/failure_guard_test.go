package ratelimit

import (
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
