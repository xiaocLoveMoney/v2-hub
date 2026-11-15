package service

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
)

// 总览分析（docs/03 §5.20 总览分析，Ruling-66）：用户、流量、服务器、财务、待处理五个分区，各自一个方法。
// 口径都写在 docs/03，这里不复述。

const (
	analyticsDaysWeek    = 7
	analyticsDaysMonth   = 30
	analyticsDaysQuarter = 90

	analyticsTopUsers       = 10
	analyticsPlanRows       = 50
	analyticsTopServers     = 20
	analyticsMaxServers     = 500
	analyticsAvailability   = 100
	analyticsTopLoads       = 5
	analyticsTopPlans       = 10
	analyticsTopCoupons     = 5
	analyticsBacklogServers = 10
	analyticsHoursPerDay    = 24
	analyticsFullPercent    = 100
	analyticsPercentScale   = 10 // 可用率保留一位小数

	analyticsSilentWindow     = 7 * 24 * time.Hour
	analyticsExpiryWeek       = 7 * 24 * time.Hour
	analyticsExpiryMonth      = 30 * 24 * time.Hour
	analyticsAvailWindow      = 7 * 24 * time.Hour
	analyticsLoadWindow       = 15 * time.Minute
	analyticsFirstReplyWindow = 30 * 24 * time.Hour
	analyticsOutboxWindow     = 24 * time.Hour
	metricBucket              = 5 * time.Minute

	orderKindPlan        = "plan"
	orderKindRenew       = "renew"
	orderKindTrafficPack = "traffic_pack"

	msgBadAnalyticsDays = "days 只能是 7、30 或 90"
)

// AnalyticsService 汇总总览的各个分析分区。结果按（分区，参数）短时间缓存。
type AnalyticsService struct {
	store
	settings *SettingsService
	reach    *ReachService
	now      func() time.Time
	cache    analyticsCache
}

// NewAnalyticsService 创建分析服务；reach 可为 nil（服务器分区不带大陆连通汇总），cacheTTL 为 0 不缓存，now 为 nil 时用 time.Now。
func NewAnalyticsService(pool *pgxpool.Pool, settings *SettingsService, reach *ReachService, cacheTTL time.Duration, now func() time.Time) *AnalyticsService {
	if now == nil {
		now = time.Now
	}
	return &AnalyticsService{
		store: newStore(pool), settings: settings, reach: reach, now: now,
		cache: analyticsCache{ttl: cacheTTL, now: now, items: make(map[string]analyticsCacheItem, 16)},
	}
}

type analyticsCacheItem struct {
	value any
	at    time.Time
}

// analyticsCache 是按键的短时缓存，并发安全；键的个数由分区与参数的组合决定，是有限的。
type analyticsCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	now   func() time.Time
	items map[string]analyticsCacheItem
}

func (c *analyticsCache) get(key string) (any, bool) {
	if c.ttl <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok || c.now().Sub(it.at) >= c.ttl {
		return nil, false
	}
	return it.value, true
}

func (c *analyticsCache) put(key string, value any) {
	if c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = analyticsCacheItem{value: value, at: c.now()}
}

// cachedAnalytics 先查缓存，没有再算；缓存里的值只读，调用方不得修改。
func cachedAnalytics[T any](c *analyticsCache, key string, load func() (*T, error)) (*T, error) {
	if v, ok := c.get(key); ok {
		if out, ok := v.(*T); ok {
			return out, nil
		}
	}
	out, err := load()
	if err != nil {
		return nil, err
	}
	c.put(key, out)
	return out, nil
}

// analyticsWindow 是一次查询的时间窗：窗口内的自然日（旧的在前）与第一天的零点。
type analyticsWindow struct {
	tz    string
	since time.Time
	dates []string
	index map[string]int
}

// slot 返回某个自然日在窗口里的下标；落在窗口外（时区换算的边界）返回 false。
func (w analyticsWindow) slot(d pgtype.Date) (int, bool) {
	if !d.Valid {
		return 0, false
	}
	i, ok := w.index[d.Time.Format(dateLayout)]
	return i, ok
}

func validAnalyticsDays(days int) (int, error) {
	switch days {
	case 0:
		return analyticsDaysMonth, nil
	case analyticsDaysWeek, analyticsDaysMonth, analyticsDaysQuarter:
		return days, nil
	default:
		return 0, newError(CodeInvalidArgument, msgBadAnalyticsDays)
	}
}

