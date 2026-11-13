package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/pay"
	"v2-hub-server/internal/manager/ratelimit"
)

// 在线充值（docs/03 §5.15，Ruling-28）：只给余额充值，购买仍从余额扣款。入账以网关的异步通知为准，重复通知只入账一次。

// 充值单状态。
const (
	PaymentPending = "pending"
	PaymentPaid    = "paid"
	PaymentExpired = "expired"
)

const (
	paymentNoPrefix     = "P"
	paymentTTL          = 30 * time.Minute
	minPaymentAmount    = 100 // 最低充值 1 元
	paymentsPerHour     = 10
	paymentNotifyPath   = "/api/pay/notify/"
	constraintPaymentNo = "uk_payment_trade_no"
	guardKeyPayment     = "payment:"

	ActionPaymentCreate = "portal.payment_create"
	ActionPaymentPaid   = "payment.paid"
	targetPayment       = "payment"

	msgPayNoGateway  = "没有开通在线充值"
	msgPayBadGateway = "不支持的支付方式"
	msgPayBadAmount  = "充值金额须在 1 元到 999999.99 元之间"
	msgPayTooOften   = "发起充值过于频繁，请稍后再试"
	msgPayBadNotify  = "支付通知无效"
	msgPayBadStatus  = "status 只能是 pending、paid、expired"
)

// PayService 管在线充值单：发起、回调入账、过期与列表。并发安全。
type PayService struct {
	store
	settings *SettingsService
	gateways *pay.Registry
	telegram *TelegramService
	now      func() time.Time
	rate     *ratelimit.FailureGuard
}

// NewPayService 创建在线充值服务。gateways 为空表示没有配置任何网关：充值入口不出现，发起接口返回 CodeFeatureDisabled。
// now 为 nil 时用 time.Now。
// telegram 为 nil 表示入账时不发 Telegram 通知（docs/03 §5.20）。
func NewPayService(pool *pgxpool.Pool, settings *SettingsService, gateways *pay.Registry, telegram *TelegramService, now func() time.Time) *PayService {
	if now == nil {
		now = time.Now
	}
	rate := ratelimit.NewFailureGuard(ratelimit.FailureGuardConfig{MaxFailures: paymentsPerHour, Window: time.Hour, Lockout: time.Hour, MaxKeys: shopGuardMaxKeys}, now)
	return &PayService{store: newStore(pool), settings: settings, gateways: gateways, telegram: telegram, now: now, rate: rate}
}

// GatewayInfo 是展示给用户的充值方式。
type GatewayInfo struct {
	Code string
	Name string
}

// Gateways 返回可用的在线充值方式；没有配置时为空。
func (s *PayService) Gateways() []GatewayInfo {
	list := s.gateways.List()
	out := make([]GatewayInfo, 0, len(list))
	for _, g := range list {
		out = append(out, GatewayInfo{Code: g.Code(), Name: g.Name()})
	}
	return out
}

// PaymentCreated 是发起充值的结果：PayURL 与 QRContent 由网关决定给哪一个。
type PaymentCreated struct {
	TradeNo   string
	PayURL    string
	QRContent string
	ExpiresAt time.Time
}

