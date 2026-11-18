package xui

import (
	"context"
	_ "embed" // 内嵌 acme.sh 与 DNS 钩子的快照
	"fmt"
	"maps"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"v2-hub-server/internal/manager/deploy"
	"v2-hub-server/internal/manager/remote"
)

// acme.sh 快照，由 `make gen-upstream` 从 contract/upstream/acme.sh/ 拷入（生成物入库，Ruling-74）。
var (
	//go:embed upstream/acme-3.1.6.sh
	acmeScript []byte
	//go:embed upstream/dns_cf-3.1.6.sh
	dnsCFHook []byte
)

// dnsHooks 是内嵌了的 acme.sh DNS 钩子；新增服务商时在这里加一项并补快照。
var dnsHooks = map[string][]byte{"dns_cf": dnsCFHook}

const (
	// acmeExitReused 是 acme.sh 判断证书还没到续期时间、跳过签发时的退出码（它的 RENEW_SKIP）。
	acmeExitReused = 2
	// installExitNoCert / installExitDown 是 installCertScript 的两种可区分的失败；其它（含设不上面板的 4）都算证书配置失败。
	installExitNoCert = 3
	installExitDown   = 5
	// issueSummaryLines 是签发失败时写进错误摘要的 acme.sh 报错行数；完整的末尾输出进任务日志。
	issueSummaryLines = 2
	maxEnvValueLen    = 1024

	msgBadCertRequest = "证书申请参数不合法"
	msgBadDNSHook     = "不支持的 DNS 服务商"
	msgIssueFailed    = "主机上的 acme.sh 申请证书失败"
	msgNoIssuedCert   = "主机上没有这个域名的证书，请先重做“配置证书”"
)

var (
	envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	// acmeLinePrefix 是 acme.sh 每行输出开头的时间戳，摘要里去掉。
	acmeLinePrefix = regexp.MustCompile(`^\[[^]]*\]\s*`)
)

// issueCertScript 在主机上用 acme.sh 签发证书（docs/03 §5.22 “公开证书”，Ruling-74），参数都已转义：
// %[1]s 域名，%[2]s 签发方目录地址，%[3]s 钩子名，%[4]s / %[5]s / %[6]s 已上传的 acme.sh、钩子、凭据文件，
// %[7]s 擦凭据用的 sed 表达式。凭据文件读完即删；无论成败都擦掉 acme.sh 记下的凭据、删掉上传的文件。
// 只回传 acme.sh 以时间戳开头的那些行（不含证书正文），退出码沿用 acme.sh 的：0 签发、2 复用、其它失败。
const issueCertScript = `h=/etc/x-ui/hub-acme
d=%[1]s
server=%[2]s
hook=%[3]s
acme=%[4]s
hookf=%[5]s
envf=%[6]s
scrub() {
  for f in "$h/account.conf" "$h/${d}_ecc/${d}.conf"; do
    if [ -f "$f" ]; then sed -i %[7]s "$f"; fi
  done
}
trap 'rm -f "$envf" "$acme" "$hookf"; scrub' EXIT
umask 077
install -d -m 700 "$h" "$h/dnsapi" || exit 1
install -m 700 "$acme" "$h/acme.sh" || exit 1
install -m 600 "$hookf" "$h/dnsapi/$hook.sh" || exit 1
set -a
. "$envf"
set +a
rm -f "$envf"
force=
conf="$h/${d}_ecc/${d}.conf"
if [ -f "$conf" ] && ! grep -qxF "Le_API='$server'" "$conf"; then force=--force; fi
out=$(mktemp /tmp/hub-acme.XXXXXXXX) || exit 1
"$h/acme.sh" --home "$h" --server "$server" --issue --dns "$hook" -d "$d" --keylength ec-256 $force >"$out" 2>&1
rc=$?
grep '^\[' "$out" | tail -n 30
rm -f "$out"
exit $rc
`

