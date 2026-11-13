package service

import (
	"testing"
	"time"
)

const gib = int64(1) << 30

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.UTC)
	if err != nil {
		panic(err)
	}
	return t
}

func ptr(t time.Time) *time.Time { return &t }

func TestQuotaStatusPriority(t *testing.T) {
	now := at("2026-11-12 10:00")
	cases := []struct {
		name string
		q    Quota
		want string
	}{
		{"unlimited", Quota{}, UserStatusActive},
		{"limited", Quota{TrafficLimit: gib, TrafficUsed: gib}, UserStatusLimited},
		{"expired beats limited", Quota{TrafficLimit: gib, TrafficUsed: gib, ExpiresAt: ptr(now)}, UserStatusExpired},
		{"disabled beats all", Quota{Disabled: true, ExpiresAt: ptr(now.Add(-time.Hour))}, UserStatusDisabled},
		{"waiting first use is active", Quota{PendingDays: 30}, UserStatusActive},
		{"not yet expired", Quota{ExpiresAt: ptr(now.Add(time.Minute))}, UserStatusActive},
	}
	for _, c := range cases {
		if got := c.q.Status(now); got != c.want {
			t.Errorf("%s: status = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestQuotaFirstUseStartsClock(t *testing.T) {
	now := at("2026-11-12 10:00")
	q := Quota{PendingDays: 30}
	q.AddTraffic(0, 0, now)
	if q.ExpiresAt != nil {
		t.Fatal("zero traffic must not start the clock")
	}
	q.AddTraffic(100, 200, now)
	if q.TrafficUsed != 300 || q.PendingDays != 0 || !q.ExpiresAt.Equal(at("2026-12-12 10:00")) {
		t.Fatalf("q = %+v", q)
	}
}

func TestQuotaAutoRenew(t *testing.T) {
	loc := time.UTC
	cases := []struct {
		name       string
		q          Quota
		now        time.Time
		wantExpiry time.Time
		wantCount  int
		renewed    bool
	}{
		{"every 30 days catches up two periods", Quota{RenewEveryDays: 30, ExpiresAt: ptr(at("2026-10-01 00:00"))}, at("2026-11-12 10:00"), at("2026-11-30 00:00"), 2, true},
		{"monthly day 31 clamps to month end", Quota{RenewMonthDay: 31, ExpiresAt: ptr(at("2026-01-31 00:00"))}, at("2026-02-10 00:00"), at("2026-02-28 00:00"), 1, true},
		{"weekly on monday", Quota{RenewWeekday: 1, ExpiresAt: ptr(at("2026-11-09 00:00"))}, at("2026-11-12 10:00"), at("2026-11-16 00:00"), 1, true},
		{"max reached stays expired", Quota{RenewEveryDays: 30, RenewMax: 1, RenewCount: 1, ExpiresAt: ptr(at("2026-10-01 00:00"))}, at("2026-11-12 10:00"), at("2026-10-01 00:00"), 1, false},
		{"max limits catch-up", Quota{RenewEveryDays: 10, RenewMax: 2, ExpiresAt: ptr(at("2026-10-01 00:00"))}, at("2026-11-12 10:00"), at("2026-10-21 00:00"), 2, true},
		{"no renewal configured", Quota{ExpiresAt: ptr(at("2026-10-01 00:00"))}, at("2026-11-12 10:00"), at("2026-10-01 00:00"), 0, false},
		{"not expired yet", Quota{RenewEveryDays: 30, ExpiresAt: ptr(at("2026-12-01 00:00"))}, at("2026-11-12 10:00"), at("2026-12-01 00:00"), 0, false},
	}
	for _, c := range cases {
		q := c.q
		q.TrafficUsed = gib
		changed := q.Advance(c.now, loc)
		if changed != c.renewed || !q.ExpiresAt.Equal(c.wantExpiry) || q.RenewCount != c.wantCount {
			t.Errorf("%s: changed=%v expiry=%v count=%d", c.name, changed, q.ExpiresAt, q.RenewCount)
		}
		if c.renewed && q.TrafficUsed != 0 {
			t.Errorf("%s: renewal must clear used traffic", c.name)
		}
	}
}

func TestQuotaPeriodicReset(t *testing.T) {
	loc := time.UTC
	cases := []struct {
		name  string
		cycle string
		day   int
		last  time.Time
		now   time.Time
		reset bool
	}{
		{"hourly crossed", TrafficResetHourly, 1, at("2026-11-12 09:59"), at("2026-11-12 10:00"), true},
		{"hourly same hour", TrafficResetHourly, 1, at("2026-11-12 10:01"), at("2026-11-12 10:59"), false},
		{"daily crossed midnight", TrafficResetDaily, 1, at("2026-11-11 23:00"), at("2026-11-12 00:30"), true},
		{"weekly crossed sunday", TrafficResetWeekly, 1, at("2026-11-07 12:00"), at("2026-11-08 01:00"), true},
		{"weekly same week", TrafficResetWeekly, 1, at("2026-11-08 01:00"), at("2026-11-12 10:00"), false},
		{"monthly on the 15th", TrafficResetMonthly, 15, at("2026-11-10 00:00"), at("2026-11-15 00:01"), true},
		{"monthly before the 15th", TrafficResetMonthly, 15, at("2026-10-20 00:00"), at("2026-11-12 00:00"), false},
		{"monthly 31 in february", TrafficResetMonthly, 31, at("2026-02-20 00:00"), at("2026-02-28 08:00"), true},
		{"never", TrafficResetNever, 1, at("2020-01-01 00:00"), at("2026-11-12 10:00"), false},
	}
	for _, c := range cases {
		q := Quota{TrafficUsed: gib, TrafficReset: c.cycle, TrafficResetDay: c.day, LastTrafficResetAt: c.last}
		changed := q.Advance(c.now, loc)
		if changed != c.reset || (c.reset && (q.TrafficUsed != 0 || !q.LastTrafficResetAt.Equal(c.now))) {
			t.Errorf("%s: changed=%v used=%d last=%v", c.name, changed, q.TrafficUsed, q.LastTrafficResetAt)
		}
	}
}

func TestQuotaApplyPlan(t *testing.T) {
	now := at("2026-11-12 10:00")
	plan := PlanQuota{TrafficLimit: 100 * gib, DurationDays: 30, LimitIP: 3, TrafficReset: TrafficResetMonthly, TrafficResetDay: 1}
	cases := []struct {
		name        string
		q           Quota
		p           PlanQuota
		wantExpiry  *time.Time
		wantPending int
	}{
		{"new user starts now", Quota{}, plan, ptr(at("2026-12-12 10:00")), 0},
		{"active user extends from expiry", Quota{ExpiresAt: ptr(at("2026-11-20 00:00"))}, plan, ptr(at("2026-12-20 00:00")), 0},
		{"expired user restarts now", Quota{ExpiresAt: ptr(at("2026-11-01 00:00"))}, plan, ptr(at("2026-12-12 10:00")), 0},
		{"start on first use waits", Quota{}, PlanQuota{DurationDays: 30, StartOnFirstUse: true}, nil, 30},
		{"still waiting adds days", Quota{PendingDays: 10}, plan, nil, 40},
		{"unlimited duration", Quota{ExpiresAt: ptr(at("2026-11-20 00:00"))}, PlanQuota{}, nil, 0},
	}
	for _, c := range cases {
		q := c.q
		q.TrafficUsed, q.RenewCount = 5*gib, 3
		q.ApplyPlan(c.p, now)
		sameExpiry := (q.ExpiresAt == nil && c.wantExpiry == nil) || (q.ExpiresAt != nil && c.wantExpiry != nil && q.ExpiresAt.Equal(*c.wantExpiry))
		if !sameExpiry || q.PendingDays != c.wantPending || q.TrafficUsed != 0 || q.RenewCount != 0 || q.TrafficLimit != c.p.TrafficLimit {
			t.Errorf("%s: q = %+v", c.name, q)
		}
	}
}
