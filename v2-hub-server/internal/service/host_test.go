package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/deploy"
	"v2-hub-server/internal/manager/deploy/deploytest"
	"v2-hub-server/internal/manager/dns"
	"v2-hub-server/internal/manager/dns/dnstest"
	"v2-hub-server/internal/manager/panel"
	"v2-hub-server/internal/manager/remote"
	"v2-hub-server/internal/manager/remote/remotetest"
)

const (
	hostAddrA      = "203.0.113.10"
	hostAddrB      = "203.0.113.11"
	hostKeyFP      = "SHA256:hostkeyhostkeyhostkeyhostkeyhostkeyhostkey0"
	issuedToken    = "issuedTokenValue0123456789abcdefghijklmnopqrstuvw"
	testCertPin    = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	testPanelPort  = 2053
	testBasePath   = "/abc/"
	urlHTTPHostA   = "http://203.0.113.10:2053/abc"
	urlHTTPSHostA  = "https://203.0.113.10:2053/abc"
	hostTestLimit  = 3
	hostActionsMax = 100
)

type hostFixture struct {
	*panelFixture
	hosts    *HostService
	dialer   *remotetest.Dialer
	runner   *remotetest.Runner
	deployer *deploytest.Fake
	dnsFake  *dnstest.Fake
	credID   int64
}

func testPrivateKey(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return string(pem.EncodeToMemory(block))
}

// newHostFixture 搭好假拨号器、假部署实现与一份私钥凭据；默认主机上已有标准安装的明文 HTTP 面板。
func newHostFixture(t *testing.T) *hostFixture {
	t.Helper()
	pf := newPanelFixture(t)
	runner := &remotetest.Runner{Privileged: true, HostKey: hostKeyFP}
	dialer := &remotetest.Dialer{Runner: runner}
	deployer := &deploytest.Fake{
		Detection:   deploy.Detection{OSID: "debian", Arch: "x86_64", IsPrivileged: true, PanelState: deploy.PanelStandard, PanelVersion: "3.11.0", Plan: deploy.PlanAdopt},
		Provisioned: deploy.Provisioned{Access: deploy.PanelAccess{Port: testPanelPort, BasePath: testBasePath}, Token: issuedToken},
		Token:       issuedToken, CertPin: testCertPin, Errors: map[string]error{},
		Login: deploy.PanelLogin{Username: "newuser", Password: "newpassword1234567890"},
	}
	deployer.Installed = testInstalledCert(pf.clock.now())
	dnsFake := dnstest.New()
	dnsFake.Hook = dns.ACMEHook{Name: testDNSHook, Env: map[string]string{testDNSEnv: testDNSValue}}
	hosts := NewHostService(pf.pool, testSealer(t), HostDeps{
		Dialer: dialer, Deployers: deploy.NewRegistry(map[string]deploy.Deployer{fakeType: deployer}),
		DNS: dns.NewRegistry(map[string]dns.Factory{fakeDNSType: dnsFake.Factory()}), Panels: pf.panels,
	}, HostConfig{
		CommandTimeout: time.Minute, InstallTimeout: time.Minute, CertTimeout: time.Minute, ACMEDirectoryURL: fakeACMEURL,
		TestLimit: hostTestLimit, TestWindow: time.Minute,
		ActionLimit: hostActionsMax, ActionWindow: time.Minute, MaxBatch: 3, PanelWaitAttempts: 2, PanelWaitInterval: time.Millisecond, Now: pf.clock.now,
	})
	f := &hostFixture{panelFixture: pf, hosts: hosts, dialer: dialer, runner: runner, deployer: deployer, dnsFake: dnsFake}
	cred, err := hosts.CreateCredential(context.Background(), pf.actor, SSHCredentialInput{Name: "root-key", Username: "root", AuthType: SSHAuthKey, Secret: testPrivateKey(t)}, nil)
	if err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}
	f.credID = cred.ID
	return f
}

func (f *hostFixture) createHost(t *testing.T, address, name string) ServerHost {
	t.Helper()
	list, err := f.hosts.CreateHosts(context.Background(), f.actor, HostBatchInput{
		CredentialID: f.credID, ProviderType: fakeType, Hosts: []HostBatchItem{{Address: address, Name: name}},
	}, nil)
	if err != nil {
		t.Fatalf("CreateHosts: %v", err)
	}
	return list[0]
}

