// Package ipprobe 是服务器 IP 质量检测的服务器侧（docs/03 §5.23，Ruling-81）：
// 把包内的探测表渲染成一段只依赖 sh 与 curl 的固定脚本，经 SSH 在主机上执行，解析回传的“键=值”行并在平台上判定。
// 本包不认识面板、不连数据库；脚本里非常量的参数只有校验过的主机名、随机标签与联调用的覆盖地址，都经 remote.Quote 拼入。
package ipprobe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"v2-hub-server/internal/manager/remote"
)

const (
	// userAgent 是探测请求统一用的浏览器标识（部分站点对 curl 默认标识直接拒绝）。
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36"
	// maxValueLen 是回传的单个值的长度上限；脚本里已截断，这里再防一道。
	maxValueLen = 400
	// curlExitResolve 是 curl 的“解析不了主机名”退出码。
	curlExitResolve = 6
	// curlExitConnect 是 curl 的“连不上”退出码。
	curlExitConnect = 7
	// curlExitTimeout 是 curl 的“超时”退出码。
	curlExitTimeout = 28
	// exitTimeoutCmd 是 timeout(1) 超时时的退出码（bash 退而求其次的 25 端口探测）。
	exitTimeoutCmd = 124
	// exitMissing 表示回传里没有这个退出码（脚本没跑到或被截断）。
	exitMissing = -1
	// exitNoTool 表示主机上没有能连 25 端口的工具。
	exitNoTool = -2
	flagOn     = "1"
	noToolMark = "nt"
	// smtpPort 是邮局收信端口。
	smtpPort    = 25
	keyToolCurl = "tool.curl"
	keyToolTel  = "tool.telnet"
	keyToolBash = "tool.bash"
	// randomLabelPrefix 是随机子域名的前缀，便于在对端日志里认出来。
	randomLabelPrefix = "hubprobe-"
)

var (
	// ErrBadOverrides 表示联调覆盖地址不合法。
	ErrBadOverrides = errors.New("ipprobe overrides are invalid")
	// ErrNoCurl 表示主机上没有 curl，服务器侧整段做不了。
	ErrNoCurl = errors.New("curl is not available on host")

	keyPattern      = regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]+){1,2}$`)
	noncePattern    = regexp.MustCompile(`^[a-z0-9]{12}$`)
	hostnamePattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)+$`)
	codePattern     = regexp.MustCompile(`^[0-9]{3}$`)
	// sharedAddressSpace 是运营商级 NAT 的共享地址段（RFC 6598），netip 不把它算作私网。
	sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")
)

// Resolver 是平台侧的 DNS 查询能力（*net.Resolver 满足）。
type Resolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
}

// Overrides 只给联调用：把探测目标换成模拟端。两项都空 = 用真实目标。
type Overrides struct {
	// BaseURL 非空时每个 HTTP 探测改为请求 BaseURL + "/" + 探测键，且不做随机子域名检查。
	BaseURL string
	// MailAddr 非空时（host:port）邮局一律连这个地址，不查 MX。
	MailAddr string
}

// Prober 在主机上执行服务器侧探测，并发安全。
type Prober struct {
	resolver Resolver
	baseURL  string
	mailHost string
	mailPort int
}

// New 校验覆盖地址并返回 Prober；resolver 用来查邮局 MX 与对照随机子域名。
func New(resolver Resolver, ov Overrides) (*Prober, error) {
	p := &Prober{resolver: resolver, mailPort: smtpPort}
	if ov.BaseURL != "" {
		u, err := url.Parse(ov.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("%w: base url %q", ErrBadOverrides, ov.BaseURL)
		}
		p.baseURL = strings.TrimRight(ov.BaseURL, "/")
	}
	if ov.MailAddr != "" {
		host, port, err := net.SplitHostPort(ov.MailAddr)
		n, perr := strconv.Atoi(port)
		if err != nil || perr != nil || n <= 0 || n > 65535 || !validHost(host) {
			return nil, fmt.Errorf("%w: mail addr %q", ErrBadOverrides, ov.MailAddr)
		}
		p.mailHost, p.mailPort = host, n
	}
	return p, nil
}

// Egress 是服务器侧取到的出口信息；取不到时 IP 为零值。
type Egress struct {
	IP      netip.Addr
	Country string
}

// Output 是一次服务器侧探测的结果，各列表按 Key 排序。
type Output struct {
	Egress Egress
	Media  []MediaResult
	Mail   []MailResult
}

// Run 查邮局 MX、在主机上执行探测脚本并判定。nonce 是 12 位小写字母数字的随机标签（随机子域名用）。
// 主机上没有 curl 返回 ErrNoCurl；SSH 层面的错误原样返回（可用 remote.AsError 归类）。
func (p *Prober) Run(ctx context.Context, r remote.Runner, nonce string) (*Output, error) {
	if !noncePattern.MatchString(nonce) {
		return nil, fmt.Errorf("ipprobe: bad nonce %q", nonce)
	}
	targets := p.mailTargets(ctx)
	script := p.render(targets, nonce)
	res, err := r.Run(ctx, script)
	if err != nil {
		return nil, err
	}
	kv := parseOutput(res.Output, allowedKeys(targets))
	if kv[keyToolCurl] != flagOn {
		return nil, ErrNoCurl
	}
	out := &Output{Egress: judgeEgress(kv)}
	random := p.randomResolvable(ctx, kv, nonce)
	for _, svc := range mediaServices {
		out.Media = append(out.Media, judgeMedia(svc, fetchesOf(kv, svc.Fetches), random[svc.Key]))
	}
	noTool := kv[keyToolTel] != flagOn && kv[keyToolBash] != flagOn
	for _, t := range targets {
		out.Mail = append(out.Mail, judgeMail(t, kv, noTool))
	}
	sort.Slice(out.Media, func(i, j int) bool { return out.Media[i].Key < out.Media[j].Key })
	sort.Slice(out.Mail, func(i, j int) bool { return out.Mail[i].Key < out.Mail[j].Key })
	return out, nil
}

