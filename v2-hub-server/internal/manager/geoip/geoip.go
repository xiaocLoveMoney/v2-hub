// Package geoip 把公网 IP 换算成 ISO 3166-1 两位国家码（docs/03 §5.1 服务器位置，Ruling-65）。
// 查询走可配置的在线 IP 库（默认 api.country.is）；只把公网单播地址发出去。
package geoip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultLookupURL 是默认的查询地址，{ip} 处替换成要查的地址。
	DefaultLookupURL = "https://api.country.is/{ip}"
	// IPPlaceholder 是查询地址里的 IP 占位符。
	IPPlaceholder    = "{ip}"
	defaultTimeout   = 5 * time.Second
	maxResponseBytes = 64 << 10
	headerAccept     = "Accept"
	contentTypeJSON  = "application/json"
	countryCodeLen   = 2
)

var (
	// ErrBadConfig 表示查询地址不合法。
	ErrBadConfig = errors.New("geoip config is invalid")
	// ErrNotPublic 表示地址不是公网单播地址，不发给 IP 库。
	ErrNotPublic = errors.New("ip is not a public unicast address")
	// ErrUnreachable 表示连不上 IP 库或响应异常（网络、超时、非 2xx、无法解析）。
	ErrUnreachable = errors.New("geoip lookup unreachable")
	// ErrUnknown 表示 IP 库没给出合法的国家码。
	ErrUnknown = errors.New("geoip country unknown")
)

// sharedAddressSpace 是运营商级 NAT 的共享地址段（RFC 6598），netip 不把它算作私网。
var sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")

// countryFieldNames 是响应里可能装国家码的字段，按顺序取第一个非空的（兼容几家常见的 IP 库）。
var countryFieldNames = []string{"country", "country_code", "countryCode"}

// Config 是 IP 库配置。LookupURL 为空即关闭扫描。
type Config struct {
	LookupURL string
	Timeout   time.Duration
}

// Locator 能查出一个公网 IP 所在的国家。
type Locator interface {
	// Country 返回大写的两位国家码；地址不是公网返回 ErrNotPublic，查不到返回 ErrUnknown。
	Country(ctx context.Context, ip netip.Addr) (string, error)
}

// Client 是基于 HTTP 的 Locator，并发安全。
type Client struct {
	lookupURL string
	http      *http.Client
}

// New 按配置创建客户端；LookupURL 为空返回 (nil, nil)，表示扫描已关闭。
func New(cfg Config) (*Client, error) {
	if cfg.LookupURL == "" {
		return nil, nil
	}
	if !strings.Contains(cfg.LookupURL, IPPlaceholder) {
		return nil, fmt.Errorf("%w: lookup url must contain %s", ErrBadConfig, IPPlaceholder)
	}
	u, err := url.Parse(strings.ReplaceAll(cfg.LookupURL, IPPlaceholder, "192.0.2.1"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("%w: lookup url must be an http(s) url", ErrBadConfig)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{lookupURL: cfg.LookupURL, http: &http.Client{Timeout: timeout}}, nil
}

// IsPublic 判断地址是否是可以发给 IP 库的公网单播地址。
func IsPublic(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !sharedAddressSpace.Contains(ip)
}

// Country 实现 Locator。
func (c *Client) Country(ctx context.Context, ip netip.Addr) (string, error) {
	ip = ip.Unmap()
	if !IsPublic(ip) {
		return "", ErrNotPublic
	}
	target := strings.ReplaceAll(c.lookupURL, IPPlaceholder, ip.String())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", fmt.Errorf("%w: build request: %v", ErrUnreachable, err)
	}
	req.Header.Set(headerAccept, contentTypeJSON)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() {
		_ = resp.Body.Close() // 只读响应，关闭失败不影响结果
	}()
	if resp.StatusCode == http.StatusNotFound {
		return "", ErrUnknown
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("%w: http %d", ErrUnreachable, resp.StatusCode)
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&body); err != nil {
		return "", fmt.Errorf("%w: decode response: %v", ErrUnreachable, err)
	}
	for _, name := range countryFieldNames {
		var code string
		if json.Unmarshal(body[name], &code) != nil || code == "" {
			continue
		}
		code = strings.ToUpper(strings.TrimSpace(code))
		if IsCountryCode(code) {
			return code, nil
		}
	}
	return "", ErrUnknown
}

// IsCountryCode 判断是否是大写的 ISO 3166-1 两位国家码（另含 IP 库常用的 XK 科索沃）。
func IsCountryCode(code string) bool {
	if len(code) != countryCodeLen {
		return false
	}
	_, ok := countryCodes[code]
	return ok
}

// countryCodeList 是 ISO 3166-1 alpha-2 正式分配的码，外加 XK；与 admin-web 的 countries.ts 保持一致。
const countryCodeList = "AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ " +
	"CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ DE DJ DK DM DO DZ EC EE EG EH ER ES ET FI FJ FK FM FO FR " +
	"GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU ID IE IL IM IN IO IQ IR IS IT JE JM JO JP " +
	"KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT " +
	"MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW " +
	"SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ " +
	"UA UG UM US UY UZ VA VC VE VG VI VN VU WF WS XK YE YT ZA ZM ZW"

var countryCodes = func() map[string]struct{} {
	codes := strings.Fields(countryCodeList)
	set := make(map[string]struct{}, len(codes))
	for _, c := range codes {
		set[c] = struct{}{}
	}
	return set
}()
