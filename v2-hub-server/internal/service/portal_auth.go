package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/crypto"
	"v2-hub-server/internal/manager/ratelimit"
	"v2-hub-server/internal/manager/totp"
)

// 终端用户的登录、会话、密码与两步验证（docs/03 §5.3）。规则与管理员侧同构，差异只在有效期、密码长度与停用处理。

const (
	// CodeAccountDisabled：账号已停用，只允许查看本人信息与退出（docs/03 §5）。
	CodeAccountDisabled Code = "account_disabled"

	ActorUser = "user"

	ActionPortalLogin        = "portal.login"
	ActionPortalLogout       = "portal.logout"
	ActionPortalSecretChange = "portal.password_change"
	ActionPortalTOTPEnable   = "portal.totp_enable"
	ActionPortalTOTPDisable  = "portal.totp_disable"
	ActionUserTOTPReset      = "user.totp_reset"

	minUserPasswordLen    = 8
	userLoginLogLimit     = 10
	guardKeyPortalUser    = "pu:"
	guardKeyPortalIP      = "pip:"
	guardKeyPortalTOTP    = "ptotp:"
	guardKeyPortalSecret  = "ppw:"
	msgAccountDisabled    = "账号已停用，请联系管理员"
	msgOldSecretIncorrect = "原密码不正确"
)

// ErrAccountDisabled 供 errors.Is 判定。
var ErrAccountDisabled = &Error{Code: CodeAccountDisabled}

// PortalAuthConfig 是终端用户会话与登录频控参数（docs/03 §5.3）。
type PortalAuthConfig struct {
	SessionIdleTimeout   time.Duration
	SessionMaxAge        time.Duration
	SessionTouchInterval time.Duration
	LoginMaxFailures     int
	LoginWindow          time.Duration
	LoginLockout         time.Duration
	LoginMaxTrackedKeys  int
	// LoginLogRetention 是登录记录的保留时长。
	LoginLogRetention time.Duration
}

// UserPrincipal 是已登录的终端用户。接口只凭它识别身份，不接受请求里的用户 id（R8）。
type UserPrincipal struct {
	UserID      int64
	Username    string
	SessionID   int64
	CSRFToken   string
	Status      string
	IsDisabled  bool
	TOTPEnabled bool
	// Email 为空串表示未绑定。
	Email         string
	EmailVerified bool
	// ImpersonatorAdminID 非 0 表示这是管理员切换为该用户身份的只读会话（docs/03 §5.17）。
	ImpersonatorAdminID int64
}

// Impersonated 表示当前是管理员代入的只读会话：除退出外的写请求一律拒绝（Ruling-33）。
func (p *UserPrincipal) Impersonated() bool {
	return p.ImpersonatorAdminID != 0
}

// PortalLoginResult 是登录成功的结果；Token 只放进 Cookie，不得写日志。
type PortalLoginResult struct {
	Token     string
	ExpiresAt time.Time
	Principal *UserPrincipal
}

// LoginLogItem 是一条登录记录。
type LoginLogItem struct {
	ClientIP  string
	IsSuccess bool
	CreatedAt time.Time
}

// PortalAuthService 负责终端用户登录、会话校验、改密码与两步验证。
type PortalAuthService struct {
	store
	cfg    PortalAuthConfig
	sealer *crypto.Sealer
	guard  *ratelimit.FailureGuard
	// ticketGuard 给兑换代入票据的失败按 IP 计数，防止穷举票据。
	ticketGuard *ratelimit.FailureGuard
	now         func() time.Time
	dummyHash   string
}

// NewPortalAuthService 创建服务；now 为 nil 时用 time.Now。
func NewPortalAuthService(pool *pgxpool.Pool, cfg PortalAuthConfig, sealer *crypto.Sealer, now func() time.Time) (*PortalAuthService, error) {
	if now == nil {
		now = time.Now
	}
	dummy, err := crypto.HashPassword(dummyPassword)
	if err != nil {
		return nil, fmt.Errorf("init portal auth service: %w", err)
	}
	guard := ratelimit.NewFailureGuard(ratelimit.FailureGuardConfig{
		MaxFailures: cfg.LoginMaxFailures,
		Window:      cfg.LoginWindow,
		Lockout:     cfg.LoginLockout,
		MaxKeys:     cfg.LoginMaxTrackedKeys,
	}, now)
	ticketGuard := ratelimit.NewFailureGuard(ratelimit.FailureGuardConfig{
		MaxFailures: impersonateFailsPerHour, Window: time.Hour, Lockout: time.Hour, MaxKeys: cfg.LoginMaxTrackedKeys,
	}, now)
	return &PortalAuthService{store: newStore(pool), cfg: cfg, sealer: sealer, guard: guard, ticketGuard: ticketGuard, now: now, dummyHash: dummy}, nil
}

