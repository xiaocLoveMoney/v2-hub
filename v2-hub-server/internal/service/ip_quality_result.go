package service

import (
	"v2-hub-server/internal/manager/ipquality"
)

// 以下是检测报告的 JSON 形状，与 hub-api 的 IpCheckResult 逐字段对应（docs/03 §5.23）：
// 一律是枚举码；数组非 nil 并按 key 排序（数据源、黑名单、流媒体、邮局各自在 manager 里已排序），前端逐项对比。

type ipCheckResult struct {
	Schema int                     `json:"schema"`
	Stacks map[string]ipCheckStack `json:"stacks"`
}

type ipCheckStack struct {
	IP       string         `json:"ip"`
	IPSource string         `json:"ipSource"`
	Basics   ipBasicsJSON   `json:"basics"`
	Sources  []ipSourceJSON `json:"sources"`
	DNSBL    ipDNSBLJSON    `json:"dnsbl"`
	Egress   ipEgressJSON   `json:"egress"`
	Media    ipMediaJSON    `json:"media"`
	Mail     ipMailJSON     `json:"mail"`
	summary  ipStackSummary // 只给摘要列用，不进报告
}

type ipStackSummary struct {
	usage string
	risk  string
}

type ipBasicsJSON struct {
	Country    string   `json:"country"`
	Region     string   `json:"region"`
	City       string   `json:"city"`
	Timezone   string   `json:"timezone"`
	Latitude   *float64 `json:"latitude"`
	Longitude  *float64 `json:"longitude"`
	ASN        *int64   `json:"asn"`
	ASOrg      string   `json:"asOrg"`
	Prefix     string   `json:"prefix"`
	RegCountry string   `json:"regCountry"`
	Registry   string   `json:"registry"`
	IPKind     string   `json:"ipKind"`
}

type ipScoreJSON struct {
	Value float64 `json:"value"`
	Level string  `json:"level"`
}

type ipFactorsJSON struct {
	Country string `json:"country"`
	Proxy   *bool  `json:"proxy"`
	Tor     *bool  `json:"tor"`
	VPN     *bool  `json:"vpn"`
	Server  *bool  `json:"server"`
	Abuser  *bool  `json:"abuser"`
	Robot   *bool  `json:"robot"`
}

type ipSourceJSON struct {
	Key         string        `json:"key"`
	Status      string        `json:"status"`
	UsageType   string        `json:"usageType"`
	CompanyType string        `json:"companyType"`
	Score       *ipScoreJSON  `json:"score"`
	Factors     ipFactorsJSON `json:"factors"`
}

type ipDNSBLItemJSON struct {
	Key    string `json:"key"`
	Result string `json:"result"`
	Answer string `json:"answer"`
}

type ipDNSBLJSON struct {
	Listed      int               `json:"listed"`
	Policy      int               `json:"policy"`
	Clean       int               `json:"clean"`
	Unavailable int               `json:"unavailable"`
	Items       []ipDNSBLItemJSON `json:"items"`
}

type ipEgressJSON struct {
	IP      string `json:"ip"`
	Country string `json:"country"`
}

type ipMediaItemJSON struct {
	Key      string            `json:"key"`
	Status   string            `json:"status"`
	Region   string            `json:"region"`
	Method   string            `json:"method"`
	Evidence map[string]string `json:"evidence"`
}

type ipMediaJSON struct {
	Status string            `json:"status"`
	Items  []ipMediaItemJSON `json:"items"`
}

type ipMailItemJSON struct {
	Key    string `json:"key"`
	MX     string `json:"mx"`
	Result string `json:"result"`
	Code   string `json:"code"`
}

type ipMailJSON struct {
	Status string           `json:"status"`
	Port25 string           `json:"port25"`
	Items  []ipMailItemJSON `json:"items"`
}

func toBasicsJSON(b ipquality.Basics) ipBasicsJSON {
	return ipBasicsJSON{
		Country: b.Country, Region: b.Region, City: b.City, Timezone: b.Timezone, Latitude: b.Lat, Longitude: b.Lon,
		ASN: b.ASN, ASOrg: b.ASOrg, Prefix: b.Prefix, RegCountry: b.RegCountry, Registry: b.Registry, IPKind: b.IPKind,
	}
}

func toSourcesJSON(results []ipquality.SourceResult) []ipSourceJSON {
	out := make([]ipSourceJSON, 0, len(results))
	for _, r := range results {
		f := r.Report.Factors
		item := ipSourceJSON{
			Key: r.Key, Status: r.Status, UsageType: string(r.Report.UsageType), CompanyType: string(r.Report.CompanyType),
			Factors: ipFactorsJSON{Country: f.Country, Proxy: f.Proxy, Tor: f.Tor, VPN: f.VPN, Server: f.Server, Abuser: f.Abuser, Robot: f.Robot},
		}
		if sc := r.Report.Score; sc != nil {
			item.Score = &ipScoreJSON{Value: sc.Value, Level: string(sc.Level)}
		}
		out = append(out, item)
	}
	return out
}

func toDNSBLJSON(r ipquality.DNSBLReport) ipDNSBLJSON {
	out := ipDNSBLJSON{Listed: r.Listed, Policy: r.Policy, Clean: r.Clean, Unavailable: r.Unavailable, Items: make([]ipDNSBLItemJSON, 0, len(r.Items))}
	for _, it := range r.Items {
		out.Items = append(out.Items, ipDNSBLItemJSON{Key: it.Key, Result: it.Result, Answer: it.Answer})
	}
	return out
}