func (s *AnalyticsService) window(ctx context.Context, days int) (analyticsWindow, error) {
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return analyticsWindow{}, err
	}
	loc := cur.Location()
	todayStart, _ := dayBounds(s.now(), loc)
	since := todayStart.AddDate(0, 0, -(days - 1))
	w := analyticsWindow{tz: loc.String(), since: since, dates: make([]string, days), index: make(map[string]int, days)}
	for i := range days {
		date := since.AddDate(0, 0, i).Format(dateLayout)
		w.dates[i], w.index[date] = date, i
	}
	return w, nil
}

// UserGrowthDay 是一天的新增与累计用户数。
type UserGrowthDay struct {
	Date       string
	NewUsers   int
	TotalUsers int
}

// CountDay 是一天的一个计数。
type CountDay struct {
	Date  string
	Count int
}

// UserStatusCounts 是用户按状态的分布。
type UserStatusCounts struct {
	Active, Limited, Expired, Disabled int
}

// UserExpiryCounts 是用户按到期时间的分布；Within7dNoAutoRenew 是 7 日内到期且没开自动续费的人数。
type UserExpiryCounts struct {
	Expired, Within7d, Within30d, Later, Never, Within7dNoAutoRenew int
}

// PlanUsers 是一个套餐的用户数；PlanID 为 nil 表示没有套餐。
type PlanUsers struct {
	PlanID      *int64
	Name        string
	Users       int
	ActiveUsers int
}

// TopUser 是流量排行里的一个用户。
type TopUser struct {
	UserID   int64
	Username string
	PlanName string
	Bytes    int64
}

// SubscribeDay 是一天的订阅拉取次数与去重用户数。
type SubscribeDay struct {
	Date  string
	Pulls int
	Users int
}

// SubscribeClient 是一类客户端的拉取次数与去重用户数。
type SubscribeClient struct {
	Client string
	Pulls  int
	Users  int
}

// SubscribeFormat 是一种订阅格式的拉取次数。
type SubscribeFormat struct {
	Format string
	Pulls  int
}

// AnalyticsUsers 是总览的用户分区。
type AnalyticsUsers struct {
	Growth           []UserGrowthDay
	ActiveDays       []CountDay
	DAU, WAU, MAU    int
	Status           UserStatusCounts
	Expiry           UserExpiryCounts
	Plans            []PlanUsers
	SilentUsers      int
	TopUsers         []TopUser
	SubscribeDays    []SubscribeDay
	SubscribeClients []SubscribeClient
	SubscribeFormats []SubscribeFormat
}

// Users 返回用户分区；days 只能是 7 / 30 / 90（0 取 30），否则返回 400。
func (s *AnalyticsService) Users(ctx context.Context, days int) (*AnalyticsUsers, error) {
	days, err := validAnalyticsDays(days)
	if err != nil {
		return nil, err
	}
	return cachedAnalytics(&s.cache, fmt.Sprintf("users:%d", days), func() (*AnalyticsUsers, error) {
		w, err := s.window(ctx, days)
		if err != nil {
			return nil, err
		}
		out := &AnalyticsUsers{}
		if err := s.fillUserGrowth(ctx, w, out); err != nil {
			return nil, err
		}
		if err := s.fillUserActivity(ctx, w, out); err != nil {
			return nil, err
		}
		if err := s.fillUserBreakdown(ctx, w, out); err != nil {
			return nil, err
		}
		if err := s.fillSubscription(ctx, w, out); err != nil {
			return nil, err
		}
		return out, nil
	})
}

func (s *AnalyticsService) fillUserGrowth(ctx context.Context, w analyticsWindow, out *AnalyticsUsers) error {
	since := timestamptz(w.since)
	base, err := s.q.CountUsersCreatedBefore(ctx, since)
	if err != nil {
		return fmt.Errorf("analytics users before window: %w", err)
	}
	rows, err := s.q.AnalyticsUserGrowth(ctx, sqlcgen.AnalyticsUserGrowthParams{Tz: w.tz, Since: since})
	if err != nil {
		return fmt.Errorf("analytics user growth: %w", err)
	}
	out.Growth = make([]UserGrowthDay, len(w.dates))
	for i, date := range w.dates {
		out.Growth[i].Date = date
	}
	for _, r := range rows {
		if i, ok := w.slot(r.Day); ok {
			out.Growth[i].NewUsers = int(r.NewUsers)
		}
	}
	total := int(base)
	for i := range out.Growth {
		total += out.Growth[i].NewUsers
		out.Growth[i].TotalUsers = total
	}
	return nil
}