func userTOTPAAD(userID int64, pending bool) []byte {
	if pending {
		return fmt.Appendf(nil, "end_user:%d:totp-pending", userID)
	}
	return fmt.Appendf(nil, "end_user:%d:totp", userID)
}

func validateUserLoginSecret(pw string) error {
	if n := utf8.RuneCountInString(pw); n < minUserPasswordLen || n > maxPasswordLen {
		return newError(CodeInvalidArgument, fmt.Sprintf("密码长度须为 %d–%d 个字符", minUserPasswordLen, maxPasswordLen))
	}
	return nil
}

// Login 校验用户名密码并创建会话。停用的用户可以登录（只能看到停用提示）。
func (s *PortalAuthService) Login(ctx context.Context, in LoginInput) (*PortalLoginResult, error) {
	userKey := guardKeyPortalUser + strings.ToLower(in.Username)
	ipKey := ""
	if in.ClientIP != nil {
		ipKey = guardKeyPortalIP + in.ClientIP.String()
	}
	if s.guard.IsLocked(userKey) || (ipKey != "" && s.guard.IsLocked(ipKey)) {
		return nil, newError(CodeRateLimited, msgTooManyLogin)
	}
	user, verified, err := s.verifyCredentials(ctx, in.Username, in.Password)
	if err != nil {
		return nil, err
	}
	failure := func(message string) error {
		s.guard.RecordFailure(userKey)
		if ipKey != "" {
			s.guard.RecordFailure(ipKey)
		}
		if user != nil {
			s.recordLogin(ctx, user.ID, in.ClientIP, false)
		}
		return newError(CodeUnauthenticated, message)
	}
	if !verified {
		return nil, failure(msgBadCredentials)
	}
	if user.TotpSecretEnc != nil {
		// 密码已正确：未带验证码时提示补交，不计失败
		if in.TOTPCode == "" {
			return nil, newError(CodeTOTPRequired, msgTOTPRequired)
		}
		ok, err := s.consumeTOTP(ctx, user.ID, user.TotpSecretEnc, user.TotpLastStep, in.TOTPCode)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, failure(msgTOTPInvalid)
		}
	}
	s.guard.Reset(userKey)
	if ipKey != "" {
		s.guard.Reset(ipKey)
	}

	principal := &UserPrincipal{
		UserID: user.ID, Username: user.Username, Status: user.Status, IsDisabled: user.IsDisabled, TOTPEnabled: user.TotpSecretEnc != nil,
		Email: user.Email.String, EmailVerified: user.IsEmailVerified,
	}
	var res *PortalLoginResult
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if res, err = s.createSessionTx(ctx, q, principal, in.ClientIP, in.UserAgent); err != nil {
			return err
		}
		return q.InsertUserLoginLog(ctx, sqlcgen.InsertUserLoginLogParams{UserID: user.ID, ClientIp: in.ClientIP, IsSuccess: true})
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// createSessionTx 在事务里为 principal 建一条会话并填上会话 id 与 CSRF token；登录与自助注册共用。
func (s *PortalAuthService) createSessionTx(ctx context.Context, q *sqlcgen.Queries, principal *UserPrincipal, ip *netip.Addr, userAgent string) (*PortalLoginResult, error) {
	return s.newSession(ctx, q, principal, ip, userAgent, s.cfg.SessionMaxAge, s.cfg.SessionIdleTimeout)
}

