package service

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"v2-hub-server/internal/manager/acmecert"
	"v2-hub-server/internal/manager/deploy"
	"v2-hub-server/internal/manager/dns"
	"v2-hub-server/internal/manager/panel"
)

const (
	fakeDNSType      = "fakedns"
	testZone         = "example.com"
	testDNSValue     = "dns-api-value-0123456789abcdef"
	testCertLifetime = 90 * 24 * time.Hour
	testRenewBefore  = 30 * 24 * time.Hour
	domainNodeA      = "node-a.example.com"
	urlDomainNodeA   = "https://node-a.example.com:2053/abc"
	fakeACMEURL      = "https://acme.test/directory"
)

// fakeIssuer 是假的证书签发方：照样经 solver 写、删验证记录，然后给出一张假证书。
type fakeIssuer struct {
	mu       sync.Mutex
	err      error
	notAfter time.Time
	domains  []string
	keys     [][]byte
}

func (f *fakeIssuer) DirectoryURL() string { return fakeACMEURL }

func (f *fakeIssuer) Issue(ctx context.Context, accountKey []byte, domain string, solver acmecert.Solver) (*acmecert.Certificate, error) {
	f.mu.Lock()
	f.domains = append(f.domains, domain)
	f.keys = append(f.keys, accountKey)
	err, notAfter := f.err, f.notAfter
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	cleanup, err := solver.Present(ctx, "_acme-challenge."+domain, "challenge-value")
	if err != nil {
		return nil, err
	}
	if err := cleanup(ctx); err != nil {
		return nil, err
	}
	return &acmecert.Certificate{CertPEM: []byte("CERT " + domain), KeyPEM: []byte("KEY " + domain), NotAfter: notAfter, Leaf: []byte("leaf " + domain)}, nil
}

func (f *fakeIssuer) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeIssuer) setNotAfter(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notAfter = t
}

func (f *fakeIssuer) issued() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.domains...)
}

func (f *hostFixture) createDNSAccount(t *testing.T) DNSAccount {
	t.Helper()
	a, err := f.hosts.CreateDNSAccount(context.Background(), f.actor, DNSAccountInput{Name: "cf", ProviderType: fakeDNSType, Zone: testZone, Token: testDNSValue}, nil)
	if err != nil {
		t.Fatalf("CreateDNSAccount: %v", err)
	}
	return *a
}

func (f *hostFixture) createHostWithDNS(t *testing.T, address, name string, accountID int64) ServerHost {
	t.Helper()
	list, err := f.hosts.CreateHosts(context.Background(), f.actor, HostBatchInput{
		CredentialID: f.credID, ProviderType: fakeType, DNSAccountID: &accountID, Hosts: []HostBatchItem{{Address: address, Name: name}},
	}, nil)
	if err != nil {
		t.Fatalf("CreateHosts: %v", err)
	}
	return list[0]
}

// freshInstall 把假部署实现改成“主机上没有面板，装完是自签证书”。
func (f *hostFixture) freshInstall() {
	f.deployer.Detection.PanelState, f.deployer.Detection.PanelVersion, f.deployer.Detection.Plan = deploy.PanelNone, "", deploy.PlanInstall
	f.deployer.Provisioned = deploy.Provisioned{
		Access: deploy.PanelAccess{HasTLS: true, Port: testPanelPort, BasePath: testBasePath, CertSHA256: testCertPin}, Token: issuedToken, IsFresh: true,
	}
}

// installed 把假部署实现改成“面板已装好”，之后的运维动作才通得过检测。
func (f *hostFixture) installed() {
	f.deployer.Detection.PanelState, f.deployer.Detection.PanelVersion, f.deployer.Detection.Plan = deploy.PanelStandard, "3.11.0", deploy.PlanAdopt
}

