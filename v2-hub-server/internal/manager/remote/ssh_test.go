package remote

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	testUser     = "deploy"
	testPassword = "correct-horse"
	testTimeout  = 5 * time.Second
)

// execHandler 模拟远端执行一条命令：返回输出与退出码。
type execHandler func(cmd string, stdin []byte) (string, uint32)

type testServer struct {
	addr     *net.TCPAddr
	hostKey  ssh.Signer
	mu       sync.Mutex
	commands []string
}

func (s *testServer) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

func newSigner(t *testing.T) (ssh.Signer, []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return signer, pem.EncodeToMemory(block)
}

// startServer 起一个进程内 SSH 服务端：只认 testUser 的 clientKey 或 testPassword，exec 请求交给 handler。
func startServer(t *testing.T, clientKey ssh.PublicKey, handler execHandler) *testServer {
	t.Helper()
	hostKey, _ := newSigner(t)
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == testUser && string(pass) == testPassword {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if c.User() == testUser && clientKey != nil && string(key.Marshal()) == string(clientKey.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(hostKey)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() }) // 测试收尾，关闭失败不影响断言
	srv := &testServer{addr: ln.Addr().(*net.TCPAddr), hostKey: hostKey}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.serve(conn, cfg, handler)
		}
	}()
	return srv
}

func (s *testServer) serve(conn net.Conn, cfg *ssh.ServerConfig, handler execHandler) {
	sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer func() { _ = sc.Close() }() // 测试服务端，连接多由客户端先关
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "session only") // 测试里没有别的通道类型
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			return
		}
		go s.session(ch, chReqs, handler)
	}
}

func (s *testServer) session(ch ssh.Channel, reqs <-chan *ssh.Request, handler execHandler) {
	defer func() { _ = ch.Close() }() // 同上
	for req := range reqs {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
			_ = req.Reply(false, nil)
			return
		}
		_ = req.Reply(true, nil)
		s.mu.Lock()
		s.commands = append(s.commands, payload.Command)
		s.mu.Unlock()
		var stdin []byte
		if strings.Contains(payload.Command, "cat >") {
			stdin, _ = io.ReadAll(ch)
		}
		out, code := handler(payload.Command, stdin)
		_, _ = io.WriteString(ch, out)
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{code}))
		return
	}
}

// rootHandler 模拟 root 登录：id -u 返回 0，其余命令回显。
func rootHandler(cmd string, _ []byte) (string, uint32) {
	if cmd == cmdWhoami {
		return "0\n", 0
	}
	return "ran:" + cmd, 0
}

func target(srv *testServer, key []byte) Target {
	return Target{Address: srv.addr.IP.String(), Port: srv.addr.Port, Username: testUser, PrivateKey: key}
}

func dialer() *SSHDialer {
	return NewSSHDialer(Config{ConnectTimeout: testTimeout, AllowLoopback: true})
}

func mustDial(t *testing.T, tg Target) Runner {
	t.Helper()
	r, err := dialer().Dial(context.Background(), tg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() }) // 测试收尾
	return r
}

func wantKind(t *testing.T, err error, kind ErrorKind) {
	t.Helper()
	re := AsError(err)
	if re == nil || re.Kind != kind {
		t.Fatalf("err = %v, want kind %s", err, kind)
	}
}

func TestDialWithKeyRunsAsRoot(t *testing.T) {
	signer, keyPEM := newSigner(t)
	srv := startServer(t, signer.PublicKey(), rootHandler)
	r := mustDial(t, target(srv, keyPEM))
	if !r.IsPrivileged() {
		t.Fatal("root login should be privileged")
	}
	if got, want := r.HostKeySHA256(), ssh.FingerprintSHA256(srv.hostKey.PublicKey()); got != want {
		t.Fatalf("host key = %s, want %s", got, want)
	}
	res, err := r.Run(context.Background(), "echo it's")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// root 不加 sudo；脚本整体是一个单引号字面量，里面的单引号被转义
	if want := `ran:sh -c 'echo it'\''s'`; res.Output != want {
		t.Fatalf("output = %q, want %q", res.Output, want)
	}
}

func TestDialWithPassword(t *testing.T) {
	srv := startServer(t, nil, rootHandler)
	tg := target(srv, nil)
	tg.Password = testPassword
	mustDial(t, tg)

	tg.Password = "wrong"
	_, err := dialer().Dial(context.Background(), tg)
	wantKind(t, err, KindAuth)
}

func TestDialRejectsUnknownKey(t *testing.T) {
	signer, _ := newSigner(t)
	_, otherPEM := newSigner(t)
	srv := startServer(t, signer.PublicKey(), rootHandler)
	_, err := dialer().Dial(context.Background(), target(srv, otherPEM))
	wantKind(t, err, KindAuth)
}

