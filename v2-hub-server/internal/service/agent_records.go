package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"

	"v2-hub-server/internal/dao/sqlcgen"
)

// 代理侧的邀请码、工单、订单、余额流水与开通记录（docs/03 §5.23）。每个查询都带 agent_id = 当前代理 的条件。

// ---- 邀请码 ----------------------------------------------------------------------

func agentInviteFromRow(r sqlcgen.AgentListInviteCodesRow, agentID int64) InviteCode {
	id := agentID
	return InviteCode{
		ID: r.ID, Code: r.Code, Kind: InviteKindAgent, AgentID: &id, AgentUsername: r.AgentUsername, MaxUses: int(r.MaxUses),
		UsedCount: int(r.UsedCount), ExpiresAt: timePtr(r.ExpiresAt), IsDisabled: r.IsDisabled, Remark: r.Remark, CreatedAt: r.CreatedAt.Time,
	}
}

// ListInviteCodes 分页列出当前代理自己的邀请码。
func (s *AgentService) ListInviteCodes(ctx context.Context, p *AdminPrincipal, pq PageQuery) (*Page[InviteCode], error) {
	agentID, err := agentScope(p)
	if err != nil {
		return nil, err
	}
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	total, err := s.q.AgentCountInviteCodes(ctx, agentKey(agentID))
	if err != nil {
		return nil, fmt.Errorf("count invite codes of agent %d: %w", agentID, err)
	}
	rows, err := s.q.AgentListInviteCodes(ctx, sqlcgen.AgentListInviteCodesParams{AgentID: agentKey(agentID), RowLimit: limit, RowOffset: offset})
	if err != nil {
		return nil, fmt.Errorf("list invite codes of agent %d: %w", agentID, err)
	}
	items := make([]InviteCode, 0, len(rows))
	for _, r := range rows {
		items = append(items, agentInviteFromRow(r, agentID))
	}
	return &Page[InviteCode]{Items: items, Total: total}, nil
}

func (s *AgentService) getInviteCode(ctx context.Context, agentID, id int64) (*InviteCode, error) {
	rows, err := s.q.AgentListInviteCodes(ctx, sqlcgen.AgentListInviteCodesParams{AgentID: agentKey(agentID), ID: int8Arg(id), RowLimit: 1})
	if err != nil {
		return nil, fmt.Errorf("get invite code %d of agent %d: %w", id, agentID, err)
	}
	if len(rows) == 0 {
		return nil, newError(CodeNotFound, msgInviteNotFound)
	}
	c := agentInviteFromRow(rows[0], agentID)
	return &c, nil
}

// CreateInviteCode 新建当前代理的邀请码，码由服务端生成；经它注册的用户归该代理（Ruling-102）。错误：CodeInvalidArgument。
func (s *AgentService) CreateInviteCode(ctx context.Context, p *AdminPrincipal, in InviteCodeInput, ip *netip.Addr) (*InviteCode, error) {
	agentID, err := agentScope(p)
	if err != nil {
		return nil, err
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	op := fmt.Sprintf("create invite code of agent %d", agentID)
	id, err := s.insertWithFreshInviteCode(ctx, op, func(q *sqlcgen.Queries, code string) (int64, error) {
		id, err := q.AgentCreateInviteCode(ctx, sqlcgen.AgentCreateInviteCodeParams{
			Code: code, MaxUses: int32(in.MaxUses), ExpiresAt: optionalTime(in.ExpiresAt), IsDisabled: in.IsDisabled, Remark: in.Remark,
			AgentID: agentKey(agentID),
		})
		if err != nil {
			return 0, err
		}
		return id, writeAudit(ctx, q, adminAudit(p, ActionInviteCreate, targetInvite, id, ip, map[string]any{"maxUses": in.MaxUses}))
	})
	if err != nil {
		return nil, err
	}
	return s.getInviteCode(ctx, agentID, id)
}

// lockAgentInviteCode 锁住当前代理自己的一条邀请码；不是他的码一律 CodeNotFound。
func lockAgentInviteCode(ctx context.Context, q *sqlcgen.Queries, agentID, id int64) error {
	_, err := q.AgentLockInviteCode(ctx, sqlcgen.AgentLockInviteCodeParams{ID: id, AgentID: agentKey(agentID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return newError(CodeNotFound, msgInviteNotFound)
	}
	if err != nil {
		return fmt.Errorf("lock invite code %d of agent %d: %w", id, agentID, err)
	}
	return nil
}

// UpdateInviteCode 修改当前代理自己的邀请码。错误：CodeInvalidArgument、CodeNotFound。
func (s *AgentService) UpdateInviteCode(ctx context.Context, p *AdminPrincipal, id int64, in InviteCodeInput, ip *netip.Addr) (*InviteCode, error) {
	agentID, err := agentScope(p)
	if err != nil {
		return nil, err
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := lockAgentInviteCode(ctx, q, agentID, id); err != nil {
			return err
		}
		if _, err := q.AgentUpdateInviteCode(ctx, sqlcgen.AgentUpdateInviteCodeParams{
			ID: id, AgentID: agentKey(agentID), MaxUses: int32(in.MaxUses), ExpiresAt: optionalTime(in.ExpiresAt), IsDisabled: in.IsDisabled, Remark: in.Remark,
		}); err != nil {
			return fmt.Errorf("update invite code %d of agent %d: %w", id, agentID, err)
		}
		return writeAudit(ctx, q, adminAudit(p, ActionInviteUpdate, targetInvite, id, ip, map[string]any{"isDisabled": in.IsDisabled}))
	})
	if err != nil {
		return nil, err
	}
	return s.getInviteCode(ctx, agentID, id)
}

// DeleteInviteCode 删除当前代理自己的邀请码（已注册用户的归属不变）。错误：CodeNotFound。
func (s *AgentService) DeleteInviteCode(ctx context.Context, p *AdminPrincipal, id int64, ip *netip.Addr) error {
	agentID, err := agentScope(p)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := lockAgentInviteCode(ctx, q, agentID, id); err != nil {
			return err
		}
		if _, err := q.AgentDeleteInviteCode(ctx, sqlcgen.AgentDeleteInviteCodeParams{ID: id, AgentID: agentKey(agentID)}); err != nil {
			return fmt.Errorf("delete invite code %d of agent %d: %w", id, agentID, err)
		}
		return writeAudit(ctx, q, adminAudit(p, ActionInviteDelete, targetInvite, id, ip, nil))
	})
}

