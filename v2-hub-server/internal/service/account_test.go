package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"
)

// registerInput 返回一份默认合法的注册输入；secret 用测试口令。
func registerInput(username string) RegisterInput {
	in := RegisterInput{Username: username, Email: username + "@example.com"}
	in.Password = cred
	return in
}

// openRegistration 把注册改成开放且不要邮箱验证码，便于只测某一条规则。
func (f *accountFixture) openRegistration(t *testing.T) {
	t.Helper()
	if _, err := f.update(t, map[string]any{"register.mode": RegisterOpen, "register.emailVerify": false}); err != nil {
		t.Fatalf("open registration: %v", err)
	}
}

func (f *accountFixture) adminInvite(t *testing.T, in InviteCodeInput) *InviteCode {
	t.Helper()
	c, err := f.account.CreateInviteCode(context.Background(), f.actor, in, nil)
	if err != nil {
		t.Fatalf("CreateInviteCode: %v", err)
	}
	if !inviteCodePattern.MatchString(c.Code) || c.Kind != InviteKindAdmin {
		t.Fatalf("invite code = %+v", c)
	}
	return c
}

func (f *accountFixture) principalOf(t *testing.T, res *PortalLoginResult) *UserPrincipal {
	t.Helper()
	p, err := f.portal.Authenticate(context.Background(), res.Token)
	if err != nil {
		t.Fatalf("Authenticate after register/login: %v", err)
	}
	return p
}

func TestRegisterWithInviteAndEmailCode(t *testing.T) {
	f := newAccountFixture(t)
	ctx := context.Background()
	plan := f.plan(t, "trial", PlanQuota{TrafficLimit: gib, DurationDays: 7})
	if _, err := f.update(t, map[string]any{"register.planId": plan.ID}); err != nil {
		t.Fatalf("set register plan: %v", err)
	}
	invite := f.adminInvite(t, InviteCodeInput{MaxUses: 1})

	in := registerInput("alice")
	if _, err := f.account.Register(ctx, in); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("invite mode without code = %v, want invalid_argument", err)
	}
	in.InviteCode = "  " + lowerASCII(invite.Code) + " " // 不区分大小写，容忍首尾空白
	if _, err := f.account.Register(ctx, in); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("missing email code = %v, want invalid_argument", err)
	}
	if err := f.account.SendEmailCode(ctx, in.Email, EmailCodeRegister, nil); err != nil {
		t.Fatalf("SendEmailCode: %v", err)
	}
	code := f.lastCode(t, in.Email)
	in.EmailCode = wrongCode(code)
	if _, err := f.account.Register(ctx, in); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("wrong email code = %v, want invalid_argument", err)
	}
	in.EmailCode = code
	res, err := f.account.Register(ctx, in)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	p := f.principalOf(t, res)
	if p.Username != "alice" || p.Email != "alice@example.com" || !p.EmailVerified || p.Status != UserStatusActive {
		t.Fatalf("principal = %+v", p)
	}
	u, err := f.users.GetUser(ctx, p.UserID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if u.PlanName != "trial" || u.Quota.TrafficLimit != gib || u.InvitedBy != nil || !u.EmailVerified {
		t.Fatalf("registered user = %+v", u)
	}
	f.reconcile(t)
	if c := f.panelClient(t, f.emailOf(t, u.ID)); !c.Enabled {
		t.Fatalf("registered user's client should be provisioned and enabled: %+v", c)
	}

	// 码只有 1 次：第二个人用同一个码注册失败，且不留下用户
	in2 := registerInput("bob")
	in2.InviteCode = invite.Code
	f.clock.advance(2 * time.Minute)
	if err := f.account.SendEmailCode(ctx, in2.Email, EmailCodeRegister, nil); err != nil {
		t.Fatalf("SendEmailCode bob: %v", err)
	}
	in2.EmailCode = f.lastCode(t, in2.Email)
	if _, err := f.account.Register(ctx, in2); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("used-up invite = %v, want invalid_argument", err)
	}
	if _, err := f.portal.Login(ctx, LoginInput{Username: "bob", Password: cred}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("bob must not exist after a failed registration, login = %v", err)
	}
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

