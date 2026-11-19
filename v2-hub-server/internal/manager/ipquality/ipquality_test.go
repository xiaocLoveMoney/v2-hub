package ipquality

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testKey = "sekret-key-123"
	testIP  = "203.0.113.10"
)

// fakeResolver 按表应答 A / TXT 查询；不在表里的一律 NXDOMAIN。
type fakeResolver struct {
	hosts map[string][]string
	txt   map[string][]string
	fail  map[string]bool
}

func notFound(name string) error {
	return &net.DNSError{Name: name, Err: "no such host", IsNotFound: true}
}

func (f fakeResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	if f.fail[host] {
		return nil, &net.DNSError{Name: host, Err: "server misbehaving", IsTemporary: true}
	}
	if v, ok := f.hosts[host]; ok {
		return v, nil
	}
	return nil, notFound(host)
}

func (f fakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if v, ok := f.txt[name]; ok {
		return v, nil
	}
	return nil, notFound(name)
}

func (f fakeResolver) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	return nil, notFound(name)
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// sourceServer 模拟各 HTTP 源：路径前缀是源键（同联调的 BaseURL 覆盖）。
func sourceServer(t *testing.T, ip2lFixture string, hits *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/ip2location/"):
			_, _ = w.Write([]byte(readFixture(t, ip2lFixture)))
		case strings.HasPrefix(r.URL.Path, "/ipapiis/"):
			if r.URL.Query().Get("key") != testKey {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(readFixture(t, "ipapiis.json")))
		case r.URL.Path == "/abuseipdb/api/v2/check":
			if r.Header.Get("Key") != testKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(readFixture(t, "abuseipdb.json")))
		case r.URL.Path == "/ipqs/api/json/ip/"+testKey+"/"+testIP:
			_, _ = w.Write([]byte(readFixture(t, "ipqs.json")))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testResolver() fakeResolver {
	return fakeResolver{
		txt: map[string][]string{
			"10.113.0.203.origin.asn.cymru.com": {"64500 | 203.0.113.0/24 | US | arin | 2020-01-01"},
			"AS64500.asn.cymru.com":             {"64500 | US | arin | 2001-01-01 | EXAMPLE - Example Hosting, US"},
		},
	}
}

func allKeys() Keys { return Keys{IPAPIIs: testKey, AbuseIPDB: testKey, IPQS: testKey} }

func TestRegistryLookupAllSources(t *testing.T) {
	srv := sourceServer(t, "ip2location_security.json", nil)
	reg, err := NewRegistry(Config{Keys: allKeys(), BaseURL: srv.URL, Resolver: testResolver()})
	if err != nil {
		t.Fatal(err)
	}
	results, err := reg.Lookup(context.Background(), netip.MustParseAddr(testIP))
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]SourceResult{}
	for _, r := range results {
		by[r.Key] = r
		if r.Status != StatusOK {
			t.Errorf("%s status = %s", r.Key, r.Status)
		}
	}
	if results[0].Key != SourceAbuseIPDB || len(results) != 6 {
		t.Errorf("results not sorted/complete: %+v", results)
	}
	if s := by[SourceIPAPIIs].Report; s.UsageType != NetHosting || s.Score.Level != LevelMedium || s.Score.Value != 1.27 ||
		*s.Factors.VPN != true || *s.Factors.Abuser != true || *s.Factors.Robot != false || s.Factors.Country != "SG" {
		t.Errorf("ipapiis = %+v score=%+v", s, s.Score)
	}
	if s := by[SourceAbuseIPDB].Report; s.UsageType != NetHosting || s.Score.Level != LevelHigh || *s.Factors.Tor {
		t.Errorf("abuseipdb = %+v", s)
	}
	if s := by[SourceIPQS].Report; s.UsageType != NetHosting || s.Score.Level != LevelHigh || !*s.Factors.Server || !*s.Factors.VPN {
		t.Errorf("ipqs = %+v", s)
	}
	if s := by[SourceIP2Location].Report; s.Score.Level != LevelMedium || !*s.Factors.Abuser || !*s.Factors.Server || s.CompanyType != NetHosting {
		t.Errorf("ip2location = %+v", s)
	}
	if s := by[SourceTor].Report; s.Factors.Tor == nil || *s.Factors.Tor {
		t.Errorf("tor = %+v", s)
	}
	b := BuildBasics(results)
	if b.Country != "SG" || b.RegCountry != "US" || b.IPKind != IPKindBroadcast || *b.ASN != 64500 || b.ASOrg != "EXAMPLE - Example Hosting, US" || b.Prefix != "203.0.113.0/24" {
		t.Errorf("basics = %+v", b)
	}
	if SummaryUsage(results) != NetHosting || MaxLevel(results) != LevelHigh {
		t.Errorf("summary usage/level = %s/%s", SummaryUsage(results), MaxLevel(results))
	}
}

