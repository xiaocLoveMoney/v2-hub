// Package xui 是 3x-ui 面板的 deploy.Deployer 实现（docs/03 §5.22，Ruling-69、Ruling-70）。
// 所有远端命令都是本文件里的固定模板；拼进模板的值要么是本包常量，要么先校验再经 remote.Quote 转义。
package xui

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	_ "embed" // 内嵌上游安装与升级脚本的快照
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"v2-hub-server/internal/manager/crypto"
	"v2-hub-server/internal/manager/deploy"
	"v2-hub-server/internal/manager/remote"
)

// 上游脚本快照，由 `make gen-upstream` 从 contract/upstream/3x-ui/ 拷入（生成物入库）。
var (
	//go:embed upstream/install-v3.11.0.sh
	installScript []byte
	//go:embed upstream/update-v3.11.0.sh
	updateScript []byte
)

const (
	// ProviderType 与 manager/panel/xui.ProviderType 同值。
	ProviderType = "xui"
	// BaselineTag 是平台安装与升级到的 3x-ui 版本（Ruling-7、Ruling-69）；内嵌的脚本快照就是这个 tag 的。
	BaselineTag = "v3.11.0"
	// tokenName 是平台在面板上签发的令牌名；重签只轮换这一个（Ruling-70）。
	tokenName = "v2-hub"
	// minMajorVersion 是能接管的最低主版本（docs/03 §5.1：3x-ui ≥ v3.0）。
	minMajorVersion = 3

	minPort = 1
	maxPort = 65535

	loginUsernameLen = 10
	loginPasswordLen = 20
	loginAlphabet    = "abcdefghijkmnopqrstuvwxyz23456789"
	loginLetters     = "abcdefghijkmnopqrstuvwxyz"

	exitNotFound = 127
	flagOn       = "1"

	msgNoBash         = "目标机没有 bash，无法执行安装脚本"
	msgDownload       = "目标机下载不到安装包，多为连不上 GitHub"
	msgListenLocal    = "面板只监听本机地址，平台连不上"
	msgNoToken        = "面板没有返回令牌，可能版本不支持经命令行签发"
	msgBadAccess      = "读不到面板的端口或路径"
	msgBadCert        = "读不到面板证书"
	msgBadAddress     = "地址不是合法的 IP 或域名"
	msgServiceDown    = "面板服务没有运行"
	msgReloadFailed   = "面板重载内核失败"
	msgTLSFailed      = "证书配置失败"
	msgLoginFailed    = "面板登录口令重置失败"
	msgFirewallFailed = "防火墙放行端口失败"
	msgNoSS           = "主机上没有 ss 命令，读不到正在监听的端口"

	protoTCP = "tcp"
	protoUDP = "udp"
)

// detectMessages 是各原因的说明（docs/03 §5.22 “不能部署的原因”）；版本门槛等只有这种面板才有的细节写在这里。
var detectMessages = map[deploy.Reason]string{
	deploy.ReasonUnsupportedOS: "不是受支持的发行版",
	deploy.ReasonNoSystemd:     "主机没有 systemd",
	deploy.ReasonNoRoot:        "登录用户不是 root，也不能免密 sudo",
	deploy.ReasonNonstandard:   "主机上的面板不是标准安装（如 Docker），请改用手动接入",
	deploy.ReasonVersionTooOld: "已有面板的版本过低，请先升级到 v3.0 以上",
}

// supportedOS 与上游 install.sh 认识的发行版一致，去掉没有 systemd 的 alpine。
var supportedOS = map[string]struct{}{
	"ubuntu": {}, "debian": {}, "armbian": {}, "fedora": {}, "amzn": {}, "virtuozzo": {}, "rhel": {},
	"almalinux": {}, "rocky": {}, "ol": {}, "centos": {}, "arch": {}, "manjaro": {}, "parch": {},
	"opensuse-tumbleweed": {}, "opensuse-leap": {},
}

