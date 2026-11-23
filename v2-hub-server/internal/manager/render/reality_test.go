package render

import (
	"encoding/json"
	"net/url"
	"slices"
	"testing"
)

const (
	realityPBK   = "Q2xpZW50UHVibGljS2V5MDEyMzQ1Njc4OWFiY2RlZg"
	realityOther = "T3RoZXJQdWJsaWNLZXkwMTIzNDU2Nzg5YWJjZGVmZ2g"
)

func realityLink(pbk, sid, sni string) string {
	return "vless://11111111-2222-3333-4444-555555555555@203.0.113.7:443?type=tcp&security=reality&pbk=" + pbk +
		"&fp=chrome&sni=" + sni + "&sid=" + sid + "&spx=%2Fabc123&flow=xtls-rprx-vision#hk-a"
}

func queryOf(t *testing.T, link string) url.Values {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

var sampleChoices = []RealityChoices{
	{PublicKey: realityOther, ShortIDs: []string{"aa"}, ServerNames: []string{"other.example.com"}},
	{PublicKey: realityPBK, ShortIDs: []string{"0a", "1b2c", "3d4e5f", "6a7b8c9d"}, ServerNames: []string{"www.example.com", "cdn.example.com"}},
}

// 面板每次从列表里随机取 sid / sni（Ruling-92）：无论取到哪一项，同一个 client 改写后都相同，且仍在列表里。
func TestPinRealityStable(t *testing.T) {
	want := ""
	for _, sid := range sampleChoices[1].ShortIDs {
		for _, sni := range sampleChoices[1].ServerNames {
			got := PinReality(realityLink(realityPBK, sid, sni), "u7-abc123", sampleChoices)
			if want == "" {
				want = got
			}
			if got != want {
				t.Fatalf("同一 client 的链接应稳定：\n%s\n%s", got, want)
			}
		}
	}
	q := queryOf(t, want)
	if !slices.Contains(sampleChoices[1].ShortIDs, q.Get("sid")) || !slices.Contains(sampleChoices[1].ServerNames, q.Get("sni")) {
		t.Fatalf("改写后的值应取自所属入站的列表：%s", want)
	}
	if q.Get("spx") != "/abc123" || q.Get("flow") != "xtls-rprx-vision" || q.Get("pbk") != realityPBK {
		t.Fatalf("其余参数应保留：%s", want)
	}
	n, err := ParseLink(want)
	if err != nil || n.Name != "hk-a" || n.Port != 443 {
		t.Fatalf("改写后应仍可解析：%+v, %v", n, err)
	}
	// 不同 client 可以落在不同项上（按 email 哈希分散），不要求一定不同，只要求各自稳定
	other := PinReality(realityLink(realityPBK, "0a", "www.example.com"), "u8-def456", sampleChoices)
	if other != PinReality(realityLink(realityPBK, "6a7b8c9d", "cdn.example.com"), "u8-def456", sampleChoices) {
		t.Fatal("另一个 client 也应稳定")
	}
}

// 认不出所属入站、值不在列表里、不是 REALITY 时原样返回。
func TestPinRealityLeavesOthers(t *testing.T) {
	cases := map[string]string{
		"公钥对不上":           realityLink("bm9TdWNoS2V5", "0a", "www.example.com"),
		"不是 REALITY":      "vless://11111111-2222-3333-4444-555555555555@a.example.com:443?type=ws&security=tls&sni=www.example.com#x",
		"vmess":           "vmess://eyJhZGQiOiJhLmV4YW1wbGUuY29tIn0=",
		"无查询串":            "trojan://secret@a.example.com:443#x",
		"sid 与 sni 都不在列表": realityLink(realityPBK, "ffff", "host.example.org"),
	}
	for name, link := range cases {
		if got := PinReality(link, "u1-abc123", sampleChoices); got != link {
			t.Errorf("%s 应原样返回：%s", name, got)
		}
	}
	// sni 是对外地址自己指定的（不在列表里）：只改 sid，sni 不动
	got := PinReality(realityLink(realityPBK, "1b2c", "host.example.org"), "u1-abc123", sampleChoices)
	if q := queryOf(t, got); q.Get("sni") != "host.example.org" || !slices.Contains(sampleChoices[1].ShortIDs, q.Get("sid")) {
		t.Fatalf("不在列表里的 sni 应保持原样：%s", got)
	}
	// 两个公钥相同、列表不同的入站都认领这条链接：认不准，原样返回
	ambiguous := append(slices.Clone(sampleChoices), RealityChoices{PublicKey: realityPBK, ShortIDs: []string{"1b2c", "99"}})
	link := realityLink(realityPBK, "1b2c", "www.example.com")
	if got := PinReality(link, "u1-abc123", ambiguous); got != link {
		t.Fatalf("认出多个入站时应原样返回：%s", got)
	}
}

func TestRealityChoicesOf(t *testing.T) {
	cfg := `{"settings":{},"streamSettings":{"network":"tcp","security":"reality","realitySettings":{"show":false,` +
		`"serverNames":["www.example.com"],"shortIds":["0a","1b2c"],"settings":{"publicKey":"` + realityPBK + `","fingerprint":"chrome"}}}}`
	c, ok := RealityChoicesOf(json.RawMessage(cfg))
	if !ok || c.PublicKey != realityPBK || !slices.Equal(c.ShortIDs, []string{"0a", "1b2c"}) || !slices.Equal(c.ServerNames, []string{"www.example.com"}) {
		t.Fatalf("choices = %+v, %v", c, ok)
	}
	for _, bad := range []string{`{"settings":{}}`, `{"streamSettings":{"security":"tls"}}`, `not json`,
		`{"streamSettings":{"security":"reality","realitySettings":{"shortIds":["0a"]}}}`} {
		if _, ok := RealityChoicesOf(json.RawMessage(bad)); ok {
			t.Errorf("%s 不应取出可选值", bad)
		}
	}
}