// runNext 领取并执行一条排队的任务，返回它的最终状态。
func (f *hostFixture) runNext(t *testing.T) *HostTaskDetail {
	t.Helper()
	ctx := context.Background()
	ids, err := f.hosts.ClaimTasks(ctx, 1)
	if err != nil || len(ids) != 1 {
		t.Fatalf("ClaimTasks = %v, err = %v", ids, err)
	}
	f.hosts.RunTask(ctx, ids[0])
	task, err := f.hosts.GetTask(ctx, ids[0])
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	return task
}

func (f *hostFixture) action(t *testing.T, hostID int64, kind string) *HostTaskDetail {
	t.Helper()
	if _, err := f.hosts.CreateAction(context.Background(), f.actor, hostID, kind, nil); err != nil {
		t.Fatalf("CreateAction(%s): %v", kind, err)
	}
	return f.runNext(t)
}

func wantCode(t *testing.T, err error, code Code) {
	t.Helper()
	var se *Error
	if !errors.As(err, &se) || se.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
}

func wantTask(t *testing.T, task *HostTaskDetail, status string, reason deploy.Reason) {
	t.Helper()
	if task.Status != status || task.ErrorCode != string(reason) {
		t.Fatalf("task status=%s code=%q lastError=%q, want %s / %q\nlog:\n%s", task.Status, task.ErrorCode, task.LastError, status, reason, task.Log)
	}
}

func TestCredentialSecretIsSealedAndWriteOnly(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	keyPEM := testPrivateKey(t)

	cred, err := f.hosts.CreateCredential(ctx, f.actor, SSHCredentialInput{Name: "k2", Username: "deploy", AuthType: SSHAuthKey, Secret: keyPEM, Remark: "测试"}, nil)
	if err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}
	if !strings.HasPrefix(cred.KeyFingerprint, "SHA256:") {
		t.Fatalf("fingerprint = %q", cred.KeyFingerprint)
	}
	row, err := f.hosts.q.GetSSHCredentialSecret(ctx, cred.ID)
	if err != nil {
		t.Fatalf("GetSSHCredentialSecret: %v", err)
	}
	if bytes.Contains(row.SecretEnc, []byte("PRIVATE KEY")) {
		t.Fatal("private key must not be stored in plaintext")
	}
	opened, err := f.hosts.openCredential(ctx, cred.ID)
	if err != nil || string(opened.secret) != keyPEM {
		t.Fatalf("sealed key does not round-trip: %v", err)
	}

	// 留空 = 沿用原值；改名不动密文
	updated, err := f.hosts.UpdateCredential(ctx, f.actor, cred.ID, SSHCredentialInput{Name: "k2-renamed", Username: "deploy", AuthType: SSHAuthKey}, nil)
	if err != nil || updated.Name != "k2-renamed" || updated.KeyFingerprint != cred.KeyFingerprint {
		t.Fatalf("update = %+v, err = %v", updated, err)
	}
	if opened, err = f.hosts.openCredential(ctx, cred.ID); err != nil || string(opened.secret) != keyPEM {
		t.Fatalf("secret changed by a rename: %v", err)
	}
	// 改认证方式必须重新给密文
	_, err = f.hosts.UpdateCredential(ctx, f.actor, cred.ID, SSHCredentialInput{Name: "k2-renamed", Username: "deploy", AuthType: SSHAuthPassword}, nil)
	wantCode(t, err, CodeInvalidArgument)
	updated, err = f.hosts.UpdateCredential(ctx, f.actor, cred.ID, SSHCredentialInput{Name: "k2-renamed", Username: "deploy", AuthType: SSHAuthPassword, Secret: "pw-123456"}, nil)
	if err != nil || updated.AuthType != SSHAuthPassword || updated.KeyFingerprint != "" {
		t.Fatalf("switch to password = %+v, err = %v", updated, err)
	}

	logs, err := f.rbac.ListAuditLogs(ctx, PageQuery{PageSize: 100})
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	for _, l := range logs.Items {
		if bytes.Contains(l.Detail, []byte("PRIVATE KEY")) || bytes.Contains(l.Detail, []byte("pw-123456")) {
			t.Fatalf("audit %s leaks the secret: %s", l.Action, l.Detail)
		}
	}
}

