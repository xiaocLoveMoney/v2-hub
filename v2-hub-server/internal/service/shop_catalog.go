package service

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/ratelimit"
)

// 商店的商品侧（docs/03 §5.12、§5.15）：流量包与优惠码的管理。套餐的时长选项与上架随套餐接口（user.go）。

// 优惠码类型。
const (
	CouponPercent = "percent"
	CouponFixed   = "fixed"
)

const (
	maxTrafficPackNameLen = 64
	maxCouponRemarkLen    = 255
	maxCouponUses         = 1000000
	maxCouponUsesPerUser  = 1000
	maxCouponPlans        = 100
	maxCouponPercent      = 100

	// 下单与询价的频控（docs/03 §5.13）：每个用户每分钟下单 10 次、询价 60 次
	ordersPerMinute = 10
	quotesPerMinute = 60
	shopRateWindow  = time.Minute
	// 优惠码防猜（docs/03 §5.15）：每个用户每小时无效 20 次后拒绝
	couponFailsPerHour = 20
	couponGuardWindow  = time.Hour
	shopGuardMaxKeys   = 10000
	guardKeyOrder      = "order:"
	guardKeyQuote      = "quote:"
	guardKeyCoupon     = "coupon:"

	ActionTrafficPackCreate = "traffic_pack.create"
	ActionTrafficPackUpdate = "traffic_pack.update"
	ActionTrafficPackDelete = "traffic_pack.delete"
	ActionCouponCreate      = "coupon.create"
	ActionCouponUpdate      = "coupon.update"
	ActionCouponDelete      = "coupon.delete"
	targetTrafficPack       = "traffic_pack"
	targetCoupon            = "coupon"

	msgTrafficPackNotFound = "流量包不存在"
	msgTrafficPackBadName  = "名称须为 1–64 个字符"
	msgTrafficPackBadBytes = "流量须大于 0"
	msgTrafficPackNameUsed = "流量包名称已存在"
	msgCouponNotFound      = "优惠码不存在"
	msgCouponBadCode       = "优惠码须为 4–32 位字母或数字"
	msgCouponBadKind       = "类型只能是 percent、fixed"
	msgCouponBadValue      = "面额超出范围：percent 取 1–100，fixed 取 1–99999999 分"
	msgCouponBadLimits     = "次数上限超出范围"
	msgCouponBadWindow     = "失效时间须晚于生效时间"
	msgCouponBadPlans      = "包含不存在的套餐"
	msgCouponCodeUsed      = "优惠码已存在"
	msgCouponInUse         = "优惠码已被使用过，只能停用，不能删除"
)

var couponCodePattern = regexp.MustCompile(`^[A-Z0-9]{4,32}$`)

// ShopService 管流量包、优惠码、询价、下单与订单。并发安全。
type ShopService struct {
	store
	settings    *SettingsService
	users       *UserService
	mail        *MailService
	now         func() time.Time
	orderRate   *ratelimit.FailureGuard
	quoteRate   *ratelimit.FailureGuard
	couponFails *ratelimit.FailureGuard
}

// NewShopService 创建商店服务；users 用来在下单后触发对账，mails 用来发续费与提醒邮件（为 nil 或未配置 SMTP 时不发）。now 为 nil 时用 time.Now。
func NewShopService(pool *pgxpool.Pool, settings *SettingsService, users *UserService, mails *MailService, now func() time.Time) *ShopService {
	if now == nil {
		now = time.Now
	}
	mk := func(limit int, window time.Duration) *ratelimit.FailureGuard {
		return ratelimit.NewFailureGuard(ratelimit.FailureGuardConfig{MaxFailures: limit, Window: window, Lockout: window, MaxKeys: shopGuardMaxKeys}, now)
	}
	return &ShopService{
		store: newStore(pool), settings: settings, users: users, mail: mails, now: now,
		orderRate: mk(ordersPerMinute, shopRateWindow), quoteRate: mk(quotesPerMinute, shopRateWindow), couponFails: mk(couponFailsPerHour, couponGuardWindow),
	}
}

// ---- 流量包 ---------------------------------------------------------------------

// TrafficPackInput 是新建 / 修改流量包的输入；Price 为整数分。
type TrafficPackInput struct {
	Name         string
	TrafficBytes int64
	Price        int64
	OnSale       bool
	SortOrder    int
}

// TrafficPack 是流量包视图。
type TrafficPack struct {
	ID int64
	TrafficPackInput
	CreatedAt time.Time
}

