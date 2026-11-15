package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/crypto"
	"v2-hub-server/internal/manager/ratelimit"
)

// 余额、流水与充值码（docs/03 §5.14、§5.15）。金额一律整数分；改余额与写流水在同一事务内，先锁用户行。

// 流水类型（docs/03 §5.14 变动入口）。
const (
	LedgerTopupCode   = "topup_code"
	LedgerTopupOnline = "topup_online"
	LedgerPurchase    = "purchase"
	LedgerRenew       = "renew"
	LedgerRebate      = "rebate"
	LedgerAdminAdjust = "admin_adjust"
	LedgerForfeit     = "forfeit"
)

// 流水的关联对象类型。
const (
	refOrder     = "order"
	refTopupCode = "topup_code"
	refPayment   = "payment"
	refRebate    = "rebate"
)

// 充值码状态（由 used_at / is_disabled / expires_at 推出）。
const (
	TopupUnused   = "unused"
	TopupUsed     = "used"
	TopupDisabled = "disabled"
	TopupExpired  = "expired"
)

const (
	topupCodeLen     = 16
	topupCodeTailLen = 4
	topupAADPrefix   = "topup_code:"
	topupAADKeyBytes = 12
	topupMACPurpose  = "topup-code"
	maxTopupBatch    = 500
	maxTopupBatchLen = 64
	maxLedgerRemark  = 255

	// 兑换防猜（docs/03 §5.15）：每个用户每小时失败 10 次、每个 IP 每小时 30 次后拒绝
	redeemFailsPerUser  = 10
	redeemFailsPerIP    = 30
	redeemGuardWindow   = time.Hour
	redeemGuardMaxKeys  = 10000
	guardKeyRedeemUser  = "redeem-user:"
	guardKeyRedeemIP    = "redeem-ip:"
	forfeitRemark       = "账号注销，余额作废"
	ActionBalanceAdjust = "user.balance_adjust"
	ActionTopupCreate   = "topup_code.create"
	ActionTopupUpdate   = "topup_code.update"
	ActionTopupDelete   = "topup_code.delete"
	ActionTopupReveal   = "topup_code.reveal"
	ActionTopupRedeem   = "portal.topup_redeem"
	targetTopupCode     = "topup_code"

	msgShopDisabled      = "商店未开启"
	msgBalanceRange      = "调整后的余额超出范围"
	msgAdjustZero        = "调整金额不能为 0"
	msgRemarkRequired    = "请填写备注（1–255 个字符）"
	msgTopupBadAmount    = "面值须在 1–99999999 分之间"
	msgTopupBadCount     = "一次最多生成 500 个充值码"
	msgTopupBatchLong    = "批次备注过长"
	msgTopupNotFound     = "充值码不存在"
	msgTopupUsed         = "充值码已被使用，不能修改或删除"
	msgTopupInvalid      = "充值码无效"
	msgRedeemTooOften    = "兑换失败次数过多，请稍后再试"
	msgTopupBadStatus    = "status 只能是 unused、used、disabled、expired"
	msgLedgerBadKind     = "流水类型不合法"
	msgUserDisabledMoney = "账号已停用，不能操作余额"
)

var (
	// topupSeparators 是用户输入充值码时可能带的分组符号。
	topupSeparators = strings.NewReplacer(" ", "", "-", "")
	topupCodeRegexp = regexp.MustCompile(`^[2-9A-HJKMNP-Z]{16}$`)
	ledgerKinds     = map[string]bool{
		LedgerTopupCode: true, LedgerTopupOnline: true, LedgerPurchase: true, LedgerRenew: true, LedgerRebate: true, LedgerAdminAdjust: true, LedgerForfeit: true,
	}
	topupStatuses = map[string]bool{TopupUnused: true, TopupUsed: true, TopupDisabled: true, TopupExpired: true}
)

// ledgerRef 是一条流水的关联对象；零值表示没有。
type ledgerRef struct {
	Type string
	ID   int64
}

// ledgerChange 是一次余额变动。
type ledgerChange struct {
	UserID  int64
	Amount  int64 // 带符号（分）
	Kind    string
	Ref     ledgerRef
	Remark  string
	AdminID int64 // 管理员调整时的操作人，其余为 0
}