func TestDNSAccountTokenIsSealedAndChecked(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()

	for _, in := range []DNSAccountInput{
		{Name: "a", ProviderType: "nope", Zone: testZone, Token: testDNSValue},
		{Name: "a", ProviderType: fakeDNSType, Zone: "not a zone", Token: testDNSValue},
		{Name: "a", ProviderType: fakeDNSType, Zone: "localhost", Token: testDNSValue},
		{Name: "a", ProviderType: fakeDNSType, Zone: "203.0.113.1", Token: testDNSValue},
		{Name: "a", ProviderType: fakeDNSType, Zone: testZone},
		{Name: "a", ProviderType: fakeDNSType, Zone: testZone, Token: "has space"},
		{Name: "", ProviderType: fakeDNSType, Zone: testZone, Token: testDNSValue},
	} {
		_, err := f.hosts.CreateDNSAccount(ctx, f.actor, in, nil)
		wantCode(t, err, CodeInvalidArgument)
	}

	// 服务商不认令牌：502，什么都不保存
	f.dnsFake.SetError("CheckZone", &dns.Error{Kind: dns.KindAuth, Message: "令牌被拒"})
	_, err := f.hosts.CreateDNSAccount(ctx, f.actor, DNSAccountInput{Name: "cf", ProviderType: fakeDNSType, Zone: testZone, Token: testDNSValue}, nil)
	wantCode(t, err, CodeUpstream)
	if list, err := f.hosts.ListDNSAccounts(ctx); err != nil || len(list) != 0 {
		t.Fatalf("a rejected token must not be saved: %+v, %v", list, err)
	}
	f.dnsFake.SetError("CheckZone", nil)

	account, err := f.hosts.CreateDNSAccount(ctx, f.actor, DNSAccountInput{Name: " cf ", ProviderType: fakeDNSType, Zone: "Example.COM.", Token: testDNSValue, Remark: "主域名"}, nil)
	if err != nil || account.Name != "cf" || account.Zone != testZone || account.HostCount != 0 {
		t.Fatalf("account = %+v, err = %v", account, err)
	}
	var sealed []byte
	if err := f.pool.QueryRow(ctx, "SELECT token_enc FROM dns_account WHERE id = $1", account.ID).Scan(&sealed); err != nil {
		t.Fatalf("read token: %v", err)
	}
	if bytes.Contains(sealed, []byte(testDNSValue)) {
		t.Fatal("token is stored in plain text")
	}
	_, err = f.hosts.CreateDNSAccount(ctx, f.actor, DNSAccountInput{Name: "cf", ProviderType: fakeDNSType, Zone: "other.org", Token: testDNSValue}, nil)
	wantCode(t, err, CodeConflict)

	// 留空沿用原令牌：改根域名时用原令牌重新校验
	opened := len(f.dnsFake.Accounts)
	updated, err := f.hosts.UpdateDNSAccount(ctx, f.actor, account.ID, DNSAccountInput{Name: "cf", ProviderType: fakeDNSType, Zone: "other.org"}, nil)
	if err != nil || updated.Zone != "other.org" {
		t.Fatalf("update = %+v, err = %v", updated, err)
	}
	if got := f.dnsFake.Accounts[opened:]; len(got) != 1 || got[0].Token != testDNSValue || got[0].Zone != "other.org" {
		t.Fatalf("zone change must be re-checked with the stored token, opened %+v", len(got))
	}
	// 只改备注：不再访问服务商
	opened = len(f.dnsFake.Accounts)
	if _, err := f.hosts.UpdateDNSAccount(ctx, f.actor, account.ID, DNSAccountInput{Name: "cf", ProviderType: fakeDNSType, Zone: "other.org", Remark: "x"}, nil); err != nil {
		t.Fatalf("update remark: %v", err)
	}
	if len(f.dnsFake.Accounts) != opened {
		t.Fatal("changing only the remark should not call the provider")
	}
	// 换令牌
	const rotated = "dns-api-value-rotated-9876543210"
	if _, err := f.hosts.UpdateDNSAccount(ctx, f.actor, account.ID, DNSAccountInput{Name: "cf", ProviderType: fakeDNSType, Zone: "other.org", Token: rotated}, nil); err != nil {
		t.Fatalf("rotate token: %v", err)
	}
	secret, err := f.hosts.openDNSAccount(ctx, account.ID)
	if err != nil || secret.token != rotated {
		t.Fatalf("stored token after rotation: err = %v", err)
	}
	_, err = f.hosts.UpdateDNSAccount(ctx, f.actor, 999999, DNSAccountInput{Name: "x", ProviderType: fakeDNSType, Zone: testZone, Token: rotated}, nil)
	wantCode(t, err, CodeNotFound)

	logs, err := f.rbac.ListAuditLogs(ctx, PageQuery{PageSize: 100})
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	seen := map[string]bool{}
	for _, l := range logs.Items {
		seen[l.Action] = true
		if strings.Contains(string(l.Detail), testDNSValue) || strings.Contains(string(l.Detail), rotated) {
			t.Fatalf("audit %s leaks the token: %s", l.Action, l.Detail)
		}
	}
	if !seen[ActionDNSAccountCreate] || !seen[ActionDNSAccountUpdate] {
		t.Fatalf("dns account audits missing: %v", seen)
	}

	if err := f.hosts.DeleteDNSAccount(ctx, f.actor, account.ID, nil); err != nil {
		t.Fatalf("DeleteDNSAccount: %v", err)
	}
	wantCode(t, f.hosts.DeleteDNSAccount(ctx, f.actor, account.ID, nil), CodeNotFound)
}

