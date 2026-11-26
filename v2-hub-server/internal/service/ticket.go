package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
)

// 工单（docs/03 §5.10）：用户与管理员之间的纯文本对话。

// 工单状态。
const (
	TicketOpen     = "open"     // 等管理员回复
	TicketAnswered = "answered" // 等用户回复
	TicketClosed   = "closed"
)

// 消息作者类型。
const (
	TicketAuthorUser  = "user"
	TicketAuthorAdmin = "admin"
)

const (
	maxTicketSubject        = 100
	maxTicketMessage        = 5000
	maxOpenTicketsPerUser   = 5
	maxTicketsPerHour       = 5
	maxTicketRepliesPerHour = 30

	ActionTicketReply = "ticket.reply"
	ActionTicketClose = "ticket.close"
	targetTicket      = "ticket"

	msgTicketNotFound   = "工单不存在"
	msgTicketBadSubject = "主题须为 1–100 个字符"
	msgTicketBadContent = "内容须为 1–5000 个字符"
	msgTicketBadStatus  = "status 只能是 open、answered、closed"
	msgTicketTooMany    = "未关闭的工单已达上限，请先处理现有工单"
	msgTicketTooOften   = "提交过于频繁，请稍后再试"
	msgTicketDisabled   = "工单未开启"
)

// Ticket 是一张工单。UserID、Username 供管理端显示。
type Ticket struct {
	ID          int64
	UserID      int64
	Username    string
	Subject     string
	Status      string
	LastReplyAt time.Time
	CreatedAt   time.Time
}

// TicketMessage 是工单里的一条消息。AuthorName 是管理员用户名，只给管理端（用户侧一律显示“客服”）。
type TicketMessage struct {
	ID         int64
	AuthorKind string
	AuthorName string
	Content    string
	CreatedAt  time.Time
}

// TicketDetail 是工单与它的全部消息。
type TicketDetail struct {
	Ticket
	Messages []TicketMessage
}

// TicketFilter 是管理端工单列表的筛选条件，零值表示不限。
type TicketFilter struct {
	Status string
	UserID int64
}

// TicketService 负责工单。
type TicketService struct {
	store
	settings *SettingsService
	mail     *MailService
	telegram *TelegramService
	now      func() time.Time
}

// NewTicketService 创建工单服务；telegram 为 nil 表示不发 Telegram 通知（docs/03 §5.20），now 为 nil 时用 time.Now。
func NewTicketService(pool *pgxpool.Pool, settings *SettingsService, mailSvc *MailService, telegram *TelegramService, now func() time.Time) *TicketService {
	if now == nil {
		now = time.Now
	}
	return &TicketService{store: newStore(pool), settings: settings, mail: mailSvc, telegram: telegram, now: now}
}

func isTicketStatus(s string) bool {
	return s == TicketOpen || s == TicketAnswered || s == TicketClosed
}

func validTicketContent(s string) (string, error) {
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n < 1 || n > maxTicketMessage {
		return "", newError(CodeInvalidArgument, msgTicketBadContent)
	}
	return s, nil
}

func (s *TicketService) list(ctx context.Context, arg sqlcgen.ListTicketsParams) ([]Ticket, error) {
	rows, err := s.q.ListTickets(ctx, arg)
	if err != nil {
		return nil, fmt.Errorf("list tickets: %w", err)
	}
	return ticketsFromRows(rows), nil
}

// ticketsFromRows 把工单行转换为 Ticket；员工侧与代理侧共用（代理侧的行结构逐字段相同，先转换再调用）。
func ticketsFromRows(rows []sqlcgen.ListTicketsRow) []Ticket {
	out := make([]Ticket, 0, len(rows))
	for _, r := range rows {
		out = append(out, Ticket{ID: r.ID, UserID: r.UserID, Username: r.Username, Subject: r.Subject, Status: r.Status,
			LastReplyAt: r.LastReplyAt.Time, CreatedAt: r.CreatedAt.Time})
	}
	return out
}

// ticketMessagesFromRows 把消息行转换为 TicketMessage；员工侧与代理侧共用（代理侧的行结构逐字段相同，先转换再调用）。
func ticketMessagesFromRows(rows []sqlcgen.ListTicketMessagesRow) []TicketMessage {
	out := make([]TicketMessage, 0, len(rows))
	for _, r := range rows {
		out = append(out, TicketMessage{ID: r.ID, AuthorKind: r.AuthorKind, AuthorName: r.AdminUsername, Content: r.Content, CreatedAt: r.CreatedAt.Time})
	}
	return out
}

