package ipprobe

import (
	"strconv"
	"strings"

	"v2-hub-server/internal/manager/remote"
)

// uaPlaceholder 是脚本里 User-Agent 的占位符。
const uaPlaceholder = "{{ua}}"

// scriptHead 是探测脚本的公共部分（POSIX sh，只依赖 curl、grep、head、cut、tr、mktemp）：
// 页面正文写进临时目录、结束即删，回传的只有状态码、对端 IP、最终地址、特征串命中数与 ≤ 200 字节的片段。
// User-Agent 由 uaPlaceholder 处替换（已转义）。
const scriptHead = `command -v curl >/dev/null 2>&1 || { echo tool.curl=0; exit 0; }
echo tool.curl=1
d=$(mktemp -d /tmp/hub-ipq.XXXXXXXX) || exit 1
trap 'rm -rf "$d"' EXIT INT TERM
ua=` + uaPlaceholder + `
f() { k=$1; u=$2; shift 2
  w=$(curl -4 -sS -m 10 --connect-timeout 5 --max-redirs 5 --max-filesize 8388608 -A "$ua" -o "$d/$k.b" -w '%{http_code} %{remote_ip} %{url_effective}' "$@" "$u" 2>/dev/null); x=$?
  printf '%s.exit=%s\n%s.w=%s\n' "$k" "$x" "$k" "$(printf '%s' "$w" | tr -d '\r\n' | cut -c1-400)" > "$d/$k.o"
}
m() { printf '%s.m.%s=%s\n' "$1" "$2" "$(grep -acF -e "$3" "$d/$1.b" 2>/dev/null)" >> "$d/$1.o"; }
g() { printf '%s.g.%s=%s\n' "$1" "$2" "$(grep -aoE -m1 -e "$3" "$d/$1.b" 2>/dev/null | head -n1 | tr -d '\r\n' | cut -c1-200)" >> "$d/$1.o"; }
r() { curl -4 -s -m 5 --connect-timeout 3 -o /dev/null "$2" 2>/dev/null; echo "rd.$1=$?" > "$d/rd-$1.o"; }
tel=0; curl -V 2>/dev/null | grep -qw telnet && tel=1
bt=0; command -v bash >/dev/null 2>&1 && command -v timeout >/dev/null 2>&1 && bt=1
echo "tool.telnet=$tel"; echo "tool.bash=$bt"
s() { k=$1; h=$2; p=$3
  if [ "$tel" = 1 ]; then
    ( sleep 4; printf 'QUIT\r\n' ) | curl -s -m 8 --connect-timeout 5 -o "$d/$k.s" "telnet://$h:$p" 2>/dev/null; x=$?
  elif [ "$bt" = 1 ]; then
    timeout 8 bash -c 'exec 3<>"/dev/tcp/$0/$1" || exit 7; head -c 3 <&3' "$h" "$p" > "$d/$k.s" 2>/dev/null; x=$?
  else
    x=` + noToolMark + `
  fi
  printf '%s.exit=%s\n%s.code=%s\n' "$k" "$x" "$k" "$(head -c 3 "$d/$k.s" 2>/dev/null | tr -cd '0-9')" > "$d/$k.o"
}
`

// scriptTail 等所有后台探测结束后汇总输出。
const scriptTail = `wait
cat "$d"/*.o 2>/dev/null
`

// render 由探测表生成完整脚本。每个服务一个后台子进程，服务内的请求按顺序执行；邮局各家并行。
func (p *Prober) render(targets []MailTarget, nonce string) string {
	var b strings.Builder
	b.WriteString(sprintfHead())
	writeGroup(&b, p, egressFetches)
	for _, svc := range mediaServices {
		writeGroup(&b, p, svc.Fetches)
		if p.baseURL == "" {
			b.WriteString("r " + svc.Key + " " + remote.Quote("http://"+randomHost(nonce, svc)+"/") + " &\n")
		}
	}
	for _, t := range targets {
		if t.MX == "" {
			continue
		}
		b.WriteString("s " + mailKey(t.Key) + " " + remote.Quote(t.MX) + " " + strconv.Itoa(t.Port) + " &\n")
	}
	b.WriteString(scriptTail)
	return b.String()
}

func sprintfHead() string {
	return strings.Replace(scriptHead, uaPlaceholder, remote.Quote(userAgent), 1)
}

// writeGroup 把一组按顺序执行的请求写成一个后台子进程。
func writeGroup(b *strings.Builder, p *Prober, fetches []fetch) {
	b.WriteString("(\n")
	for _, f := range fetches {
		b.WriteString("  f " + f.Key + " " + remote.Quote(p.urlOf(f)))
		for _, a := range f.Args {
			b.WriteString(" " + remote.Quote(a))
		}
		b.WriteString("\n")
		for _, m := range f.Marks {
			b.WriteString("  m " + f.Key + " " + m.Name + " " + remote.Quote(m.Text) + "\n")
		}
		for _, g := range f.Grabs {
			b.WriteString("  g " + f.Key + " " + g.Name + " " + remote.Quote(g.Pattern) + "\n")
		}
	}
	b.WriteString(") &\n")
}
