package remote

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	// maxOutputBytes 是一条命令保留的输出上限；固定脚本的正常输出远小于它。
	maxOutputBytes = 256 << 10
	// maxUploadBytes 是一次上传的上限（上游安装脚本约 80 KB）。
	maxUploadBytes = 1 << 20
	rootUID        = "0"
	sudoPrefix     = "sudo -n "
	cmdWhoami      = "id -u"
	cmdSudoCheck   = "sudo -n true"
	// cmdUpload 把标准输入写进新建的临时文件并打印路径。
	cmdUpload = `sh -c 'umask 077; f=$(mktemp /tmp/hub.XXXXXXXX) && cat > "$f" && printf %s "$f"'`

	msgDial         = "连不上主机的 SSH 端口"
	msgHandshake    = "SSH 握手失败"
	msgAuth         = "SSH 登录被拒，请检查用户名与密钥或密码"
	msgHostKey      = "主机密钥与上次记录的不一致"
	msgLoopback     = "不允许连接回环地址"
	msgBadKey       = "私钥无法解析，请检查格式与口令"
	msgSession      = "SSH 会话无法建立"
	msgUploadTooBig = "上传内容过大"
)

// errHostKeyMismatch 由主机密钥回调返回，用来在握手失败后认出原因。
var errHostKeyMismatch = errors.New("host key mismatch")

// SSHDialer 是 Dialer 的真实实现。
type SSHDialer struct {
	cfg Config
}

// NewSSHDialer 创建 SSH 拨号器。
func NewSSHDialer(cfg Config) *SSHDialer {
	return &SSHDialer{cfg: cfg}
}

// ParsePrivateKey 解析私钥并返回公钥指纹（SHA256:…）；格式或口令不对返回错误。
func ParsePrivateKey(key, passphrase []byte) (string, error) {
	signer, err := parseSigner(key, passphrase)
	if err != nil {
		return "", err
	}
	return ssh.FingerprintSHA256(signer.PublicKey()), nil
}

func parseSigner(key, passphrase []byte) (ssh.Signer, error) {
	var (
		signer ssh.Signer
		err    error
	)
	if len(passphrase) > 0 {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(key, passphrase)
	} else {
		signer, err = ssh.ParsePrivateKey(key)
	}
	if err != nil {
		// 解析错误里可能带私钥片段的位置信息，不向上透出原文
		return nil, &Error{Kind: KindAuth, Message: msgBadKey}
	}
	return signer, nil
}

func authMethods(t Target) ([]ssh.AuthMethod, error) {
	if len(t.PrivateKey) > 0 {
		signer, err := parseSigner(t.PrivateKey, t.Passphrase)
		if err != nil {
			return nil, err
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}
	password := t.Password
	// 不少发行版把密码登录配成 keyboard-interactive，两种都给
	interactive := ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
		answers := make([]string, len(questions))
		for i := range questions {
			answers[i] = password
		}
		return answers, nil
	})
	return []ssh.AuthMethod{ssh.Password(password), interactive}, nil
}

// Dial 连接主机、校验主机密钥并探明能否提权。调用方用完须 Close。
func (d *SSHDialer) Dial(ctx context.Context, t Target) (Runner, error) {
	auth, err := authMethods(t)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, d.cfg.ConnectTimeout)
	defer cancel()
	addr := net.JoinHostPort(t.Address, strconv.Itoa(t.Port))
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, &Error{Kind: KindUnreachable, Message: msgDial, Err: err}
	}
	if tcp, ok := conn.RemoteAddr().(*net.TCPAddr); ok && tcp.IP.IsLoopback() && !d.cfg.AllowLoopback {
		closeQuietly(conn)
		return nil, &Error{Kind: KindUnreachable, Message: msgLoopback}
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			closeQuietly(conn)
			return nil, &Error{Kind: KindUnreachable, Message: msgHandshake, Err: err}
		}
	}
	var seen string
	clientCfg := &ssh.ClientConfig{
		User: t.Username,
		Auth: auth,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			seen = ssh.FingerprintSHA256(key)
			if t.HostKeySHA256 != "" && seen != t.HostKeySHA256 {
				return errHostKeyMismatch
			}
			return nil
		},
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	if err != nil {
		closeQuietly(conn)
		return nil, handshakeError(err, seen)
	}
	// 握手完成后取消截止时间，后续每条命令各自受 ctx 控制
	if err := conn.SetDeadline(time.Time{}); err != nil {
		closeQuietly(sshConn)
		return nil, &Error{Kind: KindUnreachable, Message: msgHandshake, Err: err}
	}
	r := &sshRunner{client: ssh.NewClient(sshConn, chans, reqs), hostKey: seen}
	if err := r.detectPrivilege(ctx); err != nil {
		closeQuietly(r.client)
		return nil, err
	}
	return r, nil
}

