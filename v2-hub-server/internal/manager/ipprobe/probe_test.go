package ipprobe

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"v2-hub-server/internal/manager/remote"
	"v2-hub-server/internal/manager/remote/remotetest"
)

const testNonce = "abc123def456"

// shellRunner 在本机用 sh 执行脚本，用来验证脚本本身能跑通（需要本机有 curl）。
type shellRunner struct{ remotetest.Runner }

func (s *shellRunner) Run(ctx context.Context, script string) (remote.Result, error) {
	out, err := exec.CommandContext(ctx, "sh", "-c", script).CombinedOutput()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		return remote.Result{}, err
	}
	return remote.Result{Output: string(out)}, nil
}

// fakeResolver 按表返回 MX 与解析结果。
type fakeResolver struct {
	mx    map[string]string
	hosts map[string]bool
}

func (f fakeResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	if f.hosts[host] {
		return []string{"203.0.113.9"}, nil
	}
	return nil, &net.DNSError{Name: host, Err: "no such host", IsNotFound: true}
}

func (f fakeResolver) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if h, ok := f.mx[name]; ok {
		return []*net.MX{{Host: "backup." + h + ".", Pref: 20}, {Host: h + ".", Pref: 10}}, nil
	}
	return nil, &net.DNSError{Name: name, Err: "no such host", IsNotFound: true}
}

func TestNewRejectsBadOverrides(t *testing.T) {
	for _, ov := range []Overrides{{BaseURL: "ftp://x"}, {BaseURL: "http://"}, {MailAddr: "nope"}, {MailAddr: "a.b:0"}, {MailAddr: "a;b.c:25"}} {
		if _, err := New(nil, ov); !errors.Is(err, ErrBadOverrides) {
			t.Errorf("New(%+v) err = %v, want ErrBadOverrides", ov, err)
		}
	}
}

func TestRenderQuotesAndTargets(t *testing.T) {
	p, err := New(fakeResolver{}, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	targets := []MailTarget{{Key: "gmail", MX: "gmail-smtp-in.l.google.com", Port: smtpPort}, {Key: "qq"}}
	script := p.render(targets, testNonce)
	for _, want := range []string{
		"f nfo 'https://www.netflix.com/title/81280792' '-L'",
		"m nfo oh 'Oh no!'",
		"s mxgmail 'gmail-smtp-in.l.google.com' 25 &",
		"r netflix 'http://hubprobe-" + testNonce + ".netflix.com/' &",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q", want)
		}
	}
	if strings.Contains(script, "mxqq") {
		t.Error("target without MX must not be probed")
	}
	ov, _ := New(nil, Overrides{BaseURL: "http://127.0.0.1:9/"})
	script = ov.render(nil, testNonce)
	if strings.Contains(script, "hubprobe-") || !strings.Contains(script, "f nfo 'http://127.0.0.1:9/nfo'") {
		t.Errorf("override script wrong:\n%s", script)
	}
}

func TestRunRejectsBadNonce(t *testing.T) {
	p, _ := New(nil, Overrides{})
	if _, err := p.Run(context.Background(), &remotetest.Runner{}, "BAD;rm"); err == nil {
		t.Fatal("bad nonce accepted")
	}
}

func TestRunNoCurl(t *testing.T) {
	p, _ := New(nil, Overrides{MailAddr: "127.0.0.1:2525"})
	r := &remotetest.Runner{Handle: func(string) (remote.Result, error) { return remote.Result{Output: "tool.curl=0\n"}, nil }}
	if _, err := p.Run(context.Background(), r, testNonce); !errors.Is(err, ErrNoCurl) {
		t.Fatalf("err = %v, want ErrNoCurl", err)
	}
}

func TestParseOutputOnlyDeclaredKeys(t *testing.T) {
	allowed := allowedKeys(nil)
	kv := parseOutput("tool.curl=1\nevil.key=1\nnfo.exit=0\nnfo.exit=7\nnfo.w="+strings.Repeat("x", 900)+"\nnot a line\n", allowed)
	if kv["evil.key"] != "" || kv["nfo.exit"] != "0" || len(kv["nfo.w"]) != maxValueLen || kv[keyToolCurl] != flagOn {
		t.Fatalf("kv = %v", kv)
	}
}

