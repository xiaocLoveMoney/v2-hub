package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"v2-hub-server/internal/dao/sqlcgen"
)

// 商店的购买侧（docs/03 §5.13）：询价、下单、订单、钱包总览与自动续费开关。
// 购买一律从余额扣款（Ruling-28）；一笔下单在同一事务内完成，任一步失败整体回滚。

// 订单类型。
const (
	OrderPlan        = "plan"
	OrderTrafficPack = "traffic_pack"
	OrderRenew       = "renew"
)

const (
	orderNoPrefix     = "O"
	orderNoTimeLayout = "20060102150405"
	orderNoRandomMax  = 1000000 // 6 位随机数字
	orderNoRetries    = 3
	constraintOrderNo = "uk_shop_order_no"
	constraintTrial   = "uk_shop_order_trial"
	maxCouponCodeLen  = 32

	ActionPortalOrder     = "portal.order"
	ActionPortalAutoRenew = "portal.auto_renew"
	targetOrder           = "order"

	msgOrderBadKind      = "kind 只能是 plan、traffic_pack"
	msgOrderBadItem      = "请选择要购买的商品"
	msgOrderTooOften     = "操作过于频繁，请稍后再试"
	msgPlanOffSale       = "该套餐已下架或时长选项已不存在"
	msgPackOffSale       = "该流量包已下架"
	msgTrialUsed         = "试用套餐每个账号只能领取一次"
	msgTrialHasPlan      = "当前套餐尚未到期，不能领取试用"
	msgTrialNoCoupon     = "试用套餐不能使用优惠码"
	msgRenewNeverExpires = "当前套餐不会到期，无需续费"
	msgPackNoPlan        = "还没有套餐，不能购买流量包"
	msgPackUnlimited     = "当前套餐不限流量，无需购买流量包"
	msgPackInactive      = "账号当前状态不能购买流量包"
	msgCouponInvalid     = "优惠码无效或不适用于本次购买"
	msgCouponTooOften    = "优惠码尝试次数过多，请稍后再试"
	msgUserDisabledShop  = "账号已停用，不能购买"
	msgAutoRenewNoOption = "还没有买过可续费的套餐"
	msgAutoRenewTrial    = "试用套餐不能自动续费"
	msgAutoRenewOffSale  = "该套餐已下架或时长选项已不存在，无法自动续费"
	msgAutoRenewOther    = "当前套餐不是上次购买的套餐，请重新购买后再开启"
	msgOrderBadTime      = "from 须早于 to"
)

// errCouponInvalid 是优惠码不可用的统一错误（提示不区分原因，docs/03 §5.15 防猜）；按指针比较，用来给防猜计数。
var errCouponInvalid = newError(CodeInvalidArgument, msgCouponInvalid)

// OrderInput 是询价与下单的输入：Kind = plan 时给 PlanPriceID，Kind = traffic_pack 时给 TrafficPackID。
type OrderInput struct {
	Kind          string
	PlanPriceID   int64
	TrafficPackID int64
	CouponCode    string
}

// Quote 是询价结果；金额为整数分。
type Quote struct {
	AmountOriginal int64
	AmountDiscount int64
	AmountPayable  int64
	Balance        int64
	Shortfall      int64 // 还差多少；余额够时为 0
	Renewal        bool  // 买的是当前套餐
	// ExpiresAt 是下单后的到期时间；nil = 不过期或等首次使用后起算。TrafficLimit 是下单后的额度（0 不限）。
	ExpiresAt    *time.Time
	TrafficLimit int64
	CouponCode   string
}

// Order 是订单视图。UserID 为 0 表示用户已删除。
type Order struct {
	ID             int64
	OrderNo        string
	UserID         int64
	Username       string
	Kind           string
	ItemName       string
	DurationDays   int
	TrafficBytes   int64
	Trial          bool
	AmountOriginal int64
	AmountDiscount int64
	AmountPaid     int64
	CouponCode     string
	CreatedAt      time.Time
}

// orderItem 是写进订单的商品快照（docs/03 §5.13）；套餐或流量包之后改名改价，订单不受影响。
type orderItem struct {
	Name         string `json:"name"`
	DurationDays int    `json:"durationDays"`
	TrafficBytes int64  `json:"trafficBytes"`
	UnitPrice    int64  `json:"unitPrice"`
}

