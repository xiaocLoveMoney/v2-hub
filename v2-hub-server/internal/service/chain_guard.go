package service

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"v2-hub-server/internal/manager/render"
)

// 用户出口用于服务端链式的防护（docs/03 §5.19 用户出口的地址限制、防环、出站字段校验；Ruling-48、Ruling-49）。
// 起飞服务器会去连用户填的地址，所以只许公网地址；域名由平台解析后固定成 IP 下发（只做 DNS 查询，不连接）。
// 本文件不写日志：出口参数含凭据。

// Resolver 把域名解析成地址；生产用系统解析器，单测注入假实现，不发真实查询。
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

const (
	// chainResolveTimeout 是一次域名解析的时限；chainRepinAfter 是固定的地址多久复查一次（docs/03 §5.19）。
	chainResolveTimeout = 3 * time.Second
	chainRepinAfter     = 6 * time.Hour

	resolveNetworkIP = "ip"

	maxExitHostLen     = 255
	maxExitPathLen     = 1024
	maxExitSecretLen   = 255
	maxExitALPN        = 4
	maxLooseIDLen      = 30
	realityKeyLen      = 32
	maxRealityShortID  = 16
	ssKeyLenAES128     = 16
	ssKeyLenDefault    = 32
	ss2022Prefix       = "2022-blake3-"
	ss2022AES128       = "2022-blake3-aes-128-gcm"
	ss2022KeySeparator = ":"

	msgExitUnsupported = "这个出口的协议或传输方式不能用于服务端链式"
	msgExitBadField    = "这个出口的参数不能用于服务端链式：%s"
	msgExitPrivate     = "用于服务端链式的出口必须是公网地址"
	msgExitResolve     = "出口的域名解析失败，稍后再试"
	msgExitPlatform    = "出口不能指向平台自己的服务器"
)

// 各字段允许的取值（白名单）：值会写进起飞面板的 Xray 配置，写错会让内核起不来。
var (
	exitFlows        = []string{"", "xtls-rprx-vision", "xtls-rprx-vision-udp443"}
	exitSecurities   = []string{"", render.SecNone, render.SecTLS, render.SecReality}
	exitFingerprints = []string{"", "chrome", "firefox", "safari", "ios", "android", "edge", "360", "qq", "random", "randomized"}
	exitALPNs        = []string{"h2", "http/1.1", "h3"}
	exitHeaderTypes  = []string{"", "none", "http"}
	exitXHTTPModes   = []string{"", "auto", "packet-up", "stream-up", "stream-one"}
	exitVMessCiphers = []string{"", "auto", "aes-128-gcm", "chacha20-poly1305", "none", "zero"}
	exitSSMethods    = []string{
		"aes-128-gcm", "aes-256-gcm", "chacha20-poly1305", "chacha20-ietf-poly1305", "xchacha20-poly1305", "xchacha20-ietf-poly1305",
		ss2022AES128, "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305",
	}
)

