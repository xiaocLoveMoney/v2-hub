package inboundtpl

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"v2-hub-server/internal/manager/panel"
)

// 一键部署的预设（docs/03 §5.1 “一键部署入站”，Ruling-76），与 hub-api.yaml InboundPresetKey 一致，顺序即弹窗里的顺序。
const (
	PresetVLESSRealityVision  = "vless_reality_vision"
	PresetVLESSRealityMLKEM   = "vless_reality_mlkem"
	PresetVLESSXHTTPTLS       = "vless_xhttp_tls"
	PresetVMessWSTLS          = "vmess_ws_tls"
	PresetTrojanTLS           = "trojan_tls"
	PresetShadowsocks2022     = "shadowsocks_2022"
	PresetHysteria2Salamander = "hysteria2_salamander"
)

const (
	protocolTrojan   = "trojan"
	protocolHysteria = "hysteria"

	networkTCP      = "tcp"
	networkXHTTP    = "xhttp"
	networkWS       = "ws"
	networkHysteria = "hysteria"
	securityTLS     = "tls"
	xhttpModeAuto   = "auto"
	encryptionNone  = "none"
	hysteriaVersion = 2

	// presetPathBytes 是随机路径的字节数（十六进制后 16 个字符）；salamanderPasswordBytes 是混淆密码的字节数（Xray 要求 ≥ 4）。
	presetPathBytes         = 8
	salamanderPasswordBytes = 16
	udpIdleTimeoutSeconds   = 60
)

// PresetKeys 是全部预设，按弹窗里的顺序。
var PresetKeys = []string{
	PresetVLESSRealityVision, PresetVLESSRealityMLKEM, PresetVLESSXHTTPTLS, PresetVMessWSTLS,
	PresetTrojanTLS, PresetShadowsocks2022, PresetHysteria2Salamander,
}

// presetMeta 是每个预设的固定属性。
type presetMeta struct {
	remark    string
	needsCert bool
	tcp, udp  bool
	vision    bool
}

var presets = map[string]presetMeta{
	PresetVLESSRealityVision:  {remark: "VLESS-REALITY", tcp: true, vision: true},
	PresetVLESSRealityMLKEM:   {remark: "VLESS-REALITY-MLKEM", tcp: true, vision: true},
	PresetVLESSXHTTPTLS:       {remark: "VLESS-XHTTP", needsCert: true, tcp: true},
	PresetVMessWSTLS:          {remark: "VMess-WS", needsCert: true, tcp: true},
	PresetTrojanTLS:           {remark: "Trojan", needsCert: true, tcp: true},
	PresetShadowsocks2022:     {remark: "SS2022", tcp: true, udp: true},
	PresetHysteria2Salamander: {remark: "Hysteria2", needsCert: true, udp: true},
}

// IsPreset 报告 key 是不是已知的预设。
func IsPreset(key string) bool {
	_, ok := presets[key]
	return ok
}

// PresetNeedsCert 报告预设是否要主机的域名证书。
func PresetNeedsCert(key string) bool { return presets[key].needsCert }

// PresetRemark 是预设建出的入站备注（重名时调用方再加端口）。
func PresetRemark(key string) string { return presets[key].remark }

// PresetPorts 报告预设要监听 TCP 与 / 或 UDP（放行防火墙用）。
func PresetPorts(key string) (tcp, udp bool) { return presets[key].tcp, presets[key].udp }

// PresetWantsVision 报告预设要服务器打开 VLESS Vision 流控（Ruling-77）。
func PresetWantsVision(key string) bool { return presets[key].vision }

// PresetParams 是预设用到的参数；没用到的字段忽略。
type PresetParams struct {
	// Domain 是主机域名（TLS 预设的 SNI、Host），CertFile / KeyFile 是面板机器上的证书与私钥路径。
	Domain   string
	CertFile string
	KeyFile  string
	// RealityTarget 是 REALITY 伪装目标（域名:端口），SNI 取它的主机名。
	RealityTarget string
	// VLESSDecryption / VLESSEncryption 是面板生成的 ML-KEM-768 那一组 VLESS Encryption 参数。
	VLESSDecryption string
	VLESSEncryption string
}

// BuildPreset 按预设生成入站规格；随机值（REALITY 密钥、Short ID、路径、SS 服务端密钥、Salamander 密码）在这里生成。
func BuildPreset(key string, base Base, p PresetParams) (panel.InboundSpec, error) {
	meta, ok := presets[key]
	if !ok {
		return panel.InboundSpec{}, ErrUnknownTemplate
	}
	if meta.needsCert && (!hostnamePattern.MatchString(p.Domain) || p.CertFile == "" || p.KeyFile == "") {
		return panel.InboundSpec{}, &ParamError{Message: "这个预设要主机的域名与证书"}
	}
	var (
		protocol string
		cfg      map[string]any
		err      error
	)
	switch key {
	case PresetVLESSRealityVision:
		protocol, cfg, err = vlessReality(Params{Target: p.RealityTarget})
	case PresetVLESSRealityMLKEM:
		protocol, cfg, err = vlessRealityMLKEM(p)
	case PresetVLESSXHTTPTLS:
		protocol, cfg, err = vlessXHTTPTLS(p)
	case PresetVMessWSTLS:
		protocol, cfg, err = vmessWSTLS(p)
	case PresetTrojanTLS:
		protocol, cfg = protocolTrojan, map[string]any{
			"settings":       map[string]any{"clients": []any{}, "fallbacks": []any{}},
			"streamSettings": tlsStream(networkTCP, "tcpSettings", tcpNone(), p, []string{"h2", "http/1.1"}),
		}
	case PresetShadowsocks2022:
		protocol, cfg, err = shadowsocks2022(Params{Method: ss2022AES256})
	default:
		protocol, cfg, err = hysteria2Salamander(p)
	}
	if err != nil {
		return panel.InboundSpec{}, err
	}
	cfg["sniffing"] = map[string]any{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "metadataOnly": false, "routeOnly": false}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return panel.InboundSpec{}, fmt.Errorf("encode preset config: %w", err)
	}
	return panel.InboundSpec{Protocol: protocol, Port: base.Port, Listen: base.Listen, Remark: base.Remark, Enabled: base.Enabled, Config: raw}, nil
}