// applyLedger 在调用方的事务里改余额并写一条流水，返回变动后的余额。
// 先锁用户行；结果为负返回 CodeInsufficientBalance（Data 带差额），超过上限返回 CodeConflict；用户不存在返回 CodeNotFound。
func applyLedger(ctx context.Context, q *sqlcgen.Queries, c ledgerChange, now time.Time) (int64, error) {
	u, err := q.LockUserBalance(ctx, c.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, newError(CodeNotFound, msgUserNotFound)
	}
	if err != nil {
		return 0, fmt.Errorf("lock balance of user %d: %w", c.UserID, err)
	}
	return applyLedgerLocked(ctx, q, u, c, now)
}

// applyLedgerLocked 同 applyLedger，但用户行已由调用方在同一事务里用 LockUserBalance 锁住，u 是锁住时读到的行。
// 调用方须保证加锁之后、调用之前没有改过该用户的余额。
func applyLedgerLocked(ctx context.Context, q *sqlcgen.Queries, u sqlcgen.LockUserBalanceRow, c ledgerChange, now time.Time) (int64, error) {
	after := u.Balance + c.Amount
	if after < 0 {
		return 0, &Error{Code: CodeInsufficientBalance, Message: "余额不足", Data: map[string]int64{"shortfall": -after}}
	}
	if after > maxBalance {
		return 0, newError(CodeConflict, msgBalanceRange)
	}
	if err := q.SetUserBalance(ctx, sqlcgen.SetUserBalanceParams{ID: c.UserID, Balance: after}); err != nil {
		return 0, fmt.Errorf("set balance of user %d: %w", c.UserID, err)
	}
	_, err := q.InsertLedger(ctx, sqlcgen.InsertLedgerParams{
		UserID: pgtype.Int8{Int64: c.UserID, Valid: true}, Username: u.Username, Kind: c.Kind, Amount: c.Amount, BalanceAfter: after,
		RefType: c.Ref.Type, RefID: pgtype.Int8{Int64: c.Ref.ID, Valid: c.Ref.ID != 0}, Remark: c.Remark,
		AdminID: pgtype.Int8{Int64: c.AdminID, Valid: c.AdminID != 0}, CreatedAt: timestamptz(now),
	})
	if err != nil {
		return 0, fmt.Errorf("insert ledger for user %d: %w", c.UserID, err)
	}
	return after, nil
}

// forfeitBalance 在删除用户的事务里把剩余余额作废并留一条流水（docs/03 §5.14 注销，Ruling-32）。余额为 0 时什么都不做。
func forfeitBalance(ctx context.Context, q *sqlcgen.Queries, userID int64, now time.Time) error {
	u, err := q.LockUserBalance(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock balance of user %d: %w", userID, err)
	}
	if u.Balance == 0 {
		return nil
	}
	_, err = applyLedgerLocked(ctx, q, u, ledgerChange{UserID: userID, Amount: -u.Balance, Kind: LedgerForfeit, Remark: forfeitRemark}, now)
	return err
}

// WalletService 管余额流水与充值码。并发安全。
type WalletService struct {
	store
	sealer   *crypto.Sealer
	settings *SettingsService
	now      func() time.Time
	userFail *ratelimit.FailureGuard
	ipFail   *ratelimit.FailureGuard
}

// NewWalletService 创建钱包服务；now 为 nil 时用 time.Now。
func NewWalletService(pool *pgxpool.Pool, sealer *crypto.Sealer, settings *SettingsService, now func() time.Time) *WalletService {
	if now == nil {
		now = time.Now
	}
	mk := func(limit int) *ratelimit.FailureGuard {
		return ratelimit.NewFailureGuard(ratelimit.FailureGuardConfig{
			MaxFailures: limit, Window: redeemGuardWindow, Lockout: redeemGuardWindow, MaxKeys: redeemGuardMaxKeys,
		}, now)
	}
	return &WalletService{store: newStore(pool), sealer: sealer, settings: settings, now: now, userFail: mk(redeemFailsPerUser), ipFail: mk(redeemFailsPerIP)}
}

