package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"v2-hub-server/internal/dao/sqlcgen"
)

// 自动续费、提醒邮件与邀请返利（docs/03 §5.16）。由用量任务每轮调用 AdvanceUsers 驱动。

const (
	// 一轮最多处理的用户数；处理不完的下一轮（一分钟后）接着来
	renewBatchSize  = 500
	notifyBatchSize = 200
	// 到期后这么久以内的自动续费仍从原到期日顺延，不让用户因为任务的执行间隔少掉几十秒；超过（如服务停机多日）则从当前时刻起算
	renewGrace         = 24 * time.Hour
	mailTimeLayout     = "2006-01-02 15:04"
	moneyCentsPerUnit  = 100
	bytesPerGiB        = int64(1) << 30
	ActionAutoRenew    = "user.auto_renew"
	ActionAutoRenewOff = "user.auto_renew_failed"

	reasonRenewNoBalance = "余额不足"
	reasonRenewOffSale   = "套餐已下架或该时长选项已不存在"
	noteAutoRenewOK      = "已开启自动续费，到期时会从余额扣款。"
	noteAutoRenewShort   = "已开启自动续费，但当前余额不够续费一次，请及时充值，否则到期后自动续费会关闭。"
	noteAutoRenewOff     = "没有开启自动续费，到期后节点会停用，可以登录后续费。"
)

// formatMoney 把整数分写成“币种 元.分”的文本，只用于邮件；不做任何换算（Ruling-29）。
func formatMoney(cents int64, currency string) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s%s %d.%02d", sign, currency, cents/moneyCentsPerUnit, cents%moneyCentsPerUnit)
}

// formatTraffic 把字节数写成 GB 或 MB 的文本，只用于邮件。
func formatTraffic(bytes int64) string {
	if bytes >= bytesPerGiB {
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(bytesPerGiB))
	}
	return fmt.Sprintf("%d MB", bytes/bytesPerMB)
}

// notifyUser 在调用方的事务里给用户入一封通知邮件；邮件不可用、没有已验证邮箱或已停用时什么都不做（docs/03 §5.16 邮件条件）。
func (s *ShopService) notifyUser(ctx context.Context, q *sqlcgen.Queries, userID int64, tpl mailTemplate, vars mailVars) error {
	if s.mail == nil || !s.mail.Enabled() {
		return nil
	}
	target, err := q.GetUserNotifyTarget(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get notify target of user %d: %w", userID, err)
	}
	if !target.IsEmailVerified || target.IsDisabled || target.Email == "" {
		return nil
	}
	content, err := renderMail(tpl, vars)
	if err != nil {
		return err
	}
	return s.mail.enqueue(ctx, q, target.Email, content, mailPriorityNotice)
}

// recordRebate 在下单事务里给邀请人记返利（docs/03 §5.16 返利，Ruling-34）：按订单实付 × 比例向下取整到分，只进余额。
// 没开返利、实付为 0、没有邀请人、邀请人已停用或已删除、或“只返首笔”而这不是首笔时什么都不做。
func (s *ShopService) recordRebate(ctx context.Context, q *sqlcgen.Queries, cur Settings, inviteeID, orderID, paid int64, now time.Time) error {
	if cur.Rebate.Percent <= 0 || paid <= 0 {
		return nil
	}
	amount := paid * int64(cur.Rebate.Percent) / percentBase
	if amount <= 0 {
		return nil
	}
	rel, err := q.GetUserInviter(ctx, inviteeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get inviter of user %d: %w", inviteeID, err)
	}
	if rel.IsInviterDisabled {
		return nil
	}
	if cur.Rebate.Mode == RebateFirst {
		others, err := q.CountOtherPaidOrders(ctx, sqlcgen.CountOtherPaidOrdersParams{UserID: pgtype.Int8{Int64: inviteeID, Valid: true}, ID: orderID})
		if err != nil {
			return fmt.Errorf("count paid orders of user %d: %w", inviteeID, err)
		}
		if others > 0 {
			return nil
		}
	}
	inviter, err := q.LockUserBalance(ctx, rel.InviterID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock balance of inviter %d: %w", rel.InviterID, err)
	}
	if inviter.Balance+amount > maxBalance {
		// 邀请人余额已到上限：这笔返利作罢，不能因此让被邀请人的订单失败
		slog.Warn("rebate skipped: inviter balance at cap", "inviter_id", rel.InviterID, "order_id", orderID)
		return nil
	}
	rebateID, err := q.InsertRebate(ctx, sqlcgen.InsertRebateParams{
		InviterID: pgtype.Int8{Int64: rel.InviterID, Valid: true}, InviterUsername: rel.InviterUsername,
		InviteeID: pgtype.Int8{Int64: inviteeID, Valid: true}, InviteeUsername: rel.Username,
		OrderID: orderID, OrderAmount: paid, Percent: int32(cur.Rebate.Percent), Amount: amount, CreatedAt: timestamptz(now),
	})
	if err != nil {
		return fmt.Errorf("insert rebate for order %d: %w", orderID, err)
	}
	_, err = applyLedgerLocked(ctx, q, inviter, ledgerChange{UserID: rel.InviterID, Amount: amount, Kind: LedgerRebate, Ref: ledgerRef{Type: refRebate, ID: rebateID}}, now)
	return err
}