func (s *AnalyticsService) fillUserActivity(ctx context.Context, w analyticsWindow, out *AnalyticsUsers) error {
	rows, err := s.q.AnalyticsActiveDays(ctx, sqlcgen.AnalyticsActiveDaysParams{Tz: w.tz, Since: timestamptz(w.since)})
	if err != nil {
		return fmt.Errorf("analytics active days: %w", err)
	}
	out.ActiveDays = make([]CountDay, len(w.dates))
	for i, date := range w.dates {
		out.ActiveDays[i].Date = date
	}
	for _, r := range rows {
		if i, ok := w.slot(r.Day); ok {
			out.ActiveDays[i].Count = int(r.Users)
		}
	}
	// 日活 / 周活 / 月活不随 days 变：窗口最后一天就是今天
	todayStart := w.since.AddDate(0, 0, len(w.dates)-1)
	tomorrow := timestamptz(todayStart.AddDate(0, 0, 1))
	for _, span := range []struct {
		days int
		dst  *int
	}{{1, &out.DAU}, {analyticsDaysWeek, &out.WAU}, {analyticsDaysMonth, &out.MAU}} {
		from := timestamptz(todayStart.AddDate(0, 0, -(span.days - 1)))
		n, err := s.q.CountActiveUsersBetween(ctx, sqlcgen.CountActiveUsersBetweenParams{FromAt: from, ToAt: tomorrow})
		if err != nil {
			return fmt.Errorf("analytics active users of %d days: %w", span.days, err)
		}
		*span.dst = int(n)
	}
	top, err := s.q.AnalyticsTopUsers(ctx, sqlcgen.AnalyticsTopUsersParams{Since: timestamptz(w.since), RowLimit: analyticsTopUsers})
	if err != nil {
		return fmt.Errorf("analytics top users: %w", err)
	}
	out.TopUsers = make([]TopUser, 0, len(top))
	for _, r := range top {
		out.TopUsers = append(out.TopUsers, TopUser{UserID: r.UserID, Username: r.Username, PlanName: r.PlanName, Bytes: r.Bytes})
	}
	return nil
}

func (s *AnalyticsService) fillUserBreakdown(ctx context.Context, _ analyticsWindow, out *AnalyticsUsers) error {
	now := s.now()
	b, err := s.q.AnalyticsUserBreakdown(ctx, sqlcgen.AnalyticsUserBreakdownParams{
		NowAt: timestamptz(now), WeekEnd: timestamptz(now.Add(analyticsExpiryWeek)), MonthEnd: timestamptz(now.Add(analyticsExpiryMonth)),
		SilentBefore: timestamptz(now.Add(-analyticsSilentWindow)),
	})
	if err != nil {
		return fmt.Errorf("analytics user breakdown: %w", err)
	}
	out.Status = UserStatusCounts{Active: int(b.Active), Limited: int(b.Limited), Expired: int(b.Expired), Disabled: int(b.Disabled)}
	out.Expiry = UserExpiryCounts{
		Expired: int(b.ExpiryExpired), Within7d: int(b.ExpiryWeek), Within30d: int(b.ExpiryMonth), Later: int(b.ExpiryLater),
		Never: int(b.ExpiryNever), Within7dNoAutoRenew: int(b.ExpiryWeekManual),
	}
	out.SilentUsers = int(b.Silent)
	plans, err := s.q.AnalyticsPlanUsers(ctx, analyticsPlanRows)
	if err != nil {
		return fmt.Errorf("analytics plan users: %w", err)
	}
	out.Plans = make([]PlanUsers, 0, len(plans))
	for _, r := range plans {
		out.Plans = append(out.Plans, PlanUsers{PlanID: int8Ptr(r.PlanID), Name: r.Name, Users: int(r.Users), ActiveUsers: int(r.ActiveUsers)})
	}
	return nil
}