// pricedOrder 是一次购买算好的全部结果，询价与下单共用同一段计算。
type pricedOrder struct {
	kind         string
	item         orderItem
	planID       int64
	planPriceID  int64
	packID       int64
	trial        bool
	renewal      bool
	original     int64
	discount     int64
	couponID     int64
	couponCode   string
	balance      int64
	username     string
	planRef      pgtype.Int8 // 下单后用户的 plan_id
	quota        Quota       // 下单后的额度
	statusBefore string
}

func (o *pricedOrder) payable() int64 { return o.original - o.discount }

// newOrderNo 生成订单号：前缀 + UTC 时间到秒 + 6 位随机数字（docs/03 §6）。
func newOrderNo(prefix string, now time.Time) (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(orderNoRandomMax))
	if err != nil {
		return "", fmt.Errorf("generate order no: %w", err)
	}
	return fmt.Sprintf("%s%s%06d", prefix, now.UTC().Format(orderNoTimeLayout), n.Int64()), nil
}

func violatedConstraint(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		return pgErr.ConstraintName
	}
	return ""
}

// packBlocker 返回不能买流量包的原因；可以买时为空串（docs/03 §5.13 可买条件）。
func packBlocker(hasPlan bool, q Quota, status string) string {
	switch {
	case !hasPlan:
		return msgPackNoPlan
	case q.TrafficLimit == 0:
		return msgPackUnlimited
	case status != UserStatusActive && status != UserStatusLimited:
		return msgPackInactive
	default:
		return ""
	}
}

// trialBlocker 返回不能领试用的原因：已领过，或正持有未到期的其它套餐（docs/03 §5.12 试用）。
func trialBlocker(trialUsed, hasOtherPlan bool, status string) string {
	switch {
	case trialUsed:
		return msgTrialUsed
	case hasOtherPlan && status != UserStatusExpired:
		return msgTrialHasPlan
	default:
		return ""
	}
}

// discountOf 计算优惠金额（docs/03 §5.15）：percent 向下取整到分，fixed 不超过原价。
func discountOf(kind string, value, original int64) int64 {
	if kind == CouponPercent {
		return original * value / percentBase
	}
	return min(value, original)
}

