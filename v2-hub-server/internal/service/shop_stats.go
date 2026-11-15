package service

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"net/netip"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"v2-hub-server/internal/dao/sqlcgen"
)

// 收支统计与返利列表（docs/03 §5.16、§4 finance:read）。

const (
	defaultStatsDays   = 30
	maxStatsDays       = 366
	defaultStatsMonths = 12
	maxStatsMonths     = 24

	// 收支统计的粒度（docs/03 §5.20 财务报表）。
	StatsByDay   = "day"
	StatsByMonth = "month"

	monthLayout       = "2006-01"
	msgStatsBadGrain  = "granularity 只能是 day 或 month"
	msgStatsBadMonths = "months 须在 1–24 之间"
)

// FinanceQuery 是收支统计的查询：按日看最近 Days 天，或按月看最近 Months 个月（含本月）；零值取默认。
type FinanceQuery struct {
	Granularity string
	Days        int
	Months      int
}

// DayStats 是一个自然日（平台时区）的收支；金额为整数分。
type DayStats struct {
	Date       string // yyyy-MM-dd
	Topup      int64  // 充值码与在线充值入账
	Spend      int64  // 订单实付（含自动续费）
	Rebate     int64
	OrderCount int
}

// FinanceStats 是一段时间的收支统计；Balance 是当前全部用户余额之和，其余是区间内合计。
// 按月时 Days 的每一项代表一个月，Date 是该月 1 日。
type FinanceStats struct {
	Granularity string
	Days        []DayStats
	Balance     int64
	Topup       int64
	Spend       int64
	Rebate      int64
}

// FinanceStatsBy 按日或按月返回收支统计（docs/03 §5.20 财务报表）：按日最多 366 天，按月最多 24 个月；口径同 M5。
func (s *ShopService) FinanceStatsBy(ctx context.Context, fq FinanceQuery) (*FinanceStats, error) {
	if fq.Granularity == "" {
		fq.Granularity = StatsByDay
	}
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return nil, err
	}
	loc := cur.Location()
	today := s.now().In(loc)
	switch fq.Granularity {
	case StatsByDay:
		if fq.Days < 0 || fq.Days > maxStatsDays {
			return nil, newError(CodeInvalidArgument, fmt.Sprintf("days 须在 1–%d 之间", maxStatsDays))
		}
		return s.dailyStats(ctx, loc, today, fq.Days)
	case StatsByMonth:
		if fq.Months == 0 {
			fq.Months = defaultStatsMonths
		}
		if fq.Months < 1 || fq.Months > maxStatsMonths {
			return nil, newError(CodeInvalidArgument, msgStatsBadMonths)
		}
		firstMonth := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, -(fq.Months - 1), 0)
		days := int(today.Sub(firstMonth).Hours()/24) + 1
		daily, err := s.dailyStats(ctx, loc, today, days)
		if err != nil {
			return nil, err
		}
		return foldMonths(daily, firstMonth, fq.Months), nil
	default:
		return nil, newError(CodeInvalidArgument, msgStatsBadGrain)
	}
}

// foldMonths 把按日的统计归并成按月；每月一项，Date 为该月 1 日。
func foldMonths(daily *FinanceStats, firstMonth time.Time, months int) *FinanceStats {
	out := &FinanceStats{Granularity: StatsByMonth, Days: make([]DayStats, months), Balance: daily.Balance,
		Topup: daily.Topup, Spend: daily.Spend, Rebate: daily.Rebate}
	index := make(map[string]int, months)
	for i := range months {
		m := firstMonth.AddDate(0, i, 0)
		out.Days[i].Date, index[m.Format(monthLayout)] = m.Format(dateLayout), i
	}
	for _, d := range daily.Days {
		i, ok := index[d.Date[:len(monthLayout)]]
		if !ok {
			continue
		}
		out.Days[i].Topup += d.Topup
		out.Days[i].Spend += d.Spend
		out.Days[i].Rebate += d.Rebate
		out.Days[i].OrderCount += d.OrderCount
	}
	return out
}

// dailyStats 是按日的统计；days 为 0 取默认 30。范围由调用方校验（按月时会传入两年内的天数）。
func (s *ShopService) dailyStats(ctx context.Context, loc *time.Location, today time.Time, days int) (*FinanceStats, error) {
	if days == 0 {
		days = defaultStatsDays
	}
	first := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -(days - 1))
	since := pgtype.Timestamptz{Time: first, Valid: true}
	tz := loc.String()

	out := &FinanceStats{Granularity: StatsByDay, Days: make([]DayStats, days)}
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

const (
	ActionFinanceExport = "finance.export"
	targetFinance       = "finance"
	csvBOM              = "\uFEFF"
	csvFilenameFormat   = "finance-%s-%s.csv"
)

// csvHeader 是导出 CSV 的表头（docs/03 §5.20 财务报表）。
var csvHeader = []string{"日期", "充值入账（元）", "订单实付（元）", "返利（元）", "订单数"}

// ExportFinanceCSV 把收支统计导出为 CSV（UTF-8 带 BOM，金额以元为单位、两位小数），记审计；返回内容与建议的文件名。
func (s *ShopService) ExportFinanceCSV(ctx context.Context, actor *AdminPrincipal, fq FinanceQuery, ip *netip.Addr) ([]byte, string, error) {
	stats, err := s.FinanceStatsBy(ctx, fq)
	if err != nil {
		return nil, "", err
	}
	var buf bytes.Buffer
	buf.WriteString(csvBOM)
	w := csv.NewWriter(&buf)
	rows := make([][]string, 0, len(stats.Days)+2)
	rows = append(rows, csvHeader)
	for _, d := range stats.Days {
		date := d.Date
		if stats.Granularity == StatsByMonth {
			date = d.Date[:len(monthLayout)]
		}
		rows = append(rows, []string{date, formatCents(d.Topup), formatCents(d.Spend), formatCents(d.Rebate), strconv.Itoa(d.OrderCount)})
	}
	rows = append(rows, []string{"合计", formatCents(stats.Topup), formatCents(stats.Spend), formatCents(stats.Rebate), ""})
	if err := w.WriteAll(rows); err != nil {
		return nil, "", fmt.Errorf("write finance csv: %w", err)
	}
	if err := writeAudit(ctx, s.q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionFinanceExport, TargetType: targetFinance,
		Detail: map[string]any{"granularity": stats.Granularity, "rows": len(stats.Days)}, ClientIP: ip}); err != nil {
		return nil, "", err
	}
	name := fmt.Sprintf(csvFilenameFormat, stats.Granularity, s.now().In(time.UTC).Format(dateLayout))
	return buf.Bytes(), name, nil
}