// AdvanceUsers 是用量任务每轮的入口：先处理到期的扣余额续费（U10），再推进全部用户的时钟与状态（U2–U5），最后发提醒邮件。
// 续费放在状态推进之前，续费成功的用户不会先被标成到期再恢复，面板上的 client 也就不会被来回启停。返回状态变化的用户数。
func (s *ShopService) AdvanceUsers(ctx context.Context) (int, error) {
	if err := s.RenewDue(ctx); err != nil {
		// 续费出错不挡住到期判定：没续上的用户照常到期
		slog.Error("auto renew", "err", err)
	}
	changed, err := s.users.AdvanceUsers(ctx)
	if err != nil {
		return changed, err
	}
	if err := s.SendReminders(ctx); err != nil {
		slog.Error("send reminders", "err", err)
	}
	return changed, nil
}

// RenewDue 处理到期且开了自动续费的用户（docs/03 §5.16，Ruling-35）：每人只在到期时尝试一次，
// 成功则扣余额、生成 renew 订单并顺延；失败则关闭自动续费并发通知。商店关闭期间不处理，也不关闭开关。
func (s *ShopService) RenewDue(ctx context.Context) error {
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return err
	}
	if !cur.Shop.Enabled {
		return nil
	}
	now := s.now()
	ids, err := s.q.ListAutoRenewDueIDs(ctx, sqlcgen.ListAutoRenewDueIDsParams{Now: timestamptz(now), RowLimit: renewBatchSize})
	if err != nil {
		return fmt.Errorf("list auto renew due: %w", err)
	}
	// 时区整轮只解析一次，不在每个用户上重复
	loc := cur.Location()
	renewed := false
	for _, id := range ids {
		ok := false
		err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
			var err error
			ok, err = s.renewOne(ctx, q, cur, loc, id, now)
			return err
		})
		if err != nil {
			return fmt.Errorf("auto renew user %d: %w", id, err)
		}
		renewed = renewed || ok
	}
	if renewed {
		s.users.TriggerReconcile()
	}
	return nil
}

