package render

import (
	"encoding/json"
	"testing"

	"go.yaml.in/yaml/v3"
)

// 一键部署新增的两种 VLESS 形状（Ruling-77）：示例值，不对应真实节点。
const (
	sampleEncryption = "mlkem768x25519plus.native.0rtt.sample-client-key"
	linkVLESSEnc     = "vless://" + sampleUUID + "@kr.example.com:41001?type=tcp&encryption=" + sampleEncryption +
		"&security=reality&pbk=" + samplePubKey + "&fp=chrome&sni=www.example.org&sid=a1b2c3d4&flow=xtls-rprx-vision#kr-mlkem"
	linkXHTTP = "vless://" + sampleUUID + "@kr.example.com:41002?type=xhttp&encryption=none&path=%2Fxh&host=kr.example.com&mode=auto" +
		"&security=tls&sni=kr.example.com&fp=chrome#kr-xhttp"
)

func variantNodes(t *testing.T) []ProxyNode {
	t.Helper()
	nodes := make([]ProxyNode, 0, 2)
	for _, l := range []string{linkVLESSEnc, linkXHTTP} {
		n, err := ParseLink(l)
		if err != nil {
			t.Fatalf("ParseLink(%.24s…): %v", l, err)
		}
		nodes = append(nodes, n)
	}
	return nodes
}

func TestParseVLESSEncryption(t *testing.T) {
	nodes := variantNodes(t)
	if !nodes[0].hasVLESSEncryption() || nodes[0].Encryption != sampleEncryption {
		t.Fatalf("encryption not parsed: %+v", nodes[0])
	}
	if nodes[1].hasVLESSEncryption() || nodes[1].Encryption != "" {
		t.Fatalf("encryption=none must count as unencrypted: %+v", nodes[1])
	}
}

func TestMihomoCarriesEncryptionAndXHTTP(t *testing.T) {
	out, err := Mihomo(variantNodes(t), nil)
	if err != nil {
		t.Fatalf("Mihomo: %v", err)
	}
	var cfg struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("output is not valid YAML: %v", err)
	}
	if len(cfg.Proxies) != 2 {
		t.Fatalf("want both nodes, got %d", len(cfg.Proxies))
	}
	enc, xh := cfg.Proxies[0], cfg.Proxies[1]
	if enc["encryption"] != sampleEncryption || enc["flow"] != "xtls-rprx-vision" {
		t.Fatalf("encryption node: %+v", enc)
	}
	opts, _ := xh["xhttp-opts"].(map[string]any)
	if xh["network"] != "xhttp" || opts["path"] != "/xh" || opts["host"] != "kr.example.com" || opts["mode"] != "auto" {
		t.Fatalf("xhttp node: %+v", xh)
	}
	if _, has := xh["encryption"]; has {
		t.Fatal("an unencrypted node must not carry encryption")
	}
}

func TestSingBoxSkipsEncryptionAndXHTTP(t *testing.T) {
	out, err := SingBox(variantNodes(t), nil)
	if err != nil {
		t.Fatalf("SingBox: %v", err)
	}
	var cfg struct {
		Outbounds []struct {
			Type string `json:"type"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	for _, o := range cfg.Outbounds {
		if o.Type == "vless" {
			t.Fatal("sing-box supports neither VLESS Encryption nor XHTTP; neither node may be output")
		}
	}
}

func TestXrayCarriesEncryption(t *testing.T) {
	out, err := Xray(variantNodes(t), nil)
	if err != nil {
		t.Fatalf("Xray: %v", err)
	}
	var configs []struct {
		Outbounds []struct {
			Settings struct {
				Vnext []struct {
					Users []map[string]any `json:"users"`
				} `json:"vnext"`
			} `json:"settings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(out, &configs); err != nil || len(configs) != 2 {
		t.Fatalf("configs: %d, %v", len(configs), err)
	}
	user := configs[0].Outbounds[0].Settings.Vnext[0].Users[0]
	if user["encryption"] != sampleEncryption {
		t.Fatalf("xray user encryption = %v", user["encryption"])
	}
	if got := configs[1].Outbounds[0].Settings.Vnext[0].Users[0]["encryption"]; got != encryptionNone {
		t.Fatalf("unencrypted node should keep encryption none, got %v", got)
	}
	if _, ok := XrayOutbound(variantNodes(t)[0], "exit"); ok {
		t.Fatal("a VLESS Encryption node must not be used as a chain exit")
	}
}
