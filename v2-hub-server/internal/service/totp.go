package service

import (
	"context"
	"fmt"
	"net/netip"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/totp"
)

// TOTP 相关业务（docs/03 §4.3）。

const (
	totpIssuer            = "v2-hub"
	guardKeyTOTPPrefix    = "totp:"
	msgTOTPAlreadyOn      = "两步验证已开启"
	msgTOTPNotOn          = "两步验证未开启"
	msgTOTPNoPending      = "请先生成密钥再确认"
	msgTOTPCodeMalformed  = "验证码须为 6 位数字"
	msgTOTPSuperCannotOff = "超级管理员必须保持两步验证开启"
	msgOwnerProtected     = "所有者只能在服务器上用命令行处理"
)

// TOTPSetup 是开启流程第一步的结果；Secret 只返回这一次。
type TOTPSetup struct {
	Secret     string
	OtpauthURI string
}

// totpAAD 把密文绑定到管理员与用途，防止把一条记录的密文挪用到另一条。
func totpAAD(adminID int64, pending bool) []byte {
	if pending {
		return fmt.Appendf(nil, "admin:%d:totp-pending", adminID)
	}
	return fmt.Appendf(nil, "admin:%d:totp", adminID)
}

func validTOTPCode(code string) bool {
	if len(code) != totp.Digits {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// consumeTOTP 校验验证码并原子推进已用步长（防重放）；返回是否通过。
func (s *AuthService) consumeTOTP(ctx context.Context, adminID int64, sealed []byte, lastStep int64, code string) (bool, error) {
	if !validTOTPCode(code) {
		return false, nil
	}
	secret, err := s.sealer.Open(sealed, totpAAD(adminID, false))
	if err != nil {
		return false, fmt.Errorf("open totp secret of admin %d: %w", adminID, err)
	}
	step, ok := totp.Verify(secret, code, s.now(), lastStep)
	if !ok {
		return false, nil
	}
	n, err := s.q.AdvanceAdminTOTPStep(ctx, sqlcgen.AdvanceAdminTOTPStepParams{ID: adminID, Step: step})
	if err != nil {
		return false, fmt.Errorf("advance totp step of admin %d: %w", adminID, err)
	}
	// 0 行：并发请求已用掉同一步长，按重放拒绝
	return n == 1, nil
}

// SetupTOTP 生成待确认密钥（重复调用覆盖旧的待定密钥）。已开启时返回 409。
func (s *AuthService) SetupTOTP(ctx context.Context, p *AdminPrincipal) (*TOTPSetup, error) {
	row, err := s.q.GetAdminTOTP(ctx, p.AdminID)
	if err != nil {
		return nil, fmt.Errorf("get totp of admin %d: %w", p.AdminID, err)
	}
	if row.TotpSecretEnc != nil {
		return nil, newError(CodeConflict, msgTOTPAlreadyOn)
	}
	raw, text, err := totp.GenerateSecret()
	if err != nil {
		return nil, err
	}
	sealed, err := s.sealer.Seal(raw, totpAAD(p.AdminID, true))
	if err != nil {
		return nil, fmt.Errorf("seal pending totp: %w", err)
	}
	if err := s.q.SetAdminTOTPPending(ctx, sqlcgen.SetAdminTOTPPendingParams{ID: p.AdminID, TotpPendingEnc: sealed}); err != nil {
		return nil, fmt.Errorf("save pending totp of admin %d: %w", p.AdminID, err)
	}
	return &TOTPSetup{Secret: text, OtpauthURI: totp.URI(totpIssuer, p.Username, text)}, nil
}

// EnableTOTP 用验证码确认待定密钥并开启两步验证。
func (s *AuthService) EnableTOTP(ctx context.Context, p *AdminPrincipal, code string, ip *netip.Addr) error {
	if !validTOTPCode(code) {
		return newError(CodeInvalidArgument, msgTOTPCodeMalformed)
	}
	guardKey := guardKeyTOTPPrefix + fmt.Sprint(p.AdminID)
	if s.guard.IsLocked(guardKey) {
		return newError(CodeRateLimited, msgTooManyLogin)
	}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := q.GetAdminTOTP(ctx, p.AdminID)
		if err != nil {
			return fmt.Errorf("get totp of admin %d: %w", p.AdminID, err)
		}
		if row.TotpSecretEnc != nil {
			return newError(CodeConflict, msgTOTPAlreadyOn)
		}
		if row.TotpPendingEnc == nil {
			return newError(CodeConflict, msgTOTPNoPending)
		}
		secret, err := s.sealer.Open(row.TotpPendingEnc, totpAAD(p.AdminID, true))
		if err != nil {
			return fmt.Errorf("open pending totp of admin %d: %w", p.AdminID, err)
		}
		step, ok := totp.Verify(secret, code, s.now(), 0)
		if !ok {
			s.guard.RecordFailure(guardKey)
			return newError(CodeInvalidArgument, msgTOTPInvalid)
		}
		s.guard.Reset(guardKey)
		sealed, err := s.sealer.Seal(secret, totpAAD(p.AdminID, false))
		if err != nil {
			return fmt.Errorf("seal totp: %w", err)
		}
		if err := q.EnableAdminTOTP(ctx, sqlcgen.EnableAdminTOTPParams{ID: p.AdminID, TotpSecretEnc: sealed, TotpLastStep: step}); err != nil {
			return fmt.Errorf("enable totp of admin %d: %w", p.AdminID, err)
		}
		// 开启成功视为刚做过二次验证（R12）：刚用掉的步长不能再拿来验一次
		if _, err := s.refreshReauth(ctx, q, p.SessionID); err != nil {
			return err
		}
		return writeAudit(ctx, q, adminAudit(p, ActionAdminTOTPEnable, targetAdmin, p.AdminID, ip, nil))
	})
}

