// Package inboundtpl 按模板生成 Xray 入站配置（docs/03 §5.1、Ruling-15）。
// 密钥（Reality x25519、SS2022 服务端密钥）在这里生成，不经浏览器。生成结果是 panel.InboundSpec，
// 由 Provider 写入面板；模板只覆盖常用组合，其余协议走高级 JSON。
package inboundtpl

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"v2-hub-server/internal/manager/panel"
)

// 模板标识，与 hub-api.yaml InboundTemplate 枚举一致。
const (
	VlessReality    = "vless-reality"
	VmessWS         = "vmess-ws"
	Shadowsocks2022 = "shadowsocks-2022"
)

const (
	protocolVless       = "vless"
	protocolVmess       = "vmess"
	protocolShadowsocks = "shadowsocks"

	ss2022AES256      = "2022-blake3-aes-256-gcm"
	ss2022AES128      = "2022-blake3-aes-128-gcm"
	ss2022AES256Bytes = 32
	ss2022AES128Bytes = 16
	shortIDBytes      = 8
	maxServerNames    = 8
	defaultWSPath     = "/"
	fingerprintChrome = "chrome"
)

var (
	// ErrUnknownTemplate 表示模板标识未知。
	ErrUnknownTemplate = errors.New("unknown inbound template")
	wsPathPattern      = regexp.MustCompile(`^/[A-Za-z0-9._~\-/]{0,127}$`)
	hostnamePattern    = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9\-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9\-]{0,61}[A-Za-z0-9])?)+$`)
)

// Params 是模板参数（字段随模板不同，未用到的忽略）。
type Params struct {
	// Target 是 Reality 伪装目标，形如 www.cloudflare.com:443。
	Target string `json:"target"`
	// ServerNames 是 Reality 允许的 SNI 列表；为空时取 Target 的主机名。
	ServerNames []string `json:"serverNames"`
	// Path 是 WebSocket 路径。
	Path string `json:"path"`
	// Method 是 SS2022 加密方式。
	Method string `json:"method"`
}

// Base 是所有模板共用的基本字段。
type Base struct {
	Remark  string
	Port    int
	Listen  string
	Enabled bool
}

// ParamError 表示模板参数不合法，Message 可直接展示。
type ParamError struct{ Message string }

func (e *ParamError) Error() string { return e.Message }

// Build 按模板生成入站规格。
func Build(template string, base Base, params Params) (panel.InboundSpec, error) {
	var (
		protocol string
		cfg      map[string]any
		err      error
	)
	switch template {
	case VlessReality:
		protocol, cfg, err = vlessReality(params)
	case VmessWS:
		protocol, cfg, err = vmessWS(params)
	case Shadowsocks2022:
		protocol, cfg, err = shadowsocks2022(params)
	default:
		return panel.InboundSpec{}, ErrUnknownTemplate
	}
	if err != nil {
		return panel.InboundSpec{}, err
	}
	return finishSpec(protocol, cfg, base)
}

// finishSpec 给模板或预设拼好的配置补上默认的 sniffing，编码成入站规格。
func finishSpec(protocol string, cfg map[string]any, base Base) (panel.InboundSpec, error) {
	cfg["sniffing"] = map[string]any{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "metadataOnly": false, "routeOnly": false}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return panel.InboundSpec{}, fmt.Errorf("encode inbound config: %w", err)
	}
	return panel.InboundSpec{Protocol: protocol, Port: base.Port, Listen: base.Listen, Remark: base.Remark, Enabled: base.Enabled, Config: raw}, nil
}

func vlessReality(p Params) (string, map[string]any, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(p.Target))
	if err != nil || !hostnamePattern.MatchString(host) {
		return "", nil, &ParamError{Message: "Reality 目标须为 域名:端口，如 www.cloudflare.com:443"}
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", nil, &ParamError{Message: "Reality 目标端口不合法"}
	}
	names := p.ServerNames
	if len(names) == 0 {
		names = []string{host}
	}
	if len(names) > maxServerNames {
		return "", nil, &ParamError{Message: fmt.Sprintf("serverNames 最多 %d 个", maxServerNames)}
	}
	for _, n := range names {
		if !hostnamePattern.MatchString(n) {
			return "", nil, &ParamError{Message: "serverNames 里有不合法的域名：" + n}
		}
	}
	priv, pub, err := newX25519()
	if err != nil {
		return "", nil, err
	}
	shortID, err := randomHex(shortIDBytes)
	if err != nil {
		return "", nil, err
	}
	return protocolVless, map[string]any{
		"settings": map[string]any{"clients": []any{}, "decryption": "none", "fallbacks": []any{}},
		"streamSettings": map[string]any{
			"network":  "tcp",
			"security": "reality",
			"realitySettings": map[string]any{
				"show": false, "xver": 0, "target": host + ":" + port, "serverNames": names,
				"privateKey": priv, "shortIds": []string{shortID},
				"settings": map[string]any{"publicKey": pub, "fingerprint": fingerprintChrome, "serverName": "", "spiderX": "/"},
			},
			"tcpSettings": tcpNone(),
		},
	}, nil
}

func vmessWS(p Params) (string, map[string]any, error) {
	path := strings.TrimSpace(p.Path)
	if path == "" {
		path = defaultWSPath
	}
	if !wsPathPattern.MatchString(path) {
		return "", nil, &ParamError{Message: "WebSocket 路径须以 / 开头，只含字母、数字与 . _ ~ - /"}
	}
	return protocolVmess, map[string]any{
		"settings": map[string]any{"clients": []any{}},
		"streamSettings": map[string]any{
			"network": "ws", "security": "none",
			"wsSettings": map[string]any{"acceptProxyProtocol": false, "path": path, "headers": map[string]any{}},
		},
	}, nil
}

func shadowsocks2022(p Params) (string, map[string]any, error) {
	method := p.Method
	if method == "" {
		method = ss2022AES256
	}
	var keyLen int
	switch method {
	case ss2022AES256:
		keyLen = ss2022AES256Bytes
	case ss2022AES128:
		keyLen = ss2022AES128Bytes
	default:
		return "", nil, &ParamError{Message: "SS2022 加密方式只支持 " + ss2022AES256 + " / " + ss2022AES128}
	}
	key := make([]byte, keyLen)
	if _, err := rand.Read(key); err != nil {
		return "", nil, fmt.Errorf("generate ss2022 key: %w", err)
	}
	return protocolShadowsocks, map[string]any{
		"settings": map[string]any{
			"method": method, "password": base64.StdEncoding.EncodeToString(key),
			"network": "tcp,udp", "clients": []any{},
		},
		"streamSettings": map[string]any{
			"network": "tcp", "security": "none",
			"tcpSettings": tcpNone(),
		},
	}, nil
}

// newX25519 生成 Reality 用的 x25519 密钥对，编码与 xray x25519 命令一致（URL 安全 Base64、无填充）。
func newX25519() (privateKey, publicKey string, err error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate x25519 key: %w", err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(key.Bytes()), enc.EncodeToString(key.PublicKey().Bytes()), nil
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random hex: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
