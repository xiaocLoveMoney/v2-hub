package ipprobe

import (
	"context"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"v2-hub-server/internal/manager/remote"
)

// 真实主机上的服务器侧探测（docs/03 §5.23，核对流媒体特征串用）。默认跳过；设齐环境变量才跑：
//
//	HUB_TEST_SSH_ADDR / HUB_TEST_SSH_PORT / HUB_TEST_SSH_KEY_FILE  root 登录的主机与私钥文件
//
// 只打印结论与证据，不断言取值（各站点的结果随主机而变），人工与在该机上手工 curl 的结果对照。
const (
	liveDialTimeout  = 15 * time.Second
	liveProbeTimeout = 90 * time.Second
	liveSSHPort      = 22
)

func TestLiveProbe(t *testing.T) {
	addr, keyFile := os.Getenv("HUB_TEST_SSH_ADDR"), os.Getenv("HUB_TEST_SSH_KEY_FILE")
	if addr == "" || keyFile == "" {
		t.Skip("HUB_TEST_SSH_ADDR / HUB_TEST_SSH_KEY_FILE not set; skipping the live probe test")
	}
	port := liveSSHPort
	if p := os.Getenv("HUB_TEST_SSH_PORT"); p != "" {
		var err error
		if port, err = strconv.Atoi(p); err != nil {
			t.Fatalf("HUB_TEST_SSH_PORT: %v", err)
		}
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveProbeTimeout)
	defer cancel()
	r, err := remote.NewSSHDialer(remote.Config{ConnectTimeout: liveDialTimeout}).Dial(ctx, remote.Target{Address: addr, Port: port, Username: "root", PrivateKey: key})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	}()
	p, err := New(net.DefaultResolver, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.Run(ctx, r, "livetest0001")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	t.Logf("egress %s %s", out.Egress.IP, out.Egress.Country)
	for _, m := range out.Media {
		t.Logf("media %-16s %-15s %-2s %-7s %v", m.Key, m.Status, m.Region, m.Method, m.Evidence)
	}
	for _, m := range out.Mail {
		t.Logf("mail %-10s %-9s %s %s", m.Key, m.Result, m.Code, m.MX)
	}
	t.Logf("port25 %s", Port25(out.Mail))
}