func (s *TicketService) page(ctx context.Context, f TicketFilter, pq PageQuery) (*Page[Ticket], error) {
	if f.Status != "" && !isTicketStatus(f.Status) {
		return nil, newError(CodeInvalidArgument, msgTicketBadStatus)
	}
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	total, err := s.q.CountTickets(ctx, sqlcgen.CountTicketsParams{UserID: f.UserID, Status: f.Status})
	if err != nil {
		return nil, fmt.Errorf("count tickets: %w", err)
	}
	items, err := s.list(ctx, sqlcgen.ListTicketsParams{UserID: f.UserID, Status: f.Status, RowLimit: limit, RowOffset: offset})
	if err != nil {
		return nil, err
	}
	return &Page[Ticket]{Items: items, Total: total}, nil
}

// detail 读一张工单与全部消息；ownerID 非 0 时要求工单属于该用户，否则按不存在处理（R8）。
func (s *TicketService) detail(ctx context.Context, id, ownerID int64) (*TicketDetail, error) {
	tickets, err := s.list(ctx, sqlcgen.ListTicketsParams{ID: id, UserID: ownerID, RowLimit: 1})
	if err != nil {
		return nil, err
	}
	if len(tickets) == 0 {
		return nil, newError(CodeNotFound, msgTicketNotFound)
	}
	rows, err := s.q.ListTicketMessages(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list messages of ticket %d: %w", id, err)
	}
	return &TicketDetail{Ticket: tickets[0], Messages: ticketMessagesFromRows(rows)}, nil
}

// lockTicket 锁住工单行；ownerID 非 0 时要求属于该用户，否则 CodeNotFound（R8）。
func lockTicket(ctx context.Context, q *sqlcgen.Queries, id, ownerID int64) (sqlcgen.GetTicketForUpdateRow, error) {
	row, err := q.GetTicketForUpdate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && ownerID != 0 && row.UserID != ownerID) {
		return row, newError(CodeNotFound, msgTicketNotFound)
	}
	if err != nil {
		return row, fmt.Errorf("lock ticket %d: %w", id, err)
	}
	return row, nil
}

// notify 把一封提醒邮件写进发件箱；邮件不可用或没有收件人时跳过。提醒发不出去不应让工单操作失败，所以只留痕。
func (s *TicketService) notify(ctx context.Context, q *sqlcgen.Queries, to string, tpl mailTemplate, subject string) {
	if to == "" || !s.mail.Enabled() {
		return
	}
	cur, err := s.settings.Current(ctx)
	if err != nil {
		slog.Error("load settings for ticket notification", "err", err)
		return
	}
	content, err := renderMail(tpl, mailVars{Site: cur.Site.Name, Title: subject})
	if err != nil {
		slog.Error("render ticket notification", "err", err)
		return
	}
	if err := s.mail.enqueue(ctx, q, to, content, mailPriorityNotice); err != nil {
		slog.Error("enqueue ticket notification", "to", maskEmail(to), "err", err)
	}
}

func (s *TicketService) notifyAdmins(ctx context.Context, q *sqlcgen.Queries, subject string) {
	cur, err := s.settings.Current(ctx)
	if err != nil {
		slog.Error("load settings for ticket notification", "err", err)
		return
	}
	s.notify(ctx, q, cur.Ticket.NotifyEmail, mailTplTicketNew, subject)
	s.telegram.Notify(ctx, q, TelegramEventTicket, "【"+cur.Site.Name+"】工单待处理："+subject)
}

func (s *TicketService) requireEnabled(ctx context.Context) error {
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return err
	}
	if !cur.Ticket.Enabled {
		return newError(CodeFeatureDisabled, msgTicketDisabled)
	}
	return nil
}

// ---- 用户侧 --------------------------------------------------------------------

// ListOwn 分页列出本人的工单，按最近回复倒序。
func (s *TicketService) ListOwn(ctx context.Context, p *UserPrincipal, pq PageQuery) (*Page[Ticket], error) {
	return s.page(ctx, TicketFilter{UserID: p.UserID}, pq)
}

// GetOwn 返回本人的一张工单；他人的按不存在处理（R8）。
func (s *TicketService) GetOwn(ctx context.Context, p *UserPrincipal, id int64) (*TicketDetail, error) {
	return s.detail(ctx, id, p.UserID)
}