var (
	versionPattern  = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)`)
	tokenPattern    = regexp.MustCompile(`(?m)^apiToken: (\S+)\s*$`)
	hostnamePattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)
	// downloadFailure 命中上游脚本下载失败时的提示。
	downloadFailure = regexp.MustCompile(`(?i)(access github|failed to download|failed to fetch|could not resolve host)`)
)

const detectScript = `. /etc/os-release 2>/dev/null
echo "os_id=${ID:-}"
echo "arch=$(uname -m)"
if command -v systemctl >/dev/null 2>&1; then echo systemd=1; else echo systemd=0; fi
if [ -x /usr/local/x-ui/x-ui ]; then echo bin=1; echo "version=$(/usr/local/x-ui/x-ui -v 2>/dev/null | head -n 1)"; else echo bin=0; fi
if command -v systemctl >/dev/null 2>&1 && systemctl cat x-ui >/dev/null 2>&1; then echo unit=1; else echo unit=0; fi
if command -v docker >/dev/null 2>&1 && docker ps --format '{{.Image}} {{.Names}}' 2>/dev/null | grep -qi 'x-ui'; then echo docker=1; else echo docker=0; fi
if pgrep -x x-ui >/dev/null 2>&1; then echo proc=1; else echo proc=0; fi
`

const accessScript = `x=/usr/local/x-ui/x-ui
"$x" setting -show 2>/dev/null | sed -n 's/^port: */port=/p; s/^webBasePath: */path=/p'
"$x" setting -getCert 2>/dev/null | sed -n 's/^cert: */cert=/p'
"$x" setting -getListen 2>/dev/null | sed -n 's/^listenIP: */listen=/p'
`

// 开关型参数不带值：上游的参数解析遇到第一个位置参数（如多写的 true）就停，后面的参数会被丢掉。
const tokenScript = `/usr/local/x-ui/x-ui setting -tokenName ` + tokenName + ` -tokenScope admin -getApiToken 2>&1
`

// upstreamScript 执行一份已上传的上游脚本：%[1]s 环境变量，%[2]s 脚本路径（已转义），%[3]s 位置参数。
// 脚本的完整输出含账号与令牌，只留在远端的临时文件里并随即删除，回传的只有末尾 40 行。
const upstreamScript = `command -v bash >/dev/null 2>&1 || exit 127
log=$(mktemp /tmp/hub-log.XXXXXXXX)
%[1]s bash %[2]s %[3]s </dev/null >"$log" 2>&1
rc=$?
tail -n 40 "$log"
rm -f "$log" %[2]s
exit $rc
`

// tlsScript 生成自签证书并设给面板：%[1]s 证书主体，%[2]s subjectAltName（都已转义）。
const tlsScript = `set -e
d=/etc/x-ui/hub-tls
install -d -m 700 "$d"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 3650 -subj %[1]s -addext %[2]s -keyout "$d/panel.key" -out "$d/panel.crt" >/dev/null 2>&1
chmod 600 "$d/panel.key" "$d/panel.crt"
/usr/local/x-ui/x-ui cert -webCert "$d/panel.crt" -webCertKey "$d/panel.key" 2>&1 | grep -c 'success' >/dev/null
systemctl restart x-ui
cat "$d/panel.crt"
`

// firewallScript 只在主机自带防火墙开着时放行端口：%[1]s 是空格分隔的“端口/协议”列表（已校验，只有数字与 tcp / udp）。
const firewallScript = `specs=%[1]s
if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q '^Status: active'; then
  for p in $specs; do ufw allow "$p" >/dev/null || exit 1; done
fi
if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
  for p in $specs; do firewall-cmd --permanent --add-port="$p" >/dev/null || exit 1; done
  firewall-cmd --reload >/dev/null || exit 1
