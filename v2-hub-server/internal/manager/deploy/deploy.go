// Package deploy 定义“经 SSH 部署与运维一种面板”的抽象（docs/03 §5.22，Ruling-69）。
// 每种面板一个 Deployer 实现，按 provider_type 注册；业务层只依赖本包的接口与中立结果，不按面板类型写分支。
package deploy

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"v2-hub-server/internal/manager/remote"
)

// Reason 是失败或不能部署的原因大类，取值与 docs/03 §5.22、hub-api 的 HostErrorCode 逐项对应。
type Reason string

const (
	ReasonNone             Reason = ""
	ReasonUnsupportedOS    Reason = "unsupported_os"
	ReasonNoSystemd        Reason = "no_systemd"
	ReasonNoRoot           Reason = "no_root"
	ReasonNonstandard      Reason = "nonstandard_install"
	ReasonListenLocal      Reason = "listen_local"
	ReasonVersionTooOld    Reason = "version_too_old"
	ReasonDownloadFailed   Reason = "download_failed"
	ReasonPanelUnreachable Reason = "panel_unreachable"
	ReasonAuthFailed       Reason = "auth_failed"
	ReasonUnreachable      Reason = "unreachable"
	ReasonHostKeyChanged   Reason = "host_key_changed"
	ReasonNameTaken        Reason = "name_taken"
	ReasonInterrupted      Reason = "interrupted"
	ReasonCommandFailed    Reason = "command_failed"
)

// PanelState 是主机上面板的安装情况。
type PanelState string

const (
	// PanelNone 表示没有面板，可以安装。
	PanelNone PanelState = "none"
	// PanelStandard 表示有按官方方式安装的面板，可以接管。
	PanelStandard PanelState = "standard"
	// PanelNonstandard 表示有面板但不是标准安装（Docker、路径不对、没有服务单元），不接管。
	PanelNonstandard PanelState = "nonstandard"
)

// Plan 是部署时会走的路径。
type Plan string

const (
	PlanNone    Plan = ""
	PlanInstall Plan = "install"
	PlanAdopt   Plan = "adopt"
)

// Detection 是对一台主机的检测结果。Reason 非空时不能部署，Plan 为空，Message 是给人看的说明。
type Detection struct {
	OSID         string
	Arch         string
	IsPrivileged bool
	PanelState   PanelState
	PanelVersion string
	Plan         Plan
	Reason       Reason
	// Message 由实现填写：各面板的门槛（最低版本、支持的安装方式）不同，文案也不同。
	Message string
}

// PanelAccess 是从主机上读出的面板接入信息（不含令牌）。
type PanelAccess struct {
	// HasTLS 表示面板配了证书，走 https。
	HasTLS bool
	Port   int
	// BasePath 以 / 开头并以 / 结尾，根路径为 "/"。
	BasePath string
	// CertSHA256 是面板证书（叶子）的 SHA-256，标准 Base64；没有证书为空串。
	CertSHA256 string
}

// ProvisionOptions 是部署面板的参数。
type ProvisionOptions struct {
	// Address 是平台连这台主机用的地址（IP 或域名）；新装面板时写进自签证书的主体。
	Address string
	// PanelPort 是新装面板时指定的端口；0 表示由实现自己选。接管已有面板时不用。
	PanelPort int
}

// Provisioned 是部署的结果：面板已在运行，Token 可用来接入。
type Provisioned struct {
	Access PanelAccess
	// Token 是平台专用的管理令牌明文；调用方加密保存，不进日志。
	Token string
	// IsFresh 表示面板是这次新装的（证书由平台生成，接入时直接固定指纹）；false 表示接管了已有面板。
	IsFresh bool
}

// PanelLogin 是面板网页的登录信息；只在重置的那一次返回，不落库、不进日志。
type PanelLogin struct {
	Username string
	Password string
}

// Logger 接收一行可进任务日志的文字；实现方负责再脱敏一遍。
type Logger func(line string)

