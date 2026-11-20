package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/panel"
)

// 服务器账单与流量额度（docs/03 §5.1 “账单与流量额度”，Ruling-82）。
const (
	// 计费周期。
	BillingCycleMonth    = "month"
	BillingCycleQuarter  = "quarter"
	BillingCycleHalfYear = "half_year"
	BillingCycleYear     = "year"
	BillingCycleOnce     = "once"

	// 流量额度的计量方式：收发合计、只算出站、收发取大。
	TrafficModeBoth = "both"
	TrafficModeOut  = "out"
	TrafficModeMax  = "max"

	// ActionServerBilling 是保存账单与流量额度的审计动作。
	ActionServerBilling = "server.billing.update"

	maxServerPriceCents = 99999999
	minTrafficResetDay  = 1
	maxTrafficResetDay  = 31
	// trafficCycleLookbackDays 是一个流量周期最长的天数：列表按它取各台的按天流量，再按各自的周期求和。
	trafficCycleLookbackDays = 31
	// nicTrafficRetentionDays 是按天流量的保留天数。
	nicTrafficRetentionDays = 62

	msgBadPrice        = "价格须在 0 到 99999999 分之间"
	msgBadCurrency     = "币种须为三位大写字母"
	msgBadBillingCycle = "计费周期只能是 month、quarter、half_year、year、once"
	msgBadTrafficQuota = "流量额度不能为负"
	msgBadResetDay     = "重置日须在 1 到 31 之间"
	msgBadTrafficMode  = "计量方式只能是 both、out、max"
	msgBadTrafficUsed  = "已用流量不能为负"
)

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// ServerBilling 是一台服务器的账单与流量额度。日期都是站点时区的日期，以 UTC 零点的 time.Time 表示。
// PriceCents、ExpiresOn、TrafficQuotaBytes 为 nil 表示没填（额度为 nil 也表示不限）。
type ServerBilling struct {
	PriceCents        *int32
	PriceCurrency     string
	BillingCycle      string
	ExpiresOn         *time.Time
	TrafficQuotaBytes *int64
	TrafficResetDay   int
	TrafficMode       string
}

// ServerBillingInput 是保存账单的输入；TrafficUsedBytes 非 nil 时把本周期已用校准成这个值。
type ServerBillingInput struct {
	ServerBilling
	TrafficUsedBytes *int64
}

// ServerCycleTraffic 是当前流量周期的用量：SentBytes / RecvBytes 是网卡收发的增量之和，
// UsedBytes 是按计量方式折算并加上校准值后的已用（不小于 0）。
type ServerCycleTraffic struct {
	CycleStart  time.Time
	NextResetOn time.Time
	SentBytes   int64
	RecvBytes   int64
	UsedBytes   int64
}

func isValidBillingCycle(cycle string) bool {
	switch cycle {
	case BillingCycleMonth, BillingCycleQuarter, BillingCycleHalfYear, BillingCycleYear, BillingCycleOnce:
		return true
	default:
		return false
	}
}

func isValidTrafficMode(mode string) bool {
	switch mode {
	case TrafficModeBoth, TrafficModeOut, TrafficModeMax:
		return true
	default:
		return false
	}
}

func validateBilling(in ServerBillingInput) error {
	switch {
	case in.PriceCents != nil && (*in.PriceCents < 0 || *in.PriceCents > maxServerPriceCents):
		return newError(CodeInvalidArgument, msgBadPrice)
	case !currencyPattern.MatchString(in.PriceCurrency):
		return newError(CodeInvalidArgument, msgBadCurrency)
	case !isValidBillingCycle(in.BillingCycle):
		return newError(CodeInvalidArgument, msgBadBillingCycle)
	case in.TrafficQuotaBytes != nil && *in.TrafficQuotaBytes < 0:
		return newError(CodeInvalidArgument, msgBadTrafficQuota)
	case in.TrafficResetDay < minTrafficResetDay || in.TrafficResetDay > maxTrafficResetDay:
		return newError(CodeInvalidArgument, msgBadResetDay)
	case !isValidTrafficMode(in.TrafficMode):
		return newError(CodeInvalidArgument, msgBadTrafficMode)
	case in.TrafficUsedBytes != nil && *in.TrafficUsedBytes < 0:
		return newError(CodeInvalidArgument, msgBadTrafficUsed)
	default:
		return nil
	}
}

func billingFromRow(r sqlcgen.GetServerRow) ServerBilling {
	b := ServerBilling{
		PriceCurrency: r.PriceCurrency, BillingCycle: r.BillingCycle, ExpiresOn: datePtr(r.ExpiresOn),
		TrafficResetDay: int(r.TrafficResetDay), TrafficMode: r.TrafficMode,
	}
	if r.PriceCents.Valid {
		v := r.PriceCents.Int32
		b.PriceCents = &v
	}
	if r.TrafficQuotaBytes.Valid {
		v := r.TrafficQuotaBytes.Int64
		b.TrafficQuotaBytes = &v
	}
	return b
}