// newSession 建一条会话：maxAge 是绝对有效期，idle 是空闲有效期。principal.ImpersonatorAdminID 非 0 时记为代入会话。
func (s *PortalAuthService) newSession(ctx context.Context, q *sqlcgen.Queries, principal *UserPrincipal, ip *netip.Addr, userAgent string, maxAge, idle time.Duration) (*PortalLoginResult, error) {
	token, err := crypto.RandomToken(sessionTokenBytes)
	if err != nil {
		return nil, err
	}
	csrf, err := crypto.RandomToken(csrfTokenBytes)
	if err != nil {
		return nil, err
	}
	now := s.now()
	expiresAt := now.Add(maxAge)
	sid, err := q.CreateUserSession(ctx, sqlcgen.CreateUserSessionParams{
		TokenHash:           crypto.SHA256(token),
		UserID:              principal.UserID,
		CsrfToken:           csrf,
		ClientIp:            ip,
		UserAgent:           pgtype.Text{String: truncate(userAgent, maxUserAgentLen), Valid: userAgent != ""},
		ExpiresAt:           pgtype.Timestamptz{Time: expiresAt, Valid: true},
		IdleUntil:           pgtype.Timestamptz{Time: now.Add(idle), Valid: true},
		ImpersonatorAdminID: pgtype.Int8{Int64: principal.ImpersonatorAdminID, Valid: principal.ImpersonatorAdminID != 0},
	})
	if err != nil {
		return nil, fmt.Errorf("create user session: %w", err)
	}
	principal.SessionID, principal.CSRFToken = sid, csrf
	return &PortalLoginResult{Token: token, ExpiresAt: expiresAt, Principal: principal}, nil
}

// verifyCredentials 返回 (用户行, 是否通过, 系统错误)。用户名不存在时仍跑一次 argon2，防止按响应时间枚举。
func (s *PortalAuthService) verifyCredentials(ctx context.Context, username, password string) (*sqlcgen.GetEndUserForLoginRow, bool, error) {
	row, err := s.q.GetEndUserForLogin(ctx, username)
	if errors.Is(err, pgx.ErrNoRows) && strings.Contains(username, "@") {
		// 先按用户名、再按邮箱（docs/03 §5.3 登录）；两条查询的列一致
		var byEmail sqlcgen.GetEndUserForLoginByEmailRow
		if byEmail, err = s.q.GetEndUserForLoginByEmail(ctx, strings.ToLower(strings.TrimSpace(username))); err == nil {
			row = sqlcgen.GetEndUserForLoginRow(byEmail)
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if _, verr := crypto.VerifyPassword(password, s.dummyHash); verr != nil {
			return nil, false, fmt.Errorf("verify dummy hash: %w", verr)
		}
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get end user for login: %w", err)
	}
	ok, err := crypto.VerifyPassword(password, row.PasswordHash)
	if err != nil {
		return nil, false, fmt.Errorf("verify password of user %d: %w", row.ID, err)
	}
	return &row, ok, nil
}

// recordLogin 写登录记录；写失败不影响登录结果，但必须留痕。
func (s *PortalAuthService) recordLogin(ctx context.Context, userID int64, ip *netip.Addr, success bool) {
	if err := s.q.InsertUserLoginLog(ctx, sqlcgen.InsertUserLoginLogParams{UserID: userID, ClientIp: ip, IsSuccess: success}); err != nil {
		slog.Error("record user login", "user_id", userID, "err", err)
	}
}

// Authenticate 用 Cookie 里的会话 token 换取用户身份；无效、过期、用户已删除均返回 unauthenticated。
func (s *PortalAuthService) Authenticate(ctx context.Context, token string) (*UserPrincipal, error) {
	if token == "" {
		return nil, newError(CodeUnauthenticated, msgLoginRequired)
	}
	row, err := s.q.GetUserSessionByTokenHash(ctx, crypto.SHA256(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, newError(CodeUnauthenticated, msgLoginRequired)
	}
	if err != nil {
		return nil, fmt.Errorf("get user session: %w", err)
	}
	now := s.now()
	if row.IsDeleted || !now.Before(row.ExpiresAt.Time) || !now.Before(row.IdleUntil.Time) {
		if err := s.q.DeleteUserSessionByID(ctx, row.ID); err != nil {
			slog.Warn("delete invalid user session", "session_id", row.ID, "err", err)
		}
		return nil, newError(CodeUnauthenticated, msgLoginRequired)
	}
	// 空闲期顺延写库节流：剩余空闲时间少于 (idle - touchInterval) 时才更新
	if row.IdleUntil.Time.Sub(now) < s.cfg.SessionIdleTimeout-s.cfg.SessionTouchInterval {
		idle := pgtype.Timestamptz{Time: now.Add(s.cfg.SessionIdleTimeout), Valid: true}
		if err := s.q.TouchUserSession(ctx, sqlcgen.TouchUserSessionParams{ID: row.ID, IdleUntil: idle}); err != nil {
			return nil, fmt.Errorf("touch user session %d: %w", row.ID, err)
		}
	}
	return &UserPrincipal{
		UserID: row.UserID, Username: row.Username, SessionID: row.ID, CSRFToken: row.CsrfToken,
		Status: row.Status, IsDisabled: row.IsDisabled, TOTPEnabled: row.TotpEnabled,
		Email: row.Email.String, EmailVerified: row.IsEmailVerified, ImpersonatorAdminID: row.ImpersonatorAdminID.Int64,
	}, nil
}

// RequireEnabled 对停用的账号返回 account_disabled（docs/03 §5.3）。
func (p *UserPrincipal) RequireEnabled() error {
	if p.IsDisabled {
		return newError(CodeAccountDisabled, msgAccountDisabled)
	}
	return nil
}

// Logout 删除当前会话；代入会话结束时另记一条审计（docs/03 §5.17）。
func (s *PortalAuthService) Logout(ctx context.Context, p *UserPrincipal) error {
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.DeleteUserSessionByID(ctx, p.SessionID); err != nil {
			return fmt.Errorf("delete user session %d: %w", p.SessionID, err)
		}
		if !p.Impersonated() {
			return nil
		}
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorAdmin, ActorID: p.ImpersonatorAdminID, Action: ActionImpersonateEnd, TargetType: targetUser,
			TargetID: fmt.Sprint(p.UserID), Detail: map[string]any{"username": p.Username}})
	})
}

