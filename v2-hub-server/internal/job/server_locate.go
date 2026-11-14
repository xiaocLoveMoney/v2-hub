package job

import (
	"context"
	"log/slog"
)

// ServerLocator 能对位置为空的服务器补扫位置（由 service.PanelService 实现，Ruling-65）。
type ServerLocator interface {
	BackfillServerLocations(ctx context.Context) (int, error)
}

// RunServerLocate 启动时执行一次位置补扫；单次任务，自带 recover，随 ctx 取消提前结束。
func RunServerLocate(ctx context.Context, locator ServerLocator) {
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
