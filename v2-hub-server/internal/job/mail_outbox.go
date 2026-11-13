package job

import (
	"context"
	"log/slog"
	"time"
)

// MailProcessor 能发送发件箱里到期的邮件（由 service.MailService 实现）。
type MailProcessor interface {
	// ProcessOutbox 发送到期邮件，window 是距上次调用的间隔，用来折算本次可发的封数。
	ProcessOutbox(ctx context.Context, window time.Duration) (int, error)
}

// RunMailOutbox 按 interval 周期发送发件箱，阻塞到 ctx 取消（docs/03 §5.7 发件箱）。
func RunMailOutbox(ctx context.Context, p MailProcessor, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			processMailOnce(ctx, p, interval)
		}
	}
}

func processMailOnce(ctx context.Context, p MailProcessor, window time.Duration) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("mail outbox panicked", "panic", r)
		}
	}()
	if _, err := p.ProcessOutbox(ctx, window); err != nil {
		slog.Error("process mail outbox", "err", err)
	}
}
