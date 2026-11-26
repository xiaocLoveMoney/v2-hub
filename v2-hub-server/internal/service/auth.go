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
)

const (
	sessionTokenBytes   = 32 // 256 bit（docs/03 §4.2）
	csrfTokenBytes      = 32
	maxUserAgentLen     = 255
	maxAuditUsernameLen = 64
	guardKeyUserPrefix  = "u:"
	guardKeyIPPrefix    = "ip:"
	// guardKeyReauthPrefix / guardKeyPasswordPrefix：二次验证与本人改密码各自计数，不与登录共用（Ruling-96、Ruling-100）
	guardKeyReauthPrefix   = "reauth:"
	guardKeyPasswordPrefix = "admin-password:"
	reauthMethodTOTP       = "totp"
	reauthMethodPassword   = "password"
	msgReauthTOTPMissing   = "该账号已开启两步验证，请输入验证码"
	msgReauthPassMissing   = "请输入密码"
	msgReauthPassInvalid   = "密码错误"
	msgReauthTooMany       = "验证失败次数过多，请稍后再试"
	msgOldPasswordInvalid  = "旧密码不正确"
	// dummyPassword 只用于给不存在的用户名也跑一次 argon2，抹平响应时间差，防止枚举用户名。
	dummyPassword = "v2-hub-timing-equalizer"
)

// AuthConfig 是会话与登录频控参数（默认值见 config 包，语义见 docs/03 §4.2）。
type AuthConfig struct {
	SessionIdleTimeout   time.Duration
	SessionMaxAge        time.Duration
	SessionTouchInterval time.Duration
	LoginMaxFailures     int
	LoginWindow          time.Duration
	LoginLockout         time.Duration
	LoginMaxTrackedKeys  int
	// ReauthWindow 是二次验证的有效期（配置 auth.reauthWindow，R12）。
	ReauthWindow time.Duration
}

// AuthService 负责管理员登录、会话校验与登出。
type AuthService struct {
	store
	cfg       AuthConfig
	sealer    *crypto.Sealer
	guard     *ratelimit.FailureGuard
	now       func() time.Time
	dummyHash string
}

// NewAuthService 创建认证服务；sealer 用于加解密 TOTP 密钥；now 为 nil 时用 time.Now。
func NewAuthService(pool *pgxpool.Pool, cfg AuthConfig, sealer *crypto.Sealer, now func() time.Time) (*AuthService, error) {
	if now == nil {
		now = time.Now
	}
	dummy, err := crypto.HashPassword(dummyPassword)
	if err != nil {
		return nil, fmt.Errorf("init auth service: %w", err)
	}
	guard := ratelimit.NewFailureGuard(ratelimit.FailureGuardConfig{
		MaxFailures: cfg.LoginMaxFailures,
		Window:      cfg.LoginWindow,
		Lockout:     cfg.LoginLockout,
		MaxKeys:     cfg.LoginMaxTrackedKeys,
	}, now)
	return &AuthService{store: newStore(pool), cfg: cfg, sealer: sealer, guard: guard, now: now, dummyHash: dummy}, nil
}

// LoginInput 是登录请求。
type LoginInput struct {
	Username  string
	Password  string
	TOTPCode  string // 已开启两步验证的账号必填（docs/03 §4.3）
	ClientIP  *netip.Addr
	UserAgent string
}

// LoginResult 是登录成功的结果；Token 只放进 Cookie，不得写日志。
type LoginResult struct {
	Token     string
	ExpiresAt time.Time
	Principal *AdminPrincipal
}

