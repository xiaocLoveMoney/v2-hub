package service

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"v2-hub-server/internal/manager/render"
)

// 用户出口用于服务端链式的防护（docs/03 §5.19，Ruling-48、Ruling-49）；域名解析走假实现，不发真实查询。

// fakeResolver 按主机名返回预设的地址；没有预设的主机名解析失败。
type fakeResolver map[string][]string

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	raw, ok := f[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	out := make([]netip.Addr, 0, len(raw))
	for _, s := range raw {
		out = append(out, netip.MustParseAddr(s))
	}
	return out, nil
}

// 示例参数：全部是按格式拼出来的示例值（顺序字节的 Base64），不对应任何真实节点。
const (
	guardUUID     = "11111111-1111-4111-8111-111111111111"
	guardKey32    = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	guardKey16    = "AAECAwQFBgcICQoLDA0ODw=="
	guardPublicV4 = "93.184.216.34"
	guardPublicV6 = "2606:2800:220:1:248:1893:25c8:1946"
)

// withKey 返回换了口令字段的节点。
func withKey(n render.ProxyNode, key string) render.ProxyNode {
	n.Password = key
	return n
}

func TestIsPublicAddr(t *testing.T) {
	cases := map[string]bool{
		guardPublicV4: true, guardPublicV6: true, "1.1.1.1": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.1": false, "169.254.169.254": false,
		"100.64.0.1": false, "0.0.0.0": false, "224.0.0.1": false, "198.18.0.1": false, "192.0.2.1": false, "240.0.0.1": false,
		"255.255.255.255": false, "::1": false, "::": false, "fe80::1": false, "fc00::1": false, "fd12:3456::1": false, "ff02::1": false,
		"2001:db8::1": false, "::ffff:127.0.0.1": false, "::ffff:10.0.0.1": false, "64:ff9b::7f00:1": false, "2002:7f00:1::": false,
		"::ffff:93.184.216.34": true,
	}
	for raw, want := range cases {
		if got := isPublicAddr(netip.MustParseAddr(raw)); got != want {
			t.Errorf("isPublicAddr(%s) = %v, want %v", raw, got, want)
		}
	}
	if isPublicAddr(netip.Addr{}) {
		t.Error("zero address must not be public")
	}
}

func TestValidateExitFields(t *testing.T) {
	vless := render.ProxyNode{
		Protocol: render.ProtoVLESS, Address: "exit.example.net", Port: 443, UUID: guardUUID, Network: render.NetTCP,
		Security: render.SecReality, SNI: "www.example.com", Fingerprint: "chrome", PublicKey: guardKey32, ShortID: "0123abcd", Flow: "xtls-rprx-vision",
	}
	ss := withKey(render.ProxyNode{Protocol: render.ProtoShadowsocks, Address: "1.1.1.1", Port: 8388, Method: "2022-blake3-aes-128-gcm", Network: render.NetTCP}, guardKey16)
	socks := render.ProxyNode{Protocol: render.ProtoSocks, Address: "1.1.1.1", Port: 1080, Network: render.NetTCP}
	trojan := render.ProxyNode{Protocol: render.ProtoTrojan, Address: "1.1.1.1", Port: 443, Network: render.NetTCP}
	for what, n := range map[string]render.ProxyNode{"vless reality": vless, "ss 2022": ss, "socks": socks, "trojan": withKey(trojan, "x")} {
		if err := validateExitFields(n); err != nil {
			t.Errorf("%s should pass: %v", what, err)
		}
	}
	with := func(base render.ProxyNode, mutate func(n *render.ProxyNode)) render.ProxyNode {
		mutate(&base)
		return base
	}
	bad := map[string]render.ProxyNode{
		"reality key too short":  with(vless, func(n *render.ProxyNode) { n.PublicKey = "AAEC" }),
		"reality key garbage":    with(vless, func(n *render.ProxyNode) { n.PublicKey = "not base64 !!" }),
		"short id odd length":    with(vless, func(n *render.ProxyNode) { n.ShortID = "abc" }),
		"short id not hex":       with(vless, func(n *render.ProxyNode) { n.ShortID = "zz" }),
		"short id too long":      with(vless, func(n *render.ProxyNode) { n.ShortID = "0123456789abcdef00" }),
		"reality without sni":    with(vless, func(n *render.ProxyNode) { n.SNI = "" }),
		"unknown flow":           with(vless, func(n *render.ProxyNode) { n.Flow = "xtls-rprx-direct" }),
		"unknown fingerprint":    with(vless, func(n *render.ProxyNode) { n.Fingerprint = "netscape" }),
		"unknown security":       with(vless, func(n *render.ProxyNode) { n.Security = "xtls" }),
		"unknown alpn":           with(vless, func(n *render.ProxyNode) { n.ALPN = []string{"spdy/3"} }),
		"empty user id":          with(vless, func(n *render.ProxyNode) { n.UUID = "" }),
		"user id with newline":   with(vless, func(n *render.ProxyNode) { n.UUID = "a\nb" }),
		"sni with space":         with(vless, func(n *render.ProxyNode) { n.SNI = "a b.example.com" }),
		"path with control char": with(vless, func(n *render.ProxyNode) { n.Path = "/a\x00b" }),
		"unsupported network":    with(vless, func(n *render.ProxyNode) { n.Network = "kcp" }),
		"port out of range":      with(vless, func(n *render.ProxyNode) { n.Port = 70000 }),
		"ss unknown method":      with(ss, func(n *render.ProxyNode) { n.Method = "rc4-md5" }),
		"ss 2022 key wrong size": withKey(ss, guardKey32),
		"ss empty key":           withKey(with(ss, func(n *render.ProxyNode) { n.Method = "aes-256-gcm" }), ""),
		"vmess alter id":         {Protocol: render.ProtoVMess, Address: "1.1.1.1", Port: 443, UUID: guardUUID, Network: render.NetTCP, AlterID: 64},
		"hysteria2":              withKey(render.ProxyNode{Protocol: render.ProtoHysteria2, Address: "1.1.1.1", Port: 443, Network: render.NetTCP}, "x"),
		"trojan empty key":       trojan,
	}
	for what, n := range bad {
		if err := validateExitFields(n); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%s should be rejected, err = %v", what, err)
		}
	}
}

