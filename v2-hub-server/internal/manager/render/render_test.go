package render

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

var update = flag.Bool("update", false, "rewrite golden files")

// 测试链接里的标识与密码均为示例值，不对应任何真实节点。
const (
	sampleUUID = "11111111-1111-4111-8111-111111111111"
	sampleAuth = "sample-auth-0001"
	// 以下几项是随手生成的示例密钥（对应私钥已丢弃），只为让样例通过客户端的格式校验。
	samplePubKey = "l2ERKxSso8OaBMBPYhAEJJBPEoj4bnAgH3lSxrTqY3k"
	sampleSSKey  = "Saj028XpviCiL4sprc+nIw==:8j1gg9VlzeACK3QYDnD8WQ=="
	linkReality  = "vless://" + sampleUUID + "@hk.example.com:443?type=tcp&encryption=none&security=reality&pbk=" + samplePubKey + "&fp=chrome&sni=www.example.org&sid=6ba85179e30d4fc2&spx=%2F&flow=xtls-rprx-vision#hk-reality-u1"
	linkGRPC     = "vless://" + sampleUUID + "@jp.example.com:8443?type=grpc&serviceName=grpc-svc&security=tls&sni=jp.example.com&alpn=h2%2Chttp%2F1.1&fp=firefox#jp-grpc-u1"
	linkTrojan   = "trojan://" + sampleAuth + "@us.example.com:443?type=ws&path=%2Fws&host=cdn.example.com&security=tls&sni=cdn.example.com#us-trojan-u1"
	linkSS       = "ss://MjAyMi1ibGFrZTMtYWVzLTEyOC1nY206U2FqMDI4WHB2aUNpTDRzcHJjK25Jdz09OjhqMWdnOVZsemVBQ0szUVlEbkQ4V1E9PQ@sg.example.com:8388?type=tcp#sg-ss-u1"
	linkHy2      = "hysteria2://" + sampleAuth + "@tw.example.com:8443?sni=tw.example.com&insecure=1&obfs=salamander&obfs-password=obfs-sample#tw-hy2-u1"
	linkUpgrade  = "vless://" + sampleUUID + "@de.example.com:443?type=httpupgrade&path=%2Fup&host=de.example.com&security=tls&sni=de.example.com#de-upgrade-u1"
	linkWireGuar = "wireguard://privkey@wg.example.com:51820#wg"
)

// linkVMess 取自 3x-ui v3.11.0 实际生成的形状（port 为数字）。
func linkVMess(t *testing.T) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"add": "localhost", "host": "", "id": sampleUUID, "net": "ws", "path": "/it4", "port": 24601,
		"ps": "it4-ws-u1", "scy": "auto", "tls": "none", "type": "none", "v": "2",
	})
	if err != nil {
		t.Fatalf("marshal vmess: %v", err)
	}
	return "vmess://" + base64.StdEncoding.EncodeToString(raw)
}

func sampleNodes(t *testing.T) []ProxyNode {
	t.Helper()
	links := []string{linkReality, linkVMess(t), linkTrojan, linkSS, linkHy2, linkGRPC, linkUpgrade}
	nodes := make([]ProxyNode, 0, len(links)+1)
	nodes = append(nodes, InfoNode("剩余流量：12.50 GB"))
	for _, l := range links {
		n, err := ParseLink(l)
		if err != nil {
			t.Fatalf("ParseLink(%.24s…): %v", l, err)
		}
		nodes = append(nodes, n)
	}
	return nodes
}