func validateTrafficPack(in TrafficPackInput) (TrafficPackInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > maxTrafficPackNameLen {
		return in, newError(CodeInvalidArgument, msgTrafficPackBadName)
	}
	if in.TrafficBytes < 1 {
		return in, newError(CodeInvalidArgument, msgTrafficPackBadBytes)
	}
	if in.Price < 0 || in.Price > maxMoney {
		return in, newError(CodeInvalidArgument, msgBadMoney)
	}
	if in.SortOrder < 0 || in.SortOrder > maxSortOrder {
		return in, newError(CodeInvalidArgument, msgBadSortOrder)
	}
	return in, nil
}

// ListTrafficPacks 返回全部流量包（含未上架），按排序值升序。
func (s *ShopService) ListTrafficPacks(ctx context.Context) ([]TrafficPack, error) {
	return listTrafficPacks(ctx, s.q, false)
}

func listTrafficPacks(ctx context.Context, q *sqlcgen.Queries, onlyOnSale bool) ([]TrafficPack, error) {
	rows, err := q.ListTrafficPacks(ctx, onlyOnSale)
	if err != nil {
		return nil, fmt.Errorf("list traffic packs: %w", err)
	}
	out := make([]TrafficPack, 0, len(rows))
	for _, r := range rows {
		out = append(out, TrafficPack{ID: r.ID, CreatedAt: r.CreatedAt.Time, TrafficPackInput: TrafficPackInput{
			Name: r.Name, TrafficBytes: r.TrafficBytes, Price: r.Price, OnSale: r.IsOnSale, SortOrder: int(r.SortOrder)}})
	}
	return out, nil
}

// CreateTrafficPack 新建流量包；名称重复返回 CodeConflict。
func (s *ShopService) CreateTrafficPack(ctx context.Context, actor *AdminPrincipal, in TrafficPackInput, ip *netip.Addr) (*TrafficPack, error) {
	v, err := validateTrafficPack(in)
	if err != nil {
		return nil, err
	}
	var id int64
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		id, err = q.CreateTrafficPack(ctx, sqlcgen.CreateTrafficPackParams{
			Name: v.Name, TrafficBytes: v.TrafficBytes, Price: v.Price, IsOnSale: v.OnSale, SortOrder: int32(v.SortOrder)})
		if err != nil {
			if isUniqueViolation(err) {
				return newError(CodeConflict, msgTrafficPackNameUsed)
			}
			return fmt.Errorf("create traffic pack: %w", err)
		}
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionTrafficPackCreate, TargetType: targetTrafficPack,
			TargetID: fmt.Sprint(id), ClientIP: ip, Detail: map[string]any{"name": v.Name, "price": v.Price}})
	})
	if err != nil {
		return nil, err
	}
	return &TrafficPack{ID: id, TrafficPackInput: v, CreatedAt: s.now()}, nil
}

// UpdateTrafficPack 修改流量包；已有订单保留下单时的快照，不受影响。
func (s *ShopService) UpdateTrafficPack(ctx context.Context, actor *AdminPrincipal, id int64, in TrafficPackInput, ip *netip.Addr) (*TrafficPack, error) {
	v, err := validateTrafficPack(in)
	if err != nil {
		return nil, err
	}
	var createdAt time.Time
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.UpdateTrafficPack(ctx, sqlcgen.UpdateTrafficPackParams{
			ID: id, Name: v.Name, TrafficBytes: v.TrafficBytes, Price: v.Price, IsOnSale: v.OnSale, SortOrder: int32(v.SortOrder)})
		if err != nil {
			if isUniqueViolation(err) {
				return newError(CodeConflict, msgTrafficPackNameUsed)
			}
			return fmt.Errorf("update traffic pack %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgTrafficPackNotFound)
		}
		row, err := q.GetTrafficPack(ctx, id)
		if err != nil {
			return fmt.Errorf("get traffic pack %d: %w", id, err)
		}
		createdAt = row.CreatedAt.Time
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionTrafficPackUpdate, TargetType: targetTrafficPack,
			TargetID: fmt.Sprint(id), ClientIP: ip, Detail: map[string]any{"name": v.Name, "price": v.Price, "isOnSale": v.OnSale}})
	})
	if err != nil {
		return nil, err
	}
	return &TrafficPack{ID: id, TrafficPackInput: v, CreatedAt: createdAt}, nil
}

// DeleteTrafficPack 删除流量包。
func (s *ShopService) DeleteTrafficPack(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) error {
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.DeleteTrafficPack(ctx, id)
		if err != nil {
			return fmt.Errorf("delete traffic pack %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgTrafficPackNotFound)
		}
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionTrafficPackDelete, TargetType: targetTrafficPack,
			TargetID: fmt.Sprint(id), ClientIP: ip})
	})
}

// ---- 优惠码 ---------------------------------------------------------------------

