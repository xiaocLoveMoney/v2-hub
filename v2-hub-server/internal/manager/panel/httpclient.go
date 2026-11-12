package panel

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultTimeout      = 15 * time.Second
	dialTimeout         = 10 * time.Second
	tlsHandshakeTimeout = 10 * time.Second
	maxIdleConnsPerHost = 4
	idleConnTimeout     = 90 * time.Second
	schemeHTTP          = "http"
	schemeHTTPS         = "https"
)

// ErrPinMismatch 表示面板证书与固定的 SHA-256 不符。
var ErrPinMismatch = errors.New("panel certificate does not match pinned sha256")

// ParseBaseURL 校验并规范化面板根地址：只允许 http / https，不带查询串与片段，去掉末尾斜杠。
func ParseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != schemeHTTP && u.Scheme != schemeHTTPS) {
		return nil, fmt.Errorf("panel base url must be http(s)://host[:port][/base-path]")
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("panel base url must not contain query, fragment or userinfo")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

// NewHTTPClient 按 TLS 模式创建面板专用的 HTTP 客户端；不跟随跨主机重定向。
func NewHTTPClient(cfg ConnConfig) (*http.Client, error) {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	tlsConfig, err := tlsConfigFor(cfg)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy:               nil, // 直连面板，不经环境代理
		DialContext:         (&net.Dialer{Timeout: dialTimeout}).DialContext,
		TLSClientConfig:     tlsConfig,
		TLSHandshakeTimeout: tlsHandshakeTimeout,
		MaxIdleConnsPerHost: maxIdleConnsPerHost,
		IdleConnTimeout:     idleConnTimeout,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		// 面板 API 不应重定向；跟随重定向可能把 Authorization 头带到别处
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

func tlsConfigFor(cfg ConnConfig) (*tls.Config, error) {
	switch cfg.TLSMode {
	case TLSVerify, "":
		return &tls.Config{MinVersion: tls.VersionTLS12}, nil
	case TLSSkip:
		//nolint:gosec // docs/03 §5.1：skip 模式仅供调试，界面明示风险
		return &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}, nil
	case TLSPin:
		want, err := base64.StdEncoding.DecodeString(cfg.CertPinSHA256)
		if err != nil || len(want) != sha256.Size {
			return nil, fmt.Errorf("certPinSha256 must be base64 of a sha256 digest")
		}
		return &tls.Config{
			MinVersion: tls.VersionTLS12,
			// 固定证书模式自己校验叶子证书指纹，不走 CA 链（自签证书场景）
			InsecureSkipVerify: true, //nolint:gosec // 由 VerifyPeerCertificate 做指纹校验
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return ErrPinMismatch
				}
				got := sha256.Sum256(rawCerts[0])
				if subtle.ConstantTimeCompare(got[:], want) != 1 {
					return ErrPinMismatch
				}
				return nil
			},
		}, nil
	default:
		return nil, fmt.Errorf("unknown tls mode %q", cfg.TLSMode)
	}
}

// CertFingerprint 不校验证书地连接 https 面板，返回叶子证书 SHA-256 的 Base64，供 pin 模式使用。
func CertFingerprint(ctx context.Context, baseURL string) (string, error) {
	u, err := ParseBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	if u.Scheme != schemeHTTPS {
		return "", fmt.Errorf("certificate fingerprint requires an https base url")
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "443")
	}
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: dialTimeout},
		//nolint:gosec // 只读取证书计算指纹，不传输任何数据
		Config: &tls.Config{InsecureSkipVerify: true, ServerName: u.Hostname(), MinVersion: tls.VersionTLS12},
	}
	conn, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return "", &Error{Kind: KindUnreachable, Message: "无法建立 TLS 连接", Err: err}
	}
	defer func() {
		// 只为读取证书建立的连接，关闭失败不影响结果，记录即可
		if cerr := conn.Close(); cerr != nil {
			slog.Debug("close fingerprint connection", "err", cerr)
		}
	}()
	tlsConn, ok := conn.(*tls.Conn)
	if !ok || len(tlsConn.ConnectionState().PeerCertificates) == 0 {
		return "", &Error{Kind: KindUnreachable, Message: "对端没有提供证书"}
	}
	sum := sha256.Sum256(tlsConn.ConnectionState().PeerCertificates[0].Raw)
	return base64.StdEncoding.EncodeToString(sum[:]), nil
}
