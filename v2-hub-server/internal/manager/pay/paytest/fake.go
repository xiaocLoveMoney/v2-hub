// Package paytest 提供测试用的内存支付网关，不发任何网络请求。
package paytest

import (
	"context"
	"errors"
	"strconv"
	"sync"

	"v2-hub-server/internal/manager/pay"
)

const (
	// Code 是假网关的标识。
	Code = "fake"
	// GoodSign 是假网关认可的签名值；其它值一律验签失败。
	GoodSign = "valid"
	// Ack 是假网关要求的应答文本。
	Ack = "success"
	// StatusClosed 表示“不是支付成功”的通知。
	StatusClosed = "closed"

	fieldTradeNo        = "trade_no"
	fieldGatewayTradeNo = "gateway_trade_no"
	fieldAmount         = "amount"
	fieldSign           = "sign"
	fieldStatus         = "status"
	payURLPrefix        = "https://pay.example.com/"
)

// Fake 是内存里的支付网关：记下收到的下单请求；通知用表单字段表达，sign 等于 GoodSign 才算验签通过。
type Fake struct {
	mu      sync.Mutex
	created []pay.CreateRequest
	fail    error
}

// Code 实现 pay.Gateway。
func (f *Fake) Code() string { return Code }

// Name 实现 pay.Gateway。
func (f *Fake) Name() string { return "测试支付" }

// FailWith 让之后的 Create 都返回 err；传 nil 恢复。
func (f *Fake) FailWith(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = err
}

// Created 返回收到的下单请求的副本。
func (f *Fake) Created() []pay.CreateRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pay.CreateRequest(nil), f.created...)
}

// Create 实现 pay.Gateway。
func (f *Fake) Create(_ context.Context, req pay.CreateRequest) (*pay.CreateResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	if req.TradeNo == "" || req.Amount <= 0 {
		return nil, errors.New("paytest: bad create request")
	}
	f.created = append(f.created, req)
	return &pay.CreateResult{PayURL: payURLPrefix + req.TradeNo}, nil
}

// ParseNotify 实现 pay.Gateway：从表单取字段，签名不对或金额不是整数返回 pay.ErrBadNotify。
func (f *Fake) ParseNotify(_ context.Context, n pay.NotifyRequest) (*pay.Notification, error) {
	if n.Form.Get(fieldSign) != GoodSign {
		return nil, pay.ErrBadNotify
	}
	amount, err := strconv.ParseInt(n.Form.Get(fieldAmount), 10, 64)
	if err != nil {
		return nil, pay.ErrBadNotify
	}
	return &pay.Notification{
		TradeNo: n.Form.Get(fieldTradeNo), GatewayTradeNo: n.Form.Get(fieldGatewayTradeNo), Amount: amount,
		Paid: n.Form.Get(fieldStatus) != StatusClosed, Ack: Ack,
	}, nil
}

// Notify 构造一条通知的表单内容，供测试直接传给 ParseNotify 或经 HTTP 发出。
func Notify(tradeNo, gatewayTradeNo string, amount int64, sign, status string) map[string][]string {
	return map[string][]string{
		fieldTradeNo: {tradeNo}, fieldGatewayTradeNo: {gatewayTradeNo}, fieldAmount: {strconv.FormatInt(amount, 10)}, fieldSign: {sign}, fieldStatus: {status},
	}
}
