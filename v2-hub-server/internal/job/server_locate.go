package job

import (
	"context"
	"log/slog"
	"time"
)

// ServerLocator 能对位置为空的服务器补扫位置（由 service.PanelService 实现，Ruling-65）。
type ServerLocator interface {
	BackfillServerLocations(ctx context.Context) (int, error)
}

// RunServerLocate 启动时补扫一次位置，之后每隔 interval 再扫一次（新建时面板还没取到公网 IP 的服务器靠它补上），阻塞到 ctx 取消。
func RunServerLocate(ctx context.Context, locator ServerLocator, interval time.Duration) {
	locateOnce(ctx, locator)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			locateOnce(ctx, locator)
		}
	}
}

func locateOnce(ctx context.Context, locator ServerLocator) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("server locate panicked", "panic", r)
		}
	}()
	n, err := locator.BackfillServerLocations(ctx)
	if err != nil {
		slog.Error("server locate failed", "err", err)
		return
	}
	if n > 0 {
		slog.Info("server locations filled", "count", n)
	}
}
