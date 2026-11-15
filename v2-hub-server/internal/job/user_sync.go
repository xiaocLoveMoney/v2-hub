package job

import (
	"context"
	"log/slog"
	"time"
)

// UserSyncer 是用量采集与下发对账需要的能力（由 service.UserService 实现）。
type UserSyncer interface {
	ListEnabledServerIDs(ctx context.Context) ([]int64, error)
	CollectServerTraffic(ctx context.Context, serverID int64) error
	AdvanceUsers(ctx context.Context) (int, error)
	ReconcileServer(ctx context.Context, serverID int64) error
	ReconcileRequests() <-chan struct{}
}

// UserSyncConfig 是用量与对账的节奏（docs/03 §5.2）。
type UserSyncConfig struct {
	TrafficInterval   time.Duration
	ReconcileInterval time.Duration
	// Concurrency 是同时处理的服务器数上限。
	Concurrency int
}

// RunUserSync 周期性采集用量并推进配额（续期、重置、超额、到期），并在变更触发或定时兜底时对账，阻塞到 ctx 取消。
// 每一轮等所有服务器处理完才进入下一轮，慢面板不会造成任务堆积。
func RunUserSync(ctx context.Context, s UserSyncer, cfg UserSyncConfig) {
	trafficTicker := time.NewTicker(cfg.TrafficInterval)
	reconcileTicker := time.NewTicker(cfg.ReconcileInterval)
	defer trafficTicker.Stop()
	defer reconcileTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-trafficTicker.C:
			eachServer(ctx, s, cfg.Concurrency, "traffic", s.CollectServerTraffic)
			advanceUsers(ctx, s)
		case <-reconcileTicker.C:
			eachServer(ctx, s, cfg.Concurrency, "reconcile", s.ReconcileServer)
		case <-s.ReconcileRequests():
			eachServer(ctx, s, cfg.Concurrency, "reconcile", s.ReconcileServer)
		}
	}
}

func advanceUsers(ctx context.Context, s UserSyncer) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("advance users panicked", "panic", r)
		}
	}()
	if _, err := s.AdvanceUsers(ctx); err != nil {
		slog.Error("advance users", "err", err)
	}
}

func eachServer(ctx context.Context, s UserSyncer, concurrency int, kind string, fn func(ctx context.Context, id int64) error) {
	ids, err := s.ListEnabledServerIDs(ctx)
	if err != nil {
		slog.Error("user sync: list servers", "kind", kind, "err", err)
		return
	}
	logs := fanOutLog{kind: kind, panicMsg: "user sync panicked", failMsg: "user sync failed", failLevel: slog.LevelWarn}
	fanOut(ctx, ids, concurrency, logs, func(id int64) error { return fn(ctx, id) })
}
