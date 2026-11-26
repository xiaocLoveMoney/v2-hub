// Package ratelimit 提供进程内的失败次数频控（单实例部署够用；多实例时需换成共享存储）。
package ratelimit

import (
	"slices"
	"sync"
	"time"
)

// FailureGuardConfig 是失败频控参数。
type FailureGuardConfig struct {
	// MaxFailures 是窗口内允许的失败次数，达到即锁定。
	MaxFailures int
	// Window 是统计失败次数的时间窗口。
	Window time.Duration
	// Lockout 是达到上限后的锁定时长。
	Lockout time.Duration
	// MaxKeys 是同时跟踪的 key 上限，防止被大量随机用户名 / IP 撑爆内存。
	MaxKeys int
}

type failureEntry struct {
	count       int
	windowStart time.Time
	lockedUntil time.Time
	// pending 是已占用、尚未确认结果的名额数（Ruling-105）；大于 0 的条目不会被淘汰或删除。
	pending int
}

// FailureGuard 按 key（用户名、IP 等）统计失败次数，超限后锁定一段时间。并发安全。
type FailureGuard struct {
	cfg     FailureGuardConfig
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]*failureEntry
}

// NewFailureGuard 创建失败频控器；now 为 nil 时使用 time.Now（测试可注入时钟）。
func NewFailureGuard(cfg FailureGuardConfig, now func() time.Time) *FailureGuard {
	if now == nil {
		now = time.Now
	}
	return &FailureGuard{cfg: cfg, now: now, entries: make(map[string]*failureEntry, cfg.MaxKeys)}
}

// IsLocked 报告 key 当前是否处于锁定期。
func (g *FailureGuard) IsLocked(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.entries[key]
	return ok && g.now().Before(e.lockedUntil)
}

// RecordFailure 记一次失败；达到上限时开始锁定。
func (g *FailureGuard) RecordFailure(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.recordLocked(key, g.now())
}

// Allow 把频控当作“每窗口最多 MaxFailures 次”的限速器用：key 处于锁定期时返回 false 且不计数；
// 否则记一次并返回 true（这一次正好用满上限时，之后的调用在 Lockout 内都被拒）。判断与计数在同一把锁内完成。
func (g *FailureGuard) Allow(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if e, ok := g.entries[key]; ok && now.Before(e.lockedUntil) {
		return false
	}
	g.recordLocked(key, now)
	return true
}

// recordLocked 记 key 的一次失败，调用方须持有 g.mu。
func (g *FailureGuard) recordLocked(key string, now time.Time) {
	e := g.entryLocked(key, now)
	if e == nil {
		return
	}
	if now.Sub(e.windowStart) > g.cfg.Window {
		e.count = 0
		e.windowStart = now
	}
	e.count++
	if e.count >= g.cfg.MaxFailures {
		e.lockedUntil = now.Add(g.cfg.Lockout)
		e.count = 0
		e.windowStart = now
	}
}

// Reset 清除 key 的失败记录与锁定（如登录成功后）；仍有在途占用时保留条目，让那些占用之后照常确认或撤回。
func (g *FailureGuard) Reset(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.entries[key]
	if !ok {
		return
	}
	if e.pending == 0 {
		delete(g.entries, key)
		return
	}
	e.count = 0
	e.windowStart = g.now()
	e.lockedUntil = time.Time{}
}

// Attempt 是在校验口令之前占用的一次失败名额（Ruling-105），由 FailureGuard.Acquire 返回。
// 校验不通过调 Fail 把它确认为一次失败；校验通过或因与口令无关的原因提前结束调 Release 撤回。
// 两者只有第一次调用生效，所以常见写法是占用后立即 defer Release，校验不通过时先调 Fail。
type Attempt struct {
	g *FailureGuard
	// keys 是实际占到名额的 key（跟踪上限已满而没跟踪的 key 不在其中）。
	keys []string
	// settled 由 g.mu 保护。
	settled bool
}

// Acquire 在校验之前原子地为每个非空 key 各占用一次失败名额。任一 key 处于锁定期、或窗口内已确认的失败
// 加上在途占用已达上限（再校验一次就可能超过上限）时，判为锁定：返回 (nil, false) 且不占用任何名额。
// 这样并发提交时，被真正校验的次数也不会超过上限。
func (g *FailureGuard) Acquire(keys ...string) (*Attempt, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	for _, key := range keys {
		if key != "" && g.fullLocked(key, now) {
			return nil, false
		}
	}
	a := &Attempt{g: g, keys: make([]string, 0, len(keys))}
	for _, key := range keys {
		if key == "" || slices.Contains(a.keys, key) {
			continue
		}
		e := g.entryLocked(key, now)
		if e == nil {
			continue
		}
		e.pending++
		a.keys = append(a.keys, key)
	}
	return a, true
}

// Fail 把这次占用确认为一次失败；达到上限即开始锁定。已确认或已撤回时不做任何事。
func (a *Attempt) Fail() {
	a.settle(true)
}

// Release 撤回这次占用，只撤回它自己占的名额。已确认或已撤回时不做任何事。
func (a *Attempt) Release() {
	a.settle(false)
}

func (a *Attempt) settle(failed bool) {
	g := a.g
	g.mu.Lock()
	defer g.mu.Unlock()
	if a.settled {
		return
	}
	a.settled = true
	now := g.now()
	for _, key := range a.keys {
		// pending > 0 的条目不会被删除，这里一定能取到占用时的同一个条目
		e := g.entries[key]
		e.pending--
		if failed {
			g.recordLocked(key, now)
			continue
		}
		// 撤回后什么都没留下的条目直接删掉，不占跟踪名额
		if e.pending == 0 && e.count == 0 && !now.Before(e.lockedUntil) {
			delete(g.entries, key)
		}
	}
}

// fullLocked 报告 key 是否处于锁定期，或已确认的失败加在途占用已达上限；调用方须持有 g.mu。
func (g *FailureGuard) fullLocked(key string, now time.Time) bool {
	e, ok := g.entries[key]
	if !ok {
		return false
	}
	if now.Before(e.lockedUntil) {
		return true
	}
	count := e.count
	if now.Sub(e.windowStart) > g.cfg.Window {
		count = 0
	}
	return count+e.pending >= g.cfg.MaxFailures
}

// entryLocked 取 key 的条目，没有时新建；跟踪上限已满且腾不出位置时返回 nil。调用方须持有 g.mu。
func (g *FailureGuard) entryLocked(key string, now time.Time) *failureEntry {
	if e, ok := g.entries[key]; ok {
		return e
	}
	if len(g.entries) >= g.cfg.MaxKeys {
		g.evictExpiredLocked(now)
	}
	if len(g.entries) >= g.cfg.MaxKeys {
		// 仍满：放弃跟踪新 key，宁可少锁也不让内存无界增长；已跟踪的 key 不受影响。
		return nil
	}
	e := &failureEntry{windowStart: now}
	g.entries[key] = e
	return e
}

func (g *FailureGuard) evictExpiredLocked(now time.Time) {
	for k, e := range g.entries {
		if e.pending == 0 && now.After(e.lockedUntil) && now.Sub(e.windowStart) > g.cfg.Window {
			delete(g.entries, k)
		}
	}
}
