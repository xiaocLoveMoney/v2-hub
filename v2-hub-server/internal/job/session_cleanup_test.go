package job

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type countingPurger struct {
	calls   atomic.Int32
	panicky bool
}

func (p *countingPurger) PurgeExpiredSessions(context.Context) (int64, error) {
	n := p.calls.Add(1)
	if p.panicky && n == 1 {
		panic("boom")
	}
	return 0, errors.New("db down")
}

func TestRunSessionCleanupSurvivesPanicAndStops(t *testing.T) {
	p := &countingPurger{panicky: true}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunSessionCleanup(ctx, p, 5*time.Millisecond)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for p.calls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunSessionCleanup did not stop after cancel")
	}
	if p.calls.Load() < 3 {
		t.Fatalf("calls = %d, want >= 3 (must keep running after panic and errors)", p.calls.Load())
	}
}
