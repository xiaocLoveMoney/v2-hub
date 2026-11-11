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

// Login 校验用户名口令并创建会话（docs/03 §4.2）。
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
		// 口令已正确：未带验证码时提示补交，不计失败（docs/03 §4.3）
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
		if err := q.CreateAdminSession(ctx, sqlcgen.CreateAdminSessionParams{
			TokenHash: crypto.SHA256(token),
			AdminID:   admin.ID,
			CsrfToken: csrf,
			ClientIp:  in.ClientIP,
			UserAgent: pgtype.Text{String: truncate(in.UserAgent, maxUserAgentLen), Valid: in.UserAgent != ""},
			ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
			IdleUntil: pgtype.Timestamptz{Time: now.Add(s.cfg.SessionIdleTimeout), Valid: true},
		}); err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		if err := q.UpdateAdminLastLogin(ctx, admin.ID); err != nil {
			return fmt.Errorf("update last login: %w", err)
		}
		p, err := loadPrincipal(ctx, q, admin.ID, admin.Username)
		if err != nil {
			return err
		}
		p.CSRFToken = csrf
		p.TOTPEnabled = admin.TotpSecretEnc != nil
		principal = p
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: admin.ID, Action: ActionAdminLogin,
			TargetType: targetAdmin, TargetID: fmt.Sprint(admin.ID), ClientIP: in.ClientIP,
		})
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
	// 禁用账号与口令错误返回同样结果，不暴露账号状态（docs/03 §4.2）
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
	p, err := loadPrincipal(ctx, s.q, row.AdminID, row.Username)
	if err != nil {
		return nil, err
	}
	p.SessionID = row.ID
	p.CSRFToken = row.CsrfToken
	p.TOTPEnabled = row.TotpEnabled
	return p, nil
}

// Logout 删除当前会话。
func (s *AuthService) Logout(ctx context.Context, p *AdminPrincipal, clientIP *netip.Addr) error {
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.DeleteAdminSessionByID(ctx, p.SessionID); err != nil {
			return fmt.Errorf("delete session %d: %w", p.SessionID, err)
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: p.AdminID, Action: ActionAdminLogout,
			TargetType: targetAdmin, TargetID: fmt.Sprint(p.AdminID), ClientIP: clientIP,
		})
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

func loadPrincipal(ctx context.Context, q *sqlcgen.Queries, adminID int64, username string) (*AdminPrincipal, error) {
	roles, err := q.ListRoleCodesForAdmin(ctx, adminID)
	if err != nil {
		return nil, fmt.Errorf("list roles of admin %d: %w", adminID, err)
	}
	perms, err := q.ListPermissionCodesForAdmin(ctx, adminID)
	if err != nil {
		return nil, fmt.Errorf("list permissions of admin %d: %w", adminID, err)
	}
	isSuper := false
	for _, r := range roles {
		if r == RoleSuperAdmin {
			isSuper = true
			break
		}
	}
	return &AdminPrincipal{
		AdminID:      adminID,
		Username:     username,
		IsSuperAdmin: isSuper,
		Permissions:  effectivePermissions(isSuper, perms),
	}, nil
}

func truncate(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes])
}
