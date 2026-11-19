package ipquality

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

const (
	cymruOriginZone = "origin.asn.cymru.com"
	cymruASNZone    = "asn.cymru.com"
	torExitZone     = "dnsel.torproject.org"
	// cymruFieldSep 是 Cymru TXT 记录的字段分隔符。
	cymruFieldSep    = "|"
	cymruOriginLen   = 3
	cymruASNNameIdx  = 4
	dnsDialTimeout   = 3 * time.Second
	networkUDP       = "udp"
	cymruRegistryIdx = 3
)

// torExitAnswer 是 Tor 出口查询“是出口节点”的应答。
var torExitAnswer = netip.MustParseAddr("127.0.0.2")

// Resolver 是平台侧的 DNS 查询能力（*net.Resolver 满足）。
type Resolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
}

// NewResolver 返回 DNS 解析器：addr 为空用系统解析器，否则（host:port）一律问这台 DNS 服务器
// （平台机最好用自己的递归解析器；联调指向模拟端）。
func NewResolver(addr string) (*net.Resolver, error) {
	if addr == "" {
		return net.DefaultResolver, nil
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return nil, fmt.Errorf("ipquality: dns resolver %q: %w", addr, err)
	}
	dialer := &net.Dialer{Timeout: dnsDialTimeout}
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, networkUDP, addr)
	}}, nil
}

// reverseIPv4 把 1.2.3.4 变成 4.3.2.1，用于 DNS 反查类的服务。
func reverseIPv4(ip netip.Addr) string {
	b := ip.As4()
	return fmt.Sprintf("%d.%d.%d.%d", b[3], b[2], b[1], b[0])
}

func isNotFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

// ---- Team Cymru（ASN 与注册地） ----

type cymru struct{ resolver Resolver }

func (s *cymru) Key() string      { return SourceCymru }
func (s *cymru) NeedsKey() bool   { return false }
func (s *cymru) Configured() bool { return true }

func (s *cymru) Lookup(ctx context.Context, ip netip.Addr) (Report, error) {
	if !ip.Is4() {
		return Report{}, fmt.Errorf("%w: only ipv4", ErrUnavailable)
	}
	txts, err := s.resolver.LookupTXT(ctx, reverseIPv4(ip)+"."+cymruOriginZone)
	if err != nil {
		if isNotFound(err) {
			return Report{}, nil // 没有路由信息不算错误
		}
		return Report{}, fmt.Errorf("%w: origin lookup: %v", ErrUnavailable, err)
	}
	// 形如 "15169 | 8.8.8.0/24 | US | arin | 2023-12-28"；有多条时取第一条
	for _, txt := range txts {
		fields := splitCymru(txt)
		if len(fields) < cymruOriginLen {
			continue
		}
		asn, err := strconv.ParseInt(strings.Fields(fields[0])[0], 10, 64)
		if err != nil {
			continue
		}
		nw := &Network{ASN: asn, Prefix: fields[1], RegCountry: upper(fields[2])}
		if len(fields) > cymruRegistryIdx {
			nw.Registry = fields[cymruRegistryIdx]
		}
		nw.Org = s.asName(ctx, asn)
		return Report{Network: nw, Factors: Factors{Country: nw.RegCountry}}, nil
	}
	return Report{}, nil
}

// asName 查 AS 名称（“GOOGLE - Google LLC, US”），查不到为空串。
func (s *cymru) asName(ctx context.Context, asn int64) string {
	txts, err := s.resolver.LookupTXT(ctx, "AS"+strconv.FormatInt(asn, 10)+"."+cymruASNZone)
	if err != nil || len(txts) == 0 {
		return ""
	}
	fields := splitCymru(txts[0])
	if len(fields) <= cymruASNNameIdx {
		return ""
	}
	return fields[cymruASNNameIdx]
}

func splitCymru(txt string) []string {
	parts := strings.Split(strings.Trim(txt, `"`), cymruFieldSep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	if len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		return nil
	}
	return out
}

// ---- Tor 出口查询 ----

type torExit struct{ resolver Resolver }

func (s *torExit) Key() string      { return SourceTor }
func (s *torExit) NeedsKey() bool   { return false }
func (s *torExit) Configured() bool { return true }

func (s *torExit) Lookup(ctx context.Context, ip netip.Addr) (Report, error) {
	if !ip.Is4() {
		return Report{}, fmt.Errorf("%w: only ipv4", ErrUnavailable)
	}
	addrs, err := s.resolver.LookupHost(ctx, reverseIPv4(ip)+"."+torExitZone)
	if err != nil {
		if isNotFound(err) {
			return Report{Factors: Factors{Tor: boolPtr(false)}}, nil
		}
		return Report{}, fmt.Errorf("%w: tor lookup: %v", ErrUnavailable, err)
	}
	for _, a := range addrs {
		if ip, err := netip.ParseAddr(a); err == nil && ip == torExitAnswer {
			return Report{Factors: Factors{Tor: boolPtr(true)}}, nil
		}
	}
	return Report{Factors: Factors{Tor: boolPtr(false)}}, nil
}
