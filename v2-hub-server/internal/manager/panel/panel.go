// Package panel 定义面板（3x-ui、日后的 S-UI 等）的统一抽象（Ruling-8）。
//
// service 层只依赖本包的 Provider 接口与中立模型，不接触任何面板专有路径与字段；
// 各面板的实现放在子包（如 panel/xui），经 Registry 按 provider_type 选取。
// 面板之间的差异用 Capabilities 表达，上层按能力开关功能，不按面板类型写分支。
package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// TLS 校验模式，语义见 docs/03 §5.1。
const (
	TLSVerify = "verify"
	TLSPin    = "pin"
	TLSSkip   = "skip"
)

// CoreXray 是 Xray 内核类型标记；入站高级配置的 JSON 形状由内核决定。
const CoreXray = "xray"

// ConnConfig 是连接一台面板所需的信息。Token 是明文凭据，只在内存中使用。
type ConnConfig struct {
	BaseURL       string
	Token         string
	TLSMode       string
	CertPinSHA256 string
	Timeout       time.Duration
}

// PanelInfo 是探测结果。
type PanelInfo struct {
	Version string
	GUID    string
	// Status 是资源快照（CPU、内存、在线时长等），形状随面板而定，原样透传给前端展示。
	Status json.RawMessage
}

// Capabilities 描述面板能力差异。
type Capabilities struct {
	// CoreType 是内核类型（如 xray），决定入站配置的 JSON 形状。
	CoreType string
	// Endpoints 表示支持对外地址覆盖（3x-ui 的 Host）。
	Endpoints bool
	// MultiInboundClients 表示一个 client 可挂到多个入站（M2 使用）。
	MultiInboundClients bool
}

// Inbound 是面板上的一个入站（中立模型）。Config 已删除敏感字段与 clients（Ruling-14）。
type Inbound struct {
	Ref         string
	Protocol    string
	Port        int
	Listen      string
	Tag         string
	Remark      string
	Enabled     bool
	Network     string
	Security    string
	CoreType    string
	Config      json.RawMessage
	ConfigHash  string // 覆盖完整配置（含敏感字段）但不含 clients 与流量计数，用于漂移判定
	ClientCount int
}

// InboundSpec 是新建或修改入站的期望状态。Config 是内核相关的 JSON 对象，
// 对 Xray 为 {settings, streamSettings, sniffing}；修改时省略的敏感字段由实现从面板原值补回。
type InboundSpec struct {
	Protocol string
	Port     int
	Listen   string
	Remark   string
	Enabled  bool
	Config   json.RawMessage
}

// Endpoint 是一个对外地址（3x-ui 的一行 Host）。同一 GroupRef 的地址在面板上同生同灭。
type Endpoint struct {
	GroupRef   string
	InboundRef string
	Address    string
	Port       int
	Remark     string
	Disabled   bool
}

// EndpointSpec 是新建一组对外地址的请求。
type EndpointSpec struct {
	InboundRefs []string
	Addresses   []string
	Port        int
	Remark      string
}

// Provider 是一台面板的操作集合。实现必须并发安全。
// M1 覆盖探测、入站与对外地址；client 相关方法在 M2 加入。
type Provider interface {
	Capabilities() Capabilities
	Probe(ctx context.Context) (PanelInfo, error)

	ListInbounds(ctx context.Context) ([]Inbound, error)
	GetInbound(ctx context.Context, ref string) (Inbound, error)
	CreateInbound(ctx context.Context, spec InboundSpec) (Inbound, error)
	UpdateInbound(ctx context.Context, ref string, spec InboundSpec) (Inbound, error)
	SetInboundEnabled(ctx context.Context, ref string, enabled bool) error
	DeleteInbound(ctx context.Context, ref string) error

	ListEndpoints(ctx context.Context) ([]Endpoint, error)
	CreateEndpoints(ctx context.Context, spec EndpointSpec) error
	DeleteEndpointGroup(ctx context.Context, groupRef string) error
}

// ErrorKind 是面板错误的分类，service 层据此决定状态转移（docs/03 T2、T3）与对外错误码。
type ErrorKind string

const (
	// KindUnreachable：网络不通、超时、TLS 失败、5xx、响应无法解析。
	KindUnreachable ErrorKind = "unreachable"
	// KindAuth：面板拒绝凭据（401/403）。
	KindAuth ErrorKind = "auth"
	// KindNotFound：资源不存在，或地址 / base path 不对。
	KindNotFound ErrorKind = "not_found"
	// KindRejected：面板收到请求但拒绝执行（如端口冲突、配置非法）。
	KindRejected ErrorKind = "rejected"
)

// Error 是面板调用错误。Message 已脱敏（不含 token、完整 URL 查询串），可直接展示给管理员。
type Error struct {
	Kind    ErrorKind
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("panel %s: %s: %v", e.Kind, e.Message, e.Err)
	}
	return fmt.Sprintf("panel %s: %s", e.Kind, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// Is 让 errors.Is(err, panel.ErrAuth) 只按 Kind 匹配。
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Kind == e.Kind && t.Message == "" && t.Err == nil
}

// 供 errors.Is 判定的哨兵值。
var (
	ErrUnreachable = &Error{Kind: KindUnreachable}
	ErrAuth        = &Error{Kind: KindAuth}
	ErrNotFound    = &Error{Kind: KindNotFound}
	ErrRejected    = &Error{Kind: KindRejected}
)

// AsError 取出 err 链上的 *Error；不是面板错误时返回 nil。
func AsError(err error) *Error {
	var pe *Error
	if errors.As(err, &pe) {
		return pe
	}
	return nil
}
