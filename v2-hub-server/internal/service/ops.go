package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
)

// 运营（docs/03 §5.20）：管理端仪表盘、掉线告警与恢复通知（Ruling-43）、运营日报。

const (
	ActionServerAlert    = "server.alert"
	ActionServerRecovery = "server.recovery"
	ActionDailyReport    = "ops.daily_report"

	jobKeyDailyReport  = "report.daily.lastDate"
	dashboardOffline   = 10
	dashboardTrafficDy = 7
	minutesPerHour     = 60
)

// OpsService 负责仪表盘汇总、掉线告警与日报。告警与日报经邮件发件箱与 Telegram 发件箱发出。
type OpsService struct {
	store
	settings *SettingsService
	mail     *MailService
	telegram *TelegramService
	now      func() time.Time
}

// NewOpsService 创建运营服务；mail、telegram 可为不可用状态（不发对应渠道），now 为 nil 时用 time.Now。
func NewOpsService(pool *pgxpool.Pool, settings *SettingsService, mail *MailService, telegram *TelegramService, now func() time.Time) *OpsService {
	if now == nil {
		now = time.Now
	}
	return &OpsService{store: newStore(pool), settings: settings, mail: mail, telegram: telegram, now: now}
}

// broadcast 在事务里把一条运营通知发到全部渠道：邮件发给 alert.emails，Telegram 按事件开关。
func (s *OpsService) broadcast(ctx context.Context, q *sqlcgen.Queries, cur Settings, event, title, body string) {
	if s.mail != nil && s.mail.Enabled() && len(cur.Alert.Emails) > 0 {
		content, err := renderMail(mailTplOps, mailVars{Site: cur.Site.Name, Title: title, Body: body})
		if err != nil {
			slog.Error("render ops mail", "event", event, "err", err)
		} else {
			for _, to := range cur.Alert.Emails {
				if err := s.mail.enqueue(ctx, q, to, content, mailPriorityNotice); err != nil {
					slog.Error("enqueue ops mail", "event", event, "to", maskEmail(to), "err", err)
				}
			}
		}
	}
	s.telegram.Notify(ctx, q, event, "【"+cur.Site.Name+"】"+title+"\n\n"+body)
}

// formatDuration 把时长写成“X 小时 Y 分钟”。
func formatDuration(d time.Duration) string {
	minutes := int(d.Minutes())
	if minutes < minutesPerHour {
		return fmt.Sprintf("%d 分钟", max(minutes, 1))
	}
	return fmt.Sprintf("%d 小时 %d 分钟", minutes/minutesPerHour, minutes%minutesPerHour)
}

