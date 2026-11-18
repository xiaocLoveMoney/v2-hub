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
	// PublicIP 是面板自报的服务器公网 IP（优先 IPv4）；面板没报为空。只用于扫描位置（Ruling-65），不落库。
	PublicIP string
	// CoreState 是内核（如 Xray）的运行状态：CoreRunning 为在运行，其它值为没在运行，空为面板没报；CoreError 是面板给的错误说明。
	CoreState string
	CoreError string
}

// CoreRunning 是 PanelInfo.CoreState 表示内核在运行的取值。
const CoreRunning = "running"

// Capabilities 描述面板能力差异。
type Capabilities struct {
	// CoreType 是内核类型（如 xray），决定入站配置的 JSON 形状。
	CoreType string
	// Endpoints 表示支持对外地址覆盖（3x-ui 的 Host）。
	Endpoints bool
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

// InboundConfig 是面板上入站的完整配置，含私钥与密码、settings 不含 clients，只供编辑表单回显（Ruling-16）。
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
// Auth 给 hysteria，Password 给 trojan、shadowsocks、tuic 等用 password 的协议；挂了 SS2022 入站时业务层把它设成合法密钥
// （标准 Base64 的 16 或 32 字节，Ruling-60），面板原样使用。面板上不设额度与到期。
type ClientSpec struct {
	Email string
	UUID  string
	Auth  string
	// Password 是用户自设的连接密码（Ruling-58）；空 = 沿用 Auth。用 PasswordOrAuth 取实际值。
	Password string
	// Flow 是 VLESS 流控，由服务器的开关决定（Ruling-77）；空串为不带。
	Flow        string
	LimitIP     int
	Enabled     bool
	InboundRefs []string
}

// PasswordOrAuth 返回面板 password 字段该用的值：自设的连接密码，没设时沿用 Auth。
func (c ClientSpec) PasswordOrAuth() string {
	if c.Password != "" {
		return c.Password
	}
	return c.Auth
}

// Client 是面板上一个 client 的现状；Up / Down 是面板累计的流量计数（字节）。
type Client struct {
	Email   string
	Enabled bool
	LimitIP int
	// Flow 是 client 的 VLESS 流控（如 xtls-rprx-vision），空为不带；面板只在支持它的入站上生效（Ruling-77）。
	Flow        string
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

// OutboundTestMode 是出站测试的方式（docs/03 §5.21 延迟测试，Ruling-52）。
type OutboundTestMode string

// 出站测试的三种方式。
const (
	// OutboundTestTCP 只做 TCP 连接。
	OutboundTestTCP OutboundTestMode = "tcp"
	// OutboundTestHTTP 经出站发一次 HTTP 请求，报热连接的往返时间，并带出口 IP。
	OutboundTestHTTP OutboundTestMode = "http"
	// OutboundTestReal 是真实延迟：含建立隧道在内的整次请求耗时。
	OutboundTestReal OutboundTestMode = "real"
)

// OutboundFailure 是面板适配层对一次不通的探测的归类（docs/03 §5.21，Ruling-56）；只说这一次探测断在哪，不含地址。
type OutboundFailure string

// 一次探测不通的几种情形；连通时为 OutboundFailNone。
const (
	OutboundFailNone OutboundFailure = ""
	// OutboundFailRefused 对端拒绝连接。
	OutboundFailRefused OutboundFailure = "refused"
	// OutboundFailTimeout 等到超时也没有结果。
	OutboundFailTimeout OutboundFailure = "timeout"
	// OutboundFailAborted 连接或请求被对端断开。
	OutboundFailAborted OutboundFailure = "aborted"
	// OutboundFailOther 其余情形（含面板拒绝执行这次测试）。
	OutboundFailOther OutboundFailure = "other"
)

// OutboundTestResult 是一次出站测试的结果。连不上不算调用失败：OK 为 false，Failure 是归类，
// Message 是面板给出的原因（可能含地址，只给管理员看）。
type OutboundTestResult struct {
	OK bool
	// Failure 在 OK 为 false 时必不为 OutboundFailNone。
	Failure OutboundFailure
	// Mode 是面板实际采用的方式（UDP 类出站在 tcp 方式下会被面板改走 http）。
	Mode         OutboundTestMode
	DelayMs      int64
	HTTPStatus   int
	EgressIP     string
	EgressRegion string
	Message      string
}

// OutboundTester 是可选能力：让面板所在的机器去连一个出站并报告延迟。业务层用类型断言判断面板支不支持。
// outbound 是一个完整的内核出站配置（含凭据，不得写日志），必须带 tag。
type OutboundTester interface {
	TestOutbound(ctx context.Context, outbound json.RawMessage, mode OutboundTestMode) (OutboundTestResult, error)
}

// CertFiles 是面板所在机器上的一对证书 / 私钥文件路径；没配时为空串。
type CertFiles struct {
	CertFile string
	KeyFile  string
}

// CertLocator 是可选能力：取面板自己的网页证书路径，供新建入站的 TLS 证书预填（Ruling-53）。只有路径，没有证书内容。
type CertLocator interface {
	WebCertFiles(ctx context.Context) (CertFiles, error)
}

// RealityScan 是面板对一个 REALITY 目标的探测结果（docs/03 §5.2 REALITY 目标扫描，Ruling-54）。
type RealityScan struct {
	Target   string
	Host     string
	Port     int
	IP       string
	Feasible bool
	// Reason 是面板给出的不可用原因；可用时为空。
	Reason      string
	LatencyMs   int64
	TLSVersion  string
	ALPN        string
	CertValid   bool
	CertSubject string
	CertIssuer  string
	// ServerNames 是证书里可作 SNI 的域名。
	ServerNames []string
	// PrivateTarget 表示目标解析到内网 / 回环地址，面板没有探测。
	PrivateTarget bool
}

// RealityScanner 是可选能力：让面板所在的机器探测 REALITY 目标。实现不得开启对内网地址的探测。
type RealityScanner interface {
	// ScanRealityTarget 探测一个目标；sni 为空用目标的域名，xver 是目标期望的 PROXY 协议版本。
	ScanRealityTarget(ctx context.Context, target, sni string, xver int) (RealityScan, error)
	// ScanRealityTargets 探测一组目标并按可用性与延迟排序；targets 为空时探测面板自己的候选站点。
	ScanRealityTargets(ctx context.Context, targets []string) ([]RealityScan, error)
}
