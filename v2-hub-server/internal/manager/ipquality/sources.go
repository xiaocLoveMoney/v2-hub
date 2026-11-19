package ipquality

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	ip2locationBase = "https://api.ip2location.io"
	ipapiIsBase     = "https://api.ipapi.is"
	abuseIPDBBase   = "https://api.abuseipdb.com"
	ipqsBase        = "https://ipqualityscore.com"

	maxResponseBytes = 256 << 10
	headerAccept     = "Accept"
	contentTypeJSON  = "application/json"
	// abuseIPDBMaxAgeDays 是 AbuseIPDB 统计举报的天数窗口。
	abuseIPDBMaxAgeDays = "90"

	// 风险分阈值（docs/03 §5.23 风险评分）。
	abuseLowBelow     = 25
	abuseHighBelow    = 75
	ipqsLowBelow      = 75
	ipqsMediumBelow   = 85
	ipqsHighBelow     = 90
	ip2lLowBelow      = 33
	ip2lMediumBelow   = 66
	percentScale      = 100
	scoreRoundingBase = 100
)

// abuserScorePattern 解析 ipapi.is 的 abuser_score，如 "0.0039 (Low)"。
var abuserScorePattern = regexp.MustCompile(`^\s*([0-9.]+)\s*\(([A-Za-z ]+)\)\s*$`)

// fetchJSON 发请求并把 2xx 的 JSON 解到 out。错误里不带 URL（有的源把 key 放在 URL 里）。
func fetchJSON(ctx context.Context, client *http.Client, req *http.Request, out any) error {
	req = req.WithContext(ctx)
	req.Header.Set(headerAccept, contentTypeJSON)
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }() // 只读响应体，关闭错误无关紧要
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("%w: read body: %v", ErrUnavailable, err)
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return ErrRateLimited
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ErrAuth
	case resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices:
		return fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%w: decode: %v", ErrUnavailable, err)
	}
	return nil
}

func newGet(rawURL string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: build request", ErrUnavailable)
	}
	return req, nil
}

// truthy 把“可能不是严格布尔”的字段转成布尔（ipapi.is 的 is_vpn、is_crawler 可能是字符串）。
func truthy(raw json.RawMessage) *bool {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil || v == nil {
		return nil
	}
	switch t := v.(type) {
	case bool:
		return boolPtr(t)
	case string:
		return boolPtr(t != "" && !strings.EqualFold(t, "false"))
	case float64:
		return boolPtr(t != 0)
	default:
		return boolPtr(true)
	}
}

func round2(v float64) float64 { return math.Round(v*scoreRoundingBase) / scoreRoundingBase }

func clampScore(v float64) float64 { return math.Max(0, math.Min(percentScale, v)) }

// ---- IP2Location.io ----

type ip2location struct {
	client *http.Client
	base   string
	key    string
}

func (s *ip2location) Key() string      { return SourceIP2Location }
func (s *ip2location) NeedsKey() bool   { return false }
func (s *ip2location) Configured() bool { return true }

type ip2locationResp struct {
	CountryCode string   `json:"country_code"`
	RegionName  string   `json:"region_name"`
	CityName    string   `json:"city_name"`
	Latitude    *float64 `json:"latitude"`
	Longitude   *float64 `json:"longitude"`
	TimeZone    string   `json:"time_zone"`
	ASN         string   `json:"asn"`
	AS          string   `json:"as"`
	IsProxy     *bool    `json:"is_proxy"`
	UsageType   string   `json:"usage_type"`
	FraudScore  *float64 `json:"fraud_score"`
	ASInfo      *struct {
		ASUsageType string `json:"as_usage_type"`
	} `json:"as_info"`
	Proxy *struct {
		IsVPN        *bool `json:"is_vpn"`
		IsTor        *bool `json:"is_tor"`
		IsDataCenter *bool `json:"is_data_center"`
		IsWebCrawler *bool `json:"is_web_crawler"`
		IsAICrawler  *bool `json:"is_ai_crawler"`
		IsSpammer    *bool `json:"is_spammer"`
		IsScanner    *bool `json:"is_scanner"`
		IsBotnet     *bool `json:"is_botnet"`
	} `json:"proxy"`
}

