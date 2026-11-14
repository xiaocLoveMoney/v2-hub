package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/crypto"
	"v2-hub-server/internal/manager/ratelimit"
)

const (
	maxPlanNameLen          = 64
	maxUserRemarkLen        = 255
	maxPlanGroups           = 500
	maxDurationDays         = 36500
	maxRenewEveryDays       = 3650
	maxMonthDay             = 31
	maxWeekday              = 7
	maxRenewTimes           = 10000
	maxLimitIP              = 1000
	emailCodeLen            = 6
	clientSecretBytes       = 18
	maxUserPageSize         = 100
	defaultUserPage         = 20
	msgUserNotFound         = "用户不存在"
	msgPlanNotFound         = "套餐不存在"
	emailCodeAlphabet       = "abcdefghijklmnopqrstuvwxyz0123456789"
	ActionPlanCreate        = "plan.create"
	ActionPlanUpdate        = "plan.update"
	ActionPlanDelete        = "plan.delete"
	ActionUserCreate        = "user.create"
	ActionUserUpdate        = "user.update"
	ActionUserDelete        = "user.delete"
	ActionUserPlan          = "user.plan_assign"
	ActionUserQuota         = "user.quota_update"
	ActionUserPasswordReset = "user.password_reset"
	ActionUserStatus        = "user.status_change"
	targetPlan              = "plan"
	targetUser              = "user"
)

