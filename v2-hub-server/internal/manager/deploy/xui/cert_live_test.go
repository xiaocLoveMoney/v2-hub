package xui

import (
	"crypto/tls"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"v2-hub-server/internal/manager/deploy"
	"v2-hub-server/internal/manager/remote"
)

// 真实主机上的证书签发（docs/03 §5.22 “公开证书”，Ruling-74）。默认跳过；设齐下面的环境变量才跑：
//
//	HUB_TEST_SSH_ADDR / HUB_TEST_SSH_PORT / HUB_TEST_SSH_KEY_FILE  root 登录的主机与私钥文件
//	HUB_TEST_CERT_DOMAIN  已托管在 Cloudflare 的域名（这里不改解析，须已指向主机）
//	HUB_TEST_CF_TOKEN     能编辑该域名 DNS 的 Cloudflare 令牌
//	HUB_TEST_ACME_URL     签发方目录地址，默认 Let's Encrypt 测试环境
//
// 主机上没有面板时先按平台的做法装一个；跑完面板留在主机上。
const (
	liveACMEStaging   = "https://acme-staging-v02.api.letsencrypt.org/directory"
	liveDialTimeout   = 15 * time.Second
	liveHandshakeWait = 15 * time.Second
	defaultSSHPort    = 22
)

func liveEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("%s not set; skipping the live certificate test", name)
	}
	return v
}

func TestLiveIssueAndInstallCert(t *testing.T) {
	addr, keyFile := liveEnv(t, "HUB_TEST_SSH_ADDR"), liveEnv(t, "HUB_TEST_SSH_KEY_FILE")
	domain, cfValue := liveEnv(t, "HUB_TEST_CERT_DOMAIN"), liveEnv(t, "HUB_TEST_CF_TOKEN")
	directory := os.Getenv("HUB_TEST_ACME_URL")
	if directory == "" {
		directory = liveACMEStaging
	}
	port := defaultSSHPort
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
	r, err := remote.NewSSHDialer(remote.Config{ConnectTimeout: liveDialTimeout}).Dial(ctx, remote.Target{Address: addr, Port: port, Username: "root", PrivateKey: key})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	}()
	logf := func(line string) { t.Log(line) }
	d := New()

	det, err := d.Detect(ctx, r)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	provisioned, err := d.Provision(ctx, r, det, deploy.ProvisionOptions{Address: addr}, logf)
	if err != nil {
		t.Fatalf("provision (%s): %v", det.Plan, err)
	}
	panelPort := provisioned.Access.Port
	t.Logf("panel plan %s, port %d", det.Plan, panelPort)

	req := deploy.CertRequest{Domain: domain, DirectoryURL: directory, DNSHook: "dns_cf", Env: map[string]string{"CF_Token": cfValue}}
	reused, err := d.IssueCert(ctx, r, req, logf)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	t.Logf("first issue reused=%v", reused)
	installed, err := d.InstallCert(ctx, r, domain)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	t.Logf("installed: changed=%v notAfter=%s renewAt=%s", installed.Changed, installed.NotAfter, installed.RenewAt)
	if installed.RenewAt.IsZero() || !installed.RenewAt.Before(installed.NotAfter) {
		t.Fatalf("renew time from acme.sh = %s", installed.RenewAt)
	}

	// 第二次：acme.sh 复用、面板不重启
	if reused, err = d.IssueCert(ctx, r, req, logf); err != nil || !reused {
		t.Fatalf("second issue must reuse: reused %v, err %v", reused, err)
	}
	again, err := d.InstallCert(ctx, r, domain)
	if err != nil || again.Changed || !again.NotAfter.Equal(installed.NotAfter) {
		t.Fatalf("second install must change nothing: %+v, %v", again, err)
	}

	// 面板对外出示的就是这张证书（测试环境的证书不受信任，只比对叶子）
	dialer := &net.Dialer{Timeout: liveHandshakeWait}
	conn, err := tls.DialWithDialer(dialer, "tcp", net.JoinHostPort(domain, strconv.Itoa(panelPort)), &tls.Config{
		ServerName: domain, InsecureSkipVerify: directory == liveACMEStaging, //nolint:gosec // 测试环境的证书链不受信任，只比对叶子
	})
	if err != nil {
		t.Fatalf("tls dial panel: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close tls: %v", err)
		}
	}()
	leaf := conn.ConnectionState().PeerCertificates[0]
	if string(leaf.Raw) != string(installed.LeafDER) {
		t.Fatalf("panel serves another certificate: %s issued by %s", leaf.Subject, leaf.Issuer)
	}
	t.Logf("panel serves %s issued by %s, valid until %s", leaf.Subject.CommonName, leaf.Issuer.CommonName, leaf.NotAfter)
}