func TestDialPinsHostKey(t *testing.T) {
	signer, keyPEM := newSigner(t)
	srv := startServer(t, signer.PublicKey(), rootHandler)
	tg := target(srv, keyPEM)

	tg.HostKeySHA256 = ssh.FingerprintSHA256(srv.hostKey.PublicKey())
	mustDial(t, tg)

	tg.HostKeySHA256 = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	_, err := dialer().Dial(context.Background(), tg)
	wantKind(t, err, KindHostKey)
}

func TestDialRefusesLoopbackByDefault(t *testing.T) {
	signer, keyPEM := newSigner(t)
	srv := startServer(t, signer.PublicKey(), rootHandler)
	_, err := NewSSHDialer(Config{ConnectTimeout: testTimeout}).Dial(context.Background(), target(srv, keyPEM))
	wantKind(t, err, KindUnreachable)
	if len(srv.recorded()) != 0 {
		t.Fatal("no command should reach a refused host")
	}
}

func TestDialUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, keyPEM := newSigner(t)
	_, err = dialer().Dial(context.Background(), Target{Address: addr.IP.String(), Port: addr.Port, Username: testUser, PrivateKey: keyPEM})
	wantKind(t, err, KindUnreachable)
}

func TestBadPrivateKey(t *testing.T) {
	if _, err := ParsePrivateKey([]byte("not a key"), nil); AsError(err) == nil {
		t.Fatalf("err = %v, want remote error", err)
	}
	_, keyPEM := newSigner(t)
	fp, err := ParsePrivateKey(keyPEM, nil)
	if err != nil || !strings.HasPrefix(fp, "SHA256:") {
		t.Fatalf("fingerprint = %q, err = %v", fp, err)
	}
}

func TestSudoWhenNotRoot(t *testing.T) {
	signer, keyPEM := newSigner(t)
	tests := []struct {
		name           string
		sudoExit       uint32
		wantPrivileged bool
		wantPrefix     string
	}{
		{name: "passwordless sudo", sudoExit: 0, wantPrivileged: true, wantPrefix: sudoPrefix},
		{name: "no sudo", sudoExit: 1, wantPrivileged: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := startServer(t, signer.PublicKey(), func(cmd string, _ []byte) (string, uint32) {
				switch cmd {
				case cmdWhoami:
					return "1000\n", 0
				case cmdSudoCheck:
					return "", tt.sudoExit
				default:
					return "", 0
				}
			})
			r := mustDial(t, target(srv, keyPEM))
			if r.IsPrivileged() != tt.wantPrivileged {
				t.Fatalf("privileged = %v, want %v", r.IsPrivileged(), tt.wantPrivileged)
			}
			if _, err := r.Run(context.Background(), "true"); err != nil {
				t.Fatalf("run: %v", err)
			}
			cmds := srv.recorded()
			if last := cmds[len(cmds)-1]; last != tt.wantPrefix+"sh -c 'true'" {
				t.Fatalf("command = %q", last)
			}
		})
	}
}

func TestRunReportsExitCodeAndCapsOutput(t *testing.T) {
	signer, keyPEM := newSigner(t)
	big := strings.Repeat("x", maxOutputBytes+1024)
	srv := startServer(t, signer.PublicKey(), func(cmd string, _ []byte) (string, uint32) {
		if cmd == cmdWhoami {
			return "0", 0
		}
		return big, 7
	})
	r := mustDial(t, target(srv, keyPEM))
	res, err := r.Run(context.Background(), "false")
	if err != nil {
		t.Fatalf("non-zero exit must not be an error: %v", err)
	}
	if res.ExitCode != 7 || len(res.Output) != maxOutputBytes {
		t.Fatalf("exit = %d, output len = %d", res.ExitCode, len(res.Output))
	}
}

func TestUpload(t *testing.T) {
	signer, keyPEM := newSigner(t)
	var got []byte
	startSrv := startServer(t, signer.PublicKey(), func(cmd string, stdin []byte) (string, uint32) {
		if cmd == cmdWhoami {
			return "0", 0
		}
		got = stdin
		return "/tmp/hub.abc", 0
	})
	r := mustDial(t, target(startSrv, keyPEM))
	path, err := r.Upload(context.Background(), []byte("#!/bin/sh\necho hi\n"))
	if err != nil || path != "/tmp/hub.abc" {
		t.Fatalf("path = %q, err = %v", path, err)
	}
	if string(got) != "#!/bin/sh\necho hi\n" {
		t.Fatalf("uploaded = %q", got)
	}
	if _, err := r.Upload(context.Background(), make([]byte, maxUploadBytes+1)); AsError(err) == nil {
		t.Fatalf("oversized upload err = %v", err)
	}
}

func TestQuote(t *testing.T) {
	tests := map[string]string{
		"plain":      "'plain'",
		"a b":        "'a b'",
		"it's":       `'it'\''s'`,
		"$(rm -rf)":  "'$(rm -rf)'",
		"":           "''",
		"x'; echo y": `'x'\''; echo y'`,
	}
	for in, want := range tests {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}
