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

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/dns"
)

const (
	maxDNSTokenLen    = 255
	maxDomainLabelLen = 63
	// ipLabelPrefix 加在由 IP 地址转成的域名前缀上（1.2.3.4 → ip-1-2-3-4）。
	ipLabelPrefix = "ip-"

	ActionDNSAccountCreate = "dns_account.create"
	ActionDNSAccountUpdate = "dns_account.update"
	ActionDNSAccountDelete = "dns_account.delete"
	targetDNSAccount       = "dns_account"

	msgDNSAccountNotFound  = "DNS 账号不存在"
	msgDNSAccountInUse     = "还有主机在用这个 DNS 账号，不能删除"
	msgDNSAccountNameTaken = "DNS 账号名称已存在"
	msgDNSZoneInUse        = "还有主机在用这个 DNS 账号，不能改根域名或服务商"
	msgBadDNSProvider      = "不支持的 DNS 服务商："
	msgBadZone             = "根域名不合法"
	msgBadDNSToken         = "令牌不能为空，不超过 255 个字符，且不含空白"
	msgDomainNeedsAccount  = "没选 DNS 账号时域名必须留空"
	msgBadDomain           = "域名不合法"
	msgDomainOutOfZone     = "域名须是所选 DNS 账号的根域名或它的子域名："
	msgDomainNeedsPublicIP = "选了 DNS 账号时主机地址要填公网 IP 或域名：域名会解析到这个地址，内网地址客户端连不上"
)

// nonLabelChars 匹配域名前缀里不能出现的字符。
var nonLabelChars = regexp.MustCompile(`[^a-z0-9]+`)

// DNSAccount 是 DNS 账号视图，不含令牌。
type DNSAccount struct {
	ID           int64
	Name         string
	ProviderType string
	Zone         string
	Remark       string
	HostCount    int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// DNSAccountInput 是新建 / 修改 DNS 账号的输入。Token 为空表示沿用原值（仅修改时）。
type DNSAccountInput struct {
	Name         string
	ProviderType string
	Zone         string
	Token        string
	Remark       string
}

func dnsTokenAAD(id int64) []byte { return fmt.Appendf(nil, "dns_account:%d:token", id) }

func dnsAccountFromRow(r sqlcgen.GetDNSAccountRow) DNSAccount {
	return DNSAccount{
		ID: r.ID, Name: r.Name, ProviderType: r.ProviderType, Zone: r.Zone, Remark: r.Remark,
		HostCount: int(r.HostCount), CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
}

// normalizeDomainName 转小写、去掉首尾空白与结尾的点，并校验是不是合法的多级域名；不合法返回空串。
func normalizeDomainName(name string) string {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	if name == "" || len(name) > maxDomainLen || !strings.Contains(name, ".") || !hostnamePattern.MatchString(name) {
		return ""
	}
	if _, err := netip.ParseAddr(name); err == nil {
		return ""
	}
	return name
}

// validateDNSAccount 校验并规范化 DNS 账号的输入；令牌是否必填由调用方决定。
func (s *HostService) validateDNSAccount(in DNSAccountInput) (DNSAccountInput, error) {
	name, err := validateName(in.Name)
	if err != nil {
		return in, err
	}
	in.Name = name
	if !s.dns.Has(in.ProviderType) {
		return in, newError(CodeInvalidArgument, msgBadDNSProvider+in.ProviderType)
	}
	if in.Zone = normalizeDomainName(in.Zone); in.Zone == "" {
		return in, newError(CodeInvalidArgument, msgBadZone)
	}
	if utf8.RuneCountInString(in.Remark) > maxRemarkLen {
		return in, newError(CodeInvalidArgument, "备注过长")
	}
	if len(in.Token) > maxDNSTokenLen || strings.ContainsAny(in.Token, " \t\r\n") {
		return in, newError(CodeInvalidArgument, msgBadDNSToken)
	}
	return in, nil
}

// ListDNSAccounts 返回全部 DNS 账号。
func (s *HostService) ListDNSAccounts(ctx context.Context) ([]DNSAccount, error) {
	rows, err := s.q.ListDNSAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("list dns accounts: %w", err)
	}
	out := make([]DNSAccount, 0, len(rows))
	for _, r := range rows {
		out = append(out, dnsAccountFromRow(sqlcgen.GetDNSAccountRow(r)))
	}
	return out, nil
}

func (s *HostService) getDNSAccount(ctx context.Context, id int64) (*DNSAccount, error) {
	row, err := s.q.GetDNSAccount(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, newError(CodeNotFound, msgDNSAccountNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get dns account %d: %w", id, err)
	}
	a := dnsAccountFromRow(row)
	return &a, nil
}

// checkZone 用令牌在线查一次根域名（docs/03 §5.22 “DNS 账号”）；服务商拒绝或连不上返回 502。
func (s *HostService) checkZone(ctx context.Context, providerType, zone, token string) error {
	provider, err := s.dns.Open(providerType, dns.Account{Zone: zone, Token: token})
	if err != nil {
		return fmt.Errorf("open dns provider %s: %w", providerType, err)
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.CommandTimeout)
	defer cancel()
	err = provider.CheckZone(ctx)
	if de := dns.AsError(err); de != nil {
		return newError(CodeUpstream, de.Message)
	}
	return err
}

func (s *HostService) sealDNSToken(ctx context.Context, q *sqlcgen.Queries, id int64, token string) error {
	sealed, err := s.sealer.Seal([]byte(token), dnsTokenAAD(id))
	if err != nil {
		return fmt.Errorf("seal dns token: %w", err)
	}
	if err := q.SetDNSAccountToken(ctx, sqlcgen.SetDNSAccountTokenParams{ID: id, TokenEnc: sealed}); err != nil {
		return fmt.Errorf("save dns token: %w", err)
	}
	return nil
}

func dnsAccountAudit(in DNSAccountInput) map[string]any {
	return map[string]any{"name": in.Name, "providerType": in.ProviderType, "zone": in.Zone}
}

// CreateDNSAccount 新建 DNS 账号；保存前用令牌在线校验根域名。按管理员频控（与发起动作共用额度）。
func (s *HostService) CreateDNSAccount(ctx context.Context, actor *AdminPrincipal, in DNSAccountInput, ip *netip.Addr) (*DNSAccount, error) {
	in, err := s.validateDNSAccount(in)
	if err != nil {
		return nil, err
	}
	if in.Token == "" {
		return nil, newError(CodeInvalidArgument, msgBadDNSToken)
	}
	if err := allow(s.actions, actor.AdminID); err != nil {
		return nil, err
	}
	if err := s.checkZone(ctx, in.ProviderType, in.Zone, in.Token); err != nil {
		return nil, err
	}
	var id int64
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		id, err = q.CreateDNSAccount(ctx, sqlcgen.CreateDNSAccountParams{Name: in.Name, ProviderType: in.ProviderType, Zone: in.Zone, Remark: in.Remark})
		if isUniqueViolation(err) {
			return newError(CodeConflict, msgDNSAccountNameTaken)
		}
		if err != nil {
			return fmt.Errorf("create dns account: %w", err)
		}
		if err := s.sealDNSToken(ctx, q, id, in.Token); err != nil {
			return err
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionDNSAccountCreate, TargetType: targetDNSAccount,
			TargetID: fmt.Sprint(id), ClientIP: ip, Detail: dnsAccountAudit(in),
		})
	})
	if err != nil {
		return nil, err
	}
	return s.getDNSAccount(ctx, id)
}

