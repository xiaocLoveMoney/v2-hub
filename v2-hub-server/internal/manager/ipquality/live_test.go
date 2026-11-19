package ipquality

import (
	"context"
	"net/netip"
	"os"
	"testing"
	"time"
)

// 真实数据源的平台侧查询（docs/03 §5.23）。默认跳过；设 HUB_TEST_IPQ_LIVE=<公网 IPv4> 才跑，
// 数据源 key 取 HUB_IPQ_* 环境变量（没配的源为 not_configured），DNS 解析器取 HUB_TEST_IPQ_DNS（空 = 系统）。
// 只打印结果，不断言取值。
const liveTimeout = 60 * time.Second

func TestLiveLookup(t *testing.T) {
	raw := os.Getenv("HUB_TEST_IPQ_LIVE")
	if raw == "" {
		t.Skip("HUB_TEST_IPQ_LIVE not set; skipping the live lookup test")
	}
	ip, err := netip.ParseAddr(raw)
	if err != nil {
		t.Fatal(err)
	}
	res, err := NewResolver(os.Getenv("HUB_TEST_IPQ_DNS"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewRegistry(Config{Resolver: res, Keys: Keys{
		IP2Location: os.Getenv("HUB_IPQ_IP2LOCATION_KEY"), IPAPIIs: os.Getenv("HUB_IPQ_IPAPIIS_KEY"),
		AbuseIPDB: os.Getenv("HUB_IPQ_ABUSEIPDB_KEY"), IPQS: os.Getenv("HUB_IPQ_IPQS_KEY"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveTimeout)
	defer cancel()
	results, err := reg.Lookup(ctx, ip)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		t.Logf("%-12s %-15s usage=%s company=%s score=%+v factors=%+v", r.Key, r.Status, r.Report.UsageType, r.Report.CompanyType, r.Report.Score, r.Report.Factors)
	}
	t.Logf("basics %+v", BuildBasics(results))
	rep := NewDNSBL(res, nil).Check(ctx, ip)
	t.Logf("dnsbl listed=%d policy=%d clean=%d unavailable=%d", rep.Listed, rep.Policy, rep.Clean, rep.Unavailable)
	for _, it := range rep.Items {
		t.Logf("  %-26s %-11s %s", it.Key, it.Result, it.Answer)
	}
}
