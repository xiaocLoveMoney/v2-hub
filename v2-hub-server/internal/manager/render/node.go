// Package render 把面板生成的分享链接解析为统一的 ProxyNode，再渲染成各订阅格式（docs/03 §5.4）。
// 本包与面板类型无关：输入只有分享链接，不接触任何面板接口。链接含凭据，本包不写日志。
package render

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// 协议名（ProxyNode.Protocol 的取值）。
const (
	ProtoVLESS       = "vless"
	ProtoVMess       = "vmess"
	ProtoTrojan      = "trojan"
	ProtoShadowsocks = "shadowsocks"
	ProtoHysteria2   = "hysteria2"
	ProtoSocks       = "socks" // SOCKS5，只来自链式出口（docs/03 §5.21）
	ProtoHTTP        = "http"  // HTTP / HTTPS 代理，只来自链式出口；HTTPS 即 Security 为 tls（Ruling-51）
)

// 传输与安全层取值。
const (
	NetTCP         = "tcp"
	NetWS          = "ws"
	NetGRPC        = "grpc"
	NetHTTPUpgrade = "httpupgrade"
	NetXHTTP       = "xhttp"

	SecNone    = "none"
	SecTLS     = "tls"
	SecReality = "reality"
)

const (
	schemeVLESS     = "vless"
	schemeVMess     = "vmess"
	schemeTrojan    = "trojan"
	schemeSS        = "ss"
	schemeHysteria2 = "hysteria2"
	schemeHy2       = "hy2"
	schemeSocks5    = "socks5"
	schemeSocks     = "socks"
	schemeHTTP      = "http"
	schemeHTTPS     = "https"
	schemeSep       = "://"

	vmessDefaultCipher = "auto"
	maxPort            = 65535
)

// encryptionNone 是 VLESS 不加密时 encryption 参数的取值。
const encryptionNone = "none"

// ErrUnsupportedLink 表示链接的协议或形状不在支持范围内（调用方跳过该链接）。
var ErrUnsupportedLink = errors.New("unsupported share link")

// ProxyNode 是一条代理节点的中立描述。未用到的字段保持零值。
type ProxyNode struct {
	Name     string
	Protocol string
	Address  string
	Port     int

	UUID     string // vless / vmess
	Username string // socks / http
	Password string // trojan / shadowsocks / hysteria2 / socks / http
	Method   string // shadowsocks 加密方式；vmess 的 scy
	Flow     string // vless flow
	// Encryption 是 VLESS Encryption 的客户端参数（链接的 encryption=），空或 none 为不加密（Ruling-77）。
	Encryption string
	AlterID  int    // vmess

	Network     string // tcp / ws / grpc / httpupgrade / xhttp ...
	Host        string // 传输层 Host 头
	Path        string
	ServiceName string // grpc
	HeaderType  string // tcp 伪装类型
	Mode        string // xhttp mode

	Security      string // none / tls / reality
	SNI           string
	ALPN          []string
	Fingerprint   string
	AllowInsecure bool
	PublicKey     string // reality
	ShortID       string // reality
	SpiderX       string // reality

	Obfs         string // hysteria2
	ObfsPassword string // hysteria2

	// Info 为 true 表示这是一条不可连通的说明节点（剩余流量、到期时间等）。
	Info bool
}

// hasVLESSEncryption 报告节点是否开了 VLESS Encryption；不认识它的格式不能把它当成不加密输出。
func (n ProxyNode) hasVLESSEncryption() bool {
	return n.Protocol == ProtoVLESS && n.Encryption != "" && n.Encryption != encryptionNone
}

// ParseLink 把一条分享链接解析为 ProxyNode。协议不支持或形状不对返回 ErrUnsupportedLink。
func ParseLink(link string) (ProxyNode, error) {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(link), schemeSep)
	if !ok {
		return ProxyNode{}, ErrUnsupportedLink
	}
	switch strings.ToLower(scheme) {
	case schemeVMess:
		return parseVMess(rest)
	case schemeVLESS:
		return parseURLLink(link, ProtoVLESS)
	case schemeTrojan:
		return parseURLLink(link, ProtoTrojan)
	case schemeHysteria2, schemeHy2:
		return parseURLLink(link, ProtoHysteria2)
	case schemeSS:
		return parseShadowsocks(link)
	case schemeSocks5, schemeSocks:
		return parseSocks(link)
	case schemeHTTP:
		return parseHTTPProxy(link, SecNone)
	case schemeHTTPS:
		return parseHTTPProxy(link, SecTLS)
	default:
		return ProxyNode{}, ErrUnsupportedLink
	}
}