// price 锁住用户行，校验可买条件并算价；不写任何数据。优惠码行也在这里加锁，次数判断因此不会被并发下单穿透。
func (s *ShopService) price(ctx context.Context, q *sqlcgen.Queries, userID int64, in OrderInput, now time.Time) (*pricedOrder, error) {
	row, err := lockUser(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	if row.IsDisabled {
		return nil, newError(CodeConflict, msgUserDisabledShop)
	}
	wallet, err := q.LockUserBalance(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("lock balance of user %d: %w", userID, err)
	}
	qt := quotaFromRow(row)
	o := &pricedOrder{kind: in.Kind, balance: wallet.Balance, username: wallet.Username, statusBefore: row.Status, planRef: row.PlanID}
	switch in.Kind {
	case OrderPlan:
		if err := s.pricePlan(ctx, q, userID, row, in, o, &qt, now); err != nil {
			return nil, err
		}
	case OrderTrafficPack:
		if err := pricePack(ctx, q, row, in, o, &qt, now); err != nil {
			return nil, err
		}
	default:
		return nil, newError(CodeInvalidArgument, msgOrderBadKind)
	}
	o.quota = qt
	if err := s.applyCoupon(ctx, q, userID, in.CouponCode, o, now); err != nil {
		return nil, err
	}
	return o, nil
}

func (s *ShopService) pricePlan(ctx context.Context, q *sqlcgen.Queries, userID int64, row sqlcgen.GetUserQuotaRow, in OrderInput, o *pricedOrder, qt *Quota, now time.Time) error {
	if in.PlanPriceID < 1 || in.TrafficPackID != 0 {
		return newError(CodeInvalidArgument, msgOrderBadItem)
	}
	pp, err := q.GetPlanPriceForSale(ctx, in.PlanPriceID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !pp.IsOnSale) {
		return newError(CodeConflict, msgPlanOffSale)
	}
	if err != nil {
		return fmt.Errorf("get plan price %d: %w", in.PlanPriceID, err)
	}
	o.renewal = row.PlanID.Valid && row.PlanID.Int64 == pp.PlanID
	if pp.IsTrial {
		used, err := q.HasTrialOrder(ctx, pgtype.Int8{Int64: userID, Valid: true})
		if err != nil {
			return fmt.Errorf("check trial of user %d: %w", userID, err)
		}
		if reason := trialBlocker(used, row.PlanID.Valid && !o.renewal, qt.Status(now)); reason != "" {
			return newError(CodeConflict, reason)
		}
	}
	if o.renewal && qt.ExpiresAt == nil && qt.PendingDays == 0 {
		return newError(CodeConflict, msgRenewNeverExpires)
	}
	if !o.renewal {
		// 换购：立即生效，原套餐剩余时长作废、不折算（Ruling-30）
		qt.ExpiresAt, qt.PendingDays = nil, 0
	}
	qt.ApplyPlan(PlanQuota{
		TrafficLimit: pp.TrafficLimit, DurationDays: int(pp.DurationDays), StartOnFirstUse: pp.StartOnFirstUse, LimitIP: int(pp.LimitIp),
		TrafficReset: pp.TrafficReset, TrafficResetDay: int(pp.TrafficResetDay),
	}, now)
	o.planID, o.planPriceID, o.trial, o.original = pp.PlanID, pp.ID, pp.IsTrial, pp.Price
	o.planRef = pgtype.Int8{Int64: pp.PlanID, Valid: true}
	o.item = orderItem{Name: pp.Name, DurationDays: int(pp.DurationDays), TrafficBytes: pp.TrafficLimit, UnitPrice: pp.Price}
	return nil
}

func pricePack(ctx context.Context, q *sqlcgen.Queries, row sqlcgen.GetUserQuotaRow, in OrderInput, o *pricedOrder, qt *Quota, now time.Time) error {
	if in.TrafficPackID < 1 || in.PlanPriceID != 0 {
		return newError(CodeInvalidArgument, msgOrderBadItem)
	}
	pack, err := q.GetTrafficPack(ctx, in.TrafficPackID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !pack.IsOnSale) {
		return newError(CodeConflict, msgPackOffSale)
	}
	if err != nil {
		return fmt.Errorf("get traffic pack %d: %w", in.TrafficPackID, err)
	}
	if reason := packBlocker(row.PlanID.Valid, *qt, qt.Status(now)); reason != "" {
		return newError(CodeConflict, reason)
	}
	// 只加额度不改到期；和签到奖励一样，续费 / 换购 / 管理员改额度时不保留（Ruling-26）
	qt.TrafficLimit += pack.TrafficBytes
	o.packID, o.original = pack.ID, pack.Price
	o.item = orderItem{Name: pack.Name, TrafficBytes: pack.TrafficBytes, UnitPrice: pack.Price}
	return nil
}

// applyCoupon 校验并套用优惠码；任何不可用的情况都返回 errCouponInvalid，不区分原因。
func (s *ShopService) applyCoupon(ctx context.Context, q *sqlcgen.Queries, userID int64, raw string, o *pricedOrder, now time.Time) error {
	code := strings.ToUpper(strings.TrimSpace(raw))
	if code == "" {
		return nil
	}
	if o.trial {
		return newError(CodeInvalidArgument, msgTrialNoCoupon)
	}
	if len(code) > maxCouponCodeLen || !couponCodePattern.MatchString(code) {
		return errCouponInvalid
	}
	c, err := q.GetCouponByCodeForUpdate(ctx, code)
	if errors.Is(err, pgx.ErrNoRows) {
		return errCouponInvalid
	}
	if err != nil {
		return fmt.Errorf("get coupon: %w", err)
	}
	usable := !c.IsDisabled &&
		(!c.StartsAt.Valid || !now.Before(c.StartsAt.Time)) &&
		(!c.EndsAt.Valid || now.Before(c.EndsAt.Time)) &&
		o.original >= c.MinAmount &&
		(c.MaxUses == 0 || c.UsedCount < c.MaxUses)
	if !usable {
		return errCouponInvalid
	}
	inScope := c.ForTrafficPack
	if o.kind == OrderPlan {
		inScope = c.AllPlans
		if !inScope {
			if inScope, err = q.CouponCoversPlan(ctx, sqlcgen.CouponCoversPlanParams{CouponID: c.ID, PlanID: o.planID}); err != nil {
				return fmt.Errorf("check coupon %d scope: %w", c.ID, err)
			}
		}
	}
	if !inScope {
		return errCouponInvalid
	}
	if c.MaxUsesPerUser > 0 {
		used, err := q.CountCouponUsesByUser(ctx, sqlcgen.CountCouponUsesByUserParams{CouponID: c.ID, UserID: pgtype.Int8{Int64: userID, Valid: true}})
		if err != nil {
			return fmt.Errorf("count coupon %d uses: %w", c.ID, err)
		}
		if used >= int64(c.MaxUsesPerUser) {
			return errCouponInvalid
		}
	}
	o.couponID, o.couponCode, o.discount = c.ID, c.Code, discountOf(c.Kind, c.Value, o.original)
	return nil
}

// guardCoupon 在带优惠码的请求前检查防猜计数；返回的 note 用来在优惠码无效时计一次。
func (s *ShopService) guardCoupon(userID int64, code string) (note func(err error), err error) {
	key := fmt.Sprint(guardKeyCoupon, userID)
	if strings.TrimSpace(code) != "" && s.couponFails.IsLocked(key) {
		return nil, newError(CodeRateLimited, msgCouponTooOften)
	}
	return func(e error) {
		var se *Error
		if errors.As(e, &se) && se == errCouponInvalid {
			s.couponFails.RecordFailure(key)
		}
	}, nil
}

// takeRate 给一次请求计数；窗口内超过上限返回 CodeRateLimited。
func takeRate(g interface {
	IsLocked(string) bool
	RecordFailure(string)
}, key string) error {
	if g.IsLocked(key) {
		return newError(CodeRateLimited, msgOrderTooOften)
	}
	g.RecordFailure(key)
	return nil
}

// QuoteOrder 询价：不落库、不占优惠码次数。错误：CodeFeatureDisabled、CodeInvalidArgument（含优惠码无效）、
// CodeConflict（商品当前不可买，message 说明原因）、CodeRateLimited。
func (s *ShopService) QuoteOrder(ctx context.Context, p *UserPrincipal, in OrderInput) (*Quote, error) {
	if _, err := requireShop(ctx, s.settings); err != nil {
		return nil, err
	}
	if err := takeRate(s.quoteRate, fmt.Sprint(guardKeyQuote, p.UserID)); err != nil {
		return nil, err
	}
	note, err := s.guardCoupon(p.UserID, in.CouponCode)
	if err != nil {
		return nil, err
	}
	var o *pricedOrder
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		o, err = s.price(ctx, q, p.UserID, in, s.now())
		return err
	})
	if err != nil {
		note(err)
		return nil, err
	}
	return &Quote{
		AmountOriginal: o.original, AmountDiscount: o.discount, AmountPayable: o.payable(), Balance: o.balance,
		Shortfall: max(o.payable()-o.balance, 0), Renewal: o.renewal, ExpiresAt: o.quota.ExpiresAt, TrafficLimit: o.quota.TrafficLimit, CouponCode: o.couponCode,
	}, nil
}

