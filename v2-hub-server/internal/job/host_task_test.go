package job

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeHostRunner struct {
	mu          sync.Mutex
	queued      []int64
	done        []int64
	interrupted atomic.Int32
	active      atomic.Int32
	maxActive   atomic.Int32
	release     chan struct{}
	requests    chan struct{}
}

func (f *fakeHostRunner) FailInterrupted(context.Context) (int64, error) {
	f.interrupted.Add(1)
	return 0, nil
}

func (f *fakeHostRunner) ClaimTasks(_ context.Context, limit int) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := min(limit, len(f.queued))
	ids := append([]int64(nil), f.queued[:n]...)
	f.queued = f.queued[n:]
	return ids, nil
}

func (f *fakeHostRunner) RunTask(_ context.Context, id int64) {
	now := f.active.Add(1)
	for {
		seen := f.maxActive.Load()
		if now <= seen || f.maxActive.CompareAndSwap(seen, now) {
			break
		}
	}
	<-f.release
	f.active.Add(-1)
	f.mu.Lock()
	f.done = append(f.done, id)
	f.mu.Unlock()
	if id == panicTaskID {
		panic("boom")
	}
}

func (f *fakeHostRunner) Requests() <-chan struct{} { return f.requests }

func (f *fakeHostRunner) doneCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.done)
}

const (
	panicTaskID     = 3
	testConcurrency = 2
	testPoll        = 5 * time.Millisecond
	testDeadline    = 2 * time.Second
)

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testDeadline)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(testPoll)
	}
}

func TestRunHostTasksLimitsConcurrencyAndSurvivesPanic(t *testing.T) {
	r := &fakeHostRunner{queued: []int64{1, 2, panicTaskID, 4, 5}, release: make(chan struct{}), requests: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() {
		RunHostTasks(ctx, r, testPoll, testConcurrency)
		close(finished)
	}()

	waitUntil(t, func() bool { return r.active.Load() == testConcurrency })
	if r.interrupted.Load() != 1 {
		t.Fatal("interrupted tasks must be marked once at startup")
	}
	// 名额占满期间再多轮询几次，也不会多领
	time.Sleep(4 * testPoll)
	if got := r.maxActive.Load(); got != testConcurrency {
		t.Fatalf("max concurrent = %d, want %d", got, testConcurrency)
	}
	close(r.release)
	waitUntil(t, func() bool { return r.doneCount() == 5 })
	if got := r.maxActive.Load(); got > testConcurrency {
		t.Fatalf("max concurrent = %d, want <= %d", got, testConcurrency)
	}

	cancel()
	select {
	case <-finished:
	case <-time.After(testDeadline):
		t.Fatal("RunHostTasks did not stop after cancel")
	}
}

type fakeCertRenewer struct {
	rounds chan struct{}
}

func (f *fakeCertRenewer) QueueCertRenewals(context.Context) (int, error) {
	f.rounds <- struct{}{}
	return 1, nil
}

// 启动时先检查一次，之后每个间隔再检查；ctx 取消后退出。
func TestRunCertRenewal(t *testing.T) {
	renewer := &fakeCertRenewer{rounds: make(chan struct{}, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunCertRenewal(ctx, renewer, 5*time.Millisecond)
	}()
	for range 3 {
		select {
		case <-renewer.rounds:
		case <-time.After(2 * time.Second):
			t.Fatal("renewal check did not run")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunCertRenewal did not stop after cancel")
	}
}