func TestRegistryWithoutKeys(t *testing.T) {
	var hits int32
	srv := sourceServer(t, "ip2location_free.json", &hits)
	reg, _ := NewRegistry(Config{BaseURL: srv.URL, Resolver: testResolver()})
	results, err := reg.Lookup(context.Background(), netip.MustParseAddr(testIP))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		SourceIP2Location: StatusOK, SourceCymru: StatusOK, SourceTor: StatusOK,
		SourceIPAPIIs: StatusNotConfigured, SourceAbuseIPDB: StatusNotConfigured, SourceIPQS: StatusNotConfigured,
	}
	for _, r := range results {
		if r.Status != want[r.Key] {
			t.Errorf("%s = %s, want %s", r.Key, r.Status, want[r.Key])
		}
	}
	if hits != 1 {
		t.Errorf("only ip2location should be requested, hits = %d", hits)
	}
	// 免 key 的 IP2Location.io 只有代理标记、没有分数
	b := BuildBasics(results)
	if b.Country != "US" || b.IPKind != IPKindNative || MaxLevel(results) != "" {
		t.Errorf("basics = %+v level=%s", b, MaxLevel(results))
	}
	for _, c := range reg.Catalog() {
		if c.Key == SourceIPQS && (c.Configured || !c.NeedsKey) {
			t.Errorf("catalog ipqs = %+v", c)
		}
	}
}

func TestRegistryRejectsNonPublic(t *testing.T) {
	reg, _ := NewRegistry(Config{Resolver: testResolver()})
	for _, ip := range []string{"10.0.0.1", "100.64.0.1", "127.0.0.1", "192.168.1.1"} {
		if _, err := reg.Lookup(context.Background(), netip.MustParseAddr(ip)); !errors.Is(err, ErrNotPublic) {
			t.Errorf("%s: err = %v", ip, err)
		}
	}
}

func TestErrorMappingAndCooldown(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusTooManyRequests)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	reg, _ := NewRegistry(Config{Keys: Keys{IPQS: testKey}, BaseURL: srv.URL, Resolver: testResolver(), Now: clock})
	statusOf := func() string {
		results, err := reg.Lookup(context.Background(), netip.MustParseAddr(testIP))
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range results {
			if r.Key == SourceIPQS {
				return r.Status
			}
		}
		return ""
	}
	if s := statusOf(); s != StatusRateLimited {
		t.Fatalf("429 → %s", s)
	}
	before := atomic.LoadInt32(&hits)
	if s := statusOf(); s != StatusRateLimited || atomic.LoadInt32(&hits) != before {
		// 冷却期间不再请求被限流的源（IPQS 与 IP2Location 都收到了 429）
		t.Fatalf("cooldown: status %s, new hits %d", s, atomic.LoadInt32(&hits)-before)
	}
	now = now.Add(rateLimitCooldown + time.Minute)
	status.Store(http.StatusUnauthorized)
	if s := statusOf(); s != StatusAuthFailed {
		t.Fatalf("401 → %s", s)
	}
	status.Store(http.StatusBadGateway)
	if s := statusOf(); s != StatusError {
		t.Fatalf("502 → %s", s)
	}
}

