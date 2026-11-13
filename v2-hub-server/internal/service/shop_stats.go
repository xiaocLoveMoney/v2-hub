package service

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"v2-hub-server/internal/dao/sqlcgen"
)

// 收支统计与返利列表（docs/03 §5.16、§4 finance:read）。

const (
	defaultStatsDays = 30
	maxStatsDays     = 90
)

// DayStats 是一个自然日（平台时区）的收支；金额为整数分。
type DayStats struct {
	Date       string // yyyy-MM-dd
	Topup      int64  // 充值码与在线充值入账
	Spend      int64  // 订单实付（含自动续费）
	Rebate     int64
	OrderCount int
}

// FinanceStats 是一段时间的收支统计；Balance 是当前全部用户余额之和，其余是区间内合计。
type FinanceStats struct {
	Days    []DayStats
	Balance int64
	Topup   int64
	Spend   int64
	Rebate  int64
}

// FinanceStats 返回最近 days 天按天的充值、消费与返利合计（旧的在前，没有流水的日期也给 0）；days 为 0 取默认 30，最多 90。
func (s *ShopService) FinanceStats(ctx context.Context, days int) (*FinanceStats, error) {
	if days == 0 {
		days = defaultStatsDays
	}
	if days < 1 || days > maxStatsDays {
		return nil, newError(CodeInvalidArgument, fmt.Sprintf("days 须在 1–%d 之间", maxStatsDays))
	}
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return nil, err
	}
	loc := cur.Location()
	today := s.now().In(loc)
	first := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -(days - 1))
	since := pgtype.Timestamptz{Time: first, Valid: true}
	tz := loc.String()

	out := &FinanceStats{Days: make([]DayStats, days)}
	index := make(map[string]int, days)
	for i := range days {
		date := first.AddDate(0, 0, i).Format(dateLayout)
		out.Days[i].Date, index[date] = date, i
	}
	// 落在区间外的日期（时区换算的边界）直接忽略
	slot := func(d pgtype.Date) *DayStats {
		if i, ok := index[d.Time.Format(dateLayout)]; d.Valid && ok {
			return &out.Days[i]
		}
		return nil
	}
	topups, err := s.q.DailyTopups(ctx, sqlcgen.DailyTopupsParams{Tz: tz, Since: since})
	if err != nil {
		return nil, fmt.Errorf("daily topups: %w", err)
	}
	for _, r := range topups {
		if d := slot(r.Day); d != nil {
			d.Topup = r.Total
			out.Topup += r.Total
		}
	}
	orders, err := s.q.DailyOrders(ctx, sqlcgen.DailyOrdersParams{Tz: tz, Since: since})
	if err != nil {
		return nil, fmt.Errorf("daily orders: %w", err)
	}
	for _, r := range orders {
		if d := slot(r.Day); d != nil {
			d.Spend, d.OrderCount = r.Total, int(r.OrderCount)
			out.Spend += r.Total
		}
	}
	rebates, err := s.q.DailyRebates(ctx, sqlcgen.DailyRebatesParams{Tz: tz, Since: since})
	if err != nil {
		return nil, fmt.Errorf("daily rebates: %w", err)
	}
	for _, r := range rebates {
		if d := slot(r.Day); d != nil {
			d.Rebate = r.Total
			out.Rebate += r.Total
		}
	}
	if out.Balance, err = s.q.SumUserBalances(ctx); err != nil {
		return nil, fmt.Errorf("sum balances: %w", err)
	}
	return out, nil
}

// RebateRecord 是管理端的一条返利记录。
type RebateRecord struct {
	ID              int64
	InviterUsername string
	InviteeUsername string
	OrderNo         string
	OrderAmount     int64
	Percent         int
	Amount          int64
	CreatedAt       time.Time
}

// ListRebates 返回返利记录分页，新的在前；inviterID 为 0 表示不限（finance:read）。
func (s *ShopService) ListRebates(ctx context.Context, inviterID int64, pq PageQuery) (*Page[RebateRecord], error) {
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	if inviterID < 0 {
		return nil, newError(CodeInvalidArgument, msgUserNotFound)
	}
	rows, err := s.q.ListRebates(ctx, sqlcgen.ListRebatesParams{InviterID: inviterID, RowLimit: limit, RowOffset: offset})
	if err != nil {
		return nil, fmt.Errorf("list rebates: %w", err)
	}
	total, err := s.q.CountRebates(ctx, inviterID)
	if err != nil {
		return nil, fmt.Errorf("count rebates: %w", err)
	}
	items := make([]RebateRecord, 0, len(rows))
	for _, r := range rows {
		items = append(items, RebateRecord{ID: r.ID, InviterUsername: r.InviterUsername, InviteeUsername: r.InviteeUsername, OrderNo: r.OrderNo,
			OrderAmount: r.OrderAmount, Percent: int(r.Percent), Amount: r.Amount, CreatedAt: r.CreatedAt.Time})
	}
	return &Page[RebateRecord]{Items: items, Total: total}, nil
}