func TestRunParsesFakeOutput(t *testing.T) {
	p, _ := New(fakeResolver{
		mx:    map[string]string{"gmail.com": "mx.gmail.example", "qq.com": "mx.qq.example"},
		hosts: map[string]bool{},
	}, Overrides{})
	output := strings.Join([]string{
		"tool.curl=1", "tool.telnet=1", "tool.bash=1",
		"eg.exit=0", "eg.w=200 104.16.0.1 https://www.cloudflare.com/cdn-cgi/trace", "eg.g.ip=ip=198.51.100.7", "eg.g.loc=loc=JP",
		"nfo.exit=0", "nfo.w=200 10.0.0.5 https://www.netflix.com/jp/title/81280792", "nfo.m.oh=0", "nfo.g.rc=",
		"nfl.exit=0", "nfl.w=200 10.0.0.5 https://www.netflix.com/jp/title/70143836", "nfl.m.oh=0",
		"yt.exit=0", "yt.w=200 142.250.0.1 https://www.youtube.com/premium", "yt.m.cn=0", "yt.m.na=0", "yt.m.af=3", `yt.g.gl="INNERTUBE_CONTEXT_GL":"JP"`,
		"rd.youtube_premium=0",
		"mxgmail.exit=0", "mxgmail.code=220",
		"mxqq.exit=28", "mxqq.code=",
	}, "\n")
	r := &remotetest.Runner{Handle: func(string) (remote.Result, error) { return remote.Result{Output: output}, nil }}
	out, err := p.Run(context.Background(), r, testNonce)
	if err != nil {
		t.Fatal(err)
	}
	if out.Egress.IP.String() != "198.51.100.7" || out.Egress.Country != "JP" {
		t.Errorf("egress = %+v", out.Egress)
	}
	media := map[string]MediaResult{}
	for _, m := range out.Media {
		media[m.Key] = m
	}
	if m := media[MediaNetflix]; m.Status != StatusYes || m.Region != "JP" || m.Method != MethodDNS {
		t.Errorf("netflix = %+v", m)
	}
	// 随机子域名在主机上能连（退出码不是 6）而平台解析不到 → DNS 解锁
	if m := media[MediaYouTubePremium]; m.Status != StatusYes || m.Region != "JP" || m.Method != MethodDNS {
		t.Errorf("youtube = %+v", m)
	}
	if m := media[MediaReddit]; m.Status != StatusFailed || m.Method != MethodUnknown {
		t.Errorf("reddit = %+v", m)
	}
	mail := map[string]MailResult{}
	for _, m := range out.Mail {
		mail[m.Key] = m
	}
	if mail["gmail"].Result != MailOK || mail["gmail"].MX != "mx.gmail.example" || mail["qq"].Result != MailTimeout || mail["sina"].Result != MailDNSFailed {
		t.Errorf("mail = %+v", mail)
	}
	if got := Port25(out.Mail); got != Port25Open {
		t.Errorf("Port25 = %s", got)
	}
	if len(out.Media) != len(mediaServices) || len(out.Mail) != len(mailProviders) || out.Media[0].Key != MediaChatGPT {
		t.Errorf("results not complete / sorted: %d %d %s", len(out.Media), len(out.Mail), out.Media[0].Key)
	}
}

// TestScriptRunsInShell 在本机真跑一次脚本：HTTP 探测指向本地模拟端，邮局指向本地的假 SMTP。
func TestScriptRunsInShell(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	pages := map[string]string{
		"/eg":  "fl=1\nip=198.51.100.7\nloc=SG\n",
		"/nfo": `<html>{"requestCountry":{"id":"SG","x":1}}</html>`,
		"/nfl": "<html>Oh no!\nOh no! again</html>",
		"/yt":  `<html>"INNERTUBE_CONTEXT_GL":"SG" ad-free</html>`,
		"/pv":  `{"currentTerritory":"SG"}`,
		"/tt":  `{"region":"SG"}`,
		"/cgw": `{"cookie_requirements":"ok"}`,
		"/cga": `<html>ok</html>`,
		"/cgt": "ip=198.51.100.7\nloc=SG\n",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rdt" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		body, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	smtp := fakeSMTP(t)
	p, err := New(nil, Overrides{BaseURL: srv.URL, MailAddr: smtp})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := p.Run(ctx, &shellRunner{}, testNonce)
	if err != nil {
		t.Fatal(err)
	}
	if out.Egress.IP.String() != "198.51.100.7" || out.Egress.Country != "SG" {
		t.Errorf("egress = %+v", out.Egress)
	}
	want := map[string]string{
		MediaNetflix: StatusOriginalsOnly, MediaYouTubePremium: StatusYes, MediaPrimeVideo: StatusYes,
		MediaTikTok: StatusYes, MediaReddit: StatusNo, MediaChatGPT: StatusYes,
	}
	for _, m := range out.Media {
		if m.Status != want[m.Key] {
			t.Errorf("%s status = %s (evidence %v), want %s", m.Key, m.Status, m.Evidence, want[m.Key])
		}
		if usable(m.Status) && (m.Region != "SG" || m.Method != MethodDNS) {
			// 模拟端在回环地址上，按“连到了内网地址”判为 DNS 解锁
			t.Errorf("%s region/method = %s/%s", m.Key, m.Region, m.Method)
		}
	}
	for _, m := range out.Mail {
		if m.Result != MailOK && m.Result != MailNoTool {
			t.Errorf("mail %s = %+v", m.Key, m)
		}
	}
}

// fakeSMTP 起一个只发欢迎行、收到 QUIT 就断开的 SMTP 端，返回 host:port。
func fakeSMTP(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() }) // 测试结束关闭监听，错误无关紧要
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()                    // 测试用连接，关闭错误无关紧要
				_ = c.SetDeadline(time.Now().Add(10 * time.Second)) // 设置失败只影响超时，不影响用例
				if _, err := fmt.Fprint(c, "220 fake ESMTP\r\n"); err != nil {
					return
				}
				line, _ := bufio.NewReader(c).ReadString('\n') // 读不到就直接断开
				if strings.HasPrefix(strings.ToUpper(line), "QUIT") {
					_, _ = fmt.Fprint(c, "221 bye\r\n") // 对端可能已断开
				}
			}(c)
		}
	}()
	return ln.Addr().String()
}