// requireShop 在商店未开启时返回 CodeFeatureDisabled（docs/03 §5.6 shop.enabled）。
func requireShop(ctx context.Context, settings *SettingsService) (Settings, error) {
	cur, err := settings.Current(ctx)
	if err != nil {
		return cur, err
	}
	if !cur.Shop.Enabled {
		return cur, newError(CodeFeatureDisabled, msgShopDisabled)
	}
	return cur, nil
}

// ---- 流水 ----------------------------------------------------------------------

// LedgerEntry 是一条余额流水。UserID 为 0 表示用户已删除；AdminUsername 只在管理员调整时非空。
type LedgerEntry struct {
	ID            int64
	UserID        int64
	Username      string
	Kind          string
	Amount        int64
	BalanceAfter  int64
	Remark        string
	AdminUsername string
	CreatedAt     time.Time
}

// LedgerFilter 是管理端流水列表的筛选；零值表示不限。
type LedgerFilter struct {
	UserID int64
	Kind   string
}

func (s *WalletService) listLedger(ctx context.Context, f LedgerFilter, pq PageQuery) (*Page[LedgerEntry], error) {
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	if f.Kind != "" && !ledgerKinds[f.Kind] {
		return nil, newError(CodeInvalidArgument, msgLedgerBadKind)
	}
	if f.UserID < 0 {
		return nil, newError(CodeInvalidArgument, msgUserNotFound)
	}
	rows, err := s.q.ListLedger(ctx, sqlcgen.ListLedgerParams{UserID: f.UserID, Kind: f.Kind, RowLimit: limit, RowOffset: offset})
	if err != nil {
		return nil, fmt.Errorf("list ledger: %w", err)
	}
	total, err := s.q.CountLedger(ctx, sqlcgen.CountLedgerParams{UserID: f.UserID, Kind: f.Kind})
	if err != nil {
		return nil, fmt.Errorf("count ledger: %w", err)
	}
	items := make([]LedgerEntry, 0, len(rows))
	for _, r := range rows {
		items = append(items, LedgerEntry{
			ID: r.ID, UserID: r.UserID.Int64, Username: r.Username, Kind: r.Kind, Amount: r.Amount, BalanceAfter: r.BalanceAfter,
			Remark: r.Remark, AdminUsername: r.AdminUsername, CreatedAt: r.CreatedAt.Time,
		})
	}
	return &Page[LedgerEntry]{Items: items, Total: total}, nil
}

// ListLedger 返回全部用户的流水分页（管理端，finance:read）。
func (s *WalletService) ListLedger(ctx context.Context, f LedgerFilter, pq PageQuery) (*Page[LedgerEntry], error) {
	return s.listLedger(ctx, f, pq)
}

// ListOwnLedger 返回本人的流水分页；身份只取自会话（R8）。
func (s *WalletService) ListOwnLedger(ctx context.Context, p *UserPrincipal, pq PageQuery) (*Page[LedgerEntry], error) {
	return s.listLedger(ctx, LedgerFilter{UserID: p.UserID}, pq)
}

// AdjustBalance 由管理员调整用户余额（docs/03 §5.14）：amount 可正可负、不能为 0，备注必填；记流水与审计。
// 错误：CodeInvalidArgument、CodeNotFound、CodeConflict（结果为负或超过上限）。
func (s *WalletService) AdjustBalance(ctx context.Context, actor *AdminPrincipal, userID, amount int64, remark string, ip *netip.Addr) error {
	remark = strings.TrimSpace(remark)
	if amount == 0 {
		return newError(CodeInvalidArgument, msgAdjustZero)
	}
	if amount < -maxBalance || amount > maxBalance {
		return newError(CodeInvalidArgument, msgBadMoney)
	}
	if n := utf8.RuneCountInString(remark); n < 1 || n > maxLedgerRemark {
		return newError(CodeInvalidArgument, msgRemarkRequired)
	}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		after, err := applyLedger(ctx, q, ledgerChange{UserID: userID, Amount: amount, Kind: LedgerAdminAdjust, Remark: remark, AdminID: actor.AdminID}, s.now())
		if errors.Is(err, ErrInsufficient) {
			return newError(CodeConflict, msgBalanceRange)
		}
		if err != nil {
			return err
		}
		return writeAudit(ctx, q, adminAudit(actor, ActionBalanceAdjust, targetUser, userID, ip, map[string]any{"amount": amount, "balanceAfter": after, "remark": remark}))
	})
}