// ---- 工单 ------------------------------------------------------------------------

func agentTicketsFromRows(rows []sqlcgen.AgentListTicketsRow) []Ticket {
	converted := make([]sqlcgen.ListTicketsRow, 0, len(rows))
	for _, r := range rows {
		// 两条查询的行结构逐字段相同，可直接转换
		converted = append(converted, sqlcgen.ListTicketsRow(r))
	}
	return ticketsFromRows(converted)
}

// ListTickets 分页列出名下用户的工单，按最近回复倒序；UserID 不在名下时返回空列表。错误：CodeInvalidArgument。
func (s *AgentService) ListTickets(ctx context.Context, p *AdminPrincipal, f TicketFilter, pq PageQuery) (*Page[Ticket], error) {
	agentID, err := agentScope(p)
	if err != nil {
		return nil, err
	}
	if f.Status != "" && !isTicketStatus(f.Status) {
		return nil, newError(CodeInvalidArgument, msgTicketBadStatus)
	}
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	total, err := s.q.AgentCountTickets(ctx, sqlcgen.AgentCountTicketsParams{AgentID: agentKey(agentID), UserID: int8Arg(f.UserID), Status: textArg(f.Status)})
	if err != nil {
		return nil, fmt.Errorf("count tickets of agent %d: %w", agentID, err)
	}
	rows, err := s.q.AgentListTickets(ctx, sqlcgen.AgentListTicketsParams{
		AgentID: agentKey(agentID), UserID: int8Arg(f.UserID), Status: textArg(f.Status), RowLimit: limit, RowOffset: offset,
	})
	if err != nil {
		return nil, fmt.Errorf("list tickets of agent %d: %w", agentID, err)
	}
	return &Page[Ticket]{Items: agentTicketsFromRows(rows), Total: total}, nil
}

// GetTicket 返回名下用户的一张工单与全部消息；员工的回复不带账号名。不是名下用户的工单 CodeNotFound。
func (s *AgentService) GetTicket(ctx context.Context, p *AdminPrincipal, id int64) (*TicketDetail, error) {
	agentID, err := agentScope(p)
	if err != nil {
		return nil, err
	}
	return s.ticketDetail(ctx, agentID, id)
}

