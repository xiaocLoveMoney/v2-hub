package service

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/crypto"
	"v2-hub-server/internal/manager/mail"
)

// 邮件（docs/03 §5.7）：所有邮件先入发件箱，由后台任务发送；正文加密存储，发完清空。

// 发件箱优先级：小的先发（验证码类优先于群发）。
const (
	mailPriorityCode   int16 = 0
	mailPriorityNotice int16 = 1
	mailPriorityBulk   int16 = 2
)

// 发件箱状态。
const (
	MailStatusPending = "pending"
	MailStatusSent    = "sent"
	MailStatusFailed  = "failed"
)

const (
	mailMaxAttempts     = 5
	mailRetryBase       = time.Minute // 第 n 次失败后等 base × 2^(n-1)
	mailLease           = 2 * time.Minute
	mailOutboxRetention = 30 * 24 * time.Hour
	mailAADPrefix       = "mail_outbox:"
	mailAADKeyBytes     = 12
	mailErrorMaxLen     = 255
	secondsPerMinute    = 60

	ActionSettingTestMail = "setting.test_mail"

	msgMailDisabled   = "邮件未配置，该功能不可用"
	msgMailBadAddress = "邮箱地址不合法"
	msgMailBadStatus  = "status 只能是 pending、sent、failed"
	msgMailSendFailed = "发送失败："
)

// mailBody 是加密存入发件箱的正文。
type mailBody struct {
	Text string `json:"text"`
	HTML string `json:"html"`
}

// MailOutboxItem 是发件箱的一条记录（不含正文）。
type MailOutboxItem struct {
	ID        int64
	Recipient string
	Subject   string
	Status    string
	Attempts  int
	LastError string
	CreatedAt time.Time
	SentAt    *time.Time
}

// MailService 负责发件箱的写入、发送与查询。sender 为 nil 即邮件不可用（Ruling-24）。
type MailService struct {
	store
	sealer   *crypto.Sealer
	sender   mail.Sender
	settings *SettingsService
	now      func() time.Time
}

// NewMailService 创建邮件服务；sender 为 nil 表示邮件未配置，now 为 nil 时用 time.Now。
func NewMailService(pool *pgxpool.Pool, sealer *crypto.Sealer, sender mail.Sender, settings *SettingsService, now func() time.Time) *MailService {
	if now == nil {
		now = time.Now
	}
	return &MailService{store: newStore(pool), sealer: sealer, sender: sender, settings: settings, now: now}
}

// Enabled 报告邮件是否可用。
func (s *MailService) Enabled() bool {
	return s.sender != nil
}

func (s *MailService) requireEnabled() error {
	if !s.Enabled() {
		return newError(CodeFeatureDisabled, msgMailDisabled)
	}
	return nil
}

// enqueue 在调用方的事务里把一封邮件写进发件箱。邮件不可用时返回 CodeFeatureDisabled。
func (s *MailService) enqueue(ctx context.Context, q *sqlcgen.Queries, to string, c mailContent, priority int16) error {
	if err := s.requireEnabled(); err != nil {
		return err
	}
	aadKey, err := crypto.RandomToken(mailAADKeyBytes)
	if err != nil {
		return fmt.Errorf("generate mail aad key: %w", err)
	}
	raw, err := json.Marshal(mailBody{Text: c.Text, HTML: c.HTML})
	if err != nil {
		return fmt.Errorf("marshal mail body: %w", err)
	}
	sealed, err := s.sealer.Seal(raw, []byte(mailAADPrefix+aadKey))
	if err != nil {
		return fmt.Errorf("seal mail body: %w", err)
	}
	if _, err := q.InsertMailOutbox(ctx, sqlcgen.InsertMailOutboxParams{
		AadKey: aadKey, Recipient: to, Subject: c.Subject, BodyEnc: sealed, Priority: priority,
		QueuedAt: pgtype.Timestamptz{Time: s.now(), Valid: true},
	}); err != nil {
		return fmt.Errorf("insert mail outbox: %w", err)
	}
	return nil
}

// ProcessOutbox 发送到期的待发邮件，一次最多发 window 这段时间按 mail.ratePerMinute 折算的封数（至少 1 封）。
// 返回本次尝试的封数。邮件不可用时什么都不做。
func (s *MailService) ProcessOutbox(ctx context.Context, window time.Duration) (int, error) {
	if !s.Enabled() {
		return 0, nil
	}
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return 0, err
	}
	batch := int32(float64(cur.Mail.RatePerMinute) * window.Seconds() / secondsPerMinute)
	if batch < 1 {
		batch = 1
	}
	now := s.now()
	rows, err := s.q.ClaimDueMail(ctx, sqlcgen.ClaimDueMailParams{
		LeaseUntil: pgtype.Timestamptz{Time: now.Add(mailLease), Valid: true},
		Now:        pgtype.Timestamptz{Time: now, Valid: true},
		Batch:      batch,
	})
	if err != nil {
		return 0, fmt.Errorf("claim due mail: %w", err)
	}
	// UPDATE … RETURNING 不保证顺序，按优先级、再按入箱先后发
	slices.SortFunc(rows, func(a, b sqlcgen.ClaimDueMailRow) int {
		return cmp.Or(cmp.Compare(a.Priority, b.Priority), cmp.Compare(a.ID, b.ID))
	})
	for _, row := range rows {
		if err := s.deliver(ctx, row); err != nil {
			return len(rows), err
		}
	}
	return len(rows), nil
}

