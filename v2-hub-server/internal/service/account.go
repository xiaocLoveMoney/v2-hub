package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/crypto"
	"v2-hub-server/internal/manager/ratelimit"
)

// 注册、找回密码、换绑邮箱与注销（docs/03 §5.8）。

const (
	ActionPortalRegister    = "portal.register"
	ActionPortalSecretReset = "portal.password_reset"
	ActionPortalEmailBind   = "portal.email_bind"
	ActionPortalDelete      = "portal.account_delete"

	// 注册频控（docs/03 §5.8）：同一 IP 每小时提交 20 次、成功注册 3 个
	registerAttemptsPerHour  = 20
	registerSuccessesPerHour = 3
	// 找回密码的提交按 IP 限 20 次 / 小时（验证码本身另有 5 次上限）
	resetAttemptsPerHour = 20
	accountGuardWindow   = time.Hour
	accountGuardMaxKeys  = 50000

	guardKeyRegisterTry = "reg-try:"
	guardKeyRegisterOK  = "reg-ok:"
	guardKeyResetTry    = "reset-try:"

	msgRegisterTooOften  = "注册过于频繁，请稍后再试"
	msgResetTooOften     = "操作过于频繁，请稍后再试"
	msgInviteRequired    = "请填写邀请码"
	msgInviteInvalid     = "邀请码无效"
	msgEmailCodeRequired = "请填写邮箱验证码"
	msgEmailUnchanged    = "新邮箱与当前邮箱相同"
)

// accountGuards 是注册与找回密码的按 IP 计数器：记满即在窗口内拒绝。
type accountGuards struct {
	registerTry *ratelimit.FailureGuard
	registerOK  *ratelimit.FailureGuard
	resetTry    *ratelimit.FailureGuard
	inviteReset *ratelimit.FailureGuard
}

func newAccountGuards(now func() time.Time) accountGuards {
	mk := func(limit int) *ratelimit.FailureGuard {
		return ratelimit.NewFailureGuard(ratelimit.FailureGuardConfig{
			MaxFailures: limit, Window: accountGuardWindow, Lockout: accountGuardWindow, MaxKeys: accountGuardMaxKeys,
		}, now)
	}
	return accountGuards{registerTry: mk(registerAttemptsPerHour), registerOK: mk(registerSuccessesPerHour), resetTry: mk(resetAttemptsPerHour),
		inviteReset: mk(inviteResetsPerHour)}
}

// RegisterInput 是自助注册的输入。
type RegisterInput struct {
	Username   string
	Email      string
	Password   string
	InviteCode string
	EmailCode  string
	ClientIP   *netip.Addr
	UserAgent  string
}

func ipKey(prefix string, ip *netip.Addr) string {
	if ip == nil {
		return ""
	}
	return prefix + ip.String()
}