// urlOf 是探测实际请求的地址：联调覆盖时换成模拟端。
func (p *Prober) urlOf(f fetch) string {
	if p.baseURL != "" {
		return p.baseURL + "/" + f.Key
	}
	return f.URL
}

// randomResolvable 判断每个服务的随机子域名是否“主机上能解析、平台上解析不到”（DNS 解锁的信号之一）。
// 联调覆盖时不做（模拟端没有域名）。
func (p *Prober) randomResolvable(ctx context.Context, kv map[string]string, nonce string) map[string]bool {
	out := make(map[string]bool, len(mediaServices))
	if p.baseURL != "" || p.resolver == nil {
		return out
	}
	for _, svc := range mediaServices {
		code, err := strconv.Atoi(kv[randomKey(svc.Key)])
		if err != nil || code == curlExitResolve {
			continue
		}
		_, lerr := p.resolver.LookupHost(ctx, randomHost(nonce, svc))
		var dnsErr *net.DNSError
		if errors.As(lerr, &dnsErr) && dnsErr.IsNotFound {
			out[svc.Key] = true
		}
	}
	return out
}

func randomHost(nonce string, svc mediaService) string {
	return randomLabelPrefix + nonce + "." + svc.Domain
}

func randomKey(service string) string { return "rd." + service }

// parseOutput 只收 allowed 里的键，值截到上限；同一个键出现多次取第一次。
func parseOutput(output string, allowed map[string]bool) map[string]string {
	kv := make(map[string]string, len(allowed))
	for _, line := range strings.Split(output, "\n") {
		k, v, ok := strings.Cut(strings.TrimRight(line, "\r"), "=")
		if !ok || !keyPattern.MatchString(k) || !allowed[k] {
			continue
		}
		if _, seen := kv[k]; seen {
			continue
		}
		if len(v) > maxValueLen {
			v = v[:maxValueLen]
		}
		kv[k] = strings.TrimSpace(v)
	}
	return kv
}

// allowedKeys 是脚本可能回传的全部键（由探测表推出）。
func allowedKeys(targets []MailTarget) map[string]bool {
	keys := map[string]bool{keyToolCurl: true, keyToolTel: true, keyToolBash: true}
	add := func(f fetch) {
		keys[f.Key+".exit"], keys[f.Key+".w"] = true, true
		for _, m := range f.Marks {
			keys[f.Key+".m."+m.Name] = true
		}
		for _, g := range f.Grabs {
			keys[f.Key+".g."+g.Name] = true
		}
	}
	for _, f := range egressFetches {
		add(f)
	}
	for _, svc := range mediaServices {
		keys[randomKey(svc.Key)] = true
		for _, f := range svc.Fetches {
			add(f)
		}
	}
	for _, t := range targets {
		keys[mailKey(t.Key)+".exit"], keys[mailKey(t.Key)+".code"] = true, true
	}
	return keys
}

// Fetch 是一次 HTTP 探测解析后的结果。
type Fetch struct {
	// Exit 是 curl 的退出码；没有回传为 exitMissing。
	Exit     int
	Code     int
	RemoteIP netip.Addr
	URL      string
	Marks    map[string]int
	Grabs    map[string]string
}

// OK 报告请求是否拿到了 HTTP 响应。
func (f Fetch) OK() bool { return f.Exit == 0 && f.Code > 0 }

func parseFetch(kv map[string]string, def fetch) Fetch {
	f := Fetch{Exit: exitMissing, Marks: make(map[string]int, len(def.Marks)), Grabs: make(map[string]string, len(def.Grabs))}
	if n, err := strconv.Atoi(kv[def.Key+".exit"]); err == nil {
		f.Exit = n
	}
	if parts := strings.Fields(kv[def.Key+".w"]); len(parts) > 0 {
		f.Code, _ = strconv.Atoi(parts[0]) // 不是数字时为 0，按没拿到响应处理
		if len(parts) > 1 {
			if ip, err := netip.ParseAddr(parts[1]); err == nil {
				f.RemoteIP = ip.Unmap()
			}
		}
		if len(parts) > 2 {
			f.URL = parts[2]
		}
	}
	for _, m := range def.Marks {
		f.Marks[m.Name], _ = strconv.Atoi(kv[def.Key+".m."+m.Name]) // 空串（文件不存在）按 0 次
	}
	for _, g := range def.Grabs {
		f.Grabs[g.Name] = kv[def.Key+".g."+g.Name]
	}
	return f
}

func fetchesOf(kv map[string]string, defs []fetch) map[string]Fetch {
	out := make(map[string]Fetch, len(defs))
	for _, d := range defs {
		out[d.Key] = parseFetch(kv, d)
	}
	return out
}

// isInternal 报告对端地址是不是内网、回环、链路本地或运营商级 NAT（DNS 解锁会把站点解析到这类地址的代理上）。
func isInternal(ip netip.Addr) bool {
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || sharedAddressSpace.Contains(ip)
}

func validHost(h string) bool {
	if ip, err := netip.ParseAddr(h); err == nil {
		return ip.IsValid()
	}
	return len(h) <= 253 && hostnamePattern.MatchString(h)
}
