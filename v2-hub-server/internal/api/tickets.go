package api

import (
	"context"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"v2-hub-server/internal/service"
)

// 工单与签到接口（docs/03 §5.10、§5.11），与 hub-api.yaml 的 admin-tickets 及用户侧对应路径逐条对应。

// TicketManager 是工单接口需要的业务能力（由 service.TicketService 实现）。
type TicketManager interface {
	List(ctx context.Context, f service.TicketFilter, pq service.PageQuery) (*service.Page[service.Ticket], error)
	Get(ctx context.Context, id int64) (*service.TicketDetail, error)
	Reply(ctx context.Context, actor *service.AdminPrincipal, id int64, content string, ip *netip.Addr) (*service.TicketDetail, error)
	Close(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) (*service.TicketDetail, error)
	ListOwn(ctx context.Context, p *service.UserPrincipal, pq service.PageQuery) (*service.Page[service.Ticket], error)
	GetOwn(ctx context.Context, p *service.UserPrincipal, id int64) (*service.TicketDetail, error)
	Create(ctx context.Context, p *service.UserPrincipal, subject, content string) (*service.TicketDetail, error)
	ReplyOwn(ctx context.Context, p *service.UserPrincipal, id int64, content string) (*service.TicketDetail, error)
	CloseOwn(ctx context.Context, p *service.UserPrincipal, id int64) (*service.TicketDetail, error)
}

// ticketVO 对应 hub-api.yaml Ticket；userId、username 只在管理端输出。
type ticketVO struct {
	ID          int64     `json:"id"`
	Subject     string    `json:"subject"`
	Status      string    `json:"status"`
	LastReplyAt time.Time `json:"lastReplyAt"`
	CreatedAt   time.Time `json:"createdAt"`
	UserID      *int64    `json:"userId,omitempty"`
	Username    string    `json:"username,omitempty"`
}

func toTicketVO(t service.Ticket, forAdmin bool) ticketVO {
	vo := ticketVO{ID: t.ID, Subject: t.Subject, Status: t.Status, LastReplyAt: t.LastReplyAt, CreatedAt: t.CreatedAt}
	if forAdmin {
		uid := t.UserID
		vo.UserID, vo.Username = &uid, t.Username
	}
	return vo
}

// ticketMessageVO 对应 hub-api.yaml TicketMessage；authorName 只在管理端输出，用户侧不暴露管理员用户名。
type ticketMessageVO struct {
	ID         int64     `json:"id"`
	AuthorKind string    `json:"authorKind"`
	AuthorName string    `json:"authorName,omitempty"`
	Content    string    `json:"content"`
	CreatedAt  time.Time `json:"createdAt"`
}

type ticketDetailVO struct {
	ticketVO
	Messages []ticketMessageVO `json:"messages"`
}

func toTicketDetailVO(d *service.TicketDetail, forAdmin bool) ticketDetailVO {
	out := ticketDetailVO{ticketVO: toTicketVO(d.Ticket, forAdmin), Messages: make([]ticketMessageVO, 0, len(d.Messages))}
	for _, m := range d.Messages {
		vo := ticketMessageVO{ID: m.ID, AuthorKind: m.AuthorKind, Content: m.Content, CreatedAt: m.CreatedAt}
		if forAdmin {
			vo.AuthorName = m.AuthorName
		}
		out.Messages = append(out.Messages, vo)
	}
	return out
}

type ticketPageVO struct {
	Items []ticketVO `json:"items"`
	Total int64      `json:"total"`
}

func toTicketPageVO(page *service.Page[service.Ticket], forAdmin bool) ticketPageVO {
	out := ticketPageVO{Items: make([]ticketVO, 0, len(page.Items)), Total: page.Total}
	for _, t := range page.Items {
		out.Items = append(out.Items, toTicketVO(t, forAdmin))
	}
	return out
}

type ticketCreateRequest struct {
	Subject string `json:"subject"`
	Content string `json:"content"`
}

type ticketMessageRequest struct {
	Content string `json:"content"`
}

// ---- 管理端 ----

func (h *handlers) listTickets(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	f := service.TicketFilter{Status: r.URL.Query().Get(queryStatus)}
	if raw := r.URL.Query().Get(queryUserID); raw != "" {
		if f.UserID, err = strconv.ParseInt(raw, 10, 64); err != nil || f.UserID < 1 {
			writeError(w, r, badRequest(msgBadID))
			return
		}
	}
	page, err := h.tickets.List(r.Context(), f, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toTicketPageVO(page, true))
}

func (h *handlers) getTicket(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	d, err := h.tickets.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toTicketDetailVO(d, true))
}

func (h *handlers) replyTicket(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req ticketMessageRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	d, err := h.tickets.Reply(r.Context(), p, id, req.Content, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toTicketDetailVO(d, true))
}

func (h *handlers) closeTicket(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	d, err := h.tickets.Close(r.Context(), p, id, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toTicketDetailVO(d, true))
}

// ---- 用户侧 ----

func (h *handlers) listOwnTickets(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.tickets.ListOwn(r.Context(), p, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toTicketPageVO(page, false))
}

func (h *handlers) createTicket(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	var req ticketCreateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	d, err := h.tickets.Create(r.Context(), p, req.Subject, req.Content)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toTicketDetailVO(d, false))
}

func (h *handlers) getOwnTicket(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	d, err := h.tickets.GetOwn(r.Context(), p, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toTicketDetailVO(d, false))
}

func (h *handlers) replyOwnTicket(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req ticketMessageRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	d, err := h.tickets.ReplyOwn(r.Context(), p, id, req.Content)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toTicketDetailVO(d, false))
}

func (h *handlers) closeOwnTicket(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	d, err := h.tickets.CloseOwn(r.Context(), p, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toTicketDetailVO(d, false))
}

// ---- 签到 ----

const checkinDateLayout = "2006-01-02"

type checkinLogVO struct {
	Date        string `json:"date"`
	RewardBytes int64  `json:"rewardBytes"`
}

// checkinInfoVO 对应 hub-api.yaml CheckinInfo。
type checkinInfoVO struct {
	Enabled      bool           `json:"enabled"`
	CanCheckin   bool           `json:"canCheckin"`
	Reason       string         `json:"reason"`
	CheckedToday bool           `json:"checkedToday"`
	Logs         []checkinLogVO `json:"logs"`
}

type checkinResultVO struct {
	RewardBytes  int64 `json:"rewardBytes"`
	TrafficLimit int64 `json:"trafficLimit"`
}

func (h *handlers) getCheckin(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	info, err := h.portal.GetCheckin(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := checkinInfoVO{Enabled: info.Enabled, CanCheckin: info.CanCheckin, Reason: info.Reason, CheckedToday: info.CheckedToday,
		Logs: make([]checkinLogVO, 0, len(info.Logs))}
	for _, l := range info.Logs {
		out.Logs = append(out.Logs, checkinLogVO{Date: l.Date.Format(checkinDateLayout), RewardBytes: l.RewardBytes})
	}
	writeOK(w, out)
}

func (h *handlers) checkin(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	res, err := h.portal.Checkin(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, checkinResultVO{RewardBytes: res.RewardBytes, TrafficLimit: res.TrafficLimit})
}
