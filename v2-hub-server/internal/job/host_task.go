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
	// freed 在有任务结束、空出名额时发信号，排队的任务不必等到下一个 tick
	freed := make(chan struct{}, 1)
	var running sync.WaitGroup
	defer running.Wait()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		claimHostTasks(ctx, r, slots, freed, &running)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.Requests():
		case <-freed:
		}
	}
}

// CertRenewer 能给证书快到期的主机排续期任务（由 service.HostService 实现）。
type CertRenewer interface {
	QueueCertRenewals(ctx context.Context) (int, error)
}

// RunCertRenewal 启动时检查一次，之后按 interval 周期检查平台申请的面板证书，给快到期的排“配置证书”任务
// （docs/03 §5.22 “证书自动续期”，Ruling-73），阻塞到 ctx 取消。任务本身由 RunHostTasks 执行。
func RunCertRenewal(ctx context.Context, r CertRenewer, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		guarded("cert renewal", func() error {
			n, err := r.QueueCertRenewals(ctx)
			if n > 0 {
				slog.Info("cert renewal: queued tasks", "count", n)
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

// claimHostTasks 按空闲的名额领取任务，每条各起一个 goroutine；名额占满时这一轮什么都不领。
func claimHostTasks(ctx context.Context, r HostTaskRunner, slots, freed chan struct{}, running *sync.WaitGroup) {
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
			defer func() {
				<-slots
				select {
				case freed <- struct{}{}:
				default:
				}
			}()
			defer func() {
				if p := recover(); p != nil {
					slog.Error("host task panicked", "task_id", id, "panic", p)
				}
			}()
			r.RunTask(ctx, id)
		}()
	}
}