func TestHostDomainRules(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	account := f.createDNSAccount(t)
	batch := func(item HostBatchItem, accountID *int64) ([]ServerHost, error) {
		return f.hosts.CreateHosts(ctx, f.actor, HostBatchInput{CredentialID: f.credID, ProviderType: fakeType, DNSAccountID: accountID, Hosts: []HostBatchItem{item}}, nil)
	}

	// 没选账号却填了域名；域名不在根域名下；域名不合法；账号不存在
	_, err := batch(HostBatchItem{Address: hostAddrA, Name: "node-a", Domain: domainNodeA}, nil)
	wantCode(t, err, CodeInvalidArgument)
	_, err = batch(HostBatchItem{Address: hostAddrA, Name: "node-a", Domain: "node.other.org"}, &account.ID)
	wantCode(t, err, CodeInvalidArgument)
	_, err = batch(HostBatchItem{Address: hostAddrA, Name: "node-a", Domain: "bad_name.example.com"}, &account.ID)
	wantCode(t, err, CodeInvalidArgument)
	missing := int64(999999)
	_, err = batch(HostBatchItem{Address: hostAddrA, Name: "node-a"}, &missing)
	wantCode(t, err, CodeNotFound)

	// 留空：前缀取名称；名称转不出前缀就取地址
	host := f.createHostWithDNS(t, hostAddrA, "Node A", account.ID)
	if host.Domain != domainNodeA || host.DNSAccountID == nil || *host.DNSAccountID != account.ID || host.DNSAccountName != "cf" || host.CertNotAfter != nil {
		t.Fatalf("host = %+v", host)
	}
	second := f.createHostWithDNS(t, hostAddrB, "香港一号", account.ID)
	if second.Domain != "ip-203-0-113-11.example.com" {
		t.Fatalf("domain from address = %q", second.Domain)
	}
	// 手填：转小写；与别的主机重复 409
	list, err := batch(HostBatchItem{Address: "203.0.113.12", Name: "node-c", Domain: "HK3.Example.com"}, &account.ID)
	if err != nil || list[0].Domain != "hk3.example.com" {
		t.Fatalf("explicit domain = %+v, err = %v", list, err)
	}
	_, err = batch(HostBatchItem{Address: "203.0.113.13", Name: "node-d", Domain: domainNodeA}, &account.ID)
	wantCode(t, err, CodeConflict)

	// 被引用时：不能删，不能改根域名，改别的可以
	wantCode(t, f.hosts.DeleteDNSAccount(ctx, f.actor, account.ID, nil), CodeConflict)
	_, err = f.hosts.UpdateDNSAccount(ctx, f.actor, account.ID, DNSAccountInput{Name: "cf", ProviderType: fakeDNSType, Zone: "other.org"}, nil)
	wantCode(t, err, CodeConflict)
	if got, err := f.hosts.UpdateDNSAccount(ctx, f.actor, account.ID, DNSAccountInput{Name: "cf-renamed", ProviderType: fakeDNSType, Zone: testZone}, nil); err != nil || got.HostCount != 3 {
		t.Fatalf("rename = %+v, err = %v", got, err)
	}

	// 改主机：先取消排队的部署任务
	tasks, err := f.hosts.ListHostTasks(ctx, host.ID)
	if err != nil {
		t.Fatalf("ListHostTasks: %v", err)
	}
	if _, err := f.hosts.CancelTask(ctx, f.actor, tasks[0].ID, nil); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	expiry := f.clock.now().Add(testCertLifetime)
	if _, err := f.pool.Exec(ctx, "UPDATE server_host SET cert_not_after = $2 WHERE id = $1", host.ID, expiry); err != nil {
		t.Fatalf("seed cert expiry: %v", err)
	}
	in := ServerHostInput{Name: "Node A", Address: hostAddrA, SSHPort: 22, CredentialID: f.credID, DNSAccountID: &account.ID, Domain: domainNodeA}
	// 域名没变：到期时间保留
	kept, err := f.hosts.UpdateHost(ctx, f.actor, host.ID, in, nil)
	if err != nil || kept.CertNotAfter == nil {
		t.Fatalf("unchanged domain must keep the expiry: %+v, err = %v", kept, err)
	}
	// 换了域名：到期时间清空，等下一次“配置证书”
	in.Domain = "moved.example.com"
	moved, err := f.hosts.UpdateHost(ctx, f.actor, host.ID, in, nil)
	if err != nil || moved.Domain != "moved.example.com" || moved.CertNotAfter != nil {
		t.Fatalf("changed domain must clear the expiry: %+v, err = %v", moved, err)
	}
	// 去掉 DNS 账号：域名一并清掉
	in.DNSAccountID, in.Domain = nil, ""
	cleared, err := f.hosts.UpdateHost(ctx, f.actor, host.ID, in, nil)
	if err != nil || cleared.DNSAccountID != nil || cleared.Domain != "" {
		t.Fatalf("cleared = %+v, err = %v", cleared, err)
	}
}