func (s *ip2location) Lookup(ctx context.Context, ip netip.Addr) (Report, error) {
	q := url.Values{"ip": {ip.String()}, "format": {"json"}}
	if s.key != "" {
		q.Set("key", s.key)
	}
	req, err := newGet(s.base + "/?" + q.Encode())
	if err != nil {
		return Report{}, err
	}
	var r ip2locationResp
	if err := fetchJSON(ctx, s.client, req, &r); err != nil {
		return Report{}, err
	}
	rep := Report{
		Geo:       &Geo{Country: upper(r.CountryCode), Region: r.RegionName, City: r.CityName, Timezone: r.TimeZone, Lat: r.Latitude, Lon: r.Longitude},
		UsageType: ip2locationUsage(r.UsageType),
		Factors:   Factors{Country: upper(r.CountryCode), Proxy: r.IsProxy},
	}
	if asn, err := strconv.ParseInt(strings.TrimPrefix(strings.ToUpper(r.ASN), "AS"), 10, 64); err == nil && asn > 0 {
		rep.Network = &Network{ASN: asn, Org: r.AS}
	}
	if r.ASInfo != nil {
		rep.CompanyType = ip2locationUsage(r.ASInfo.ASUsageType)
	}
	if r.FraudScore != nil {
		v := clampScore(*r.FraudScore)
		rep.Score = &Score{Value: round2(v), Level: ip2locationLevel(v)}
	}
	if p := r.Proxy; p != nil {
		rep.Factors.VPN, rep.Factors.Tor, rep.Factors.Server = p.IsVPN, p.IsTor, p.IsDataCenter
		rep.Factors.Robot = anyTrue(p.IsWebCrawler, p.IsAICrawler)
		rep.Factors.Abuser = anyTrue(p.IsSpammer, p.IsScanner, p.IsBotnet)
	}
	return rep, nil
}

func ip2locationLevel(v float64) Level {
	switch {
	case v < ip2lLowBelow:
		return LevelLow
	case v < ip2lMediumBelow:
		return LevelMedium
	default:
		return LevelHigh
	}
}

// ip2locationUsage 映射 IP2Location 的 usage_type 代码；复合代码（ISP/MOB）取第一段。
func ip2locationUsage(code string) NetType {
	if code == "" {
		return ""
	}
	first, _, _ := strings.Cut(strings.ToUpper(code), "/")
	switch first {
	case "COM":
		return NetBusiness
	case "ORG":
		return NetOrganization
	case "GOV":
		return NetGovernment
	case "MIL":
		return NetMilitary
	case "EDU":
		return NetEducation
	case "LIB":
		return NetLibrary
	case "CDN":
		return NetCDN
	case "ISP":
		return NetResidential
	case "MOB":
		return NetMobile
	case "DCH":
		return NetHosting
	case "SES", "AIC":
		return NetCrawler
	case "RSV":
		return NetReserved
	default:
		return NetUnknown
	}
}

// ---- ipapi.is ----

type ipapiIs struct {
	client *http.Client
	base   string
	key    string
}

func (s *ipapiIs) Key() string      { return SourceIPAPIIs }
func (s *ipapiIs) NeedsKey() bool   { return true }
func (s *ipapiIs) Configured() bool { return s.key != "" }

type ipapiIsEntity struct {
	Type        string `json:"type"`
	AbuserScore string `json:"abuser_score"`
}

type ipapiIsResp struct {
	IsTor        json.RawMessage `json:"is_tor"`
	IsProxy      json.RawMessage `json:"is_proxy"`
	IsVPN        json.RawMessage `json:"is_vpn"`
	IsDatacenter json.RawMessage `json:"is_datacenter"`
	IsAbuser     json.RawMessage `json:"is_abuser"`
	IsCrawler    json.RawMessage `json:"is_crawler"`
	Company      *ipapiIsEntity  `json:"company"`
	ASN          *ipapiIsEntity  `json:"asn"`
	Location     *struct {
		CountryCode string `json:"country_code"`
	} `json:"location"`
}