func datePtr(d pgtype.Date) *time.Time {
	if !d.Valid {
		return nil
	}
	v := d.Time
	return &v
}

func dateOf(t time.Time) pgtype.Date {
	return pgtype.Date{Time: t, Valid: true}
}

func dateOrNull(t *time.Time) pgtype.Date {
	if t == nil {
		return pgtype.Date{}
	}
	return dateOf(*t)
}

// civilDate 取 t 在 loc 里的日期，以 UTC 零点表示（与数据库 DATE 列读出的形状一致）。
func civilDate(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// today 返回站点时区的今天。
func (s *PanelService) today(ctx context.Context) time.Time {
	loc := time.UTC
	if s.cfg.SiteLocation != nil {
		loc = s.cfg.SiteLocation(ctx)
	}
	return civilDate(s.now(), loc)
}

// resetDateIn 返回某年某月的重置日；当月没有这一天取月末。
func resetDateIn(year int, month time.Month, resetDay int) time.Time {
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	return time.Date(year, month, min(resetDay, lastDay), 0, 0, 0, 0, time.UTC)
}

// trafficCycle 返回 today 所在流量周期的起始日与下一次重置的日期：
// 周期从最近一个不晚于今天的重置日起，到下一个重置日的前一天。
func trafficCycle(today time.Time, resetDay int) (start, next time.Time) {
	year, month, _ := today.Date()
	start = resetDateIn(year, month, resetDay)
	if today.Before(start) {
		next = start
		return resetDateIn(year, month-1, resetDay), next
	}
	return start, resetDateIn(year, month+1, resetDay)
}

// usedByMode 按计量方式把收发折算成已用。
func usedByMode(mode string, sent, recv int64) int64 {
	switch mode {
	case TrafficModeOut:
		return sent
	case TrafficModeMax:
		return max(sent, recv)
	default:
		return sent + recv
	}
}

// trafficOf 由周期内的收发算出一台服务器的用量；校准值只在它所属的周期等于当前周期时计入。
func trafficOf(srv *Server, today time.Time, sent, recv int64) ServerCycleTraffic {
	start, next := trafficCycle(today, srv.Billing.TrafficResetDay)
	used := usedByMode(srv.Billing.TrafficMode, sent, recv)
	if srv.trafficAdjustCycle != nil && srv.trafficAdjustCycle.Equal(start) {
		used += srv.trafficAdjust
	}
	return ServerCycleTraffic{CycleStart: start, NextResetOn: next, SentBytes: sent, RecvBytes: recv, UsedBytes: max(used, 0)}
}

// attachTraffic 给一批服务器填上本周期的流量用量：一次取出最长周期范围内的按天流量，再按各台自己的周期求和。
func (s *PanelService) attachTraffic(ctx context.Context, servers []Server) error {
	if len(servers) == 0 {
		return nil
	}
	today := s.today(ctx)
	since := dateOf(today.AddDate(0, 0, -trafficCycleLookbackDays))
	var rows []sqlcgen.ServerNicTrafficDaily
	var err error
	if len(servers) == 1 {
		rows, err = s.q.ListServerNicTrafficOf(ctx, sqlcgen.ListServerNicTrafficOfParams{ServerID: servers[0].ID, Day: since})
	} else {
		rows, err = s.q.ListServerNicTraffic(ctx, since)
	}
	if err != nil {
		return fmt.Errorf("list server nic traffic: %w", err)
	}
	type sum struct{ sent, recv int64 }
	starts := make(map[int64]time.Time, len(servers))
	for i := range servers {
		starts[servers[i].ID], _ = trafficCycle(today, servers[i].Billing.TrafficResetDay)
	}
	sums := make(map[int64]sum, len(servers))
	for _, r := range rows {
		start, ok := starts[r.ServerID]
		if !ok || r.Day.Time.Before(start) {
			continue
		}
		cur := sums[r.ServerID]
		sums[r.ServerID] = sum{sent: cur.sent + r.Sent, recv: cur.recv + r.Recv}
	}
	for i := range servers {
		cur := sums[servers[i].ID]
		servers[i].Traffic = trafficOf(&servers[i], today, cur.sent, cur.recv)
	}
	return nil
}

// nicDelta 由上次与这次的网卡累计计数算增量：没有基线（第一次）不计；比上次小视为机器重启，增量取当前值。
func nicDelta(last pgtype.Int8, cur int64) int64 {
	switch {
	case !last.Valid:
		return 0
	case cur < last.Int64:
		return cur
	default:
		return cur - last.Int64
	}
}

// recordNicTraffic 在一次成功的健康探测后把网卡累计计数的增量记到当天（Ruling-82）。
// 面板没给资源快照或没报累计计数（收发都是 0）时不动基线。
func (s *PanelService) recordNicTraffic(ctx context.Context, serverID int64, res *panel.Resources) error {
	if res == nil || (res.NetSent == 0 && res.NetRecv == 0) {
		return nil
	}
	day := dateOf(s.today(ctx))
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		last, err := q.GetServerNicCountersForUpdate(ctx, serverID)
		if errors.Is(err, pgx.ErrNoRows) {
			// 探测期间服务器被删除
			return nil
		}
		if err != nil {
			return err
		}
		if err := q.SetServerNicCounters(ctx, sqlcgen.SetServerNicCountersParams{ID: serverID, Sent: res.NetSent, Recv: res.NetRecv}); err != nil {
			return err
		}
		sent, recv := nicDelta(last.NicLastSent, res.NetSent), nicDelta(last.NicLastRecv, res.NetRecv)
		if sent == 0 && recv == 0 {
			return nil
		}
		return q.AddServerNicTraffic(ctx, sqlcgen.AddServerNicTrafficParams{ServerID: serverID, Day: day, Sent: sent, Recv: recv})
	})
	if err != nil {
		return fmt.Errorf("record nic traffic of server %d: %w", serverID, err)
	}
	return nil
}