func TestRegisterConflictsKeepEmailCodeUsable(t *testing.T) {
	f := newAccountFixture(t)
	ctx := context.Background()
	if _, err := f.update(t, map[string]any{"register.mode": RegisterOpen}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	f.newUser(t, "taken", 0)

	in := registerInput("taken")
	in.Email = "fresh@example.com"
	if err := f.account.SendEmailCode(ctx, in.Email, EmailCodeRegister, nil); err != nil {
		t.Fatalf("SendEmailCode: %v", err)
	}
	in.EmailCode = f.lastCode(t, in.Email)
	if _, err := f.account.Register(ctx, in); !errors.Is(err, ErrConflict) {
		t.Fatalf("taken username = %v, want conflict", err)
	}
	// 用户名冲突不应把验证码用掉：换个用户名用同一个码能注册成功
	in.Username = "fresh"
	if _, err := f.account.Register(ctx, in); err != nil {
		t.Fatalf("retry with the same email code: %v", err)
	}
	// 邮箱已被占用
	dup := registerInput("other")
	dup.Email = "Fresh@Example.com"
	if _, err := f.update(t, map[string]any{"register.emailVerify": false}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := f.account.Register(ctx, dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("taken email = %v, want conflict", err)
	}
	for name, bad := range map[string]RegisterInput{
		"username with @": func() RegisterInput { in := registerInput("x"); in.Username = "a@b.c"; return in }(),
		"short secret":    func() RegisterInput { in := registerInput("shorty"); in.Password = "1234567"; return in }(),
		"bad email":       func() RegisterInput { in := registerInput("bademail"); in.Email = "nope"; return in }(),
		"bad invite":      func() RegisterInput { in := registerInput("badinvite"); in.InviteCode = "not-a-code"; return in }(),
	} {
		if _, err := f.account.Register(ctx, bad); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("%s = %v, want invalid_argument", name, err)
		}
	}
	if _, err := f.update(t, map[string]any{"register.mode": RegisterClosed}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := f.account.Register(ctx, registerInput("late")); !errors.Is(err, ErrFeatureDisabled) {
		t.Fatalf("closed registration = %v, want feature_disabled", err)
	}
}

func TestRegisterInviteCodeIsNotOversoldConcurrently(t *testing.T) {
	f := newAccountFixture(t)
	if _, err := f.update(t, map[string]any{"register.emailVerify": false}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	const limit, racers = 2, 8
	invite := f.adminInvite(t, InviteCodeInput{MaxUses: limit})

	var wg sync.WaitGroup
	results := make([]error, racers)
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := registerInput(fmt.Sprintf("racer%d", i))
			in.InviteCode = invite.Code
			_, results[i] = f.account.Register(context.Background(), in)
		}()
	}
	wg.Wait()
	ok := 0
	for i, err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrInvalidArgument):
		default:
			t.Fatalf("racer %d: unexpected error %v", i, err)
		}
	}
	if ok != limit {
		t.Fatalf("%d registrations succeeded with a %d-use invite code", ok, limit)
	}
	var used, users int
	if err := f.pool.QueryRow(context.Background(), "SELECT used_count FROM invite_code WHERE id = $1", invite.ID).Scan(&used); err != nil || used != limit {
		t.Fatalf("used_count = %d, %v; want %d", used, err, limit)
	}
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM end_user WHERE invite_code_id = $1", invite.ID).Scan(&users); err != nil || users != limit {
		t.Fatalf("users registered with the code = %d, %v; want %d", users, err, limit)
	}
}

