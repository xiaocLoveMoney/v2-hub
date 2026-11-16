// Package remotetest 提供脚本化的假 SSH 执行器，供 deploy 与 service 的单测使用，不发真实连接。
package remotetest

import (
	"context"
	"sync"

	"v2-hub-server/internal/manager/remote"
)

// UploadPath 是假执行器给每次上传返回的路径。
const UploadPath = "/tmp/hub.fake"

// Handler 决定一段脚本的结果。
type Handler func(script string) (remote.Result, error)

// Runner 是假的 remote.Runner：记录跑过的脚本与上传的内容，结果由 Handle 给出（为 nil 时一律成功、无输出）。
type Runner struct {
	Privileged bool
	HostKey    string
	Handle     Handler

	mu      sync.Mutex
	scripts []string
	uploads [][]byte
	closed  bool
}

var _ remote.Runner = (*Runner)(nil)

// Run 见 remote.Runner。
func (r *Runner) Run(_ context.Context, script string) (remote.Result, error) {
	r.mu.Lock()
	r.scripts = append(r.scripts, script)
	r.mu.Unlock()
	if r.Handle == nil {
		return remote.Result{}, nil
	}
	return r.Handle(script)
}

// Upload 见 remote.Runner。
func (r *Runner) Upload(_ context.Context, content []byte) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.uploads = append(r.uploads, append([]byte(nil), content...))
	return UploadPath, nil
}

// HostKeySHA256 见 remote.Runner。
func (r *Runner) HostKeySHA256() string { return r.HostKey }

// IsPrivileged 见 remote.Runner。
func (r *Runner) IsPrivileged() bool { return r.Privileged }

// Close 见 remote.Runner。
func (r *Runner) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

// Scripts 返回按顺序跑过的脚本。
func (r *Runner) Scripts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.scripts...)
}

// Uploads 返回按顺序上传过的内容。
func (r *Runner) Uploads() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.uploads...)
}

// Closed 报告连接是否已关闭。
func (r *Runner) Closed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// Dialer 是假的 remote.Dialer：Err 非空时拨号失败，否则返回 Runner；记录每次拨号的目标。
type Dialer struct {
	Runner *Runner
	Err    error

	mu      sync.Mutex
	targets []remote.Target
}

var _ remote.Dialer = (*Dialer)(nil)

// Dial 见 remote.Dialer。已固定的主机密钥与 Runner 的不一致时，像真实拨号器一样拒绝。
func (d *Dialer) Dial(_ context.Context, t remote.Target) (remote.Runner, error) {
	d.mu.Lock()
	d.targets = append(d.targets, t)
	d.mu.Unlock()
	if d.Err != nil {
		return nil, d.Err
	}
	if t.HostKeySHA256 != "" && t.HostKeySHA256 != d.Runner.HostKey {
		return nil, &remote.Error{Kind: remote.KindHostKey, Message: "主机密钥与上次记录的不一致"}
	}
	return d.Runner, nil
}

// Targets 返回按顺序拨过的目标。
func (d *Dialer) Targets() []remote.Target {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]remote.Target(nil), d.targets...)
}
