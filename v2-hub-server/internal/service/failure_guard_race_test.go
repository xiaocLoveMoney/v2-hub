package service

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
)

// concurrentAttempts 是并发提交错误口令的请求数，远大于失败上限。
const concurrentAttempts = 40

// raceOutcome 统计并发尝试的结果：verified 是真正走到口令校验、返回“口令错误”的次数，limited 是被频控拒绝的次数。
type raceOutcome struct {
	verified int
	limited  int
	other    []error
}

// runConcurrently 同时发起 n 次 call；isVerified 判断错误是否属于“口令被校验且错误”。
func runConcurrently(n int, call func() error, isVerified func(error) bool) raceOutcome {
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		out raceOutcome
	)
	start := make(chan struct{})
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := call()
			mu.Lock()
			defer mu.Unlock()
			switch {
			case errors.Is(err, ErrRateLimited):
				out.limited++
			case isVerified(err):
				out.verified++
			default:
				out.other = append(out.other, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	return out
}

func (o raceOutcome) check(t *testing.T, limit int) {
	t.Helper()
	if len(o.other) > 0 {
		t.Fatalf("unexpected errors: %v", o.other)
	}
	if o.verified > limit {
		t.Fatalf("verified %d wrong passwords concurrently, want at most %d (limited %d)", o.verified, limit, o.limited)
	}
	if o.verified+o.limited != concurrentAttempts {
		t.Fatalf("verified %d + limited %d != %d", o.verified, o.limited, concurrentAttempts)
	}
}

func hasCode(code Code) func(error) bool {
	return func(err error) bool {
		var se *Error
		return errors.As(err, &se) && se.Code == code
	}
}

// 并发错密码登录（Ruling-105）：被真正校验的次数不超过上限，审计里的失败条数与之一致；之后正确密码也被拒。
func TestConcurrentAdminLoginCapped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bootstrap(t, "root")
	ip := netip.MustParseAddr("203.0.113.9")
	limit := testAuthConfig().LoginMaxFailures

	out := runConcurrently(concurrentAttempts, func() error {
		_, err := f.auth.Login(ctx, LoginInput{Username: "root", Password: bad, ClientIP: &ip})
		return err
	}, hasCode(CodeUnauthenticated))
	out.check(t, limit)

	var failed int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE action = $1", ActionAdminLoginFailed).Scan(&failed); err != nil {
		t.Fatalf("count audits: %v", err)
	}
	if failed != out.verified || failed > limit {
		t.Fatalf("login failure audits = %d, verified = %d, limit = %d", failed, out.verified, limit)
	}
	if _, err := f.auth.Login(ctx, LoginInput{Username: "root", Password: cred, ClientIP: &ip}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("login after concurrent failures err = %v, want rate_limited", err)
	}
}

// 并发错密码二次验证与本人改密码：被真正校验的次数不超过上限。
func TestConcurrentReauthAndPasswordChangeCapped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bootstrap(t, "root")
	p := f.login(t, "root").Principal
	limit := testAuthConfig().LoginMaxFailures

	out := runConcurrently(concurrentAttempts, func() error {
		_, err := f.auth.Reauth(ctx, p, bad, "", nil)
		return err
	}, hasCode(CodeInvalidArgument))
	out.check(t, limit)
	if _, err := f.auth.Reauth(ctx, p, cred, "", nil); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("reauth after concurrent failures err = %v, want rate_limited", err)
	}

	out = runConcurrently(concurrentAttempts, func() error {
		return f.auth.ChangeOwnPassword(ctx, p, bad, "another-strong-pass-1", nil)
	}, hasCode(CodeInvalidArgument))
	out.check(t, limit)
}

// 用户端并发错密码登录与确认口令（改密码）：被真正校验的次数不超过上限。
func TestConcurrentPortalLoginCapped(t *testing.T) {
	f := newPortalFixture(t)
	ctx := context.Background()
	f.newUser(t, "carol", 0)
	limit := 5

	out := runConcurrently(concurrentAttempts, func() error {
		_, err := f.portal.Login(ctx, LoginInput{Username: "carol", Password: bad})
		return err
	}, hasCode(CodeUnauthenticated))
	out.check(t, limit)

	f.newUser(t, "dave", 0)
	p, err := f.portal.Authenticate(ctx, f.mustLogin(t, "dave").Token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	out = runConcurrently(concurrentAttempts, func() error {
		return f.portal.ChangeLoginSecret(ctx, p, bad, "another-strong-pass-1", nil)
	}, hasCode(CodeInvalidArgument))
	out.check(t, limit)
}