func TestRegisterRateLimitedPerIP(t *testing.T) {
	f := newAccountFixture(t)
	f.openRegistration(t)
	ip := netip.MustParseAddr("203.0.113.9")
	for i := range registerSuccessesPerHour {
		in := registerInput(fmt.Sprintf("user%d", i))
		in.ClientIP = &ip
		if _, err := f.account.Register(context.Background(), in); err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
	}
	in := registerInput("onemore")
	in.ClientIP = &ip
	if _, err := f.account.Register(context.Background(), in); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("4th registration from one ip within an hour = %v, want rate_limited", err)
	}
	other := netip.MustParseAddr("203.0.113.10")
	in.ClientIP = &other
	if _, err := f.account.Register(context.Background(), in); err != nil {
		t.Fatalf("another ip must not be affected: %v", err)
	}
}

func TestPersonalInviteCode(t *testing.T) {
	f := newAccountFixture(t)
	ctx := context.Background()
	if _, err := f.update(t, map[string]any{"register.emailVerify": false, "invite.userMaxUses": 1}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	alice := f.newUser(t, "alice", 0)
	ap := &UserPrincipal{UserID: alice.ID, Username: alice.Username}
	info, err := f.account.GetInvite(ctx, ap)
	if err != nil || !info.Enabled || !inviteCodePattern.MatchString(info.Code) || info.MaxUses != 1 || info.UsedCount != 0 {
		t.Fatalf("GetInvite = %+v, %v", info, err)
	}
	again, err := f.account.GetInvite(ctx, ap)
	if err != nil || again.Code != info.Code {
		t.Fatalf("personal code must be stable: %+v, %v", again, err)
	}

	in := registerInput("bobby")
	in.InviteCode = info.Code
	res, err := f.account.Register(ctx, in)
	if err != nil {
		t.Fatalf("Register with personal code: %v", err)
	}
	bob, err := f.users.GetUser(ctx, res.Principal.UserID)
	if err != nil || bob.InvitedBy == nil || *bob.InvitedBy != alice.ID || bob.InvitedByUsername != "alice" {
		t.Fatalf("invitee = %+v, %v", bob, err)
	}
	info, err = f.account.GetInvite(ctx, ap)
	if err != nil || info.UsedCount != 1 || len(info.Invitees) != 1 || info.Invitees[0].Username != "bo***" {
		t.Fatalf("after one invite = %+v, %v", info, err)
	}
	carol := registerInput("carol")
	carol.InviteCode = info.Code
	if _, err := f.account.Register(ctx, carol); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("personal code over its limit = %v, want invalid_argument", err)
	}

	// 重置：旧码作废，已用次数不清零（不能靠重置绕过上限）
	reset, err := f.account.ResetInvite(ctx, ap, nil)
	if err != nil || reset.Code == info.Code || reset.UsedCount != 1 {
		t.Fatalf("ResetInvite = %+v, %v", reset, err)
	}
	carol.InviteCode = reset.Code
	if _, err := f.account.Register(ctx, carol); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("reset must not refill uses, got %v", err)
	}
	if _, err := f.update(t, map[string]any{"invite.userMaxUses": 0}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := f.account.Register(ctx, carol); err != nil {
		t.Fatalf("unlimited personal code: %v", err)
	}

	// 管理员停用个人码；个人码不可删
	page, err := f.account.ListInviteCodes(ctx, InviteKindUser, PageQuery{})
	if err != nil || page.Total != 1 || page.Items[0].OwnerUsername != "alice" || page.Items[0].MaxUses != 0 {
		t.Fatalf("user invite codes = %+v, %v", page, err)
	}
	id := page.Items[0].ID
	if err := f.account.DeleteInviteCode(ctx, f.actor, id, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete personal code = %v, want conflict", err)
	}
	if _, err := f.account.UpdateInviteCode(ctx, f.actor, id, InviteCodeInput{IsDisabled: true}, nil); err != nil {
		t.Fatalf("disable personal code: %v", err)
	}
	dave := registerInput("dave")
	dave.InviteCode = reset.Code
	if _, err := f.account.Register(ctx, dave); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("disabled personal code = %v, want invalid_argument", err)
	}
	if info, err := f.account.GetInvite(ctx, ap); err != nil || info.Enabled || info.Code != "" {
		t.Fatalf("owner view of a disabled code = %+v, %v", info, err)
	}

	// 设置关闭个人邀请码
	if _, err := f.update(t, map[string]any{"invite.userEnabled": false}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := f.account.ResetInvite(ctx, ap, nil); !errors.Is(err, ErrFeatureDisabled) {
		t.Fatalf("reset with personal codes off = %v, want feature_disabled", err)
	}
}