// CheckAlerts 检查一轮掉线告警与恢复通知（docs/03 §5.20 掉线告警）：每台服务器一次掉线只告警一次，恢复时若告警过则补一条恢复通知。
func (s *OpsService) CheckAlerts(ctx context.Context) error {
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	due, err := s.q.ListAlertDueServers(ctx, pgtype.Timestamptz{Time: now.Add(-time.Duration(cur.Alert.OfflineMinutes) * time.Minute), Valid: true})
	if err != nil {
		return fmt.Errorf("list alert due servers: %w", err)
	}
	for _, sv := range due {
		title := fmt.Sprintf("服务器 %s 不在线", sv.Name)
		body := fmt.Sprintf("状态：%s\n已持续：%s\n最近错误：%s", sv.Status, formatDuration(now.Sub(sv.OfflineSince.Time)), orDash(sv.LastError))
		err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
			if err := q.MarkServerAlerted(ctx, sqlcgen.MarkServerAlertedParams{ID: sv.ID, AlertedAt: pgtype.Timestamptz{Time: now, Valid: true}}); err != nil {
				return fmt.Errorf("mark server %d alerted: %w", sv.ID, err)
			}
			s.broadcast(ctx, q, cur, TelegramEventAlert, title, body)
			return writeAudit(ctx, q, AuditEntry{ActorType: ActorSystem, Action: ActionServerAlert, TargetType: targetServer, TargetID: fmt.Sprint(sv.ID),
				Detail: map[string]any{"status": sv.Status}})
		})
		if err != nil {
			return err
		}
	}
	recovered, err := s.q.ListRecoveredServers(ctx)
	if err != nil {
		return fmt.Errorf("list recovered servers: %w", err)
	}
	for _, sv := range recovered {
		err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
			if err := q.ClearServerAlert(ctx, sv.ID); err != nil {
				return fmt.Errorf("clear alert of server %d: %w", sv.ID, err)
			}
			// 被停用的服务器只清标记，不发恢复通知（docs/03 §5.20 停用）
			if sv.IsDisabled {
				return nil
			}
			title := fmt.Sprintf("服务器 %s 已恢复在线", sv.Name)
			body := fmt.Sprintf("告警发出于：%s", sv.AlertSentAt.Time.In(cur.Location()).Format(time.DateTime))
			s.broadcast(ctx, q, cur, TelegramEventAlert, title, body)
			return writeAudit(ctx, q, AuditEntry{ActorType: ActorSystem, Action: ActionServerRecovery, TargetType: targetServer, TargetID: fmt.Sprint(sv.ID)})
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// dayBounds 返回 t 所在自然日（loc 时区）的起止。
func dayBounds(t time.Time, loc *time.Location) (time.Time, time.Time) {
	local := t.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return start, start.AddDate(0, 0, 1)
}

func ts(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// DailyReport 到点后发前一自然日的运营日报，每天只发一次（docs/03 §5.20 运营日报）。返回是否发出。
func (s *OpsService) DailyReport(ctx context.Context) (bool, error) {
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return false, err
	}
	if !cur.Report.DailyEnabled {
		return false, nil
	}
	loc := cur.Location()
	now := s.now().In(loc)
	if now.Hour() < cur.Report.DailyHour {
		return false, nil
	}
	todayStart, _ := dayBounds(now, loc)
	from, to := todayStart.AddDate(0, 0, -1), todayStart
	day := from.Format(dateLayout)
	last, err := s.q.GetJobState(ctx, jobKeyDailyReport)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("get daily report state: %w", err)
	}
	if last == day {
		return false, nil
	}
	body, err := s.reportBody(ctx, from, to)
	if err != nil {
		return false, err
	}
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.UpsertJobState(ctx, sqlcgen.UpsertJobStateParams{Key: jobKeyDailyReport, Value: day}); err != nil {
			return fmt.Errorf("save daily report state: %w", err)
		}
		s.broadcast(ctx, q, cur, TelegramEventReport, "运营日报 "+day, body)
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorSystem, Action: ActionDailyReport, TargetType: targetSetting, Detail: map[string]any{"date": day}})
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

