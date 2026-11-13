// Package mail 封装邮件发送：Sender 接口与 SMTP 实现。配置只来自环境变量（Ruling-24），
// 业务层只依赖 Sender，不关心传输细节（docs/03 §5.7）。
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// 加密方式。
const (
	TLSStartTLS = "starttls" // 明文连接后升级，服务器不支持即失败（不降级）
	TLSImplicit = "tls"      // 连接即 TLS（通常是 465 端口）
	TLSNone     = "none"     // 不加密，只用于本机或联调
)

const (
	defaultPort    = 587
	defaultTimeout = 20 * time.Second
	boundaryBytes  = 12
	messageIDBytes = 12
	crlf           = "\r\n"
	extStartTLS    = "STARTTLS"
)

var (
	// ErrDisabled 表示邮件未配置（主机或发件人为空）。
	ErrDisabled = errors.New("mail is not configured")
	// ErrBadMessage 表示收件人或主题含换行等不允许的字符（防邮件头注入）。
	ErrBadMessage = errors.New("mail recipient or subject is invalid")
	// ErrNoStartTLS 表示要求 STARTTLS 而服务器不支持。
	ErrNoStartTLS = errors.New("smtp server does not support STARTTLS")
	// ErrBadConfig 表示配置取值不合法。
	ErrBadConfig = errors.New("mail config is invalid")
)

// Config 是 SMTP 配置。
type Config struct {
	// Host 为空即邮件不可用。
	Host string
	// Port 为 0 时取 587。
	Port int
	// Username 为空时不做认证。
	Username string
	// Secret 是 SMTP 口令或授权码。
	Secret string
	// From 是发件人地址；为空即邮件不可用。
	From string
	// TLS 取 TLSStartTLS / TLSImplicit / TLSNone，为空时取 TLSStartTLS。
	TLS string
	// Timeout 是单封邮件从连接到发完的总时限，为 0 时取 20 秒。
	Timeout time.Duration
}

// Enabled 报告邮件是否已配置。
func (c Config) Enabled() bool {
	return c.Host != "" && c.From != ""
}

// Message 是一封邮件。Text 必填；HTML 非空时发 multipart/alternative。
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Sender 能发出一封邮件。
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// SMTPSender 是 Sender 的 SMTP 实现；每封邮件一条连接，并发安全。
type SMTPSender struct {
	cfg Config
}

// NewSMTPSender 校验配置并创建发送器。未配置返回 ErrDisabled，取值不合法返回 ErrBadConfig。
func NewSMTPSender(cfg Config) (*SMTPSender, error) {
	if !cfg.Enabled() {
		return nil, ErrDisabled
	}
	if cfg.Port == 0 {
		cfg.Port = defaultPort
	}
	if cfg.TLS == "" {
		cfg.TLS = TLSStartTLS
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.TLS != TLSStartTLS && cfg.TLS != TLSImplicit && cfg.TLS != TLSNone {
		return nil, fmt.Errorf("%w: tls mode %q", ErrBadConfig, cfg.TLS)
	}
	if hasLineBreak(cfg.From) || hasLineBreak(cfg.Host) || cfg.Port < 1 || cfg.Port > 65535 {
		return nil, fmt.Errorf("%w: host, port or from", ErrBadConfig)
	}
	return &SMTPSender{cfg: cfg}, nil
}

func hasLineBreak(s string) bool {
	return strings.ContainsAny(s, "\r\n")
}

// Send 发出一封邮件。收件人或主题含换行返回 ErrBadMessage；其余为连接或 SMTP 协议错误。
func (s *SMTPSender) Send(ctx context.Context, m Message) error {
	if m.To == "" || hasLineBreak(m.To) || hasLineBreak(m.Subject) {
		return ErrBadMessage
	}
	body, err := s.build(m)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	conn, err := s.dial(ctx)
	if err != nil {
		return fmt.Errorf("dial smtp: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return errors.Join(fmt.Errorf("set smtp deadline: %w", err), conn.Close())
		}
	}
	client, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return errors.Join(fmt.Errorf("smtp greeting: %w", err), conn.Close())
	}
	if err := s.deliver(client, m.To, body); err != nil {
		return errors.Join(err, client.Close())
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("smtp quit: %w", err)
	}
	return nil
}

func (s *SMTPSender) dial(ctx context.Context) (net.Conn, error) {
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	if s.cfg.TLS == TLSImplicit {
		d := tls.Dialer{Config: s.tlsConfig()}
		return d.DialContext(ctx, "tcp", addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr)
}

func (s *SMTPSender) tlsConfig() *tls.Config {
	return &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}
}

func (s *SMTPSender) deliver(c *smtp.Client, to string, body []byte) error {
	if s.cfg.TLS == TLSStartTLS {
		if ok, _ := c.Extension(extStartTLS); !ok {
			return ErrNoStartTLS
		}
		if err := c.StartTLS(s.tlsConfig()); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}
	if s.cfg.Username != "" {
		// PlainAuth 自身拒绝在未加密的非本机连接上发送口令
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Secret, s.cfg.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(s.cfg.From); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt to: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return errors.Join(fmt.Errorf("smtp write body: %w", err), w.Close())
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp end data: %w", err)
	}
	return nil
}

// build 组装 RFC 5322 报文：主题按 RFC 2047 编码，正文 quoted-printable。
func (s *SMTPSender) build(m Message) ([]byte, error) {
	msgID, err := randomHex(messageIDBytes)
	if err != nil {
		return nil, err
	}
	domain := s.cfg.Host
	if at := strings.LastIndexByte(s.cfg.From, '@'); at >= 0 {
		domain = s.cfg.From[at+1:]
	}
	var buf bytes.Buffer
	header := func(k, v string) { buf.WriteString(k + ": " + v + crlf) }
	header("From", s.cfg.From)
	header("To", m.To)
	header("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	header("Date", time.Now().Format(time.RFC1123Z))
	header("Message-ID", "<"+msgID+"@"+domain+">")
	header("MIME-Version", "1.0")
	if m.HTML == "" {
		if err := writePart(&buf, "text/plain", m.Text); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	boundary, err := randomHex(boundaryBytes)
	if err != nil {
		return nil, err
	}
	header("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	buf.WriteString(crlf)
	for _, part := range [][2]string{{"text/plain", m.Text}, {"text/html", m.HTML}} {
		buf.WriteString("--" + boundary + crlf)
		if err := writePart(&buf, part[0], part[1]); err != nil {
			return nil, err
		}
		buf.WriteString(crlf)
	}
	buf.WriteString("--" + boundary + "--" + crlf)
	return buf.Bytes(), nil
}

func writePart(buf *bytes.Buffer, contentType, content string) error {
	buf.WriteString("Content-Type: " + contentType + "; charset=utf-8" + crlf)
	buf.WriteString("Content-Transfer-Encoding: quoted-printable" + crlf + crlf)
	w := quotedprintable.NewWriter(buf)
	if _, err := w.Write([]byte(content)); err != nil {
		return fmt.Errorf("encode mail body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("encode mail body: %w", err)
	}
	return nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random: %w", err)
	}
	return hex.EncodeToString(b), nil
}
