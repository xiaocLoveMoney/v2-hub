package job

import (
	"context"
	"log/slog"
	"time"
)

// TelegramProcessor 能发送 Telegram 发件箱里到期的消息（由 service.TelegramService 实现）。
type TelegramProcessor interface {
	ProcessOutbox(ctx context.Context) (int, error)
}

// RunTelegramOutbox 按 interval 周期发送 Telegram 发件箱，阻塞到 ctx 取消（docs/03 §5.20）。
func RunTelegramOutbox(ctx context.Context, p TelegramProcessor, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			guarded("telegram outbox", func() error {
				_, err := p.ProcessOutbox(ctx)
				return err
			})
		}
	}
}

// OpsChecker 是掉线告警与日报需要的能力（由 service.OpsService 实现）。
type OpsChecker interface {
	CheckAlerts(ctx context.Context) error
	DailyReport(ctx context.Context) (bool, error)
}

// RunOps 按 interval 周期检查掉线告警与恢复、到点发运营日报，阻塞到 ctx 取消（docs/03 §5.20）。
func RunOps(ctx context.Context, c OpsChecker, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			guarded("server alerts", func() error { return c.CheckAlerts(ctx) })
			guarded("daily report", func() error {
				_, err := c.DailyReport(ctx)
				return err
			})
		}
	}
}

// guarded 跑一次任务：panic 兜底、错误记日志，不让一次失败结束整个循环。
func guarded(name string, fn func() error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("ops job panicked", "job", name, "panic", r)
		}
	}()
	if err := fn(); err != nil {
		slog.Error("ops job failed", "job", name, "err", err)
	}
}
