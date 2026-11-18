package service

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"v2-hub-server/internal/dao/sqlcgen"
)

// 签到（docs/03 §5.11，Ruling-26）：每人每个自然日一次，随机奖励流量加到当前额度上。

const (
	checkinLogLimit = 30
	bytesPerMB      = int64(1) << 20

	msgCheckinDisabled  = "签到未开启"
	msgCheckinDone      = "今天已经签到"
	msgCheckinNoPlan    = "还没有套餐，无法签到"
	msgCheckinUnlimited = "当前套餐不限流量，无需签到"
	msgCheckinInactive  = "账号当前状态不能签到"
)

// CheckinLog 是一条签到记录。Date 是 site.timezone 下的自然日（时刻为该日 0 点 UTC，只取年月日）。
type CheckinLog struct {
	Date        time.Time
	RewardBytes int64
}

// CheckinInfo 是签到状态。Reason 是不能签到的原因，可以签到时为空。
type CheckinInfo struct {
	Enabled      bool
	CanCheckin   bool
	Reason       string
	CheckedToday bool
	Logs         []CheckinLog
}

// CheckinResult 是一次签到的结果。
type CheckinResult struct {
	RewardBytes  int64
	TrafficLimit int64
}

// checkinSettings 返回签到设置与“今天”（site.timezone 的自然日）。没接系统设置时视为未开启。
func (s *UserService) checkinSettings(ctx context.Context) (CheckinSettings, pgtype.Date, error) {
	if s.cfg.Settings == nil {
		return CheckinSettings{}, pgtype.Date{}, nil
	}
	cur, err := s.cfg.Settings.Current(ctx)
	if err != nil {
		return CheckinSettings{}, pgtype.Date{}, err
	}
	now := s.now().In(cur.Location())
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return cur.Checkin, pgtype.Date{Time: today, Valid: true}, nil
}

// trafficTopUpMessages 是“给当前额度加流量”不可用时的三种提示：没有套餐、不限流量、账号状态不对。
type trafficTopUpMessages struct {
	noPlan    string
	unlimited string
	inactive  string
}

var (
	// checkinBlockMessages 是签到的提示（docs/03 §5.11 条件）。
	checkinBlockMessages = trafficTopUpMessages{noPlan: msgCheckinNoPlan, unlimited: msgCheckinUnlimited, inactive: msgCheckinInactive}
	// packBlockMessages 是买流量包的提示（docs/03 §5.13 可买条件）。
	packBlockMessages = trafficTopUpMessages{noPlan: msgPackNoPlan, unlimited: msgPackUnlimited, inactive: msgPackInactive}
)

// trafficTopUpBlocker 返回不能给当前额度加流量（签到、买流量包）的原因，文案取自 msgs；可以加时返回空串。
func trafficTopUpBlocker(hasPlan bool, qt Quota, status string, msgs trafficTopUpMessages) string {
	switch {
	case !hasPlan:
		return msgs.noPlan
	case qt.TrafficLimit == 0:
		return msgs.unlimited
	case status != UserStatusActive && status != UserStatusLimited:
		return msgs.inactive
	default:
		return ""
	}
}

// GetCheckin 返回本人的签到状态与最近 30 条记录。
func (s *UserService) GetCheckin(ctx context.Context, p *UserPrincipal) (*CheckinInfo, error) {
	cfg, today, err := s.checkinSettings(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListCheckins(ctx, sqlcgen.ListCheckinsParams{UserID: p.UserID, Limit: checkinLogLimit})
	if err != nil {
		return nil, fmt.Errorf("list checkins of user %d: %w", p.UserID, err)
	}
	info := &CheckinInfo{Enabled: cfg.Enabled, Logs: make([]CheckinLog, 0, len(rows))}
	for _, r := range rows {
		info.Logs = append(info.Logs, CheckinLog{Date: r.CheckinDate.Time, RewardBytes: r.RewardBytes})
		if today.Valid && r.CheckinDate.Time.Equal(today.Time) {
			info.CheckedToday = true
		}
	}
	if !cfg.Enabled {
		info.Reason = msgCheckinDisabled
		return info, nil
	}
	if info.CheckedToday {
		info.Reason = msgCheckinDone
		return info, nil
	}
	row, err := s.q.GetUserCheckinState(ctx, p.UserID)
	if err != nil {
		return nil, fmt.Errorf("get checkin state of user %d: %w", p.UserID, err)
	}
	qt := Quota{TrafficLimit: row.TrafficLimit, TrafficUsed: row.TrafficUsed, ExpiresAt: timePtr(row.ExpiresAt), Disabled: row.IsDisabled}
	info.Reason = trafficTopUpBlocker(row.PlanID.Valid, qt, qt.Status(s.now()), checkinBlockMessages)
	info.CanCheckin = info.Reason == ""
	return info, nil
}

// Checkin 签到：奖励与签到记录在同一事务内写入；因此回到额度以内的用户状态重算并触发对账。
// 错误：CodeFeatureDisabled（未开启）、CodeConflict（今天已签到，或不满足条件，message 说明原因）。
func (s *UserService) Checkin(ctx context.Context, p *UserPrincipal) (*CheckinResult, error) {
	cfg, today, err := s.checkinSettings(ctx)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, newError(CodeFeatureDisabled, msgCheckinDisabled)
	}
	rewardMB, err := randomBetween(cfg.MinMB, cfg.MaxMB)
	if err != nil {
		return nil, err
	}
	reward := int64(rewardMB) * bytesPerMB
	now := s.now()
	res := &CheckinResult{RewardBytes: reward}
	needsSync := false
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := lockUser(ctx, q, p.UserID)
		if err != nil {
			return err
		}
		qt := quotaFromRow(row)
		if reason := trafficTopUpBlocker(row.PlanID.Valid, qt, qt.Status(now), checkinBlockMessages); reason != "" {
			return newError(CodeConflict, reason)
		}
		n, err := q.InsertCheckin(ctx, sqlcgen.InsertCheckinParams{
			UserID: p.UserID, CheckinDate: today, RewardBytes: reward, CreatedAt: timestamptz(now),
		})
		if err != nil {
			return fmt.Errorf("insert checkin of user %d: %w", p.UserID, err)
		}
		if n == 0 {
			return newError(CodeConflict, msgCheckinDone)
		}
		qt.TrafficLimit += reward
		status := qt.Status(now)
		if err := saveQuota(ctx, q, p.UserID, row.PlanID, qt, status); err != nil {
			return err
		}
		res.TrafficLimit = qt.TrafficLimit
		if status != row.Status {
			needsSync = true
			if err := q.MarkUserProvisionsPending(ctx, p.UserID); err != nil {
				return fmt.Errorf("mark provisions of user %d pending: %w", p.UserID, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if needsSync {
		s.TriggerReconcile()
	}
	return res, nil
}

// randomBetween 返回 [lo, hi] 内均匀分布的整数；lo ≤ hi 由调用方保证。
func randomBetween(lo, hi int) (int, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(hi-lo+1)))
	if err != nil {
		return 0, fmt.Errorf("random integer in [%d, %d]: %w", lo, hi, err)
	}
	return lo + int(n.Int64()), nil
}
