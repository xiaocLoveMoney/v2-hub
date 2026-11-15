package job

import (
	"context"
	"log/slog"
	"sync"
)

// fanOutLog 是 fanOut 的日志口径：各任务保留自己原有的文案与失败级别。
type fanOutLog struct {
	kind      string
	panicMsg  string
	failMsg   string
	failLevel slog.Level
}

// fanOut 以至多 concurrency 个 goroutine 并发对每个 id 执行 fn，等全部结束才返回；
// ctx 取消后不再派发新任务，只等已派发的完成。单个任务的 panic 被兜住并记日志，失败按 logs.failLevel 记日志。
func fanOut(ctx context.Context, ids []int64, concurrency int, logs fanOutLog, fn func(id int64) error) {
	sem := make(chan struct{}, max(concurrency, 1))
	var wg sync.WaitGroup
	for _, id := range ids {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					slog.Error(logs.panicMsg, "kind", logs.kind, "server_id", id, "panic", r)
				}
			}()
			if err := fn(id); err != nil {
				// 与 slog.Debug / slog.Warn 一致，用 context.Background() 记日志
				slog.Log(context.Background(), logs.failLevel, logs.failMsg, "kind", logs.kind, "server_id", id, "err", err)
			}
		}(id)
	}
	wg.Wait()
}
