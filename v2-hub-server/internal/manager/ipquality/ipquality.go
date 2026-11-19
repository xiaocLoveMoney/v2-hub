// Package ipquality 是服务器 IP 质量检测的平台侧（docs/03 §5.23，Ruling-82）：
// 各 IP 数据源（每源一个 Source 实现）输出统一的中立模型，Registry 并发查询并归类每个源的状态；DNSBL 查精选的 DNS 黑名单。
// 只把公网单播地址发给数据源；数据源的 key 由调用方从环境变量注入，不进日志与错误信息。
package ipquality

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"sort"
	"sync"
	"time"
)

// 数据源的键。
const (
	SourceIP2Location = "ip2location"
	SourceCymru       = "cymru"
	SourceTor         = "tor"
	SourceIPAPIIs     = "ipapiis"
	SourceAbuseIPDB   = "abuseipdb"
	SourceIPQS        = "ipqs"
)

// 一个源在一次检测里的状态。
const (
	StatusOK            = "ok"
	StatusNotConfigured = "not_configured"
	StatusRateLimited   = "rate_limited"
	StatusAuthFailed    = "auth_failed"
	StatusError         = "error"
)

const (
	defaultTimeout = 10 * time.Second
	// rateLimitCooldown 是某源返回限流后不再请求它的时长。
	rateLimitCooldown = time.Hour
)

var (
	// ErrRateLimited 表示数据源的额度用完。
	ErrRateLimited = errors.New("ip data source rate limited")
	// ErrAuth 表示 key 无效或被拒（IP2Location.io 免 key 额度用完也是这一类）。
	ErrAuth = errors.New("ip data source rejected the key")
	// ErrUnavailable 表示连不上或返回异常。
	ErrUnavailable = errors.New("ip data source unavailable")
	// ErrNotPublic 表示地址不是公网单播地址，不发给数据源。
	ErrNotPublic = errors.New("ip is not a public unicast address")
)

// NetType 是归一后的使用类型（docs/03 §5.23 使用类型）。
type NetType string

// 使用类型取值。
const (
	NetHosting      NetType = "hosting"
	NetResidential  NetType = "residential"
	NetMobile       NetType = "mobile"
	NetBusiness     NetType = "business"
	NetEducation    NetType = "education"
	NetGovernment   NetType = "government"
	NetMilitary     NetType = "military"
	NetOrganization NetType = "organization"
	NetLibrary      NetType = "library"
	NetBanking      NetType = "banking"
	NetCDN          NetType = "cdn"
	NetCrawler      NetType = "crawler"
	NetReserved     NetType = "reserved"
	NetUnknown      NetType = "unknown"
)

// Level 是风险等级，取值按严重程度递增。
type Level string

// 风险等级取值。
const (
	LevelLow      Level = "low"
	LevelMedium   Level = "medium"
	LevelHigh     Level = "high"
	LevelVeryHigh Level = "very_high"
)

var levelRank = map[Level]int{LevelLow: 1, LevelMedium: 2, LevelHigh: 3, LevelVeryHigh: 4}

// Score 是一个源的风险分（0–100）与等级。
type Score struct {
	Value float64
	Level Level
}

// Factors 是风险因子；nil 表示该源不提供或未知。
type Factors struct {
	Country string
	Proxy   *bool
	Tor     *bool
	VPN     *bool
	Server  *bool
	Abuser  *bool
	Robot   *bool
}

// Geo 是使用地信息。
type Geo struct {
	Country  string
	Region   string
	City     string
	Timezone string
	Lat, Lon *float64
}

// Network 是网络归属信息。
type Network struct {
	ASN        int64
	Org        string
	Prefix     string
	RegCountry string
	Registry   string
}

// Report 是一个源给出的中立结果；没有的部分为零值。
type Report struct {
	Geo         *Geo
	Network     *Network
	UsageType   NetType
	CompanyType NetType
	Score       *Score
	Factors     Factors
}

// Source 是一个 IP 数据源。
type Source interface {
	Key() string
	// NeedsKey 报告这个源是否要 key 才能用；Configured 报告 key 是否已配置（免 key 的源恒为真）。
	NeedsKey() bool
	Configured() bool
	Lookup(ctx context.Context, ip netip.Addr) (Report, error)
}

// SourceInfo 是设置页显示的数据源目录项。
type SourceInfo struct {
	Key        string
	NeedsKey   bool
	Configured bool
}

// SourceResult 是一个源在一次检测里的结果。
type SourceResult struct {
	Key    string
	Status string
	Report Report
}

// Keys 是要 key 的数据源的 key（环境变量 HUB_IPQ_*），空 = 未配置。
type Keys struct {
	IP2Location string
	IPAPIIs     string
	AbuseIPDB   string
	IPQS        string
}