// dnsSecret 是解密后的 DNS 账号，只在内存里短暂存在。
type dnsSecret struct {
	providerType string
	zone         string
	token        string
}

func (s *HostService) openDNSAccount(ctx context.Context, id int64) (*dnsSecret, error) {
	row, err := s.q.GetDNSAccountSecret(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, newError(CodeNotFound, msgDNSAccountNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get dns account %d: %w", id, err)
	}
	token, err := s.sealer.Open(row.TokenEnc, dnsTokenAAD(id))
	if err != nil {
		return nil, fmt.Errorf("open dns token %d: %w", id, err)
	}
	return &dnsSecret{providerType: row.ProviderType, zone: row.Zone, token: string(token)}, nil
}

// UpdateDNSAccount 修改 DNS 账号；Token 留空沿用原值。改了令牌、根域名或服务商会重新在线校验；
// 仍被主机引用时不能改根域名与服务商（主机的域名都在原来的根域名下）。
func (s *HostService) UpdateDNSAccount(ctx context.Context, actor *AdminPrincipal, id int64, in DNSAccountInput, ip *netip.Addr) (*DNSAccount, error) {
	in, err := s.validateDNSAccount(in)
	if err != nil {
		return nil, err
	}
	current, err := s.getDNSAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	isMoved := in.Zone != current.Zone || in.ProviderType != current.ProviderType
	if isMoved && current.HostCount > 0 {
		return nil, newError(CodeConflict, msgDNSZoneInUse)
	}
	isTokenChanged := in.Token != ""
	if isMoved || isTokenChanged {
		token := in.Token
		if !isTokenChanged {
			secret, err := s.openDNSAccount(ctx, id)
			if err != nil {
				return nil, err
			}
			token = secret.token
		}
		if err := allow(s.actions, actor.AdminID); err != nil {
			return nil, err
		}
		if err := s.checkZone(ctx, in.ProviderType, in.Zone, token); err != nil {
			return nil, err
		}
	}
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.UpdateDNSAccount(ctx, sqlcgen.UpdateDNSAccountParams{ID: id, Name: in.Name, ProviderType: in.ProviderType, Zone: in.Zone, Remark: in.Remark})
		if isUniqueViolation(err) {
			return newError(CodeConflict, msgDNSAccountNameTaken)
		}
		if err != nil {
			return fmt.Errorf("update dns account %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgDNSAccountNotFound)
		}
		if isTokenChanged {
			if err := s.sealDNSToken(ctx, q, id, in.Token); err != nil {
				return err
			}
		}
		detail := dnsAccountAudit(in)
		detail["tokenChanged"] = isTokenChanged
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionDNSAccountUpdate, TargetType: targetDNSAccount,
			TargetID: fmt.Sprint(id), ClientIP: ip, Detail: detail,
		})
	})
	if err != nil {
		return nil, err
	}
	return s.getDNSAccount(ctx, id)
}