// DisableTOTP 本人凭当前验证码关闭两步验证；超管不能关闭（R10，409）。
func (s *AuthService) DisableTOTP(ctx context.Context, p *AdminPrincipal, code string, ip *netip.Addr) error {
	if p.IsSuperAdmin {
		return newError(CodeConflict, msgTOTPSuperCannotOff)
	}
	if !validTOTPCode(code) {
		return newError(CodeInvalidArgument, msgTOTPCodeMalformed)
	}
	guardKey := guardKeyTOTPPrefix + fmt.Sprint(p.AdminID)
	if s.guard.IsLocked(guardKey) {
		return newError(CodeRateLimited, msgTooManyLogin)
	}
	row, err := s.q.GetAdminTOTP(ctx, p.AdminID)
	if err != nil {
		return fmt.Errorf("get totp of admin %d: %w", p.AdminID, err)
	}
	if row.TotpSecretEnc == nil {
		return newError(CodeConflict, msgTOTPNotOn)
	}
	ok, err := s.consumeTOTP(ctx, p.AdminID, row.TotpSecretEnc, row.TotpLastStep, code)
	if err != nil {
		return err
	}
	if !ok {
		s.guard.RecordFailure(guardKey)
		return newError(CodeInvalidArgument, msgTOTPInvalid)
	}
	s.guard.Reset(guardKey)
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := q.ClearAdminTOTP(ctx, p.AdminID); err != nil {
			return fmt.Errorf("clear totp of admin %d: %w", p.AdminID, err)
		}
		return writeAudit(ctx, q, adminAudit(p, ActionAdminTOTPDisable, targetAdmin, p.AdminID, ip, nil))
	})
}

// ResetAdminTOTP 为遗失设备的管理员清除两步验证（R3、R4、R11），并使其全部会话失效（R7）。
func (s *RBACService) ResetAdminTOTP(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) error {
	if id == actor.AdminID {
		return newError(CodeConflict, "请在账号安全页用验证码关闭自己的两步验证")
	}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		target, currentRoles, err := getAdminWithRoles(ctx, q, id)
		if err != nil {
			return err
		}
		if target.IsOwner {
			return newError(CodePermissionDenied, msgOwnerProtected)
		}
		if _, err := resolveAssignableRoles(ctx, q, actor, currentRoles); err != nil {
			return err
		}
		return clearAdminTOTPTx(ctx, q, id, adminAudit(actor, ActionAdminTOTPReset, targetAdmin, id, ip, nil))
	})
}

// clearAdminTOTPTx 在调用方的事务里清除管理员的两步验证、吊销其全部会话并写审计（R7）；
// 管理端重置与命令行重置共用，审计项（操作者不同）由调用方给出。
func clearAdminTOTPTx(ctx context.Context, q *sqlcgen.Queries, id int64, audit AuditEntry) error {
	if _, err := q.ClearAdminTOTP(ctx, id); err != nil {
		return fmt.Errorf("clear totp of admin %d: %w", id, err)
	}
	if err := q.DeleteAdminSessionsByAdmin(ctx, id); err != nil {
		return fmt.Errorf("revoke sessions of admin %d: %w", id, err)
	}
	return writeAudit(ctx, q, audit)
}