func (s *ipapiIs) Lookup(ctx context.Context, ip netip.Addr) (Report, error) {
	req, err := newGet(s.base + "/?" + url.Values{"q": {ip.String()}, "key": {s.key}}.Encode())
	if err != nil {
		return Report{}, err
	}
	var r ipapiIsResp
	if err := fetchJSON(ctx, s.client, req, &r); err != nil {
		return Report{}, err
	}
	rep := Report{Factors: Factors{
		Proxy: truthy(r.IsProxy), Tor: truthy(r.IsTor), VPN: truthy(r.IsVPN),
		Server: truthy(r.IsDatacenter), Abuser: truthy(r.IsAbuser), Robot: truthy(r.IsCrawler),
	}}
	if r.Location != nil {
		rep.Factors.Country = upper(r.Location.CountryCode)
	}
	if r.ASN != nil {
		rep.UsageType = ipapiIsType(r.ASN.Type)
	}
	if r.Company != nil {
		rep.CompanyType = ipapiIsType(r.Company.Type)
		rep.Score = ipapiIsScore(r.Company.AbuserScore)
	}
	return rep, nil
}

func ipapiIsType(t string) NetType {
	switch strings.ToLower(t) {
	case "":
		return ""
	case "hosting":
		return NetHosting
	case "isp":
		return NetResidential
	case "business":
		return NetBusiness
	case "education":
		return NetEducation
	case "government":
		return NetGovernment
	case "banking":
		return NetBanking
	default:
		return NetUnknown
	}
}

// ipapiIsScore 解析 "0.0039 (Low)"：比例 × 100 为分数，等级按标签。
func ipapiIsScore(raw string) *Score {
	m := abuserScorePattern.FindStringSubmatch(raw)
	if m == nil {
		return nil
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return nil
	}
	var level Level
	switch strings.ToLower(strings.TrimSpace(m[2])) {
	case "very low", "low":
		level = LevelLow
	case "elevated":
		level = LevelMedium
	case "high":
		level = LevelHigh
	case "very high":
		level = LevelVeryHigh
	default:
		return nil
	}
	return &Score{Value: round2(clampScore(v * percentScale)), Level: level}
}

// ---- AbuseIPDB ----

type abuseIPDB struct {
	client *http.Client
	base   string
	key    string
}

func (s *abuseIPDB) Key() string      { return SourceAbuseIPDB }
func (s *abuseIPDB) NeedsKey() bool   { return true }
func (s *abuseIPDB) Configured() bool { return s.key != "" }

type abuseIPDBResp struct {
	Data struct {
		AbuseConfidenceScore *float64 `json:"abuseConfidenceScore"`
		CountryCode          string   `json:"countryCode"`
		UsageType            string   `json:"usageType"`
		IsTor                *bool    `json:"isTor"`
	} `json:"data"`
}

func (s *abuseIPDB) Lookup(ctx context.Context, ip netip.Addr) (Report, error) {
	q := url.Values{"ipAddress": {ip.String()}, "maxAgeInDays": {abuseIPDBMaxAgeDays}}
	req, err := newGet(s.base + "/api/v2/check?" + q.Encode())
	if err != nil {
		return Report{}, err
	}
	req.Header.Set("Key", s.key)
	var r abuseIPDBResp
	if err := fetchJSON(ctx, s.client, req, &r); err != nil {
		return Report{}, err
	}
	rep := Report{UsageType: abuseIPDBUsage(r.Data.UsageType), Factors: Factors{Country: upper(r.Data.CountryCode), Tor: r.Data.IsTor}}
	if r.Data.AbuseConfidenceScore != nil {
		v := clampScore(*r.Data.AbuseConfidenceScore)
		rep.Score = &Score{Value: round2(v), Level: abuseIPDBLevel(v)}
	}
	return rep, nil
}

func abuseIPDBLevel(v float64) Level {
	switch {
	case v < abuseLowBelow:
		return LevelLow
	case v < abuseHighBelow:
		return LevelHigh
	default:
		return LevelVeryHigh
	}
}

