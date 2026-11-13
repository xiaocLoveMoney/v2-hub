package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/panel"
)

// counterDelta 求面板计数的增量；读数变小视为面板侧清零，增量取当前读数（docs/03 §5.2 用量）。
func counterDelta(cur, last int64) int64 {
	if cur >= last {
		return cur - last
	}
	return cur
}

// CollectServerTraffic 拉一台面板上平台 client 的累计流量，按增量累加到用户已用并记小时明细。已停用的服务器跳过（T4）。
func (s *UserService) CollectServerTraffic(ctx context.Context, serverID int64) error {
	unlock := lockServer(serverID)
	defer unlock()
	p, srv, err := s.panels.openProvider(ctx, serverID)
	if err != nil {
		return err
	}
	if srv.IsDisabled {
		return nil
	}
	clients, err := p.ListClients(ctx)
	if err != nil {
		return fmt.Errorf("list clients of server %d: %w", serverID, err)
	}
	provs, err := s.q.ListServerProvisions(ctx, serverID)
	if err != nil {
		return fmt.Errorf("list provisions of server %d: %w", serverID, err)
	}
	byEmail := make(map[string]sqlcgen.ListServerProvisionsRow, len(provs))
	for _, v := range provs {
		byEmail[v.Email] = v
	}
	if err := s.collectOnline(ctx, p, serverID); err != nil {
		// 在线状态只用于展示，采集失败不影响计量
		slog.Warn("collect online clients", "server_id", serverID, "err", err)
	}
	now := s.now()
	for _, c := range clients {
		v, ok := byEmail[c.Email]
		if !ok || (c.Up == v.LastUp && c.Down == v.LastDown) {
			continue
		}
		up, down := counterDelta(c.Up, v.LastUp), counterDelta(c.Down, v.LastDown)
		err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
			if err := q.SetProvisionCounters(ctx, sqlcgen.SetProvisionCountersParams{ID: v.ID, LastUp: c.Up, LastDown: c.Down}); err != nil {
				return fmt.Errorf("save counters of provision %d: %w", v.ID, err)
			}
			if up+down == 0 {
				return nil
			}
			row, err := lockUser(ctx, q, v.UserID)
			if errors.Is(err, ErrNotFound) {
				return nil // 待删除的用户不再计量
			}
			if err != nil {
				return err
			}
			qt := quotaFromRow(row)
			qt.AddTraffic(up, down, now)
			if err := saveQuota(ctx, q, row.ID, row.PlanID, qt, row.Status); err != nil {
				return err
			}
			if err := q.TouchUserLastUsed(ctx, sqlcgen.TouchUserLastUsedParams{ID: row.ID, LastUsedAt: pgtype.Timestamptz{Time: now, Valid: true}}); err != nil {
				return fmt.Errorf("touch last used of user %d: %w", row.ID, err)
			}
			return q.AddUserTraffic(ctx, sqlcgen.AddUserTrafficParams{UserID: row.ID, Hour: pgtype.Timestamptz{Time: hourOf(now), Valid: true}, Up: up, Down: down})
		})
		if err != nil {
			return fmt.Errorf("account traffic of %s on server %d: %w", c.Email, serverID, err)
		}
	}
	return nil
}

// collectOnline 记录这台面板上在线的 client（docs/03 §5.5）；面板不提供时不动现有记录。
func (s *UserService) collectOnline(ctx context.Context, p panel.Provider, serverID int64) error {
	if !p.Capabilities().OnlineClients {
		return nil
	}
	emails, err := p.OnlineClients(ctx)
	if err != nil {
		return err
	}
	if emails == nil {
		emails = []string{}
	}
	if err := s.q.SetServerProvisionsOnline(ctx, sqlcgen.SetServerProvisionsOnlineParams{ServerID: serverID, Emails: emails}); err != nil {
		return fmt.Errorf("save online provisions of server %d: %w", serverID, err)
	}
	if err := s.q.SetServerOnlineClients(ctx, sqlcgen.SetServerOnlineClientsParams{ID: serverID, OnlineClients: int32(len(emails))}); err != nil {
		return fmt.Errorf("save online count of server %d: %w", serverID, err)
	}
	return nil
}

// AdvanceUsers 推进全部用户的时钟（自动续期 U4、周期重置 U5）并按额度 / 到期重算状态（U2、U3）。
// 状态变了的用户，下发标为待同步并触发对账。返回状态变化的用户数。
func (s *UserService) AdvanceUsers(ctx context.Context) (int, error) {
	ids, err := s.q.ListActiveUserIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("list users: %w", err)
	}
	now := s.now()
	changed := 0
	for _, id := range ids {
		err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
			row, err := lockUser(ctx, q, id)
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			qt := quotaFromRow(row)
			advanced := qt.Advance(now, s.cfg.Location)
			status := qt.Status(now)
			if !advanced && status == row.Status {
				return nil
			}
			if err := saveQuota(ctx, q, id, row.PlanID, qt, status); err != nil {
				return err
			}
			if status == row.Status {
				return nil
			}
			changed++
			slog.Info("user status changed", "user_id", id, "from", row.Status, "to", status)
			if err := q.MarkUserProvisionsPending(ctx, id); err != nil {
				return fmt.Errorf("mark provisions of user %d pending: %w", id, err)
			}
			return writeAudit(ctx, q, AuditEntry{ActorType: ActorSystem, Action: ActionUserStatus, TargetType: targetUser,
				TargetID: fmt.Sprint(id), Detail: map[string]any{"from": row.Status, "to": status}})
		})
		if err != nil {
			return changed, fmt.Errorf("advance user %d: %w", id, err)
		}
	}
	if changed > 0 {
		s.TriggerReconcile()
	}
	return changed, nil
}

// ListEnabledServerIDs 供用量与对账任务遍历服务器。
func (s *UserService) ListEnabledServerIDs(ctx context.Context) ([]int64, error) {
	return s.panels.ListEnabledServerIDs(ctx)
}
