package ipprobe

import (
	"context"
	"sort"
	"strconv"
	"strings"
)

// 邮局 25 端口的结论（docs/03 §5.23 邮局）。
const (
	MailOK        = "ok"
	MailRejected  = "rejected"
	MailRefused   = "refused"
	MailTimeout   = "timeout"
	MailDNSFailed = "dns_failed"
	MailNoTool    = "no_tool"
	MailFailed    = "failed"
)

// 本机 25 端口出站。
const (
	Port25Open    = "open"
	Port25Blocked = "blocked"
	Port25Unknown = "unknown"
)

const smtpGreeting = "220"

// mailProvider 是一家邮局：Key 是报告里的键，Domain 用来查 MX。
type mailProvider struct{ Key, Domain string }

// mailProviders 是检测的 12 家邮局。
var mailProviders = []mailProvider{
	{"gmail", "gmail.com"},
	{"outlook", "outlook.com"},
	{"yahoo", "yahoo.com"},
	{"icloud", "icloud.com"},
	{"qq", "qq.com"},
	{"mailru", "mail.ru"},
	{"aol", "aol.com"},
	{"gmx", "gmx.com"},
	{"mailcom", "mail.com"},
	{"netease163", "163.com"},
	{"sohu", "sohu.com"},
	{"sina", "sina.com"},
}

// MailTarget 是一家邮局要连的地址；MX 为空表示查不到（不探测，记 dns_failed）。
type MailTarget struct {
	Key  string
	MX   string
	Port int
}

// MailResult 是一家邮局的结论。
type MailResult struct {
	Key    string
	MX     string
	Result string
	Code   string
}

func mailKey(provider string) string { return "mx" + provider }

// mailTargets 在平台上查各家的 MX，取优先级最高的一条；联调覆盖时一律连覆盖地址。
func (p *Prober) mailTargets(ctx context.Context) []MailTarget {
	out := make([]MailTarget, 0, len(mailProviders))
	for _, mp := range mailProviders {
		t := MailTarget{Key: mp.Key, Port: p.mailPort}
		switch {
		case p.mailHost != "":
			t.MX = p.mailHost
		case p.resolver != nil:
			t.MX = p.lookupMX(ctx, mp.Domain)
		}
		out = append(out, t)
	}
	return out
}

func (p *Prober) lookupMX(ctx context.Context, domain string) string {
	mxs, err := p.resolver.LookupMX(ctx, domain)
	if err != nil || len(mxs) == 0 {
		return ""
	}
	sort.SliceStable(mxs, func(i, j int) bool { return mxs[i].Pref < mxs[j].Pref })
	host := strings.TrimSuffix(mxs[0].Host, ".")
	if !validHost(host) {
		return "" // 空 MX（"."）或不合法的主机名都按查不到处理
	}
	return host
}

func judgeMail(t MailTarget, kv map[string]string, noTool bool) MailResult {
	out := MailResult{Key: t.Key, MX: t.MX}
	if t.MX == "" {
		out.Result = MailDNSFailed
		return out
	}
	k := mailKey(t.Key)
	if code := kv[k+".code"]; codePattern.MatchString(code) {
		out.Code = code
	}
	raw := kv[k+".exit"]
	exit, err := strconv.Atoi(raw)
	switch {
	case raw == noToolMark:
		exit = exitNoTool
	case err != nil:
		exit = exitMissing
	}
	switch {
	case out.Code == smtpGreeting:
		out.Result = MailOK
	case out.Code != "":
		out.Result = MailRejected
	case exit == exitNoTool || (exit == exitMissing && noTool):
		out.Result = MailNoTool
	case exit == curlExitConnect:
		out.Result = MailRefused
	case exit == curlExitTimeout || exit == exitTimeoutCmd || exit == 0:
		// 连上了但等不到欢迎行也算超时
		out.Result = MailTimeout
	case exit == curlExitResolve:
		out.Result = MailDNSFailed
	default:
		out.Result = MailFailed
	}
	return out
}

// Port25 由各家结论得出本机 25 端口出站：有一家 ok 即 open；没有 ok 且全是被拒或超时为 blocked；其余 unknown。
func Port25(results []MailResult) string {
	blocked, probed := 0, 0
	for _, r := range results {
		switch r.Result {
		case MailOK:
			return Port25Open
		case MailRefused, MailTimeout:
			blocked++
			probed++
		case MailDNSFailed:
			// 没探测，不计
		default:
			probed++
		}
	}
	if probed > 0 && blocked == probed {
		return Port25Blocked
	}
	return Port25Unknown
}