// Deployer 是一种面板的部署与运维动作。所有方法只执行实现内部的固定脚本模板。
type Deployer interface {
	// Detect 检测系统、权限与面板安装情况，并给出部署会走安装还是接管。
	Detect(ctx context.Context, r remote.Runner) (Detection, error)
	// Provision 按 det.Plan 把面板弄到“在运行、平台拿得到令牌”的状态：没有面板就安装，有标准安装的面板就接管。
	// 安装时要做哪些事、先后顺序如何（配证书、放行防火墙等）由各实现自己定；det.Plan 为空时不应调用。
	// 面板只监听本机时返回 ReasonListenLocal。
	Provision(ctx context.Context, r remote.Runner, det Detection, opts ProvisionOptions, log Logger) (Provisioned, error)
	// IssueToken 签发（同名则轮换）平台专用的管理令牌，不动其它令牌与管理员口令。
	IssueToken(ctx context.Context, r remote.Runner) (string, error)
	// EnableTLS 生成自签证书设给面板并重启；address 写进证书的主体，返回证书的 SHA-256（Base64）。
	EnableTLS(ctx context.Context, r remote.Runner, address string) (string, error)
	// RestartPanel 重启面板（连带内核）并确认服务在运行。
	RestartPanel(ctx context.Context, r remote.Runner) error
	// RestartCore 只让面板重载内核。
	RestartCore(ctx context.Context, r remote.Runner) error
	// Upgrade 升级或修复到平台基线版本，保留数据。
	Upgrade(ctx context.Context, r remote.Runner, log Logger) error
	// Diagnose 返回服务状态与最近日志，供人看。
	Diagnose(ctx context.Context, r remote.Runner) (string, error)
	// ResetPanelLogin 把面板网页的登录用户名与口令换成新的随机值。
	ResetPanelLogin(ctx context.Context, r remote.Runner) (PanelLogin, error)
}

// Error 是带原因大类的部署错误；Message 可直接展示，不含凭据。
type Error struct {
	Reason  Reason
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Reason, e.Message) }

// NewError 创建部署错误。
func NewError(reason Reason, message string) *Error {
	return &Error{Reason: reason, Message: message}
}

// AsError 从错误链里取出 *Error；不是部署错误返回 nil。
func AsError(err error) *Error {
	var de *Error
	if errors.As(err, &de) {
		return de
	}
	return nil
}

// Registry 按面板类型保存 Deployer。
type Registry struct {
	deployers map[string]Deployer
}

// NewRegistry 创建注册表；新增面板类型只在组装处加一项。
func NewRegistry(deployers map[string]Deployer) *Registry {
	copied := make(map[string]Deployer, len(deployers))
	for k, v := range deployers {
		copied[k] = v
	}
	return &Registry{deployers: copied}
}

// Get 取某类面板的 Deployer。
func (r *Registry) Get(providerType string) (Deployer, bool) {
	d, ok := r.deployers[providerType]
	return d, ok
}

// reasonByRemoteKind 把 SSH 层的失败大类对到任务的原因大类；两边的取值各自独立，只在这里对应。
var reasonByRemoteKind = map[remote.ErrorKind]Reason{
	remote.KindUnreachable: ReasonUnreachable,
	remote.KindAuth:        ReasonAuthFailed,
	remote.KindHostKey:     ReasonHostKeyChanged,
	remote.KindCommand:     ReasonCommandFailed,
}

// FailureOf 从错误链里认出部署错误或 SSH 错误，给出原因大类与可展示的说明；都不是时 ok 为 false。
func FailureOf(err error) (reason Reason, message string, ok bool) {
	if de := AsError(err); de != nil {
		return de.Reason, de.Message, true
	}
	re := remote.AsError(err)
	if re == nil {
		return ReasonNone, "", false
	}
	reason, known := reasonByRemoteKind[re.Kind]
	if !known {
		reason = ReasonCommandFailed
	}
	return reason, re.Message, true
}

const redactedMark = "[已隐去]"

// sensitiveLine 命中的整行不进日志：安装脚本等会把账号、口令、令牌打印出来。
var sensitiveLine = regexp.MustCompile(`(?i)(password|passwd|token|secret|username|private key|access url|apikey|api key)`)

// Redact 给要进任务日志或错误摘要的远端输出脱敏：去掉提到凭据的整行，再把已知的密文原样替换掉。
func Redact(text string, secrets ...string) string {
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if sensitiveLine.MatchString(line) {
			continue
		}
		kept = append(kept, line)
	}
	out := strings.Join(kept, "\n")
	for _, secret := range secrets {
		if secret != "" {
			out = strings.ReplaceAll(out, secret, redactedMark)
		}
	}
	return out
}