func (s *AnalyticsService) fillSubscription(ctx context.Context, w analyticsWindow, out *AnalyticsUsers) error {
	since := timestamptz(w.since)
	days, err := s.q.AnalyticsSubscribeDays(ctx, sqlcgen.AnalyticsSubscribeDaysParams{Tz: w.tz, Since: since})
	if err != nil {
		return fmt.Errorf("analytics subscribe days: %w", err)
	}
	out.SubscribeDays = make([]SubscribeDay, len(w.dates))
	for i, date := range w.dates {
		out.SubscribeDays[i].Date = date
	}
	for _, r := range days {
		if i, ok := w.slot(r.Day); ok {
			out.SubscribeDays[i].Pulls, out.SubscribeDays[i].Users = int(r.Pulls), int(r.Users)
		}
	}
	clients, err := s.q.AnalyticsSubscribeClients(ctx, since)
	if err != nil {
		return fmt.Errorf("analytics subscribe clients: %w", err)
	}
	out.SubscribeClients = make([]SubscribeClient, 0, len(clients))
	for _, r := range clients {
		out.SubscribeClients = append(out.SubscribeClients, SubscribeClient{Client: r.Client, Pulls: int(r.Pulls), Users: int(r.Users)})
	}
	formats, err := s.q.AnalyticsSubscribeFormats(ctx, since)
	if err != nil {
		return fmt.Errorf("analytics subscribe formats: %w", err)
	}
	out.SubscribeFormats = make([]SubscribeFormat, 0, len(formats))
	for _, r := range formats {
		out.SubscribeFormats = append(out.SubscribeFormats, SubscribeFormat{Format: r.Format, Pulls: int(r.Pulls)})
	}
	return nil
}

func int8Ptr(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	out := v.Int64
	return &out
}

// TrafficDay 是一天的上行与下行（字节）。
type TrafficDay struct {
	Date string
	Up   int64
	Down int64
}

// TrafficHour 是一天中某个小时的日均流量（字节）。
type TrafficHour struct {
	Hour  int
	Bytes int64
}

// ServerTraffic 是一台服务器在窗口内的流量。
type ServerTraffic struct {
	ServerID int64
	Name     string
	Up       int64
	Down     int64
}

// AnalyticsTraffic 是总览的流量分区；Servers 为 nil 表示调用方没有 server:read。
type AnalyticsTraffic struct {
	Days    []TrafficDay
	Hours   []TrafficHour
	Servers []ServerTraffic
}

// Traffic 返回流量分区；withServers 为假时不带按服务器的那一块（调用方没有 server:read）。
func (s *AnalyticsService) Traffic(ctx context.Context, days int, withServers bool) (*AnalyticsTraffic, error) {
	days, err := validAnalyticsDays(days)
	if err != nil {
		return nil, err
	}
	return cachedAnalytics(&s.cache, fmt.Sprintf("traffic:%d:%t", days, withServers), func() (*AnalyticsTraffic, error) {
		w, err := s.window(ctx, days)
		if err != nil {
			return nil, err
		}
		since := timestamptz(w.since)
		out := &AnalyticsTraffic{Days: make([]TrafficDay, days), Hours: make([]TrafficHour, analyticsHoursPerDay)}
		for i, date := range w.dates {
			out.Days[i].Date = date
		}
		daily, err := s.q.AnalyticsTrafficDays(ctx, sqlcgen.AnalyticsTrafficDaysParams{Tz: w.tz, Since: since})
		if err != nil {
			return nil, fmt.Errorf("analytics traffic days: %w", err)
		}
		for _, r := range daily {
			if i, ok := w.slot(r.Day); ok {
				out.Days[i].Up, out.Days[i].Down = r.Up, r.Down
			}
		}
		for h := range out.Hours {
			out.Hours[h].Hour = h
		}
		hourly, err := s.q.AnalyticsTrafficHours(ctx, sqlcgen.AnalyticsTrafficHoursParams{Tz: w.tz, Since: since})
		if err != nil {
			return nil, fmt.Errorf("analytics traffic hours: %w", err)
		}
		for _, r := range hourly {
			if h := int(r.HourOfDay); h >= 0 && h < analyticsHoursPerDay {
				out.Hours[h].Bytes = r.Bytes / int64(days)
			}
		}
		if !withServers {
			return out, nil
		}
		servers, err := s.q.AnalyticsTrafficServers(ctx, sqlcgen.AnalyticsTrafficServersParams{Since: since, RowLimit: analyticsTopServers})
		if err != nil {
			return nil, fmt.Errorf("analytics traffic servers: %w", err)
		}
		out.Servers = make([]ServerTraffic, 0, len(servers))
		for _, r := range servers {
			out.Servers = append(out.Servers, ServerTraffic{ServerID: r.ServerID, Name: r.Name, Up: r.Up, Down: r.Down})
		}
		return out, nil
	})
}

