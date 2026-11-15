package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/telegram"
)

// Telegram 管理员通知（docs/03 §5.20，Ruling-42）：先写 telegram_outbox，再由后台任务调用 Bot API；token 只走环境变量。

// 通知事件，决定看哪个开关。
const (
	TelegramEventAlert   = "alert"
	TelegramEventTicket  = "ticket"
	TelegramEventPayment = "payment"
	TelegramEventReport  = "report"
)

const (
	telegramBatch       = 20
	telegramLease       = 2 * time.Minute
	telegramMaxAttempts = 5
	telegramRetryBase   = time.Minute
	telegramRetention   = 30 * 24 * time.Hour
	maxTelegramErrorLen = 255

	ActionSettingTestTelegram = "setting.test_telegram"

	msgTelegramDisabled = "Telegram 未配置：需要环境变量 HUB_TELEGRAM_BOT_TOKEN 与系统设置里的会话 id"
	msgTelegramFailed   = "Telegram 发送失败："
	msgTelegramTestText = "这是一条测试消息，说明 Telegram 通知可用。"
)

// TelegramService 负责 Telegram 发件箱的写入、发送与测试。sender 为 nil 即 Telegram 不可用。
type TelegramService struct {
	store
	sender   telegram.Sender
	settings *SettingsService
	now      func() time.Time
}

// NewTelegramService 创建 Telegram 服务；sender 为 nil 表示未配置 token，now 为 nil 时用 time.Now。
func NewTelegramService(pool *pgxpool.Pool, sender telegram.Sender, settings *SettingsService, now func() time.Time) *TelegramService {
	if now == nil {
		now = time.Now
	}
	return &TelegramService{store: newStore(pool), sender: sender, settings: settings, now: now}
}

// Enabled 报告是否配置了机器人 token（会话 id 另在设置里）。
func (s *TelegramService) Enabled() bool {
	return s != nil && s.sender != nil
}

// eventOn 报告这类事件在当前设置下是否要发。
func eventOn(cur Settings, event string) bool {
	switch event {
	case TelegramEventAlert:
		return cur.Telegram.NotifyAlerts
	case TelegramEventTicket:
		return cur.Telegram.NotifyTickets
	case TelegramEventPayment:
		return cur.Telegram.NotifyPayments
	case TelegramEventReport:
		return true
	default:
		return false
	}
}

// Notify 在调用方的事务里写一条待发消息；Telegram 不可用、没设会话或该事件没开时什么都不做。
// 通知是附带动作：写失败只记日志，不让业务操作失败。
func (s *TelegramService) Notify(ctx context.Context, q *sqlcgen.Queries, event, text string) {
	if !s.Enabled() {
		return
	}
	cur, err := s.settings.Current(ctx)
	if err != nil {
		slog.Error("load settings for telegram", "event", event, "err", err)
		return
	}
	if cur.Telegram.ChatID == "" || !eventOn(cur, event) {
		return
	}
	queued := timestamptz(s.now())
	if _, err := q.InsertTelegramOutbox(ctx, sqlcgen.InsertTelegramOutboxParams{ChatID: cur.Telegram.ChatID, Text: pgtype.Text{String: text, Valid: true}, QueuedAt: queued}); err != nil {
		slog.Error("enqueue telegram", "event", event, "err", err)
	}
}

// sanitizeTelegramError 给出不含 token 的错误摘要（telegram 包的错误本身不带地址）。
func sanitizeTelegramError(err error) string {
	return truncate(err.Error(), maxTelegramErrorLen)
}

// ProcessOutbox 发送到期的待发消息，一次最多 20 条；返回尝试的条数。不可用时什么都不做。
func (s *TelegramService) ProcessOutbox(ctx context.Context) (int, error) {
	if !s.Enabled() {
		return 0, nil
	}
	now := s.now()
	rows, err := s.q.ClaimDueTelegram(ctx, sqlcgen.ClaimDueTelegramParams{
		LeaseUntil: timestamptz(now.Add(telegramLease)),
		Now:        timestamptz(now),
		Batch:      telegramBatch,
	})
	if err != nil {
		return 0, fmt.Errorf("claim due telegram: %w", err)
	}
	for _, row := range rows {
		if err := s.deliver(ctx, row); err != nil {
			return len(rows), err
		}
	}
	return len(rows), nil
}

// deliver 发一条并记录结果；只有写库失败才返回错误，发送失败按退避重试，被拒绝或次数用完就放弃。
func (s *TelegramService) deliver(ctx context.Context, row sqlcgen.ClaimDueTelegramRow) error {
	sendErr := s.sender.SendMessage(ctx, row.ChatID, row.Text)
	if sendErr == nil {
		if err := s.q.MarkTelegramSent(ctx, sqlcgen.MarkTelegramSentParams{ID: row.ID, SentAt: timestamptz(s.now())}); err != nil {
			return fmt.Errorf("mark telegram %d sent: %w", row.ID, err)
		}
		return nil
	}
	reason := sanitizeTelegramError(sendErr)
	slog.Warn("send telegram failed", "id", row.ID, "attempt", row.Attempts, "err", reason)
	if int(row.Attempts) >= telegramMaxAttempts || errors.Is(sendErr, telegram.ErrRejected) {
		if err := s.q.MarkTelegramFailed(ctx, sqlcgen.MarkTelegramFailedParams{ID: row.ID, LastError: reason}); err != nil {
			return fmt.Errorf("mark telegram %d failed: %w", row.ID, err)
		}
		return nil
	}
	backoff := telegramRetryBase << (row.Attempts - 1)
	if err := s.q.SetTelegramRetry(ctx, sqlcgen.SetTelegramRetryParams{
		ID: row.ID, NextAttemptAt: timestamptz(s.now().Add(backoff)), LastError: reason,
	}); err != nil {
		return fmt.Errorf("schedule telegram %d retry: %w", row.ID, err)
	}
	return nil
}

// SendTest 直接向设置里的会话发一条测试消息（setting:write）。
// 错误：CodeFeatureDisabled（未配置 token 或会话）、CodeUpstream（Bot API 失败，说明不含 token）。
func (s *TelegramService) SendTest(ctx context.Context, actor *AdminPrincipal, ip *netip.Addr) error {
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return err
	}
	if !s.Enabled() || cur.Telegram.ChatID == "" {
		return newError(CodeFeatureDisabled, msgTelegramDisabled)
	}
	if err := writeAudit(ctx, s.q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionSettingTestTelegram,
		TargetType: targetSetting, ClientIP: ip}); err != nil {
		return err
	}
	if err := s.sender.SendMessage(ctx, cur.Telegram.ChatID, "【"+cur.Site.Name+"】"+msgTelegramTestText); err != nil {
		return newError(CodeUpstream, msgTelegramFailed+sanitizeTelegramError(err))
	}
	return nil
}

// PurgeOutbox 删除 30 天前的记录，返回删除条数。
func (s *TelegramService) PurgeOutbox(ctx context.Context) (int64, error) {
	n, err := s.q.PurgeTelegramOutbox(ctx, timestamptz(s.now().Add(-telegramRetention)))
	if err != nil {
		return 0, fmt.Errorf("purge telegram outbox: %w", err)
	}
	return n, nil
}
