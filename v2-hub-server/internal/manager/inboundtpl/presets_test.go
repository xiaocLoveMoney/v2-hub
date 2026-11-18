package inboundtpl

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const (
	testDomain     = "node.example.com"
	testCertFile   = "/root/cert/node.example.com/fullchain.pem"
	testKeyFile    = "/root/cert/node.example.com/privkey.pem"
	testTarget     = "www.example.org:443"
	testDecrypt    = "mlkem768x25519plus.native.600s.sample-server"
	testEncrypt    = "mlkem768x25519plus.native.0rtt.sample-client"
	presetBasePort = 41000
)

type presetConfig struct {
	Settings       map[string]any `json:"settings"`
	StreamSettings map[string]any `json:"streamSettings"`
}

func fullParams() PresetParams {
	return PresetParams{Domain: testDomain, CertFile: testCertFile, KeyFile: testKeyFile, RealityTarget: testTarget,
		VLESSDecryption: testDecrypt, VLESSEncryption: testEncrypt}
}

func buildPreset(t *testing.T, key string, p PresetParams) (string, presetConfig) {
	t.Helper()
	spec, err := BuildPreset(key, Base{Remark: PresetRemark(key), Port: presetBasePort, Enabled: true}, p)
	if err != nil {
		t.Fatalf("BuildPreset(%s): %v", key, err)
	}
	var cfg presetConfig
	if err := json.Unmarshal(spec.Config, &cfg); err != nil {
		t.Fatalf("decode %s: %v", key, err)
	}
	if spec.Port != presetBasePort || spec.Remark == "" || !spec.Enabled {
		t.Fatalf("%s base fields: %+v", key, spec)
	}
	return spec.Protocol, cfg
}

func tlsOf(t *testing.T, key string, cfg presetConfig) map[string]any {
	t.Helper()
	tls, _ := cfg.StreamSettings["tlsSettings"].(map[string]any)
	certs, _ := tls["certificates"].([]any)
	if cfg.StreamSettings["security"] != "tls" || tls["serverName"] != testDomain || len(certs) != 1 {
		t.Fatalf("%s tls: %+v", key, cfg.StreamSettings)
	}
	cert, _ := certs[0].(map[string]any)
	if cert["certificateFile"] != testCertFile || cert["keyFile"] != testKeyFile {
		t.Fatalf("%s certificate must reference the host's files: %+v", key, cert)
	}
	return tls
}

func TestPresetsMatchUpstreamShapes(t *testing.T) {
	if len(PresetKeys) != 7 {
		t.Fatalf("want 7 presets, got %d", len(PresetKeys))
	}
	for _, key := range PresetKeys {
		buildPreset(t, key, fullParams())
	}

	proto, cfg := buildPreset(t, PresetVLESSRealityVision, fullParams())
	reality, _ := cfg.StreamSettings["realitySettings"].(map[string]any)
	if proto != "vless" || cfg.StreamSettings["security"] != "reality" || reality["target"] != testTarget || cfg.Settings["decryption"] != "none" {
		t.Fatalf("reality: %s %+v", proto, cfg)
	}

	_, cfg = buildPreset(t, PresetVLESSRealityMLKEM, fullParams())
	if cfg.Settings["decryption"] != testDecrypt || cfg.Settings["encryption"] != testEncrypt {
		t.Fatalf("mlkem settings: %+v", cfg.Settings)
	}

	_, cfg = buildPreset(t, PresetVLESSXHTTPTLS, fullParams())
	xhttp, _ := cfg.StreamSettings["xhttpSettings"].(map[string]any)
	if cfg.StreamSettings["network"] != "xhttp" || xhttp["host"] != testDomain || !strings.HasPrefix(xhttp["path"].(string), "/") || xhttp["mode"] != "auto" {
		t.Fatalf("xhttp: %+v", cfg.StreamSettings)
	}
	tlsOf(t, PresetVLESSXHTTPTLS, cfg)

	proto, cfg = buildPreset(t, PresetVMessWSTLS, fullParams())
	if proto != "vmess" || cfg.StreamSettings["network"] != "ws" {
		t.Fatalf("vmess: %s %+v", proto, cfg.StreamSettings)
	}
	if alpn := tlsOf(t, PresetVMessWSTLS, cfg)["alpn"].([]any); len(alpn) != 1 || alpn[0] != "http/1.1" {
		t.Fatalf("ws over tls must use http/1.1, got %v", alpn)
	}

	proto, cfg = buildPreset(t, PresetTrojanTLS, fullParams())
	if proto != "trojan" || cfg.StreamSettings["network"] != "tcp" {
		t.Fatalf("trojan: %s %+v", proto, cfg.StreamSettings)
	}
	tlsOf(t, PresetTrojanTLS, cfg)

	proto, cfg = buildPreset(t, PresetShadowsocks2022, fullParams())
	if proto != "shadowsocks" || cfg.Settings["method"] != "2022-blake3-aes-256-gcm" {
		t.Fatalf("ss2022: %s %+v", proto, cfg.Settings)
	}

	proto, cfg = buildPreset(t, PresetHysteria2Salamander, fullParams())
	mask, _ := cfg.StreamSettings["finalmask"].(map[string]any)
	udp, _ := mask["udp"].([]any)
	if proto != "hysteria" || cfg.StreamSettings["network"] != "hysteria" || len(udp) != 1 {
		t.Fatalf("hysteria2: %s %+v", proto, cfg.StreamSettings)
	}
	salamander, _ := udp[0].(map[string]any)
	settings, _ := salamander["settings"].(map[string]any)
	if salamander["type"] != "salamander" || len(settings["password"].(string)) < 4 {
		t.Fatalf("salamander mask: %+v", salamander)
	}
	if alpn := tlsOf(t, PresetHysteria2Salamander, cfg)["alpn"].([]any); len(alpn) != 1 || alpn[0] != "h3" {
		t.Fatalf("hysteria2 alpn = %v", alpn)
	}
}