func TestCredentialValidation(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	key := testPrivateKey(t)
	tests := []struct {
		name string
		in   SSHCredentialInput
		code Code
	}{
		{"garbage key", SSHCredentialInput{Name: "a", Username: "root", AuthType: SSHAuthKey, Secret: "not a key"}, CodeInvalidArgument},
		{"empty secret", SSHCredentialInput{Name: "a", Username: "root", AuthType: SSHAuthKey}, CodeInvalidArgument},
		{"bad username", SSHCredentialInput{Name: "a", Username: "root; rm -rf /", AuthType: SSHAuthKey, Secret: key}, CodeInvalidArgument},
		{"bad auth type", SSHCredentialInput{Name: "a", Username: "root", AuthType: "agent", Secret: key}, CodeInvalidArgument},
		{"password with newline", SSHCredentialInput{Name: "a", Username: "root", AuthType: SSHAuthPassword, Secret: "a\nb"}, CodeInvalidArgument},
		{"duplicate name", SSHCredentialInput{Name: "root-key", Username: "root", AuthType: SSHAuthKey, Secret: key}, CodeConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.hosts.CreateCredential(ctx, f.actor, tt.in, nil)
			wantCode(t, err, tt.code)
		})
	}

	f.createHost(t, hostAddrA, "node-a")
	wantCode(t, f.hosts.DeleteCredential(ctx, f.actor, f.credID, nil), CodeConflict)
}

func TestNormalizeHostAddress(t *testing.T) {
	tests := []struct {
		in            string
		allowLoopback bool
		want          string
		isRejected    bool
	}{
		{in: " 203.0.113.10 ", want: "203.0.113.10"},
		{in: "Panel.Example.COM", want: "panel.example.com"},
		{in: "10.0.0.5", want: "10.0.0.5"},
		{in: "2001:DB8::1", want: "2001:db8::1"},
		{in: "::ffff:203.0.113.10", want: "203.0.113.10"},
		{in: "127.0.0.1", isRejected: true},
		{in: "::1", isRejected: true},
		{in: "localhost", isRejected: true},
		{in: "0.0.0.0", isRejected: true},
		{in: "127.0.0.1", allowLoopback: true, want: "127.0.0.1"},
		{in: "localhost", allowLoopback: true, want: "localhost"},
		{in: "", isRejected: true},
		{in: "a b", isRejected: true},
		{in: "host;reboot", isRejected: true},
		{in: "-bad.example.com", isRejected: true},
	}
	for _, tt := range tests {
		got, err := normalizeHostAddress(tt.in, tt.allowLoopback)
		if tt.isRejected != (err != nil) || got != tt.want {
			t.Errorf("normalizeHostAddress(%q, %v) = %q, %v", tt.in, tt.allowLoopback, got, err)
		}
	}
}

func TestCreateHostsBatch(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	batch := func(items ...HostBatchItem) HostBatchInput {
		return HostBatchInput{CredentialID: f.credID, ProviderType: fakeType, Hosts: items}
	}

	_, err := f.hosts.CreateHosts(ctx, f.actor, batch(HostBatchItem{Address: hostAddrA, Name: "a"}, HostBatchItem{Address: "127.0.0.1", Name: "b"}), nil)
	wantCode(t, err, CodeInvalidArgument)
	if !strings.Contains(err.Error(), "第 2 行") {
		t.Fatalf("error should name the bad row: %v", err)
	}
	_, err = f.hosts.CreateHosts(ctx, f.actor, batch(HostBatchItem{Address: hostAddrA, Name: "a"}, HostBatchItem{Address: hostAddrA, Name: "b"}), nil)
	wantCode(t, err, CodeInvalidArgument)
	_, err = f.hosts.CreateHosts(ctx, f.actor, batch(make([]HostBatchItem, 4)...), nil)
	wantCode(t, err, CodeInvalidArgument)
	bad := batch(HostBatchItem{Address: hostAddrA, Name: "a"})
	bad.ProviderType = "sui"
	_, err = f.hosts.CreateHosts(ctx, f.actor, bad, nil)
	wantCode(t, err, CodeInvalidArgument)
	if list, err := f.hosts.ListHosts(ctx); err != nil || len(list) != 0 {
		t.Fatalf("rejected batches must save nothing: %v, %v", list, err)
	}

	list, err := f.hosts.CreateHosts(ctx, f.actor, batch(HostBatchItem{Address: hostAddrA, Name: "a", Region: "HK"}, HostBatchItem{Address: hostAddrB, Name: "b"}), nil)
	if err != nil || len(list) != 2 {
		t.Fatalf("CreateHosts = %v, err = %v", list, err)
	}
	for _, h := range list {
		if h.SSHPort != defaultSSHPort || h.LastTask == nil || h.LastTask.Kind != HostTaskProvision || h.LastTask.Status != HostTaskQueued {
			t.Fatalf("host = %+v, task = %+v", h, h.LastTask)
		}
	}
	select {
	case <-f.hosts.Requests():
	default:
		t.Fatal("new tasks should kick the runner")
	}
	// 整批原子：第二行与已有主机冲突，第一行也不保存
	_, err = f.hosts.CreateHosts(ctx, f.actor, batch(HostBatchItem{Address: "203.0.113.12", Name: "c"}, HostBatchItem{Address: hostAddrB, Name: "d"}), nil)
	wantCode(t, err, CodeConflict)
	if list, err := f.hosts.ListHosts(ctx); err != nil || len(list) != 2 {
		t.Fatalf("hosts after a conflicting batch = %d, err = %v", len(list), err)
	}
	// 一台主机同时只有一条未结束的任务（Ruling-71）
	_, err = f.hosts.CreateAction(ctx, f.actor, list[0].ID, HostTaskProvision, nil)
	wantCode(t, err, CodeConflict)
	_, err = f.hosts.UpdateHost(ctx, f.actor, list[0].ID, ServerHostInput{Name: "a", Address: hostAddrA, SSHPort: 2222, CredentialID: f.credID}, nil)
	wantCode(t, err, CodeConflict)
	wantCode(t, f.hosts.DeleteHost(ctx, f.actor, list[0].ID, nil), CodeConflict)
}

