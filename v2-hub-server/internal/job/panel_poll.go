package job

import (
	"context"
	"log/slog"
	"net/netip"
	"time"

	"v2-hub-server/internal/service"
)

// PanelPoller 是定时探测与同步需要的能力（由 service.PanelService 实现）。
type PanelPoller interface {
	ListEnabledServerIDs(ctx context.Context) ([]int64, error)
	ProbeServer(ctx context.Context, id int64) error
	SyncServer(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) (*service.SyncResult, error)
}

// PanelPollConfig 是轮询参数（docs/03 §5.1）。
type PanelPollConfig struct {
	ProbeInterval time.Duration
	SyncInterval  time.Duration
	// Concurrency 是同时处理的服务器数上限，防止服务器多时瞬时打开过多连接。
	Concurrency int
}

// RunPanelPolling 周期性探测（T1–T3）与同步全部启用的服务器，阻塞到 ctx 取消。
// 每一轮等所有服务器处理完才进入下一轮，慢面板不会造成任务堆积。
func RunPanelPolling(ctx context.Context, poller PanelPoller, cfg PanelPollConfig) {
	probeTicker := time.NewTicker(cfg.ProbeInterval)
	syncTicker := time.NewTicker(cfg.SyncInterval)
	defer probeTicker.Stop()
	defer syncTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-probeTicker.C:
			forEachServer(ctx, poller, cfg.Concurrency, "probe", func(id int64) error {
				return poller.ProbeServer(ctx, id)
			})
		case <-syncTicker.C:
			forEachServer(ctx, poller, cfg.Concurrency, "sync", func(id int64) error {
				_, err := poller.SyncServer(ctx, nil, id, nil)
				return err
			})
		}
	}
}

func forEachServer(ctx context.Context, poller PanelPoller, concurrency int, kind string, fn func(id int64) error) {
	ids, err := poller.ListEnabledServerIDs(ctx)
	if err != nil {
		slog.Error("panel poll: list servers", "kind", kind, "err", err)
		return
	}
	// 探测失败已按转移表落库，这里只记调试日志，避免离线服务器刷屏
	logs := fanOutLog{kind: kind, panicMsg: "panel poll panicked", failMsg: "panel poll failed", failLevel: slog.LevelDebug}
	fanOut(ctx, ids, concurrency, logs, fn)
}