// SetServerBilling 保存一台服务器的账单与流量额度并返回修改后的服务器。
// 带 TrafficUsedBytes 时把本周期已用校准成该值；不带而重置日或计量方式变了，原校准值作废。
// 取值不合法返回 400，服务器不存在返回 404。
func (s *PanelService) SetServerBilling(ctx context.Context, actor *AdminPrincipal, id int64, in ServerBillingInput, ip *netip.Addr) (*Server, error) {
	if err := validateBilling(in); err != nil {
		return nil, err
	}
	old, err := s.GetServer(ctx, id)
	if err != nil {
		return nil, err
	}
	start, _ := trafficCycle(s.today(ctx), in.TrafficResetDay)
	params := sqlcgen.SetServerBillingParams{
		ID: id, PriceCurrency: in.PriceCurrency, BillingCycle: in.BillingCycle, ExpiresOn: dateOrNull(in.ExpiresOn),
		TrafficResetDay: int16(in.TrafficResetDay), TrafficMode: in.TrafficMode,
	}
	if in.PriceCents != nil {
		params.PriceCents = pgtype.Int4{Int32: *in.PriceCents, Valid: true}
	}
	if in.TrafficQuotaBytes != nil {
		params.TrafficQuotaBytes = int8Of(*in.TrafficQuotaBytes)
	}
	isSameRule := old.Billing.TrafficResetDay == in.TrafficResetDay && old.Billing.TrafficMode == in.TrafficMode
	switch {
	case in.TrafficUsedBytes != nil:
		measured, err := s.measuredTraffic(ctx, id, start, in.TrafficMode)
		if err != nil {
			return nil, err
		}
		params.TrafficAdjustBytes, params.TrafficAdjustCycle = *in.TrafficUsedBytes-measured, dateOf(start)
	case isSameRule:
		params.TrafficAdjustBytes, params.TrafficAdjustCycle = old.trafficAdjust, dateOrNull(old.trafficAdjustCycle)
	default:
		// 重置日或计量方式变了：原校准值按旧规则算出，不再适用
	}
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.SetServerBilling(ctx, params)
		if err != nil {
			return fmt.Errorf("set billing of server %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgServerNotFound)
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionServerBilling, TargetType: targetServer,
			TargetID: fmt.Sprint(id), ClientIP: ip, Detail: billingAuditDetail(in),
		})
	})
	if err != nil {
		return nil, err
	}
	return s.GetServer(ctx, id)
}

// measuredTraffic 返回一台服务器自 start 起测得的流量，按计量方式折算、不含校准值。
func (s *PanelService) measuredTraffic(ctx context.Context, id int64, start time.Time, mode string) (int64, error) {
	rows, err := s.q.ListServerNicTrafficOf(ctx, sqlcgen.ListServerNicTrafficOfParams{ServerID: id, Day: dateOf(start)})
	if err != nil {
		return 0, fmt.Errorf("list nic traffic of server %d: %w", id, err)
	}
	var sent, recv int64
	for _, r := range rows {
		sent, recv = sent+r.Sent, recv+r.Recv
	}
	return usedByMode(mode, sent, recv), nil
}

func billingAuditDetail(in ServerBillingInput) map[string]any {
	detail := map[string]any{
		"priceCurrency": in.PriceCurrency, "billingCycle": in.BillingCycle, "trafficResetDay": in.TrafficResetDay,
		"trafficMode": in.TrafficMode, "isCalibrated": in.TrafficUsedBytes != nil,
	}
	if in.PriceCents != nil {
		detail["priceCents"] = *in.PriceCents
	}
	if in.ExpiresOn != nil {
		detail["expiresOn"] = in.ExpiresOn.Format(time.DateOnly)
	}
	if in.TrafficQuotaBytes != nil {
		detail["trafficQuotaBytes"] = *in.TrafficQuotaBytes
	}
	return detail
}