// Config 是平台侧数据源的配置。
type Config struct {
	Keys Keys
	// BaseURL 只给联调用：非空时各 HTTP 源改为请求 BaseURL + "/" + 源键 + 原路径。
	BaseURL string
	// Resolver 给 Cymru 与 Tor 查询用；nil 用系统解析器。
	Resolver Resolver
	Timeout  time.Duration
	// HTTPClient 为空时按 Timeout 新建。
	HTTPClient *http.Client
	Now        func() time.Time
}

// Registry 持有全部数据源，并发安全。
type Registry struct {
	sources []Source
	now     func() time.Time

	mu       sync.Mutex
	cooldown map[string]time.Time
}

// NewRegistry 按配置组装第一期的数据源（Ruling-82）。
func NewRegistry(cfg Config) (*Registry, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: cfg.Timeout}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Resolver == nil {
		res, err := NewResolver("")
		if err != nil {
			return nil, err
		}
		cfg.Resolver = res
	}
	hc := cfg.HTTPClient
	base := func(def, key string) string {
		if cfg.BaseURL != "" {
			return cfg.BaseURL + "/" + key
		}
		return def
	}
	sources := []Source{
		&ip2location{client: hc, base: base(ip2locationBase, SourceIP2Location), key: cfg.Keys.IP2Location},
		&cymru{resolver: cfg.Resolver},
		&torExit{resolver: cfg.Resolver},
		&ipapiIs{client: hc, base: base(ipapiIsBase, SourceIPAPIIs), key: cfg.Keys.IPAPIIs},
		&abuseIPDB{client: hc, base: base(abuseIPDBBase, SourceAbuseIPDB), key: cfg.Keys.AbuseIPDB},
		&ipqs{client: hc, base: base(ipqsBase, SourceIPQS), key: cfg.Keys.IPQS},
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Key() < sources[j].Key() })
	return &Registry{sources: sources, now: cfg.Now, cooldown: make(map[string]time.Time, len(sources))}, nil
}

// Catalog 返回数据源目录，按键排序。
func (r *Registry) Catalog() []SourceInfo {
	out := make([]SourceInfo, 0, len(r.sources))
	for _, s := range r.sources {
		out = append(out, SourceInfo{Key: s.Key(), NeedsKey: s.NeedsKey(), Configured: s.Configured()})
	}
	return out
}

// Lookup 并发查询全部数据源，返回按键排序的结果；单个源失败只体现在它自己的状态里。
func (r *Registry) Lookup(ctx context.Context, ip netip.Addr) ([]SourceResult, error) {
	if !IsPublic(ip) {
		return nil, ErrNotPublic
	}
	out := make([]SourceResult, len(r.sources))
	var wg sync.WaitGroup
	for i, s := range r.sources {
		out[i] = SourceResult{Key: s.Key()}
		if !s.Configured() {
			out[i].Status = StatusNotConfigured
			continue
		}
		if r.coolingDown(s.Key()) {
			out[i].Status = StatusRateLimited
			continue
		}
		wg.Add(1)
		go func(i int, s Source) {
			defer wg.Done()
			rep, err := s.Lookup(ctx, ip)
			out[i].Report, out[i].Status = rep, r.statusOf(s.Key(), err)
		}(i, s)
	}
	wg.Wait()
	return out, nil
}

func (r *Registry) statusOf(key string, err error) string {
	switch {
	case err == nil:
		return StatusOK
	case errors.Is(err, ErrRateLimited):
		r.mu.Lock()
		r.cooldown[key] = r.now().Add(rateLimitCooldown)
		r.mu.Unlock()
		return StatusRateLimited
	case errors.Is(err, ErrAuth):
		return StatusAuthFailed
	default:
		return StatusError
	}
}

func (r *Registry) coolingDown(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	until, ok := r.cooldown[key]
	return ok && r.now().Before(until)
}

// sharedAddressSpace 是运营商级 NAT 的共享地址段（RFC 6598），netip 不把它算作私网。
var sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")

// IsPublic 报告地址是否是可以发给数据源的公网单播地址。
func IsPublic(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !sharedAddressSpace.Contains(ip)
}

func boolPtr(b bool) *bool { return &b }

// MaxLevel 是各源等级里最高的一档；没有任何源给分时为空串。
func MaxLevel(results []SourceResult) Level {
	var best Level
	for _, r := range results {
		if r.Report.Score == nil {
			continue
		}
		if levelRank[r.Report.Score.Level] > levelRank[best] {
			best = r.Report.Score.Level
		}
	}
	return best
}