func TestHostNameMustNotCollideWithServer(t *testing.T) {
	f := newHostFixture(t)
	if _, err := f.panels.CreateServer(context.Background(), f.actor, serverInput("taken"), nil); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	_, err := f.hosts.CreateHosts(context.Background(), f.actor, HostBatchInput{
		CredentialID: f.credID, ProviderType: fakeType, Hosts: []HostBatchItem{{Address: hostAddrA, Name: "taken"}},
	}, nil)
	wantCode(t, err, CodeConflict)
}

func TestProvisionAdoptsExistingPanel(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	seedInbound(f.fleet.get(urlHTTPHostA), "1", 443, "现有入站")
	host := f.createHost(t, hostAddrA, "node-a")

	task := f.runNext(t)
	wantTask(t, task, HostTaskSucceeded, deploy.ReasonNone)
	if strings.Contains(task.Log, issuedToken) {
		t.Fatalf("task log leaks the token:\n%s", task.Log)
	}
	if !strings.Contains(task.Log, "明文 HTTP") {
		t.Fatalf("an http panel should be flagged in the log:\n%s", task.Log)
	}
	calls := strings.Join(f.deployer.Calls(), ",")
	if calls != "Detect,Provision" {
		t.Fatalf("the remote steps belong to the deployer, calls = %s", calls)
	}

	got, err := f.hosts.GetHost(ctx, host.ID)
	if err != nil || got.ServerID == nil || got.HostKeySHA256 != hostKeyFP || got.OSID != "debian" {
		t.Fatalf("host = %+v, err = %v", got, err)
	}
	srv, err := f.panels.GetServer(ctx, *got.ServerID)
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	if srv.Name != "node-a" || srv.BaseURL != urlHTTPHostA || srv.TLSMode != panel.TLSVerify || srv.Status != ServerStatusOnline || srv.InboundCount != 1 {
		t.Fatalf("server = %+v", srv)
	}
	if srv.HostID == nil || *srv.HostID != host.ID {
		t.Fatalf("server.HostID = %v", srv.HostID)
	}
	if f.fleet.tokens[urlHTTPHostA] != issuedToken {
		t.Fatal("the issued token should be the one used to reach the panel")
	}
	// 已接入后不能再部署
	_, err = f.hosts.CreateAction(ctx, f.actor, host.ID, HostTaskProvision, nil)
	wantCode(t, err, CodeConflict)
}

func TestProvisionInstallsOnCleanHost(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	f.deployer.Detection.PanelState, f.deployer.Detection.PanelVersion, f.deployer.Detection.Plan = deploy.PanelNone, "", deploy.PlanInstall
	f.deployer.Provisioned.IsFresh = true
	f.deployer.Provisioned.Access.HasTLS, f.deployer.Provisioned.Access.CertSHA256 = true, testCertPin
	host := f.createHost(t, hostAddrA, "node-a")

	task := f.runNext(t)
	wantTask(t, task, HostTaskSucceeded, deploy.ReasonNone)
	if task.Step != hostStepRegister || !strings.Contains(task.Log, "开始安装") {
		t.Fatalf("step = %s, log:\n%s", task.Step, task.Log)
	}
	got, err := f.hosts.GetHost(ctx, host.ID)
	if err != nil || got.ServerID == nil {
		t.Fatalf("host = %+v, err = %v", got, err)
	}
	srv, err := f.panels.GetServer(ctx, *got.ServerID)
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	// 新装的面板一律 HTTPS + 固定证书指纹（Ruling-70）
	if srv.BaseURL != urlHTTPSHostA || srv.TLSMode != panel.TLSPin || srv.CertPinSHA256 == nil || *srv.CertPinSHA256 != testCertPin {
		t.Fatalf("server = %+v", srv)
	}
}

