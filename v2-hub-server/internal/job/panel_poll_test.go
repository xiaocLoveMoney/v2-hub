package job

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"v2-hub-server/internal/service"
)

type countingPoller struct {
	mu        sync.Mutex
	probed    map[int64]int
	synced    atomic.Int32
	inFlight  atomic.Int32
	maxFlight atomic.Int32
}

func (p *countingPoller) ListEnabledServerIDs(context.Context) ([]int64, error) {
	return []int64{1, 2, 3, 4, 5}, nil
}

func (p *countingPoller) ProbeServer(_ context.Context, id int64) error {
	n := p.inFlight.Add(1)
	defer p.inFlight.Add(-1)
	for {
		cur := p.maxFlight.Load()
		if n <= cur || p.maxFlight.CompareAndSwap(cur, n) {
			break
		}
	}
	time.Sleep(5 * time.Millisecond)
	p.mu.Lock()
	p.probed[id]++
	p.mu.Unlock()
	if id == 3 {
		panic("boom")
	}
	return errors.New("offline")
}

func (p *countingPoller) SyncServer(context.Context, *service.AdminPrincipal, int64, *netip.Addr) (*service.SyncResult, error) {
	p.synced.Add(1)
	return &service.SyncResult{}, nil
}

func TestRunPanelPollingBoundsConcurrencyAndSurvivesPanics(t *testing.T) {
	p := &countingPoller{probed: map[int64]int{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunPanelPolling(ctx, p, PanelPollConfig{ProbeInterval: 20 * time.Millisecond, SyncInterval: 30 * time.Millisecond, Concurrency: 2})
		close(done)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		rounds := p.probed[5]
		p.mu.Unlock()
		if rounds >= 2 && p.synced.Load() >= 5 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("polling did not stop after cancel")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.probed[5] < 2 || p.synced.Load() < 5 {
		t.Fatalf("probed=%v synced=%d", p.probed, p.synced.Load())
	}
	if p.maxFlight.Load() > 2 {
		t.Fatalf("concurrency %d exceeds limit 2", p.maxFlight.Load())
	}
}
