package service

import "time"

// 用户状态（docs/03 §2）。
const (
	UserStatusActive   = "active"
	UserStatusLimited  = "limited"
	UserStatusExpired  = "expired"
	UserStatusDisabled = "disabled"
)

// 周期流量重置（docs/03 §5.2，取值同 3x-ui 的 trafficReset）。
const (
	TrafficResetNever   = "never"
	TrafficResetHourly  = "hourly"
	TrafficResetDaily   = "daily"
	TrafficResetWeekly  = "weekly"
	TrafficResetMonthly = "monthly"
)

const (
	daysPerWeek = 7
	// isoSunday 是“每周几”里的周日（1–7 = 周一至周日），对应 time.Sunday。
	isoSunday = 7
)

var trafficResetCycles = map[string]bool{
	TrafficResetNever: true, TrafficResetHourly: true, TrafficResetDaily: true, TrafficResetWeekly: true, TrafficResetMonthly: true,
}

// Quota 是用户的配额状态，模型照 3x-ui 的 client：额度、到期、首次使用后生效、自动续期、周期重置（docs/03 §3 U1–U9）。
// 只含规则需要的字段，便于表驱动测试；持久化由调用方负责。
type Quota struct {
	TrafficLimit       int64 // 字节，0 不限
	TrafficUsed        int64
	ExpiresAt          *time.Time // nil = 不过期，或 PendingDays > 0 时等待首次使用
	PendingDays        int
	LimitIP            int
	RenewEveryDays     int
	RenewMonthDay      int
	RenewWeekday       int // 1–7 = 周一至周日
	RenewMax           int // 0 不限
	RenewCount         int
	TrafficReset       string
	TrafficResetDay    int
	LastTrafficResetAt time.Time
	Disabled           bool
}

// PlanQuota 是套餐里会拷给用户的配额模板。
type PlanQuota struct {
	TrafficLimit    int64
	DurationDays    int
	StartOnFirstUse bool
	LimitIP         int
	RenewEveryDays  int
	RenewMonthDay   int
	RenewWeekday    int
	RenewMax        int
	TrafficReset    string
	TrafficResetDay int
}

// Status 按优先级判定用户状态：停用 > 到期 > 流量用尽 > 正常（docs/03 §2）。
func (q Quota) Status(now time.Time) string {
	switch {
	case q.Disabled:
		return UserStatusDisabled
	case q.ExpiresAt != nil && !now.Before(*q.ExpiresAt):
		return UserStatusExpired
	case q.TrafficLimit > 0 && q.TrafficUsed >= q.TrafficLimit:
		return UserStatusLimited
	default:
		return UserStatusActive
	}
}

func (q Quota) hasRenewal() bool {
	return q.RenewEveryDays > 0 || q.RenewMonthDay > 0 || q.RenewWeekday > 0
}

// AddTraffic 累加一次增量（U2 的输入）；“首次使用后生效”的用户第一次有流量时开始计时（U9）。
func (q *Quota) AddTraffic(up, down int64, now time.Time) {
	if up+down <= 0 {
		return
	}
	q.TrafficUsed += up + down
	if q.ExpiresAt == nil && q.PendingDays > 0 {
		at := now.AddDate(0, 0, q.PendingDays)
		q.ExpiresAt = &at
		q.PendingDays = 0
	}
}

// Advance 按时间推进：到期且有自动续期时顺延（U4），到了周期重置点就清零已用（U5）。返回是否有改动。
func (q *Quota) Advance(now time.Time, loc *time.Location) bool {
	changed := q.autoRenew(now, loc)
	if q.periodicResetDue(now, loc) {
		q.TrafficUsed = 0
		q.LastTrafficResetAt = now
		changed = true
	}
	return changed
}

// autoRenew 照 3x-ui 的 catch-up：从原到期时间按规则一步步顺延到晚于当前时刻，每步计一次续期，到次数上限为止。
func (q *Quota) autoRenew(now time.Time, loc *time.Location) bool {
	if q.ExpiresAt == nil || now.Before(*q.ExpiresAt) || !q.hasRenewal() {
		return false
	}
	expiry := *q.ExpiresAt
	renewals := 0
	for !now.Before(expiry) {
		if q.RenewMax > 0 && q.RenewCount+renewals >= q.RenewMax {
			break
		}
		next := q.nextRenewal(expiry, loc)
		if !next.After(expiry) {
			return false
		}
		expiry = next
		renewals++
	}
	if renewals == 0 {
		return false
	}
	q.ExpiresAt = &expiry
	q.RenewCount += renewals
	q.TrafficUsed = 0
	q.LastTrafficResetAt = now
	return true
}