func TestProvisionFailures(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(f *hostFixture)
		reason deploy.Reason
	}{
		{"unsupported host", func(f *hostFixture) {
			f.deployer.Detection.Plan, f.deployer.Detection.Reason, f.deployer.Detection.Message = deploy.PlanNone, deploy.ReasonNonstandard, "不是标准安装"
		}, deploy.ReasonNonstandard},
		{"ssh auth", func(f *hostFixture) {
			f.dialer.Err = &remote.Error{Kind: remote.KindAuth, Message: "SSH 登录被拒"}
		}, deploy.ReasonAuthFailed},
		{"ssh unreachable", func(f *hostFixture) {
			f.dialer.Err = &remote.Error{Kind: remote.KindUnreachable, Message: "连不上"}
		}, deploy.ReasonUnreachable},
		{"listen local", func(f *hostFixture) {
			f.deployer.Errors["Provision"] = deploy.NewError(deploy.ReasonListenLocal, "面板只监听本机")
		}, deploy.ReasonListenLocal},
		{"panel unreachable from hub", func(f *hostFixture) {
			f.fleet.get(urlHTTPHostA).FailWith(&panel.Error{Kind: panel.KindUnreachable, Message: "连接超时"})
		}, deploy.ReasonPanelUnreachable},
		{"panel rejects token", func(f *hostFixture) {
			f.fleet.get(urlHTTPHostA).FailWith(&panel.Error{Kind: panel.KindAuth, Message: "面板拒绝了 API token"})
		}, deploy.ReasonCommandFailed},
		{"server name taken", func(f *hostFixture) {
			// 主机建好之后才出现的同名服务器：接入时撞名
			in := serverInput("node-a")
			in.BaseURL = "https://other.example.com:2053/x"
			f.fleet.get(in.BaseURL).SetGUID("other-guid")
			if _, err := f.panels.CreateServer(context.Background(), f.actor, in, nil); err != nil {
				panic(err)
			}
		}, deploy.ReasonNameTaken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newHostFixture(t)
			host := f.createHost(t, hostAddrA, "node-a")
			tt.setup(f)
			task := f.runNext(t)
			wantTask(t, task, HostTaskFailed, tt.reason)
			if task.LastError == "" || task.FinishedAt == nil {
				t.Fatalf("failed task should carry a summary and a finish time: %+v", task.HostTask)
			}
			got, err := f.hosts.GetHost(context.Background(), host.ID)
			if err != nil || got.ServerID != nil {
				t.Fatalf("a failed provision must not link a server: %+v, %v", got, err)
			}
		})
	}
}

func TestProvisionIsReentrant(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	host := f.createHost(t, hostAddrA, "node-a")
	f.fleet.get(urlHTTPHostA).FailWith(&panel.Error{Kind: panel.KindUnreachable, Message: "安全组没放行"})
	wantTask(t, f.runNext(t), HostTaskFailed, deploy.ReasonPanelUnreachable)

	// 放行后重做：从检测开始，再走一遍接管
	f.fleet.get(urlHTTPHostA).FailWith(nil)
	wantTask(t, f.action(t, host.ID, HostTaskProvision), HostTaskSucceeded, deploy.ReasonNone)
	servers, err := f.panels.ListServers(ctx)
	if err != nil || len(servers) != 1 {
		t.Fatalf("servers = %d, err = %v", len(servers), err)
	}
}