func TestProvisionIssuesPublicCertificate(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	f.freshInstall()
	account := f.createDNSAccount(t)
	host := f.createHostWithDNS(t, hostAddrA, "node-a", account.ID)

	task := f.runNext(t)
	wantTask(t, task, HostTaskSucceeded, deploy.ReasonNone)
	got, err := f.hosts.GetHost(ctx, host.ID)
	if err != nil || got.ServerID == nil || got.CertNotAfter == nil || !got.CertNotAfter.Equal(f.issuer.notAfter) {
		t.Fatalf("host after provision = %+v, err = %v", got, err)
	}
	srv, err := f.panels.GetServer(ctx, *got.ServerID)
	if err != nil || srv.BaseURL != urlDomainNodeA || srv.TLSMode != panel.TLSVerify || srv.CertPinSHA256 != nil {
		t.Fatalf("server should be reached by domain with verify: %+v, err = %v", srv, err)
	}
	if f.fleet.tokens[urlDomainNodeA] != issuedToken {
		t.Fatal("the token must carry over to the domain address")
	}
	points, txt, removed := f.dnsFake.Snapshot()
	if points[domainNodeA] != hostAddrA || len(txt) != 0 || removed != 1 {
		t.Fatalf("dns: points = %v, txt left = %v, removed = %d", points, txt, removed)
	}
	if string(f.deployer.InstalledCert()) != "CERT "+domainNodeA {
		t.Fatalf("installed cert = %q", f.deployer.InstalledCert())
	}
	if calls := strings.Join(f.deployer.Calls(), ","); calls != "Detect,Provision,InstallCert" {
		t.Fatalf("deployer calls = %s", calls)
	}
	if issued := f.issuer.issued(); len(issued) != 1 || issued[0] != domainNodeA {
		t.Fatalf("issued = %v", issued)
	}
	if task.Step != hostStepInstallCert {
		t.Fatalf("last step = %q", task.Step)
	}
	for _, secret := range []string{testDNSValue, issuedToken, "KEY " + domainNodeA} {
		if strings.Contains(task.Log, secret) || strings.Contains(task.LastError, secret) {
			t.Fatalf("task log leaks %q:\n%s", secret, task.Log)
		}
	}

	// ACME 账号密钥：首次生成后加密保存，之后复用同一把
	f.installed()
	wantTask(t, f.action(t, host.ID, HostTaskEnableTLS), HostTaskSucceeded, deploy.ReasonNone)
	if len(f.issuer.keys) != 2 || !bytes.Equal(f.issuer.keys[0], f.issuer.keys[1]) || len(f.issuer.keys[0]) == 0 {
		t.Fatal("the acme account key must be generated once and reused")
	}
	var sealed []byte
	if err := f.pool.QueryRow(ctx, "SELECT key_enc FROM acme_account WHERE directory_url = $1", fakeACMEURL).Scan(&sealed); err != nil {
		t.Fatalf("read acme account: %v", err)
	}
	if bytes.Equal(sealed, f.issuer.keys[0]) {
		t.Fatal("acme account key is stored in plain text")
	}
}