var (
	exitUUIDPattern     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	exitHostnamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*\.?$`)
)

// nonPublicPrefixes 是 netip.Addr 自带的判断覆盖不到的非公网段：本网络、运营商级 NAT、协议保留、文档、基准测试、保留段，
// 以及内嵌 IPv4 的 IPv6 段（NAT64、6to4、Teredo 等，能绕到任意 IPv4 上）。
var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

// isPublicAddr 判断地址是不是公网单播地址；IPv4 映射的 IPv6 先还原成 IPv4 再判断。
func isPublicAddr(a netip.Addr) bool {
	if !a.IsValid() {
		return false
	}
	a = a.Unmap().WithZone("")
	isSpecial := a.IsUnspecified() || a.IsLoopback() || a.IsPrivate() || a.IsMulticast() ||
		a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast()
	if isSpecial {
		return false
	}
	for _, p := range nonPublicPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, unicode.IsControl)
}

func badExitField(name string) error {
	return newError(CodeInvalidArgument, fmt.Sprintf(msgExitBadField, name))
}

// validExitText 校验会原样写进配置的文本：不超长、不含控制字符。
func validExitText(s string, limit int) bool {
	return len(s) <= limit && !hasControl(s)
}

// validExitID 校验 vless / vmess 的用户 id：标准 UUID，或 Xray 接受的短字符串（不超过 30 字节）。
func validExitID(id string) bool {
	if exitUUIDPattern.MatchString(id) {
		return true
	}
	return id != "" && len(id) <= maxLooseIDLen && !hasControl(id) && !strings.ContainsFunc(id, unicode.IsSpace)
}

// validSSKey 校验 Shadowsocks 的密码；2022 系列必须是定长密钥的 Base64（可以是“服务端密钥:用户密钥”）。
func validSSKey(method, password string) bool {
	if password == "" || !validExitText(password, maxExitSecretLen) {
		return false
	}
	if !strings.HasPrefix(method, ss2022Prefix) {
		return true
	}
	want := ssKeyLenDefault
	if method == ss2022AES128 {
		want = ssKeyLenAES128
	}
	for _, part := range strings.Split(password, ss2022KeySeparator) {
		key, err := base64.StdEncoding.DecodeString(part)
		if err != nil || len(key) != want {
			return false
		}
	}
	return true
}

// validRealityKey 校验 REALITY 公钥：32 字节的 URL 安全 Base64（有无填充都接受）。
func validRealityKey(key string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(key, "="))
	return err == nil && len(raw) == realityKeyLen
}

// validRealityShortID 校验 REALITY short id：偶数位十六进制，不超过 16 位（可为空）。
func validRealityShortID(id string) bool {
	if len(id) > maxRealityShortID || len(id)%2 != 0 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// validateExitFields 逐字段校验一个出口能否写进起飞面板的 Xray 配置（docs/03 §5.19 出站字段校验）。
// 不通过返回 invalid_argument，说明里只带字段名，不带取值（取值可能是凭据）。
func validateExitFields(n render.ProxyNode) error {
	if _, ok := render.XrayOutbound(n, ""); !ok {
		return newError(CodeInvalidArgument, msgExitUnsupported)
	}
	if n.Port < 1 || n.Port > maxExitPort {
		return badExitField("端口")
	}
	if err := validateExitCredential(n); err != nil {
		return err
	}
	return validateExitTransport(n)
}

const maxExitPort = 65535

func validateExitCredential(n render.ProxyNode) error {
	switch n.Protocol {
	case render.ProtoVLESS:
		if !validExitID(n.UUID) {
			return badExitField("用户 id")
		}
		if !slices.Contains(exitFlows, n.Flow) {
			return badExitField("flow")
		}
	case render.ProtoVMess:
		if !validExitID(n.UUID) {
			return badExitField("用户 id")
		}
		if n.AlterID != 0 {
			return badExitField("alterId（只支持 0）")
		}
		if !slices.Contains(exitVMessCiphers, n.Method) {
			return badExitField("加密方式")
		}
	case render.ProtoTrojan:
		if n.Password == "" || !validExitText(n.Password, maxExitSecretLen) {
			return badExitField("密码")
		}
	case render.ProtoShadowsocks:
		if !slices.Contains(exitSSMethods, n.Method) {
			return badExitField("加密方式")
		}
		if !validSSKey(n.Method, n.Password) {
			return badExitField("密码")
		}
	case render.ProtoSocks, render.ProtoHTTP:
		if !validExitText(n.Username, maxExitSecretLen) || !validExitText(n.Password, maxExitSecretLen) {
			return badExitField("用户名或密码")
		}
	default:
		return newError(CodeInvalidArgument, msgExitUnsupported)
	}
	return nil
}

func validateExitTransport(n render.ProxyNode) error {
	if !slices.Contains(exitSecurities, n.Security) {
		return badExitField("安全类型")
	}
	if !slices.Contains(exitFingerprints, n.Fingerprint) {
		return badExitField("指纹")
	}
	if len(n.ALPN) > maxExitALPN {
		return badExitField("alpn")
	}
	for _, a := range n.ALPN {
		if !slices.Contains(exitALPNs, a) {
			return badExitField("alpn")
		}
	}
	if !slices.Contains(exitHeaderTypes, n.HeaderType) {
		return badExitField("伪装类型")
	}
	if !slices.Contains(exitXHTTPModes, n.Mode) {
		return badExitField("xhttp 模式")
	}
	if n.SNI != "" && !exitHostnamePattern.MatchString(n.SNI) {
		return badExitField("SNI")
	}
	isTextOK := validExitText(n.Host, maxExitHostLen) && validExitText(n.Path, maxExitPathLen) &&
		validExitText(n.ServiceName, maxExitHostLen) && validExitText(n.SpiderX, maxExitPathLen)
	if !isTextOK {
		return badExitField("Host、路径或 serviceName")
	}
	if n.Security != render.SecReality {
		return nil
	}
	if !validRealityKey(n.PublicKey) {
		return badExitField("REALITY 公钥")
	}
	if !validRealityShortID(n.ShortID) {
		return badExitField("REALITY short id")
	}
	if n.SNI == "" || n.Fingerprint == "" {
		return badExitField("REALITY 的 SNI 或指纹")
	}
	return nil
}

// chainGuard 负责把用户出口的地址固定成公网 IP，并挡住指向平台自己服务器的地址。
type chainGuard struct {
	resolver Resolver
}

func newChainGuard(resolver Resolver) *chainGuard {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &chainGuard{resolver: resolver}
}

var (
	// errExitResolve 表示域名这次没解析出来（暂时性的，已固定的地址可以继续用）。
	errExitResolve = errors.New("resolve exit host")
	// errExitPrivate 表示地址或解析结果里有非公网地址。
	errExitPrivate = errors.New("exit host is not public")
)

// lookup 解析一个主机名；IP 字面量直接返回。
func (g *chainGuard) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip.Unmap()}, nil
	}
	if !exitHostnamePattern.MatchString(host) {
		return nil, errExitPrivate
	}
	ctx, cancel := context.WithTimeout(ctx, chainResolveTimeout)
	defer cancel()
	addrs, err := g.resolver.LookupNetIP(ctx, resolveNetworkIP, host)
	if err != nil || len(addrs) == 0 {
		return nil, errExitResolve
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.Unmap())
	}
	return out, nil
}

// pin 把出口地址固定成一个公网 IP：全部解析结果都必须是公网地址；previous 仍在结果里就沿用，否则优先取 IPv4。
// 返回 errExitResolve（暂时解析不了）或 errExitPrivate（含非公网地址）。
func (g *chainGuard) pin(ctx context.Context, host string, previous *netip.Addr) (netip.Addr, error) {
	addrs, err := g.lookup(ctx, host)
	if err != nil {
		return netip.Addr{}, err
	}
	for _, a := range addrs {
		if !isPublicAddr(a) {
			return netip.Addr{}, errExitPrivate
		}
	}
	if previous != nil && slices.Contains(addrs, previous.Unmap()) {
		return previous.Unmap(), nil
	}
	for _, a := range addrs {
		if a.Is4() {
			return a, nil
		}
	}
	return addrs[0], nil
}

// platformAddrs 把平台自己的地址（面板地址、节点对外地址）解析成 IP 集合；解析不了的跳过。
func (g *chainGuard) platformAddrs(ctx context.Context, hosts []string) map[netip.Addr]bool {
	out := make(map[netip.Addr]bool, len(hosts))
	for _, h := range hosts {
		host := hostOf(h)
		if host == "" {
			continue
		}
		addrs, err := g.lookup(ctx, host)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			out[a] = true
		}
	}
	return out
}

// hostOf 从面板地址（URL）或对外地址（主机名 / IP）里取主机部分。
func hostOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		return u.Hostname()
	}
	return strings.Trim(raw, "[]")
}

// pinnedExit 返回把地址换成固定 IP 后的出口参数：原域名留作 TLS 的 SNI 与传输层的 Host（docs/03 §5.19）。
func pinnedExit(n render.ProxyNode, ip netip.Addr) render.ProxyNode {
	host := n.Address
	_, parseErr := netip.ParseAddr(host)
	isDomain := parseErr != nil
	n.Address = ip.String()
	if !isDomain {
		return n
	}
	if n.Security == render.SecTLS && n.SNI == "" {
		n.SNI = host
	}
	isHostTransport := n.Network == render.NetWS || n.Network == render.NetHTTPUpgrade || n.Network == render.NetXHTTP
	if isHostTransport && n.Host == "" {
		n.Host = host
	}
	return n
}

// guardError 把固定地址的失败转成给用户的说明。
func guardError(err error) error {
	if errors.Is(err, errExitResolve) {
		return newError(CodeInvalidArgument, msgExitResolve)
	}
	return newError(CodeInvalidArgument, msgExitPrivate)
}
