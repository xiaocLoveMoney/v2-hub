package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"v2-hub-server/internal/manager/totp"
)

const (
	portalIdle   = 7 * 24 * time.Hour
	portalMaxAge = 30 * 24 * time.Hour
	nextCred     = "another-horse-staple"
)

type portalFixture struct {
	*userFixture
	portal *PortalAuthService
}

func newPortalFixture(t *testing.T) *portalFixture {
	t.Helper()
	uf := newUserFixture(t)
	cfg := PortalAuthConfig{
		SessionIdleTimeout: portalIdle, SessionMaxAge: portalMaxAge, SessionTouchInterval: time.Minute,
		LoginMaxFailures: 5, LoginWindow: 15 * time.Minute, LoginLockout: 15 * time.Minute, LoginMaxTrackedKeys: 100,
		LoginLogRetention: 90 * 24 * time.Hour,
	}
	portal, err := NewPortalAuthService(uf.pool, cfg, testSealer(t), uf.clock.now)
	if err != nil {
		t.Fatalf("NewPortalAuthService: %v", err)
	}
	return &portalFixture{userFixture: uf, portal: portal}
}

func (f *portalFixture) login(t *testing.T, username, secret, code string) (*PortalLoginResult, error) {
	t.Helper()
	return f.portal.Login(context.Background(), LoginInput{Username: username, Password: secret, TOTPCode: code})
}

func (f *portalFixture) mustLogin(t *testing.T, username string) *PortalLoginResult {
	t.Helper()
	res, err := f.login(t, username, cred, "")
	if err != nil {
		t.Fatalf("Login(%s): %v", username, err)
	}
	return res
}

func TestPortalLoginSessionAndLogs(t *testing.T) {
	f := newPortalFixture(t)
	ctx := context.Background()
	f.newUser(t, "alice", 0)

	if _, err := f.login(t, "alice", bad, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("wrong secret: err = %v, want unauthenticated", err)
	}
	if _, err := f.login(t, "nobody", cred, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("unknown user: err = %v, want unauthenticated", err)
	}
	res := f.mustLogin(t, "alice")
	p, err := f.portal.Authenticate(ctx, res.Token)
	if err != nil || p.Username != "alice" || p.CSRFToken == "" || p.SessionID == 0 {
		t.Fatalf("Authenticate = %+v, %v", p, err)
	}
	logs, err := f.portal.ListLoginLogs(ctx, p)
	if err != nil || len(logs) != 2 || !logs[0].IsSuccess || logs[1].IsSuccess {
		t.Fatalf("login logs = %+v, %v; want success then failure", logs, err)
	}

	// 空闲 7 天过期
	f.clock.advance(portalIdle + time.Second)
	if _, err := f.portal.Authenticate(ctx, res.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("idle session: err = %v, want unauthenticated", err)
	}

	// 登出后会话失效
	res = f.mustLogin(t, "alice")
	p, _ = f.portal.Authenticate(ctx, res.Token)
	if err := f.portal.Logout(ctx, p); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := f.portal.Authenticate(ctx, res.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("after logout: err = %v, want unauthenticated", err)
	}
}

func TestPortalLoginRateLimitAndDisabledUser(t *testing.T) {
	f := newPortalFixture(t)
	ctx := context.Background()
	u := f.newUser(t, "bob", 0)
	f.newUser(t, "carol", 0)

	// 停用的用户可以登录，但业务接口被拦
	if _, err := f.users.UpdateUser(ctx, f.actor, u.ID, UserUpdateInput{IsDisabled: true}, nil); err != nil {
		t.Fatalf("disable user: %v", err)
	}
	res := f.mustLogin(t, "bob")
	p, err := f.portal.Authenticate(ctx, res.Token)
	if err != nil || !p.IsDisabled {
		t.Fatalf("disabled user Authenticate = %+v, %v", p, err)
	}
	if err := p.RequireEnabled(); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("RequireEnabled: err = %v, want account_disabled", err)
	}

	// 连续 5 次失败后锁定，正确密码也被拒
	for i := range 5 {
		if _, err := f.login(t, "carol", bad, ""); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("failure %d: err = %v", i, err)
		}
	}
	if _, err := f.login(t, "carol", cred, ""); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("locked: err = %v, want rate_limited", err)
	}
	f.clock.advance(16 * time.Minute)
	if _, err := f.login(t, "carol", cred, ""); err != nil {
		t.Fatalf("after lockout: %v", err)
	}

	// 删除的用户不能再登录，已有会话失效
	if err := f.users.DeleteUser(ctx, f.actor, u.ID, nil); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := f.portal.Authenticate(ctx, res.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("deleted user session: err = %v, want unauthenticated", err)
	}
}