// Login 校验用户名密码并创建会话（docs/03 §4.2）。
func (s *AuthService) Login(ctx context.Context, in LoginInput) (*LoginResult, error) {
	userKey := guardKeyUserPrefix + strings.ToLower(in.Username)
	ipKey := ""
	if in.ClientIP != nil {
		ipKey = guardKeyIPPrefix + in.ClientIP.String()
	}
	if s.guard.IsLocked(userKey) || (ipKey != "" && s.guard.IsLocked(ipKey)) {
		return nil, newError(CodeRateLimited, msgTooManyLogin)
	}

	admin, verified, err := s.verifyCredentials(ctx, in.Username, in.Password)
	if err != nil {
		return nil, err
	}
	failure := func(reason, message string) error {
		s.guard.RecordFailure(userKey)
		if ipKey != "" {
			s.guard.RecordFailure(ipKey)
		}
		s.auditLoginFailure(ctx, in, admin, reason)
		return newError(CodeUnauthenticated, message)
	}
	if !verified {
		return nil, failure("credentials", msgBadCredentials)
	}
	if admin.TotpSecretEnc != nil {
		// 密码已正确：未带验证码时提示补交，不计失败（docs/03 §4.3）
		if in.TOTPCode == "" {
			return nil, newError(CodeTOTPRequired, msgTOTPRequired)
		}
		ok, err := s.consumeTOTP(ctx, admin.ID, admin.TotpSecretEnc, admin.TotpLastStep, in.TOTPCode)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, failure("totp", msgTOTPInvalid)
		}
	}
	s.guard.Reset(userKey)
	if ipKey != "" {
		s.guard.Reset(ipKey)
	}

	token, err := crypto.RandomToken(sessionTokenBytes)
	if err != nil {
		return nil, err
	}
	csrf, err := crypto.RandomToken(csrfTokenBytes)
	if err != nil {
		return nil, err
	}
	now := s.now()
	expiresAt := now.Add(s.cfg.SessionMaxAge)
	var principal *AdminPrincipal
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		sessionID, err := q.CreateAdminSession(ctx, sqlcgen.CreateAdminSessionParams{
			TokenHash: crypto.SHA256(token),
			AdminID:   admin.ID,
			CsrfToken: csrf,
			ClientIp:  in.ClientIP,
			UserAgent: pgtype.Text{String: truncate(in.UserAgent, maxUserAgentLen), Valid: in.UserAgent != ""},
			ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
			IdleUntil: pgtype.Timestamptz{Time: now.Add(s.cfg.SessionIdleTimeout), Valid: true},
			// 登录成功视为刚做过二次验证（R12）
			ReauthUntil: pgtype.Timestamptz{Time: now.Add(s.cfg.ReauthWindow), Valid: true},
		})
		if err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		if err := q.UpdateAdminLastLogin(ctx, admin.ID); err != nil {
			return fmt.Errorf("update last login: %w", err)
		}
		p, err := loadPrincipal(ctx, q, admin.ID, admin.Username, admin.Kind, principalSession{
			SessionID: sessionID, CSRFToken: csrf, TOTPEnabled: admin.TotpSecretEnc != nil, IsOwner: admin.IsOwner, ReauthFresh: true,
		})
		if err != nil {
			return err
		}
		principal = p
		return writeAudit(ctx, q, adminAudit(p, ActionAdminLogin, targetAdmin, admin.ID, in.ClientIP, nil))
	})
	if err != nil {
		return nil, err
	}
	return &LoginResult{Token: token, ExpiresAt: expiresAt, Principal: principal}, nil
}

// verifyCredentials 返回 (管理员行, 是否通过, 系统错误)。用户名不存在时仍跑一次 argon2。
func (s *AuthService) verifyCredentials(ctx context.Context, username, password string) (*sqlcgen.GetAdminByUsernameRow, bool, error) {
	row, err := s.q.GetAdminByUsername(ctx, username)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, verr := crypto.VerifyPassword(password, s.dummyHash); verr != nil {
			return nil, false, fmt.Errorf("verify dummy hash: %w", verr)
		}
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get admin by username: %w", err)
	}
	ok, err := crypto.VerifyPassword(password, row.PasswordHash)
	if err != nil {
		return nil, false, fmt.Errorf("verify password of admin %d: %w", row.ID, err)
	}
	// 禁用账号与密码错误返回同样结果，不暴露账号状态（docs/03 §4.2）
	return &row, ok && !row.IsDisabled, nil
}