// CreatePayment 发起一笔在线充值：先落一张待支付的充值单，再向网关下单。
// 错误：CodeFeatureDisabled（商店未开启或没有配置网关）、CodeInvalidArgument、CodeRateLimited、CodeUpstream（网关下单失败）。
func (s *PayService) CreatePayment(ctx context.Context, p *UserPrincipal, gatewayCode string, amount int64, ip *netip.Addr) (*PaymentCreated, error) {
	cur, err := requireShop(ctx, s.settings)
	if err != nil {
		return nil, err
	}
	if len(s.gateways.List()) == 0 {
		return nil, newError(CodeFeatureDisabled, msgPayNoGateway)
	}
	gateway, ok := s.gateways.Get(gatewayCode)
	if !ok {
		return nil, newError(CodeInvalidArgument, msgPayBadGateway)
	}
	if amount < minPaymentAmount || amount > maxMoney {
		return nil, newError(CodeInvalidArgument, msgPayBadAmount)
	}
	if err := takeRate(s.rate, fmt.Sprint(guardKeyPayment, p.UserID)); err != nil {
		return nil, newError(CodeRateLimited, msgPayTooOften)
	}
	now := s.now()
	expiresAt := now.Add(paymentTTL)
	tradeNo := ""
	for range orderNoRetries {
		if tradeNo, err = newOrderNo(paymentNoPrefix, now); err != nil {
			return nil, err
		}
		err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
			id, err := q.InsertPayment(ctx, sqlcgen.InsertPaymentParams{
				TradeNo: tradeNo, UserID: pgtype.Int8{Int64: p.UserID, Valid: true}, Username: p.Username, Gateway: gateway.Code(), Amount: amount,
				ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true}, CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
			})
			if err != nil {
				return err
			}
			return writeAudit(ctx, q, AuditEntry{ActorType: ActorUser, ActorID: p.UserID, Action: ActionPaymentCreate, TargetType: targetPayment,
				TargetID: fmt.Sprint(id), ClientIP: ip, Detail: map[string]any{"tradeNo": tradeNo, "gateway": gateway.Code(), "amount": amount}})
		})
		if violatedConstraint(err) != constraintPaymentNo {
			break // 成功，或不是撞号的其它错误
		}
	}
	if err != nil {
		return nil, fmt.Errorf("insert payment for user %d: %w", p.UserID, err)
	}
	res, err := gateway.Create(ctx, pay.CreateRequest{
		TradeNo: tradeNo, Amount: amount, Subject: cur.Site.Name, ExpiresAt: expiresAt,
		NotifyURL: strings.TrimRight(cur.Site.PortalURL, "/") + paymentNotifyPath + gateway.Code(),
	})
	if err != nil {
		// 充值单留在待支付状态，到期自动过期；不向用户透出网关的原始报错
		slog.Warn("payment gateway create failed", "gateway", gateway.Code(), "trade_no", tradeNo, "err", err)
		return nil, newError(CodeUpstream, "支付网关暂时不可用，请稍后再试")
	}
	return &PaymentCreated{TradeNo: tradeNo, PayURL: res.PayURL, QRContent: res.QRContent, ExpiresAt: expiresAt}, nil
}

// HandleNotify 处理网关的异步通知：验签由网关实现完成，这里按平台单号幂等入账，返回网关要求的应答文本。
// 错误：CodeNotFound（没有这个网关或这张充值单）、CodeInvalidArgument（通知无效、网关或金额与充值单不符）。
func (s *PayService) HandleNotify(ctx context.Context, gatewayCode string, req pay.NotifyRequest) (string, error) {
	gateway, ok := s.gateways.Get(gatewayCode)
	if !ok {
		return "", newError(CodeNotFound, msgPayBadGateway)
	}
	n, err := gateway.ParseNotify(ctx, req)
	if errors.Is(err, pay.ErrBadNotify) {
		return "", newError(CodeInvalidArgument, msgPayBadNotify)
	}
	if err != nil {
		return "", fmt.Errorf("parse %s notification: %w", gatewayCode, err)
	}
	if !n.Paid {
		return n.Ack, nil
	}
	if n.TradeNo == "" || n.GatewayTradeNo == "" {
		return "", newError(CodeInvalidArgument, msgPayBadNotify)
	}
	if err := s.confirm(ctx, gateway.Code(), n); err != nil {
		return "", err
	}
	return n.Ack, nil
}