fi
exit 0
`

// listeningScript 列出正在监听的 TCP / UDP 端口：每行“协议 本地地址”（ss 是 iproute2 的，受支持的发行版都有）。
const listeningScript = `command -v ss >/dev/null 2>&1 || exit 127
ss -Hltnu 2>/dev/null | awk '{print $1, $5}'
`

const (
	restartPanelScript = "systemctl restart x-ui && sleep 2 && systemctl is-active --quiet x-ui\n"
	restartCoreScript  = "systemctl reload x-ui\n"
	isActiveScript     = "systemctl is-active --quiet x-ui\n"
	// catScript 读一个文件：%[1]s 路径（已转义）。
	catScript = "cat %[1]s\n"
	// loginScript 重设面板登录：%[1]s 用户名，%[2]s 口令（都已转义）。上游成功时打印 "updated successfully"。
	loginScript = `/usr/local/x-ui/x-ui setting -username %[1]s -password %[2]s 2>&1 | grep -q 'updated successfully'
`
)

const diagnoseScript = `echo "面板版本: $(/usr/local/x-ui/x-ui -v 2>/dev/null | head -n 1)"
echo "服务状态: $(systemctl is-active x-ui 2>&1)"
echo "开机自启: $(systemctl is-enabled x-ui 2>&1)"
echo "系统负载: $(uptime 2>/dev/null)"
echo "根分区: $(df -h / 2>/dev/null | tail -n 1)"
echo "---- 最近日志 ----"
journalctl -u x-ui -n 60 --no-pager 2>/dev/null || true
`

// Deployer 是 3x-ui 的部署实现；无状态，可并发使用。
type Deployer struct{}

// New 创建 3x-ui 的 Deployer。
func New() *Deployer { return &Deployer{} }

var _ deploy.Deployer = (*Deployer)(nil)

// parseKV 把每行 key=value 的输出解析成表；没有等号的行忽略。
func parseKV(output string) map[string]string {
	kv := make(map[string]string, 8)
	for _, line := range strings.Split(output, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			kv[k] = strings.TrimSpace(v)
		}
	}
	return kv
}

func commandError(message string) error {
	return deploy.NewError(deploy.ReasonCommandFailed, message)
}

// Detect 见 deploy.Deployer。
func (d *Deployer) Detect(ctx context.Context, r remote.Runner) (deploy.Detection, error) {
	res, err := r.Run(ctx, detectScript)
	if err != nil {
		return deploy.Detection{}, err
	}
	return classify(parseKV(res.Output), r.IsPrivileged()), nil
}

// classify 按 docs/03 §5.22 “接管”一行的条件给检测结果定性；判断顺序即原因的优先级。
func classify(kv map[string]string, privileged bool) deploy.Detection {
	det := deploy.Detection{OSID: kv["os_id"], Arch: kv["arch"], IsPrivileged: privileged, PanelState: panelState(kv)}
	if det.PanelState == deploy.PanelStandard {
		det.PanelVersion = strings.TrimPrefix(kv["version"], "v")
	}
	_, osOK := supportedOS[det.OSID]
	switch {
	case !privileged:
		det.Reason = deploy.ReasonNoRoot
	case kv["systemd"] != flagOn:
		det.Reason = deploy.ReasonNoSystemd
	case !osOK:
		det.Reason = deploy.ReasonUnsupportedOS
	case det.PanelState == deploy.PanelNonstandard:
		det.Reason = deploy.ReasonNonstandard
	case det.PanelState == deploy.PanelStandard && !versionSupported(kv["version"]):
		det.Reason = deploy.ReasonVersionTooOld
	case det.PanelState == deploy.PanelStandard:
		det.Plan = deploy.PlanAdopt
	default:
		det.Plan = deploy.PlanInstall
	}
	det.Message = detectMessages[det.Reason]
	return det
}

func panelState(kv map[string]string) deploy.PanelState {
	hasBin, hasUnit := kv["bin"] == flagOn, kv["unit"] == flagOn
	switch {
	case hasBin && hasUnit:
		return deploy.PanelStandard
	case hasBin || hasUnit || kv["docker"] == flagOn || kv["proc"] == flagOn:
		return deploy.PanelNonstandard
	default:
		return deploy.PanelNone
	}
}

func versionSupported(version string) bool {
	m := versionPattern.FindStringSubmatch(strings.TrimSpace(version))
	if m == nil {
		return false
	}
	major, err := strconv.Atoi(m[1])
	return err == nil && major >= minMajorVersion
}

// runUpstream 上传并执行一份上游脚本，失败时把脱敏后的末尾输出写进日志。
func runUpstream(ctx context.Context, r remote.Runner, script []byte, env, args, what string, log deploy.Logger) error {
	path, err := r.Upload(ctx, script)
	if err != nil {
		return err
	}
	res, err := r.Run(ctx, fmt.Sprintf(upstreamScript, env, remote.Quote(path), args))
	if err != nil {
		return err
	}
	if res.ExitCode == 0 {
		return nil
	}
	if res.ExitCode == exitNotFound {
		return commandError(msgNoBash)
	}
	log(deploy.Redact(res.Output))
	if downloadFailure.MatchString(res.Output) {
		return deploy.NewError(deploy.ReasonDownloadFailed, msgDownload)
	}
	return commandError(fmt.Sprintf("%s脚本以状态 %d 退出", what, res.ExitCode))
}

// Provision 见 deploy.Deployer。3x-ui 的安装顺序（Ruling-70）：跑上游安装脚本（装完是明文 HTTP）→ 生成自签证书并改成 HTTPS
// → 读出端口与路径 → 放行防火墙 → 签发令牌；接管只做“读出接入信息、签发令牌”两步，不改面板的其它配置。
func (d *Deployer) Provision(ctx context.Context, r remote.Runner, det deploy.Detection, opts deploy.ProvisionOptions, log deploy.Logger) (deploy.Provisioned, error) {
	isFresh := det.Plan == deploy.PlanInstall
	if isFresh {
		log("安装 3x-ui " + BaselineTag)
		if err := d.Install(ctx, r, opts.PanelPort, log); err != nil {
			return deploy.Provisioned{}, err
		}
		log("安装完成，生成自签证书并改为 HTTPS")
		if _, err := d.EnableTLS(ctx, r, opts.Address); err != nil {
			return deploy.Provisioned{}, err
		}
	}
	access, err := d.Access(ctx, r)
	if err != nil {
		return deploy.Provisioned{}, err
	}
	if isFresh {
		if err := d.OpenPorts(ctx, r, []deploy.Port{{Number: access.Port}}); err != nil {
			return deploy.Provisioned{}, err
		}
	}
	token, err := d.IssueToken(ctx, r)
	if err != nil {
		return deploy.Provisioned{}, err
	}
	return deploy.Provisioned{Access: access, Token: token, IsFresh: isFresh}, nil
}

func isValidPort(port int) bool { return port >= minPort && port <= maxPort }

// Install 在没有面板的主机上安装基线版本；panelPort 为 0 表示由安装脚本随机选端口。装完面板是明文 HTTP。
func (d *Deployer) Install(ctx context.Context, r remote.Runner, panelPort int, log deploy.Logger) error {
	env := "XUI_NONINTERACTIVE=1 XUI_SSL_MODE=none XUI_DB_TYPE=sqlite"
	if panelPort != 0 {
		if !isValidPort(panelPort) {
			return commandError("面板端口不合法")
		}
		env += " XUI_PANEL_PORT=" + strconv.Itoa(panelPort)
	}
	return runUpstream(ctx, r, installScript, env, BaselineTag, "安装", log)
}

// Upgrade 见 deploy.Deployer。
func (d *Deployer) Upgrade(ctx context.Context, r remote.Runner, log deploy.Logger) error {
	if err := runUpstream(ctx, r, updateScript, "XUI_UPDATE_TAG="+BaselineTag, "", "升级", log); err != nil {
		return err
	}
	return expectOK(ctx, r, isActiveScript, msgServiceDown)
}

// expectOK 跑一段只看退出码的脚本，非 0 即返回带 message 的部署错误。
func expectOK(ctx context.Context, r remote.Runner, script, message string) error {
	res, err := r.Run(ctx, script)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return commandError(message)
	}
	return nil
}

// Access 读出面板的端口、路径与证书情况。面板只监听本机时返回 ReasonListenLocal。
func (d *Deployer) Access(ctx context.Context, r remote.Runner) (deploy.PanelAccess, error) {
	res, err := r.Run(ctx, accessScript)
	if err != nil {
		return deploy.PanelAccess{}, err
	}
	kv := parseKV(res.Output)
	port, err := strconv.Atoi(kv["port"])
	if err != nil || !isValidPort(port) {
		return deploy.PanelAccess{}, commandError(msgBadAccess)
	}
	if isLocalListen(kv["listen"]) {
		return deploy.PanelAccess{}, deploy.NewError(deploy.ReasonListenLocal, msgListenLocal)
	}
	access := deploy.PanelAccess{Port: port, BasePath: normalizeBasePath(kv["path"])}
	certPath := kv["cert"]
	if certPath == "" {
		return access, nil
	}
	certRes, err := r.Run(ctx, fmt.Sprintf(catScript, remote.Quote(certPath)))
	if err != nil {
		return deploy.PanelAccess{}, err
	}
	pin, err := certFingerprint(certRes.Output)
	if err != nil || certRes.ExitCode != 0 {
		return deploy.PanelAccess{}, commandError(msgBadCert)
	}
	access.HasTLS = true
	access.CertSHA256 = pin
	return access, nil
}

func isLocalListen(listen string) bool {
	if listen == "localhost" {
		return true
	}
	addr, err := netip.ParseAddr(listen)
	return err == nil && addr.IsLoopback()
}

func normalizeBasePath(p string) string {
	p = "/" + strings.Trim(strings.TrimSpace(p), "/")
	if p != "/" {
		p += "/"
	}
	return p
}

// certFingerprint 取 PEM 里第一张证书（叶子）的 SHA-256，标准 Base64，与 server.cert_pin_sha256 的格式一致。
func certFingerprint(pemText string) (string, error) {
	cert, err := parseLeaf(pemText)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(cert.Raw)
	return base64.StdEncoding.EncodeToString(sum[:]), nil
}

// parseLeaf 解析 PEM 里的第一张证书（叶子）；没有 PEM 块或证书不合法时返回 error。
func parseLeaf(pemText string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("no pem block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	return cert, nil
}

// IssueToken 见 deploy.Deployer。命令的输出含令牌明文，只在这里解析，不向外传。
func (d *Deployer) IssueToken(ctx context.Context, r remote.Runner) (string, error) {
	res, err := r.Run(ctx, tokenScript)
	if err != nil {
		return "", err
	}
	m := tokenPattern.FindStringSubmatch(res.Output)
	if m == nil {
		return "", commandError(msgNoToken)
	}
	return m[1], nil
}

// EnableTLS 见 deploy.Deployer。
func (d *Deployer) EnableTLS(ctx context.Context, r remote.Runner, address string) (string, error) {
	san, err := subjectAltName(address)
	if err != nil {
		return "", err
	}
	res, err := r.Run(ctx, fmt.Sprintf(tlsScript, remote.Quote("/CN="+address), remote.Quote("subjectAltName="+san)))
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", commandError(msgTLSFailed)
	}
	pin, err := certFingerprint(res.Output)
	if err != nil {
		return "", commandError(msgTLSFailed)
	}
	return pin, nil
}

func subjectAltName(address string) (string, error) {
	if addr, err := netip.ParseAddr(address); err == nil {
		return "IP:" + addr.String(), nil
	}
	if !hostnamePattern.MatchString(address) {
		return "", commandError(msgBadAddress)
	}
	return "DNS:" + address, nil
}

// OpenPorts 见 deploy.Deployer。
func (d *Deployer) OpenPorts(ctx context.Context, r remote.Runner, ports []deploy.Port) error {
	if len(ports) == 0 {
		return nil
	}
	specs := make([]string, 0, len(ports))
	for _, p := range ports {
		if !isValidPort(p.Number) {
			return commandError(msgFirewallFailed)
		}
		specs = append(specs, strconv.Itoa(p.Number)+"/"+portProto(p))
	}
	return expectOK(ctx, r, fmt.Sprintf(firewallScript, remote.Quote(strings.Join(specs, " "))), msgFirewallFailed)
}

func portProto(p deploy.Port) string {
	if p.UDP {
		return protoUDP
	}
	return protoTCP
}

// ListeningPorts 见 deploy.Deployer。
func (d *Deployer) ListeningPorts(ctx context.Context, r remote.Runner) ([]deploy.Port, error) {
	res, err := r.Run(ctx, listeningScript)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, commandError(msgNoSS)
	}
	return parseListening(res.Output), nil
}

// parseListening 解析“协议 本地地址”行；地址形如 0.0.0.0:443、[::]:443、*:80、127.0.0.53%lo:53，取最后一个冒号后的端口。
// 同一端口同一协议只留一条；认不出的行跳过。
func parseListening(output string) []deploy.Port {
	seen := make(map[deploy.Port]bool, 32)
	out := make([]deploy.Port, 0, 32)
	for _, line := range strings.Split(output, "\n") {
		proto, addr, ok := strings.Cut(strings.TrimSpace(line), " ")
		i := strings.LastIndex(addr, ":")
		if !ok || i < 0 || (proto != protoTCP && proto != protoUDP) {
			continue
		}
		n, err := strconv.Atoi(addr[i+1:])
		if err != nil || !isValidPort(n) {
			continue
		}
		p := deploy.Port{Number: n, UDP: proto == protoUDP}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// RestartPanel 见 deploy.Deployer。
func (d *Deployer) RestartPanel(ctx context.Context, r remote.Runner) error {
	return expectOK(ctx, r, restartPanelScript, msgServiceDown)
}

// RestartCore 见 deploy.Deployer。
func (d *Deployer) RestartCore(ctx context.Context, r remote.Runner) error {
	return expectOK(ctx, r, restartCoreScript, msgReloadFailed)
}

// Diagnose 见 deploy.Deployer。
func (d *Deployer) Diagnose(ctx context.Context, r remote.Runner) (string, error) {
	res, err := r.Run(ctx, diagnoseScript)
	if err != nil {
		return "", err
	}
	return deploy.Redact(res.Output), nil
}

// ResetPanelLogin 见 deploy.Deployer。
func (d *Deployer) ResetPanelLogin(ctx context.Context, r remote.Runner) (deploy.PanelLogin, error) {
	first, err := crypto.RandomString(loginLetters, 1)
	if err != nil {
		return deploy.PanelLogin{}, fmt.Errorf("generate panel username: %w", err)
	}
	rest, err := crypto.RandomString(loginAlphabet, loginUsernameLen-1)
	if err != nil {
		return deploy.PanelLogin{}, fmt.Errorf("generate panel username: %w", err)
	}
	password, err := crypto.RandomString(loginAlphabet, loginPasswordLen)
	if err != nil {
		return deploy.PanelLogin{}, fmt.Errorf("generate panel password: %w", err)
	}
	login := deploy.PanelLogin{Username: first + rest, Password: password}
	script := fmt.Sprintf(loginScript, remote.Quote(login.Username), remote.Quote(login.Password))
	if err := expectOK(ctx, r, script, msgLoginFailed); err != nil {
		return deploy.PanelLogin{}, err
	}
	return login, nil
}
