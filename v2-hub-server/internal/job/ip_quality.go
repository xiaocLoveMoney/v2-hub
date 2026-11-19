package job

import (
	"context"
	"log/slog"
	"time"
)

// RunIPChecks 领取并执行排队的 IP 质量检测（docs/03 §5.23，Ruling-81），阻塞到 ctx 取消；与主机任务共用同一个循环。
func RunIPChecks(ctx context.Context, r HostTaskRunner, interval time.Duration, concurrency int) {
	runTaskLoop(ctx, "ip checks", r, interval, concurrency)
}

// IPCheckScheduler 能给到期的服务器排定期复检（由 service.IPQualityService 实现）。
type IPCheckScheduler interface {
	QueueDueChecks(ctx context.Context) (int, error)
}

// RunIPCheckSchedule 启动时检查一次，之后按 interval 周期给到期的服务器排定期复检（开关在系统设置），阻塞到 ctx 取消。
// 检测本身由 RunIPChecks 执行。
func RunIPCheckSchedule(ctx context.Context, r IPCheckScheduler, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		guarded("ip check schedule", func() error {
			n, err := r.QueueDueChecks(ctx)
			if n > 0 {
				slog.Info("ip check schedule: queued checks", "count", n)
			}
			return err
		})
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