func TestProvisionLinksKnownPanelOnSameAddressOnly(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	// 这台面板早先已手动接入（同一地址）：部署只更新它的令牌，不新建
	manual := serverInput("manual")
	manual.BaseURL = urlHTTPHostA
	existing, err := f.panels.CreateServer(ctx, f.actor, manual, nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	hostA := f.createHost(t, hostAddrA, "node-a")
	wantTask(t, f.runNext(t), HostTaskSucceeded, deploy.ReasonNone)
	gotA, err := f.hosts.GetHost(ctx, hostA.ID)
	if err != nil || gotA.ServerID == nil || *gotA.ServerID != existing.ID || gotA.ServerName != "manual" {
		t.Fatalf("host A should link the existing server: %+v, %v", gotA, err)
	}
	if f.fleet.tokens[urlHTTPHostA] != issuedToken {
		t.Fatal("the existing server should now use the issued token")
	}

	// 另一台机器是同一份镜像克隆的，面板标识相同、地址不同：必须新建，不能错连
	hostB := f.createHost(t, hostAddrB, "node-b")
	wantTask(t, f.runNext(t), HostTaskSucceeded, deploy.ReasonNone)
	gotB, err := f.hosts.GetHost(ctx, hostB.ID)
	if err != nil || gotB.ServerID == nil || *gotB.ServerID == existing.ID {
		t.Fatalf("host B must get its own server: %+v, %v", gotB, err)
	}
}

func TestHostKeyIsPinned(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	host := f.createHost(t, hostAddrA, "node-a")
	wantTask(t, f.runNext(t), HostTaskSucceeded, deploy.ReasonNone)
	targets := f.dialer.Targets()
	if targets[0].HostKeySHA256 != "" || targets[0].Username != "root" || len(targets[0].PrivateKey) == 0 {
		t.Fatalf("first dial should be unpinned and carry the key: %+v", targets[0].HostKeySHA256)
	}

	// 机器的主机密钥变了：之后的任务一律拒绝，直到管理员重置
	f.runner.HostKey = "SHA256:another"
	wantTask(t, f.action(t, host.ID, HostTaskDiagnose), HostTaskFailed, deploy.ReasonHostKeyChanged)
	if _, err := f.hosts.ResetHostKey(ctx, f.actor, host.ID, nil); err != nil {
		t.Fatalf("ResetHostKey: %v", err)
	}
	wantTask(t, f.action(t, host.ID, HostTaskDiagnose), HostTaskSucceeded, deploy.ReasonNone)
	got, err := f.hosts.GetHost(ctx, host.ID)
	if err != nil || got.HostKeySHA256 != "SHA256:another" {
		t.Fatalf("host key after reset = %q, err = %v", got.HostKeySHA256, err)
	}
	// 改地址同样清空已固定的密钥
	got, err = f.hosts.UpdateHost(ctx, f.actor, host.ID, ServerHostInput{Name: "node-a", Address: hostAddrB, SSHPort: defaultSSHPort, CredentialID: f.credID}, nil)
	if err != nil || got.HostKeySHA256 != "" {
		t.Fatalf("host key after address change = %q, err = %v", got.HostKeySHA256, err)
	}
}

func TestOperateActions(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	host := f.createHost(t, hostAddrA, "node-a")
	// 还没接入时只能部署和查看状态
	if _, err := f.hosts.CreateAction(ctx, f.actor, host.ID, HostTaskRestartPanel, nil); err == nil {
		t.Fatal("restart before provision must be refused")
	}
	wantTask(t, f.runNext(t), HostTaskSucceeded, deploy.ReasonNone)
	got, err := f.hosts.GetHost(ctx, host.ID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	serverID := *got.ServerID

	wantTask(t, f.action(t, host.ID, HostTaskRestartPanel), HostTaskSucceeded, deploy.ReasonNone)
	wantTask(t, f.action(t, host.ID, HostTaskRestartCore), HostTaskSucceeded, deploy.ReasonNone)
	// 升级要下载程序包、耗时不定，不占对账锁：锁被对账占着时照样能跑完
	unlock := lockServer(serverID)
	wantTask(t, f.action(t, host.ID, HostTaskUpgrade), HostTaskSucceeded, deploy.ReasonNone)
	unlock()

	f.deployer.Diagnosis = "服务状态: active\nxray started"
	diag := f.action(t, host.ID, HostTaskDiagnose)
	wantTask(t, diag, HostTaskSucceeded, deploy.ReasonNone)
	if !strings.Contains(diag.Log, "xray started") {
		t.Fatalf("diagnose output should be in the log:\n%s", diag.Log)
	}

	// 重签令牌：服务器换用新令牌
	f.deployer.Token = "rotatedTokenValue0123456789abcdefghijklmnopqrstuv"
	reissue := f.action(t, host.ID, HostTaskReissueToken)
	wantTask(t, reissue, HostTaskSucceeded, deploy.ReasonNone)
	if f.fleet.tokens[urlHTTPHostA] != f.deployer.Token || strings.Contains(reissue.Log, f.deployer.Token) {
		t.Fatal("reissue should switch the server to the new token without logging it")
	}

	// 配置证书：明文 HTTP 的服务器变成 https + pin，令牌沿用
	wantTask(t, f.action(t, host.ID, HostTaskEnableTLS), HostTaskSucceeded, deploy.ReasonNone)
	srv, err := f.panels.GetServer(ctx, serverID)
	if err != nil || srv.BaseURL != urlHTTPSHostA || srv.TLSMode != panel.TLSPin || srv.CertPinSHA256 == nil || *srv.CertPinSHA256 != testCertPin {
		t.Fatalf("server after enable_tls = %+v, err = %v", srv, err)
	}
	if f.fleet.tokens[urlHTTPSHostA] != f.deployer.Token {
		t.Fatal("enable_tls must keep the current token")
	}
	// 已是 HTTPS 再配一次：失败且不动面板
	before := len(f.deployer.Calls())
	wantTask(t, f.action(t, host.ID, HostTaskEnableTLS), HostTaskFailed, deploy.ReasonCommandFailed)
	if calls := f.deployer.Calls()[before:]; strings.Join(calls, ",") != "Detect" {
		t.Fatalf("second enable_tls calls = %v", calls)
	}

	// 动作执行了但面板没回来
	f.fleet.get(urlHTTPSHostA).FailWith(&panel.Error{Kind: panel.KindUnreachable, Message: "连接被拒"})
	wantTask(t, f.action(t, host.ID, HostTaskRestartPanel), HostTaskFailed, deploy.ReasonPanelUnreachable)
	// 命令本身失败
	f.fleet.get(urlHTTPSHostA).FailWith(nil)
	f.deployer.Errors["RestartCore"] = deploy.NewError(deploy.ReasonCommandFailed, "面板重载内核失败")
	wantTask(t, f.action(t, host.ID, HostTaskRestartCore), HostTaskFailed, deploy.ReasonCommandFailed)

	_, err = f.hosts.CreateAction(ctx, f.actor, host.ID, HostTaskVerify, nil)
	wantCode(t, err, CodeInvalidArgument)
	_, err = f.hosts.CreateAction(ctx, f.actor, host.ID, "rm -rf /", nil)
	wantCode(t, err, CodeInvalidArgument)
}

func TestTaskLifecycle(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	host := f.createHost(t, hostAddrA, "node-a")
	tasks, err := f.hosts.ListHostTasks(ctx, host.ID)
	if err != nil || len(tasks) != 1 || tasks[0].CreatedByName != "root" {
		t.Fatalf("tasks = %+v, err = %v", tasks, err)
	}

	// 排队中的可以取消，取消后主机空闲
	canceled, err := f.hosts.CancelTask(ctx, f.actor, tasks[0].ID, nil)
	if err != nil || canceled.Status != HostTaskCanceled {
		t.Fatalf("cancel = %+v, err = %v", canceled, err)
	}
	_, err = f.hosts.CancelTask(ctx, f.actor, tasks[0].ID, nil)
	wantCode(t, err, CodeConflict)
	if ids, err := f.hosts.ClaimTasks(ctx, 5); err != nil || len(ids) != 0 {
		t.Fatalf("a canceled task must not be claimed: %v, %v", ids, err)
	}

	// 进程重启：进行中的任务记为失败，主机可以重新发起
	if _, err := f.hosts.CreateAction(ctx, f.actor, host.ID, HostTaskProvision, nil); err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	ids, err := f.hosts.ClaimTasks(ctx, 5)
	if err != nil || len(ids) != 1 {
		t.Fatalf("ClaimTasks = %v, err = %v", ids, err)
	}
	_, err = f.hosts.CancelTask(ctx, f.actor, ids[0], nil)
	wantCode(t, err, CodeConflict)
	if n, err := f.hosts.FailInterrupted(ctx); err != nil || n != 1 {
		t.Fatalf("FailInterrupted = %d, err = %v", n, err)
	}
	task, err := f.hosts.GetTask(ctx, ids[0])
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	wantTask(t, task, HostTaskFailed, deploy.ReasonInterrupted)
	wantTask(t, f.action(t, host.ID, HostTaskProvision), HostTaskSucceeded, deploy.ReasonNone)

	if _, err := f.hosts.GetTask(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing task err = %v", err)
	}
	// 删主机：任务历史一并删除，服务器保留
	if err := f.hosts.DeleteHost(ctx, f.actor, host.ID, nil); err != nil {
		t.Fatalf("DeleteHost: %v", err)
	}
	if servers, err := f.panels.ListServers(ctx); err != nil || len(servers) != 1 || servers[0].HostID != nil {
		t.Fatalf("server should survive without a host: %+v, %v", servers, err)
	}
}

func TestBindServerAndDeleteServer(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	in := serverInput("manual")
	in.BaseURL = urlHTTPHostA
	srv, err := f.panels.CreateServer(ctx, f.actor, in, nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	host, err := f.hosts.BindServer(ctx, f.actor, srv.ID, HostBindInput{Address: hostAddrA, CredentialID: f.credID}, nil)
	if err != nil || host.Name != "manual" || host.ServerID == nil || host.LastTask == nil || host.LastTask.Kind != HostTaskVerify {
		t.Fatalf("bind = %+v, err = %v", host, err)
	}
	_, err = f.hosts.BindServer(ctx, f.actor, srv.ID, HostBindInput{Address: hostAddrB, CredentialID: f.credID}, nil)
	wantCode(t, err, CodeConflict)
	wantTask(t, f.runNext(t), HostTaskSucceeded, deploy.ReasonNone)

	// 机器上其实没有标准安装的面板：校验失败
	f.deployer.Detection.PanelState = deploy.PanelNone
	if _, err := f.hosts.q.CreateHostTask(ctx, createVerifyTask(host.ID)); err != nil {
		t.Fatalf("queue verify: %v", err)
	}
	wantTask(t, f.runNext(t), HostTaskFailed, deploy.ReasonNonstandard)

	// 删服务器：主机保留、解除关联，可再次部署
	if err := f.panels.DeleteServer(ctx, f.actor, srv.ID, nil); err != nil {
		t.Fatalf("DeleteServer: %v", err)
	}
	got, err := f.hosts.GetHost(ctx, host.ID)
	if err != nil || got.ServerID != nil {
		t.Fatalf("host after server delete = %+v, err = %v", got, err)
	}
}

func TestResetPanelLogin(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	host := f.createHost(t, hostAddrA, "node-a")
	// 有未结束的任务、或还没接入时都不行
	_, err := f.hosts.ResetPanelLogin(ctx, f.actor, host.ID, nil)
	wantCode(t, err, CodeConflict)
	wantTask(t, f.runNext(t), HostTaskSucceeded, deploy.ReasonNone)

	login, err := f.hosts.ResetPanelLogin(ctx, f.actor, host.ID, nil)
	if err != nil || login.Username != "newuser" || login.Password == "" {
		t.Fatalf("login = %+v, err = %v", login, err)
	}
	logs, err := f.rbac.ListAuditLogs(ctx, PageQuery{PageSize: 100})
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	var isAudited bool
	for _, l := range logs.Items {
		if bytes.Contains(l.Detail, []byte(login.Password)) || bytes.Contains(l.Detail, []byte(issuedToken)) {
			t.Fatalf("audit %s leaks a secret: %s", l.Action, l.Detail)
		}
		isAudited = isAudited || l.Action == ActionHostPanelLoginReset
	}
	if !isAudited {
		t.Fatal("panel login reset should be audited")
	}

	f.deployer.Errors["ResetPanelLogin"] = deploy.NewError(deploy.ReasonCommandFailed, "面板登录口令重置失败")
	_, err = f.hosts.ResetPanelLogin(ctx, f.actor, host.ID, nil)
	wantCode(t, err, CodeUpstream)
}

func TestTestHost(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	in := HostTestInput{Address: hostAddrA, CredentialID: f.credID, ProviderType: fakeType}

	det, err := f.hosts.TestHost(ctx, f.actor, in)
	if err != nil || det.HostKeySHA256 != hostKeyFP || det.Plan != deploy.PlanAdopt || det.PanelVersion != "3.11.0" {
		t.Fatalf("detection = %+v, err = %v", det, err)
	}
	if !f.runner.Closed() {
		t.Fatal("the test connection should be closed")
	}
	if list, err := f.hosts.ListHosts(ctx); err != nil || len(list) != 0 {
		t.Fatal("a connection test must not save anything")
	}

	f.dialer.Err = &remote.Error{Kind: remote.KindAuth, Message: "SSH 登录被拒"}
	_, err = f.hosts.TestHost(ctx, f.actor, in)
	wantCode(t, err, CodeUpstream)
	// 不合法的输入不计入频控
	_, err = f.hosts.TestHost(ctx, f.actor, HostTestInput{Address: "127.0.0.1", CredentialID: f.credID, ProviderType: fakeType})
	wantCode(t, err, CodeInvalidArgument)
	_, err = f.hosts.TestHost(ctx, f.actor, in)
	wantCode(t, err, CodeUpstream)
	_, err = f.hosts.TestHost(ctx, f.actor, in)
	wantCode(t, err, CodeRateLimited)
}

// createVerifyTask 直接排一条校验任务（正常只由绑定服务器产生），用来重跑校验。
func createVerifyTask(hostID int64) sqlcgen.CreateHostTaskParams {
	return sqlcgen.CreateHostTaskParams{HostID: hostID, Kind: HostTaskVerify}
}