// ChangeLoginSecret 本人改密码：须提交旧密码，成功后其余会话全部失效。
func (s *PortalAuthService) ChangeLoginSecret(ctx context.Context, p *UserPrincipal, oldSecret, newSecret string, ip *netip.Addr) error {
	if err := validateUserLoginSecret(newSecret); err != nil {
		return err
	}
	if err := s.confirmSecret(ctx, p, oldSecret); err != nil {
		return err
	}
	hash, err := crypto.HashPassword(newSecret)
	if err != nil {
		return fmt.Errorf("hash login secret: %w", err)
	}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := q.SetUserPasswordHash(ctx, sqlcgen.SetUserPasswordHashParams{ID: p.UserID, PasswordHash: hash}); err != nil {
			return fmt.Errorf("save login secret of user %d: %w", p.UserID, err)
		}
		if err := q.DeleteOtherUserSessions(ctx, sqlcgen.DeleteOtherUserSessionsParams{UserID: p.UserID, KeepID: p.SessionID}); err != nil {
			return fmt.Errorf("revoke other sessions of user %d: %w", p.UserID, err)
		}
		return writeAudit(ctx, q, portalAudit(p, ActionPortalSecretChange, ip))
	})
}

// confirmSecret 校验本人当前密码，用于改密码、换绑邮箱、注销等敏感操作；连续输错按登录频控锁定。
// 错误：CodeInvalidArgument（密码不对）、CodeRateLimited。
func (s *PortalAuthService) confirmSecret(ctx context.Context, p *UserPrincipal, secret string) error {
	guardKey := guardKeyPortalSecret + fmt.Sprint(p.UserID)
	if s.guard.IsLocked(guardKey) {
		return newError(CodeRateLimited, msgTooManyLogin)
	}
	current, err := s.q.GetEndUserLoginSecretHash(ctx, p.UserID)
	if err != nil {
		return fmt.Errorf("get login secret of user %d: %w", p.UserID, err)
	}
	ok, err := crypto.VerifyPassword(secret, current)
	if err != nil {
		return fmt.Errorf("verify login secret of user %d: %w", p.UserID, err)
	}
	if !ok {
		s.guard.RecordFailure(guardKey)
		return newError(CodeInvalidArgument, msgOldSecretIncorrect)
	}
	s.guard.Reset(guardKey)
	return nil
}

// confirmTOTP 在本人已开启两步验证时校验并消耗一个验证码；未开启时直接通过。
// 错误：CodeTOTPRequired（已开启但没带验证码）、CodeInvalidArgument（验证码错误）、CodeRateLimited。
func (s *PortalAuthService) confirmTOTP(ctx context.Context, p *UserPrincipal, code string) error {
	row, err := s.q.GetUserTOTP(ctx, p.UserID)
	if err != nil {
		return fmt.Errorf("get totp of user %d: %w", p.UserID, err)
	}
	if row.TotpSecretEnc == nil {
		return nil
	}
	if code == "" {
		return newError(CodeTOTPRequired, msgTOTPRequired)
	}
	guardKey := guardKeyPortalTOTP + fmt.Sprint(p.UserID)
	if s.guard.IsLocked(guardKey) {
		return newError(CodeRateLimited, msgTooManyLogin)
	}
	ok, err := s.consumeTOTP(ctx, p.UserID, row.TotpSecretEnc, row.TotpLastStep, code)
	if err != nil {
		return err
	}
	if !ok {
		s.guard.RecordFailure(guardKey)
		return newError(CodeInvalidArgument, msgTOTPInvalid)
	}
	s.guard.Reset(guardKey)
	return nil
}