func splitHostPort(u *url.URL) (string, int, error) {
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > maxPort || u.Hostname() == "" {
		return "", 0, ErrUnsupportedLink
	}
	return u.Hostname(), port, nil
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func isTruthy(s string) bool {
	return s == "1" || strings.EqualFold(s, "true")
}

// parseURLLink 解析 vless / trojan / hysteria2 这类 scheme://secret@host:port?query#name 形状的链接。
func parseURLLink(link, proto string) (ProxyNode, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.User == nil {
		return ProxyNode{}, ErrUnsupportedLink
	}
	host, port, err := splitHostPort(u)
	if err != nil {
		return ProxyNode{}, err
	}
	q := u.Query()
	n := ProxyNode{Name: u.Fragment, Protocol: proto, Address: host, Port: port}
	secret := u.User.Username()
	if secret == "" {
		return ProxyNode{}, ErrUnsupportedLink
	}
	n.SNI = q.Get("sni")
	n.ALPN = splitCSV(q.Get("alpn"))
	n.Fingerprint = q.Get("fp")
	n.AllowInsecure = isTruthy(q.Get("allowInsecure")) || isTruthy(q.Get("insecure"))
	if proto == ProtoHysteria2 {
		n.Password = secret
		n.Network, n.Security = NetTCP, SecTLS
		n.Obfs, n.ObfsPassword = q.Get("obfs"), q.Get("obfs-password")
		return n, nil
	}
	if proto == ProtoVLESS {
		n.UUID, n.Flow = secret, q.Get("flow")
		if enc := q.Get("encryption"); enc != encryptionNone {
			n.Encryption = enc
		}
	} else {
		n.Password = secret
	}
	n.Network = orDefault(q.Get("type"), NetTCP)
	n.Security = orDefault(q.Get("security"), SecNone)
	n.Host, n.Path = q.Get("host"), q.Get("path")
	n.ServiceName, n.HeaderType, n.Mode = q.Get("serviceName"), q.Get("headerType"), q.Get("mode")
	n.PublicKey, n.ShortID, n.SpiderX = q.Get("pbk"), q.Get("sid"), q.Get("spx")
	return n, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// vmessJSON 是 vmess:// 后 Base64 里的 JSON；port 与 aid 在不同实现里可能是数字或字符串。
type vmessJSON struct {
	PS   string      `json:"ps"`
	Add  string      `json:"add"`
	Port json.Number `json:"port"`
	ID   string      `json:"id"`
	Aid  json.Number `json:"aid"`
	Scy  string      `json:"scy"`
	Net  string      `json:"net"`
	Type string      `json:"type"`
	Host string      `json:"host"`
	Path string      `json:"path"`
	TLS  string      `json:"tls"`
	SNI  string      `json:"sni"`
	ALPN string      `json:"alpn"`
	FP   string      `json:"fp"`
}

func decodeBase64Loose(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawURLEncoding.DecodeString(s)
}

func parseVMess(payload string) (ProxyNode, error) {
	raw, err := decodeBase64Loose(payload)
	if err != nil {
		return ProxyNode{}, ErrUnsupportedLink
	}
	var v vmessJSON
	if err := json.Unmarshal(raw, &v); err != nil {
		return ProxyNode{}, ErrUnsupportedLink
	}
	port, err := strconv.Atoi(v.Port.String())
	if err != nil || port < 1 || port > maxPort || v.Add == "" || v.ID == "" {
		return ProxyNode{}, ErrUnsupportedLink
	}
	aid := 0
	if s := v.Aid.String(); s != "" {
		if aid, err = strconv.Atoi(s); err != nil {
			return ProxyNode{}, ErrUnsupportedLink
		}
	}
	n := ProxyNode{
		Name: v.PS, Protocol: ProtoVMess, Address: v.Add, Port: port, UUID: v.ID, AlterID: aid,
		Method: orDefault(v.Scy, vmessDefaultCipher), Network: orDefault(v.Net, NetTCP),
		Host: v.Host, Path: v.Path, HeaderType: v.Type, SNI: v.SNI, ALPN: splitCSV(v.ALPN), Fingerprint: v.FP,
		Security: SecNone,
	}
	if v.TLS == SecTLS {
		n.Security = SecTLS
	}
	if n.Network == NetGRPC {
		n.ServiceName, n.Path = v.Path, ""
	}
	return n, nil
}

// parseShadowsocks 解析 SIP002：ss://base64(method:password)@host:port#name，也接受整体 Base64 的旧形状。
func parseShadowsocks(link string) (ProxyNode, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return ProxyNode{}, ErrUnsupportedLink
	}
	if u.User == nil {
		return parseLegacyShadowsocks(u)
	}
	host, port, err := splitHostPort(u)
	if err != nil {
		return ProxyNode{}, err
	}
	userinfo := u.User.String()
	if pw, hasPW := u.User.Password(); hasPW {
		// 未做 Base64 的 method:password 形状
		return ProxyNode{Name: u.Fragment, Protocol: ProtoShadowsocks, Address: host, Port: port, Method: u.User.Username(), Password: pw, Network: NetTCP, Security: SecNone}, nil
	}
	if unescaped, uerr := url.PathUnescape(userinfo); uerr == nil {
		userinfo = unescaped
	}
	raw, err := decodeBase64Loose(userinfo)
	if err != nil {
		return ProxyNode{}, ErrUnsupportedLink
	}
	method, password, ok := strings.Cut(string(raw), ":")
	if !ok || method == "" || password == "" {
		return ProxyNode{}, ErrUnsupportedLink
	}
	return ProxyNode{Name: u.Fragment, Protocol: ProtoShadowsocks, Address: host, Port: port, Method: method, Password: password, Network: NetTCP, Security: SecNone}, nil
}

func parseLegacyShadowsocks(u *url.URL) (ProxyNode, error) {
	raw, err := decodeBase64Loose(u.Host + u.Path)
	if err != nil {
		return ProxyNode{}, ErrUnsupportedLink
	}
	cred, addr, ok := cutLast(string(raw), "@")
	if !ok {
		return ProxyNode{}, ErrUnsupportedLink
	}
	method, password, ok := strings.Cut(cred, ":")
	host, portStr, err := net.SplitHostPort(addr)
	if !ok || err != nil {
		return ProxyNode{}, ErrUnsupportedLink
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > maxPort {
		return ProxyNode{}, ErrUnsupportedLink
	}
	return ProxyNode{Name: u.Fragment, Protocol: ProtoShadowsocks, Address: host, Port: port, Method: method, Password: password, Network: NetTCP, Security: SecNone}, nil
}

func cutLast(s, sep string) (string, string, bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}

// parseSocks 解析 socks5://[user:pass@]host:port#name；也接受 v2rayN 的 socks://base64(user:pass)@host:port 形状。
func parseSocks(link string) (ProxyNode, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return ProxyNode{}, ErrUnsupportedLink
	}
	host, port, err := splitHostPort(u)
	if err != nil {
		return ProxyNode{}, err
	}
	n := ProxyNode{Name: u.Fragment, Protocol: ProtoSocks, Address: host, Port: port, Network: NetTCP, Security: SecNone}
	if u.User == nil {
		return n, nil
	}
	if pw, hasPW := u.User.Password(); hasPW {
		n.Username, n.Password = u.User.Username(), pw
		return n, nil
	}
	raw, err := decodeBase64Loose(u.User.Username())
	if err != nil {
		return ProxyNode{}, ErrUnsupportedLink
	}
	user, pw, ok := strings.Cut(string(raw), ":")
	if !ok || user == "" {
		return ProxyNode{}, ErrUnsupportedLink
	}
	n.Username, n.Password = user, pw
	return n, nil
}

// parseHTTPProxy 解析 http://[user:pass@]host:port#name 与 https://…（HTTPS 代理可带 sni、allowInsecure 参数）。
// 只认“地址 + 端口”：带路径的是网页地址，不当作代理。
func parseHTTPProxy(link, security string) (ProxyNode, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || (u.Path != "" && u.Path != "/") {
		return ProxyNode{}, ErrUnsupportedLink
	}
	host, port, err := splitHostPort(u)
	if err != nil {
		return ProxyNode{}, err
	}
	n := ProxyNode{Name: u.Fragment, Protocol: ProtoHTTP, Address: host, Port: port, Network: NetTCP, Security: security}
	if u.User != nil {
		n.Username = u.User.Username()
		n.Password, _ = u.User.Password()
	}
	if n.Username == "" && n.Password != "" {
		return ProxyNode{}, ErrUnsupportedLink
	}
	if security == SecTLS {
		q := u.Query()
		n.SNI = q.Get("sni")
		n.AllowInsecure = isTruthy(q.Get("allowInsecure")) || isTruthy(q.Get("insecure"))
	}
	return n, nil
}

// Rewrite 返回把名称与地址换成 name、address 后的链接，其余参数原样保留（Base64 订阅用）。
// address 为空表示不改地址。
func Rewrite(link, name, address string) (string, error) {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(link), schemeSep)
	if !ok {
		return "", ErrUnsupportedLink
	}
	if strings.EqualFold(scheme, schemeVMess) {
		return rewriteVMess(rest, name, address)
	}
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Host == "" {
		return "", ErrUnsupportedLink
	}
	if address != "" && u.Port() != "" {
		u.Host = net.JoinHostPort(address, u.Port())
	}
	u.Fragment = name
	return u.String(), nil
}

func rewriteVMess(payload, name, address string) (string, error) {
	raw, err := decodeBase64Loose(payload)
	if err != nil {
		return "", ErrUnsupportedLink
	}
	var fields map[string]any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&fields); err != nil {
		return "", ErrUnsupportedLink
	}
	fields["ps"] = name
	if address != "" {
		fields["add"] = address
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("encode vmess link: %w", err)
	}
	return schemeVMess + schemeSep + base64.StdEncoding.EncodeToString(out), nil
}