// ---- 充值码 ---------------------------------------------------------------------

// TopupBatchInput 是生成一批充值码的输入。
type TopupBatchInput struct {
	Amount    int64
	Count     int
	ExpiresAt *time.Time // nil = 不过期
	Batch     string
}

// TopupCode 是充值码的列表视图：只带末 4 位，不含明文（Ruling-31）。
type TopupCode struct {
	ID             int64
	CodeTail       string
	Amount         int64
	Batch          string
	Status         string
	ExpiresAt      *time.Time
	Disabled       bool
	UsedByUserID   int64 // 0 = 未使用或使用人已删除
	UsedByUsername string
	UsedAt         *time.Time
	CreatedAt      time.Time
}

// TopupFilter 是充值码列表的筛选；零值表示不限。
type TopupFilter struct {
	Status string
	Batch  string
}

func randomTopupCode() (string, error) {
	code, err := randomString(inviteAlphabet, topupCodeLen)
	if err != nil {
		return "", fmt.Errorf("generate topup code: %w", err)
	}
	return code, nil
}

// normalizeTopupCode 去掉分组用的空格与连字符并转大写；形状不对时返回空串（不必查库）。
func normalizeTopupCode(raw string) string {
	v := strings.ToUpper(topupSeparators.Replace(strings.TrimSpace(raw)))
	if !topupCodeRegexp.MatchString(v) {
		return ""
	}
	return v
}

func topupStatus(disabled bool, usedAt, expiresAt pgtype.Timestamptz, now time.Time) string {
	switch {
	case usedAt.Valid:
		return TopupUsed
	case disabled:
		return TopupDisabled
	case expiresAt.Valid && !now.Before(expiresAt.Time):
		return TopupExpired
	default:
		return TopupUnused
	}
}

// CreateTopupCodes 生成一批充值码并返回明文；明文只在这里出现一次，库里只存 HMAC 与密文（docs/03 §5.15）。
func (s *WalletService) CreateTopupCodes(ctx context.Context, actor *AdminPrincipal, in TopupBatchInput, ip *netip.Addr) ([]string, error) {
	in.Batch = strings.TrimSpace(in.Batch)
	if in.Amount < 1 || in.Amount > maxMoney {
		return nil, newError(CodeInvalidArgument, msgTopupBadAmount)
	}
	if in.Count < 1 || in.Count > maxTopupBatch {
		return nil, newError(CodeInvalidArgument, msgTopupBadCount)
	}
	if utf8.RuneCountInString(in.Batch) > maxTopupBatchLen {
		return nil, newError(CodeInvalidArgument, msgTopupBatchLong)
	}
	now := s.now()
	if in.ExpiresAt != nil && !in.ExpiresAt.After(now) {
		return nil, newError(CodeInvalidArgument, "到期时间须晚于现在")
	}
	expires := optionalTime(in.ExpiresAt)
	codes := make([]string, 0, in.Count)
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		for range in.Count {
			code, err := randomTopupCode()
			if err != nil {
				return err
			}
			aadKey, err := crypto.RandomToken(topupAADKeyBytes)
			if err != nil {
				return fmt.Errorf("generate topup aad key: %w", err)
			}
			sealed, err := s.sealer.Seal([]byte(code), []byte(topupAADPrefix+aadKey))
			if err != nil {
				return fmt.Errorf("seal topup code: %w", err)
			}
			// 16 位、31 个字符的码空间约 2^79，批内与库内撞码的概率可以忽略；真撞上由唯一约束报错、整批回滚
			err = q.InsertTopupCode(ctx, sqlcgen.InsertTopupCodeParams{
				CodeHash: s.sealer.MAC(topupMACPurpose, []byte(code)), CodeEnc: sealed, AadKey: aadKey, CodeTail: code[topupCodeLen-topupCodeTailLen:],
				Amount: in.Amount, Batch: in.Batch, ExpiresAt: expires, CreatedBy: pgtype.Int8{Int64: actor.AdminID, Valid: true},
				CreatedAt: timestamptz(now),
			})
			if err != nil {
				return fmt.Errorf("insert topup code: %w", err)
			}
			codes = append(codes, code)
		}
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionTopupCreate, TargetType: targetTopupCode,
			ClientIP: ip, Detail: map[string]any{"amount": in.Amount, "count": in.Count, "batch": in.Batch}})
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// ListTopupCodes 返回充值码分页（finance:read），不含明文。
func (s *WalletService) ListTopupCodes(ctx context.Context, f TopupFilter, pq PageQuery) (*Page[TopupCode], error) {
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	if f.Status != "" && !topupStatuses[f.Status] {
		return nil, newError(CodeInvalidArgument, msgTopupBadStatus)
	}
	if utf8.RuneCountInString(f.Batch) > maxTopupBatchLen {
		return nil, newError(CodeInvalidArgument, msgTopupBatchLong)
	}
	now := s.now()
	at := timestamptz(now)
	rows, err := s.q.ListTopupCodes(ctx, sqlcgen.ListTopupCodesParams{Batch: f.Batch, Status: f.Status, Now: at, RowLimit: limit, RowOffset: offset})
	if err != nil {
		return nil, fmt.Errorf("list topup codes: %w", err)
	}
	total, err := s.q.CountTopupCodes(ctx, sqlcgen.CountTopupCodesParams{Batch: f.Batch, Status: f.Status, Now: at})
	if err != nil {
		return nil, fmt.Errorf("count topup codes: %w", err)
	}
	items := make([]TopupCode, 0, len(rows))
	for _, r := range rows {
		items = append(items, TopupCode{
			ID: r.ID, CodeTail: r.CodeTail, Amount: r.Amount, Batch: r.Batch, Status: topupStatus(r.IsDisabled, r.UsedAt, r.ExpiresAt, now),
			ExpiresAt: timePtr(r.ExpiresAt), Disabled: r.IsDisabled, UsedByUserID: r.UsedBy.Int64, UsedByUsername: r.UsedByUsername,
			UsedAt: timePtr(r.UsedAt), CreatedAt: r.CreatedAt.Time,
		})
	}
	return &Page[TopupCode]{Items: items, Total: total}, nil
}

