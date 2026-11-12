package inboundtpl

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
)

func decode(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return m
}

func TestVlessRealityKeysMatch(t *testing.T) {
	spec, err := Build(VlessReality, Base{Remark: "r", Port: 443, Enabled: true}, Params{Target: "www.cloudflare.com:443"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if spec.Protocol != "vless" || spec.Port != 443 {
		t.Fatalf("spec = %+v", spec)
	}
	reality := decode(t, spec.Config)["streamSettings"].(map[string]any)["realitySettings"].(map[string]any)
	priv, err := base64.RawURLEncoding.DecodeString(reality["privateKey"].(string))
	if err != nil {
		t.Fatalf("private key encoding: %v", err)
	}
	key, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		t.Fatalf("private key: %v", err)
	}
	pub := reality["settings"].(map[string]any)["publicKey"].(string)
	if base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()) != pub {
		t.Fatal("public key does not match private key")
	}
	if names := reality["serverNames"].([]any); len(names) != 1 || names[0] != "www.cloudflare.com" {
		t.Fatalf("serverNames = %v", names)
	}
}

func TestTemplatesAndValidation(t *testing.T) {
	base := Base{Remark: "r", Port: 8443, Enabled: true}
	ss, err := Build(Shadowsocks2022, base, Params{})
	if err != nil {
		t.Fatalf("ss2022: %v", err)
	}
	key, err := base64.StdEncoding.DecodeString(decode(t, ss.Config)["settings"].(map[string]any)["password"].(string))
	if err != nil || len(key) != ss2022AES256Bytes {
		t.Fatalf("ss2022 key = %d bytes, %v", len(key), err)
	}
	ws, err := Build(VmessWS, base, Params{Path: "/ray"})
	if err != nil || decode(t, ws.Config)["streamSettings"].(map[string]any)["wsSettings"].(map[string]any)["path"] != "/ray" {
		t.Fatalf("vmess-ws: %v", err)
	}

	var pe *ParamError
	for name, run := range map[string]func() error{
		"reality bad target": func() error { _, err := Build(VlessReality, base, Params{Target: "no-port"}); return err },
		"reality bad sni": func() error {
			_, err := Build(VlessReality, base, Params{Target: "a.com:443", ServerNames: []string{"bad name"}})
			return err
		},
		"ws bad path":       func() error { _, err := Build(VmessWS, base, Params{Path: "ray<script>"}); return err },
		"ss2022 bad method": func() error { _, err := Build(Shadowsocks2022, base, Params{Method: "aes-128-gcm"}); return err },
	} {
		if err := run(); !errors.As(err, &pe) {
			t.Fatalf("%s: err = %v, want ParamError", name, err)
		}
	}
	if _, err := Build("trojan", base, Params{}); !errors.Is(err, ErrUnknownTemplate) {
		t.Fatalf("unknown template err = %v", err)
	}
}