func TestCertificateFailureKeepsServerAttached(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	f.freshInstall()
	account := f.createDNSAccount(t)
	host := f.createHostWithDNS(t, hostAddrA, "node-a", account.ID)

	// 签发方拒绝：部署任务失败，但面板已按自签证书接入
	f.issuer.setErr(&acmecert.Error{Kind: acmecert.KindCA, Message: "证书签发方限流，请晚些再试"})
	task := f.runNext(t)
	wantTask(t, task, HostTaskFailed, deploy.ReasonCertFailed)
	if !strings.Contains(task.Log, "不需要重新部署") {
		t.Fatalf("log should say the panel is attached:\n%s", task.Log)
	}
	got, err := f.hosts.GetHost(ctx, host.ID)
	if err != nil || got.ServerID == nil || got.CertNotAfter != nil {
		t.Fatalf("host = %+v, err = %v", got, err)
	}
	serverID := *got.ServerID
	srv, err := f.panels.GetServer(ctx, serverID)
	if err != nil || srv.BaseURL != urlHTTPSHostA || srv.TLSMode != panel.TLSPin || srv.CertPinSHA256 == nil || *srv.CertPinSHA256 != testCertPin {
		t.Fatalf("server should stay on the self-signed pin: %+v, err = %v", srv, err)
	}
	if strings.Contains(strings.Join(f.deployer.Calls(), ","), "InstallCert") {
		t.Fatal("nothing must be installed when issuing failed")
	}

	// 验证记录没生效 → dns_failed；DNS 服务商拒绝 → dns_failed
	f.installed()
	f.issuer.setErr(&acmecert.Error{Kind: acmecert.KindDNS, Message: "验证用的 TXT 记录迟迟没有生效"})
	wantTask(t, f.action(t, host.ID, HostTaskEnableTLS), HostTaskFailed, deploy.ReasonDNSFailed)
	f.issuer.setErr(nil)
	f.dnsFake.SetError("PointTo", &dns.Error{Kind: dns.KindRejected, Message: "Cloudflare 拒绝了请求"})
	wantTask(t, f.action(t, host.ID, HostTaskEnableTLS), HostTaskFailed, deploy.ReasonDNSFailed)
	f.dnsFake.SetError("PointTo", nil)

	// 证书装好了但按域名连不上：退回“原地址 + 新证书指纹”，任务记失败
	f.fleet.get(urlDomainNodeA).FailWith(&panel.Error{Kind: panel.KindUnreachable, Message: "解析不到"})
	wantTask(t, f.action(t, host.ID, HostTaskEnableTLS), HostTaskFailed, deploy.ReasonPanelUnreachable)
	srv, err = f.panels.GetServer(ctx, serverID)
	wantPin := leafPin([]byte("leaf " + domainNodeA))
	if err != nil || srv.BaseURL != urlHTTPSHostA || srv.TLSMode != panel.TLSPin || srv.CertPinSHA256 == nil || *srv.CertPinSHA256 != wantPin {
		t.Fatalf("server should fall back to the new certificate's pin: %+v, err = %v", srv, err)
	}
	if got, err := f.hosts.GetHost(ctx, host.ID); err != nil || got.CertNotAfter == nil {
		t.Fatalf("the installed certificate's expiry must be recorded: %+v, err = %v", got, err)
	}

	// 解析通了再做一次：换成域名 + verify
	f.fleet.get(urlDomainNodeA).FailWith(nil)
	wantTask(t, f.action(t, host.ID, HostTaskEnableTLS), HostTaskSucceeded, deploy.ReasonNone)
	srv, err = f.panels.GetServer(ctx, serverID)
	if err != nil || srv.BaseURL != urlDomainNodeA || srv.TLSMode != panel.TLSVerify || srv.CertPinSHA256 != nil {
		t.Fatalf("server after retry = %+v, err = %v", srv, err)
	}
}