// Create 新建工单。错误：CodeFeatureDisabled、CodeInvalidArgument、CodeConflict（未关闭的工单已达上限）、CodeRateLimited。
func (s *TicketService) Create(ctx context.Context, p *UserPrincipal, subject, content string) (*TicketDetail, error) {
	if err := s.requireEnabled(ctx); err != nil {
		return nil, err
	}
	subject = strings.TrimSpace(subject)
	if !lenBetween(subject, 1, maxTicketSubject) {
		return nil, newError(CodeInvalidArgument, msgTicketBadSubject)
	}
	content, err := validTicketContent(content)
	if err != nil {
		return nil, err
	}
	now := s.now()
	var id int64
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		// 锁住用户行，让同一用户的并发新建串行，上限判断才不会被穿透
		if _, err := lockUser(ctx, q, p.UserID); err != nil {
			return err
		}
		open, err := q.CountOpenTicketsByUser(ctx, p.UserID)
		if err != nil {
			return fmt.Errorf("count open tickets of user %d: %w", p.UserID, err)
		}
		if open >= maxOpenTicketsPerUser {
			return newError(CodeConflict, msgTicketTooMany)
		}
		recent, err := q.CountTicketsCreatedSince(ctx, sqlcgen.CountTicketsCreatedSinceParams{UserID: p.UserID, CreatedAt: timestamptz(now.Add(-time.Hour))})
		if err != nil {
			return fmt.Errorf("count recent tickets of user %d: %w", p.UserID, err)
		}
		if recent >= maxTicketsPerHour {
			return newError(CodeRateLimited, msgTicketTooOften)
		}
		if id, err = q.CreateTicket(ctx, sqlcgen.CreateTicketParams{UserID: p.UserID, Subject: subject, Now: timestamptz(now)}); err != nil {
			return fmt.Errorf("create ticket: %w", err)
		}
		if err := q.InsertTicketMessage(ctx, sqlcgen.InsertTicketMessageParams{TicketID: id, AuthorKind: TicketAuthorUser, Content: content, CreatedAt: timestamptz(now)}); err != nil {
			return fmt.Errorf("insert first message of ticket %d: %w", id, err)
		}
		s.notifyAdmins(ctx, q, subject)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.detail(ctx, id, p.UserID)
}

// ReplyOwn 用户回复自己的工单，状态变为 open（已关闭的重新打开）。
// 错误：CodeFeatureDisabled、CodeInvalidArgument、CodeNotFound、CodeRateLimited。
func (s *TicketService) ReplyOwn(ctx context.Context, p *UserPrincipal, id int64, content string) (*TicketDetail, error) {
	if err := s.requireEnabled(ctx); err != nil {
		return nil, err
	}
	content, err := validTicketContent(content)
	if err != nil {
		return nil, err
	}
	now := s.now()
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := lockTicket(ctx, q, id, p.UserID)
		if err != nil {
			return err
		}
		recent, err := q.CountUserTicketMessagesSince(ctx, sqlcgen.CountUserTicketMessagesSinceParams{UserID: p.UserID, CreatedAt: timestamptz(now.Add(-time.Hour))})
		if err != nil {
			return fmt.Errorf("count recent replies of user %d: %w", p.UserID, err)
		}
		if recent >= maxTicketRepliesPerHour {
			return newError(CodeRateLimited, msgTicketTooOften)
		}
		if err := q.InsertTicketMessage(ctx, sqlcgen.InsertTicketMessageParams{TicketID: id, AuthorKind: TicketAuthorUser, Content: content, CreatedAt: timestamptz(now)}); err != nil {
			return fmt.Errorf("insert message of ticket %d: %w", id, err)
		}
		if err := q.SetTicketStatus(ctx, sqlcgen.SetTicketStatusParams{ID: id, Status: TicketOpen, TouchReply: true, Now: timestamptz(now)}); err != nil {
			return fmt.Errorf("reopen ticket %d: %w", id, err)
		}
		s.notifyAdmins(ctx, q, row.Subject)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.detail(ctx, id, p.UserID)
}

// CloseOwn 用户关闭自己的工单。错误：CodeNotFound。
func (s *TicketService) CloseOwn(ctx context.Context, p *UserPrincipal, id int64) (*TicketDetail, error) {
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := lockTicket(ctx, q, id, p.UserID); err != nil {
			return err
		}
		return closeTx(ctx, q, id, s.now())
	})
	if err != nil {
		return nil, err
	}
	return s.detail(ctx, id, p.UserID)
}

