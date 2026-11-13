package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"v2-hub-server/internal/manager/pay"
	"v2-hub-server/internal/service"
)

// 在线充值、收支统计与返利列表接口（docs/03 §5.15、§5.16），与 hub-api.yaml 的 /api/user/wallet/payments、/api/pay/notify/{gateway}、
// /api/admin/payments、/api/admin/rebates、/api/admin/finance/stats 逐条对应。

const (
	pathParamGateway = "gateway"
	queryDays        = "days"
	queryInviterID   = "inviterId"
	contentTypeForm  = "application/x-www-form-urlencoded"
	msgBadDays       = "days 须为正整数"
	msgBadInviter    = "inviterId 须为正整数"
	msgBadNotifyBody = "通知内容无法读取"
)

// PayManager 是在线充值的业务接口，由 service.PayService 实现。
type PayManager interface {
	Gateways() []service.GatewayInfo
	CreatePayment(ctx context.Context, p *service.UserPrincipal, gateway string, amount int64, ip *netip.Addr) (*service.PaymentCreated, error)
	HandleNotify(ctx context.Context, gateway string, req pay.NotifyRequest) (string, error)
	ListPayments(ctx context.Context, f service.PaymentFilter, pq service.PageQuery) (*service.Page[service.Payment], error)
}

// FinanceManager 是收支统计与返利列表的业务接口，由 service.ShopService 实现。
type FinanceManager interface {
	FinanceStats(ctx context.Context, days int) (*service.FinanceStats, error)
	FinanceStatsBy(ctx context.Context, fq service.FinanceQuery) (*service.FinanceStats, error)
	ExportFinanceCSV(ctx context.Context, actor *service.AdminPrincipal, fq service.FinanceQuery, ip *netip.Addr) ([]byte, string, error)
	ListRebates(ctx context.Context, inviterID int64, pq service.PageQuery) (*service.Page[service.RebateRecord], error)
}

// paymentCreateBody 对应 hub-api.yaml PaymentCreateInput。
type paymentCreateBody struct {
	Gateway string `json:"gateway"`
	Amount  int64  `json:"amount"`
}