func (s *AuthService) auditLoginFailure(ctx context.Context, in LoginInput, admin *sqlcgen.GetAdminByUsernameRow, reason string) {
	entry := AuditEntry{
		ActorType: ActorAdmin, Action: ActionAdminLoginFailed, ClientIP: in.ClientIP,
		Detail: map[string]any{"username": truncate(in.Username, maxAuditUsernameLen), "reason": reason},
	}
	if admin != nil {
		entry.ActorID = admin.ID
	}
	// 审计写失败不影响登录结果，但必须留痕
	if err := writeAudit(ctx, s.q, entry); err != nil {
		slog.Error("audit login failure", "err", err)
	}
}

// Authenticate 用 Cookie 里的会话 token 换取管理员身份；无效、过期、账号禁用均返回 unauthenticated。
func (s *AuthService) Authenticate(ctx context.Context, token string) (*AdminPrincipal, error) {
	if token == "" {
		return nil, newError(CodeUnauthenticated, msgLoginRequired)
	}
	row, err := s.q.GetAdminSessionByTokenHash(ctx, crypto.SHA256(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, newError(CodeUnauthenticated, msgLoginRequired)
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	now := s.now()
	if row.IsDisabled || !now.Before(row.ExpiresAt.Time) || !now.Before(row.IdleUntil.Time) {
		if err := s.q.DeleteAdminSessionByID(ctx, row.ID); err != nil {
			slog.Warn("delete invalid session", "session_id", row.ID, "err", err)
		}
		return nil, newError(CodeUnauthenticated, msgLoginRequired)
	}
	// 空闲期顺延写库节流：剩余空闲时间少于 (idle - touchInterval) 时才更新
	if row.IdleUntil.Time.Sub(now) < s.cfg.SessionIdleTimeout-s.cfg.SessionTouchInterval {
		idle := pgtype.Timestamptz{Time: now.Add(s.cfg.SessionIdleTimeout), Valid: true}
		if err := s.q.TouchAdminSession(ctx, sqlcgen.TouchAdminSessionParams{ID: row.ID, IdleUntil: idle}); err != nil {
			return nil, fmt.Errorf("touch session %d: %w", row.ID, err)
		}
	}
	return loadPrincipal(ctx, s.q, row.AdminID, row.Username, row.Kind, principalSession{
		SessionID: row.ID, CSRFToken: row.CsrfToken, TOTPEnabled: row.TotpEnabled, IsOwner: row.IsOwner,
		// 有效期为空（存量会话、迁移前登录）或已过期都要重新验证（docs/03 §4.4）
		ReauthFresh: row.ReauthUntil.Valid && now.Before(row.ReauthUntil.Time),
	})
}

// Logout 删除当前会话。
func (s *AuthService) Logout(ctx context.Context, p *AdminPrincipal, clientIP *netip.Addr) error {
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.DeleteAdminSessionByID(ctx, p.SessionID); err != nil {
			return fmt.Errorf("delete session %d: %w", p.SessionID, err)
		}
		return writeAudit(ctx, q, adminAudit(p, ActionAdminLogout, targetAdmin, p.AdminID, clientIP, nil))
	})
}

// PurgeExpiredSessions 删除已过期会话，返回删除条数（定时任务调用）。
func (s *AuthService) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	n, err := s.q.DeleteExpiredAdminSessions(ctx, pgtype.Timestamptz{Time: s.now(), Valid: true})
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return n, nil
}

// principalSession 是拼装 AdminPrincipal 时与权限无关的会话与账号状态，由 Login / Authenticate 各自从会话行取得。
type principalSession struct {
	SessionID   int64
	CSRFToken   string
	TOTPEnabled bool
	IsOwner     bool
	ReauthFresh bool
}