func TestAdminInviteCodeLifecycle(t *testing.T) {
	f := newAccountFixture(t)
	ctx := context.Background()
	if _, err := f.update(t, map[string]any{"register.emailVerify": false}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	past := f.clock.now().Add(-time.Hour)
	expired := f.adminInvite(t, InviteCodeInput{ExpiresAt: &past, Remark: "过期的"})
	in := registerInput("erin")
	in.InviteCode = expired.Code
	if _, err := f.account.Register(ctx, in); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expired invite = %v, want invalid_argument", err)
	}
	future := f.clock.now().Add(time.Hour)
	updated, err := f.account.UpdateInviteCode(ctx, f.actor, expired.ID, InviteCodeInput{ExpiresAt: &future, MaxUses: 5, Remark: "续期"}, nil)
	if err != nil || updated.MaxUses != 5 || updated.Remark != "续期" || updated.ExpiresAt == nil {
		t.Fatalf("UpdateInviteCode = %+v, %v", updated, err)
	}
	if _, err := f.account.Register(ctx, in); err != nil {
		t.Fatalf("register after extending the code: %v", err)
	}
	if _, err := f.account.CreateInviteCode(ctx, f.actor, InviteCodeInput{MaxUses: -1}, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("negative uses = %v", err)
	}
	if _, err := f.account.ListInviteCodes(ctx, "nope", PageQuery{}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("bad kind = %v", err)
	}
	if err := f.account.DeleteInviteCode(ctx, f.actor, expired.ID, nil); err != nil {
		t.Fatalf("DeleteInviteCode: %v", err)
	}
	if err := f.account.DeleteInviteCode(ctx, f.actor, expired.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice = %v, want not_found", err)
	}
	// 删除邀请码不影响已注册的用户
	if _, err := f.portal.Login(ctx, LoginInput{Username: "erin", Password: cred}); err != nil {
		t.Fatalf("user registered with a deleted code must still log in: %v", err)
	}
}