func TestParseLink(t *testing.T) {
	cases := []struct {
		name string
		link string
		want ProxyNode
	}{
		{"vless reality", linkReality, ProxyNode{
			Name: "hk-reality-u1", Protocol: ProtoVLESS, Address: "hk.example.com", Port: 443, UUID: sampleUUID, Flow: "xtls-rprx-vision",
			Network: NetTCP, Security: SecReality, SNI: "www.example.org", Fingerprint: "chrome", PublicKey: samplePubKey, ShortID: "6ba85179e30d4fc2", SpiderX: "/",
		}},
		{"vmess ws", linkVMess(t), ProxyNode{
			Name: "it4-ws-u1", Protocol: ProtoVMess, Address: "localhost", Port: 24601, UUID: sampleUUID, Method: "auto",
			Network: NetWS, Path: "/it4", HeaderType: "none", Security: SecNone,
		}},
		{"trojan ws tls", linkTrojan, ProxyNode{
			Name: "us-trojan-u1", Protocol: ProtoTrojan, Address: "us.example.com", Port: 443, Password: sampleAuth,
			Network: NetWS, Host: "cdn.example.com", Path: "/ws", Security: SecTLS, SNI: "cdn.example.com",
		}},
		{"shadowsocks 2022", linkSS, ProxyNode{
			Name: "sg-ss-u1", Protocol: ProtoShadowsocks, Address: "sg.example.com", Port: 8388,
			Method: "2022-blake3-aes-128-gcm", Password: sampleSSKey, Network: NetTCP, Security: SecNone,
		}},
		{"hysteria2", linkHy2, ProxyNode{
			Name: "tw-hy2-u1", Protocol: ProtoHysteria2, Address: "tw.example.com", Port: 8443, Password: sampleAuth,
			Network: NetTCP, Security: SecTLS, SNI: "tw.example.com", AllowInsecure: true, Obfs: "salamander", ObfsPassword: "obfs-sample",
		}},
		{"vless grpc", linkGRPC, ProxyNode{
			Name: "jp-grpc-u1", Protocol: ProtoVLESS, Address: "jp.example.com", Port: 8443, UUID: sampleUUID,
			Network: NetGRPC, ServiceName: "grpc-svc", Security: SecTLS, SNI: "jp.example.com", ALPN: []string{"h2", "http/1.1"}, Fingerprint: "firefox",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseLink(tc.link)
			if err != nil {
				t.Fatalf("ParseLink: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
	for _, bad := range []string{linkWireGuar, "not a link", "vless://@host:443", "vless://" + sampleUUID + "@host:99999", "vmess://%%%", "ss://bm9jb2xvbg@h:1"} {
		if _, err := ParseLink(bad); !errors.Is(err, ErrUnsupportedLink) {
			t.Errorf("ParseLink(%q) err = %v, want ErrUnsupportedLink", bad, err)
		}
	}
}

func TestRewrite(t *testing.T) {
	got, err := Rewrite(linkTrojan, "美国 01", "edge.example.net")
	if err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	n, err := ParseLink(got)
	if err != nil || n.Name != "美国 01" || n.Address != "edge.example.net" || n.Port != 443 || n.Password != sampleAuth || n.Path != "/ws" {
		t.Fatalf("rewritten = %+v, %v", n, err)
	}
	got, err = Rewrite(linkVMess(t), "本地 WS", "")
	if err != nil {
		t.Fatalf("Rewrite vmess: %v", err)
	}
	n, err = ParseLink(got)
	if err != nil || n.Name != "本地 WS" || n.Address != "localhost" || n.Port != 24601 || n.UUID != sampleUUID {
		t.Fatalf("rewritten vmess = %+v, %v", n, err)
	}
	if n, err := ParseLink(InfoLink("到期时间：2026-12-13")); err != nil || n.Name != "到期时间：2026-12-13" || n.Protocol != ProtoShadowsocks {
		t.Fatalf("InfoLink parse = %+v, %v", n, err)
	}
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from golden; run `go test ./internal/manager/render -update` and review the diff", name)
	}
}

func TestMihomoGolden(t *testing.T) {
	out, err := Mihomo(sampleNodes(t), nil)
	if err != nil {
		t.Fatalf("Mihomo: %v", err)
	}
	var cfg struct {
		Proxies []map[string]any `yaml:"proxies"`
		Groups  []struct {
			Name    string   `yaml:"name"`
			Proxies []string `yaml:"proxies"`
		} `yaml:"proxy-groups"`
		Rules []string `yaml:"rules"`
	}
	if err := yaml.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("output is not valid YAML: %v", err)
	}
	if len(cfg.Proxies) != 8 || len(cfg.Groups) != 2 || cfg.Groups[0].Name != GroupSelect || cfg.Groups[1].Name != GroupAuto {
		t.Fatalf("proxies=%d groups=%+v", len(cfg.Proxies), cfg.Groups)
	}
	// 说明节点只进手动选择组，不参与自动测速
	if len(cfg.Groups[0].Proxies) != 9 || len(cfg.Groups[1].Proxies) != 7 {
		t.Fatalf("select=%d auto=%d", len(cfg.Groups[0].Proxies), len(cfg.Groups[1].Proxies))
	}
	if last := cfg.Rules[len(cfg.Rules)-1]; last != "MATCH,"+GroupSelect {
		t.Fatalf("last rule = %q", last)
	}
	checkGolden(t, "mihomo.golden.yaml", out)
}

func TestSingBoxGolden(t *testing.T) {
	out, err := SingBox(sampleNodes(t), nil)
	if err != nil {
		t.Fatalf("SingBox: %v", err)
	}
	var cfg struct {
		Outbounds []struct {
			Type string `json:"type"`
			Tag  string `json:"tag"`
		} `json:"outbounds"`
		Route struct {
			Final string `json:"final"`
		} `json:"route"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if len(cfg.Outbounds) != 11 || cfg.Outbounds[0].Type != "selector" || cfg.Outbounds[1].Type != "urltest" || cfg.Route.Final != GroupSelect {
		t.Fatalf("outbounds = %+v final=%q", cfg.Outbounds, cfg.Route.Final)
	}
	checkGolden(t, "singbox.golden.json", out)
}

func TestXrayGolden(t *testing.T) {
	out, err := Xray(sampleNodes(t), nil)
	if err != nil {
		t.Fatalf("Xray: %v", err)
	}
	var configs []struct {
		Remarks string `json:"remarks"`
	}
	if err := json.Unmarshal(out, &configs); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	// 说明节点与 hysteria2 不输出
	if len(configs) != 6 || configs[0].Remarks != "hk-reality-u1" {
		t.Fatalf("configs = %+v", configs)
	}
	checkGolden(t, "xray.golden.json", out)
}

func TestBase64AndEmptyInputs(t *testing.T) {
	out := Base64([]string{linkTrojan, linkSS})
	raw, err := base64.StdEncoding.DecodeString(string(out))
	if err != nil || strings.Count(string(raw), "\n") != 1 || !strings.HasPrefix(string(raw), "trojan://") {
		t.Fatalf("Base64 = %q, %v", raw, err)
	}
	// 没有任何节点时仍输出可解析的配置
	if m, err := Mihomo(nil, nil); err != nil || !strings.Contains(string(m), GroupSelect) {
		t.Fatalf("Mihomo(nil) = %q, %v", m, err)
	}
	if s, err := SingBox([]ProxyNode{InfoNode("已到期")}, nil); err != nil || strings.Contains(string(s), `"urltest"`) {
		t.Fatalf("SingBox(info only) should have no urltest group: %v", err)
	}
	if x, err := Xray(nil, nil); err != nil || strings.TrimSpace(string(x)) != "[]" {
		t.Fatalf("Xray(nil) = %q, %v", x, err)
	}
}

func TestUniqueNames(t *testing.T) {
	keepAll := func(ProxyNode) bool { return true }
	got := selectNodes([]ProxyNode{{Name: "a"}, {Name: "a"}, {Name: ""}, {Name: "a"}, {Protocol: "vless", Address: "h"}}, nil, keepAll).names
	want := []string{"a", "a 2", "-", "a 3", "vless-h"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %q, want %q", got, want)
	}
}

// 自有落地节点的 SOCKS5 地址；用户名与密码是示例值。
const (
	sampleSocksUser = "sample-user"
	linkSocksAuth   = "socks5://" + sampleSocksUser + ":" + sampleAuth + "@home.example.net:1080#home-socks"
	linkSocksPlain  = "socks5://203.0.113.9:1080"
)

func TestParseSocks(t *testing.T) {
	encoded := "socks://" + base64.StdEncoding.EncodeToString([]byte(sampleSocksUser+":"+sampleAuth)) + "@home.example.net:1080#home-socks"
	withAuth := ProxyNode{
		Name: "home-socks", Protocol: ProtoSocks, Address: "home.example.net", Port: 1080,
		Username: sampleSocksUser, Password: sampleAuth, Network: NetTCP, Security: SecNone,
	}
	cases := []struct {
		name string
		link string
		want ProxyNode
	}{
		{"user and password", linkSocksAuth, withAuth},
		{"base64 userinfo", encoded, withAuth},
		{"no auth", linkSocksPlain, ProxyNode{Protocol: ProtoSocks, Address: "203.0.113.9", Port: 1080, Network: NetTCP, Security: SecNone}},
	}
	for _, c := range cases {
		got, err := ParseLink(c.link)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: ParseLink = %+v, %v; want %+v", c.name, got, err, c.want)
		}
	}
	for _, bad := range []string{"socks5://home.example.net", "socks5://home.example.net:0", "socks5://only-user@home.example.net:1080", "socks5://:1080"} {
		if _, err := ParseLink(bad); !errors.Is(err, ErrUnsupportedLink) {
			t.Errorf("ParseLink(%q) error = %v, want ErrUnsupportedLink", bad, err)
		}
	}
}

// sampleNodes 里各节点的下标（0 是说明节点）。
const (
	idxInfo    = 0
	idxReality = 1
	idxTrojan  = 3
	idxSS      = 4
	idxHy2     = 5
	idxGRPC    = 6
)

const (
	chainNameHome    = "家宽落地"
	chainNameDefault = "hk-reality-u1 → us-trojan-u1"
	// 与入口节点重名的链路名，输出时应追加序号
	chainNameClash = "hk-reality-u1 2"
)

// sampleChains 覆盖：落地为平台节点（默认名）、落地为自有 SOCKS5、入口是说明节点 / 下标越界（都不输出）、
// 入口为 hysteria2 且与普通节点重名（Xray 不支持该入口，不输出）。
func sampleChains(t *testing.T, nodes []ProxyNode) []Chain {
	t.Helper()
	socks, err := ParseLink(linkSocksAuth)
	if err != nil {
		t.Fatalf("ParseLink socks: %v", err)
	}
	return []Chain{
		{Entry: idxReality, Exit: nodes[idxTrojan]},
		{Name: chainNameHome, Entry: idxGRPC, Exit: socks},
		{Name: "入口是说明节点", Entry: idxInfo, Exit: socks},
		{Name: "下标越界", Entry: len(nodes), Exit: socks},
		{Name: "下标为负", Entry: -1, Exit: socks},
		{Name: "hk-reality-u1", Entry: idxHy2, Exit: nodes[idxSS]},
	}
}

func TestMihomoChains(t *testing.T) {
	nodes := sampleNodes(t)
	out, err := Mihomo(nodes, sampleChains(t, nodes))
	if err != nil {
		t.Fatalf("Mihomo: %v", err)
	}
	var cfg struct {
		Proxies []map[string]any `yaml:"proxies"`
		Groups  []struct {
			Name    string   `yaml:"name"`
			Proxies []string `yaml:"proxies"`
		} `yaml:"proxy-groups"`
	}
	if err := yaml.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("output is not valid YAML: %v", err)
	}
	dialers := make(map[string]string, len(cfg.Proxies))
	for _, p := range cfg.Proxies {
		if d, ok := p["dialer-proxy"].(string); ok {
			dialers[p["name"].(string)] = d
		}
	}
	want := map[string]string{chainNameDefault: "hk-reality-u1", chainNameHome: "jp-grpc-u1", chainNameClash: "tw-hy2-u1"}
	if !reflect.DeepEqual(dialers, want) {
		t.Fatalf("dialer-proxy = %v, want %v", dialers, want)
	}
	// 链路进手动选择组、不进自动测速组
	if len(cfg.Proxies) != 11 || len(cfg.Groups[0].Proxies) != 12 || len(cfg.Groups[1].Proxies) != 7 {
		t.Fatalf("proxies=%d select=%d auto=%d", len(cfg.Proxies), len(cfg.Groups[0].Proxies), len(cfg.Groups[1].Proxies))
	}
	checkGolden(t, "mihomo-chain.golden.yaml", out)
}

func TestSingBoxChains(t *testing.T) {
	nodes := sampleNodes(t)
	out, err := SingBox(nodes, sampleChains(t, nodes))
	if err != nil {
		t.Fatalf("SingBox: %v", err)
	}
	var cfg struct {
		Outbounds []struct {
			Type      string   `json:"type"`
			Tag       string   `json:"tag"`
			Detour    string   `json:"detour"`
			Outbounds []string `json:"outbounds"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	detours := make(map[string]string, len(cfg.Outbounds))
	for _, o := range cfg.Outbounds {
		if o.Detour != "" {
			detours[o.Tag] = o.Detour
		}
	}
	want := map[string]string{chainNameDefault: "hk-reality-u1", chainNameHome: "jp-grpc-u1", chainNameClash: "tw-hy2-u1"}
	if !reflect.DeepEqual(detours, want) {
		t.Fatalf("detour = %v, want %v", detours, want)
	}
	if len(cfg.Outbounds[0].Outbounds) != 12 || len(cfg.Outbounds[1].Outbounds) != 7 {
		t.Fatalf("select=%d auto=%d", len(cfg.Outbounds[0].Outbounds), len(cfg.Outbounds[1].Outbounds))
	}
	checkGolden(t, "singbox-chain.golden.json", out)
}

func TestXrayChains(t *testing.T) {
	nodes := sampleNodes(t)
	out, err := Xray(nodes, sampleChains(t, nodes))
	if err != nil {
		t.Fatalf("Xray: %v", err)
	}
	var configs []struct {
		Remarks   string `json:"remarks"`
		Outbounds []struct {
			Tag      string `json:"tag"`
			Protocol string `json:"protocol"`
			Stream   struct {
				Sockopt struct {
					DialerProxy string `json:"dialerProxy"`
				} `json:"sockopt"`
			} `json:"streamSettings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(out, &configs); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	// 6 个普通节点 + 2 条链路；入口是 hysteria2 的那条 Xray 不支持，不输出
	if len(configs) != 8 || configs[6].Remarks != chainNameDefault || configs[7].Remarks != chainNameHome {
		t.Fatalf("configs = %d", len(configs))
	}
	for _, c := range configs[6:] {
		ob := c.Outbounds
		if len(ob) != 3 || ob[0].Tag != xrayProxyTag || ob[0].Stream.Sockopt.DialerProxy != xrayChainEntryTag || ob[1].Tag != xrayChainEntryTag || ob[2].Tag != tagDirect {
			t.Fatalf("%s: outbounds = %+v", c.Remarks, ob)
		}
	}
	if configs[7].Outbounds[0].Protocol != "socks" || configs[7].Outbounds[1].Protocol != "vless" {
		t.Fatalf("home chain protocols = %s via %s", configs[7].Outbounds[0].Protocol, configs[7].Outbounds[1].Protocol)
	}
	checkGolden(t, "xray-chain.golden.json", out)
}

// HTTP / HTTPS 代理出口（Ruling-51）；用户名与密码是示例值。
func TestParseHTTPProxy(t *testing.T) {
	const host = "proxy.example.net"
	withAuth := "http://" + sampleSocksUser + ":" + sampleAuth + "@" + host + ":8080#office"
	tests := []struct {
		name, link string
		want       ProxyNode
	}{
		{"http with auth", withAuth, ProxyNode{
			Name: "office", Protocol: ProtoHTTP, Address: host, Port: 8080, Username: sampleSocksUser, Password: sampleAuth, Network: NetTCP, Security: SecNone,
		}},
		{"http no auth", "http://203.0.113.9:3128", ProxyNode{Protocol: ProtoHTTP, Address: "203.0.113.9", Port: 3128, Network: NetTCP, Security: SecNone}},
		{"https with sni", "https://" + host + ":443/?sni=front.example.net&allowInsecure=1#tls", ProxyNode{
			Name: "tls", Protocol: ProtoHTTP, Address: host, Port: 443, Network: NetTCP, Security: SecTLS, SNI: "front.example.net", AllowInsecure: true,
		}},
		{"user without password", "http://only-user@" + host + ":8080", ProxyNode{
			Protocol: ProtoHTTP, Address: host, Port: 8080, Username: "only-user", Network: NetTCP, Security: SecNone,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLink(tt.link)
			if err != nil {
				t.Fatalf("ParseLink: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
	// 没有端口、带路径的网页地址、只有密码没有用户名的都不是代理地址
	for _, bad := range []string{"http://" + host, "https://" + host + "/index.html", "http://" + host + ":8080/path", "http://:" + sampleAuth + "@" + host + ":8080"} {
		if _, err := ParseLink(bad); !errors.Is(err, ErrUnsupportedLink) {
			t.Errorf("ParseLink(%q) err = %v, want ErrUnsupportedLink", bad, err)
		}
	}
}

func TestHTTPProxyOutbounds(t *testing.T) {
	node := ProxyNode{
		Name: "office", Protocol: ProtoHTTP, Address: "proxy.example.net", Port: 443, Username: sampleSocksUser, Password: sampleAuth,
		Network: NetTCP, Security: SecTLS, SNI: "front.example.net",
	}
	xr, ok := XrayOutbound(node, "hub-r1")
	if !ok {
		t.Fatal("XrayOutbound: unsupported")
	}
	raw, err := json.Marshal(xr)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"protocol":"http"`, `"user":"` + sampleSocksUser + `"`, `"security":"tls"`, `"serverName":"front.example.net"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("xray outbound lacks %s: %s", want, raw)
		}
	}
	if !mihomoSupports(node) || !singBoxSupports(node) {
		t.Fatal("http proxy should be supported by mihomo and sing-box")
	}
	mh := mihomoProxy(node, "office")
	if mh["type"] != "http" || mh["tls"] != true || mh["sni"] != "front.example.net" || mh["username"] != sampleSocksUser {
		t.Errorf("mihomo proxy = %v", mh)
	}
	if _, hasUDP := mh["udp"]; hasUDP {
		t.Errorf("mihomo http proxy must not carry udp: %v", mh)
	}
	sb := singBoxOutbound(node, "office")
	tls, _ := sb["tls"].(map[string]any)
	if sb["type"] != "http" || tls["server_name"] != "front.example.net" || sb["username"] != sampleSocksUser {
		t.Errorf("sing-box outbound = %v", sb)
	}
	// 不带 TLS 的 HTTP 代理：没有 tls 段
	plain := node
	plain.Security, plain.SNI = SecNone, ""
	if _, hasTLS := singBoxOutbound(plain, "p")["tls"]; hasTLS {
		t.Error("plain http proxy must not carry tls")
	}
}
