// Package pay 是在线支付网关的抽象（docs/03 §5.15，Ruling-28）：只用于给余额充值。
// M5 不带具体网关，只定接口与注册表；网关的密钥只走环境变量，不进系统设置表。
package pay

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"time"
)

// ErrBadNotify 表示回调内容无法解析或验签失败；调用方按 400 处理，不入账。
var ErrBadNotify = errors.New("pay: bad notification")

var codePattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// CreateRequest 是发起一笔支付的参数；Amount 为整数分。
type CreateRequest struct {
	TradeNo   string
	Amount    int64
	Subject   string
	NotifyURL string // 网关回调本平台的地址
	ExpiresAt time.Time
}

// CreateResult 是发起支付的结果：跳转地址与二维码内容至少给一个，另一个为空串。
type CreateResult struct {
	PayURL    string
	QRContent string
}

// NotifyRequest 是网关异步通知的原始内容；各网关自己决定从哪里取字段。
type NotifyRequest struct {
	Header map[string][]string
	Query  url.Values
	Form   url.Values
	Body   []byte
}

// Notification 是验签通过后的通知。Paid 为假表示这次通知不是“支付成功”（如关闭、退款），平台不入账。
type Notification struct {
	TradeNo        string // 平台单号
	GatewayTradeNo string // 网关交易号，同一网关内唯一
	Amount         int64  // 实付金额（分），须与充值单一致
	Paid           bool
	// Ack 是网关要求的应答文本（如 success）。
	Ack string
}

// Gateway 是一个在线支付网关。实现必须并发安全。
type Gateway interface {
	// Code 是网关的标识，出现在回调路径里：小写字母、数字、下划线或连字符，1–32 位。
	Code() string
	// Name 是展示给用户的名称。
	Name() string
	// Create 向网关发起一笔支付。
	Create(ctx context.Context, req CreateRequest) (*CreateResult, error)
	// ParseNotify 解析并验签异步通知；内容不合法或验签失败返回 ErrBadNotify。
	ParseNotify(ctx context.Context, n NotifyRequest) (*Notification, error)
}

// Registry 是已配置的网关集合，创建后只读。
type Registry struct {
	byCode map[string]Gateway
	order  []Gateway
}

// NewRegistry 由若干网关构造注册表；标识不合法或重复时返回错误。不传网关即“没有配置在线充值”。
func NewRegistry(gateways ...Gateway) (*Registry, error) {
	r := &Registry{byCode: make(map[string]Gateway, len(gateways)), order: make([]Gateway, 0, len(gateways))}
	for _, g := range gateways {
		code := g.Code()
		if !codePattern.MatchString(code) {
			return nil, fmt.Errorf("pay: invalid gateway code %q", code)
		}
		if _, dup := r.byCode[code]; dup {
			return nil, fmt.Errorf("pay: duplicate gateway code %q", code)
		}
		r.byCode[code] = g
		r.order = append(r.order, g)
	}
	return r, nil
}

// Get 按标识取网关。
func (r *Registry) Get(code string) (Gateway, bool) {
	g, ok := r.byCode[code]
	return g, ok
}

// List 返回全部网关，顺序同注册顺序。
func (r *Registry) List() []Gateway {
	return append([]Gateway(nil), r.order...)
}