// CreateOrder 下单并从余额扣款，成功即生效（docs/03 §5.13 原子性）。
// 错误：CodeFeatureDisabled、CodeInvalidArgument、CodeConflict（商品不可买）、CodeInsufficientBalance（Data 带差额）、CodeRateLimited。
func (s *ShopService) CreateOrder(ctx context.Context, p *UserPrincipal, in OrderInput, ip *netip.Addr) (*Order, error) {
	if _, err := requireShop(ctx, s.settings); err != nil {
		return nil, err
	}
	if err := takeRate(s.orderRate, fmt.Sprint(guardKeyOrder, p.UserID)); err != nil {
		return nil, err
	}
	note, err := s.guardCoupon(p.UserID, in.CouponCode)
	if err != nil {
		return nil, err
	}
	now := s.now()
	var order *Order
	for range orderNoRetries {
		orderNo, err := newOrderNo(orderNoPrefix, now)
		if err != nil {
			return nil, err
		}
		err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
			var err error
			order, err = s.placeOrder(ctx, q, p, in, orderNo, ip, now)
			return err
		})
		if violatedConstraint(err) == constraintOrderNo {
			continue // 同一秒内随机数撞号，换一个号重来
		}
		if err != nil {
			note(err)
			return nil, err
		}
		s.users.TriggerReconcile()
		return order, nil
	}
	return nil, fmt.Errorf("create order for user %d: order no kept colliding", p.UserID)
}