// ServerAvailability 是一台服务器近 7 日的可用率（百分数，一位小数）。
type ServerAvailability struct {
	ServerID int64
	Name     string
	Percent  float64
	Samples  int
	Expected int
}

// OnlinePoint 是全站在线连接数曲线上的一个点。
type OnlinePoint struct {
	At          time.Time
	Connections int
}

// ServerLoad 是一台服务器最新的负载采样；CPU 为 nil 表示面板没报。
type ServerLoad struct {
	ServerID      int64
	Name          string
	CPU           *float64
	OnlineClients int
}

// AnalyticsServers 是总览的服务器分区；Reach 为 nil 表示大陆连通检测没开。
type AnalyticsServers struct {
	Availability []ServerAvailability
	Online       []OnlinePoint
	TopCPU       []ServerLoad
	TopOnline    []ServerLoad
	Reach        *ReachSummary
}

// Servers 返回服务器分区；rng 只影响在线连接数曲线（24h / 7d，空取 24h），不合法返回 400。
func (s *AnalyticsService) Servers(ctx context.Context, rng string) (*AnalyticsServers, error) {
	if rng == "" {
		rng = MetricRange24h
	}
	if rng != MetricRange24h && rng != MetricRange7d {
		return nil, newError(CodeInvalidArgument, msgBadMetricRange)
	}
	return cachedAnalytics(&s.cache, "servers:"+rng, func() (*AnalyticsServers, error) {
		out := &AnalyticsServers{}
		var err error
		if out.Availability, err = s.serverAvailability(ctx); err != nil {
			return nil, err
		}
		if out.Online, err = s.onlineSeries(ctx, rng); err != nil {
			return nil, err
		}
		if out.TopCPU, out.TopOnline, err = s.topLoads(ctx); err != nil {
			return nil, err
		}
		if s.reach != nil {
			if out.Reach, err = s.reach.Summary(ctx); err != nil {
				return nil, err
			}
		}
		return out, nil
	})
}

func (s *AnalyticsService) serverAvailability(ctx context.Context) ([]ServerAvailability, error) {
	now := s.now()
	since := now.Add(-analyticsAvailWindow)
	rows, err := s.q.AnalyticsServerSamples(ctx, sqlcgen.AnalyticsServerSamplesParams{Since: timestamptz(since), RowLimit: analyticsMaxServers})
	if err != nil {
		return nil, fmt.Errorf("analytics server samples: %w", err)
	}
	out := make([]ServerAvailability, 0, len(rows))
	for _, r := range rows {
		from := since
		if r.CreatedAt.Valid && from.Before(r.CreatedAt.Time) {
			from = r.CreatedAt.Time
		}
		expected := max(int(now.Sub(from)/metricBucket), 1)
		samples := min(int(r.Samples), expected)
		percent := float64(samples*analyticsFullPercent*analyticsPercentScale/expected) / analyticsPercentScale
		out = append(out, ServerAvailability{ServerID: r.ID, Name: r.Name, Percent: percent, Samples: samples, Expected: expected})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Percent < out[j].Percent })
	if len(out) > analyticsAvailability {
		out = out[:analyticsAvailability]
	}
	return out, nil
}

func (s *AnalyticsService) onlineSeries(ctx context.Context, rng string) ([]OnlinePoint, error) {
	if rng == MetricRange7d {
		rows, err := s.q.AnalyticsOnlineSeriesHourly(ctx, timestamptz(s.now().Add(-metricWindow7d)))
		if err != nil {
			return nil, fmt.Errorf("analytics hourly online series: %w", err)
		}
		out := make([]OnlinePoint, 0, len(rows))
		for _, r := range rows {
			out = append(out, OnlinePoint{At: r.BucketAt.Time, Connections: int(r.Connections)})
		}
		return out, nil
	}
	rows, err := s.q.AnalyticsOnlineSeries(ctx, timestamptz(s.now().Add(-metricWindow24h)))
	if err != nil {
		return nil, fmt.Errorf("analytics online series: %w", err)
	}
	out := make([]OnlinePoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, OnlinePoint{At: r.BucketAt.Time, Connections: int(r.Connections)})
	}
	return out, nil
}

