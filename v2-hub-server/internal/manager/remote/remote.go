// Package remote 经 SSH 在托管主机上执行脚本、上传文件（docs/03 §5.22，Ruling-68）。
// 这一层不认识面板：跑什么脚本由 manager/deploy（部署与运维）与 manager/ipprobe（IP 质量检测的服务器侧，Ruling-81）里的
// 固定模板决定，本包只负责连接、主机密钥固定、提权与输出限长。
package remote

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrorKind 是 SSH 失败的大类，取值与 docs/03 §5.22 “不能部署的原因”里的同名项一致。
type ErrorKind string

const (
	// KindUnreachable 表示 TCP 连不上或握手没完成。
	KindUnreachable ErrorKind = "unreachable"
	// KindAuth 表示主机密钥已通过、但登录被拒。
	KindAuth ErrorKind = "auth_failed"
	// KindHostKey 表示主机密钥与已固定的指纹不一致。
	KindHostKey ErrorKind = "host_key_changed"
	// KindCommand 表示会话已建立，但命令没法执行或中途断开。
	KindCommand ErrorKind = "command_failed"
)

// Error 是可展示的 SSH 错误；Message 不含凭据。
type Error struct {
	Kind    ErrorKind
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Kind, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// AsError 从错误链里取出 *Error；不是本包的错误返回 nil。
func AsError(err error) *Error {
	var re *Error
	if errors.As(err, &re) {
		return re
	}
	return nil
}

// Target 是一次连接的全部信息。PrivateKey 与 Password 二选一。
type Target struct {
	Address  string
	Port     int
	Username string
	// PrivateKey 是 OpenSSH / PEM 私钥；Passphrase 是它的口令，没有为空。
	PrivateKey []byte
	Passphrase []byte
	Password   string
	// HostKeySHA256 是已固定的主机公钥指纹（SHA256:…）；空串表示首次连接，接受并由调用方记录。
	HostKeySHA256 string
}

// Result 是一条命令的结果。命令以非 0 退出不算错误，由调用方按 ExitCode 判断。
type Result struct {
	// Output 是标准输出与标准错误合并后的内容，超过上限的部分被丢弃。
	Output   string
	ExitCode int
}

// Runner 是一条已建立的 SSH 连接。
type Runner interface {
	// Run 用 sh 执行一段脚本；登录用户不是 root 时经 sudo -n 提权。
	Run(ctx context.Context, script string) (Result, error)
	// Upload 把内容写进远端的一个临时文件（权限 600），返回路径；用完由调用方删除。
	Upload(ctx context.Context, content []byte) (string, error)
	// HostKeySHA256 是本次连接看到的主机公钥指纹。
	HostKeySHA256() string
	// IsPrivileged 报告登录用户是 root 或能免密 sudo。
	IsPrivileged() bool
	Close() error
}

// Dialer 建立到主机的连接。
type Dialer interface {
	Dial(ctx context.Context, t Target) (Runner, error)
}

// Config 是 SSH 连接的参数。
type Config struct {
	// ConnectTimeout 是 TCP 连接加握手的总时限。
	ConnectTimeout time.Duration
	// AllowLoopback 为 false 时拒绝连到回环地址（含域名解析到回环的情况）；只给联调打开。
	AllowLoopback bool
}

const shellQuote = `'`

// Quote 把任意字符串变成 sh 里的一个单引号字面量，供拼进脚本模板。
func Quote(s string) string {
	return shellQuote + strings.ReplaceAll(s, shellQuote, `'\''`) + shellQuote
}