// lockUnusedTopupCode 锁住一条充值码；不存在返回 CodeNotFound，已使用返回 CodeConflict。
func lockUnusedTopupCode(ctx context.Context, q *sqlcgen.Queries, id int64) (sqlcgen.GetTopupCodeForUpdateRow, error) {
	row, err := q.GetTopupCodeForUpdate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return row, newError(CodeNotFound, msgTopupNotFound)
	}
	if err != nil {
		return row, fmt.Errorf("get topup code %d: %w", id, err)
	}
	if row.UsedAt.Valid {
		return row, newError(CodeConflict, msgTopupUsed)
	}
	return row, nil
}

// SetTopupCodeDisabled 停用 / 启用一条未使用的充值码。
func (s *WalletService) SetTopupCodeDisabled(ctx context.Context, actor *AdminPrincipal, id int64, disabled bool, ip *netip.Addr) (*TopupCode, error) {
	var out TopupCode
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := lockUnusedTopupCode(ctx, q, id)
		if err != nil {
			return err
		}
		if err := q.SetTopupCodeDisabled(ctx, sqlcgen.SetTopupCodeDisabledParams{ID: id, IsDisabled: disabled}); err != nil {
			return fmt.Errorf("set topup code %d disabled: %w", id, err)
		}
		out = TopupCode{
			ID: row.ID, CodeTail: row.CodeTail, Amount: row.Amount, Batch: row.Batch, Status: topupStatus(disabled, row.UsedAt, row.ExpiresAt, s.now()),
			ExpiresAt: timePtr(row.ExpiresAt), Disabled: disabled, CreatedAt: row.CreatedAt.Time,
		}
		return writeAudit(ctx, q, adminAudit(actor, ActionTopupUpdate, targetTopupCode, id, ip, map[string]any{"isDisabled": disabled}))
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteTopupCode 删除一条未使用的充值码；已使用的返回 CodeConflict。
func (s *WalletService) DeleteTopupCode(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) error {
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := lockUnusedTopupCode(ctx, q, id)
		if err != nil {
			return err
		}
		if err := q.DeleteTopupCode(ctx, id); err != nil {
			return fmt.Errorf("delete topup code %d: %w", id, err)
		}
		return writeAudit(ctx, q, adminAudit(actor, ActionTopupDelete, targetTopupCode, id, ip, map[string]any{"amount": row.Amount, "batch": row.Batch}))
	})
}