func TestLoginByEmailAndLegacyUsernamePrecedence(t *testing.T) {
	f := newAccountFixture(t)
	ctx := context.Background()
	f.openRegistration(t)
	if _, err := f.account.Register(ctx, registerInput("alice")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	res, err := f.portal.Login(ctx, LoginInput{Username: " Alice@Example.com ", Password: cred})
	if err != nil || res.Principal.Username != "alice" {
		t.Fatalf("login by email = %+v, %v", res, err)
	}
	if _, err := f.portal.Login(ctx, LoginInput{Username: "alice@example.com", Password: bad}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("login by email with wrong secret = %v", err)
	}
	// 存量的含 @ 用户名：按用户名优先，且别人不能把邮箱绑成这个用户名
	if _, err := f.pool.Exec(ctx, `UPDATE end_user SET username = 'legacy@example.com' WHERE username = 'alice'`); err != nil {
		t.Fatalf("make legacy username: %v", err)
	}
	if res, err := f.portal.Login(ctx, LoginInput{Username: "legacy@example.com", Password: cred}); err != nil || res.Principal.Username != "legacy@example.com" {
		t.Fatalf("login by legacy username = %+v, %v", res, err)
	}
	dup := registerInput("mallory")
	dup.Email = "legacy@example.com"
	if _, err := f.account.Register(ctx, dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("email equal to a legacy username = %v, want conflict", err)
	}
}

func TestResetLoginSecretKeepsTOTPAndRevokesSessions(t *testing.T) {
	f := newAccountFixture(t)
	ctx := context.Background()
	f.openRegistration(t)
	res, err := f.account.Register(ctx, registerInput("alice"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE end_user SET totp_secret_enc = '\x01' WHERE id = $1`, res.Principal.UserID); err != nil {
		t.Fatalf("mark totp enabled: %v", err)
	}
	if err := f.account.SendEmailCode(ctx, "alice@example.com", EmailCodeReset, nil); err != nil {
		t.Fatalf("SendEmailCode: %v", err)
	}
	code := f.lastCode(t, "alice@example.com")

	if err := f.account.ResetLoginSecret(ctx, "alice@example.com", code, "short", nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("short new secret = %v", err)
	}
	if err := f.account.ResetLoginSecret(ctx, "ghost@example.com", code, nextCred, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unknown email = %v, want invalid_argument", err)
	}
	if err := f.account.ResetLoginSecret(ctx, "alice@example.com", wrongCode(code), nextCred, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("wrong code = %v", err)
	}
	if err := f.account.ResetLoginSecret(ctx, "alice@example.com", code, nextCred, nil); err != nil {
		t.Fatalf("ResetLoginSecret: %v", err)
	}
	if _, err := f.portal.Authenticate(ctx, res.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("old session after reset = %v, want unauthenticated", err)
	}
	var totpOn bool
	if err := f.pool.QueryRow(ctx, "SELECT totp_secret_enc IS NOT NULL FROM end_user WHERE id = $1", res.Principal.UserID).Scan(&totpOn); err != nil || !totpOn {
		t.Fatalf("totp enabled after reset = %v, %v; resetting the secret must not turn off two-step verification (Ruling-23)", totpOn, err)
	}
	if _, err := f.portal.Login(ctx, LoginInput{Username: "alice", Password: cred}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("old secret after reset = %v", err)
	}
	// 新口令正确 → 进入两步验证环节（而不是直接登录）
	if _, err := f.portal.Login(ctx, LoginInput{Username: "alice", Password: nextCred}); !errors.Is(err, ErrTOTPRequired) {
		t.Fatalf("new secret after reset = %v, want totp_required", err)
	}
	if err := f.account.ResetLoginSecret(ctx, "alice@example.com", code, cred, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("reusing a reset code = %v, want invalid_argument", err)
	}
}

func TestBindEmail(t *testing.T) {
	f := newAccountFixture(t)
	ctx := context.Background()
	f.newUser(t, "alice", 0)
	f.newUser(t, "bob", 0)
	if _, err := f.pool.Exec(ctx, `UPDATE end_user SET email = 'bob@example.com' WHERE username = 'bob'`); err != nil {
		t.Fatalf("seed bob email: %v", err)
	}
	p := f.principalOf(t, f.mustLogin(t, "alice"))

	if err := f.account.SendBindEmailCode(ctx, p, "bob@example.com", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("bind someone else's email = %v, want conflict", err)
	}
	if err := f.account.SendBindEmailCode(ctx, p, "Alice@Example.com", nil); err != nil {
		t.Fatalf("SendBindEmailCode: %v", err)
	}
	code := f.lastCode(t, "alice@example.com")
	if err := f.account.BindEmail(ctx, p, bad, "alice@example.com", code, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("wrong current secret = %v", err)
	}
	if err := f.account.BindEmail(ctx, p, cred, "other@example.com", code, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("code for another address = %v", err)
	}
	if err := f.account.BindEmail(ctx, p, cred, "alice@example.com", code, nil); err != nil {
		t.Fatalf("BindEmail: %v", err)
	}
	if p.Email != "alice@example.com" || !p.EmailVerified {
		t.Fatalf("principal after bind = %+v", p)
	}
	if res, err := f.portal.Login(ctx, LoginInput{Username: "alice@example.com", Password: cred}); err != nil || res.Principal.UserID != p.UserID {
		t.Fatalf("login by bound email = %+v, %v", res, err)
	}
	if err := f.account.SendBindEmailCode(ctx, p, "alice@example.com", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("rebinding the same email = %v, want conflict", err)
	}
}

func TestDeleteOwnAccountFreesIdentityAndRemovesClient(t *testing.T) {
	f := newAccountFixture(t)
	ctx := context.Background()
	f.openRegistration(t)
	plan := f.plan(t, "basic", PlanQuota{TrafficLimit: gib, DurationDays: 30})
	if _, err := f.update(t, map[string]any{"register.planId": plan.ID}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	res, err := f.account.Register(ctx, registerInput("alice"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	f.reconcile(t)
	clientEmail := f.emailOf(t, res.Principal.UserID)
	f.panelClient(t, clientEmail)
	p := f.principalOf(t, res)

	if err := f.account.DeleteOwnAccount(ctx, p, bad, "", nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("wrong secret = %v", err)
	}
	if err := f.account.DeleteOwnAccount(ctx, p, cred, "", nil); err != nil {
		t.Fatalf("DeleteOwnAccount: %v", err)
	}
	if _, err := f.portal.Authenticate(ctx, res.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("session after account deletion = %v", err)
	}
	if _, ok := f.fleet.get(fakeBase).ClientSnapshot(clientEmail); ok {
		t.Fatal("panel client must be removed after account deletion")
	}
	// 原用户名与邮箱随即可重新注册（docs/03 §5.8 注销账号）
	again, err := f.account.Register(ctx, registerInput("alice"))
	if err != nil {
		t.Fatalf("re-register the same username and email: %v", err)
	}
	if again.Principal.UserID == res.Principal.UserID {
		t.Fatal("re-registration must create a new user")
	}
}

func TestAdminSetsAndClearsUserEmail(t *testing.T) {
	f := newAccountFixture(t)
	ctx := context.Background()
	in := UserCreateInput{Username: "alice", Email: " Alice@Example.com "}
	in.Password = cred
	u, err := f.users.CreateUser(ctx, f.actor, in, nil)
	if err != nil || u.Email != "alice@example.com" || !u.EmailVerified {
		t.Fatalf("CreateUser with email = %+v, %v", u, err)
	}
	dup := UserCreateInput{Username: "bob", Email: "alice@example.com"}
	dup.Password = cred
	if _, err := f.users.CreateUser(ctx, f.actor, dup, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate email = %v, want conflict", err)
	}
	withAt := UserCreateInput{Username: "carol@example.com"}
	withAt.Password = cred
	if _, err := f.users.CreateUser(ctx, f.actor, withAt, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("new username with @ = %v, want invalid_argument", err)
	}
	bob := f.newUser(t, "bob", 0)
	taken := "alice@example.com"
	if _, err := f.users.UpdateUser(ctx, f.actor, bob.ID, UserUpdateInput{Email: &taken}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("update to a taken email = %v, want conflict", err)
	}
	// 省略不改；空串清除
	if got, err := f.users.UpdateUser(ctx, f.actor, u.ID, UserUpdateInput{Remark: "vip"}, nil); err != nil || got.Email != "alice@example.com" {
		t.Fatalf("update without email = %+v, %v", got, err)
	}
	empty := ""
	if got, err := f.users.UpdateUser(ctx, f.actor, u.ID, UserUpdateInput{Remark: "vip", Email: &empty}, nil); err != nil || got.Email != "" || got.EmailVerified {
		t.Fatalf("clear email = %+v, %v", got, err)
	}
	if got, err := f.users.UpdateUser(ctx, f.actor, bob.ID, UserUpdateInput{Email: &taken}, nil); err != nil || got.Email != taken {
		t.Fatalf("reuse a cleared email = %+v, %v", got, err)
	}
}