// CouponInput 是新建 / 修改优惠码的输入。Value：percent 为 1–100，fixed 为分。
type CouponInput struct {
	Code           string
	Kind           string
	Value          int64
	AllPlans       bool
	PlanIDs        []int64
	ForTrafficPack bool
	MinAmount      int64
	MaxUses        int
	MaxUsesPerUser int
	StartsAt       *time.Time // nil = 立即生效
	EndsAt         *time.Time // nil = 不过期
	Disabled       bool
	Remark         string
}

// Coupon 是优惠码视图。
type Coupon struct {
	ID int64
	CouponInput
	UsedCount int
	CreatedAt time.Time
}

func (s *ShopService) validateCoupon(ctx context.Context, q *sqlcgen.Queries, in CouponInput) (CouponInput, error) {
	in.Code = strings.ToUpper(strings.TrimSpace(in.Code))
	in.Remark = strings.TrimSpace(in.Remark)
	if !couponCodePattern.MatchString(in.Code) {
		return in, newError(CodeInvalidArgument, msgCouponBadCode)
	}
	if in.Kind != CouponPercent && in.Kind != CouponFixed {
		return in, newError(CodeInvalidArgument, msgCouponBadKind)
	}
	if in.Value < 1 || in.Value > maxMoney || (in.Kind == CouponPercent && in.Value > maxCouponPercent) {
		return in, newError(CodeInvalidArgument, msgCouponBadValue)
	}
	if in.MinAmount < 0 || in.MinAmount > maxMoney {
		return in, newError(CodeInvalidArgument, msgBadMoney)
	}
	if in.MaxUses < 0 || in.MaxUses > maxCouponUses || in.MaxUsesPerUser < 0 || in.MaxUsesPerUser > maxCouponUsesPerUser {
		return in, newError(CodeInvalidArgument, msgCouponBadLimits)
	}
	if in.StartsAt != nil && in.EndsAt != nil && !in.EndsAt.After(*in.StartsAt) {
		return in, newError(CodeInvalidArgument, msgCouponBadWindow)
	}
	if utf8.RuneCountInString(in.Remark) > maxCouponRemarkLen {
		return in, newError(CodeInvalidArgument, "备注过长")
	}
	in.PlanIDs = dedupeIDs(in.PlanIDs)
	if in.AllPlans {
		in.PlanIDs = []int64{}
	}
	if len(in.PlanIDs) > maxCouponPlans {
		return in, newError(CodeInvalidArgument, "指定的套餐过多")
	}
	n, err := q.CountPlansByIDs(ctx, in.PlanIDs)
	if err != nil {
		return in, fmt.Errorf("count plans: %w", err)
	}
	if int(n) != len(in.PlanIDs) {
		return in, newError(CodeInvalidArgument, msgCouponBadPlans)
	}
	return in, nil
}

