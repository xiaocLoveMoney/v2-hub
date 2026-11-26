package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/crypto"
)

// 只能在服务器上经命令行执行的管理员维护（docs/03 §4.3 命令行，Ruling-98、Ruling-99）。
// 不经 HTTP、没有操作者，审计的操作者记为系统。

// detailSourceCLI 标出审计记录来自命令行。
const detailSourceCLI = "cli"

// ownerExists 报告库里是否已有所有者。
func ownerExists(ctx context.Context, q *sqlcgen.Queries) (bool, error) {
	_, err := q.GetOwnerAdminID(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get owner: %w", err)
	}
	return true, nil
}

// adminByUsername 按用户名取管理员；不存在返回 CodeNotFound。
func adminByUsername(ctx context.Context, q *sqlcgen.Queries, username string) (sqlcgen.GetAdminByUsernameRow, error) {
	row, err := q.GetAdminByUsername(ctx, username)
	if errors.Is(err, pgx.ErrNoRows) {
		return row, newError(CodeNotFound, msgAdminNotFound)
	}
	if err != nil {
		return row, fmt.Errorf("get admin %s: %w", username, err)
	}
	return row, nil
}

// TransferOwner 把所有者指定或转移给 username（R11）：目标须是启用状态的 super_admin。
// 同一事务里先锁 super_admin 角色行，再清掉旧所有者、设新所有者。返回旧所有者 ID（没有为 0）。
// 错误：CodeNotFound（用户名不存在）、CodeConflict（目标不是启用的超管）。
func (s *RBACService) TransferOwner(ctx context.Context, username string) (previousID int64, err error) {
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.LockSuperAdminRole(ctx); err != nil {
			return fmt.Errorf("lock super_admin role: %w", err)
		}
		target, err := adminByUsername(ctx, q, username)
		if err != nil {
			return err
		}
		// 代理不能成为所有者（Ruling-101）；库里另有约束 ck_admin_owner_staff 兜底
		if target.Kind != AdminKindStaff {
			return newError(CodeConflict, msgOwnerNotSuper)
		}
		roles, err := q.ListRolesForAdmin(ctx, target.ID)
		if err != nil {
			return fmt.Errorf("list roles of admin %d: %w", target.ID, err)
		}
		if !hasSuperAdminRole(roles) || target.IsDisabled {
			return newError(CodeConflict, msgOwnerNotSuper)
		}
		previousID, err = q.GetOwnerAdminID(ctx)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("get owner: %w", err)
		}
		if err := q.ClearAdminOwner(ctx); err != nil {
			return fmt.Errorf("clear owner: %w", err)
		}
		n, err := q.SetAdminOwner(ctx, target.ID)
		if err != nil {
			return fmt.Errorf("set owner %d: %w", target.ID, err)
		}
		if n == 0 {
			return newError(CodeConflict, msgOwnerNotSuper)
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorSystem, Action: ActionAdminOwnerTransfer, TargetType: targetAdmin, TargetID: fmt.Sprint(target.ID),
			Detail: map[string]any{"source": detailSourceCLI, "username": username, "previousOwnerId": previousID},
		})
	})
	if err != nil {
		return 0, err
	}
	return previousID, nil
}

// ResetAdminTOTPByUsername 清除 username 的两步验证并吊销其全部会话（所有者遗失设备时用，Ruling-98）。
// 被清除的超管下次登录后须重新开启（R10）。错误：CodeNotFound。
func (s *RBACService) ResetAdminTOTPByUsername(ctx context.Context, username string) error {
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		target, err := adminByUsername(ctx, q, username)
		if err != nil {
			return err
		}
		return clearAdminTOTPTx(ctx, q, target.ID, AuditEntry{
			ActorType: ActorSystem, Action: ActionAdminTOTPReset, TargetType: targetAdmin, TargetID: fmt.Sprint(target.ID),
			Detail: map[string]any{"source": detailSourceCLI},
		})
	})
}

// ResetAdminPasswordByUsername 重设 username 的密码并吊销其全部会话（忘记密码时用，Ruling-98）。
// 错误：CodeInvalidArgument（密码不合规）、CodeNotFound。
func (s *RBACService) ResetAdminPasswordByUsername(ctx context.Context, username, password string) error {
	if err := validatePassword(password); err != nil {
		return err
	}
	hash, err := crypto.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		target, err := adminByUsername(ctx, q, username)
		if err != nil {
			return err
		}
		return resetAdminPasswordTx(ctx, q, target.ID, hash, AuditEntry{
			ActorType: ActorSystem, Action: ActionAdminPasswordReset, TargetType: targetAdmin, TargetID: fmt.Sprint(target.ID),
			Detail: map[string]any{"source": detailSourceCLI},
		})
	})
}
