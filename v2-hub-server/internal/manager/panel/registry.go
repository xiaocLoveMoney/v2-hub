package panel

import (
	"fmt"
	"sort"
)

// Factory 按连接信息创建一个 Provider。
type Factory func(cfg ConnConfig) (Provider, error)

// Registry 按 provider_type 选取面板实现。创建后只读，并发安全。
type Registry struct {
	factories map[string]Factory
}

// NewRegistry 由 provider_type → Factory 的映射创建注册表。
func NewRegistry(factories map[string]Factory) *Registry {
	copied := make(map[string]Factory, len(factories))
	for k, v := range factories {
		copied[k] = v
	}
	return &Registry{factories: copied}
}

// Open 创建指定类型的 Provider；类型未注册时返回错误。
func (r *Registry) Open(providerType string, cfg ConnConfig) (Provider, error) {
	f, ok := r.factories[providerType]
	if !ok {
		return nil, fmt.Errorf("unknown panel provider type %q", providerType)
	}
	return f(cfg)
}

// Has 报告类型是否已注册。
func (r *Registry) Has(providerType string) bool {
	_, ok := r.factories[providerType]
	return ok
}

// Types 返回已注册的类型（排序后）。
func (r *Registry) Types() []string {
	out := make([]string, 0, len(r.factories))
	for k := range r.factories {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
