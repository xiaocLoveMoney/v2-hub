// Package ratelimit 提供进程内的失败次数频控（单实例部署够用；多实例时需换成共享存储）。
package ratelimit

import (
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
	e, ok := g.entries[key]
	if !ok {
		if len(g.entries) >= g.cfg.MaxKeys {
			g.evictExpiredLocked(now)
		}
		if len(g.entries) >= g.cfg.MaxKeys {
			// 仍满：放弃跟踪新 key，宁可少锁也不让内存无界增长；已跟踪的 key 不受影响。
			return
		}
		e = &failureEntry{windowStart: now}
		g.entries[key] = e
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

// Reset 清除 key 的失败记录（如登录成功后）。
func (g *FailureGuard) Reset(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.entries, key)
}

func (g *FailureGuard) evictExpiredLocked(now time.Time) {
	for k, e := range g.entries {
		if now.After(e.lockedUntil) && now.Sub(e.windowStart) > g.cfg.Window {
			delete(g.entries, k)
		}
	}
}