// confirm 把一张充值单置为已支付并给余额入账；已支付的直接返回成功（重复通知只入账一次）。
// 过期的充值单晚到的成功通知仍然入账：钱已经付了，不能因为平台侧超时就不认。
func (s *PayService) confirm(ctx context.Context, gatewayCode string, n *pay.Notification) error {
	now := s.now()
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := q.GetPaymentByTradeNoForUpdate(ctx, n.TradeNo)
		if errors.Is(err, pgx.ErrNoRows) {
			return newError(CodeNotFound, msgPayBadNotify)
		}
		if err != nil {
			return fmt.Errorf("get payment %s: %w", n.TradeNo, err)
		}
		if row.Gateway != gatewayCode || row.Amount != n.Amount {
			slog.Warn("payment notification does not match the payment", "trade_no", n.TradeNo, "gateway", gatewayCode, "want_amount", row.Amount, "got_amount", n.Amount)
			return newError(CodeInvalidArgument, msgPayBadNotify)
		}
		if row.Status == PaymentPaid {
			return nil
		}
		err = q.MarkPaymentPaid(ctx, sqlcgen.MarkPaymentPaidParams{
			ID: row.ID, GatewayTradeNo: pgtype.Text{String: n.GatewayTradeNo, Valid: true}, PaidAt: pgtype.Timestamptz{Time: now, Valid: true},
		})
		if err != nil {
			if isUniqueViolation(err) {
				// 同一个网关交易号已经用在另一张充值单上：拒绝，避免一笔钱入账两次
				return newError(CodeInvalidArgument, msgPayBadNotify)
			}
			return fmt.Errorf("mark payment %d paid: %w", row.ID, err)
		}
		if !row.UserID.Valid {
			// 用户在支付途中注销了：充值单照常记为已支付，余额无处可入，留给管理员按流水处理
			slog.Warn("payment paid for a deleted user", "trade_no", n.TradeNo)
		} else if _, err := applyLedger(ctx, q, ledgerChange{UserID: row.UserID.Int64, Amount: row.Amount, Kind: LedgerTopupOnline, Ref: ledgerRef{Type: refPayment, ID: row.ID}}, now); err != nil {
			return err
		}
		s.telegram.Notify(ctx, q, TelegramEventPayment, fmt.Sprintf("在线充值入账：充值单 %s，%s 元（%s）", n.TradeNo, formatCents(row.Amount), gatewayCode))
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorSystem, Action: ActionPaymentPaid, TargetType: targetPayment, TargetID: fmt.Sprint(row.ID),
			Detail: map[string]any{"tradeNo": n.TradeNo, "gateway": gatewayCode, "amount": row.Amount}})
	})
}

// ExpirePayments 把超过 30 分钟仍未支付的充值单置为过期，返回处理的张数（定时任务调用）。
func (s *PayService) ExpirePayments(ctx context.Context) (int64, error) {
	n, err := s.q.ExpirePayments(ctx, pgtype.Timestamptz{Time: s.now(), Valid: true})
	if err != nil {
		return 0, fmt.Errorf("expire payments: %w", err)
	}
	return n, nil
}

// Payment 是充值单视图。UserID 为 0 表示用户已删除。
type Payment struct {
	ID        int64
	TradeNo   string
	UserID    int64
	Username  string
	Gateway   string
	Amount    int64
	Status    string
	PaidAt    *time.Time
	CreatedAt time.Time
}

// PaymentFilter 是充值单列表的筛选；零值表示不限。
type PaymentFilter struct {
	UserID int64
	Status string
}

// ListPayments 返回充值单分页，新的在前（finance:read）。
func (s *PayService) ListPayments(ctx context.Context, f PaymentFilter, pq PageQuery) (*Page[Payment], error) {
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	if f.Status != "" && f.Status != PaymentPending && f.Status != PaymentPaid && f.Status != PaymentExpired {
		return nil, newError(CodeInvalidArgument, msgPayBadStatus)
	}
	rows, err := s.q.ListPayments(ctx, sqlcgen.ListPaymentsParams{UserID: f.UserID, Status: f.Status, RowLimit: limit, RowOffset: offset})
	if err != nil {
		return nil, fmt.Errorf("list payments: %w", err)
	}
	total, err := s.q.CountPayments(ctx, sqlcgen.CountPaymentsParams{UserID: f.UserID, Status: f.Status})
	if err != nil {
		return nil, fmt.Errorf("count payments: %w", err)
	}
	items := make([]Payment, 0, len(rows))
	for _, r := range rows {
		items = append(items, Payment{ID: r.ID, TradeNo: r.TradeNo, UserID: r.UserID.Int64, Username: r.Username, Gateway: r.Gateway, Amount: r.Amount,
			Status: r.Status, PaidAt: timePtr(r.PaidAt), CreatedAt: r.CreatedAt.Time})
	}
	return &Page[Payment]{Items: items, Total: total}, nil
}
