// Package service 承载 hub-server 的业务逻辑。业务语义以 docs/03-业务语义.md 为准，
// 代码注释只引编号（R-N、T-N、Ruling-N）。
package service

import "fmt"

// Code 是对外错误码，取值与 docs/03 §5 错误码表逐行对应。
type Code string

const (
	CodeInvalidArgument  Code = "invalid_argument"
	CodeUnauthenticated  Code = "unauthenticated"
	CodePermissionDenied Code = "permission_denied"
	CodeNotFound         Code = "not_found"
	CodeConflict         Code = "conflict"
	CodeRateLimited      Code = "rate_limited"
	CodeTOTPRequired     Code = "totp_required"
	// CodeTOTPSetupRequired 表示超管尚未开启两步验证，须先开启（R10）。
	CodeTOTPSetupRequired Code = "totp_setup_required"
	// CodeReauthRequired 表示高危操作须先做二次验证（R12，docs/03 §4.4）。
	CodeReauthRequired Code = "reauth_required"
	CodeUpstream       Code = "upstream_unavailable"
	CodeInternal       Code = "internal"
	// CodeFeatureDisabled 表示功能未开启（注册关闭、邮件未配置等，docs/03 §5）。
	CodeFeatureDisabled Code = "feature_disabled"
	// CodeInsufficientBalance 表示余额不足以支付本次下单（docs/03 §5.13）；Error.Data 带差额。
	CodeInsufficientBalance Code = "insufficient_balance"
)

// Error 是业务错误：Code 决定 HTTP 状态，Message 是可直接展示给用户的中文说明（不含内部细节）。
type Error struct {
	Code    Code
	Message string
	// Data 是随错误一起返回给前端的附加数据（如余额不足时的差额）；多数错误为 nil。不得放内部细节。
	Data any
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Is 让 errors.Is(err, ErrXxx) 只按 Code 匹配，Message 不参与。
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// 供 errors.Is 判定的哨兵值。
var (
	ErrInvalidArgument   = &Error{Code: CodeInvalidArgument}
	ErrUnauthenticated   = &Error{Code: CodeUnauthenticated}
	ErrPermissionDenied  = &Error{Code: CodePermissionDenied}
	ErrNotFound          = &Error{Code: CodeNotFound}
	ErrConflict          = &Error{Code: CodeConflict}
	ErrRateLimited       = &Error{Code: CodeRateLimited}
	ErrTOTPRequired      = &Error{Code: CodeTOTPRequired}
	ErrTOTPSetupRequired = &Error{Code: CodeTOTPSetupRequired}
	ErrReauthRequired    = &Error{Code: CodeReauthRequired}
	ErrUpstream          = &Error{Code: CodeUpstream}
	ErrFeatureDisabled   = &Error{Code: CodeFeatureDisabled}
	ErrInsufficient      = &Error{Code: CodeInsufficientBalance}
)

func newError(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

const (
	msgBadCredentials = "用户名或密码错误"
	msgLoginRequired  = "请先登录"
	msgNoPermission   = "没有权限执行该操作"
	msgTooManyLogin   = "登录失败次数过多，请稍后再试"
	msgTOTPRequired   = "请输入两步验证码"
	msgTOTPInvalid    = "验证码错误"
)