// renewOne 在事务里给一个用户做到期续费，返回是否续上。loc 是站点时区，只用于通知邮件里的时间。
func (s *ShopService) renewOne(ctx context.Context, q *sqlcgen.Queries, cur Settings, loc *time.Location, userID int64, now time.Time) (bool, error) {
	row, err := lockUser(ctx, q, userID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	qt := quotaFromRow(row)
	// 管理员设的免费续期优先（U4）：能免费续上的留给状态推进去处理，这里不扣钱
	if free := qt; free.Advance(now, s.users.cfg.Location) && free.Status(now) != UserStatusExpired {
		return false, nil
	}
	if row.IsDisabled || qt.ExpiresAt == nil || now.Before(*qt.ExpiresAt) {
		return false, nil
	}
	st, err := q.GetUserShopState(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("get shop state of user %d: %w", userID, err)
	}
	if !st.AutoRenew {
		return false, nil
	}
	info, pp, err := autoRenewInfo(ctx, q, st)
	if err != nil {
		return false, err
	}
	if !info.Available {
		return false, s.failRenew(ctx, q, cur, userID, info.PlanName, reasonRenewOffSale)
	}
	price := *info.Price
	if st.Balance < price {
		return false, s.failRenew(ctx, q, cur, userID, info.PlanName, reasonRenewNoBalance)
	}

	expiredAt := *qt.ExpiresAt
	qt.ApplyPlan(PlanQuota{
		TrafficLimit: pp.TrafficLimit, DurationDays: int(pp.DurationDays), LimitIP: int(pp.LimitIp), TrafficReset: pp.TrafficReset, TrafficResetDay: int(pp.TrafficResetDay),
	}, now)
	if now.Sub(expiredAt) <= renewGrace {
		next := expiredAt.AddDate(0, 0, int(pp.DurationDays))
		qt.ExpiresAt = &next
	}
	orderNo, err := newOrderNo(orderNoPrefix, now)
	if err != nil {
		return false, err
	}
	item, err := json.Marshal(orderItem{Name: pp.Name, DurationDays: int(pp.DurationDays), TrafficBytes: pp.TrafficLimit, UnitPrice: price})
	if err != nil {
		return false, fmt.Errorf("encode order item: %w", err)
	}
	wallet, err := q.LockUserBalance(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("lock balance of user %d: %w", userID, err)
	}
	at := timestamptz(now)
	orderID, err := q.InsertOrder(ctx, sqlcgen.InsertOrderParams{
		OrderNo: orderNo, UserID: pgtype.Int8{Int64: userID, Valid: true}, Username: wallet.Username, Kind: OrderRenew,
		PlanID: pgtype.Int8{Int64: pp.PlanID, Valid: true}, Item: item, AmountOriginal: price, AmountPaid: price, CreatedAt: at,
	})
	if err != nil {
		return false, fmt.Errorf("insert renew order: %w", err)
	}
	balance := st.Balance
	if price > 0 {
		if balance, err = applyLedgerLocked(ctx, q, wallet, ledgerChange{UserID: userID, Amount: -price, Kind: LedgerRenew, Ref: ledgerRef{Type: refOrder, ID: orderID}, Remark: pp.Name}, now); err != nil {
			return false, err
		}
	}
	status := qt.Status(now)
	if err := saveQuota(ctx, q, userID, row.PlanID, qt, status); err != nil {
		return false, err
	}
	if err := q.SetUserPurchase(ctx, sqlcgen.SetUserPurchaseParams{ID: userID, PlanPriceID: st.PlanPriceID, AutoRenew: true}); err != nil {
		return false, fmt.Errorf("record renewal of user %d: %w", userID, err)
	}
	if status != row.Status {
		if err := q.MarkUserProvisionsPending(ctx, userID); err != nil {
			return false, fmt.Errorf("mark provisions of user %d pending: %w", userID, err)
		}
	}
	if err := s.recordRebate(ctx, q, cur, userID, orderID, price, now); err != nil {
		return false, err
	}
	err = writeAudit(ctx, q, AuditEntry{ActorType: ActorSystem, Action: ActionAutoRenew, TargetType: targetUser, TargetID: fmt.Sprint(userID),
		Detail: map[string]any{"orderNo": orderNo, "plan": pp.Name, "paid": price}})
	if err != nil {
		return false, err
	}
	return true, s.notifyUser(ctx, q, userID, mailTplRenewOK, mailVars{
		Site: cur.Site.Name, Plan: pp.Name, Amount: formatMoney(price, cur.Shop.Currency), Balance: formatMoney(balance, cur.Shop.Currency),
		Expires: qt.ExpiresAt.In(loc).Format(mailTimeLayout),
	})
}

// failRenew 关闭自动续费并通知用户；到期状态留给随后的状态推进去标（U3）。
func (s *ShopService) failRenew(ctx context.Context, q *sqlcgen.Queries, cur Settings, userID int64, planName, reason string) error {
	if err := q.SetUserAutoRenew(ctx, sqlcgen.SetUserAutoRenewParams{ID: userID, AutoRenew: false}); err != nil {
		return fmt.Errorf("turn off auto renew of user %d: %w", userID, err)
	}
	err := writeAudit(ctx, q, AuditEntry{ActorType: ActorSystem, Action: ActionAutoRenewOff, TargetType: targetUser, TargetID: fmt.Sprint(userID),
		Detail: map[string]any{"reason": reason}})
	if err != nil {
		return err
	}
	return s.notifyUser(ctx, q, userID, mailTplRenewFailed, mailVars{Site: cur.Site.Name, Plan: planName, Reason: reason})
}

// SendReminders 发到期提醒与流量提醒（docs/03 §5.16）：每个到期时间只提醒一次；流量每次回到阈值以上后才会再提醒。
// 邮件不可用时不发、也不记“已提醒”，等邮件配好后还能补上。
func (s *ShopService) SendReminders(ctx context.Context) error {
	if s.mail == nil || !s.mail.Enabled() {
		return nil
	}
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	if cur.Notify.ExpireDays > 0 {
		if err := s.remindExpiring(ctx, cur, now); err != nil {
			return err
		}
	}
	if cur.Notify.TrafficPercent > 0 {
		return s.remindLowTraffic(ctx, cur)
	}
	return nil
}

func (s *ShopService) remindExpiring(ctx context.Context, cur Settings, now time.Time) error {
	until := now.AddDate(0, 0, cur.Notify.ExpireDays)
	rows, err := s.q.ListExpiringUsers(ctx, sqlcgen.ListExpiringUsersParams{
		Now: timestamptz(now), Until: timestamptz(until), RowLimit: notifyBatchSize,
	})
	if err != nil {
		return fmt.Errorf("list expiring users: %w", err)
	}
	loc := cur.Location()
	for _, r := range rows {
		note := noteAutoRenewOff
		if r.AutoRenew {
			note = noteAutoRenewOK
			if !r.Price.Valid || r.Balance < r.Price.Int64 {
				note = noteAutoRenewShort
			}
		}
		vars := mailVars{Site: cur.Site.Name, Plan: r.PlanName, Expires: r.ExpiresAt.Time.In(loc).Format(mailTimeLayout), Note: note}
		err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
			if err := q.SetExpireNotified(ctx, sqlcgen.SetExpireNotifiedParams{ID: r.ID, ExpireNotifiedFor: r.ExpiresAt}); err != nil {
				return fmt.Errorf("mark expire notified: %w", err)
			}
			return s.notifyUser(ctx, q, r.ID, mailTplExpireSoon, vars)
		})
		if err != nil {
			return fmt.Errorf("remind expiring user %d: %w", r.ID, err)
		}
	}
	return nil
}

func (s *ShopService) remindLowTraffic(ctx context.Context, cur Settings) error {
	percent := int64(cur.Notify.TrafficPercent)
	if err := s.q.ResetTrafficNotified(ctx, percent); err != nil {
		return fmt.Errorf("reset traffic notified: %w", err)
	}
	rows, err := s.q.ListLowTrafficUsers(ctx, sqlcgen.ListLowTrafficUsersParams{Percent: percent, RowLimit: notifyBatchSize})
	if err != nil {
		return fmt.Errorf("list low traffic users: %w", err)
	}
	for _, r := range rows {
		vars := mailVars{Site: cur.Site.Name, Remain: formatTraffic(max(r.TrafficLimit-r.TrafficUsed, 0))}
		err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
			if err := q.SetTrafficNotified(ctx, r.ID); err != nil {
				return fmt.Errorf("mark traffic notified: %w", err)
			}
			return s.notifyUser(ctx, q, r.ID, mailTplTrafficLow, vars)
		})
		if err != nil {
			return fmt.Errorf("remind low traffic user %d: %w", r.ID, err)
		}
	}
	return nil
}