// handshakeError 给握手失败归类。ssh 包不导出认证失败的错误类型，
// 所以按阶段判断：主机密钥回调已放行之后还失败的，就是登录被拒。
func handshakeError(err error, seenHostKey string) error {
	if errors.Is(err, errHostKeyMismatch) {
		return &Error{Kind: KindHostKey, Message: msgHostKey}
	}
	if seenHostKey != "" {
		return &Error{Kind: KindAuth, Message: msgAuth}
	}
	return &Error{Kind: KindUnreachable, Message: msgHandshake, Err: err}
}

type sshRunner struct {
	client     *ssh.Client
	hostKey    string
	privileged bool
	prefix     string
}

func (r *sshRunner) HostKeySHA256() string { return r.hostKey }

func (r *sshRunner) IsPrivileged() bool { return r.privileged }

func (r *sshRunner) Close() error { return r.client.Close() }

func (r *sshRunner) detectPrivilege(ctx context.Context) error {
	res, err := r.exec(ctx, cmdWhoami, nil)
	if err != nil {
		return err
	}
	if strings.TrimSpace(res.Output) == rootUID {
		r.privileged = true
		return nil
	}
	res, err = r.exec(ctx, cmdSudoCheck, nil)
	if err != nil {
		return err
	}
	if res.ExitCode == 0 {
		r.privileged = true
		r.prefix = sudoPrefix
	}
	return nil
}

func (r *sshRunner) Run(ctx context.Context, script string) (Result, error) {
	return r.exec(ctx, r.prefix+"sh -c "+Quote(script), nil)
}

func (r *sshRunner) Upload(ctx context.Context, content []byte) (string, error) {
	if len(content) > maxUploadBytes {
		return "", &Error{Kind: KindCommand, Message: msgUploadTooBig}
	}
	res, err := r.exec(ctx, r.prefix+cmdUpload, bytes.NewReader(content))
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(res.Output)
	if res.ExitCode != 0 || path == "" {
		return "", &Error{Kind: KindCommand, Message: "临时文件创建失败"}
	}
	return path, nil
}

// exec 跑一条命令并等它结束；ctx 取消时关掉会话，让远端收到断开。
func (r *sshRunner) exec(ctx context.Context, cmd string, stdin io.Reader) (Result, error) {
	session, err := r.client.NewSession()
	if err != nil {
		return Result{}, &Error{Kind: KindCommand, Message: msgSession, Err: err}
	}
	defer closeQuietly(session)
	out := &cappedBuffer{limit: maxOutputBytes}
	session.Stdout = out
	session.Stderr = out
	session.Stdin = stdin
	done := make(chan error, 1)
	go func() {
		// Run 在会话被关闭时必然返回，这个 goroutine 不会泄漏
		done <- session.Run(cmd)
	}()
	select {
	case <-ctx.Done():
		closeQuietly(session)
		<-done
		return Result{Output: out.String()}, &Error{Kind: KindCommand, Message: "命令超时或被取消", Err: ctx.Err()}
	case err := <-done:
		return runResult(out.String(), err)
	}
}

func runResult(output string, err error) (Result, error) {
	if err == nil {
		return Result{Output: output}, nil
	}
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		return Result{Output: output, ExitCode: exitErr.ExitStatus()}, nil
	}
	return Result{Output: output}, &Error{Kind: KindCommand, Message: "命令执行中断", Err: err}
}

// cappedBuffer 只保留前 limit 字节，其余丢弃；会话的两路输出并发写入，所以带锁。
type cappedBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.limit - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// closeQuietly 关闭已不再需要的连接或会话；此时主流程的结果或错误已定，关闭失败（多为对端先断开）不改变结论。
func closeQuietly(c io.Closer) {
	if err := c.Close(); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		slog.Debug("close ssh resource", "err", err)
	}
}