// closeTx 在调用方的事务里把工单置为已关闭（调用方已锁定工单并确认归属）。
func closeTx(ctx context.Context, q *sqlcgen.Queries, id int64, now time.Time) error {
	if err := q.SetTicketStatus(ctx, sqlcgen.SetTicketStatusParams{ID: id, Status: TicketClosed, Now: timestamptz(now)}); err != nil {
		return fmt.Errorf("close ticket %d: %w", id, err)
	}
	return nil
}

// staffCloseTx 是员工与代理关闭工单的事务体：置为已关闭并写审计（调用方已锁定工单并确认归属）。
func staffCloseTx(ctx context.Context, q *sqlcgen.Queries, actor *AdminPrincipal, id int64, now time.Time, ip *netip.Addr) error {
	if err := closeTx(ctx, q, id, now); err != nil {
		return err
	}
	return writeAudit(ctx, q, adminAudit(actor, ActionTicketClose, targetTicket, id, ip, nil))
}

// staffReplyTx 是员工与代理回复工单的事务体（调用方已锁定工单并确认归属）：插入消息 → 状态变为 answered →
// 给用户的已验证邮箱发不含正文的提醒 → 写审计（不记回复正文）。消息作者记为 actor。
func (s *TicketService) staffReplyTx(ctx context.Context, q *sqlcgen.Queries, actor *AdminPrincipal, ticket sqlcgen.GetTicketForUpdateRow,
	content string, now time.Time, ip *netip.Addr) error {
	if err := q.InsertTicketMessage(ctx, sqlcgen.InsertTicketMessageParams{
		TicketID: ticket.ID, AuthorKind: TicketAuthorAdmin, AdminID: int8Arg(actor.AdminID), Content: content, CreatedAt: timestamptz(now),
	}); err != nil {
		return fmt.Errorf("insert message of ticket %d: %w", ticket.ID, err)
	}
	if err := q.SetTicketStatus(ctx, sqlcgen.SetTicketStatusParams{ID: ticket.ID, Status: TicketAnswered, TouchReply: true, Now: timestamptz(now)}); err != nil {
		return fmt.Errorf("mark ticket %d answered: %w", ticket.ID, err)
	}
	to, err := q.GetUserNotifyEmail(ctx, ticket.UserID)
	if err != nil {
		return fmt.Errorf("get notify email of user %d: %w", ticket.UserID, err)
	}
	s.notify(ctx, q, to, mailTplTicketReply, ticket.Subject)
	return writeAudit(ctx, q, adminAudit(actor, ActionTicketReply, targetTicket, ticket.ID, ip, nil))
}

// ---- 管理端 --------------------------------------------------------------------

// List 分页列出工单，按最近回复倒序。错误：CodeInvalidArgument。
func (s *TicketService) List(ctx context.Context, f TicketFilter, pq PageQuery) (*Page[Ticket], error) {
	return s.page(ctx, f, pq)
}

// Get 返回一张工单与全部消息。错误：CodeNotFound。
func (s *TicketService) Get(ctx context.Context, id int64) (*TicketDetail, error) {
	return s.detail(ctx, id, 0)
}

// Reply 管理员回复，状态变为 answered，并给用户的已验证邮箱发一封不含正文的提醒。
// 错误：CodeInvalidArgument、CodeNotFound。
func (s *TicketService) Reply(ctx context.Context, actor *AdminPrincipal, id int64, content string, ip *netip.Addr) (*TicketDetail, error) {
	content, err := validTicketContent(content)
	if err != nil {
		return nil, err
	}
	now := s.now()
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := lockTicket(ctx, q, id, 0)
		if err != nil {
			return err
		}
		return s.staffReplyTx(ctx, q, actor, row, content, now, ip)
	})
	if err != nil {
		return nil, err
	}
	return s.detail(ctx, id, 0)
}

// Close 管理员关闭工单。错误：CodeNotFound。
func (s *TicketService) Close(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) (*TicketDetail, error) {
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := lockTicket(ctx, q, id, 0); err != nil {
			return err
		}
		return staffCloseTx(ctx, q, actor, id, s.now(), ip)
	})
	if err != nil {
		return nil, err
	}
	return s.detail(ctx, id, 0)
}