// Register 自助注册并直接登录（docs/03 §5.8 注册结果）。
// 错误：CodeFeatureDisabled（注册关闭）、CodeInvalidArgument（字段、域名、邀请码或验证码不合法）、
// CodeConflict（用户名或邮箱已被占用）、CodeRateLimited。
func (s *AccountService) Register(ctx context.Context, in RegisterInput) (*PortalLoginResult, error) {
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return nil, err
	}
	if cur.Register.Mode == RegisterClosed {
		return nil, newError(CodeFeatureDisabled, msgRegisterClosed)
	}
	tryKey, okKey := ipKey(guardKeyRegisterTry, in.ClientIP), ipKey(guardKeyRegisterOK, in.ClientIP)
	if tryKey != "" {
		if s.guards.registerTry.IsLocked(tryKey) || s.guards.registerOK.IsLocked(okKey) {
			return nil, newError(CodeRateLimited, msgRegisterTooOften)
		}
		s.guards.registerTry.RecordFailure(tryKey)
	}

	username := strings.TrimSpace(in.Username)
	if !endUserNamePattern.MatchString(username) {
		return nil, newError(CodeInvalidArgument, msgBadUsername)
	}
	if err := validateUserLoginSecret(in.Password); err != nil {
		return nil, err
	}
	email := normalizeEmail(in.Email)
	if email == "" {
		return nil, newError(CodeInvalidArgument, msgMailBadAddress)
	}
	if !emailDomainAllowed(cur.Register, email) {
		return nil, newError(CodeInvalidArgument, msgEmailDomainBlocked)
	}
	invite := normalizeInviteCode(in.InviteCode)
	if in.InviteCode != "" && invite == "" {
		return nil, newError(CodeInvalidArgument, msgInviteInvalid)
	}
	if invite == "" && cur.Register.Mode == RegisterInvite {
		return nil, newError(CodeInvalidArgument, msgInviteRequired)
	}
	var emailCodeID int64
	if s.emailVerifyRequired(cur) {
		if in.EmailCode == "" {
			return nil, newError(CodeInvalidArgument, msgEmailCodeRequired)
		}
		if emailCodeID, err = s.checkEmailCode(ctx, email, EmailCodeRegister, in.EmailCode); err != nil {
			return nil, err
		}
	}
	hash, err := crypto.HashPassword(in.Password)
	if err != nil {
		return nil, fmt.Errorf("hash user login secret: %w", err)
	}

	var res *PortalLoginResult
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		spec := newUserSpec{Username: username, LoginHash: hash, Email: email, EmailVerified: emailCodeID != 0}
		if invite != "" {
			use, err := s.useInviteCode(ctx, q, cur, invite)
			if err != nil {
				return err
			}
			spec.InviteCodeID, spec.InvitedBy, spec.AgentID = use.CodeID, use.InviterID, use.AgentID
		}
		// 注册时就有归属的用户不送注册赠送套餐，由代理分配（Ruling-102）
		if spec.AgentID == 0 {
			if spec.PlanID, err = s.registerPlanID(ctx, q, cur); err != nil {
				return err
			}
		}
		now := s.now()
		id, err := s.users.createUserTx(ctx, q, spec, now)
		if err != nil {
			return err
		}
		if emailCodeID != 0 {
			if err := s.consumeEmailCode(ctx, q, emailCodeID); err != nil {
				return err
			}
		}
		user, err := q.GetEndUserForLogin(ctx, username)
		if err != nil {
			return fmt.Errorf("reload registered user %d: %w", id, err)
		}
		principal := &UserPrincipal{UserID: id, Username: username, Status: user.Status, Email: email, EmailVerified: emailCodeID != 0}
		if res, err = s.portal.createSessionTx(ctx, q, principal, in.ClientIP, in.UserAgent); err != nil {
			return err
		}
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorUser, ActorID: id, Action: ActionPortalRegister, TargetType: targetUser,
			TargetID: fmt.Sprint(id), ClientIP: in.ClientIP,
			Detail: map[string]any{"username": username, "inviteCodeId": spec.InviteCodeID, "planId": spec.PlanID, "agentId": spec.AgentID}})
	})
	if err != nil {
		return nil, err
	}
	if okKey != "" {
		s.guards.registerOK.RecordFailure(okKey)
	}
	s.users.TriggerReconcile()
	return res, nil
}

// registerPlanID 返回注册即分配的套餐；设置里的套餐事后被删时不阻塞注册，只是不送并留痕。
func (s *AccountService) registerPlanID(ctx context.Context, q *sqlcgen.Queries, cur Settings) (int64, error) {
	if cur.Register.PlanID == 0 {
		return 0, nil
	}
	ok, err := q.ExistsPlan(ctx, cur.Register.PlanID)
	if err != nil {
		return 0, fmt.Errorf("check register plan: %w", err)
	}
	if !ok {
		slog.Warn("register plan no longer exists; registering without a plan", "plan_id", cur.Register.PlanID)
		return 0, nil
	}
	return cur.Register.PlanID, nil
}

// ResetLoginSecret 凭邮箱验证码重设密码并让该用户全部会话失效；不关闭两步验证（Ruling-23）。
// 错误：CodeFeatureDisabled（邮件未配置）、CodeInvalidArgument（密码或验证码不合法，邮箱不存在时同样提示验证码错误）、CodeRateLimited。
func (s *AccountService) ResetLoginSecret(ctx context.Context, rawEmail, code, newSecret string, ip *netip.Addr) error {
	if err := s.mail.requireEnabled(); err != nil {
		return err
	}
	if key := ipKey(guardKeyResetTry, ip); key != "" {
		if s.guards.resetTry.IsLocked(key) {
			return newError(CodeRateLimited, msgResetTooOften)
		}
		s.guards.resetTry.RecordFailure(key)
	}
	if err := validateUserLoginSecret(newSecret); err != nil {
		return err
	}
	email := normalizeEmail(rawEmail)
	if email == "" {
		return newError(CodeInvalidArgument, msgEmailCodeInvalid)
	}
	codeID, err := s.checkEmailCode(ctx, email, EmailCodeReset, code)
	if err != nil {
		return err
	}
	hash, err := crypto.HashPassword(newSecret)
	if err != nil {
		return fmt.Errorf("hash user login secret: %w", err)
	}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		id, err := q.GetEndUserIDByEmail(ctx, email)
		if errors.Is(err, pgx.ErrNoRows) {
			return newError(CodeInvalidArgument, msgEmailCodeInvalid)
		}
		if err != nil {
			return fmt.Errorf("get user by email: %w", err)
		}
		if err := s.consumeEmailCode(ctx, q, codeID); err != nil {
			return err
		}
		if _, err := q.SetUserPasswordHash(ctx, sqlcgen.SetUserPasswordHashParams{ID: id, PasswordHash: hash}); err != nil {
			return fmt.Errorf("save login secret of user %d: %w", id, err)
		}
		if err := q.DeleteUserSessionsByUser(ctx, id); err != nil {
			return fmt.Errorf("revoke sessions of user %d: %w", id, err)
		}
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorUser, ActorID: id, Action: ActionPortalSecretReset, TargetType: targetUser,
			TargetID: fmt.Sprint(id), ClientIP: ip})
	})
}