func TestPresetsRandomizeSecrets(t *testing.T) {
	_, a := buildPreset(t, PresetHysteria2Salamander, fullParams())
	_, b := buildPreset(t, PresetHysteria2Salamander, fullParams())
	if a.StreamSettings["finalmask"].(map[string]any)["udp"].([]any)[0].(map[string]any)["settings"].(map[string]any)["password"] ==
		b.StreamSettings["finalmask"].(map[string]any)["udp"].([]any)[0].(map[string]any)["settings"].(map[string]any)["password"] {
		t.Fatal("salamander passwords must differ between inbounds")
	}
	_, x := buildPreset(t, PresetVLESSRealityVision, fullParams())
	_, y := buildPreset(t, PresetVLESSRealityMLKEM, fullParams())
	if x.StreamSettings["realitySettings"].(map[string]any)["privateKey"] == y.StreamSettings["realitySettings"].(map[string]any)["privateKey"] {
		t.Fatal("each REALITY inbound must get its own key pair")
	}
}

func TestPresetRejectsMissingInputs(t *testing.T) {
	noCert := fullParams()
	noCert.CertFile = ""
	for _, key := range PresetKeys {
		_, err := BuildPreset(key, Base{Port: presetBasePort}, noCert)
		var pe *ParamError
		if PresetNeedsCert(key) != errors.As(err, &pe) {
			t.Fatalf("%s: needsCert=%v, err=%v", key, PresetNeedsCert(key), err)
		}
	}
	noKeys := fullParams()
	noKeys.VLESSDecryption = "none"
	if _, err := BuildPreset(PresetVLESSRealityMLKEM, Base{Port: presetBasePort}, noKeys); err == nil {
		t.Fatal("mlkem preset needs the panel's ML-KEM parameters")
	}
	badTarget := fullParams()
	badTarget.RealityTarget = "not a target"
	if _, err := BuildPreset(PresetVLESSRealityVision, Base{Port: presetBasePort}, badTarget); err == nil {
		t.Fatal("reality preset needs a valid target")
	}
	if _, err := BuildPreset("nope", Base{}, fullParams()); !errors.Is(err, ErrUnknownTemplate) {
		t.Fatalf("unknown preset err = %v", err)
	}
	if tcp, udp := PresetPorts(PresetHysteria2Salamander); tcp || !udp {
		t.Fatal("hysteria2 listens on udp only")
	}
	if !PresetWantsVision(PresetVLESSRealityMLKEM) || PresetWantsVision(PresetVLESSXHTTPTLS) {
		t.Fatal("only the two REALITY presets want vision")
	}
	if RealityHost(testTarget) != "www.example.org" || RealityHost("bad") != "" {
		t.Fatal("RealityHost")
	}
}