func portalAudit(p *UserPrincipal, action string, ip *netip.Addr) AuditEntry {
	return AuditEntry{ActorType: ActorUser, ActorID: p.UserID, Action: action, TargetType: targetUser, TargetID: fmt.Sprint(p.UserID), ClientIP: ip}
}

// consumeTOTP 校验验证码并原子推进已用步长（防重放）；返回是否通过。
func (s *PortalAuthService) consumeTOTP(ctx context.Context, userID int64, sealed []byte, lastStep int64, code string) (bool, error) {
	if !validTOTPCode(code) {
		return false, nil
	}
	secret, err := s.sealer.Open(sealed, userTOTPAAD(userID, false))
	if err != nil {
		return false, fmt.Errorf("open totp secret of user %d: %w", userID, err)
	}
	step, ok := totp.Verify(secret, code, s.now(), lastStep)
	if !ok {
		return false, nil
	}
	n, err := s.q.AdvanceUserTOTPStep(ctx, sqlcgen.AdvanceUserTOTPStepParams{ID: userID, Step: step})
	if err != nil {
		return false, fmt.Errorf("advance totp step of user %d: %w", userID, err)
	}
	// 0 行：并发请求已用掉同一步长，按重放拒绝
	return n == 1, nil
}

// SetupTOTP 生成待确认密钥（重复调用覆盖旧的待定密钥）。已开启时返回 409。
func (s *PortalAuthService) SetupTOTP(ctx context.Context, p *UserPrincipal) (*TOTPSetup, error) {
	row, err := s.q.GetUserTOTP(ctx, p.UserID)
	if err != nil {
		return nil, fmt.Errorf("get totp of user %d: %w", p.UserID, err)
	}
	if row.TotpSecretEnc != nil {
		return nil, newError(CodeConflict, msgTOTPAlreadyOn)
	}
	raw, text, err := totp.GenerateSecret()
	if err != nil {
		return nil, err
	}
	sealed, err := s.sealer.Seal(raw, userTOTPAAD(p.UserID, true))
	if err != nil {
		return nil, fmt.Errorf("seal pending totp: %w", err)
	}
	if err := s.q.SetUserTOTPPending(ctx, sqlcgen.SetUserTOTPPendingParams{ID: p.UserID, TotpPendingEnc: sealed}); err != nil {
		return nil, fmt.Errorf("save pending totp of user %d: %w", p.UserID, err)
	}
	return &TOTPSetup{Secret: text, OtpauthURI: totp.URI(totpIssuer, p.Username, text)}, nil
}

// EnableTOTP 用验证码确认待定密钥并开启两步验证。
func (s *PortalAuthService) EnableTOTP(ctx context.Context, p *UserPrincipal, code string, ip *netip.Addr) error {
	if !validTOTPCode(code) {
		return newError(CodeInvalidArgument, msgTOTPCodeMalformed)
	}
	guardKey := guardKeyPortalTOTP + fmt.Sprint(p.UserID)
	if s.guard.IsLocked(guardKey) {
		return newError(CodeRateLimited, msgTooManyLogin)
	}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := q.GetUserTOTP(ctx, p.UserID)
		if err != nil {
			return fmt.Errorf("get totp of user %d: %w", p.UserID, err)
		}
		if row.TotpSecretEnc != nil {
			return newError(CodeConflict, msgTOTPAlreadyOn)
		}
		if row.TotpPendingEnc == nil {
			return newError(CodeConflict, msgTOTPNoPending)
		}
		secret, err := s.sealer.Open(row.TotpPendingEnc, userTOTPAAD(p.UserID, true))
		if err != nil {
			return fmt.Errorf("open pending totp of user %d: %w", p.UserID, err)
		}
		step, ok := totp.Verify(secret, code, s.now(), 0)
		if !ok {
			s.guard.RecordFailure(guardKey)
			return newError(CodeInvalidArgument, msgTOTPInvalid)
		}
		s.guard.Reset(guardKey)
		sealed, err := s.sealer.Seal(secret, userTOTPAAD(p.UserID, false))
		if err != nil {
			return fmt.Errorf("seal totp: %w", err)
		}
		if err := q.EnableUserTOTP(ctx, sqlcgen.EnableUserTOTPParams{ID: p.UserID, TotpSecretEnc: sealed, TotpLastStep: step}); err != nil {
			return fmt.Errorf("enable totp of user %d: %w", p.UserID, err)
		}
		return writeAudit(ctx, q, portalAudit(p, ActionPortalTOTPEnable, ip))
	})
}