// placeOrder 在事务里完成一笔下单：算价 → 写订单 → 扣余额记流水 → 应用套餐或流量包 → 记优惠码使用 → 审计。
func (s *ShopService) placeOrder(ctx context.Context, q *sqlcgen.Queries, p *UserPrincipal, in OrderInput, orderNo string, ip *netip.Addr, now time.Time) (*Order, error) {
	o, err := s.price(ctx, q, p.UserID, in, now)
	if err != nil {
		return nil, err
	}
	if short := o.payable() - o.balance; short > 0 {
		return nil, &Error{Code: CodeInsufficientBalance, Message: "余额不足", Data: map[string]int64{"shortfall": short}}
	}
	item, err := json.Marshal(o.item)
	if err != nil {
		return nil, fmt.Errorf("encode order item: %w", err)
	}
	at := pgtype.Timestamptz{Time: now, Valid: true}
	user := pgtype.Int8{Int64: p.UserID, Valid: true}
	orderID, err := q.InsertOrder(ctx, sqlcgen.InsertOrderParams{
		OrderNo: orderNo, UserID: user, Username: o.username, Kind: o.kind,
		PlanID: pgtype.Int8{Int64: o.planID, Valid: o.planID != 0}, TrafficPackID: pgtype.Int8{Int64: o.packID, Valid: o.packID != 0},
		Item: item, IsTrial: o.trial, AmountOriginal: o.original, AmountDiscount: o.discount, AmountPaid: o.payable(),
		CouponID: pgtype.Int8{Int64: o.couponID, Valid: o.couponID != 0}, CouponCode: o.couponCode, CreatedAt: at,
	})
	if err != nil {
		if violatedConstraint(err) == constraintTrial {
			return nil, newError(CodeConflict, msgTrialUsed)
		}
		return nil, fmt.Errorf("insert order: %w", err)
	}
	if o.payable() > 0 {
		if _, err := applyLedger(ctx, q, ledgerChange{UserID: p.UserID, Amount: -o.payable(), Kind: LedgerPurchase, Ref: ledgerRef{Type: refOrder, ID: orderID}, Remark: o.item.Name}, now); err != nil {
			return nil, err
		}
	}
	if err := s.applyPurchase(ctx, q, p.UserID, o, now); err != nil {
		return nil, err
	}
	if o.couponID != 0 {
		if err := q.InsertCouponUsage(ctx, sqlcgen.InsertCouponUsageParams{CouponID: o.couponID, UserID: user, OrderID: orderID, CreatedAt: at}); err != nil {
			return nil, fmt.Errorf("insert coupon usage: %w", err)
		}
		if err := q.BumpCouponUse(ctx, o.couponID); err != nil {
			return nil, fmt.Errorf("bump coupon %d: %w", o.couponID, err)
		}
	}
	err = writeAudit(ctx, q, AuditEntry{ActorType: ActorUser, ActorID: p.UserID, Action: ActionPortalOrder, TargetType: targetOrder, TargetID: fmt.Sprint(orderID),
		ClientIP: ip, Detail: map[string]any{"orderNo": orderNo, "kind": o.kind, "item": o.item.Name, "paid": o.payable()}})
	if err != nil {
		return nil, err
	}
	return &Order{
		ID: orderID, OrderNo: orderNo, UserID: p.UserID, Username: o.username, Kind: o.kind, ItemName: o.item.Name, DurationDays: o.item.DurationDays,
		TrafficBytes: o.item.TrafficBytes, Trial: o.trial, AmountOriginal: o.original, AmountDiscount: o.discount, AmountPaid: o.payable(),
		CouponCode: o.couponCode, CreatedAt: now,
	}, nil
}