func TestFetchErrorHidesKey(t *testing.T) {
	// 连不上的地址：错误里不能出现带 key 的 URL
	s := &ipqs{client: &http.Client{Timeout: time.Second}, base: "http://127.0.0.1:1", key: testKey}
	_, err := s.Lookup(context.Background(), netip.MustParseAddr(testIP))
	if err == nil || strings.Contains(err.Error(), testKey) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestIPQSSuccessFalse(t *testing.T) {
	body := `{"success":false,"message":"You have insufficient credits to make this query."}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer srv.Close()
	s := &ipqs{client: srv.Client(), base: srv.URL, key: testKey}
	if _, err := s.Lookup(context.Background(), netip.MustParseAddr(testIP)); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("credits: %v", err)
	}
	body = `{"success":false,"message":"Invalid or unauthorized key."}`
	if _, err := s.Lookup(context.Background(), netip.MustParseAddr(testIP)); !errors.Is(err, ErrAuth) {
		t.Fatalf("bad key: %v", err)
	}
}

func TestLevels(t *testing.T) {
	cases := []struct {
		got, want Level
	}{
		{abuseIPDBLevel(24), LevelLow}, {abuseIPDBLevel(25), LevelHigh}, {abuseIPDBLevel(75), LevelVeryHigh},
		{ipqsLevel(74.9), LevelLow}, {ipqsLevel(75), LevelMedium}, {ipqsLevel(85), LevelHigh}, {ipqsLevel(90), LevelVeryHigh},
		{ip2locationLevel(32), LevelLow}, {ip2locationLevel(33), LevelMedium}, {ip2locationLevel(66), LevelHigh},
	}
	for i, c := range cases {
		if c.got != c.want {
			t.Errorf("case %d: %s, want %s", i, c.got, c.want)
		}
	}
	if s := ipapiIsScore("0.0039 (Very Low)"); s == nil || s.Level != LevelLow || s.Value != 0.39 {
		t.Errorf("very low = %+v", s)
	}
	if ipapiIsScore("garbage") != nil || ipapiIsScore("0.5 (Weird)") != nil {
		t.Error("bad abuser_score must be nil")
	}
	if ip2locationUsage("ISP/MOB") != NetResidential || ip2locationUsage("XYZ") != NetUnknown || ip2locationUsage("") != "" {
		t.Error("ip2location usage mapping")
	}
}

func TestTorExit(t *testing.T) {
	res := fakeResolver{hosts: map[string][]string{"4.100.70.109.dnsel.torproject.org": {"127.0.0.2"}}, fail: map[string]bool{"5.5.5.5.dnsel.torproject.org": true}}
	s := &torExit{resolver: res}
	rep, err := s.Lookup(context.Background(), netip.MustParseAddr("109.70.100.4"))
	if err != nil || rep.Factors.Tor == nil || !*rep.Factors.Tor {
		t.Errorf("exit: %+v %v", rep, err)
	}
	if _, err := s.Lookup(context.Background(), netip.MustParseAddr("5.5.5.5")); !errors.Is(err, ErrUnavailable) {
		t.Errorf("servfail: %v", err)
	}
}

func TestDNSBL(t *testing.T) {
	hosts := map[string][]string{}
	fail := map[string]bool{}
	for _, z := range dnsblZones {
		hosts[rfc5782Listed+"."+z.Zone] = []string{"127.0.0.2"}
	}
	rev := "10.113.0.203."
	hosts[rev+"bl.spamcop.net"] = []string{"127.0.0.2"}
	hosts[rev+"zen.spamhaus.org"] = []string{"127.0.0.10"}
	// Spamhaus 经公共解析器被拒的样子
	hosts[rfc5782Listed+".bl.mailspike.net"] = []string{"127.255.255.254"}
	// 解析器劫持 NXDOMAIN
	hosts[rev+"psbl.surriel.com"] = []string{"198.51.100.1"}
	// 自检过、查询出错
	fail[rev+"all.s5h.net"] = true
	// 自检不过：测试地址 127.0.0.1 也上榜
	hosts[rfc5782Clean+".bl.0spam.org"] = []string{"127.0.0.2"}
	d := NewDNSBL(fakeResolver{hosts: hosts, fail: fail}, nil)
	rep := d.Check(context.Background(), netip.MustParseAddr(testIP))
	got := map[string]string{}
	for _, it := range rep.Items {
		got[it.Key] = it.Result
	}
	want := map[string]string{
		"bl.spamcop.net": DNSBLListed, "zen.spamhaus.org": DNSBLPolicy, "bl.mailspike.net": DNSBLUnavailable,
		"psbl.surriel.com": DNSBLUnavailable, "all.s5h.net": DNSBLUnavailable, "bl.0spam.org": DNSBLUnavailable,
		"bl.blocklist.de": DNSBLClean,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %s, want %s", k, got[k], v)
		}
	}
	if rep.Listed != 1 || rep.Policy != 1 || rep.Unavailable != 4 || rep.Usable() != len(dnsblZones)-4 {
		t.Errorf("counts = %+v", rep)
	}
	if rep.Items[0].Key > rep.Items[1].Key {
		t.Error("items not sorted")
	}
	if r := d.Check(context.Background(), netip.MustParseAddr("2001:db8::1")); len(r.Items) != 0 {
		t.Error("ipv6 must be skipped")
	}
}
