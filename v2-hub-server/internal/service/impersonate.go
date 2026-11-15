package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/crypto"
)

// 切换为用户身份（docs/03 §5.17，Ruling-33）：管理端与客户门户是两个域名，Cookie 不能跨域下发，
// 所以由管理端签发一次性票据、客户门户兑换成只读的代入会话。

const (
	// 票据 ≥ 128 bit 随机，60 秒内有效；库里只存 SHA-256
	impersonateTicketBytes = 32
	impersonateTicketTTL   = time.Minute
	// 代入会话绝对有效期 30 分钟，不续期
	impersonateSessionMaxAge = 30 * time.Minute
	impersonateFailsPerHour  = 20
	minImpersonateTicketLen  = 32
	maxImpersonateTicketLen  = 128
	guardKeyImpersonate      = "impersonate-ip:"

	ActionImpersonateStart = "user.impersonate_start"
	ActionImpersonateEnd   = "user.impersonate_end"

	msgImpersonateDisabled = "用户已停用，不能切换为该用户身份"
	msgImpersonateInvalid  = "票据无效或已过期"
	msgImpersonateTooOften = "尝试次数过多，请稍后再试"
)

// ImpersonateTicket 是签发给管理端的一次性票据；Ticket 属于凭据，不得写日志。
type ImpersonateTicket struct {
	Ticket    string
	ExpiresAt time.Time
}

// IssueImpersonateTicket 给管理员签发切换为某个用户身份的票据（需 impersonate:write）。
// 用户不存在返回 CodeNotFound；已停用返回 CodeConflict。签发即记审计。
func (s *PortalAuthService) IssueImpersonateTicket(ctx context.Context, actor *AdminPrincipal, userID int64, ip *netip.Addr) (*ImpersonateTicket, error) {
	ticket, err := crypto.RandomToken(impersonateTicketBytes)
	if err != nil {
		return nil, fmt.Errorf("generate impersonate ticket: %w", err)
	}
	now := s.now()
	expiresAt := now.Add(impersonateTicketTTL)
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		u, err := q.GetUserForImpersonate(ctx, userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return newError(CodeNotFound, msgUserNotFound)
		}
		if err != nil {
			return fmt.Errorf("get user %d: %w", userID, err)
		}
		if u.IsDisabled {
			return newError(CodeConflict, msgImpersonateDisabled)
		}
		err = q.InsertImpersonateTicket(ctx, sqlcgen.InsertImpersonateTicketParams{
			TicketHash: crypto.SHA256(ticket), AdminID: actor.AdminID, UserID: userID,
			ExpiresAt: timestamptz(expiresAt), CreatedAt: timestamptz(now),
		})
		if err != nil {
			return fmt.Errorf("insert impersonate ticket: %w", err)
		}
		return writeAudit(ctx, q, adminAudit(actor, ActionImpersonateStart, targetUser, userID, ip, map[string]any{"username": u.Username}))
	})
	if err != nil {
		return nil, err
	}
	return &ImpersonateTicket{Ticket: ticket, ExpiresAt: expiresAt}, nil
}

// RedeemImpersonateTicket 用票据换取只读的代入会话。票据无效、已用、过期或用户已不可代入一律 CodeInvalidArgument、不区分原因；
// 同一 IP 失败过多返回 CodeRateLimited。代入会话的登录不写入用户的“最近登录”。
func (s *PortalAuthService) RedeemImpersonateTicket(ctx context.Context, ticket string, ip *netip.Addr, userAgent string) (*PortalLoginResult, error) {
	key := ipKey(guardKeyImpersonate, ip)
	if key != "" && s.ticketGuard.IsLocked(key) {
		return nil, newError(CodeRateLimited, msgImpersonateTooOften)
	}
	invalid := func() (*PortalLoginResult, error) {
		if key != "" {
			s.ticketGuard.RecordFailure(key)
		}
		return nil, newError(CodeInvalidArgument, msgImpersonateInvalid)
	}
	if len(ticket) < minImpersonateTicketLen || len(ticket) > maxImpersonateTicketLen {
		return invalid()
	}
	now := s.now()
	var res *PortalLoginResult
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := q.GetImpersonateTicketForUpdate(ctx, crypto.SHA256(ticket))
		if errors.Is(err, pgx.ErrNoRows) {
			return newError(CodeInvalidArgument, msgImpersonateInvalid)
		}
		if err != nil {
			return fmt.Errorf("get impersonate ticket: %w", err)
		}
		if row.UsedAt.Valid || !now.Before(row.ExpiresAt.Time) {
			return newError(CodeInvalidArgument, msgImpersonateInvalid)
		}
		u, err := q.GetUserForImpersonate(ctx, row.UserID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && u.IsDisabled) {
			return newError(CodeInvalidArgument, msgImpersonateInvalid)
		}
		if err != nil {
			return fmt.Errorf("get user %d: %w", row.UserID, err)
		}
		if err := q.MarkImpersonateTicketUsed(ctx, sqlcgen.MarkImpersonateTicketUsedParams{ID: row.ID, UsedAt: timestamptz(now)}); err != nil {
			return fmt.Errorf("mark impersonate ticket %d used: %w", row.ID, err)
		}
		principal := &UserPrincipal{
			UserID: u.ID, Username: u.Username, Status: u.Status, IsDisabled: u.IsDisabled, TOTPEnabled: u.TotpEnabled,
			Email: u.Email.String, EmailVerified: u.IsEmailVerified, ImpersonatorAdminID: row.AdminID,
		}
		// 空闲期与绝对期相同：不靠活动续期，到点即失效
		res, err = s.newSession(ctx, q, principal, ip, userAgent, impersonateSessionMaxAge, impersonateSessionMaxAge)
		return err
	})
	if errors.Is(err, ErrInvalidArgument) {
		return invalid()
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}