// loadPrincipal 拼装完整的管理员身份：会话状态取自 sess，有效权限按持有的角色重新计算（R1，每个请求都重新加载）：
// 内置角色只在 is_builtin 为真时按编码取代码定义的权限，自定义角色读 role_permission，多个角色取并集，非超管再剔除超管专属码（Ruling-95）。
// 代理（kind = agent）的权限集合恒为空、不是超管，库里即使有角色关联行也不读（Ruling-101）；kind 不认识时按代理处理（默认拒绝）。
func loadPrincipal(ctx context.Context, q *sqlcgen.Queries, adminID int64, username, kind string, sess principalSession) (*AdminPrincipal, error) {
	p := &AdminPrincipal{
		AdminID: adminID, Username: username, Kind: AdminKindAgent, SessionID: sess.SessionID, CSRFToken: sess.CSRFToken,
		IsOwner: sess.IsOwner, TOTPEnabled: sess.TOTPEnabled, ReauthFresh: sess.ReauthFresh, Permissions: PermissionSet{},
	}
	if kind != AdminKindStaff {
		return p, nil
	}
	p.Kind = AdminKindStaff
	roles, err := q.ListRolesForAdmin(ctx, adminID)
	if err != nil {
		return nil, fmt.Errorf("list roles of admin %d: %w", adminID, err)
	}
	if hasSuperAdminRole(roles) {
		// 超管的权限集固定为全部权限，不必再查权限码
		p.IsSuperAdmin, p.Permissions = true, NewPermissionSet(AllPermissions)
		return p, nil
	}
	var codes []string
	hasCustomRole := false
	for _, r := range roles {
		if !r.IsBuiltin {
			hasCustomRole = true
			continue
		}
		if builtin, ok := builtinRolePermissions(r.Code); ok {
			for code := range builtin {
				codes = append(codes, code)
			}
		}
	}
	// 权限码查询只取自定义角色的行（查询里已排除内置角色），没有自定义角色时结果恒为空，不必查
	if hasCustomRole {
		custom, err := q.ListPermissionCodesForAdmin(ctx, adminID)
		if err != nil {
			return nil, fmt.Errorf("list permissions of admin %d: %w", adminID, err)
		}
		codes = append(codes, custom...)
	}
	// 运行时兜底剔除超管专属码，覆盖手工改库（R1）
	p.Permissions = withoutSuperOnly(codes)
	return p, nil
}

// Reauth 做一次二次验证（R12，docs/03 §4.4）：已开启两步验证的账号验验证码（防重放），否则验密码。
// 成功后把当前会话的有效期设为 now + ReauthWindow 并返回该时刻。
// 错误：CodeInvalidArgument（验证失败或缺字段，失败计入频控）、CodeRateLimited。
func (s *AuthService) Reauth(ctx context.Context, p *AdminPrincipal, password, totpCode string, ip *netip.Addr) (time.Time, error) {
	guardKey := guardKeyReauthPrefix + fmt.Sprint(p.AdminID)
	if s.guard.IsLocked(guardKey) {
		return time.Time{}, newError(CodeRateLimited, msgReauthTooMany)
	}
	row, err := s.q.GetAdminSecrets(ctx, p.AdminID)
	if err != nil {
		return time.Time{}, fmt.Errorf("get secrets of admin %d: %w", p.AdminID, err)
	}
	method, ok, err := s.verifyReauth(ctx, p.AdminID, row, password, totpCode)
	if err != nil {
		return time.Time{}, err
	}
	if !ok {
		s.guard.RecordFailure(guardKey)
		entry := adminAudit(p, ActionAdminReauthFailed, targetAdmin, p.AdminID, ip, map[string]any{"method": method})
		// 审计写失败不影响验证结果，但必须留痕
		if aerr := writeAudit(ctx, s.q, entry); aerr != nil {
			slog.Error("audit reauth failure", "admin_id", p.AdminID, "err", aerr)
		}
		if method == reauthMethodTOTP {
			return time.Time{}, newError(CodeInvalidArgument, msgTOTPInvalid)
		}
		return time.Time{}, newError(CodeInvalidArgument, msgReauthPassInvalid)
	}
	s.guard.Reset(guardKey)
	var until time.Time
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		refreshed, err := s.refreshReauth(ctx, q, p.SessionID)
		if err != nil {
			return err
		}
		until = refreshed
		return writeAudit(ctx, q, adminAudit(p, ActionAdminReauth, targetAdmin, p.AdminID, ip, map[string]any{"method": method}))
	})
	if err != nil {
		return time.Time{}, err
	}
	return until, nil
}

