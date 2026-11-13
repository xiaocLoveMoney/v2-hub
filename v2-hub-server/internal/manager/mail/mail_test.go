package mail

import (
	"bufio"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	netmail "net/mail"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeSMTP 是只懂最小对话的明文 SMTP 服务器，记下收到的信封与报文。
type fakeSMTP struct {
	ln       net.Listener
	mu       sync.Mutex
	from, to string
	data     string
	done     chan struct{}
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeSMTP{ln: ln, done: make(chan struct{})}
	t.Cleanup(func() {
		if err := ln.Close(); err != nil {
			t.Errorf("close listener: %v", err)
		}
		<-f.done
	})
	go f.serve()
	return f
}

func (f *fakeSMTP) port(t *testing.T) int {
	t.Helper()
	_, p, err := net.SplitHostPort(f.ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	return n
}

func (f *fakeSMTP) serve() {
	defer close(f.done)
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return // 监听已关闭
		}
		f.handle(conn)
	}
}

func (f *fakeSMTP) handle(conn net.Conn) {
	defer conn.Close() //nolint:errcheck // 测试用服务器，关闭错误无意义
	r := bufio.NewReader(conn)
	say := func(s string) { io.WriteString(conn, s+"\r\n") } //nolint:errcheck // 客户端断开时写失败，由客户端一侧的断言发现
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			say("250 fake")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			f.mu.Lock()
			f.from = strings.TrimSpace(line[len("MAIL FROM:"):])
			f.mu.Unlock()
			say("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			f.mu.Lock()
			f.to = strings.TrimSpace(line[len("RCPT TO:"):])
			f.mu.Unlock()
			say("250 ok")
		case cmd == "DATA":
			say("354 go")
			var sb strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				sb.WriteString(l)
			}
			f.mu.Lock()
			f.data = sb.String()
			f.mu.Unlock()
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("502 not implemented")
		}
	}
}

func TestSMTPSenderSendsMultipart(t *testing.T) {
	srv := newFakeSMTP(t)
	sender, err := NewSMTPSender(Config{Host: "127.0.0.1", Port: srv.port(t), From: "noreply@example.com", TLS: TLSNone})
	if err != nil {
		t.Fatalf("NewSMTPSender: %v", err)
	}
	msg := Message{To: "alice@example.com", Subject: "验证码 123456", Text: "你的验证码是 123456", HTML: "<p>你的验证码是 <b>123456</b></p>"}
	if err := sender.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if !strings.Contains(srv.from, "noreply@example.com") || !strings.Contains(srv.to, "alice@example.com") {
		t.Fatalf("envelope = %q → %q", srv.from, srv.to)
	}
	parsed, err := netmail.ReadMessage(strings.NewReader(srv.data))
	if err != nil {
		t.Fatalf("parse message: %v", err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil || subject != msg.Subject {
		t.Fatalf("subject = %q, %v", subject, err)
	}
	mediaType, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" {
		t.Fatalf("content type = %q, %v", mediaType, err)
	}
	mr := multipart.NewReader(parsed.Body, params["boundary"])
	want := []string{msg.Text, msg.HTML}
	for i := range want {
		part, err := mr.NextPart()
		if err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
		// multipart.Reader 已按 Content-Transfer-Encoding 透明解码 quoted-printable
		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read part %d: %v", i, err)
		}
		if got := strings.TrimSpace(string(body)); got != want[i] {
			t.Fatalf("part %d = %q, want %q", i, got, want[i])
		}
	}
}

func TestSMTPSenderPlainTextOnly(t *testing.T) {
	srv := newFakeSMTP(t)
	sender, err := NewSMTPSender(Config{Host: "127.0.0.1", Port: srv.port(t), From: "noreply@example.com", TLS: TLSNone})
	if err != nil {
		t.Fatalf("NewSMTPSender: %v", err)
	}
	if err := sender.Send(context.Background(), Message{To: "bob@example.com", Subject: "hi", Text: "纯文本"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	parsed, err := netmail.ReadMessage(strings.NewReader(srv.data))
	if err != nil {
		t.Fatalf("parse message: %v", err)
	}
	body, err := io.ReadAll(quotedprintable.NewReader(parsed.Body))
	if err != nil || strings.TrimSpace(string(body)) != "纯文本" {
		t.Fatalf("body = %q, %v", body, err)
	}
}

func TestSMTPSenderRejectsHeaderInjection(t *testing.T) {
	sender, err := NewSMTPSender(Config{Host: "127.0.0.1", Port: 1, From: "noreply@example.com", TLS: TLSNone})
	if err != nil {
		t.Fatalf("NewSMTPSender: %v", err)
	}
	for _, m := range []Message{
		{To: "a@example.com\r\nBcc: evil@example.com", Subject: "x", Text: "x"},
		{To: "a@example.com", Subject: "x\nBcc: evil@example.com", Text: "x"},
		{To: "", Subject: "x", Text: "x"},
	} {
		if err := sender.Send(context.Background(), m); !errors.Is(err, ErrBadMessage) {
			t.Fatalf("Send(%q, %q) = %v, want ErrBadMessage", m.To, m.Subject, err)
		}
	}
}

func TestSMTPSenderRequiresStartTLS(t *testing.T) {
	srv := newFakeSMTP(t) // 不通告 STARTTLS
	sender, err := NewSMTPSender(Config{Host: "127.0.0.1", Port: srv.port(t), From: "noreply@example.com"})
	if err != nil {
		t.Fatalf("NewSMTPSender: %v", err)
	}
	err = sender.Send(context.Background(), Message{To: "a@example.com", Subject: "x", Text: "x"})
	if !errors.Is(err, ErrNoStartTLS) {
		t.Fatalf("Send = %v, want ErrNoStartTLS (must not downgrade)", err)
	}
}

func TestNewSMTPSenderConfig(t *testing.T) {
	if _, err := NewSMTPSender(Config{}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("empty config = %v, want ErrDisabled", err)
	}
	if _, err := NewSMTPSender(Config{Host: "h", From: "a@b.c", TLS: "ssl3"}); !errors.Is(err, ErrBadConfig) {
		t.Fatalf("bad tls mode = %v, want ErrBadConfig", err)
	}
	if _, err := NewSMTPSender(Config{Host: "h", From: "a@b.c\r\nX: y"}); !errors.Is(err, ErrBadConfig) {
		t.Fatalf("from with line break = %v, want ErrBadConfig", err)
	}
}