// applyPurchase 把算好的额度写到用户上（U11 / U12），并把需要改面板的下发标为待同步。
func (s *ShopService) applyPurchase(ctx context.Context, q *sqlcgen.Queries, userID int64, o *pricedOrder, now time.Time) error {
	status := o.quota.Status(now)
	if err := saveQuota(ctx, q, userID, o.planRef, o.quota, status); err != nil {
		return err
	}
	needsSync := status != o.statusBefore
	if o.kind == OrderPlan {
		state, err := q.GetUserShopState(ctx, userID)
		if err != nil {
			return fmt.Errorf("get shop state of user %d: %w", userID, err)
		}
		// 自动续费的意愿跟着用户走，续费对象换成这次买的时长选项；试用套餐不能自动续费
		autoRenew := state.AutoRenew && !o.trial
		err = q.SetUserPurchase(ctx, sqlcgen.SetUserPurchaseParams{ID: userID, PlanPriceID: pgtype.Int8{Int64: o.planPriceID, Valid: true}, AutoRenew: autoRenew})
		if err != nil {
			return fmt.Errorf("record purchase of user %d: %w", userID, err)
		}
		needsSync = true // 套餐可能变了，节点授权要重算
	} else if err := q.ClearUserTrafficNotified(ctx, userID); err != nil {
		return fmt.Errorf("clear traffic notice of user %d: %w", userID, err)
	}
	if needsSync {
		if err := q.MarkUserProvisionsPending(ctx, userID); err != nil {
			return fmt.Errorf("mark provisions of user %d pending: %w", userID, err)
		}
	}
	return nil
}

// ---- 订单列表 -------------------------------------------------------------------

// OrderFilter 是管理端订单列表的筛选；零值表示不限。
type OrderFilter struct {
	UserID int64
	Kind   string
	From   *time.Time // 创建时间下限（含）
	To     *time.Time // 创建时间上限（不含）
}

func (s *ShopService) listOrders(ctx context.Context, f OrderFilter, pq PageQuery) (*Page[Order], error) {
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	if f.Kind != "" && f.Kind != OrderPlan && f.Kind != OrderTrafficPack && f.Kind != OrderRenew {
		return nil, newError(CodeInvalidArgument, "kind 只能是 plan、traffic_pack、renew")
	}
	if f.From != nil && f.To != nil && !f.From.Before(*f.To) {
		return nil, newError(CodeInvalidArgument, msgOrderBadTime)
	}
	from, to := optionalTime(f.From), optionalTime(f.To)
	rows, err := s.q.ListOrders(ctx, sqlcgen.ListOrdersParams{UserID: f.UserID, Kind: f.Kind, FromTime: from, ToTime: to, RowLimit: limit, RowOffset: offset})
	if err != nil {
		return nil, fmt.Errorf("list orders: %w", err)
	}
	total, err := s.q.CountOrders(ctx, sqlcgen.CountOrdersParams{UserID: f.UserID, Kind: f.Kind, FromTime: from, ToTime: to})
	if err != nil {
		return nil, fmt.Errorf("count orders: %w", err)
	}
	items := make([]Order, 0, len(rows))
	for _, r := range rows {
		var item orderItem
		if err := json.Unmarshal(r.Item, &item); err != nil {
			return nil, fmt.Errorf("decode item of order %d: %w", r.ID, err)
		}
		items = append(items, Order{
			ID: r.ID, OrderNo: r.OrderNo, UserID: r.UserID.Int64, Username: r.Username, Kind: r.Kind, ItemName: item.Name, DurationDays: item.DurationDays,
			TrafficBytes: item.TrafficBytes, Trial: r.IsTrial, AmountOriginal: r.AmountOriginal, AmountDiscount: r.AmountDiscount, AmountPaid: r.AmountPaid,
			CouponCode: r.CouponCode, CreatedAt: r.CreatedAt.Time,
		})
	}
	return &Page[Order]{Items: items, Total: total}, nil
}