// DisableTOTP 本人凭当前验证码关闭两步验证。
func (s *PortalAuthService) DisableTOTP(ctx context.Context, p *UserPrincipal, code string, ip *netip.Addr) error {
	if !validTOTPCode(code) {
		return newError(CodeInvalidArgument, msgTOTPCodeMalformed)
	}
	guardKey := guardKeyPortalTOTP + fmt.Sprint(p.UserID)
	if s.guard.IsLocked(guardKey) {
		return newError(CodeRateLimited, msgTooManyLogin)
	}
	row, err := s.q.GetUserTOTP(ctx, p.UserID)
	if err != nil {
		return fmt.Errorf("get totp of user %d: %w", p.UserID, err)
	}
	if row.TotpSecretEnc == nil {
		return newError(CodeConflict, msgTOTPNotOn)
	}
	ok, err := s.consumeTOTP(ctx, p.UserID, row.TotpSecretEnc, row.TotpLastStep, code)
	if err != nil {
		return err
	}
	if !ok {
		s.guard.RecordFailure(guardKey)
		return newError(CodeInvalidArgument, msgTOTPInvalid)
	}
	s.guard.Reset(guardKey)
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := q.ClearUserTOTP(ctx, p.UserID); err != nil {
			return fmt.Errorf("clear totp of user %d: %w", p.UserID, err)
		}
		return writeAudit(ctx, q, portalAudit(p, ActionPortalTOTPDisable, ip))
	})
}

// ListLoginLogs 返回本人最近的登录记录。
func (s *PortalAuthService) ListLoginLogs(ctx context.Context, p *UserPrincipal) ([]LoginLogItem, error) {
	rows, err := s.q.ListUserLoginLogs(ctx, sqlcgen.ListUserLoginLogsParams{UserID: p.UserID, Limit: userLoginLogLimit})
	if err != nil {
		return nil, fmt.Errorf("list login logs of user %d: %w", p.UserID, err)
	}
	out := make([]LoginLogItem, 0, len(rows))
	for _, r := range rows {
		item := LoginLogItem{IsSuccess: r.IsSuccess, CreatedAt: r.CreatedAt.Time}
		if r.ClientIp != nil {
			item.ClientIP = r.ClientIp.String()
		}
		out = append(out, item)
	}
	return out, nil
}

// PurgeExpiredSessions 删除过期会话与超出保留期的登录记录，返回删除的会话数（定时任务调用）。
func (s *PortalAuthService) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	now := s.now()
	n, err := s.q.DeleteExpiredUserSessions(ctx, pgtype.Timestamptz{Time: now, Valid: true})
	if err != nil {
		return 0, fmt.Errorf("delete expired user sessions: %w", err)
	}
	cutoff := pgtype.Timestamptz{Time: now.Add(-s.cfg.LoginLogRetention), Valid: true}
	if _, err := s.q.DeleteOldUserLoginLogs(ctx, cutoff); err != nil {
		return n, fmt.Errorf("delete old user login logs: %w", err)
	}
	if _, err := s.q.DeleteStaleImpersonateTickets(ctx, pgtype.Timestamptz{Time: now, Valid: true}); err != nil {
		return n, fmt.Errorf("delete stale impersonate tickets: %w", err)
	}
	return n, nil
}

// ClearUserTOTP 由管理员为遗失设备的用户清除两步验证，并使其全部会话失效（docs/03 §5.3）。
func (s *UserService) ClearUserTOTP(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) error {
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.ClearUserTOTP(ctx, id)
		if err != nil {
			return fmt.Errorf("clear totp of user %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgUserNotFound)
		}
		if err := q.DeleteUserSessionsByUser(ctx, id); err != nil {
			return fmt.Errorf("revoke sessions of user %d: %w", id, err)
		}
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionUserTOTPReset, TargetType: targetUser, TargetID: fmt.Sprint(id), ClientIP: ip})
	})
}