func optionalTime(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

func couponFromRow(r sqlcgen.ListCouponsRow) Coupon {
	return Coupon{ID: r.ID, UsedCount: int(r.UsedCount), CreatedAt: r.CreatedAt.Time, CouponInput: CouponInput{
		Code: r.Code, Kind: r.Kind, Value: r.Value, AllPlans: r.AllPlans, PlanIDs: r.PlanIds, ForTrafficPack: r.ForTrafficPack, MinAmount: r.MinAmount,
		MaxUses: int(r.MaxUses), MaxUsesPerUser: int(r.MaxUsesPerUser), StartsAt: timePtr(r.StartsAt), EndsAt: timePtr(r.EndsAt),
		Disabled: r.IsDisabled, Remark: r.Remark,
	}}
}

// ListCoupons 返回优惠码分页，新的在前。
func (s *ShopService) ListCoupons(ctx context.Context, pq PageQuery) (*Page[Coupon], error) {
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListCoupons(ctx, sqlcgen.ListCouponsParams{RowLimit: limit, RowOffset: offset})
	if err != nil {
		return nil, fmt.Errorf("list coupons: %w", err)
	}
	total, err := s.q.CountCoupons(ctx)
	if err != nil {
		return nil, fmt.Errorf("count coupons: %w", err)
	}
	items := make([]Coupon, 0, len(rows))
	for _, r := range rows {
		items = append(items, couponFromRow(r))
	}
	return &Page[Coupon]{Items: items, Total: total}, nil
}

func getCoupon(ctx context.Context, q *sqlcgen.Queries, id int64) (*Coupon, error) {
	rows, err := q.ListCoupons(ctx, sqlcgen.ListCouponsParams{ID: id, RowLimit: 1})
	if err != nil {
		return nil, fmt.Errorf("get coupon %d: %w", id, err)
	}
	if len(rows) == 0 {
		return nil, newError(CodeNotFound, msgCouponNotFound)
	}
	c := couponFromRow(rows[0])
	return &c, nil
}

func setCouponPlans(ctx context.Context, q *sqlcgen.Queries, couponID int64, planIDs []int64) error {
	if err := q.ClearCouponPlans(ctx, couponID); err != nil {
		return fmt.Errorf("clear coupon %d plans: %w", couponID, err)
	}
	if err := q.InsertCouponPlans(ctx, sqlcgen.InsertCouponPlansParams{CouponID: couponID, PlanIds: planIDs}); err != nil {
		return fmt.Errorf("insert coupon %d plans: %w", couponID, err)
	}
	return nil
}

// CreateCoupon 新建优惠码；码重复返回 CodeConflict。
func (s *ShopService) CreateCoupon(ctx context.Context, actor *AdminPrincipal, in CouponInput, ip *netip.Addr) (*Coupon, error) {
	var out *Coupon
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		v, err := s.validateCoupon(ctx, q, in)
		if err != nil {
			return err
		}
		id, err := q.CreateCoupon(ctx, sqlcgen.CreateCouponParams{
			Code: v.Code, Kind: v.Kind, Value: v.Value, AllPlans: v.AllPlans, ForTrafficPack: v.ForTrafficPack, MinAmount: v.MinAmount,
			MaxUses: int32(v.MaxUses), MaxUsesPerUser: int32(v.MaxUsesPerUser), StartsAt: optionalTime(v.StartsAt), EndsAt: optionalTime(v.EndsAt),
			IsDisabled: v.Disabled, Remark: v.Remark,
		})
		if err != nil {
			if isUniqueViolation(err) {
				return newError(CodeConflict, msgCouponCodeUsed)
			}
			return fmt.Errorf("create coupon: %w", err)
		}
		if err := setCouponPlans(ctx, q, id, v.PlanIDs); err != nil {
			return err
		}
		if out, err = getCoupon(ctx, q, id); err != nil {
			return err
		}
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionCouponCreate, TargetType: targetCoupon,
			TargetID: fmt.Sprint(id), ClientIP: ip, Detail: map[string]any{"code": v.Code, "kind": v.Kind, "value": v.Value}})
	})
	return out, err
}

// UpdateCoupon 修改优惠码；已用次数不变。
func (s *ShopService) UpdateCoupon(ctx context.Context, actor *AdminPrincipal, id int64, in CouponInput, ip *netip.Addr) (*Coupon, error) {
	var out *Coupon
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		v, err := s.validateCoupon(ctx, q, in)
		if err != nil {
			return err
		}
		n, err := q.UpdateCoupon(ctx, sqlcgen.UpdateCouponParams{
			ID: id, Code: v.Code, Kind: v.Kind, Value: v.Value, AllPlans: v.AllPlans, ForTrafficPack: v.ForTrafficPack, MinAmount: v.MinAmount,
			MaxUses: int32(v.MaxUses), MaxUsesPerUser: int32(v.MaxUsesPerUser), StartsAt: optionalTime(v.StartsAt), EndsAt: optionalTime(v.EndsAt),
			IsDisabled: v.Disabled, Remark: v.Remark,
		})
		if err != nil {
			if isUniqueViolation(err) {
				return newError(CodeConflict, msgCouponCodeUsed)
			}
			return fmt.Errorf("update coupon %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgCouponNotFound)
		}
		if err := setCouponPlans(ctx, q, id, v.PlanIDs); err != nil {
			return err
		}
		if out, err = getCoupon(ctx, q, id); err != nil {
			return err
		}
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionCouponUpdate, TargetType: targetCoupon,
			TargetID: fmt.Sprint(id), ClientIP: ip, Detail: map[string]any{"code": v.Code, "isDisabled": v.Disabled}})
	})
	return out, err
}

// DeleteCoupon 删除没被用过的优惠码；用过的返回 CodeConflict（只能停用）。
func (s *ShopService) DeleteCoupon(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) error {
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.DeleteUnusedCoupon(ctx, id)
		if err != nil {
			return fmt.Errorf("delete coupon %d: %w", id, err)
		}
		if n == 0 {
			exists, err := q.CouponExists(ctx, id)
			if err != nil {
				return fmt.Errorf("check coupon %d: %w", id, err)
			}
			if !exists {
				return newError(CodeNotFound, msgCouponNotFound)
			}
			return newError(CodeConflict, msgCouponInUse)
		}
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionCouponDelete, TargetType: targetCoupon,
			TargetID: fmt.Sprint(id), ClientIP: ip})
	})
}
