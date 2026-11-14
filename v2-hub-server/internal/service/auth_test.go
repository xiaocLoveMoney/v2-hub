package service

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/daotest"
	"v2-hub-server/internal/manager/crypto"
)

// cred 是测试用密码（仅测试库内有效）；bad 是错误密码。
const (
	cred = "correct-horse-battery"
	bad  = "wrong-guess-123"
)

// testMasterKey 是测试专用主密钥（32 个 0x2a 的 Base64），只用于测试库。
const testMasterKey = "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio="

func testSealer(t *testing.T) *crypto.Sealer {
	t.Helper()
	s, err := crypto.NewSealer(testMasterKey)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	return s
}

type testClock struct{ t time.Time }

func (c *testClock) now() time.Time          { return c.t }
func (c *testClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func testAuthConfig() AuthConfig {
	return AuthConfig{
		SessionIdleTimeout:   2 * time.Hour,
		SessionMaxAge:        7 * 24 * time.Hour,
		SessionTouchInterval: time.Minute,
		LoginMaxFailures:     5,
		LoginWindow:          15 * time.Minute,
		LoginLockout:         15 * time.Minute,
		LoginMaxTrackedKeys:  100,
	}
}

type fixture struct {
	pool  *pgxpool.Pool
	auth  *AuthService
	rbac  *RBACService
	clock *testClock
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := daotest.NewPool(t)
	clock := &testClock{t: time.Now()}
	auth, err := NewAuthService(pool, testAuthConfig(), testSealer(t), clock.now)
	if err != nil {
		t.Fatalf("NewAuthService: %v", err)
	}
	return &fixture{pool: pool, auth: auth, rbac: NewRBACService(pool), clock: clock}
}

func (f *fixture) bootstrap(t *testing.T, username string) *Admin {
	t.Helper()
	a, err := f.rbac.BootstrapSuperAdmin(context.Background(), username, cred)
	if err != nil {
		t.Fatalf("BootstrapSuperAdmin(%s): %v", username, err)
	}
	return a
}

func (f *fixture) login(t *testing.T, username string) *LoginResult {
	t.Helper()
	res, err := f.auth.Login(context.Background(), LoginInput{Username: username, Password: cred})
	if err != nil {
		t.Fatalf("Login(%s): %v", username, err)
	}
	return res
}

func TestLoginAndAuthenticate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bootstrap(t, "root")

	res := f.login(t, "root")
	if !res.Principal.IsSuperAdmin || !res.Principal.Can(PermNodeWrite) || res.Principal.CSRFToken == "" {
		t.Fatalf("unexpected principal %+v", res.Principal)
	}
	p, err := f.auth.Authenticate(ctx, res.Token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if p.AdminID != res.Principal.AdminID || p.CSRFToken != res.Principal.CSRFToken {
		t.Fatalf("principal mismatch: %+v vs %+v", p, res.Principal)
	}

	if err := f.auth.Logout(ctx, p, nil); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := f.auth.Authenticate(ctx, res.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("after logout err = %v, want unauthenticated", err)
	}
}

func TestLoginFailuresAreUniformAndRateLimited(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bootstrap(t, "root")
	ip := netip.MustParseAddr("203.0.113.7")

	cases := []LoginInput{
		{Username: "root", Password: bad, ClientIP: &ip},
		{Username: "nobody", Password: cred, ClientIP: &ip},
	}
	for _, in := range cases {
		_, err := f.auth.Login(ctx, in)
		var se *Error
		if !errors.As(err, &se) || se.Code != CodeUnauthenticated || se.Message != msgBadCredentials {
			t.Fatalf("Login(%s) err = %v, want uniform bad-credentials", in.Username, err)
		}
	}
	// 同一 IP 再失败 3 次达到 5 次上限，此后正确密码也被拒
	for i := 0; i < 3; i++ {
		if _, err := f.auth.Login(ctx, LoginInput{Username: "root", Password: bad, ClientIP: &ip}); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("attempt %d err = %v", i, err)
		}
	}
	if _, err := f.auth.Login(ctx, LoginInput{Username: "root", Password: cred, ClientIP: &ip}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("locked login err = %v, want rate_limited", err)
	}
	f.clock.advance(16 * time.Minute)
	if _, err := f.auth.Login(ctx, LoginInput{Username: "root", Password: cred, ClientIP: &ip}); err != nil {
		t.Fatalf("login after lockout: %v", err)
	}
}

func TestSessionExpiry(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bootstrap(t, "root")

	res := f.login(t, "root")
	f.clock.advance(90 * time.Minute)
	if _, err := f.auth.Authenticate(ctx, res.Token); err != nil {
		t.Fatalf("within idle timeout: %v", err)
	}
	// 上一次访问顺延了空闲期，再过 90 分钟仍有效
	f.clock.advance(90 * time.Minute)
	if _, err := f.auth.Authenticate(ctx, res.Token); err != nil {
		t.Fatalf("idle timeout should slide: %v", err)
	}
	f.clock.advance(2*time.Hour + time.Second)
	if _, err := f.auth.Authenticate(ctx, res.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("after idle timeout err = %v", err)
	}

	stale := f.login(t, "root")
	f.clock.advance(3 * time.Hour)
	if n, err := f.auth.PurgeExpiredSessions(ctx); err != nil || n != 1 {
		t.Fatalf("PurgeExpiredSessions = %d, %v, want 1", n, err)
	}
	if _, err := f.auth.Authenticate(ctx, stale.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("purged session err = %v", err)
	}
}

func TestDisabledAdminCannotLogin(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root := f.bootstrap(t, "root")
	f.bootstrap(t, "root2")
	actor := f.login(t, "root2").Principal

	victim := f.login(t, "root")
	if _, err := f.rbac.UpdateAdmin(ctx, actor, root.ID, AdminUpdateInput{Roles: []string{RoleSuperAdmin}, IsDisabled: true}, nil); err != nil {
		t.Fatalf("disable root: %v", err)
	}
	if _, err := f.auth.Authenticate(ctx, victim.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("session of disabled admin err = %v (R7)", err)
	}
	if _, err := f.auth.Login(ctx, LoginInput{Username: "root", Password: cred}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("disabled login err = %v", err)
	}
}