func (s *AnalyticsService) topLoads(ctx context.Context) ([]ServerLoad, []ServerLoad, error) {
	rows, err := s.q.AnalyticsLatestLoads(ctx, timestamptz(s.now().Add(-analyticsLoadWindow)))
	if err != nil {
		return nil, nil, fmt.Errorf("analytics latest loads: %w", err)
	}
	loads := make([]ServerLoad, 0, len(rows))
	for _, r := range rows {
		l := ServerLoad{ServerID: r.ServerID, Name: r.Name, OnlineClients: int(r.OnlineClients)}
		if r.Cpu.Valid {
			v := r.Cpu.Float64
			l.CPU = &v
		}
		loads = append(loads, l)
	}
	byCPU := make([]ServerLoad, 0, len(loads))
	for _, l := range loads {
		if l.CPU != nil {
			byCPU = append(byCPU, l)
		}
	}
	sort.SliceStable(byCPU, func(i, j int) bool { return *byCPU[i].CPU > *byCPU[j].CPU })
	byOnline := append(make([]ServerLoad, 0, len(loads)), loads...)
	sort.SliceStable(byOnline, func(i, j int) bool { return byOnline[i].OnlineClients > byOnline[j].OnlineClients })
	return firstLoads(byCPU), firstLoads(byOnline), nil
}

func firstLoads(loads []ServerLoad) []ServerLoad {
	if len(loads) > analyticsTopLoads {
		return loads[:analyticsTopLoads]
	}
	return loads
}

// FinanceDay 是一天的订单实付与充值入账（分）。
type FinanceDay struct {
	Date      string
	OrderPaid int64
	Topup     int64
}

// OrderKindStats 是一类订单的笔数与实付。
type OrderKindStats struct {
	Kind  string
	Count int
	Paid  int64
}

// PlanSales 是一个套餐的销售；PlanID 为 nil 表示套餐已删除（合并为一行）。
type PlanSales struct {
	PlanID *int64
	Name   string
	Count  int
	Paid   int64
}

// CouponStats 是一个优惠码的使用情况。
type CouponStats struct {
	Code     string
	Count    int
	Discount int64
	Paid     int64
}

// AnalyticsFinance 是总览的财务分区；金额为整数分。
type AnalyticsFinance struct {
	Days                                           []FinanceDay
	OrderKinds                                     []OrderKindStats
	TrialCount                                     int
	Plans                                          []PlanSales
	FunnelRegistered, FunnelTrial, FunnelPaid      int
	Payers                                         int
	PayersPaid                                     int64
	BalanceTotal                                   int64
	PaymentsPaid, PaymentsExpired, PaymentsPending int
	Coupons                                        []CouponStats
}

// Finance 返回财务分区；days 只能是 7 / 30 / 90（0 取 30），否则返回 400。
func (s *AnalyticsService) Finance(ctx context.Context, days int) (*AnalyticsFinance, error) {
	days, err := validAnalyticsDays(days)
	if err != nil {
		return nil, err
	}
	return cachedAnalytics(&s.cache, fmt.Sprintf("finance:%d", days), func() (*AnalyticsFinance, error) {
		w, err := s.window(ctx, days)
		if err != nil {
			return nil, err
		}
		out := &AnalyticsFinance{}
		if err := s.fillFinanceDays(ctx, w, out); err != nil {
			return nil, err
		}
		if err := s.fillOrders(ctx, w, out); err != nil {
			return nil, err
		}
		if err := s.fillFinanceTotals(ctx, w, out); err != nil {
			return nil, err
		}
		return out, nil
	})
}

func (s *AnalyticsService) fillFinanceDays(ctx context.Context, w analyticsWindow, out *AnalyticsFinance) error {
	since := timestamptz(w.since)
	out.Days = make([]FinanceDay, len(w.dates))
	for i, date := range w.dates {
		out.Days[i].Date = date
	}
	orders, err := s.q.DailyOrders(ctx, sqlcgen.DailyOrdersParams{Tz: w.tz, Since: since})
	if err != nil {
		return fmt.Errorf("analytics daily orders: %w", err)
	}
	for _, r := range orders {
		if i, ok := w.slot(r.Day); ok {
			out.Days[i].OrderPaid = r.Total
		}
	}
	topups, err := s.q.DailyTopups(ctx, sqlcgen.DailyTopupsParams{Tz: w.tz, Since: since})
	if err != nil {
		return fmt.Errorf("analytics daily topups: %w", err)
	}
	for _, r := range topups {
		if i, ok := w.slot(r.Day); ok {
			out.Days[i].Topup = r.Total
		}
	}
	return nil
}