// deliver 发一封并记录结果；只有写库失败才返回错误，发送失败记入该行并按退避重试。
func (s *MailService) deliver(ctx context.Context, row sqlcgen.ClaimDueMailRow) error {
	sendErr := s.send(ctx, row)
	if sendErr == nil {
		if err := s.q.MarkMailSent(ctx, row.ID); err != nil {
			return fmt.Errorf("mark mail %d sent: %w", row.ID, err)
		}
		return nil
	}
	reason := sanitizeMailError(sendErr)
	slog.Warn("send mail failed", "id", row.ID, "to", maskEmail(row.Recipient), "attempt", row.Attempts, "err", reason)
	giveUp := int(row.Attempts) >= mailMaxAttempts || errors.Is(sendErr, mail.ErrBadMessage) || errors.Is(sendErr, crypto.ErrCiphertext)
	if giveUp {
		if err := s.q.MarkMailFailed(ctx, sqlcgen.MarkMailFailedParams{ID: row.ID, LastError: reason}); err != nil {
			return fmt.Errorf("mark mail %d failed: %w", row.ID, err)
		}
		return nil
	}
	backoff := mailRetryBase << (row.Attempts - 1)
	if err := s.q.SetMailRetry(ctx, sqlcgen.SetMailRetryParams{
		ID: row.ID, NextAttemptAt: pgtype.Timestamptz{Time: s.now().Add(backoff), Valid: true}, LastError: reason,
	}); err != nil {
		return fmt.Errorf("schedule mail %d retry: %w", row.ID, err)
	}
	return nil
}

func (s *MailService) send(ctx context.Context, row sqlcgen.ClaimDueMailRow) error {
	raw, err := s.sealer.Open(row.BodyEnc, []byte(mailAADPrefix+row.AadKey))
	if err != nil {
		return err
	}
	var body mailBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return fmt.Errorf("%w: %w", crypto.ErrCiphertext, err)
	}
	return s.sender.Send(ctx, mail.Message{To: row.Recipient, Subject: row.Subject, Text: body.Text, HTML: body.HTML})
}

// SendTest 直接向 to 发一封测试邮件并返回结果（docs/03 §5.7 测试）。
// 错误：CodeFeatureDisabled（未配置）、CodeInvalidArgument（地址不合法）、CodeUpstream（SMTP 失败，message 为脱敏后的原因）。
func (s *MailService) SendTest(ctx context.Context, actor *AdminPrincipal, to string, ip *netip.Addr) error {
	if err := s.requireEnabled(); err != nil {
		return err
	}
	addr := normalizeEmail(to)
	if addr == "" {
		return newError(CodeInvalidArgument, msgMailBadAddress)
	}
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return err
	}
	c, err := renderMail(mailTplTest, mailVars{Site: cur.Site.Name})
	if err != nil {
		return err
	}
	if err := writeAudit(ctx, s.q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionSettingTestMail, TargetType: targetSetting,
		Detail: map[string]any{"to": maskEmail(addr)}, ClientIP: ip}); err != nil {
		return err
	}
	if err := s.sender.Send(ctx, mail.Message{To: addr, Subject: c.Subject, Text: c.Text, HTML: c.HTML}); err != nil {
		return newError(CodeUpstream, msgMailSendFailed+sanitizeMailError(err))
	}
	return nil
}

// ListOutbox 分页返回发件箱记录，按 id 倒序；status 为空表示全部。
func (s *MailService) ListOutbox(ctx context.Context, status string, pq PageQuery) (*Page[MailOutboxItem], error) {
	if status != "" && status != MailStatusPending && status != MailStatusSent && status != MailStatusFailed {
		return nil, newError(CodeInvalidArgument, msgMailBadStatus)
	}
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	total, err := s.q.CountMailOutbox(ctx, status)
	if err != nil {
		return nil, fmt.Errorf("count mail outbox: %w", err)
	}
	rows, err := s.q.ListMailOutbox(ctx, sqlcgen.ListMailOutboxParams{Status: status, RowLimit: limit, RowOffset: offset})
	if err != nil {
		return nil, fmt.Errorf("list mail outbox: %w", err)
	}
	items := make([]MailOutboxItem, 0, len(rows))
	for _, r := range rows {
		item := MailOutboxItem{ID: r.ID, Recipient: r.Recipient, Subject: r.Subject, Status: r.Status, Attempts: int(r.Attempts),
			LastError: r.LastError, CreatedAt: r.CreatedAt.Time}
		if r.SentAt.Valid {
			t := r.SentAt.Time
			item.SentAt = &t
		}
		items = append(items, item)
	}
	return &Page[MailOutboxItem]{Items: items, Total: total}, nil
}

// PurgeOutbox 删除超过保留期的发件箱记录与过期已久的验证码，返回删除的行数。
func (s *MailService) PurgeOutbox(ctx context.Context) (int64, error) {
	cutoff := pgtype.Timestamptz{Time: s.now().Add(-mailOutboxRetention), Valid: true}
	mails, err := s.q.PurgeMailOutbox(ctx, cutoff)
	if err != nil {
		return 0, fmt.Errorf("purge mail outbox: %w", err)
	}
	codes, err := s.q.PurgeEmailCodes(ctx, pgtype.Timestamptz{Time: s.now().Add(-emailCodeRetention), Valid: true})
	if err != nil {
		return mails, fmt.Errorf("purge email codes: %w", err)
	}
	return mails + codes, nil
}

// sanitizeMailError 把发送错误压成一行、截断到库列长度；SMTP 错误不含口令。
func sanitizeMailError(err error) string {
	msg := strings.Join(strings.Fields(err.Error()), " ")
	if r := []rune(msg); len(r) > mailErrorMaxLen {
		msg = string(r[:mailErrorMaxLen])
	}
	return msg
}

// maskEmail 返回日志与审计用的打码邮箱：a***@example.com。
func maskEmail(addr string) string {
	at := strings.LastIndexByte(addr, '@')
	if at < 1 {
		return "***"
	}
	return addr[:1] + "***" + addr[at:]
}