func abuseIPDBUsage(t string) NetType {
	switch t {
	case "":
		return ""
	case "Commercial":
		return NetBusiness
	case "Organization":
		return NetOrganization
	case "Government":
		return NetGovernment
	case "Military":
		return NetMilitary
	case "University/College/School":
		return NetEducation
	case "Library":
		return NetLibrary
	case "Content Delivery Network":
		return NetCDN
	case "Fixed Line ISP":
		return NetResidential
	case "Mobile ISP":
		return NetMobile
	case "Data Center/Web Hosting/Transit":
		return NetHosting
	case "Search Engine Spider":
		return NetCrawler
	case "Reserved":
		return NetReserved
	default:
		return NetUnknown
	}
}

// ---- IPQS ----

type ipqs struct {
	client *http.Client
	base   string
	key    string
}

func (s *ipqs) Key() string      { return SourceIPQS }
func (s *ipqs) NeedsKey() bool   { return true }
func (s *ipqs) Configured() bool { return s.key != "" }

type ipqsResp struct {
	Success        bool     `json:"success"`
	Message        string   `json:"message"`
	FraudScore     *float64 `json:"fraud_score"`
	CountryCode    string   `json:"country_code"`
	Proxy          *bool    `json:"proxy"`
	VPN            *bool    `json:"vpn"`
	ActiveVPN      *bool    `json:"active_vpn"`
	Tor            *bool    `json:"tor"`
	ActiveTor      *bool    `json:"active_tor"`
	RecentAbuse    *bool    `json:"recent_abuse"`
	BotStatus      *bool    `json:"bot_status"`
	IsCrawler      *bool    `json:"is_crawler"`
	ConnectionType string   `json:"connection_type"`
}

func (s *ipqs) Lookup(ctx context.Context, ip netip.Addr) (Report, error) {
	q := url.Values{"strictness": {"0"}, "allow_public_access_points": {"true"}}
	req, err := newGet(s.base + "/api/json/ip/" + url.PathEscape(s.key) + "/" + url.PathEscape(ip.String()) + "?" + q.Encode())
	if err != nil {
		return Report{}, err
	}
	var r ipqsResp
	if err := fetchJSON(ctx, s.client, req, &r); err != nil {
		return Report{}, err
	}
	if !r.Success {
		// IPQS 失败时多为 HTTP 200 + success=false：额度用完的提示带 credit / limit，其余多是 key 问题
		msg := strings.ToLower(r.Message)
		if strings.Contains(msg, "credit") || strings.Contains(msg, "limit") {
			return Report{}, ErrRateLimited
		}
		return Report{}, ErrAuth
	}
	usage := ipqsUsage(r.ConnectionType)
	rep := Report{UsageType: usage, Factors: Factors{
		Country: upper(r.CountryCode), Proxy: r.Proxy, VPN: anyTrue(r.VPN, r.ActiveVPN), Tor: anyTrue(r.Tor, r.ActiveTor),
		Abuser: r.RecentAbuse, Robot: anyTrue(r.BotStatus, r.IsCrawler),
	}}
	if usage != "" {
		rep.Factors.Server = boolPtr(usage == NetHosting)
	}
	if r.FraudScore != nil {
		v := clampScore(*r.FraudScore)
		rep.Score = &Score{Value: round2(v), Level: ipqsLevel(v)}
	}
	return rep, nil
}

func ipqsLevel(v float64) Level {
	switch {
	case v < ipqsLowBelow:
		return LevelLow
	case v < ipqsMediumBelow:
		return LevelMedium
	case v < ipqsHighBelow:
		return LevelHigh
	default:
		return LevelVeryHigh
	}
}

func ipqsUsage(t string) NetType {
	switch strings.ToLower(t) {
	case "":
		return ""
	case "residential":
		return NetResidential
	case "corporate":
		return NetBusiness
	case "education":
		return NetEducation
	case "mobile":
		return NetMobile
	case "data center":
		return NetHosting
	default:
		return NetUnknown
	}
}

// anyTrue 合并几个可空布尔：有一个为真即真；全为 nil 时为 nil。
func anyTrue(vs ...*bool) *bool {
	var out *bool
	for _, v := range vs {
		if v == nil {
			continue
		}
		if *v {
			return boolPtr(true)
		}
		out = boolPtr(false)
	}
	return out
}

func upper(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }
