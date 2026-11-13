package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/crypto"
)

// 邮箱验证码与账号自助（docs/03 §5.7、§5.8）。Ruling-23：注册、找回口令、换绑都用验证码，不发链接。

// 验证码用途，互不通用。
const (
	EmailCodeRegister = "register"
	EmailCodeReset    = "reset"
	EmailCodeBind     = "bind"
)

const (
	emailCodeDigits      = 6
	emailCodeSpace       = 1000000 // 10^emailCodeDigits
	emailCodeTTL         = 10 * time.Minute
	emailCodeMaxAttempts = 5
	emailCodeRetention   = 24 * time.Hour
	emailCodeMACPurpose  = "email_code:"

	// 发码频控（docs/03 §5.7）：同一邮箱每分钟 1 次、每小时 5 次；同一 IP 每小时 20 次
	emailCodePerMinute   = 1
	emailCodePerHour     = 5
	emailCodePerIPHourly = 20

	msgEmailCodeTooOften  = "验证码发送过于频繁，请稍后再试"
	msgEmailCodeInvalid   = "验证码错误或已过期"
	msgEmailCodeBadUse    = "验证码用途不合法"
	msgEmailTaken         = "该邮箱已被使用"
	msgEmailDomainBlocked = "该邮箱域名不支持注册"
	msgRegisterClosed     = "当前未开放注册"
)

var emailCodeActions = map[string]string{
	EmailCodeRegister: "注册账号",
	EmailCodeReset:    "重设登录口令",
	EmailCodeBind:     "绑定邮箱",
}

// AccountService 负责邮箱验证码与账号自助（注册、找回口令、换绑邮箱、注销）。
type AccountService struct {
	store
	sealer   *crypto.Sealer
	settings *SettingsService
	mail     *MailService
	users    *UserService
	portal   *PortalAuthService
	guards   accountGuards
	now      func() time.Time
}

// AccountDeps 是账号自助服务依赖的其它服务，字段均不可为 nil。
type AccountDeps struct {
	Settings *SettingsService
	Mail     *MailService
	// Users 用来建用户、删用户与触发对账；Portal 用来建会话与确认口令 / 两步验证。
	Users  *UserService
	Portal *PortalAuthService
}

// NewAccountService 创建账号自助服务；now 为 nil 时用 time.Now。
func NewAccountService(pool *pgxpool.Pool, sealer *crypto.Sealer, deps AccountDeps, now func() time.Time) *AccountService {
	if now == nil {
		now = time.Now
	}
	return &AccountService{store: newStore(pool), sealer: sealer, settings: deps.Settings, mail: deps.Mail, users: deps.Users, portal: deps.Portal,
		guards: newAccountGuards(now), now: now}
}

// SiteInfo 是客户门户未登录即可读的站点信息（docs/03 §5.6 公开子集）。
type SiteInfo struct {
	SiteName            string
	RegisterMode        string
	EmailVerifyRequired bool
	MailEnabled         bool
	CheckinEnabled      bool
	TicketEnabled       bool
	SharedEnabled       bool
}

// Site 返回公开的站点信息。
func (s *AccountService) Site(ctx context.Context) (*SiteInfo, error) {
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return nil, err
	}
	return &SiteInfo{
		SiteName: cur.Site.Name, RegisterMode: cur.Register.Mode, EmailVerifyRequired: s.emailVerifyRequired(cur), MailEnabled: s.mail.Enabled(),
		CheckinEnabled: cur.Checkin.Enabled, TicketEnabled: cur.Ticket.Enabled, SharedEnabled: cur.Shared.Enabled,
	}, nil
}

// emailVerifyRequired：设置为真且邮件可用时注册才要验证码（docs/03 §5.6 register.emailVerify）。
func (s *AccountService) emailVerifyRequired(cur Settings) bool {
	return cur.Register.EmailVerify && s.mail.Enabled()
}

// SendEmailCode 给未登录的访客发注册或找回口令的验证码。
// 错误：CodeFeatureDisabled（邮件未配置、注册已关闭）、CodeInvalidArgument（邮箱或用途不合法、域名不允许）、
// CodeConflict（注册用途下邮箱已被使用）、CodeRateLimited。找回口令对不存在的邮箱同样返回成功，只是不发信。
func (s *AccountService) SendEmailCode(ctx context.Context, rawEmail, purpose string, ip *netip.Addr) error {
	if purpose != EmailCodeRegister && purpose != EmailCodeReset {
		return newError(CodeInvalidArgument, msgEmailCodeBadUse)
	}
	if err := s.mail.requireEnabled(); err != nil {
		return err
	}
	email := normalizeEmail(rawEmail)
	if email == "" {
		return newError(CodeInvalidArgument, msgMailBadAddress)
	}
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return err
	}
	deliver := true
	if purpose == EmailCodeRegister {
		if err := s.checkRegisterEmail(ctx, s.q, cur, email); err != nil {
			return err
		}
	} else {
		_, err := s.q.GetEndUserIDByEmail(ctx, email)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("get user by email: %w", err)
		}
		deliver = err == nil
	}
	return s.issueEmailCode(ctx, cur, email, purpose, ip, deliver)
}

// checkRegisterEmail 校验注册用的邮箱：注册未关闭、域名允许、未被占用。
func (s *AccountService) checkRegisterEmail(ctx context.Context, q *sqlcgen.Queries, cur Settings, email string) error {
	if cur.Register.Mode == RegisterClosed {
		return newError(CodeFeatureDisabled, msgRegisterClosed)
	}
	if !emailDomainAllowed(cur.Register, email) {
		return newError(CodeInvalidArgument, msgEmailDomainBlocked)
	}
	return s.checkEmailFree(ctx, q, email)
}

func (s *AccountService) checkEmailFree(ctx context.Context, q *sqlcgen.Queries, email string) error {
	taken, err := q.ExistsEndUserIdentity(ctx, email)
	if err != nil {
		return fmt.Errorf("check email taken: %w", err)
	}
	if taken {
		return newError(CodeConflict, msgEmailTaken)
	}
	return nil
}

// emailDomainAllowed：先过黑名单再过白名单；白名单为空表示不限（docs/03 §5.8 邮箱域名）。
func emailDomainAllowed(r RegisterSettings, email string) bool {
	domain := email[strings.LastIndexByte(email, '@')+1:]
	if slices.Contains(r.EmailBlock, domain) {
		return false
	}
	return len(r.EmailAllow) == 0 || slices.Contains(r.EmailAllow, domain)
}

// issueEmailCode 过频控后生成一条验证码并（deliver 为真时）入发件箱。deliver 为假时照样记一条，
// 让频控与响应对“邮箱是否存在”表现一致，但库里的摘要对应一个没人知道的随机码。
func (s *AccountService) issueEmailCode(ctx context.Context, cur Settings, email, purpose string, ip *netip.Addr, deliver bool) error {
	code, err := randomDigits()
	if err != nil {
		return err
	}
	c, err := renderMail(mailTplCode, mailVars{Site: cur.Site.Name, Code: code, Minutes: int(emailCodeTTL / time.Minute), Action: emailCodeActions[purpose]})
	if err != nil {
		return err
	}
	now := s.now()
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := s.checkEmailCodeRate(ctx, q, email, ip, now); err != nil {
			return err
		}
		if err := q.SupersedeEmailCodes(ctx, sqlcgen.SupersedeEmailCodesParams{Email: email, Purpose: purpose}); err != nil {
			return fmt.Errorf("supersede email codes: %w", err)
		}
		if err := q.InsertEmailCode(ctx, sqlcgen.InsertEmailCodeParams{
			Email: email, Purpose: purpose, CodeHash: s.emailCodeMAC(email, purpose, code),
			ExpiresAt: pgtype.Timestamptz{Time: now.Add(emailCodeTTL), Valid: true}, ClientIp: ip,
			CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
		}); err != nil {
			return fmt.Errorf("insert email code: %w", err)
		}
		if !deliver {
			return nil
		}
		return s.mail.enqueue(ctx, q, email, c, mailPriorityCode)
	})
}

func (s *AccountService) checkEmailCodeRate(ctx context.Context, q *sqlcgen.Queries, email string, ip *netip.Addr, now time.Time) error {
	since := func(d time.Duration) pgtype.Timestamptz { return pgtype.Timestamptz{Time: now.Add(-d), Valid: true} }
	perMinute, err := q.CountEmailCodesByEmailSince(ctx, sqlcgen.CountEmailCodesByEmailSinceParams{Email: email, CreatedAt: since(time.Minute)})
	if err != nil {
		return fmt.Errorf("count email codes per minute: %w", err)
	}
	perHour, err := q.CountEmailCodesByEmailSince(ctx, sqlcgen.CountEmailCodesByEmailSinceParams{Email: email, CreatedAt: since(time.Hour)})
	if err != nil {
		return fmt.Errorf("count email codes per hour: %w", err)
	}
	var perIP int64
	if ip != nil {
		if perIP, err = q.CountEmailCodesByIPSince(ctx, sqlcgen.CountEmailCodesByIPSinceParams{ClientIp: ip, CreatedAt: since(time.Hour)}); err != nil {
			return fmt.Errorf("count email codes per ip: %w", err)
		}
	}
	if perMinute >= emailCodePerMinute || perHour >= emailCodePerHour || perIP >= emailCodePerIPHourly {
		return newError(CodeRateLimited, msgEmailCodeTooOften)
	}
	return nil
}

func (s *AccountService) emailCodeMAC(email, purpose, code string) []byte {
	return s.sealer.MAC(emailCodeMACPurpose+purpose, []byte(email+"\x00"+code))
}

// checkEmailCode 校验验证码但不作废，返回这条验证码的 id；调用方在自己的事务里用 consumeEmailCode 作废，
// 这样后续步骤失败（如用户名冲突）时用户不必重新要码。校验失败计一次并立即提交，达到上限即作废。
// 错误：CodeInvalidArgument（错误、过期、已作废，提示不区分）。
func (s *AccountService) checkEmailCode(ctx context.Context, email, purpose, code string) (int64, error) {
	var id int64
	matched := false
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := q.GetActiveEmailCodeForUpdate(ctx, sqlcgen.GetActiveEmailCodeForUpdateParams{Email: email, Purpose: purpose})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("get email code: %w", err)
		}
		if !row.ExpiresAt.Time.After(s.now()) || row.Attempts >= emailCodeMaxAttempts {
			return nil
		}
		if hmac.Equal(row.CodeHash, s.emailCodeMAC(email, purpose, code)) {
			id, matched = row.ID, true
			return nil
		}
		if err := q.BumpEmailCodeAttempts(ctx, sqlcgen.BumpEmailCodeAttemptsParams{ID: row.ID, MaxAttempts: emailCodeMaxAttempts}); err != nil {
			return fmt.Errorf("bump email code attempts: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if !matched {
		return 0, newError(CodeInvalidArgument, msgEmailCodeInvalid)
	}
	return id, nil
}

// consumeEmailCode 在调用方事务里作废一条已校验的验证码；已被别的请求用掉或刚好过期时返回 CodeInvalidArgument。
func (s *AccountService) consumeEmailCode(ctx context.Context, q *sqlcgen.Queries, id int64) error {
	n, err := q.ConsumeEmailCode(ctx, sqlcgen.ConsumeEmailCodeParams{ID: id, ExpiresAt: pgtype.Timestamptz{Time: s.now(), Valid: true}})
	if err != nil {
		return fmt.Errorf("consume email code: %w", err)
	}
	if n == 0 {
		return newError(CodeInvalidArgument, msgEmailCodeInvalid)
	}
	return nil
}

// randomDigits 返回均匀分布的 6 位数字串（含前导零）。
func randomDigits() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(emailCodeSpace))
	if err != nil {
		return "", fmt.Errorf("generate email code: %w", err)
	}
	return fmt.Sprintf("%0*d", emailCodeDigits, n.Int64()), nil
}