func (s *AnalyticsService) fillOrders(ctx context.Context, w analyticsWindow, out *AnalyticsFinance) error {
	since := timestamptz(w.since)
	kinds, err := s.q.AnalyticsOrderKinds(ctx, since)
	if err != nil {
		return fmt.Errorf("analytics order kinds: %w", err)
	}
	// 三类固定都出，没有订单的为 0
	out.OrderKinds = []OrderKindStats{{Kind: orderKindPlan}, {Kind: orderKindRenew}, {Kind: orderKindTrafficPack}}
	for _, r := range kinds {
		for i := range out.OrderKinds {
			if out.OrderKinds[i].Kind == r.Kind {
				out.OrderKinds[i].Count, out.OrderKinds[i].Paid = int(r.Orders), r.Paid
			}
		}
	}
	sum, err := s.q.AnalyticsOrderSummary(ctx, since)
	if err != nil {
		return fmt.Errorf("analytics order summary: %w", err)
	}
	out.TrialCount, out.Payers, out.PayersPaid = int(sum.TrialCount), int(sum.Payers), sum.Paid
	plans, err := s.q.AnalyticsPlanSales(ctx, sqlcgen.AnalyticsPlanSalesParams{Since: since, RowLimit: analyticsTopPlans})
	if err != nil {
		return fmt.Errorf("analytics plan sales: %w", err)
	}
	out.Plans = make([]PlanSales, 0, len(plans))
	for _, r := range plans {
		out.Plans = append(out.Plans, PlanSales{PlanID: int8Ptr(r.PlanID), Name: r.Name, Count: int(r.Orders), Paid: r.Paid})
	}
	coupons, err := s.q.AnalyticsCoupons(ctx, sqlcgen.AnalyticsCouponsParams{Since: since, RowLimit: analyticsTopCoupons})
	if err != nil {
		return fmt.Errorf("analytics coupons: %w", err)
	}
	out.Coupons = make([]CouponStats, 0, len(coupons))
	for _, r := range coupons {
		out.Coupons = append(out.Coupons, CouponStats{Code: r.CouponCode, Count: int(r.Orders), Discount: r.Discount, Paid: r.Paid})
	}
	return nil
}

func (s *AnalyticsService) fillFinanceTotals(ctx context.Context, w analyticsWindow, out *AnalyticsFinance) error {
	since := timestamptz(w.since)
	funnel, err := s.q.AnalyticsFunnel(ctx, since)
	if err != nil {
		return fmt.Errorf("analytics funnel: %w", err)
	}
	out.FunnelRegistered, out.FunnelTrial, out.FunnelPaid = int(funnel.Registered), int(funnel.Trial), int(funnel.Paid)
	if out.BalanceTotal, err = s.q.SumUserBalances(ctx); err != nil {
		return fmt.Errorf("analytics balance total: %w", err)
	}
	pay, err := s.q.AnalyticsPayments(ctx, since)
	if err != nil {
		return fmt.Errorf("analytics payments: %w", err)
	}
	out.PaymentsPaid, out.PaymentsExpired, out.PaymentsPending = int(pay.Paid), int(pay.Expired), int(pay.Pending)
	return nil
}

// BacklogTickets 是工单积压；OldestWaitingSince 为 nil 表示没有待回复工单，AvgFirstReplyMinutes 为 nil 表示近 30 日没有已回复的新工单。
type BacklogTickets struct {
	Open                 int
	OldestWaitingSince   *time.Time
	AvgFirstReplyMinutes *int
}

// ServerCount 是按服务器的一个计数。
type ServerCount struct {
	ServerID int64
	Name     string
	Count    int
}

// BacklogProvisions 是下发失败的 client：总数与按服务器的前几名。
type BacklogProvisions struct {
	Failed  int
	Servers []ServerCount
}

// ChainFailure 是一台链式下发失败的服务器。
type ChainFailure struct {
	ServerID      int64
	Name          string
	FailCount     int
	LastAttemptAt *time.Time
}

// BacklogPanels 是面板侧要人工处理的事项。
type BacklogPanels struct {
	ChainFailed     []ChainFailure
	DriftInbounds   int
	MissingInbounds int
}