// endUserNamePattern：客户后台登录名（hub-api Username）。自 M4 起不再允许 @，与邮箱登录区分开；存量含 @ 的用户名保留（docs/03 §5.8）。
var endUserNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,64}$`)

const (
	msgBadUsername   = "用户名须为 3–64 位字母、数字或 _ . -"
	msgUsernameTaken = "用户名已存在"
)

// UserConfig 是用户与下发的运行参数。
type UserConfig struct {
	// Location 是自动续期与周期重置用的时区（每月几号、每周几、0 点）。
	Location *time.Location
	// Subscription 是订阅设置（docs/03 §5.4）；Settings 非空时只作读设置失败时的兜底。
	Subscription SubscriptionConfig
	// Settings 是系统设置服务；非空时订阅设置以设置表为准（docs/03 §5.6）。
	Settings *SettingsService
}

// UserService 负责套餐、用户、配额、下发对账与用量（docs/03 §5.2）。面板操作经 PanelService 打开的 Provider（Ruling-8）。
type UserService struct {
	store
	sealer  *crypto.Sealer
	panels  *PanelService
	cfg     UserConfig
	now     func() time.Time
	trigger chan struct{}
	// linkStamps 记每台服务器上次取链接时的入站 / 对外地址摘要（serverID → string）；进程重启后为空，会全量重取一次。
	linkStamps sync.Map
	subGuards  subGuards
	// connPasswordGuard 限制每用户改连接密码的次数（docs/03 §5.4 连接密码）。
	connPasswordGuard *ratelimit.FailureGuard
}

// NewUserService 创建用户服务；now 为 nil 时用 time.Now，cfg.Location 为空时用本地时区。
func NewUserService(pool *pgxpool.Pool, sealer *crypto.Sealer, panels *PanelService, cfg UserConfig, now func() time.Time) *UserService {
	if now == nil {
		now = time.Now
	}
	if cfg.Location == nil {
		cfg.Location = time.Local
	}
	if cfg.Subscription.Title == "" {
		cfg.Subscription.Title = defaultSubscriptionTitle
	}
	if cfg.Subscription.UpdateIntervalHours <= 0 {
		cfg.Subscription.UpdateIntervalHours = defaultSubscriptionIntervalHours
	}
	if cfg.Subscription.LogRetention <= 0 {
		cfg.Subscription.LogRetention = defaultSubscribeLogRetention
	}
	return &UserService{store: newStore(pool), sealer: sealer, panels: panels, cfg: cfg, now: now, trigger: make(chan struct{}, 1), subGuards: newSubGuards(now),
		connPasswordGuard: ratelimit.NewFailureGuard(ratelimit.FailureGuardConfig{
			MaxFailures: connPasswordPerHour, Window: connPasswordWindow, Lockout: connPasswordWindow, MaxKeys: connPasswordMaxKeys,
		}, now)}
}

// ---- 套餐 ----------------------------------------------------------------------

// PlanPriceInput 是一个时长选项的输入（docs/03 §5.12）：天数 + 价格（分）。
type PlanPriceInput struct {
	DurationDays int
	Price        int64
}

// PlanPrice 是已保存的时长选项。
type PlanPrice struct {
	ID int64
	PlanPriceInput
}

// PlanInput 是新建 / 修改套餐的输入。
type PlanInput struct {
	Name             string
	PlanQuota        PlanQuota
	AllowClientChain bool
	AllowCustomNode  bool
	AllowServerChain bool
	Remark           string
	NodeGroupIDs     []int64
	// 售卖属性（docs/03 §5.12）
	OnSale      bool
	Trial       bool
	SortOrder   int
	Description string
	// Prices 为 nil 表示不改时长选项；非 nil（含空切片）表示整体替换。
	Prices []PlanPriceInput
}

// Plan 是套餐视图。
type Plan struct {
	ID int64
	PlanInput
	// SavedPrices 是已保存的时长选项（带 id），按天数升序。
	SavedPrices []PlanPrice
	UserCount   int
	CreatedAt   time.Time
}

func validateRenewal(everyDays, monthDay, weekday, maxTimes int) error {
	if everyDays < 0 || everyDays > maxRenewEveryDays || monthDay < 0 || monthDay > maxMonthDay || weekday < 0 || weekday > maxWeekday || maxTimes < 0 || maxTimes > maxRenewTimes {
		return newError(CodeInvalidArgument, "自动续期参数超出范围")
	}
	if renewalModes(everyDays, monthDay, weekday) > 1 {
		return newError(CodeInvalidArgument, "自动续期只能选一种方式：每 N 天、每月几号或每周几")
	}
	return nil
}

func validateReset(cycle string, day int) error {
	if !trafficResetCycles[cycle] {
		return newError(CodeInvalidArgument, "周期流量重置取值不合法")
	}
	if day < 1 || day > maxMonthDay {
		return newError(CodeInvalidArgument, "按月重置的日期须为 1–31")
	}
	return nil
}

func validatePlanQuota(p PlanQuota) error {
	if p.TrafficLimit < 0 || p.DurationDays < 0 || p.DurationDays > maxDurationDays || p.LimitIP < 0 || p.LimitIP > maxLimitIP {
		return newError(CodeInvalidArgument, "流量、时长或 IP 限制超出范围")
	}
	return validateReset(p.TrafficReset, p.TrafficResetDay)
}

// validatePlanSale 校验售卖属性与时长选项：最多 6 个、天数不重复、价格在范围内。
func validatePlanSale(in PlanInput) error {
	if in.SortOrder < 0 || in.SortOrder > maxSortOrder {
		return newError(CodeInvalidArgument, msgBadSortOrder)
	}
	if utf8.RuneCountInString(in.Description) > maxPlanDescriptionLen {
		return newError(CodeInvalidArgument, fmt.Sprintf("卖点说明不能超过 %d 个字符", maxPlanDescriptionLen))
	}
	if len(in.Prices) > maxPlanPrices {
		return newError(CodeInvalidArgument, fmt.Sprintf("时长选项最多 %d 个", maxPlanPrices))
	}
	seen := make(map[int]struct{}, len(in.Prices))
	for _, p := range in.Prices {
		if p.DurationDays < minPriceDays || p.DurationDays > maxPriceDays {
			return newError(CodeInvalidArgument, fmt.Sprintf("时长选项的天数须为 %d–%d", minPriceDays, maxPriceDays))
		}
		if p.Price < 0 || p.Price > maxMoney {
			return newError(CodeInvalidArgument, msgBadMoney)
		}
		if _, dup := seen[p.DurationDays]; dup {
			return newError(CodeInvalidArgument, "时长选项的天数不能重复")
		}
		seen[p.DurationDays] = struct{}{}
	}
	return nil
}

func (s *UserService) validatePlanInput(ctx context.Context, q *sqlcgen.Queries, in PlanInput) (PlanInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > maxPlanNameLen {
		return in, newError(CodeInvalidArgument, fmt.Sprintf("名称长度须为 1–%d", maxPlanNameLen))
	}
	if utf8.RuneCountInString(in.Remark) > maxUserRemarkLen {
		return in, newError(CodeInvalidArgument, "备注过长")
	}
	if err := validatePlanQuota(in.PlanQuota); err != nil {
		return in, err
	}
	in.Description = strings.TrimSpace(in.Description)
	if err := validatePlanSale(in); err != nil {
		return in, err
	}
	in.NodeGroupIDs = dedupeIDs(in.NodeGroupIDs)
	if len(in.NodeGroupIDs) > maxPlanGroups {
		return in, newError(CodeInvalidArgument, "节点组过多")
	}
	n, err := q.CountNodeGroupsByIDs(ctx, in.NodeGroupIDs)
	if err != nil {
		return in, fmt.Errorf("count node groups: %w", err)
	}
	if int(n) != len(in.NodeGroupIDs) {
		return in, newError(CodeInvalidArgument, "包含不存在的节点组")
	}
	return in, nil
}

func dedupeIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

func planFromRow(r sqlcgen.ListPlansRow) Plan {
	return Plan{
		ID: r.ID,
		PlanInput: PlanInput{
			Name: r.Name, AllowClientChain: r.AllowClientChain, AllowCustomNode: r.AllowCustomNode, AllowServerChain: r.AllowServerChain, Remark: r.Remark,
			NodeGroupIDs: r.NodeGroupIds, OnSale: r.IsOnSale, Trial: r.IsTrial, SortOrder: int(r.SortOrder), Description: r.Description,
			PlanQuota: PlanQuota{
				TrafficLimit: r.TrafficLimit, DurationDays: int(r.DurationDays), StartOnFirstUse: r.StartOnFirstUse,
				LimitIP: int(r.LimitIp), TrafficReset: r.TrafficReset, TrafficResetDay: int(r.TrafficResetDay),
			},
		},
		SavedPrices: []PlanPrice{}, UserCount: int(r.UserCount), CreatedAt: r.CreatedAt.Time,
	}
}

// ListPlans 返回全部套餐，各带自己的时长选项。
func (s *UserService) ListPlans(ctx context.Context) ([]Plan, error) {
	rows, err := s.q.ListPlans(ctx)
	if err != nil {
		return nil, fmt.Errorf("list plans: %w", err)
	}
	prices, err := s.q.ListPlanPrices(ctx)
	if err != nil {
		return nil, fmt.Errorf("list plan prices: %w", err)
	}
	byPlan := make(map[int64][]PlanPrice, len(rows))
	for _, p := range prices {
		byPlan[p.PlanID] = append(byPlan[p.PlanID], PlanPrice{ID: p.ID, PlanPriceInput: PlanPriceInput{DurationDays: int(p.DurationDays), Price: p.Price}})
	}
	out := make([]Plan, 0, len(rows))
	for _, r := range rows {
		plan := planFromRow(r)
		if saved, ok := byPlan[r.ID]; ok {
			plan.SavedPrices = saved
		}
		out = append(out, plan)
	}
	return out, nil
}

func (s *UserService) getPlanView(ctx context.Context, id int64) (*Plan, error) {
	list, err := s.ListPlans(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i], nil
		}
	}
	return nil, newError(CodeNotFound, msgPlanNotFound)
}

func planParams(in PlanInput) sqlcgen.CreatePlanParams {
	p := in.PlanQuota
	return sqlcgen.CreatePlanParams{
		Name: in.Name, TrafficLimit: p.TrafficLimit, DurationDays: int32(p.DurationDays), StartOnFirstUse: p.StartOnFirstUse,
		LimitIp: int32(p.LimitIP), TrafficReset: p.TrafficReset, TrafficResetDay: int32(p.TrafficResetDay),
		AllowClientChain: in.AllowClientChain, AllowCustomNode: in.AllowCustomNode, AllowServerChain: in.AllowServerChain, Remark: in.Remark,
		IsOnSale: in.OnSale, IsTrial: in.Trial, SortOrder: int32(in.SortOrder), Description: in.Description,
	}
}

// setPlanPrices 整体替换套餐的时长选项：留下的按（套餐, 天数）原地改价、保住 id，不在新清单里的删除。prices 为 nil 时不动。
func setPlanPrices(ctx context.Context, q *sqlcgen.Queries, planID int64, prices []PlanPriceInput) error {
	if prices == nil {
		return nil
	}
	keep := make([]int32, 0, len(prices))
	for _, p := range prices {
		if err := q.UpsertPlanPrice(ctx, sqlcgen.UpsertPlanPriceParams{PlanID: planID, DurationDays: int32(p.DurationDays), Price: p.Price}); err != nil {
			return fmt.Errorf("upsert plan %d price for %d days: %w", planID, p.DurationDays, err)
		}
		keep = append(keep, int32(p.DurationDays))
	}
	if err := q.DeletePlanPricesExcept(ctx, sqlcgen.DeletePlanPricesExceptParams{PlanID: planID, KeepDays: keep}); err != nil {
		return fmt.Errorf("prune plan %d prices: %w", planID, err)
	}
	return nil
}

func setPlanGroups(ctx context.Context, q *sqlcgen.Queries, planID int64, groups []int64) error {
	if err := q.ClearPlanNodeGroups(ctx, planID); err != nil {
		return fmt.Errorf("clear plan %d groups: %w", planID, err)
	}
	if err := q.InsertPlanNodeGroups(ctx, sqlcgen.InsertPlanNodeGroupsParams{PlanID: planID, GroupIds: groups}); err != nil {
		return fmt.Errorf("insert plan %d groups: %w", planID, err)
	}
	return nil
}

// CreatePlan 新建套餐。
func (s *UserService) CreatePlan(ctx context.Context, actor *AdminPrincipal, in PlanInput, ip *netip.Addr) (*Plan, error) {
	var id int64
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		v, err := s.validatePlanInput(ctx, q, in)
		if err != nil {
			return err
		}
		if id, err = q.CreatePlan(ctx, planParams(v)); err != nil {
			if isUniqueViolation(err) {
				return newError(CodeConflict, "套餐名称已存在")
			}
			return fmt.Errorf("create plan: %w", err)
		}
		if err := setPlanGroups(ctx, q, id, v.NodeGroupIDs); err != nil {
			return err
		}
		if err := setPlanPrices(ctx, q, id, v.Prices); err != nil {
			return err
		}
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionPlanCreate, TargetType: targetPlan,
			TargetID: fmt.Sprint(id), ClientIP: ip, Detail: map[string]any{"name": v.Name}})
	})
	if err != nil {
		return nil, err
	}
	return s.getPlanView(ctx, id)
}

// UpdatePlan 修改套餐；不影响已分配用户的配额（docs/03 §5.2），但节点组变了会改变授权，所以触发对账。
func (s *UserService) UpdatePlan(ctx context.Context, actor *AdminPrincipal, id int64, in PlanInput, ip *netip.Addr) (*Plan, error) {
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		v, err := s.validatePlanInput(ctx, q, in)
		if err != nil {
			return err
		}
		p := planParams(v)
		n, err := q.UpdatePlan(ctx, sqlcgen.UpdatePlanParams{
			ID: id, Name: p.Name, TrafficLimit: p.TrafficLimit, DurationDays: p.DurationDays, StartOnFirstUse: p.StartOnFirstUse,
			LimitIp: p.LimitIp, TrafficReset: p.TrafficReset, TrafficResetDay: p.TrafficResetDay,
			AllowClientChain: p.AllowClientChain, AllowCustomNode: p.AllowCustomNode, AllowServerChain: p.AllowServerChain, Remark: p.Remark,
			IsOnSale: p.IsOnSale, IsTrial: p.IsTrial, SortOrder: p.SortOrder, Description: p.Description,
		})
		if err != nil {
			if isUniqueViolation(err) {
				return newError(CodeConflict, "套餐名称已存在")
			}
			return fmt.Errorf("update plan %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgPlanNotFound)
		}
		if err := setPlanGroups(ctx, q, id, v.NodeGroupIDs); err != nil {
			return err
		}
		if err := setPlanPrices(ctx, q, id, v.Prices); err != nil {
			return err
		}
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionPlanUpdate, TargetType: targetPlan,
			TargetID: fmt.Sprint(id), ClientIP: ip, Detail: map[string]any{"name": v.Name, "nodeGroups": len(v.NodeGroupIDs)}})
	})
	if err != nil {
		return nil, err
	}
	s.TriggerReconcile()
	return s.getPlanView(ctx, id)
}

// DeletePlan 删除套餐；用户保留已拷贝的额度与到期，所属套餐置空、失去节点授权。
func (s *UserService) DeletePlan(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) error {
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.DeletePlan(ctx, id)
		if err != nil {
			return fmt.Errorf("delete plan %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgPlanNotFound)
		}
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionPlanDelete, TargetType: targetPlan, TargetID: fmt.Sprint(id), ClientIP: ip})
	})
	if err == nil {
		s.TriggerReconcile()
	}
	return err
}

// ---- 用户 ----------------------------------------------------------------------

// ProvisionCounts 是用户在各面板的下发状态计数。
type ProvisionCounts struct {
	Synced  int
	Pending int
	Failed  int
}

// User 是用户视图（不含密码与面板凭据）。
type User struct {
	ID         int64
	Username   string
	Remark     string
	PlanID     *int64
	PlanName   string
	Status     string
	Quota      Quota
	Provisions ProvisionCounts
	CreatedAt  time.Time
	// TOTPEnabled 表示用户已开启两步验证；LastUsedAt 是最近一次产生流量的时间（nil = 从未使用）。
	TOTPEnabled bool
	LastUsedAt  *time.Time
	// Email 为空串表示未绑定；InvitedBy 为 nil 表示没有邀请人（或邀请人已删除）。
	Email             string
	EmailVerified     bool
	InvitedBy         *int64
	InvitedByUsername string
	// Balance 是余额（分）；AutoRenew 表示开了扣余额自动续费（docs/03 §5.14、§5.16）。
	Balance   int64
	AutoRenew bool
}

// UserFilter 是用户列表的筛选条件。
type UserFilter struct {
	Keyword string
	Status  string
	PlanID  int64
}

// clientCredential 是用户在各面板 client 上共用的凭据，加密存储（关联数据 end_user:<id>:client）。
// Password 是用户自设的连接密码（Ruling-58），空 = 面板的 password 字段沿用 Auth。
type clientCredential struct {
	UUID     string `json:"uuid"`
	Auth     string `json:"auth"`
	Password string `json:"password,omitempty"`
}

func credentialAAD(userID int64) []byte {
	return []byte(fmt.Sprintf("end_user:%d:client", userID))
}

func randomEmailCode() (string, error) {
	var b strings.Builder
	limit := big.NewInt(int64(len(emailCodeAlphabet)))
	for range emailCodeLen {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("random email code: %w", err)
		}
		b.WriteByte(emailCodeAlphabet[n.Int64()])
	}
	return b.String(), nil
}

func newClientCredential() (clientCredential, error) {
	var u [16]byte
	if _, err := rand.Read(u[:]); err != nil {
		return clientCredential{}, fmt.Errorf("random uuid: %w", err)
	}
	u[6] = (u[6] & 0x0f) | 0x40 // RFC 4122 版本 4
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 4122 变体
	tok, err := crypto.RandomToken(clientSecretBytes)
	if err != nil {
		return clientCredential{}, err
	}
	id := fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
	cred := clientCredential{UUID: id}
	cred.Auth = tok
	return cred, nil
}

// clientEmail 是用户在面板上的 client email（docs/03 §6）。
func clientEmail(userID int64, code string) string {
	return fmt.Sprintf("u%d-%s", userID, code)
}

func quotaFromRow(r sqlcgen.GetUserQuotaRow) Quota {
	q := Quota{
		TrafficLimit: r.TrafficLimit, TrafficUsed: r.TrafficUsed, PendingDays: int(r.PendingDays), LimitIP: int(r.LimitIp),
		RenewEveryDays: int(r.RenewEveryDays), RenewMonthDay: int(r.RenewMonthDay), RenewWeekday: int(r.RenewWeekday),
		RenewMax: int(r.RenewMax), RenewCount: int(r.RenewCount), TrafficReset: r.TrafficReset, TrafficResetDay: int(r.TrafficResetDay),
		LastTrafficResetAt: r.LastTrafficResetAt.Time, Disabled: r.IsDisabled,
	}
	if r.ExpiresAt.Valid {
		t := r.ExpiresAt.Time
		q.ExpiresAt = &t
	}
	return q
}

func saveQuota(ctx context.Context, q *sqlcgen.Queries, id int64, planID pgtype.Int8, qt Quota, status string) error {
	var expires pgtype.Timestamptz
	if qt.ExpiresAt != nil {
		expires = pgtype.Timestamptz{Time: *qt.ExpiresAt, Valid: true}
	}
	err := q.SaveUserQuota(ctx, sqlcgen.SaveUserQuotaParams{
		ID: id, PlanID: planID, TrafficLimit: qt.TrafficLimit, TrafficUsed: qt.TrafficUsed, ExpiresAt: expires,
		PendingDays: int32(qt.PendingDays), LimitIp: int32(qt.LimitIP), RenewEveryDays: int32(qt.RenewEveryDays),
		RenewMonthDay: int32(qt.RenewMonthDay), RenewWeekday: int32(qt.RenewWeekday), RenewMax: int32(qt.RenewMax),
		RenewCount: int32(qt.RenewCount), TrafficReset: qt.TrafficReset, TrafficResetDay: int32(qt.TrafficResetDay),
		LastTrafficResetAt: pgtype.Timestamptz{Time: qt.LastTrafficResetAt, Valid: true}, IsDisabled: qt.Disabled, Status: status,
	})
	if err != nil {
		return fmt.Errorf("save user %d quota: %w", id, err)
	}
	return nil
}

func planQuotaFromRow(p sqlcgen.GetPlanRow) PlanQuota {
	return PlanQuota{
		TrafficLimit: p.TrafficLimit, DurationDays: int(p.DurationDays), StartOnFirstUse: p.StartOnFirstUse, LimitIP: int(p.LimitIp),
		TrafficReset: p.TrafficReset, TrafficResetDay: int(p.TrafficResetDay),
	}
}

func (s *UserService) loadPlan(ctx context.Context, q *sqlcgen.Queries, id int64) (sqlcgen.GetPlanRow, error) {
	p, err := q.GetPlan(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, newError(CodeInvalidArgument, msgPlanNotFound)
	}
	if err != nil {
		return p, fmt.Errorf("get plan %d: %w", id, err)
	}
	return p, nil
}

// lockUser 锁住用户行读取配额；用户不存在或待删除返回 404。
func lockUser(ctx context.Context, q *sqlcgen.Queries, id int64) (sqlcgen.GetUserQuotaRow, error) {
	row, err := q.GetUserQuota(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return row, newError(CodeNotFound, msgUserNotFound)
	}
	if err != nil {
		return row, fmt.Errorf("lock user %d: %w", id, err)
	}
	return row, nil
}

// UserCreateInput 是新建用户的输入；PlanID 为 0 表示暂不分配套餐。
type UserCreateInput struct {
	Username string
	Password string
	Remark   string
	PlanID   int64
	// Email 可空；管理员填的邮箱视为已验证（docs/03 §5.8 邮箱绑定）。
	Email string
}

// newUserSpec 是建一个用户所需的全部数据，管理员建号与自助注册共用。
type newUserSpec struct {
	Username      string
	LoginHash     string
	Remark        string
	Email         string // 已规范化；空串 = 没有邮箱
	EmailVerified bool
	PlanID        int64 // 0 = 不分配套餐
	InvitedBy     int64 // 0 = 没有邀请人
	InviteCodeID  int64
}

// createUserTx 在事务里建用户：生成面板凭据并加密保存，按套餐写入额度。
// 错误：CodeConflict（用户名或邮箱已被占用）、套餐不存在时的 CodeNotFound。
func (s *UserService) createUserTx(ctx context.Context, q *sqlcgen.Queries, spec newUserSpec, now time.Time) (int64, error) {
	code, err := randomEmailCode()
	if err != nil {
		return 0, err
	}
	cred, err := newClientCredential()
	if err != nil {
		return 0, err
	}
	if spec.Email != "" {
		taken, err := q.ExistsEndUserIdentity(ctx, spec.Email)
		if err != nil {
			return 0, fmt.Errorf("check email taken: %w", err)
		}
		if taken {
			return 0, newError(CodeConflict, msgEmailTaken)
		}
	}
	id, err := q.CreateUser(ctx, sqlcgen.CreateUserParams{
		Username: spec.Username, PasswordHash: spec.LoginHash, Remark: spec.Remark, EmailCode: code,
		Email:           textArg(spec.Email),
		EmailVerifiedAt: pgtype.Timestamptz{Time: now, Valid: spec.Email != "" && spec.EmailVerified},
		InvitedBy:       int8Arg(spec.InvitedBy),
		InviteCodeID:    int8Arg(spec.InviteCodeID),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return 0, newError(CodeConflict, msgUsernameTaken)
		}
		return 0, fmt.Errorf("create user: %w", err)
	}
	plain, err := json.Marshal(cred)
	if err != nil {
		return 0, fmt.Errorf("encode credential: %w", err)
	}
	sealed, err := s.sealer.Seal(plain, credentialAAD(id))
	if err != nil {
		return 0, fmt.Errorf("seal credential of user %d: %w", id, err)
	}
	if err := q.SetUserCredential(ctx, sqlcgen.SetUserCredentialParams{ID: id, ClientCredEnc: sealed}); err != nil {
		return 0, fmt.Errorf("save credential of user %d: %w", id, err)
	}
	qt := Quota{TrafficReset: TrafficResetNever, TrafficResetDay: 1, LastTrafficResetAt: now}
	var planID pgtype.Int8
	if spec.PlanID > 0 {
		p, err := s.loadPlan(ctx, q, spec.PlanID)
		if err != nil {
			return 0, err
		}
		qt.ApplyPlan(planQuotaFromRow(p), now)
		planID = pgtype.Int8{Int64: p.ID, Valid: true}
	}
	if err := saveQuota(ctx, q, id, planID, qt, qt.Status(now)); err != nil {
		return 0, err
	}
	return id, nil
}

// CreateUser 新建用户（U1），可同时分配套餐（U6）；面板凭据在此生成并加密保存。
func (s *UserService) CreateUser(ctx context.Context, actor *AdminPrincipal, in UserCreateInput, ip *netip.Addr) (*User, error) {
	in.Username = strings.TrimSpace(in.Username)
	if !endUserNamePattern.MatchString(in.Username) {
		return nil, newError(CodeInvalidArgument, msgBadUsername)
	}
	if err := validateUserLoginSecret(in.Password); err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(in.Remark) > maxUserRemarkLen {
		return nil, newError(CodeInvalidArgument, "备注过长")
	}
	email := ""
	if strings.TrimSpace(in.Email) != "" {
		if email = normalizeEmail(in.Email); email == "" {
			return nil, newError(CodeInvalidArgument, msgMailBadAddress)
		}
	}
	hash, err := crypto.HashPassword(in.Password)
	if err != nil {
		return nil, fmt.Errorf("hash user password: %w", err)
	}
	var id int64
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		spec := newUserSpec{Username: in.Username, LoginHash: hash, Remark: in.Remark, Email: email, EmailVerified: true, PlanID: in.PlanID}
		if id, err = s.createUserTx(ctx, q, spec, s.now()); err != nil {
			return err
		}
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionUserCreate, TargetType: targetUser,
			TargetID: fmt.Sprint(id), ClientIP: ip, Detail: map[string]any{"username": in.Username, "planId": in.PlanID}})
	})
	if err != nil {
		return nil, err
	}
	s.TriggerReconcile()
	return s.GetUser(ctx, id)
}

func pageBounds(page, size int) (int, int, error) {
	if page == 0 {
		page = 1
	}
	if size == 0 {
		size = defaultUserPage
	}
	if page < 1 || size < 1 || size > maxUserPageSize {
		return 0, 0, newError(CodeInvalidArgument, fmt.Sprintf("page ≥ 1，pageSize 取 1–%d", maxUserPageSize))
	}
	return page, size, nil
}

func textArg(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

func int8Arg(v int64) pgtype.Int8 {
	return pgtype.Int8{Int64: v, Valid: v > 0}
}

func userFromRow(r sqlcgen.ListUsersRow) User {
	q := Quota{
		TrafficLimit: r.TrafficLimit, TrafficUsed: r.TrafficUsed, PendingDays: int(r.PendingDays), LimitIP: int(r.LimitIp),
		RenewEveryDays: int(r.RenewEveryDays), RenewMonthDay: int(r.RenewMonthDay), RenewWeekday: int(r.RenewWeekday),
		RenewMax: int(r.RenewMax), RenewCount: int(r.RenewCount), TrafficReset: r.TrafficReset, TrafficResetDay: int(r.TrafficResetDay),
		Disabled: r.IsDisabled,
	}
	if r.ExpiresAt.Valid {
		t := r.ExpiresAt.Time
		q.ExpiresAt = &t
	}
	u := User{
		ID: r.ID, Username: r.Username, Remark: r.Remark, PlanName: r.PlanName, Status: r.Status, Quota: q, CreatedAt: r.CreatedAt.Time,
		Provisions:  ProvisionCounts{Synced: int(r.SyncedCount), Pending: int(r.PendingCount), Failed: int(r.FailedCount)},
		TOTPEnabled: r.TotpEnabled, LastUsedAt: timePtr(r.LastUsedAt),
		Email: r.Email.String, EmailVerified: r.IsEmailVerified, InvitedByUsername: r.InvitedByUsername,
		Balance: r.Balance, AutoRenew: r.AutoRenew,
	}
	if r.InvitedBy.Valid {
		id := r.InvitedBy.Int64
		u.InvitedBy = &id
	}
	if r.PlanID.Valid {
		id := r.PlanID.Int64
		u.PlanID = &id
	}
	return u
}

func isUserStatus(s string) bool {
	switch s {
	case UserStatusActive, UserStatusLimited, UserStatusExpired, UserStatusDisabled:
		return true
	default:
		return false
	}
}

// ListUsers 分页列出用户。
func (s *UserService) ListUsers(ctx context.Context, f UserFilter, page, size int) ([]User, int64, error) {
	page, size, err := pageBounds(page, size)
	if err != nil {
		return nil, 0, err
	}
	if f.Status != "" && !isUserStatus(f.Status) {
		return nil, 0, newError(CodeInvalidArgument, "状态取值不合法")
	}
	if utf8.RuneCountInString(f.Keyword) > maxPlanNameLen {
		return nil, 0, newError(CodeInvalidArgument, "关键字过长")
	}
	total, err := s.q.CountUsers(ctx, sqlcgen.CountUsersParams{Keyword: textArg(f.Keyword), Status: textArg(f.Status), PlanID: int8Arg(f.PlanID)})
	if err != nil {
		return nil, 0, fmt.Errorf("count users: %w", err)
	}
	if total == 0 {
		return []User{}, 0, nil
	}
	rows, err := s.q.ListUsers(ctx, sqlcgen.ListUsersParams{
		Keyword: textArg(f.Keyword), Status: textArg(f.Status), PlanID: int8Arg(f.PlanID),
		RowLimit: int32(size), RowOffset: int32((page - 1) * size),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}
	out := make([]User, 0, len(rows))
	for _, r := range rows {
		out = append(out, userFromRow(r))
	}
	return out, total, nil
}

// GetUser 返回用户详情。
func (s *UserService) GetUser(ctx context.Context, id int64) (*User, error) {
	rows, err := s.q.ListUsers(ctx, sqlcgen.ListUsersParams{UserID: int8Arg(id), RowLimit: 1})
	if err != nil {
		return nil, fmt.Errorf("get user %d: %w", id, err)
	}
	if len(rows) == 0 {
		return nil, newError(CodeNotFound, msgUserNotFound)
	}
	u := userFromRow(rows[0])
	return &u, nil
}

// UserUpdateInput 是修改备注与启停的输入（U8）。
type UserUpdateInput struct {
	Remark     string
	IsDisabled bool
	// Email 为 nil 表示不改；空串清除；其余按邮箱校验并视为已验证（docs/03 §5.8 邮箱绑定）。
	Email *string
}

// setUserEmailTx 设置或清除用户邮箱（设置时视为已验证）。邮箱已被别的用户占用返回 CodeConflict；与本人现有邮箱相同则原样保留。
func setUserEmailTx(ctx context.Context, q *sqlcgen.Queries, id int64, email string, now time.Time) error {
	if email != "" {
		cur, err := q.GetEndUserEmail(ctx, id)
		if err != nil {
			return fmt.Errorf("get email of user %d: %w", id, err)
		}
		if cur.String != email {
			taken, err := q.ExistsEndUserIdentity(ctx, email)
			if err != nil {
				return fmt.Errorf("check email taken: %w", err)
			}
			if taken {
				return newError(CodeConflict, msgEmailTaken)
			}
		}
	}
	_, err := q.SetUserEmail(ctx, sqlcgen.SetUserEmailParams{
		ID: id, Email: textArg(email), EmailVerifiedAt: pgtype.Timestamptz{Time: now, Valid: email != ""},
	})
	if err != nil {
		return fmt.Errorf("set email of user %d: %w", id, err)
	}
	return nil
}

// UpdateUser 修改备注、启停与邮箱。
func (s *UserService) UpdateUser(ctx context.Context, actor *AdminPrincipal, id int64, in UserUpdateInput, ip *netip.Addr) (*User, error) {
	if utf8.RuneCountInString(in.Remark) > maxUserRemarkLen {
		return nil, newError(CodeInvalidArgument, "备注过长")
	}
	email := ""
	if in.Email != nil && strings.TrimSpace(*in.Email) != "" {
		if email = normalizeEmail(*in.Email); email == "" {
			return nil, newError(CodeInvalidArgument, msgMailBadAddress)
		}
	}
	now := s.now()
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := lockUser(ctx, q, id)
		if err != nil {
			return err
		}
		if in.Email != nil {
			if err := setUserEmailTx(ctx, q, id, email, now); err != nil {
				return err
			}
		}
		qt := quotaFromRow(row)
		qt.Disabled = in.IsDisabled
		status := qt.Status(now)
		if err := saveQuota(ctx, q, id, row.PlanID, qt, status); err != nil {
			return err
		}
		if _, err := q.UpdateUserRemark(ctx, sqlcgen.UpdateUserRemarkParams{ID: id, Remark: in.Remark}); err != nil {
			return fmt.Errorf("update user %d remark: %w", id, err)
		}
		return s.afterQuotaChange(ctx, q, actor, id, ActionUserUpdate, ip, map[string]any{"isDisabled": in.IsDisabled}, row.Status != status)
	})
	if err != nil {
		return nil, err
	}
	return s.GetUser(ctx, id)
}

// afterQuotaChange 记审计；需要改面板时把该用户的下发标为待同步并触发对账。
func (s *UserService) afterQuotaChange(ctx context.Context, q *sqlcgen.Queries, actor *AdminPrincipal, id int64, action string, ip *netip.Addr, detail map[string]any, needsSync bool) error {
	if needsSync {
		if err := q.MarkUserProvisionsPending(ctx, id); err != nil {
			return fmt.Errorf("mark provisions of user %d pending: %w", id, err)
		}
		s.TriggerReconcile()
	}
	return writeAudit(ctx, q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: action, TargetType: targetUser,
		TargetID: fmt.Sprint(id), ClientIP: ip, Detail: detail})
}

// AssignPlan 分配或续费套餐（U6）。停用的用户须先启用（409）。
func (s *UserService) AssignPlan(ctx context.Context, actor *AdminPrincipal, id, planID int64, ip *netip.Addr) (*User, error) {
	now := s.now()
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := lockUser(ctx, q, id)
		if err != nil {
			return err
		}
		if row.IsDisabled {
			return newError(CodeConflict, "用户已停用，请先启用")
		}
		p, err := s.loadPlan(ctx, q, planID)
		if err != nil {
			return err
		}
		qt := quotaFromRow(row)
		qt.ApplyPlan(planQuotaFromRow(p), now)
		if err := saveQuota(ctx, q, id, pgtype.Int8{Int64: p.ID, Valid: true}, qt, qt.Status(now)); err != nil {
			return err
		}
		return s.afterQuotaChange(ctx, q, actor, id, ActionUserPlan, ip, map[string]any{"planId": planID, "planName": p.Name}, true)
	})
	if err != nil {
		return nil, err
	}
	return s.GetUser(ctx, id)
}

// UserQuotaInput 是管理员直接改配额的输入（U7），整体替换。
type UserQuotaInput struct {
	TrafficLimit    int64
	TrafficUsed     int64
	ExpiresAt       *time.Time
	PendingDays     int
	LimitIP         int
	RenewEveryDays  int
	RenewMonthDay   int
	RenewWeekday    int
	RenewMax        int
	TrafficReset    string
	TrafficResetDay int
}

// UpdateQuota 直接改已用、额度、到期、IP 限制、自动续期与周期重置（U7），每次记审计。
func (s *UserService) UpdateQuota(ctx context.Context, actor *AdminPrincipal, id int64, in UserQuotaInput, ip *netip.Addr) (*User, error) {
	if in.TrafficLimit < 0 || in.TrafficUsed < 0 || in.PendingDays < 0 || in.PendingDays > maxDurationDays || in.LimitIP < 0 || in.LimitIP > maxLimitIP {
		return nil, newError(CodeInvalidArgument, "流量、天数或 IP 限制超出范围")
	}
	if err := validateRenewal(in.RenewEveryDays, in.RenewMonthDay, in.RenewWeekday, in.RenewMax); err != nil {
		return nil, err
	}
	if err := validateReset(in.TrafficReset, in.TrafficResetDay); err != nil {
		return nil, err
	}
	now := s.now()
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := lockUser(ctx, q, id)
		if err != nil {
			return err
		}
		before := quotaFromRow(row)
		qt := before
		qt.TrafficLimit, qt.TrafficUsed, qt.ExpiresAt, qt.PendingDays, qt.LimitIP = in.TrafficLimit, in.TrafficUsed, in.ExpiresAt, in.PendingDays, in.LimitIP
		if qt.ExpiresAt != nil {
			qt.PendingDays = 0
		}
		qt.RenewEveryDays, qt.RenewMonthDay, qt.RenewWeekday, qt.RenewMax = in.RenewEveryDays, in.RenewMonthDay, in.RenewWeekday, in.RenewMax
		qt.TrafficReset, qt.TrafficResetDay = in.TrafficReset, in.TrafficResetDay
		status := qt.Status(now)
		if err := saveQuota(ctx, q, id, row.PlanID, qt, status); err != nil {
			return err
		}
		detail := map[string]any{
			"trafficUsed": [2]int64{before.TrafficUsed, qt.TrafficUsed}, "trafficLimit": [2]int64{before.TrafficLimit, qt.TrafficLimit},
			"limitIp": qt.LimitIP, "expiresAt": qt.ExpiresAt,
		}
		needsSync := row.Status != status || before.LimitIP != qt.LimitIP
		return s.afterQuotaChange(ctx, q, actor, id, ActionUserQuota, ip, detail, needsSync)
	})
	if err != nil {
		return nil, err
	}
	return s.GetUser(ctx, id)
}

// ResetUserPassword 重置客户后台登录密码。
func (s *UserService) ResetUserPassword(ctx context.Context, actor *AdminPrincipal, id int64, pw string, ip *netip.Addr) error {
	if err := validateUserLoginSecret(pw); err != nil {
		return err
	}
	hash, err := crypto.HashPassword(pw)
	if err != nil {
		return fmt.Errorf("hash user password: %w", err)
	}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.SetUserPasswordHash(ctx, sqlcgen.SetUserPasswordHashParams{ID: id, PasswordHash: hash})
		if err != nil {
			return fmt.Errorf("set user %d password: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgUserNotFound)
		}
		// 管理员重置密码后该用户全部会话失效（docs/03 §5.3）
		if err := q.DeleteUserSessionsByUser(ctx, id); err != nil {
			return fmt.Errorf("revoke sessions of user %d: %w", id, err)
		}
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionUserPasswordReset, TargetType: targetUser, TargetID: fmt.Sprint(id), ClientIP: ip})
	})
}

// DeleteUser 把用户标为待删除；对账删掉各面板 client 后再删记录（docs/03 §5.2）。
func (s *UserService) DeleteUser(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) error {
	return s.deleteUser(ctx, id, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionUserDelete, TargetType: targetUser, TargetID: fmt.Sprint(id), ClientIP: ip})
}

// deleteUser 把用户标记为待删除并触发各面板清理（docs/03 §5.2 删除用户）；管理员删除与本人注销共用，audit 由调用方给出。
// 未关闭的工单当场删除；没有面板 client 的用户当场清除平台记录。
func (s *UserService) deleteUser(ctx context.Context, id int64, audit AuditEntry) error {
	var servers []int64
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		if servers, err = q.ListUserServerIDs(ctx, id); err != nil {
			return fmt.Errorf("list servers of user %d: %w", id, err)
		}
		// 剩余余额作废并留一条流水，之后订单与流水保留、user_id 置空（Ruling-32）
		if err := forfeitBalance(ctx, q, id, s.now()); err != nil {
			return err
		}
		n, err := q.MarkUserDeleted(ctx, id)
		if err != nil {
			return fmt.Errorf("mark user %d deleted: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgUserNotFound)
		}
		if err := q.DeleteUserSessionsByUser(ctx, id); err != nil {
			return fmt.Errorf("revoke sessions of user %d: %w", id, err)
		}
		if err := q.DeleteUnclosedTicketsByUser(ctx, id); err != nil {
			return fmt.Errorf("delete open tickets of user %d: %w", id, err)
		}
		// 链式规则与用户出口随用户删除（docs/03 §5.18 生命周期）；先删规则，再删它引用的出口
		if err := q.DeleteChainRoutesByUser(ctx, id); err != nil {
			return fmt.Errorf("delete chain routes of user %d: %w", id, err)
		}
		if err := q.DeleteChainExitsByUser(ctx, id); err != nil {
			return fmt.Errorf("delete chain exits of user %d: %w", id, err)
		}
		// 没有任何面板 client 的用户不会再有对账来收尾，这里当场清除；有 client 的等对账删完后清除
		if _, err := q.PurgeDeletedUsers(ctx); err != nil {
			return fmt.Errorf("purge deleted users: %w", err)
		}
		return writeAudit(ctx, q, audit)
	})
	if err != nil {
		return err
	}
	s.reconcileServers(ctx, servers)
	return nil
}

// Provision 是用户在某台面板上的下发状态。
type Provision struct {
	ServerID   int64
	ServerName string
	Email      string
	Status     string
	LastError  string
	RetryCount int
	SyncedAt   *time.Time
	UpdatedAt  time.Time
}

// ListUserProvisions 返回用户在各面板上的下发状态。
func (s *UserService) ListUserProvisions(ctx context.Context, id int64) ([]Provision, error) {
	if _, err := s.GetUser(ctx, id); err != nil {
		return nil, err
	}
	rows, err := s.q.ListUserProvisions(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list provisions of user %d: %w", id, err)
	}
	out := make([]Provision, 0, len(rows))
	for _, r := range rows {
		out = append(out, Provision{
			ServerID: r.ServerID, ServerName: r.ServerName, Email: r.Email, Status: r.Status, LastError: r.LastError,
			RetryCount: int(r.RetryCount), SyncedAt: timePtr(r.SyncedAt), UpdatedAt: r.UpdatedAt.Time,
		})
	}
	return out, nil
}
