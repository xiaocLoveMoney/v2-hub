package ipquality

// IP 类型（docs/03 §5.23 基础信息）。
const (
	IPKindNative    = "native"
	IPKindBroadcast = "broadcast"
	IPKindUnknown   = "unknown"
)

// usagePriority 是摘要里使用类型的取用顺序。
var usagePriority = []string{SourceIPAPIIs, SourceIP2Location, SourceAbuseIPDB, SourceIPQS}

// Basics 是合并各源后的基础信息。
type Basics struct {
	Country    string
	Region     string
	City       string
	Timezone   string
	Lat, Lon   *float64
	ASN        *int64
	ASOrg      string
	Prefix     string
	RegCountry string
	Registry   string
	IPKind     string
}

// BuildBasics 合并基础信息：使用地取 IP2Location.io，网络取 Team Cymru（没有时退回 IP2Location.io 的 ASN 与组织）；
// 使用地与注册地国家都有且相同为原生，不同为广播，缺一为 unknown。
func BuildBasics(results []SourceResult) Basics {
	by := byKey(results)
	var b Basics
	if g := by[SourceIP2Location].Report.Geo; g != nil {
		b.Country, b.Region, b.City, b.Timezone, b.Lat, b.Lon = g.Country, g.Region, g.City, g.Timezone, g.Lat, g.Lon
	}
	var nw *Network
	alt := by[SourceIP2Location].Report.Network
	if c := by[SourceCymru].Report.Network; c != nil {
		merged := *c
		if merged.Org == "" && alt != nil && alt.ASN == merged.ASN {
			merged.Org = alt.Org
		}
		nw = &merged
	} else {
		nw = alt
	}
	if nw != nil {
		asn := nw.ASN
		b.ASN, b.ASOrg, b.Prefix, b.RegCountry, b.Registry = &asn, nw.Org, nw.Prefix, nw.RegCountry, nw.Registry
	}
	switch {
	case b.Country == "" || b.RegCountry == "":
		b.IPKind = IPKindUnknown
	case b.Country == b.RegCountry:
		b.IPKind = IPKindNative
	default:
		b.IPKind = IPKindBroadcast
	}
	return b
}

// SummaryUsage 取第一个给出使用类型的源（ipapi.is → IP2Location.io → AbuseIPDB → IPQS）。
func SummaryUsage(results []SourceResult) NetType {
	by := byKey(results)
	for _, k := range usagePriority {
		if t := by[k].Report.UsageType; t != "" {
			return t
		}
	}
	return ""
}

func byKey(results []SourceResult) map[string]SourceResult {
	out := make(map[string]SourceResult, len(results))
	for _, r := range results {
		if r.Status == StatusOK {
			out[r.Key] = r
		}
	}
	return out
}