// ListOrders 返回全部订单分页（管理端，finance:read）。
func (s *ShopService) ListOrders(ctx context.Context, f OrderFilter, pq PageQuery) (*Page[Order], error) {
	return s.listOrders(ctx, f, pq)
}

// ListOwnOrders 返回本人的订单分页；身份只取自会话（R8）。
func (s *ShopService) ListOwnOrders(ctx context.Context, p *UserPrincipal, pq PageQuery) (*Page[Order], error) {
	return s.listOrders(ctx, OrderFilter{UserID: p.UserID}, pq)
}

// ---- 商店与钱包 -----------------------------------------------------------------

// ShopPlan 是商店里的一个套餐：CanBuy 为假时 Reason 说明原因。
type ShopPlan struct {
	ID           int64
	Name         string
	Description  string
	TrafficLimit int64
	TrafficReset string
	LimitIP      int
	NodeCount    int
	Trial        bool
	Current      bool
	CanBuy       bool
	Reason       string
	Prices       []PlanPrice
}

// Shop 是商店页的全部数据。
type Shop struct {
	Balance           int64
	Plans             []ShopPlan
	TrafficPacks      []TrafficPack
	CanBuyTrafficPack bool
	TrafficPackReason string
}

func stateQuota(st sqlcgen.GetUserShopStateRow) Quota {
	return Quota{TrafficLimit: st.TrafficLimit, TrafficUsed: st.TrafficUsed, ExpiresAt: timePtr(st.ExpiresAt), PendingDays: int(st.PendingDays), Disabled: st.IsDisabled}
}