func (q Quota) nextRenewal(from time.Time, loc *time.Location) time.Time {
	switch {
	case q.RenewMonthDay > 0:
		return nextMonthDay(from, q.RenewMonthDay, loc)
	case q.RenewWeekday > 0:
		return nextWeekday(from, q.RenewWeekday, loc)
	default:
		return from.AddDate(0, 0, q.RenewEveryDays)
	}
}

// nextMonthDay 返回严格晚于 from 的下一个“每月 day 号 0 点”；当月没有该日时取月末（照 3x-ui，不滚到下月）。
func nextMonthDay(from time.Time, day int, loc *time.Location) time.Time {
	local := from.In(loc)
	candidate := clampedDate(local.Year(), local.Month(), day, loc)
	if !candidate.After(local) {
		candidate = clampedDate(local.Year(), local.Month()+1, day, loc)
	}
	return candidate
}

func clampedDate(year int, month time.Month, day int, loc *time.Location) time.Time {
	first := time.Date(year, month, 1, 0, 0, 0, 0, loc)
	last := first.AddDate(0, 1, -1).Day()
	return time.Date(first.Year(), first.Month(), min(day, last), 0, 0, 0, 0, loc)
}

// nextWeekday 返回严格晚于 from 的下一个“每周 weekday 0 点”（1–7 = 周一至周日）。
func nextWeekday(from time.Time, weekday int, loc *time.Location) time.Time {
	local := from.In(loc)
	target := time.Weekday(weekday % isoSunday)
	days := (int(target) - int(local.Weekday()) + daysPerWeek) % daysPerWeek
	candidate := time.Date(local.Year(), local.Month(), local.Day()+days, 0, 0, 0, 0, loc)
	if !candidate.After(local) {
		candidate = candidate.AddDate(0, 0, daysPerWeek)
	}
	return candidate
}

// periodicResetDue 判断上次重置之后是否越过了一个重置点（照 3x-ui：整点、0 点、周日 0 点、每月指定日 0 点）。
func (q Quota) periodicResetDue(now time.Time, loc *time.Location) bool {
	boundary, ok := lastResetBoundary(q.TrafficReset, q.TrafficResetDay, now, loc)
	return ok && q.LastTrafficResetAt.Before(boundary)
}

func lastResetBoundary(cycle string, day int, now time.Time, loc *time.Location) (time.Time, bool) {
	local := now.In(loc)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	switch cycle {
	case TrafficResetHourly:
		return time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, loc), true
	case TrafficResetDaily:
		return midnight, true
	case TrafficResetWeekly:
		return midnight.AddDate(0, 0, -int(local.Weekday())), true
	case TrafficResetMonthly:
		b := clampedDate(local.Year(), local.Month(), day, loc)
		if b.After(local) {
			b = clampedDate(local.Year(), local.Month()-1, day, loc)
		}
		return b, true
	default:
		return time.Time{}, false
	}
}

// ApplyPlan 分配或续费套餐（U6）：拷入套餐配额；未到期从原到期日顺延，已到期从当前时刻起算；已用清零。
func (q *Quota) ApplyPlan(p PlanQuota, now time.Time) {
	q.TrafficLimit = p.TrafficLimit
	q.LimitIP = p.LimitIP
	q.RenewEveryDays, q.RenewMonthDay, q.RenewWeekday, q.RenewMax = p.RenewEveryDays, p.RenewMonthDay, p.RenewWeekday, p.RenewMax
	q.RenewCount = 0
	q.TrafficReset, q.TrafficResetDay = p.TrafficReset, p.TrafficResetDay
	q.TrafficUsed = 0
	q.LastTrafficResetAt = now
	switch {
	case p.DurationDays == 0:
		q.ExpiresAt, q.PendingDays = nil, 0
	case q.ExpiresAt != nil && q.ExpiresAt.After(now):
		at := q.ExpiresAt.AddDate(0, 0, p.DurationDays)
		q.ExpiresAt = &at
	case q.ExpiresAt == nil && q.PendingDays > 0:
		q.PendingDays += p.DurationDays
	case p.StartOnFirstUse:
		q.ExpiresAt, q.PendingDays = nil, p.DurationDays
	default:
		at := now.AddDate(0, 0, p.DurationDays)
		q.ExpiresAt, q.PendingDays = &at, 0
	}
}

// renewalModes 统计设置了几种自动续期方式（最多一种，照 3x-ui 的校验）。
func renewalModes(everyDays, monthDay, weekday int) int {
	n := 0
	for _, v := range []int{everyDays, monthDay, weekday} {
		if v > 0 {
			n++
		}
	}
	return n
}

// hourOf 取所在整点，用于按小时汇总用量。
func hourOf(t time.Time) time.Time {
	return t.Truncate(time.Hour)
}