// installCertScript 把 acme.sh 签好的证书装到 /root/cert/<域名>/（与 3x-ui 自带脚本同一位置）并设给面板：%[1]s 域名（已转义）。
// 文件与面板设置都没变就不重启；输出 changed=、renew=（acme.sh 的下次续期时间，Unix 秒）与证书链。
const installCertScript = `set -e
h=/etc/x-ui/hub-acme
d=%[1]s
src="$h/${d}_ecc"
c="/root/cert/$d"
x=/usr/local/x-ui/x-ui
if [ ! -s "$src/fullchain.cer" ] || [ ! -s "$src/$d.key" ]; then exit 3; fi
mkdir -p /root/cert
install -d -m 700 "$c"
changed=0
if ! cmp -s "$src/fullchain.cer" "$c/fullchain.pem" || ! cmp -s "$src/$d.key" "$c/privkey.pem"; then
  install -m 644 "$src/fullchain.cer" "$c/fullchain.pem.new"
  install -m 600 "$src/$d.key" "$c/privkey.pem.new"
  mv -f "$c/fullchain.pem.new" "$c/fullchain.pem"
  mv -f "$c/privkey.pem.new" "$c/privkey.pem"
  changed=1
fi
cur=$("$x" setting -getCert 2>/dev/null | sed -n 's/^cert: *//p')
if [ "$cur" != "$c/fullchain.pem" ]; then
  "$x" cert -webCert "$c/fullchain.pem" -webCertKey "$c/privkey.pem" >/dev/null 2>&1 || true
  cur=$("$x" setting -getCert 2>/dev/null | sed -n 's/^cert: *//p')
  if [ "$cur" != "$c/fullchain.pem" ]; then exit 4; fi
  changed=1
fi
if [ "$changed" = 1 ]; then
  systemctl restart x-ui
  sleep 2
  systemctl is-active --quiet x-ui || exit 5
fi
echo "changed=$changed"
echo "renew=$(sed -n "s/^Le_NextRenewTime='\{0,1\}\([0-9][0-9]*\)'\{0,1\}\$/\1/p" "$src/$d.conf" | head -n 1)"
cat "$c/fullchain.pem"
`

// IssueCert 见 deploy.Deployer。
func (d *Deployer) IssueCert(ctx context.Context, r remote.Runner, req deploy.CertRequest, log deploy.Logger) (bool, error) {
	hook, ok := dnsHooks[req.DNSHook]
	if !ok {
		return false, commandError(msgBadDNSHook)
	}
	envFile, err := certRequestEnv(req)
	if err != nil {
		return false, err
	}
	acmePath, err := r.Upload(ctx, acmeScript)
	if err != nil {
		return false, err
	}
	hookPath, err := r.Upload(ctx, hook)
	if err != nil {
		return false, err
	}
	// 凭据最后传、传完立刻执行：脚本一开始就挂上删除它的 trap
	envPath, err := r.Upload(ctx, envFile)
	if err != nil {
		return false, err
	}
	script := fmt.Sprintf(issueCertScript, remote.Quote(req.Domain), remote.Quote(req.DirectoryURL), remote.Quote(req.DNSHook),
		remote.Quote(acmePath), remote.Quote(hookPath), remote.Quote(envPath), remote.Quote(scrubExpression(req.Env)))
	res, err := r.Run(ctx, script)
	if err != nil {
		return false, err
	}
	lines := acmeLines(res.Output)
	for _, line := range lines {
		log(line)
	}
	switch res.ExitCode {
	case 0:
		return false, nil
	case acmeExitReused:
		return true, nil
	default:
		return false, deploy.NewError(deploy.ReasonCertFailed, issueFailure(lines))
	}
}

