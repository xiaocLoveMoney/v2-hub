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
	// OnlineClients 表示能列出当前在线的 client（docs/03 §5.5）。
	OnlineClients bool
	// CoreConfig 表示实现了 CoreConfigurator，可读写内核配置模板（服务端链式，docs/03 §5.19）。
	CoreConfig bool
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

// InboundConfig 是面板上入站的完整配置，含私钥与口令、settings 不含 clients，只供编辑表单回显（Ruling-16）。
type InboundConfig struct {
	Protocol string
	Port     int
	Listen   string
	Remark   string
	Enabled  bool
	Config   json.RawMessage
}

// KeyKind 是可请求面板生成的密钥种类（Ruling-16）。
type KeyKind string

const (
	// KeyX25519 是 Reality 的 x25519 密钥对。
	KeyX25519 KeyKind = "x25519"
	// KeyMLDSA65 是 Reality 的 ML-DSA-65 种子与校验值。
	KeyMLDSA65 KeyKind = "mldsa65"
	// KeyVLESSEnc 是 VLESS encryption 的服务端 decryption 与客户端 encryption 串。
	KeyVLESSEnc KeyKind = "vlessenc"
)

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

// Endpoint 是一个对外地址（3x-ui 的一行 Host）。同一 GroupRef 的地址共享端口以外的设置。
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

// EndpointKey 标识一个对外地址：某个组里、挂在某个入站上的某个地址。
type EndpointKey struct {
	GroupRef   string
	InboundRef string
	Address    string
}

// EndpointMove 是删除对外地址时因原组拆分而换到新组的地址；地址本身不变，平台据此改写组标识以保留记录与节点。
type EndpointMove struct {
	InboundRef  string
	Address     string
	NewGroupRef string
}

// ClientSpec 是平台期望的一个面板 client（docs/03 §5.2 下发）。凭据各协议共用：UUID 给 vless / vmess，
// Auth 给 trojan、hysteria 与 shadowsocks（SS2022 由面板派生合法密钥）。面板上不设额度与到期。
type ClientSpec struct {
	Email       string
	UUID        string
	Auth        string
	LimitIP     int
	Enabled     bool
	InboundRefs []string
}

// Client 是面板上一个 client 的现状；Up / Down 是面板累计的流量计数（字节）。
type Client struct {
	Email       string
	Enabled     bool
	LimitIP     int
	InboundRefs []string
	Up          int64
	Down        int64
}

// Provider 是一台面板的操作集合。实现必须并发安全。
// M1 覆盖探测、入站与对外地址；M2 加入 client 下发与流量；M3 加入分享链接、凭据轮换与在线状态。
type Provider interface {
	Capabilities() Capabilities
	Probe(ctx context.Context) (PanelInfo, error)

	ListInbounds(ctx context.Context) ([]Inbound, error)
	GetInbound(ctx context.Context, ref string) (Inbound, error)
	CreateInbound(ctx context.Context, spec InboundSpec) (Inbound, error)
	UpdateInbound(ctx context.Context, ref string, spec InboundSpec) (Inbound, error)
	SetInboundEnabled(ctx context.Context, ref string, enabled bool) error
	// GetInboundConfig 读取面板上入站的完整配置（含密钥，不含 clients）。
	GetInboundConfig(ctx context.Context, ref string) (InboundConfig, error)
	// GenerateKeys 让面板生成一组密钥，返回面板给出的 JSON 对象。
	GenerateKeys(ctx context.Context, kind KeyKind) (json.RawMessage, error)
	DeleteInbound(ctx context.Context, ref string) error

	ListEndpoints(ctx context.Context) ([]Endpoint, error)
	CreateEndpoints(ctx context.Context, spec EndpointSpec) error
	// RemoveEndpoint 只删除 key 指定的一个地址，同组其它地址保留（docs/03 §5.1 删除对外地址）。
	// 组或地址已不存在时返回 ErrNotFound；组被拆分时返回换到新组的地址。
	RemoveEndpoint(ctx context.Context, key EndpointKey) ([]EndpointMove, error)

	// ListClients 返回面板上全部 client 及其累计流量。
	ListClients(ctx context.Context) ([]Client, error)
	// AddClient 新建 client 并挂到 spec.InboundRefs。
	AddClient(ctx context.Context, spec ClientSpec) error
	// UpdateClient 修改 client 的启停与 IP 限制，凭据与挂载不变。
	UpdateClient(ctx context.Context, spec ClientSpec) error
	AttachClient(ctx context.Context, email string, inboundRefs []string) error
	DetachClient(ctx context.Context, email string, inboundRefs []string) error
	// SetClientsEnabled 批量启停（3x-ui 禁用 client 会重启 Xray，调用方应合并成一批）。
	SetClientsEnabled(ctx context.Context, emails []string, enabled bool) error
	DeleteClient(ctx context.Context, email string) error
	// SetClientCredential 把 client 的连接凭据换成 spec 里的 UUID 与 Auth（重置订阅，docs/03 §5.4）；
	// 启停与 IP 限制按 spec 一并写入，挂载不变。
	SetClientCredential(ctx context.Context, spec ClientSpec) error
	// ClientLinks 返回面板为该 client 生成的分享链接，每个对外地址一条；链接含凭据，不得写日志。
	ClientLinks(ctx context.Context, email string) ([]string, error)
	// OnlineClients 返回当前在线的 client email；Capabilities.OnlineClients 为 false 时返回空。
	OnlineClients(ctx context.Context) ([]string, error)
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

// CoreTemplate 是面板保存的内核配置模板。Config 是完整 JSON（含面板自己的 api 入站、路由等），
// Extra 是与模板一起保存的面板设置，读出后原样回写，避免被重置。模板可能含凭据，不得写日志。
type CoreTemplate struct {
	Config json.RawMessage
	Extra  map[string]string
}

// CoreState 是内核进程的运行状态；Message 是面板给出的错误摘要（可能为空）。
type CoreState struct {
	Running bool
	State   string
	Message string
}

// CoreConfigurator 是可选能力（Capabilities.CoreConfig 为真时由 Provider 实现）：读写内核配置模板、重启内核、
// 查内核运行状态。服务端链式据此在模板里增删平台条目并在失败时回滚（docs/03 §5.19，Ruling-40）。
type CoreConfigurator interface {
	GetCoreTemplate(ctx context.Context) (CoreTemplate, error)
	// SetCoreTemplate 整份写回模板；写入不等于生效，需再 RestartCore。
	SetCoreTemplate(ctx context.Context, tpl CoreTemplate) error
	RestartCore(ctx context.Context) error
	CoreState(ctx context.Context) (CoreState, error)
}