func (s *AgentService) ticketDetail(ctx context.Context, agentID, id int64) (*TicketDetail, error) {
	rows, err := s.q.AgentListTickets(ctx, sqlcgen.AgentListTicketsParams{AgentID: agentKey(agentID), ID: int8Arg(id), RowLimit: 1})
	if err != nil {
		return nil, fmt.Errorf("get ticket %d of agent %d: %w", id, agentID, err)
	}
	if len(rows) == 0 {
		return nil, newError(CodeNotFound, msgTicketNotFound)
	}
	msgs, err := s.q.AgentListTicketMessages(ctx, sqlcgen.AgentListTicketMessagesParams{AgentID: agentID, TicketID: id})
	if err != nil {
		return nil, fmt.Errorf("list messages of ticket %d: %w", id, err)
	}
	converted := make([]sqlcgen.ListTicketMessagesRow, 0, len(msgs))
	for _, m := range msgs {
		// 两条查询的行结构逐字段相同，可直接转换
		converted = append(converted, sqlcgen.ListTicketMessagesRow(m))
	}
	return &TicketDetail{Ticket: agentTicketsFromRows(rows)[0], Messages: ticketMessagesFromRows(converted)}, nil
}

// lockAgentTicket 锁住名下用户的一张工单；不是名下用户的工单一律 CodeNotFound。
func lockAgentTicket(ctx context.Context, q *sqlcgen.Queries, agentID, id int64) (sqlcgen.GetTicketForUpdateRow, error) {
	row, err := q.AgentLockTicket(ctx, sqlcgen.AgentLockTicketParams{ID: id, AgentID: agentKey(agentID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlcgen.GetTicketForUpdateRow{}, newError(CodeNotFound, msgTicketNotFound)
	}
	if err != nil {
		return sqlcgen.GetTicketForUpdateRow{}, fmt.Errorf("lock ticket %d of agent %d: %w", id, agentID, err)
	}
	// 两条查询的行结构逐字段相同，可直接转换
	return sqlcgen.GetTicketForUpdateRow(row), nil
}

// ReplyTicket 代理回复名下用户的工单：状态变为 answered，并给用户的已验证邮箱发不含正文的提醒（同员工回复）。
// 错误：CodeInvalidArgument、CodeNotFound。
func (s *AgentService) ReplyTicket(ctx context.Context, p *AdminPrincipal, id int64, content string, ip *netip.Addr) (*TicketDetail, error) {
	agentID, err := agentScope(p)
	if err != nil {
		return nil, err
	}
	content, err = validTicketContent(content)
	if err != nil {
		return nil, err
	}
	now := s.now()
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := lockAgentTicket(ctx, q, agentID, id)
		if err != nil {
			return err
		}
		return s.tickets.staffReplyTx(ctx, q, p, row, content, now, ip)
	})
	if err != nil {
		return nil, err
	}
	return s.ticketDetail(ctx, agentID, id)
}

// CloseTicket 关闭名下用户的工单。错误：CodeNotFound。
func (s *AgentService) CloseTicket(ctx context.Context, p *AdminPrincipal, id int64, ip *netip.Addr) (*TicketDetail, error) {
	agentID, err := agentScope(p)
	if err != nil {
		return nil, err
	}
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := lockAgentTicket(ctx, q, agentID, id); err != nil {
			return err
		}
		return staffCloseTx(ctx, q, p, id, s.now(), ip)
	})
	if err != nil {
		return nil, err
	}
	return s.ticketDetail(ctx, agentID, id)
}

// ---- 订单与余额流水（只读，只含现存名下用户，Ruling-102） ------------------------

// ListOrders 分页列出名下未删除用户的订单，按时间倒序；UserID 不在名下时返回空列表。错误：CodeInvalidArgument。
func (s *AgentService) ListOrders(ctx context.Context, p *AdminPrincipal, f OrderFilter, pq PageQuery) (*Page[Order], error) {
	agentID, err := agentScope(p)
	if err != nil {
		return nil, err
	}
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	from, to := optionalTime(f.From), optionalTime(f.To)
	total, err := s.q.AgentCountOrders(ctx, sqlcgen.AgentCountOrdersParams{
		AgentID: agentKey(agentID), UserID: int8Arg(f.UserID), Kind: textArg(f.Kind), FromTime: from, ToTime: to,
	})
	if err != nil {
		return nil, fmt.Errorf("count orders of agent %d: %w", agentID, err)
	}
	rows, err := s.q.AgentListOrders(ctx, sqlcgen.AgentListOrdersParams{
		AgentID: agentKey(agentID), UserID: int8Arg(f.UserID), Kind: textArg(f.Kind), FromTime: from, ToTime: to, RowLimit: limit, RowOffset: offset,
	})
	if err != nil {
		return nil, fmt.Errorf("list orders of agent %d: %w", agentID, err)
	}
	converted := make([]sqlcgen.ListOrdersRow, 0, len(rows))
	for _, r := range rows {
		// 两条查询的行结构逐字段相同，可直接转换
		converted = append(converted, sqlcgen.ListOrdersRow(r))
	}
	items, err := ordersFromRows(converted)
	if err != nil {
		return nil, err
	}
	return &Page[Order]{Items: items, Total: total}, nil
}