func TestChainGuardPin(t *testing.T) {
	g := newChainGuard(fakeResolver{
		"exit.example.net":    {guardPublicV6, guardPublicV4},
		"v6only.example.net":  {guardPublicV6},
		"inner.example.net":   {"10.0.0.8"},
		"mixed.example.net":   {guardPublicV4, "127.0.0.1"},
		"mapped.example.net":  {"::ffff:192.168.1.1"},
		"rebound.example.net": {"1.0.0.1"},
	})
	ctx := context.Background()
	mustPin := func(host string, previous *netip.Addr) netip.Addr {
		t.Helper()
		ip, err := g.pin(ctx, host, previous)
		if err != nil {
			t.Fatalf("pin(%s): %v", host, err)
		}
		return ip
	}
	if ip := mustPin(guardPublicV4, nil); ip.String() != guardPublicV4 {
		t.Fatalf("ip literal = %s", ip)
	}
	// 域名：优先取 IPv4；之前固定的地址仍在结果里就沿用
	if ip := mustPin("exit.example.net", nil); ip.String() != guardPublicV4 {
		t.Fatalf("should prefer IPv4: %s", ip)
	}
	previous := netip.MustParseAddr(guardPublicV6)
	if ip := mustPin("exit.example.net", &previous); ip != previous {
		t.Fatalf("should keep the previous pin: %s", ip)
	}
	if ip := mustPin("v6only.example.net", nil); ip.String() != guardPublicV6 {
		t.Fatalf("v6 only = %s", ip)
	}
	// 之前固定的地址不在结果里了才换
	stale := netip.MustParseAddr("8.8.8.8")
	if ip := mustPin("rebound.example.net", &stale); ip.String() != "1.0.0.1" {
		t.Fatalf("should move to the new address: %s", ip)
	}
	for _, host := range []string{
		"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "::ffff:127.0.0.1", "inner.example.net", "mixed.example.net", "mapped.example.net",
		"bad host", "under_score.example.net",
	} {
		if _, err := g.pin(ctx, host, nil); !errors.Is(err, errExitPrivate) {
			t.Errorf("pin(%s) should be rejected as non-public, err = %v", host, err)
		}
	}
	// 解析不了是暂时性的，与“非公网”区分开；数字形式的主机名按域名处理，不会被当成 IP
	for _, host := range []string{"missing.example.net", "2130706433", "localhost"} {
		if _, err := g.pin(ctx, host, nil); !errors.Is(err, errExitResolve) {
			t.Errorf("pin(%s) should fail to resolve, err = %v", host, err)
		}
	}
}

func TestChainGuardPlatformAddrs(t *testing.T) {
	g := newChainGuard(fakeResolver{"panel.example.net": {"203.0.114.7"}, "node.example.net": {"203.0.114.8", guardPublicV6}})
	addrs := g.platformAddrs(context.Background(), []string{
		"https://panel.example.net:2053/some-path/", "node.example.net", "198.51.100.9", "[2001:db8::9]", "http://unknown.example.net", " ",
	})
	for _, want := range []string{"203.0.114.7", "203.0.114.8", guardPublicV6, "198.51.100.9", "2001:db8::9"} {
		if !addrs[netip.MustParseAddr(want)] {
			t.Errorf("platform addrs should contain %s: %v", want, addrs)
		}
	}
	if len(addrs) != 5 {
		t.Fatalf("unexpected platform addrs: %v", addrs)
	}
}

func TestPinnedExit(t *testing.T) {
	ip := netip.MustParseAddr(guardPublicV4)
	tls := render.ProxyNode{Protocol: render.ProtoTrojan, Address: "exit.example.net", Port: 443, Network: render.NetWS, Security: render.SecTLS}
	got := pinnedExit(tls, ip)
	if got.Address != guardPublicV4 || got.SNI != "exit.example.net" || got.Host != "exit.example.net" {
		t.Fatalf("domain should move to SNI and Host: %+v", got)
	}
	// 已有的 SNI / Host 不覆盖；IP 字面量不补
	tls.SNI, tls.Host = "cdn.example.com", "ws.example.com"
	if got := pinnedExit(tls, ip); got.SNI != "cdn.example.com" || got.Host != "ws.example.com" {
		t.Fatalf("explicit SNI / Host must be kept: %+v", got)
	}
	literal := render.ProxyNode{Protocol: render.ProtoTrojan, Address: guardPublicV4, Port: 443, Network: render.NetWS, Security: render.SecTLS}
	if got := pinnedExit(literal, ip); got.SNI != "" || got.Host != "" || got.Address != guardPublicV4 {
		t.Fatalf("ip literal should stay as is: %+v", got)
	}
}