// paymentCreatedVO 对应 hub-api.yaml PaymentCreated。
type paymentCreatedVO struct {
	TradeNo   string    `json:"tradeNo"`
	PayURL    string    `json:"payUrl"`
	QRContent string    `json:"qrContent"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (h *handlers) createPayment(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	var req paymentCreateBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	res, err := h.pay.CreatePayment(r.Context(), p, req.Gateway, req.Amount, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, paymentCreatedVO{TradeNo: res.TradeNo, PayURL: res.PayURL, QRContent: res.QRContent, ExpiresAt: res.ExpiresAt})
}

// paymentNotify 接收支付网关的异步通知。没有会话与 CSRF：真实性靠网关实现验签；应答是网关要求的纯文本。
func (h *handlers) paymentNotify(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBodyLen))
	if err != nil {
		writeError(w, r, badRequest(msgBadNotifyBody))
		return
	}
	form := url.Values{}
	if strings.HasPrefix(r.Header.Get(headerContent), contentTypeForm) {
		if form, err = url.ParseQuery(string(body)); err != nil {
			writeError(w, r, badRequest(msgBadNotifyBody))
			return
		}
	}
	ack, err := h.pay.HandleNotify(r.Context(), r.PathValue(pathParamGateway), pay.NotifyRequest{Header: r.Header, Query: r.URL.Query(), Form: form, Body: body})
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set(headerContent, contentTypePlain)
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, ack); err != nil {
		// 应答写不出去只能记录；网关没收到应答会重发通知，入账是幂等的
		slog.Warn("write payment notify ack", "gateway", r.PathValue(pathParamGateway), "err", err)
	}
}

// paymentVO 对应 hub-api.yaml Payment。
type paymentVO struct {
	ID        int64      `json:"id"`
	TradeNo   string     `json:"tradeNo"`
	Gateway   string     `json:"gateway"`
	Amount    int64      `json:"amount"`
	Status    string     `json:"status"`
	PaidAt    *time.Time `json:"paidAt"`
	CreatedAt time.Time  `json:"createdAt"`
	UserID    *int64     `json:"userId"`
	Username  string     `json:"username"`
}

type paymentPageVO struct {
	Items []paymentVO `json:"items"`
	Total int64       `json:"total"`
}

func (h *handlers) listPayments(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	userID, err := optionalUserID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.pay.ListPayments(r.Context(), service.PaymentFilter{UserID: userID, Status: r.URL.Query().Get(queryStatus)}, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := paymentPageVO{Items: make([]paymentVO, 0, len(page.Items)), Total: page.Total}
	for _, p := range page.Items {
		vo := paymentVO{ID: p.ID, TradeNo: p.TradeNo, Gateway: p.Gateway, Amount: p.Amount, Status: p.Status, PaidAt: p.PaidAt, CreatedAt: p.CreatedAt, Username: p.Username}
		if p.UserID != 0 {
			vo.UserID = &p.UserID
		}
		out.Items = append(out.Items, vo)
	}
	writeOK(w, out)
}

// rebateVO 对应 hub-api.yaml Rebate。
type rebateVO struct {
	ID              int64     `json:"id"`
	InviterUsername string    `json:"inviterUsername"`
	InviteeUsername string    `json:"inviteeUsername"`
	OrderNo         string    `json:"orderNo"`
	OrderAmount     int64     `json:"orderAmount"`
	Percent         int       `json:"percent"`
	Amount          int64     `json:"amount"`
	CreatedAt       time.Time `json:"createdAt"`
}

type rebatePageVO struct {
	Items []rebateVO `json:"items"`
	Total int64      `json:"total"`
}

func (h *handlers) listRebates(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var inviterID int64
	if raw := r.URL.Query().Get(queryInviterID); raw != "" {
		if inviterID, err = strconv.ParseInt(raw, 10, 64); err != nil || inviterID < 1 {
			writeError(w, r, badRequest(msgBadInviter))
			return
		}
	}
	page, err := h.finance.ListRebates(r.Context(), inviterID, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := rebatePageVO{Items: make([]rebateVO, 0, len(page.Items)), Total: page.Total}
	for _, rb := range page.Items {
		out.Items = append(out.Items, rebateVO{ID: rb.ID, InviterUsername: rb.InviterUsername, InviteeUsername: rb.InviteeUsername, OrderNo: rb.OrderNo,
			OrderAmount: rb.OrderAmount, Percent: rb.Percent, Amount: rb.Amount, CreatedAt: rb.CreatedAt})
	}
	writeOK(w, out)
}

type dayStatsVO struct {
	Date       string `json:"date"`
	Topup      int64  `json:"topup"`
	Spend      int64  `json:"spend"`
	Rebate     int64  `json:"rebate"`
	OrderCount int    `json:"orderCount"`
}

type statsTotalsVO struct {
	Balance int64 `json:"balance"`
	Topup   int64 `json:"topup"`
	Spend   int64 `json:"spend"`
	Rebate  int64 `json:"rebate"`
}

// financeStatsVO 对应 hub-api.yaml FinanceStats。
type financeStatsVO struct {
	Granularity string        `json:"granularity"`
	Days        []dayStatsVO  `json:"days"`
	Totals      statsTotalsVO `json:"totals"`
}

func (h *handlers) getFinanceStats(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	fq, err := financeQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	stats, err := h.finance.FinanceStatsBy(r.Context(), fq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := financeStatsVO{Granularity: stats.Granularity, Days: make([]dayStatsVO, 0, len(stats.Days)), Totals: statsTotalsVO{Balance: stats.Balance, Topup: stats.Topup, Spend: stats.Spend, Rebate: stats.Rebate}}
	for _, d := range stats.Days {
		out.Days = append(out.Days, dayStatsVO{Date: d.Date, Topup: d.Topup, Spend: d.Spend, Rebate: d.Rebate, OrderCount: d.OrderCount})
	}
	writeOK(w, out)
}