// reportBody 汇总 [from, to) 的日报内容；金额按分换成元显示。
func (s *OpsService) reportBody(ctx context.Context, from, to time.Time) (string, error) {
	between := func(name string, fn func() (int64, error)) (int64, error) {
		v, err := fn()
		if err != nil {
			return 0, fmt.Errorf("daily report %s: %w", name, err)
		}
		return v, nil
	}
	newUsers, err := between("new users", func() (int64, error) {
		n, err := s.q.CountUsersCreatedBetween(ctx, sqlcgen.CountUsersCreatedBetweenParams{FromAt: ts(from), ToAt: ts(to)})
		return int64(n), err
	})
	if err != nil {
		return "", err
	}
	traffic, err := between("traffic", func() (int64, error) {
		return s.q.SumTrafficBetween(ctx, sqlcgen.SumTrafficBetweenParams{FromAt: ts(from), ToAt: ts(to)})
	})
	if err != nil {
		return "", err
	}
	spend, err := between("orders", func() (int64, error) {
		return s.q.SumOrdersBetween(ctx, sqlcgen.SumOrdersBetweenParams{FromAt: ts(from), ToAt: ts(to)})
	})
	if err != nil {
		return "", err
	}
	topup, err := between("topups", func() (int64, error) {
		return s.q.SumTopupsBetween(ctx, sqlcgen.SumTopupsBetweenParams{FromAt: ts(from), ToAt: ts(to)})
	})
	if err != nil {
		return "", err
	}
	tickets, err := between("tickets", func() (int64, error) {
		n, err := s.q.CountTicketsCreatedBetween(ctx, sqlcgen.CountTicketsCreatedBetweenParams{FromAt: ts(from), ToAt: ts(to)})
		return int64(n), err
	})
	if err != nil {
		return "", err
	}
	offline, err := s.q.CountOfflineServers(ctx)
	if err != nil {
		return "", fmt.Errorf("daily report offline servers: %w", err)
	}
	users, err := s.q.DashboardUsers(ctx, sqlcgen.DashboardUsersParams{TodayStart: ts(to), WeekStart: ts(to)})
	if err != nil {
		return "", fmt.Errorf("daily report users: %w", err)
	}
	lines := []string{
		fmt.Sprintf("新增用户：%d", newUsers),
		fmt.Sprintf("有效用户：%d", users.Active),
		fmt.Sprintf("流量合计：%.2f GB", float64(traffic)/float64(bytesPerGiB)),
		fmt.Sprintf("订单实付：%s", formatCents(spend)),
		fmt.Sprintf("充值入账：%s", formatCents(topup)),
		fmt.Sprintf("新工单：%d", tickets),
		fmt.Sprintf("当前不在线服务器：%d", offline),
	}
	return strings.Join(lines, "\n"), nil
}

// formatCents 把分写成两位小数的元。
func formatCents(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/moneyCentsPerUnit, cents%moneyCentsPerUnit)
}

// DashboardUsers 是仪表盘的用户区。
type DashboardUsers struct {
	Total, Active, NewToday, New7d, Online int
}

// DashboardTraffic 是仪表盘的流量区：今日与近 7 个自然日。
type DashboardTraffic struct {
	Today int64
	Days  []DashboardDay
}

// DashboardDay 是一天的流量。
type DashboardDay struct {
	Date  string
	Bytes int64
}

// DashboardServers 是仪表盘的服务器区。
type DashboardServers struct {
	Total, Online, NodeCount int
	Offline                  []OfflineServer
}

// OfflineServer 是一台启用中但不在线的服务器。
type OfflineServer struct {
	ID           int64
	Name         string
	Status       string
	OfflineSince *time.Time
}

// DashboardFinance 是仪表盘的财务区（整数分）。
type DashboardFinance struct {
	TodaySpend, TodayTopup, MonthSpend, MonthTopup int64
}

// AdminDashboard 是管理端总览；缺权限的区块为 nil（docs/03 §5.20 仪表盘）。
type AdminDashboard struct {
	Users       *DashboardUsers
	Traffic     *DashboardTraffic
	Servers     *DashboardServers
	Finance     *DashboardFinance
	OpenTickets *int
}

// Dashboard 按管理员的权限码汇总总览。
func (s *OpsService) Dashboard(ctx context.Context, p *AdminPrincipal) (*AdminDashboard, error) {
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return nil, err
	}
	loc := cur.Location()
	now := s.now()
	todayStart, _ := dayBounds(now, loc)
	out := &AdminDashboard{}
	if p.Can(PermUserRead) {
		if out.Users, out.Traffic, err = s.dashboardUsers(ctx, loc, todayStart); err != nil {
			return nil, err
		}
	}
	if p.Can(PermServerRead) {
		if out.Servers, err = s.dashboardServers(ctx); err != nil {
			return nil, err
		}
	}
	if p.Can(PermFinanceRead) {
		if out.Finance, err = s.dashboardFinance(ctx, loc, now, todayStart); err != nil {
			return nil, err
		}
	}
	if p.Can(PermTicketRead) {
		n, err := s.q.CountOpenTickets(ctx)
		if err != nil {
			return nil, fmt.Errorf("count open tickets: %w", err)
		}
		v := int(n)
		out.OpenTickets = &v
	}
	return out, nil
}