func TestAdoptWithDNSAccount(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	account := f.createDNSAccount(t)

	// 接管的面板自己有证书：不动它
	f.deployer.Provisioned.Access.HasTLS, f.deployer.Provisioned.Access.CertSHA256 = true, testCertPin
	host := f.createHostWithDNS(t, hostAddrA, "node-a", account.ID)
	wantTask(t, f.runNext(t), HostTaskSucceeded, deploy.ReasonNone)
	if len(f.issuer.issued()) != 0 {
		t.Fatal("a panel that already has a certificate must be left alone")
	}
	got, err := f.hosts.GetHost(ctx, host.ID)
	if err != nil || got.ServerID == nil || got.CertNotAfter != nil {
		t.Fatalf("host = %+v, err = %v", got, err)
	}
	// 平台早先给它申请过证书的面板：按域名就能通过校验，直接用域名接入
	if srv, err := f.panels.GetServer(ctx, *got.ServerID); err != nil || srv.BaseURL != urlDomainNodeA || srv.TLSMode != panel.TLSVerify {
		t.Fatalf("server = %+v, err = %v", srv, err)
	}

	// 接管的面板是明文 HTTP：接着申请
	f.deployer.Provisioned.Access.HasTLS, f.deployer.Provisioned.Access.CertSHA256 = false, ""
	plain := f.createHostWithDNS(t, hostAddrB, "node-b", account.ID)
	wantTask(t, f.runNext(t), HostTaskSucceeded, deploy.ReasonNone)
	if issued := f.issuer.issued(); len(issued) != 1 || issued[0] != "node-b.example.com" {
		t.Fatalf("issued = %v", issued)
	}
	got, err = f.hosts.GetHost(ctx, plain.ID)
	if err != nil || got.ServerID == nil || got.CertNotAfter == nil {
		t.Fatalf("host = %+v, err = %v", got, err)
	}
	if srv, err := f.panels.GetServer(ctx, *got.ServerID); err != nil || srv.BaseURL != "https://node-b.example.com:2053/abc" || srv.TLSMode != panel.TLSVerify {
		t.Fatalf("server = %+v, err = %v", srv, err)
	}
}

