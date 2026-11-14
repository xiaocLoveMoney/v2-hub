package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
)

const pgUniqueViolation = "23505"

// store 封装连接池与事务，供各 service 共用。
type store struct {
	pool *pgxpool.Pool
	q    *sqlcgen.Queries
}

func newStore(pool *pgxpool.Pool) store {
	return store{pool: pool, q: sqlcgen.New(pool)}
}

// inTx 在一个事务里执行 fn；fn 返回错误或 panic 时回滚。
func (s store) inTx(ctx context.Context, fn func(q *sqlcgen.Queries) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		// Commit 成功后 Rollback 返回 ErrTxClosed，属正常；其余回滚错误只记录，主错误已向上返回。
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			slog.Warn("rollback tx", "err", rbErr)
		}
	}()
	if err := fn(s.q.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}

// 审计动作，取值稳定，前端与报表按此筛选。
const (
	ActorAdmin  = "admin"
	ActorSystem = "system"

	ActionAdminLogin         = "admin.login"
	ActionAdminLoginFailed   = "admin.login_failed"
	ActionAdminLogout        = "admin.logout"
	ActionAdminCreate        = "admin.create"
	ActionAdminUpdate        = "admin.update"
	ActionAdminPasswordReset = "admin.password_reset"
	ActionAdminTOTPEnable    = "admin.totp_enable"
	ActionAdminTOTPDisable   = "admin.totp_disable"
	ActionAdminTOTPReset     = "admin.totp_reset"
	ActionRoleCreate         = "role.create"
	ActionRoleUpdate         = "role.update"
	ActionRoleDelete         = "role.delete"

	targetAdmin = "admin"
	targetRole  = "role"
)

// AuditEntry 是一条审计记录。Detail 不得包含密码、token、分享链接。
type AuditEntry struct {
	ActorType  string
	ActorID    int64 // 0 表示无（未识别的登录失败、系统操作）
	Action     string
	TargetType string
	TargetID   string
	Detail     map[string]any
	ClientIP   *netip.Addr
}

func writeAudit(ctx context.Context, q *sqlcgen.Queries, e AuditEntry) error {
	var detail []byte
	if e.Detail != nil {
		raw, err := json.Marshal(e.Detail)
		if err != nil {
			return fmt.Errorf("marshal audit detail: %w", err)
		}
		detail = raw
	}
	err := q.InsertAuditLog(ctx, sqlcgen.InsertAuditLogParams{
		ActorType:  e.ActorType,
		ActorID:    pgtype.Int8{Int64: e.ActorID, Valid: e.ActorID != 0},
		Action:     e.Action,
		TargetType: pgtype.Text{String: e.TargetType, Valid: e.TargetType != ""},
		TargetID:   pgtype.Text{String: e.TargetID, Valid: e.TargetID != ""},
		Detail:     detail,
		ClientIp:   e.ClientIP,
	})
	if err != nil {
		return fmt.Errorf("insert audit log %s: %w", e.Action, err)
	}
	return nil
}