// RevealTopupCode 返回一条充值码的明文并记审计（不记明文）。明文是凭据，调用方不得写日志。
func (s *WalletService) RevealTopupCode(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) (string, error) {
	var code string
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := q.GetTopupCodeForUpdate(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return newError(CodeNotFound, msgTopupNotFound)
		}
		if err != nil {
			return fmt.Errorf("get topup code %d: %w", id, err)
		}
		plain, err := s.sealer.Open(row.CodeEnc, []byte(topupAADPrefix+row.AadKey))
		if err != nil {
			return fmt.Errorf("open topup code %d: %w", id, err)
		}
		code = string(plain)
		return writeAudit(ctx, q, adminAudit(actor, ActionTopupReveal, targetTopupCode, id, ip, nil))
	})
	return code, err
}

// RedeemResult 是兑换成功的结果。
type RedeemResult struct {
	Amount  int64
	Balance int64
}

// Redeem 兑换充值码（docs/03 §5.15）：码存在、未用、未停用、未过期时余额加面值并把码标记为已用，同一事务内完成。
// 失败提示不区分原因；失败次数过多返回 CodeRateLimited。错误：CodeFeatureDisabled、CodeInvalidArgument、CodeRateLimited、CodeConflict。
func (s *WalletService) Redeem(ctx context.Context, p *UserPrincipal, rawCode string, ip *netip.Addr) (*RedeemResult, error) {
	if _, err := requireShop(ctx, s.settings); err != nil {
		return nil, err
	}
	userKey, addrKey := fmt.Sprint(guardKeyRedeemUser, p.UserID), ipKey(guardKeyRedeemIP, ip)
	if s.userFail.IsLocked(userKey) || (addrKey != "" && s.ipFail.IsLocked(addrKey)) {
		return nil, newError(CodeRateLimited, msgRedeemTooOften)
	}
	fail := func() (*RedeemResult, error) {
		s.userFail.RecordFailure(userKey)
		if addrKey != "" {
			s.ipFail.RecordFailure(addrKey)
		}
		return nil, newError(CodeInvalidArgument, msgTopupInvalid)
	}
	code := normalizeTopupCode(rawCode)
	if code == "" {
		return fail()
	}
	now := s.now()
	var res RedeemResult
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := q.GetTopupCodeByHashForUpdate(ctx, s.sealer.MAC(topupMACPurpose, []byte(code)))
		if errors.Is(err, pgx.ErrNoRows) {
			return newError(CodeInvalidArgument, msgTopupInvalid)
		}
		if err != nil {
			return fmt.Errorf("get topup code: %w", err)
		}
		if topupStatus(row.IsDisabled, row.UsedAt, row.ExpiresAt, now) != TopupUnused {
			return newError(CodeInvalidArgument, msgTopupInvalid)
		}
		after, err := applyLedger(ctx, q, ledgerChange{UserID: p.UserID, Amount: row.Amount, Kind: LedgerTopupCode, Ref: ledgerRef{Type: refTopupCode, ID: row.ID}}, now)
		if err != nil {
			return err
		}
		err = q.MarkTopupCodeUsed(ctx, sqlcgen.MarkTopupCodeUsedParams{
			ID: row.ID, UsedBy: pgtype.Int8{Int64: p.UserID, Valid: true}, UsedByUsername: p.Username, UsedAt: timestamptz(now),
		})
		if err != nil {
			return fmt.Errorf("mark topup code %d used: %w", row.ID, err)
		}
		res = RedeemResult{Amount: row.Amount, Balance: after}
		return writeAudit(ctx, q, AuditEntry{ActorType: ActorUser, ActorID: p.UserID, Action: ActionTopupRedeem, TargetType: targetTopupCode,
			TargetID: fmt.Sprint(row.ID), ClientIP: ip, Detail: map[string]any{"amount": row.Amount}})
	})
	if errors.Is(err, ErrInvalidArgument) {
		return fail()
	}
	if err != nil {
		return nil, err
	}
	return &res, nil
}
