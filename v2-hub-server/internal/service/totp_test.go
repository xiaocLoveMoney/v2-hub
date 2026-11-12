package service

import (
	"context"
	"encoding/base32"
	"errors"
	"testing"
	"time"

	"v2-hub-server/internal/manager/totp"
)

func codeFor(t *testing.T, secretB32 string, at time.Time) string {
	t.Helper()
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretB32)
	if err != nil {
		t.Fatalf("decode secret: %v", err)
	}
	return totp.CodeAt(raw, totp.Step(at))
}

// enableTOTP 为 root 走完 setup → enable，返回 Base32 密钥。
func enableTOTP(t *testing.T, f *fixture, p *AdminPrincipal) string {
	t.Helper()
	ctx := context.Background()
	setup, err := f.auth.SetupTOTP(ctx, p)
	if err != nil {
		t.Fatalf("SetupTOTP: %v", err)
	}
	if err := f.auth.EnableTOTP(ctx, p, "000000", nil); !errors.Is(err, ErrInvalidArgument) && err != nil {
		t.Fatalf("EnableTOTP wrong code err = %v", err)
	}
	if err := f.auth.EnableTOTP(ctx, p, codeFor(t, setup.Secret, f.clock.now()), nil); err != nil {
		t.Fatalf("EnableTOTP: %v", err)
	}
	return setup.Secret
}

func TestTOTPLoginFlow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bootstrap(t, "root")
	p := f.login(t, "root").Principal
	secret := enableTOTP(t, f, p)

	if _, err := f.auth.SetupTOTP(ctx, p); !errors.Is(err, ErrConflict) {
		t.Fatalf("setup when enabled err = %v", err)
	}
	// 口令正确但缺验证码：totp_required，不计失败
	if _, err := f.auth.Login(ctx, LoginInput{Username: "root", Password: cred}); !errors.Is(err, ErrTOTPRequired) {
		t.Fatalf("login without code err = %v", err)
	}
	// 开启时用掉了当前步长，同一验证码不能再用（防重放）；推进到下一步长后可以登录
	if _, err := f.auth.Login(ctx, LoginInput{Username: "root", Password: cred, TOTPCode: codeFor(t, secret, f.clock.now())}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("replayed code err = %v", err)
	}
	f.clock.advance(totp.Period)
	res, err := f.auth.Login(ctx, LoginInput{Username: "root", Password: cred, TOTPCode: codeFor(t, secret, f.clock.now())})
	if err != nil {
		t.Fatalf("login with code: %v", err)
	}
	if !res.Principal.TOTPEnabled {
		t.Fatal("principal should report totp enabled")
	}
	// 错误口令不会泄露“需要验证码”
	if _, err := f.auth.Login(ctx, LoginInput{Username: "root", Password: bad}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("bad credential err = %v", err)
	}

	f.clock.advance(totp.Period)
	if err := f.auth.DisableTOTP(ctx, res.Principal, codeFor(t, secret, f.clock.now()), nil); err != nil {
		t.Fatalf("DisableTOTP: %v", err)
	}
	if _, err := f.auth.Login(ctx, LoginInput{Username: "root", Password: cred}); err != nil {
		t.Fatalf("login after disable: %v", err)
	}
}

func TestEnableTOTPRequiresSetupAndRateLimits(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bootstrap(t, "root")
	p := f.login(t, "root").Principal

	if err := f.auth.EnableTOTP(ctx, p, "123456", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("enable without setup err = %v", err)
	}
	if err := f.auth.EnableTOTP(ctx, p, "12ab56", nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("malformed code err = %v", err)
	}
	if _, err := f.auth.SetupTOTP(ctx, p); err != nil {
		t.Fatalf("SetupTOTP: %v", err)
	}
	for i := 0; i < testAuthConfig().LoginMaxFailures; i++ {
		if err := f.auth.EnableTOTP(ctx, p, "000000", nil); !errors.Is(err, ErrInvalidArgument) {
			// 极小概率 000000 恰为当前码；此时该用例无意义，直接跳过
			t.Skipf("000000 happened to be valid: %v", err)
		}
	}
	if err := f.auth.EnableTOTP(ctx, p, "000000", nil); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("after repeated failures err = %v", err)
	}
}

func TestResetAdminTOTP(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bootstrap(t, "root")
	f.bootstrap(t, "root2")
	root := f.login(t, "root").Principal
	root2Session := f.login(t, "root2")
	enableTOTP(t, f, root2Session.Principal)

	if err := f.rbac.ResetAdminTOTP(ctx, root, root.AdminID, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("reset self err = %v (R4)", err)
	}
	if err := f.rbac.ResetAdminTOTP(ctx, root, root2Session.Principal.AdminID, nil); err != nil {
		t.Fatalf("ResetAdminTOTP: %v", err)
	}
	if _, err := f.auth.Authenticate(ctx, root2Session.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("sessions should be revoked after reset, err = %v", err)
	}
	if _, err := f.auth.Login(ctx, LoginInput{Username: "root2", Password: cred}); err != nil {
		t.Fatalf("login without totp after reset: %v", err)
	}
}
