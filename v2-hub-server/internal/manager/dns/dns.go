// Package dns 定义“经 DNS 服务商的接口管理一个根域名下的记录”的抽象（docs/03 §5.22，Ruling-72）。
// 每家服务商一个 Provider 实现，按 provider_type 注册；业务层只依赖本包的接口，不按服务商写分支。
package dns

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrorKind 是 DNS 服务商接口失败的大类。
type ErrorKind string

const (
	// KindAuth 表示令牌被拒或权限不够。
	KindAuth ErrorKind = "auth"
	// KindZone 表示令牌看不到这个根域名。
	KindZone ErrorKind = "zone"
	// KindUnreachable 表示连不上服务商或响应异常。
	KindUnreachable ErrorKind = "unreachable"
	// KindRejected 表示服务商收到请求但拒绝执行。
	KindRejected ErrorKind = "rejected"
)

// Error 是带大类的 DNS 错误；Message 可直接展示，不含令牌。
type Error struct {
	Kind    ErrorKind
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("dns %s: %s: %v", e.Kind, e.Message, e.Err)
	}
	return fmt.Sprintf("dns %s: %s", e.Kind, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// AsError 从错误链里取出 *Error；不是 DNS 错误返回 nil。
func AsError(err error) *Error {
	var de *Error
	if errors.As(err, &de) {
		return de
	}
	return nil
}

// Account 是打开一个 Provider 需要的信息。
type Account struct {
	// Zone 是根域名，小写、不带结尾的点。
	Zone string
	// Token 是服务商的 API 令牌明文，只在内存里。
	Token string
}

// Provider 管理一个根域名下的记录。实现须并发安全；所有 name 都是完整域名（小写、不带结尾的点）。
type Provider interface {
	// CheckZone 确认令牌能看到并管理这个根域名；不能时返回 *Error。
	CheckZone(ctx context.Context) error
	// PointTo 把 name 解析到 target：target 是 IPv4 建 A、IPv6 建 AAAA、是域名建 CNAME，仅解析、不开代理。
	// name 下已有的 A / AAAA / CNAME 记录会被换成这一条；已经是这一条时什么都不做。
	PointTo(ctx context.Context, name, target string) error
	// SetTXT 在 name 下加一条 TXT 记录，返回删掉这条记录的函数；调用方用完必须调用。
	SetTXT(ctx context.Context, name, value string) (remove func(ctx context.Context) error, err error)
}

// Factory 按账号创建一个 Provider；创建时不得发起网络请求。
type Factory func(acc Account) (Provider, error)

// Registry 按服务商类型选取实现。创建后只读，并发安全。
type Registry struct {
	factories map[string]Factory
}

// NewRegistry 由 provider_type → Factory 的映射创建注册表；新增服务商只在组装处加一项。
func NewRegistry(factories map[string]Factory) *Registry {
	copied := make(map[string]Factory, len(factories))
	for k, v := range factories {
		copied[k] = v
	}
	return &Registry{factories: copied}
}

// Has 报告类型是否已注册。
func (r *Registry) Has(providerType string) bool {
	_, ok := r.factories[providerType]
	return ok
}

// Open 创建指定类型的 Provider；类型未注册时返回错误。
func (r *Registry) Open(providerType string, acc Account) (Provider, error) {
	f, ok := r.factories[providerType]
	if !ok {
		return nil, fmt.Errorf("unknown dns provider type %q", providerType)
	}
	return f(acc)
}

// InZone 报告 name 是不是 zone 本身或它的子域名；两者都应是小写、不带结尾的点。
func InZone(name, zone string) bool {
	return zone != "" && (name == zone || strings.HasSuffix(name, "."+zone))
}