func (s *OpsService) dashboardUsers(ctx context.Context, loc *time.Location, todayStart time.Time) (*DashboardUsers, *DashboardTraffic, error) {
	weekStart := todayStart.AddDate(0, 0, -(dashboardTrafficDy - 1))
	u, err := s.q.DashboardUsers(ctx, sqlcgen.DashboardUsersParams{TodayStart: ts(todayStart), WeekStart: ts(weekStart)})
	if err != nil {
		return nil, nil, fmt.Errorf("dashboard users: %w", err)
	}
	users := &DashboardUsers{Total: int(u.Total), Active: int(u.Active), NewToday: int(u.NewToday), New7d: int(u.New7d), Online: int(u.Online)}
	rows, err := s.q.DailyTraffic(ctx, sqlcgen.DailyTrafficParams{Tz: loc.String(), Since: ts(weekStart)})
	if err != nil {
		return nil, nil, fmt.Errorf("dashboard traffic: %w", err)
	}
	byDay := make(map[string]int64, len(rows))
	for _, r := range rows {
		if r.Day.Valid {
			byDay[r.Day.Time.Format(dateLayout)] = r.Bytes
		}
	}
	traffic := &DashboardTraffic{Days: make([]DashboardDay, dashboardTrafficDy)}
	for i := range dashboardTrafficDy {
		date := weekStart.AddDate(0, 0, i).Format(dateLayout)
		traffic.Days[i] = DashboardDay{Date: date, Bytes: byDay[date]}
	}
	traffic.Today = traffic.Days[dashboardTrafficDy-1].Bytes
	return users, traffic, nil
}

func (s *OpsService) dashboardServers(ctx context.Context) (*DashboardServers, error) {
	sv, err := s.q.DashboardServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("dashboard servers: %w", err)
	}
	rows, err := s.q.ListOfflineServers(ctx, dashboardOffline)
	if err != nil {
		return nil, fmt.Errorf("list offline servers: %w", err)
	}
	out := &DashboardServers{Total: int(sv.Total), Online: int(sv.Online), NodeCount: int(sv.NodeCount), Offline: make([]OfflineServer, 0, len(rows))}
	for _, r := range rows {
		out.Offline = append(out.Offline, OfflineServer{ID: r.ID, Name: r.Name, Status: r.Status, OfflineSince: timePtr(r.OfflineSince)})
	}
	return out, nil
}

func (s *OpsService) dashboardFinance(ctx context.Context, loc *time.Location, now, todayStart time.Time) (*DashboardFinance, error) {
	local := now.In(loc)
	monthStart := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
	end := todayStart.AddDate(0, 0, 1)
	sum := func(fn func(sqlcgen.SumOrdersBetweenParams) (int64, error), from time.Time) (int64, error) {
		return fn(sqlcgen.SumOrdersBetweenParams{FromAt: ts(from), ToAt: ts(end)})
	}
	orders := func(p sqlcgen.SumOrdersBetweenParams) (int64, error) { return s.q.SumOrdersBetween(ctx, p) }
	topups := func(p sqlcgen.SumOrdersBetweenParams) (int64, error) {
		return s.q.SumTopupsBetween(ctx, sqlcgen.SumTopupsBetweenParams{FromAt: p.FromAt, ToAt: p.ToAt})
	}
	var out DashboardFinance
	var err error
	if out.TodaySpend, err = sum(orders, todayStart); err != nil {
		return nil, fmt.Errorf("dashboard today spend: %w", err)
	}
	if out.TodayTopup, err = sum(topups, todayStart); err != nil {
		return nil, fmt.Errorf("dashboard today topup: %w", err)
	}
	if out.MonthSpend, err = sum(orders, monthStart); err != nil {
		return nil, fmt.Errorf("dashboard month spend: %w", err)
	}
	if out.MonthTopup, err = sum(topups, monthStart); err != nil {
		return nil, fmt.Errorf("dashboard month topup: %w", err)
	}
	return &out, nil
}
