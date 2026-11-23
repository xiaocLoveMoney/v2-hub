package job

import (
	"context"
	"log/slog"
	"time"
)

// ChainApplyRunner 是服务端链式下发需要的能力（由 service.ChainApplier 实现）。
type ChainApplyRunner interface {
	ListServerIDs(ctx context.Context) ([]int64, error)
	ApplyServer(ctx context.Context, serverID int64) error
	Requests() <-chan struct{}
}

// RunChainApply 定时（以及规则变化触发时）逐台检查服务端链式的下发（docs/03 §5.19），阻塞到 ctx 取消。
// 逐台串行：一次下发可能要等十几秒确认内核运行，串行避免同时改动多台面板。
func RunChainApply(ctx context.Context, r ChainApplyRunner, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			applyAll(ctx, r)
		case <-r.Requests():
			applyAll(ctx, r)
		}
	}
}

func applyAll(ctx context.Context, r ChainApplyRunner) {
	ids, err := r.ListServerIDs(ctx)
	if err != nil {
		slog.Error("chain apply: list servers", "err", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		applyOne(ctx, r, id)
	}
}

func applyOne(ctx context.Context, r ChainApplyRunner, id int64) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("chain apply panicked", "server_id", id, "panic", p)
		}
	}()
	if err := r.ApplyServer(ctx, id); err != nil {
		slog.Warn("chain apply failed", "server_id", id, "err", err)
	}
}
