package job

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type countingSyncer struct {
	collected  atomic.Int32
	advanced   atomic.Int32
	reconciled atomic.Int32
	requests   chan struct{}
}

func (s *countingSyncer) ListEnabledServerIDs(context.Context) ([]int64, error) {
	return []int64{1, 2}, nil
}

func (s *countingSyncer) CollectServerTraffic(_ context.Context, id int64) error {
	s.collected.Add(1)
	if id == 2 {
		panic("boom")
	}
	return nil
}

func (s *countingSyncer) AdvanceUsers(context.Context) (int, error) {
	s.advanced.Add(1)
	return 0, nil
}

func (s *countingSyncer) ReconcileServer(context.Context, int64) error {
	s.reconciled.Add(1)
	return nil
}

func (s *countingSyncer) ReconcileRequests() <-chan struct{} {
	return s.requests
}

// TestRunUserSync：定时采集用量并推进配额，变更请求立即触发对账（不必等兜底周期），单台面板 panic 不影响其它。
func TestRunUserSync(t *testing.T) {
	s := &countingSyncer{requests: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunUserSync(ctx, s, UserSyncConfig{TrafficInterval: 20 * time.Millisecond, ReconcileInterval: time.Hour, Concurrency: 2})
		close(done)
	}()
	s.requests <- struct{}{}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && (s.reconciled.Load() < 2 || s.advanced.Load() < 2) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("user sync did not stop after cancel")
	}
	if s.reconciled.Load() < 2 || s.advanced.Load() < 2 || s.collected.Load() < 4 {
		t.Fatalf("reconciled=%d advanced=%d collected=%d", s.reconciled.Load(), s.advanced.Load(), s.collected.Load())
	}
}