func vlessRealityMLKEM(p PresetParams) (string, map[string]any, error) {
	isMLKEM := strings.HasPrefix(p.VLESSDecryption, "mlkem768x25519plus.") && strings.HasPrefix(p.VLESSEncryption, "mlkem768x25519plus.")
	if !isMLKEM {
		return "", nil, &ParamError{Message: "缺少面板生成的 ML-KEM-768 VLESS Encryption 参数"}
	}
	protocol, cfg, err := vlessReality(Params{Target: p.RealityTarget})
	if err != nil {
		return "", nil, err
	}
	settings, _ := cfg["settings"].(map[string]any)
	settings["decryption"], settings["encryption"] = p.VLESSDecryption, p.VLESSEncryption
	// Xray 不接受 decryption 与 fallbacks 同在（哪怕是空数组），带上整个内核起不来（测试机实测）
	delete(settings, "fallbacks")
	return protocol, cfg, nil
}

func vlessXHTTPTLS(p PresetParams) (string, map[string]any, error) {
	path, err := randomPath()
	if err != nil {
		return "", nil, err
	}
	xhttp := map[string]any{"path": path, "host": p.Domain, "mode": xhttpModeAuto, "headers": map[string]any{}}
	return protocolVless, map[string]any{
		"settings":       map[string]any{"clients": []any{}, "decryption": encryptionNone, "encryption": encryptionNone, "fallbacks": []any{}},
		"streamSettings": tlsStream(networkXHTTP, "xhttpSettings", xhttp, p, []string{"h2", "http/1.1"}),
	}, nil
}

func vmessWSTLS(p PresetParams) (string, map[string]any, error) {
	path, err := randomPath()
	if err != nil {
		return "", nil, err
	}
	ws := map[string]any{"acceptProxyProtocol": false, "path": path, "host": p.Domain, "headers": map[string]any{}}
	// WebSocket 走 TLS 时 ALPN 只能是 http/1.1（Ruling-53）
	return protocolVmess, map[string]any{
		"settings":       map[string]any{"clients": []any{}},
		"streamSettings": tlsStream(networkWS, "wsSettings", ws, p, []string{"http/1.1"}),
	}, nil
}

func hysteria2Salamander(p PresetParams) (string, map[string]any, error) {
	buf := make([]byte, salamanderPasswordBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("generate salamander password: %w", err)
	}
	stream := tlsStream(networkHysteria, "hysteriaSettings",
		map[string]any{"version": hysteriaVersion, "udpIdleTimeout": udpIdleTimeoutSeconds}, p, []string{"h3"})
	// 3x-ui v3.11 的 Salamander 在 FinalMask 的 UDP 掩码里；分享链接据此输出 obfs=salamander
	stream["finalmask"] = map[string]any{"udp": []any{map[string]any{
		"type": "salamander", "settings": map[string]any{"password": base64.RawURLEncoding.EncodeToString(buf)},
	}}}
	return protocolHysteria, map[string]any{
		"settings":       map[string]any{"version": hysteriaVersion, "clients": []any{}},
		"streamSettings": stream,
	}, nil
}

// tlsStream 是用主机域名证书的 TLS 传输：证书引用文件，续期后面板重启即生效（Ruling-74）。
func tlsStream(network, settingsKey string, settings map[string]any, p PresetParams, alpn []string) map[string]any {
	return map[string]any{
		"network": network, "security": securityTLS, settingsKey: settings,
		"tlsSettings": map[string]any{
			"serverName": p.Domain, "minVersion": "1.2", "maxVersion": "1.3", "alpn": alpn, "rejectUnknownSni": false,
			"certificates": []any{map[string]any{"certificateFile": p.CertFile, "keyFile": p.KeyFile, "usage": "encipherment", "buildChain": false, "ocspStapling": 0, "oneTimeLoading": false}},
			"settings":     map[string]any{"fingerprint": fingerprintChrome},
		},
	}
}

func tcpNone() map[string]any {
	return map[string]any{"acceptProxyProtocol": false, "header": map[string]any{"type": "none"}}
}

func randomPath() (string, error) {
	s, err := randomHex(presetPathBytes)
	if err != nil {
		return "", err
	}
	return "/" + s, nil
}

// RealityHost 取 REALITY 目标（域名:端口）的主机名；不合法时返回空串。
func RealityHost(target string) string {
	host, _, err := net.SplitHostPort(target)
	if err != nil || !hostnamePattern.MatchString(host) {
		return ""
	}
	return host
}