func TestPortalChangeSecretRevokesOtherSessions(t *testing.T) {
	f := newPortalFixture(t)
	ctx := context.Background()
	u := f.newUser(t, "dave", 0)
	first := f.mustLogin(t, "dave")
	second := f.mustLogin(t, "dave")
	p, _ := f.portal.Authenticate(ctx, second.Token)

	if err := f.portal.ChangeLoginSecret(ctx, p, bad, nextCred, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("wrong old secret: err = %v, want invalid_argument", err)
	}
	if err := f.portal.ChangeLoginSecret(ctx, p, cred, "short", nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("short new secret: err = %v, want invalid_argument", err)
	}
	if err := f.portal.ChangeLoginSecret(ctx, p, cred, nextCred, nil); err != nil {
		t.Fatalf("ChangeLoginSecret: %v", err)
	}
	if _, err := f.portal.Authenticate(ctx, first.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("other session: err = %v, want unauthenticated", err)
	}
	if _, err := f.portal.Authenticate(ctx, second.Token); err != nil {
		t.Fatalf("current session should survive: %v", err)
	}
	if _, err := f.login(t, "dave", cred, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("old secret: err = %v, want unauthenticated", err)
	}
	if _, err := f.login(t, "dave", nextCred, ""); err != nil {
		t.Fatalf("new secret: %v", err)
	}

	// 管理员重置密码后全部会话失效
	if err := f.users.ResetUserPassword(ctx, f.actor, u.ID, cred, nil); err != nil {
		t.Fatalf("ResetUserPassword: %v", err)
	}
	if _, err := f.portal.Authenticate(ctx, second.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("after admin reset: err = %v, want unauthenticated", err)
	}
}

func TestPortalTOTPFlow(t *testing.T) {
	f := newPortalFixture(t)
	ctx := context.Background()
	u := f.newUser(t, "erin", 0)
	res := f.mustLogin(t, "erin")
	p, _ := f.portal.Authenticate(ctx, res.Token)

	if err := f.portal.EnableTOTP(ctx, p, "123456", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("enable without setup: err = %v, want conflict", err)
	}
	setup, err := f.portal.SetupTOTP(ctx, p)
	if err != nil {
		t.Fatalf("SetupTOTP: %v", err)
	}
	if err := f.portal.EnableTOTP(ctx, p, codeFor(t, setup.Secret, f.clock.now()), nil); err != nil {
		t.Fatalf("EnableTOTP: %v", err)
	}
	if _, err := f.portal.SetupTOTP(ctx, p); !errors.Is(err, ErrConflict) {
		t.Fatalf("setup when enabled: err = %v, want conflict", err)
	}

	// 登录须补交验证码；同一步长的验证码不能重放
	if _, err := f.login(t, "erin", cred, ""); !errors.Is(err, ErrTOTPRequired) {
		t.Fatalf("login without code: err = %v, want totp_required", err)
	}
	if _, err := f.login(t, "erin", cred, codeFor(t, setup.Secret, f.clock.now())); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("replayed code: err = %v, want unauthenticated", err)
	}
	f.clock.advance(totp.Period)
	if _, err := f.login(t, "erin", cred, codeFor(t, setup.Secret, f.clock.now())); err != nil {
		t.Fatalf("login with fresh code: %v", err)
	}

	// 关闭须提交当前验证码
	if err := f.portal.DisableTOTP(ctx, p, "000000", nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("disable with wrong code: err = %v, want invalid_argument", err)
	}
	f.clock.advance(totp.Period)
	if err := f.portal.DisableTOTP(ctx, p, codeFor(t, setup.Secret, f.clock.now()), nil); err != nil {
		t.Fatalf("DisableTOTP: %v", err)
	}
	if _, err := f.login(t, "erin", cred, ""); err != nil {
		t.Fatalf("login after disable: %v", err)
	}

	// 管理员清除两步验证：全部会话失效，之后无需验证码
	setup, _ = f.portal.SetupTOTP(ctx, p)
	if err := f.portal.EnableTOTP(ctx, p, codeFor(t, setup.Secret, f.clock.now()), nil); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if err := f.users.ClearUserTOTP(ctx, f.actor, u.ID, nil); err != nil {
		t.Fatalf("ClearUserTOTP: %v", err)
	}
	if _, err := f.portal.Authenticate(ctx, res.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("session after admin clear: err = %v, want unauthenticated", err)
	}
	if _, err := f.login(t, "erin", cred, ""); err != nil {
		t.Fatalf("login after admin clear: %v", err)
	}
	if err := f.users.ClearUserTOTP(ctx, f.actor, u.ID+999, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("clear unknown user: err = %v, want not_found", err)
	}
}
