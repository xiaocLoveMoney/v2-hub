// Package job 放 hub-server 的后台定时任务。每个任务随 ctx 取消退出，单次执行自行 recover。
package job

import (
	"context"
	"log/slog"
	"time"
)

// SessionPurger 能删除过期会话（由 service.AuthService 与 service.PortalAuthService 实现）。
type SessionPurger interface {
	PurgeExpiredSessions(ctx context.Context) (int64, error)
}

// RunSessionCleanup 按 interval 周期清理过期会话，阻塞到 ctx 取消。
func RunSessionCleanup(ctx context.Context, purger SessionPurger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			purgeOnce(ctx, purger)
		}
	}
}

func purgeOnce(ctx context.Context, purger SessionPurger) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("session cleanup panicked", "panic", r)
		}
	}()
	n, err := purger.PurgeExpiredSessions(ctx)
	if err != nil {
		slog.Error("session cleanup failed", "err", err)
		return
	}
	if n > 0 {
		slog.Info("expired sessions purged", "count", n)
	}
}
