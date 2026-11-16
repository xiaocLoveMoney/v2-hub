package job

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// HostTaskRunner 是主机任务循环需要的能力（由 service.HostService 实现）。
type HostTaskRunner interface {
	FailInterrupted(ctx context.Context) (int64, error)
	ClaimTasks(ctx context.Context, limit int) ([]int64, error)
	RunTask(ctx context.Context, taskID int64)
	Requests() <-chan struct{}
}

// RunHostTasks 领取并执行排队的主机任务（docs/03 §5.22，Ruling-71），阻塞到 ctx 取消。
// 启动时先把上次没跑完的任务记为失败；之后定时与收到信号时各领取一轮，同时执行的任务数不超过 concurrency。
func RunHostTasks(ctx context.Context, r HostTaskRunner, interval time.Duration, concurrency int) {
	if n, err := r.FailInterrupted(ctx); err != nil {
		slog.Error("host tasks: mark interrupted", "err", err)
	} else if n > 0 {
		slog.Warn("host tasks: marked interrupted tasks as failed", "count", n)
	}
	slots := make(chan struct{}, concurrency)
	var running sync.WaitGroup
	defer running.Wait()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		claimHostTasks(ctx, r, slots, &running)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.Requests():
		}
	}
}

// claimHostTasks 按空闲的名额领取任务，每条各起一个 goroutine；名额占满时这一轮什么都不领。
func claimHostTasks(ctx context.Context, r HostTaskRunner, slots chan struct{}, running *sync.WaitGroup) {
	free := cap(slots) - len(slots)
	if free <= 0 || ctx.Err() != nil {
		return
	}
	ids, err := r.ClaimTasks(ctx, free)
	if err != nil {
		slog.Error("host tasks: claim", "err", err)
		return
	}
	for _, id := range ids {
		slots <- struct{}{}
		running.Add(1)
		go func() {
			defer running.Done()
			defer func() { <-slots }()
			defer func() {
				if p := recover(); p != nil {
					slog.Error("host task panicked", "task_id", id, "panic", p)
				}
			}()
			r.RunTask(ctx, id)
		}()
	}
}