// BacklogOutbox 是发件箱的积压与近 24 小时的失败数。
type BacklogOutbox struct {
	MailPending, MailFailed24h, TelegramPending, TelegramFailed24h int
}

// AnalyticsBacklog 是总览的待处理分区；缺权限的块为 nil。
type AnalyticsBacklog struct {
	Tickets    *BacklogTickets
	Provisions *BacklogProvisions
	Panels     *BacklogPanels
	Outbox     *BacklogOutbox
}

// Backlog 按管理员的权限码返回待处理分区。各块都是小查询，不缓存。
func (s *AnalyticsService) Backlog(ctx context.Context, p *AdminPrincipal) (*AnalyticsBacklog, error) {
	out := &AnalyticsBacklog{}
	var err error
	if p.Can(PermTicketRead) {
		if out.Tickets, err = s.backlogTickets(ctx); err != nil {
			return nil, err
		}
	}
	if p.Can(PermUserRead) {
		if out.Provisions, err = s.backlogProvisions(ctx); err != nil {
			return nil, err
		}
	}
	if p.Can(PermServerRead) {
		if out.Panels, err = s.backlogPanels(ctx); err != nil {
			return nil, err
		}
	}
	if p.Can(PermSettingRead) {
		if out.Outbox, err = s.backlogOutbox(ctx); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *AnalyticsService) backlogTickets(ctx context.Context) (*BacklogTickets, error) {
	row, err := s.q.AnalyticsTicketBacklog(ctx)
	if err != nil {
		return nil, fmt.Errorf("analytics ticket backlog: %w", err)
	}
	out := &BacklogTickets{Open: int(row.Open), OldestWaitingSince: timePtr(row.OldestAt)}
	reply, err := s.q.AnalyticsFirstReply(ctx, timestamptz(s.now().Add(-analyticsFirstReplyWindow)))
	if err != nil {
		return nil, fmt.Errorf("analytics first reply: %w", err)
	}
	if reply.Replied > 0 {
		minutes := int(reply.TotalSeconds / int64(reply.Replied) / secondsPerMinute)
		out.AvgFirstReplyMinutes = &minutes
	}
	return out, nil
}

func (s *AnalyticsService) backlogProvisions(ctx context.Context) (*BacklogProvisions, error) {
	rows, err := s.q.AnalyticsFailedProvisions(ctx)
	if err != nil {
		return nil, fmt.Errorf("analytics failed provisions: %w", err)
	}
	out := &BacklogProvisions{Servers: make([]ServerCount, 0, analyticsBacklogServers)}
	for _, r := range rows {
		out.Failed += int(r.Failed)
		if len(out.Servers) < analyticsBacklogServers {
			out.Servers = append(out.Servers, ServerCount{ServerID: r.ServerID, Name: r.Name, Count: int(r.Failed)})
		}
	}
	return out, nil
}

func (s *AnalyticsService) backlogPanels(ctx context.Context) (*BacklogPanels, error) {
	rows, err := s.q.AnalyticsChainFailed(ctx, analyticsBacklogServers)
	if err != nil {
		return nil, fmt.Errorf("analytics chain failed: %w", err)
	}
	out := &BacklogPanels{ChainFailed: make([]ChainFailure, 0, len(rows))}
	for _, r := range rows {
		out.ChainFailed = append(out.ChainFailed, ChainFailure{
			ServerID: r.ServerID, Name: r.Name, FailCount: int(r.FailCount), LastAttemptAt: timePtr(r.LastAttemptAt),
		})
	}
	issues, err := s.q.AnalyticsInboundIssues(ctx)
	if err != nil {
		return nil, fmt.Errorf("analytics inbound issues: %w", err)
	}
	out.DriftInbounds, out.MissingInbounds = int(issues.Drift), int(issues.Missing)
	return out, nil
}

func (s *AnalyticsService) backlogOutbox(ctx context.Context) (*BacklogOutbox, error) {
	row, err := s.q.AnalyticsOutbox(ctx, timestamptz(s.now().Add(-analyticsOutboxWindow)))
	if err != nil {
		return nil, fmt.Errorf("analytics outbox: %w", err)
	}
	return &BacklogOutbox{
		MailPending: int(row.MailPending), MailFailed24h: int(row.MailFailed),
		TelegramPending: int(row.TelegramPending), TelegramFailed24h: int(row.TelegramFailed),
	}, nil
}