func TestQueueCertRenewals(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	f.freshInstall()
	account := f.createDNSAccount(t)
	host := f.createHostWithDNS(t, hostAddrA, "node-a", account.ID)
	wantTask(t, f.runNext(t), HostTaskSucceeded, deploy.ReasonNone)
	plain := f.createHost(t, hostAddrB, "node-b")
	f.deployer.Detection.PanelState, f.deployer.Detection.Plan = deploy.PanelStandard, deploy.PlanAdopt
	f.deployer.Provisioned = deploy.Provisioned{Access: deploy.PanelAccess{Port: testPanelPort, BasePath: testBasePath}, Token: issuedToken}
	wantTask(t, f.runNext(t), HostTaskSucceeded, deploy.ReasonNone)

	// 离到期还远：不排
	if n, err := f.hosts.QueueCertRenewals(ctx); err != nil || n != 0 {
		t.Fatalf("fresh certificate: queued %d, err = %v", n, err)
	}
	// 进入续期窗口：只给带证书的那台排一条，没有发起人
	f.clock.advance(testCertLifetime - testRenewBefore + time.Hour)
	if n, err := f.hosts.QueueCertRenewals(ctx); err != nil || n != 1 {
		t.Fatalf("due certificate: queued %d, err = %v", n, err)
	}
	tasks, err := f.hosts.ListHostTasks(ctx, host.ID)
	if err != nil || tasks[0].Kind != HostTaskEnableTLS || tasks[0].Status != HostTaskQueued || tasks[0].CreatedByName != "" {
		t.Fatalf("renewal task = %+v, err = %v", tasks[0], err)
	}
	if tasks, err := f.hosts.ListHostTasks(ctx, plain.ID); err != nil || len(tasks) != 1 {
		t.Fatalf("a host without a dns account must not be renewed: %+v, err = %v", tasks, err)
	}
	// 任务还没跑完：不重复排
	if n, err := f.hosts.QueueCertRenewals(ctx); err != nil || n != 0 {
		t.Fatalf("pending renewal: queued %d, err = %v", n, err)
	}
	// 跑完后到期时间更新，不再排
	renewed := f.clock.now().Add(testCertLifetime)
	f.issuer.setNotAfter(renewed)
	wantTask(t, f.runNext(t), HostTaskSucceeded, deploy.ReasonNone)
	if got, err := f.hosts.GetHost(ctx, host.ID); err != nil || got.CertNotAfter == nil || !got.CertNotAfter.Equal(renewed) {
		t.Fatalf("host after renewal = %+v, err = %v", got, err)
	}
	if n, err := f.hosts.QueueCertRenewals(ctx); err != nil || n != 0 {
		t.Fatalf("renewed certificate: queued %d, err = %v", n, err)
	}

	logs, err := f.rbac.ListAuditLogs(ctx, PageQuery{PageSize: 100})
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	var bySystem bool
	for _, l := range logs.Items {
		bySystem = bySystem || (l.Action == ActionHostTaskCreate && l.ActorType == ActorSystem)
	}
	if !bySystem {
		t.Fatal("the renewal task must be audited with the system as actor")
	}
}

func TestTaskTimeout(t *testing.T) {
	s := &HostService{cfg: HostConfig{CommandTimeout: time.Minute, InstallTimeout: 20 * time.Minute, CertTimeout: 10 * time.Minute}}
	cases := []struct {
		kind   string
		hasDNS bool
		want   time.Duration
	}{
		{HostTaskProvision, true, 20 * time.Minute}, {HostTaskUpgrade, false, 20 * time.Minute},
		{HostTaskEnableTLS, true, 10 * time.Minute}, {HostTaskEnableTLS, false, time.Minute}, {HostTaskRestartPanel, true, time.Minute},
	}
	for _, c := range cases {
		if got := s.taskTimeout(c.kind, c.hasDNS); got != c.want {
			t.Fatalf("taskTimeout(%s, %v) = %s, want %s", c.kind, c.hasDNS, got, c.want)
		}
	}
}

func TestDefaultDomain(t *testing.T) {
	cases := []struct{ name, address, want string }{
		{"HK-01", "203.0.113.1", "hk-01.example.com"},
		{"  日本 Tokyo #2 ", "203.0.113.1", "tokyo-2.example.com"},
		{"香港", "203.0.113.1", "ip-203-0-113-1.example.com"},
		{"香港", "2001:db8::1", "ip-2001-db8-1.example.com"},
		{"香港", "origin.example.net", "origin-example-net.example.com"},
		{strings.Repeat("a", 80), "203.0.113.1", strings.Repeat("a", 63) + ".example.com"},
	}
	for _, c := range cases {
		if got := defaultDomain(c.name, c.address, testZone); got != c.want {
			t.Fatalf("defaultDomain(%q, %q) = %q, want %q", c.name, c.address, got, c.want)
		}
	}
}
