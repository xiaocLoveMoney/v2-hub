package job

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type countingLocator struct{ calls atomic.Int32 }

func (l *countingLocator) BackfillServerLocations(context.Context) (int, error) {
	if l.calls.Add(1) == 1 {
		panic("first run panics")
	}
	return 1, nil
}

func TestRunServerLocateRepeatsAndRecovers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	loc := &countingLocator{}
	done := make(chan struct{})
	go func() {
		RunServerLocate(ctx, loc, 10*time.Millisecond)
		close(done)
	}()
	deadline := time.After(2 * time.Second)
	for loc.calls.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("calls = %d, want >= 3", loc.calls.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
}
