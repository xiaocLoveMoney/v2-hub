package ipquality

import (
	"context"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
)

// DNS 黑名单的结论（docs/03 §5.23 DNS 黑名单）。
const (
	DNSBLClean       = "clean"
	DNSBLListed      = "listed"
	DNSBLPolicy      = "policy"
	DNSBLUnavailable = "unavailable"
)

const (
	dnsblConcurrency  = 8
	dnsblQueryTimeout = 3 * time.Second
	// selfTestTTL 是列表自检结果的缓存时长。
	selfTestTTL = time.Hour
	// rfc5782Listed / rfc5782Clean 是 RFC 5782 的测试地址（反转后）：前者应上榜，后者应不上榜。
	rfc5782Listed = "2.0.0.127"
	rfc5782Clean  = "1.0.0.127"
	answerSep     = ","
)

var (
	loopbackNet = netip.MustParsePrefix("127.0.0.0/8")
	// refusedNet 是列表拒绝查询时的应答段（Spamhaus 对公共解析器返回 127.255.255.x）。
	refusedNet = netip.MustParsePrefix("127.255.255.0/24")
)

// dnsblZone 是一个黑名单：Policy 里的应答算策略类（不算上榜）。
type dnsblZone struct {
	Zone   string
	Policy []string
}

// dnsblZones 是精选的、仍在维护的列表（2026-11 按 RFC 5782 自检核对过）。
var dnsblZones = []dnsblZone{
	{Zone: "zen.spamhaus.org", Policy: []string{"127.0.0.10", "127.0.0.11"}},
	{Zone: "b.barracudacentral.org"},
	{Zone: "bl.spamcop.net"},
	{Zone: "psbl.surriel.com"},
	{Zone: "bl.mailspike.net", Policy: []string{"127.0.0.10", "127.0.0.11", "127.0.0.12", "127.0.0.13", "127.0.0.14"}},
	{Zone: "dnsbl-1.uceprotect.net"},
	{Zone: "all.s5h.net"},
	{Zone: "spam.dnsbl.anonmails.de"},
	{Zone: "bl.blocklist.de"},
	{Zone: "dnsbl.spfbl.net", Policy: []string{"127.0.0.3", "127.0.0.4"}},
	{Zone: "truncate.gbudb.net"},
	{Zone: "dnsbl.justspam.org"},
	{Zone: "bl.0spam.org"},
	{Zone: "rbl.interserver.net"},
	{Zone: "dnsbl.kempt.net"},
	{Zone: "dnsbl.dronebl.org"},
	{Zone: "bl.suomispam.net"},
}

// DNSBLItem 是一个列表的结论；Answer 是列表返回的地址（逗号分隔）。
type DNSBLItem struct {
	Key    string
	Result string
	Answer string
}

// DNSBLReport 是一次黑名单检查的结果，Items 按列表域名排序。
type DNSBLReport struct {
	Listed      int
	Policy      int
	Clean       int
	Unavailable int
	Items       []DNSBLItem
}

// Usable 是可用（不是 unavailable）的列表数。
func (r DNSBLReport) Usable() int { return r.Listed + r.Policy + r.Clean }

// DNSBL 查精选的 DNS 黑名单，并发安全。
type DNSBL struct {
	resolver Resolver
	zones    []dnsblZone
	now      func() time.Time

	mu       sync.Mutex
	selfTest map[string]selfTestEntry
}

type selfTestEntry struct {
	ok bool
	at time.Time
}

// NewDNSBL 用给定的解析器创建；now 为 nil 用 time.Now。
func NewDNSBL(resolver Resolver, now func() time.Time) *DNSBL {
	if now == nil {
		now = time.Now
	}
	return &DNSBL{resolver: resolver, zones: dnsblZones, now: now, selfTest: make(map[string]selfTestEntry, len(dnsblZones))}
}

// Check 查一个 IPv4 地址在各列表里的情况；不是 IPv4 时返回空报告。
func (d *DNSBL) Check(ctx context.Context, ip netip.Addr) DNSBLReport {
	ip = ip.Unmap()
	if !ip.Is4() {
		return DNSBLReport{}
	}
	rev := reverseIPv4(ip)
	items := make([]DNSBLItem, len(d.zones))
	sem := make(chan struct{}, dnsblConcurrency)
	var wg sync.WaitGroup
	for i, z := range d.zones {
		wg.Add(1)
		go func(i int, z dnsblZone) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				items[i] = DNSBLItem{Key: z.Zone, Result: DNSBLUnavailable}
				return
			}
			defer func() { <-sem }()
			items[i] = d.checkZone(ctx, z, rev)
		}(i, z)
	}
	wg.Wait()
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	rep := DNSBLReport{Items: items}
	for _, it := range items {
		switch it.Result {
		case DNSBLListed:
			rep.Listed++
		case DNSBLPolicy:
			rep.Policy++
		case DNSBLClean:
			rep.Clean++
		default:
			rep.Unavailable++
		}
	}
	return rep
}

func (d *DNSBL) checkZone(ctx context.Context, z dnsblZone, rev string) DNSBLItem {
	if !d.zoneHealthy(ctx, z) {
		return DNSBLItem{Key: z.Zone, Result: DNSBLUnavailable}
	}
	result, answers := d.query(ctx, z, rev)
	return DNSBLItem{Key: z.Zone, Result: result, Answer: strings.Join(answers, answerSep)}
}

// query 查一条记录并按列表的返回码归类。
func (d *DNSBL) query(ctx context.Context, z dnsblZone, rev string) (string, []string) {
	qctx, cancel := context.WithTimeout(ctx, dnsblQueryTimeout)
	defer cancel()
	addrs, err := d.resolver.LookupHost(qctx, rev+"."+z.Zone)
	if err != nil {
		if isNotFound(err) {
			return DNSBLClean, nil
		}
		return DNSBLUnavailable, nil
	}
	sort.Strings(addrs)
	allPolicy := len(addrs) > 0
	for _, a := range addrs {
		ip, err := netip.ParseAddr(a)
		if err != nil || !loopbackNet.Contains(ip) || refusedNet.Contains(ip) {
			return DNSBLUnavailable, addrs
		}
		if !contains(z.Policy, a) {
			allPolicy = false
		}
	}
	if len(addrs) == 0 {
		return DNSBLClean, nil
	}
	if allPolicy {
		return DNSBLPolicy, addrs
	}
	return DNSBLListed, addrs
}

// zoneHealthy 按 RFC 5782 自检：测试地址 127.0.0.2 应上榜、127.0.0.1 应不上榜；结果缓存一小时。
func (d *DNSBL) zoneHealthy(ctx context.Context, z dnsblZone) bool {
	d.mu.Lock()
	e, ok := d.selfTest[z.Zone]
	d.mu.Unlock()
	if ok && d.now().Sub(e.at) < selfTestTTL {
		return e.ok
	}
	listed, _ := d.query(ctx, z, rfc5782Listed)
	clean, _ := d.query(ctx, z, rfc5782Clean)
	healthy := (listed == DNSBLListed || listed == DNSBLPolicy) && clean == DNSBLClean
	d.mu.Lock()
	d.selfTest[z.Zone] = selfTestEntry{ok: healthy, at: d.now()}
	d.mu.Unlock()
	return healthy
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