// certRequestEnv 校验签发参数并生成凭据文件的内容（每行 NAME='value'）。值只出现在这个文件里，不进命令行。
func certRequestEnv(req deploy.CertRequest) ([]byte, error) {
	if !isCertDomain(req.Domain) || !isDirectoryURL(req.DirectoryURL) || len(req.Env) == 0 {
		return nil, commandError(msgBadCertRequest)
	}
	var b strings.Builder
	for _, name := range slices.Sorted(maps.Keys(req.Env)) {
		value := req.Env[name]
		if !envNamePattern.MatchString(name) || value == "" || len(value) > maxEnvValueLen || strings.ContainsAny(value, "\x00\n") {
			return nil, commandError(msgBadCertRequest)
		}
		b.WriteString(name + "=" + remote.Quote(value) + "\n")
	}
	return []byte(b.String()), nil
}

// scrubExpression 是删掉 acme.sh 记下的这些凭据的 sed 表达式：它把凭据存成 SAVED_<名> 或 <名>（变量名已校验）。
func scrubExpression(env map[string]string) string {
	parts := make([]string, 0, 2*len(env))
	for _, name := range slices.Sorted(maps.Keys(env)) {
		parts = append(parts, "/^SAVED_"+name+"=/d", "/^"+name+"=/d")
	}
	return strings.Join(parts, ";")
}

// isCertDomain 只接受小写的完整域名（至少两段），不接受 IP 与通配符。
func isCertDomain(domain string) bool {
	if _, err := netip.ParseAddr(domain); err == nil {
		return false
	}
	return domain == strings.ToLower(domain) && strings.Contains(domain, ".") && hostnamePattern.MatchString(domain)
}

func isDirectoryURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && !strings.ContainsAny(raw, `'"`)
}

// acmeLines 去掉每行开头的时间戳与空行。
func acmeLines(output string) []string {
	raw := strings.Split(output, "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(acmeLinePrefix.ReplaceAllString(line, ""))
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// issueFailure 取 acme.sh 最后几行报错作摘要；没有输出时给通用说明。
func issueFailure(lines []string) string {
	if len(lines) == 0 {
		return msgIssueFailed
	}
	tail := lines[max(0, len(lines)-issueSummaryLines):]
	return msgIssueFailed + "：" + strings.Join(tail, "；")
}

// InstallCert 见 deploy.Deployer。
func (d *Deployer) InstallCert(ctx context.Context, r remote.Runner, domain string) (deploy.InstalledCert, error) {
	if !isCertDomain(domain) {
		return deploy.InstalledCert{}, commandError(msgBadCertRequest)
	}
	res, err := r.Run(ctx, fmt.Sprintf(installCertScript, remote.Quote(domain)))
	if err != nil {
		return deploy.InstalledCert{}, err
	}
	switch res.ExitCode {
	case 0:
	case installExitNoCert:
		return deploy.InstalledCert{}, deploy.NewError(deploy.ReasonCertFailed, msgNoIssuedCert)
	case installExitDown:
		return deploy.InstalledCert{}, commandError(msgServiceDown)
	default:
		return deploy.InstalledCert{}, commandError(msgTLSFailed)
	}
	return parseInstalled(res.Output)
}

// parseInstalled 解析 installCertScript 的输出：证书链之前是 key=value 行。
func parseInstalled(output string) (deploy.InstalledCert, error) {
	head, chain, found := strings.Cut(output, "-----BEGIN")
	if !found {
		return deploy.InstalledCert{}, commandError(msgBadCert)
	}
	leaf, err := parseLeaf("-----BEGIN" + chain)
	if err != nil {
		return deploy.InstalledCert{}, commandError(msgBadCert)
	}
	kv := parseKV(head)
	out := deploy.InstalledCert{NotAfter: leaf.NotAfter, LeafDER: leaf.Raw, Changed: kv["changed"] == flagOn}
	if secs, err := strconv.ParseInt(kv["renew"], 10, 64); err == nil && secs > 0 {
		out.RenewAt = time.Unix(secs, 0)
	}
	return out, nil
}