// ListLedger 分页列出名下未删除用户的余额流水，按时间倒序；操作人恒为空串（不向代理暴露员工账号）。错误：CodeInvalidArgument。
func (s *AgentService) ListLedger(ctx context.Context, p *AdminPrincipal, f LedgerFilter, pq PageQuery) (*Page[LedgerEntry], error) {
	agentID, err := agentScope(p)
	if err != nil {
		return nil, err
	}
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	total, err := s.q.AgentCountLedger(ctx, sqlcgen.AgentCountLedgerParams{AgentID: agentKey(agentID), UserID: int8Arg(f.UserID), Kind: textArg(f.Kind)})
	if err != nil {
		return nil, fmt.Errorf("count ledger of agent %d: %w", agentID, err)
	}
	rows, err := s.q.AgentListLedger(ctx, sqlcgen.AgentListLedgerParams{
		AgentID: agentKey(agentID), UserID: int8Arg(f.UserID), Kind: textArg(f.Kind), RowLimit: limit, RowOffset: offset,
	})
	if err != nil {
		return nil, fmt.Errorf("list ledger of agent %d: %w", agentID, err)
	}
	items := make([]LedgerEntry, 0, len(rows))
	for _, r := range rows {
		items = append(items, LedgerEntry{
			ID: r.ID, UserID: r.UserID.Int64, Username: r.Username, Kind: r.Kind, Amount: r.Amount, BalanceAfter: r.BalanceAfter,
			Remark: r.Remark, CreatedAt: r.CreatedAt.Time,
		})
	}
	return &Page[LedgerEntry]{Items: items, Total: total}, nil
}

// ---- 开通记录 --------------------------------------------------------------------

// AgentAllocation 是一条开通记录（Ruling-103）；UserID / PlanID 为 nil 表示用户 / 套餐已删除。
type AgentAllocation struct {
	ID           int64
	AgentID      int64
	UserID       *int64
	Username     string
	PlanID       *int64
	PlanName     string
	TrafficBytes int64
	DurationDays int
	Kind         string
	CreatedAt    time.Time
}

// listAllocations 按时间倒序分页列出某个代理的开通记录；agentID 由调用方保证是代理（代理侧取自会话，超管侧先确认）。
func (s *AgentService) listAllocations(ctx context.Context, agentID int64, from, to *time.Time, pq PageQuery) (*Page[AgentAllocation], error) {
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	if err := validateTimeRange(from, to); err != nil {
		return nil, err
	}
	fromArg, toArg := optionalTime(from), optionalTime(to)
	total, err := s.q.CountAgentAllocations(ctx, sqlcgen.CountAgentAllocationsParams{AgentID: agentID, FromTime: fromArg, ToTime: toArg})
	if err != nil {
		return nil, fmt.Errorf("count allocations of agent %d: %w", agentID, err)
	}
	rows, err := s.q.ListAgentAllocations(ctx, sqlcgen.ListAgentAllocationsParams{
		AgentID: agentID, FromTime: fromArg, ToTime: toArg, RowLimit: limit, RowOffset: offset,
	})
	if err != nil {
		return nil, fmt.Errorf("list allocations of agent %d: %w", agentID, err)
	}
	items := make([]AgentAllocation, 0, len(rows))
	for _, r := range rows {
		items = append(items, AgentAllocation{
			ID: r.ID, AgentID: r.AgentID, UserID: int8Ptr(r.UserID), Username: r.Username, PlanID: int8Ptr(r.PlanID), PlanName: r.PlanName,
			TrafficBytes: r.TrafficBytes, DurationDays: int(r.DurationDays), Kind: r.Kind, CreatedAt: r.CreatedAt.Time,
		})
	}
	return &Page[AgentAllocation]{Items: items, Total: total}, nil
}

// ListOwnAllocations 列出当前代理自己的开通记录（含已删除用户的记录）。错误：CodeInvalidArgument。
func (s *AgentService) ListOwnAllocations(ctx context.Context, p *AdminPrincipal, from, to *time.Time, pq PageQuery) (*Page[AgentAllocation], error) {
	agentID, err := agentScope(p)
	if err != nil {
		return nil, err
	}
	return s.listAllocations(ctx, agentID, from, to, pq)
}