// SendBindEmailCode 向本人要绑定的新邮箱发验证码。
// 错误：CodeFeatureDisabled、CodeInvalidArgument、CodeConflict（与当前邮箱相同或已被占用）、CodeRateLimited。
func (s *AccountService) SendBindEmailCode(ctx context.Context, p *UserPrincipal, rawEmail string, ip *netip.Addr) error {
	if err := s.mail.requireEnabled(); err != nil {
		return err
	}
	email, err := s.checkNewEmail(ctx, s.q, p, rawEmail)
	if err != nil {
		return err
	}
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return err
	}
	return s.issueEmailCode(ctx, cur, email, EmailCodeBind, ip, true)
}

func (s *AccountService) checkNewEmail(ctx context.Context, q *sqlcgen.Queries, p *UserPrincipal, rawEmail string) (string, error) {
	email := normalizeEmail(rawEmail)
	if email == "" {
		return "", newError(CodeInvalidArgument, msgMailBadAddress)
	}
	current, err := q.GetEndUserEmail(ctx, p.UserID)
	if err != nil {
		return "", fmt.Errorf("get email of user %d: %w", p.UserID, err)
	}
	if current.String == email {
		return "", newError(CodeConflict, msgEmailUnchanged)
	}
	return email, s.checkEmailFree(ctx, q, email)
}

// BindEmail 绑定或换绑邮箱：当前密码 + 发到新邮箱的验证码。成功后更新 p 的邮箱字段。
// 错误：CodeFeatureDisabled、CodeInvalidArgument（密码或验证码不对）、CodeConflict、CodeRateLimited。
func (s *AccountService) BindEmail(ctx context.Context, p *UserPrincipal, secret, rawEmail, code string, ip *netip.Addr) error {
	if err := s.mail.requireEnabled(); err != nil {
		return err
	}
	if err := s.portal.confirmSecret(ctx, p, secret); err != nil {
		return err
	}
	email := normalizeEmail(rawEmail)
	if email == "" {
		return newError(CodeInvalidArgument, msgMailBadAddress)
	}
	codeID, err := s.checkEmailCode(ctx, email, EmailCodeBind, code)
	if err != nil {
		return err
	}
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := s.checkNewEmail(ctx, q, p, email); err != nil {
			return err
		}
		if err := s.consumeEmailCode(ctx, q, codeID); err != nil {
			return err
		}
		if err := setUserEmailTx(ctx, q, p.UserID, email, s.now()); err != nil {
			return err
		}
		return writeAudit(ctx, q, portalAudit(p, ActionPortalEmailBind, ip))
	})
	if err != nil {
		return err
	}
	p.Email, p.EmailVerified = email, true
	return nil
}

// DeleteOwnAccount 注销本人账号：须提交当前密码，已开两步验证的另须验证码；按删除用户处理（docs/03 §5.8 注销账号）。
// 错误：CodeInvalidArgument（密码或验证码不对）、CodeTOTPRequired、CodeRateLimited。
func (s *AccountService) DeleteOwnAccount(ctx context.Context, p *UserPrincipal, secret, totpCode string, ip *netip.Addr) error {
	if err := s.portal.confirmSecret(ctx, p, secret); err != nil {
		return err
	}
	if err := s.portal.confirmTOTP(ctx, p, totpCode); err != nil {
		return err
	}
	return s.users.deleteUser(ctx, p.UserID, AuditEntry{ActorType: ActorUser, ActorID: p.UserID, Action: ActionPortalDelete, TargetType: targetUser,
		TargetID: fmt.Sprint(p.UserID), ClientIP: ip, Detail: map[string]any{"username": p.Username}})
}
