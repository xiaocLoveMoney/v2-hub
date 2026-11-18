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
	// Errors 按方法名（CheckZone / PointTo）注入错误。
	Errors map[string]error
	// Points 是 PointTo 写下的 name → target。
	Points map[string]string
	// Hook 是 ACMEHook 的返回值。
	Hook dns.ACMEHook
	// Accounts 是每次 Open 收到的账号。
	Accounts []dns.Account
}

// New 创建一个空的假 Provider。
func New() *Fake {
	return &Fake{Errors: map[string]error{}, Points: map[string]string{}}
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

// ACMEHook 见 dns.Provider。
func (f *Fake) ACMEHook() dns.ACMEHook {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Hook
}

// Snapshot 返回当前解析的副本。
func (f *Fake) Snapshot() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	points := make(map[string]string, len(f.Points))
	for k, v := range f.Points {
		points[k] = v
	}
	return points
}