// DeleteDNSAccount 删除 DNS 账号；仍被主机引用时返回 409。
func (s *HostService) DeleteDNSAccount(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) error {
	// 先取名称给审计用；“仍被引用”由外键拦（ON DELETE RESTRICT）
	account, err := s.getDNSAccount(ctx, id)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.DeleteDNSAccount(ctx, id)
		if isForeignKeyViolation(err) {
			return newError(CodeConflict, msgDNSAccountInUse)
		}
		if err != nil {
			return fmt.Errorf("delete dns account %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgDNSAccountNotFound)
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionDNSAccountDelete, TargetType: targetDNSAccount,
			TargetID: fmt.Sprint(id), ClientIP: ip, Detail: map[string]any{"name": account.Name, "zone": account.Zone},
		})
	})
}

// domainLabel 把一段文字转成域名里的一级：转小写，字母数字以外的字符换成 -，去掉首尾的 -；转不出来返回空串。
func domainLabel(text string) string {
	label := strings.Trim(nonLabelChars.ReplaceAllString(strings.ToLower(text), "-"), "-")
	if len(label) > maxDomainLabelLen {
		label = strings.Trim(label[:maxDomainLabelLen], "-")
	}
	return label
}

// defaultDomain 给没填域名的主机生成一个（docs/03 §5.22 “域名”）：地址本身就是根域名之下的域名时直接用它（Ruling-79）；
// 否则取“前缀.根域名”，前缀取主机名称，转不出来就取地址。
func defaultDomain(name, address, zone string) string {
	if domain := normalizeDomainName(address); domain != "" && dns.InZone(domain, zone) {
		return domain
	}
	label := domainLabel(name)
	if label == "" {
		label = domainLabel(address)
		if _, err := netip.ParseAddr(address); err == nil {
			label = ipLabelPrefix + label
		}
	}
	return label + "." + zone
}

// hostDomain 算出一台主机要保存的 DNS 账号与域名：没选账号时域名必须为空；选了账号而域名为空就生成一个，
// 填了的校验它在账号的根域名之下。账号不存在返回 404。
func hostDomain(ctx context.Context, q *sqlcgen.Queries, dnsAccountID *int64, domain, name, address string) (pgtype.Int8, string, error) {
	if dnsAccountID == nil {
		if strings.TrimSpace(domain) != "" {
			return pgtype.Int8{}, "", newError(CodeInvalidArgument, msgDomainNeedsAccount)
		}
		return pgtype.Int8{}, "", nil
	}
	// 域名要解析到主机地址（Ruling-75）：内网、回环等地址解析出去客户端连不上；与平台同机时的 Docker 网关地址也在此列
	if addr, err := netip.ParseAddr(address); err == nil && isLANAddr(addr) {
		return pgtype.Int8{}, "", newError(CodeInvalidArgument, msgDomainNeedsPublicIP)
	}
	account, err := q.GetDNSAccountSecret(ctx, *dnsAccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.Int8{}, "", newError(CodeNotFound, msgDNSAccountNotFound)
	}
	if err != nil {
		return pgtype.Int8{}, "", fmt.Errorf("get dns account %d: %w", *dnsAccountID, err)
	}
	if strings.TrimSpace(domain) == "" {
		domain = defaultDomain(name, address, account.Zone)
	}
	normalized := normalizeDomainName(domain)
	if normalized == "" {
		return pgtype.Int8{}, "", newError(CodeInvalidArgument, msgBadDomain+"："+domain)
	}
	if !dns.InZone(normalized, account.Zone) {
		return pgtype.Int8{}, "", newError(CodeInvalidArgument, msgDomainOutOfZone+account.Zone)
	}
	return pgtype.Int8{Int64: *dnsAccountID, Valid: true}, normalized, nil
}

// isLANAddr 报告地址是不是本机或局域网里的地址（内网、回环、链路本地、未指定、组播），解析到它客户端连不上。
// 比 isPublicAddr 宽：文档、基准测试等保留网段不在这里拦，管理员不会把它们当成主机地址填进来。
func isLANAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return !addr.IsGlobalUnicast() || addr.IsPrivate()
}