// GetShop 返回上架的套餐与流量包，以及本人对每个商品能不能买（docs/03 §5.12）。商店未开启返回 CodeFeatureDisabled。
func (s *ShopService) GetShop(ctx context.Context, p *UserPrincipal) (*Shop, error) {
	if _, err := requireShop(ctx, s.settings); err != nil {
		return nil, err
	}
	st, err := s.q.GetUserShopState(ctx, p.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, newError(CodeNotFound, msgUserNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get shop state of user %d: %w", p.UserID, err)
	}
	plans, err := s.q.ListShopPlans(ctx)
	if err != nil {
		return nil, fmt.Errorf("list shop plans: %w", err)
	}
	prices, err := s.q.ListPlanPricesOnSale(ctx)
	if err != nil {
		return nil, fmt.Errorf("list plan prices on sale: %w", err)
	}
	byPlan := make(map[int64][]PlanPrice, len(plans))
	for _, pr := range prices {
		byPlan[pr.PlanID] = append(byPlan[pr.PlanID], PlanPrice{ID: pr.ID, PlanPriceInput: PlanPriceInput{DurationDays: int(pr.DurationDays), Price: pr.Price}})
	}
	qt := stateQuota(st)
	status := qt.Status(s.now())
	out := &Shop{Balance: st.Balance, Plans: make([]ShopPlan, 0, len(plans))}
	for _, r := range plans {
		current := st.PlanID.Valid && st.PlanID.Int64 == r.ID
		reason := ""
		switch {
		case r.IsTrial:
			reason = trialBlocker(st.IsTrialUsed, st.PlanID.Valid && !current, status)
		case current && qt.ExpiresAt == nil && qt.PendingDays == 0:
			reason = msgRenewNeverExpires
		}
		out.Plans = append(out.Plans, ShopPlan{
			ID: r.ID, Name: r.Name, Description: r.Description, TrafficLimit: r.TrafficLimit, TrafficReset: r.TrafficReset, LimitIP: int(r.LimitIp),
			NodeCount: int(r.NodeCount), Trial: r.IsTrial, Current: current, CanBuy: reason == "", Reason: reason, Prices: byPlan[r.ID],
		})
	}
	if out.TrafficPacks, err = listTrafficPacks(ctx, s.q, true); err != nil {
		return nil, err
	}
	out.TrafficPackReason = packBlocker(st.PlanID.Valid, qt, status)
	out.CanBuyTrafficPack = out.TrafficPackReason == ""
	return out, nil
}

// AutoRenewInfo 是自动续费的状态：Available 为假时 Reason 说明为什么不能开。Price 为 nil 表示时长选项已不存在。
type AutoRenewInfo struct {
	Enabled      bool
	Available    bool
	Reason       string
	PlanName     string
	DurationDays int
	Price        *int64
}

// Wallet 是钱包总览。
type Wallet struct {
	Balance     int64
	AutoRenew   AutoRenewInfo
	RebateTotal int64
}

// autoRenewInfo 判断用户现在能不能开自动续费（docs/03 §5.16）：买过某个时长选项、它还在售、不是试用、且仍是当前套餐。
func autoRenewInfo(ctx context.Context, q *sqlcgen.Queries, st sqlcgen.GetUserShopStateRow) (AutoRenewInfo, error) {
	info := AutoRenewInfo{Enabled: st.AutoRenew}
	if !st.PlanPriceID.Valid {
		info.Reason = msgAutoRenewNoOption
		return info, nil
	}
	pp, err := q.GetPlanPriceForSale(ctx, st.PlanPriceID.Int64)
	if errors.Is(err, pgx.ErrNoRows) {
		info.Reason = msgAutoRenewOffSale
		return info, nil
	}
	if err != nil {
		return info, fmt.Errorf("get plan price %d: %w", st.PlanPriceID.Int64, err)
	}
	price := pp.Price
	info.PlanName, info.DurationDays, info.Price = pp.Name, int(pp.DurationDays), &price
	switch {
	case pp.IsTrial:
		info.Reason = msgAutoRenewTrial
	case !pp.IsOnSale:
		info.Reason = msgAutoRenewOffSale
	case !st.PlanID.Valid || st.PlanID.Int64 != pp.PlanID:
		info.Reason = msgAutoRenewOther
	default:
		info.Available = true
	}
	return info, nil
}

// GetWallet 返回本人的余额、自动续费状态与累计返利；商店关闭时仍可查看（docs/03 §5.6 shop.enabled）。
func (s *ShopService) GetWallet(ctx context.Context, p *UserPrincipal) (*Wallet, error) {
	return getWallet(ctx, s.q, p.UserID)
}

func getWallet(ctx context.Context, q *sqlcgen.Queries, userID int64) (*Wallet, error) {
	st, err := q.GetUserShopState(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, newError(CodeNotFound, msgUserNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get shop state of user %d: %w", userID, err)
	}
	info, err := autoRenewInfo(ctx, q, st)
	if err != nil {
		return nil, err
	}
	rebate, err := q.SumRebateByInviter(ctx, pgtype.Int8{Int64: userID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("sum rebate of user %d: %w", userID, err)
	}
	return &Wallet{Balance: st.Balance, AutoRenew: info, RebateTotal: rebate}, nil
}

// SetAutoRenew 开 / 关到期自动续费。开启须满足 autoRenewInfo 的条件，否则 CodeConflict；关闭总是允许。
func (s *ShopService) SetAutoRenew(ctx context.Context, p *UserPrincipal, enabled bool, ip *netip.Addr) (*Wallet, error) {
	if _, err := requireShop(ctx, s.settings); err != nil {
		return nil, err
	}
	var out *Wallet
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := lockUser(ctx, q, p.UserID); err != nil {
			return err
		}
		w, err := getWallet(ctx, q, p.UserID)
		if err != nil {
			return err
		}
		if enabled && !w.AutoRenew.Available {
			return newError(CodeConflict, w.AutoRenew.Reason)
		}
		if err := q.SetUserAutoRenew(ctx, sqlcgen.SetUserAutoRenewParams{ID: p.UserID, AutoRenew: enabled}); err != nil {
			return fmt.Errorf("set auto renew of user %d: %w", p.UserID, err)
		}
		w.AutoRenew.Enabled = enabled
		out = w
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorUser, ActorID: p.UserID, Action: ActionPortalAutoRenew, TargetType: targetUser,
			TargetID: fmt.Sprint(p.UserID), ClientIP: ip, Detail: map[string]any{"enabled": enabled}})
	})
	return out, err
}
