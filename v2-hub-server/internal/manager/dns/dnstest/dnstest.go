// Package dnstest 提供测试用的假 dns.Provider。
package dnstest

import (
	"context"
	"sync"

	"v2-hub-server/internal/manager/dns"
)

// Fake 是内存里的 dns.Provider：记下每次调用，可按方法名注入错误。并发安全。
type Fake struct {
	mu sync.Mutex
	// Errors 按方法名（CheckZone / PointTo / SetTXT）注入错误。
	Errors map[string]error
	// Points 是 PointTo 写下的 name → target。
	Points map[string]string
	// TXT 是当前还在的 TXT 记录 name → value；Removed 是被删掉的条数。
	TXT     map[string]string
	Removed int
	// Accounts 是每次 Open 收到的账号。
	Accounts []dns.Account
}

// New 创建一个空的假 Provider。
func New() *Fake {
	return &Fake{Errors: map[string]error{}, Points: map[string]string{}, TXT: map[string]string{}}
}

// Factory 返回总是给出这个 Fake 的工厂，并记下收到的账号。
func (f *Fake) Factory() dns.Factory {
	return func(acc dns.Account) (dns.Provider, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.Accounts = append(f.Accounts, acc)
		return f, nil
	}
}

// SetError 给某个方法注入错误；nil 表示恢复正常。
func (f *Fake) SetError(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Errors[method] = err
}

// CheckZone 见 dns.Provider。
func (f *Fake) CheckZone(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Errors["CheckZone"]
}

// PointTo 见 dns.Provider。
func (f *Fake) PointTo(_ context.Context, name, target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.Errors["PointTo"]; err != nil {
		return err
	}
	f.Points[name] = target
	return nil
}

// SetTXT 见 dns.Provider。
func (f *Fake) SetTXT(_ context.Context, name, value string) (func(context.Context) error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.Errors["SetTXT"]; err != nil {
		return nil, err
	}
	f.TXT[name] = value
	return func(context.Context) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.TXT, name)
		f.Removed++
		return nil
	}, nil
}

// Snapshot 返回当前的解析与 TXT 记录的副本。
func (f *Fake) Snapshot() (points, txt map[string]string, removed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	points, txt = make(map[string]string, len(f.Points)), make(map[string]string, len(f.TXT))
	for k, v := range f.Points {
		points[k] = v
	}
	for k, v := range f.TXT {
		txt[k] = v
	}
	return points, txt, f.Removed
}