// verifyReauth 按账号是否开启两步验证选择校验方式，返回 (方式, 是否通过, 系统错误)。
// 缺少该方式需要的字段时返回 CodeInvalidArgument，不计入频控。
func (s *AuthService) verifyReauth(ctx context.Context, adminID int64, row sqlcgen.GetAdminSecretsRow, password, totpCode string) (string, bool, error) {
	if row.TotpSecretEnc != nil {
		if totpCode == "" {
			return reauthMethodTOTP, false, newError(CodeInvalidArgument, msgReauthTOTPMissing)
		}
		ok, err := s.consumeTOTP(ctx, adminID, row.TotpSecretEnc, row.TotpLastStep, totpCode)
		return reauthMethodTOTP, ok, err
	}
	if password == "" {
		return reauthMethodPassword, false, newError(CodeInvalidArgument, msgReauthPassMissing)
	}
	ok, err := crypto.VerifyPassword(password, row.PasswordHash)
	if err != nil {
		return reauthMethodPassword, false, fmt.Errorf("verify password of admin %d: %w", adminID, err)
	}
	return reauthMethodPassword, ok, nil
}

// refreshReauth 把会话的二次验证有效期设为 now + ReauthWindow 并返回该时刻（二次验证成功、开启两步验证成功时调用，R12）。
func (s *AuthService) refreshReauth(ctx context.Context, q *sqlcgen.Queries, sessionID int64) (time.Time, error) {
	until := s.now().Add(s.cfg.ReauthWindow)
	if err := q.SetAdminSessionReauth(ctx, sqlcgen.SetAdminSessionReauthParams{
		ID: sessionID, ReauthUntil: pgtype.Timestamptz{Time: until, Valid: true},
	}); err != nil {
		return time.Time{}, fmt.Errorf("set reauth of session %d: %w", sessionID, err)
	}
	return until, nil
}

// ChangeOwnPassword 本人改密码（R9，Ruling-96）：须提交旧密码，旧密码错误计入频控；成功后当前会话保留、其余失效（R7）。
// 错误：CodeInvalidArgument（新密码不合规或旧密码错误）、CodeRateLimited。
func (s *AuthService) ChangeOwnPassword(ctx context.Context, p *AdminPrincipal, oldPassword, newPassword string, ip *netip.Addr) error {
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	guardKey := guardKeyPasswordPrefix + fmt.Sprint(p.AdminID)
	if s.guard.IsLocked(guardKey) {
		return newError(CodeRateLimited, msgTooManyLogin)
	}
	row, err := s.q.GetAdminSecrets(ctx, p.AdminID)
	if err != nil {
		return fmt.Errorf("get secrets of admin %d: %w", p.AdminID, err)
	}
	ok, err := crypto.VerifyPassword(oldPassword, row.PasswordHash)
	if err != nil {
		return fmt.Errorf("verify password of admin %d: %w", p.AdminID, err)
	}
	if !ok {
		s.guard.RecordFailure(guardKey)
		return newError(CodeInvalidArgument, msgOldPasswordInvalid)
	}
	s.guard.Reset(guardKey)
	hash, err := crypto.HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := q.UpdateAdminPasswordHash(ctx, sqlcgen.UpdateAdminPasswordHashParams{ID: p.AdminID, PasswordHash: hash}); err != nil {
			return fmt.Errorf("update password of admin %d: %w", p.AdminID, err)
		}
		if err := q.DeleteOtherAdminSessions(ctx, sqlcgen.DeleteOtherAdminSessionsParams{AdminID: p.AdminID, KeepID: p.SessionID}); err != nil {
			return fmt.Errorf("revoke other sessions of admin %d: %w", p.AdminID, err)
		}
		return writeAudit(ctx, q, adminAudit(p, ActionAdminPasswordChange, targetAdmin, p.AdminID, ip, nil))
	})
}

func truncate(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes])
}
