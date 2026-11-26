package service

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"v2-hub-server/internal/manager/deploy"
)

// pwSSH 是用例里密码类型 SSH 凭据的密码（仅测试库内有效）。
const pwSSH = "ssh-pass-0123"

// newPasswordCredential 在主机夹具上再建一份密码类型的凭据。
func newPasswordCredential(t *testing.T, f *hostFixture) int64 {
	t.Helper()
	c, err := f.hosts.CreateCredential(context.Background(), f.actor, SSHCredentialInput{Name: "root-pw", Username: "root", AuthType: SSHAuthPassword, Secret: pwSSH}, nil)
	if err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}
	return c.ID
}

// 密码凭据的确认（Ruling-97）：逐入口校验“新目标必须再输一次密码”，用过的目标与私钥凭据不受影响。
func TestCredentialSecretConfirmation(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	pc := newPasswordCredential(t, f)
	const wrong = "nope"

	// 连接测试：新目标缺密码 / 密码不对都 400；正确才连
	test := HostTestInput{Address: hostAddrA, CredentialID: pc, ProviderType: fakeType}
	_, err := f.hosts.TestHost(ctx, f.actor, test)
	wantCode(t, err, CodeInvalidArgument)
	test.CredentialSecret = wrong
	_, err = f.hosts.TestHost(ctx, f.actor, test)
	wantCode(t, err, CodeInvalidArgument)
	if n := len(f.dialer.Targets()); n != 0 {
		t.Fatalf("must not dial before the password is confirmed, dialed %d", n)
	}
	test.CredentialSecret = pwSSH
	if _, err := f.hosts.TestHost(ctx, f.actor, test); err != nil {
		t.Fatalf("TestHost with confirmed password: %v", err)
	}
	// 私钥凭据不受影响
	if _, err := f.hosts.TestHost(ctx, f.actor, HostTestInput{Address: hostAddrB, CredentialID: f.credID, ProviderType: fakeType}); err != nil {
		t.Fatalf("TestHost with key credential: %v", err)
	}

	// 批量新建：整批确认一次
	batch := HostBatchInput{CredentialID: pc, ProviderType: fakeType, Hosts: []HostBatchItem{{Address: hostAddrA, Name: "pw-a"}}}
	_, err = f.hosts.CreateHosts(ctx, f.actor, batch, nil)
	wantCode(t, err, CodeInvalidArgument)
	batch.CredentialSecret = pwSSH
	list, err := f.hosts.CreateHosts(ctx, f.actor, batch, nil)
	if err != nil {
		t.Fatalf("CreateHosts: %v", err)
	}
	host := list[0]
	wantTask(t, f.runNext(t), HostTaskSucceeded, deploy.ReasonNone)

	// 用过的目标：连接测试仍一律要求（Ruling-107）
	f.clock.advance(time.Minute + time.Second) // 越过连接测试的频控窗口
	_, err = f.hosts.TestHost(ctx, f.actor, HostTestInput{Address: hostAddrA, CredentialID: pc, ProviderType: fakeType})
	wantCode(t, err, CodeInvalidArgument)
	if _, err := f.hosts.TestHost(ctx, f.actor, HostTestInput{Address: hostAddrA, CredentialID: pc, ProviderType: fakeType, CredentialSecret: pwSSH}); err != nil {
		t.Fatalf("TestHost on a used target with confirmed password: %v", err)
	}

	// 修改主机：只改名不要求；改端口要求
	edit := ServerHostInput{Name: "pw-a2", Address: hostAddrA, SSHPort: defaultSSHPort, CredentialID: pc}
	if _, err := f.hosts.UpdateHost(ctx, f.actor, host.ID, edit, nil); err != nil {
		t.Fatalf("rename host: %v", err)
	}
	edit.SSHPort = 2222
	_, err = f.hosts.UpdateHost(ctx, f.actor, host.ID, edit, nil)
	wantCode(t, err, CodeInvalidArgument)
	edit.CredentialSecret = pwSSH
	if _, err := f.hosts.UpdateHost(ctx, f.actor, host.ID, edit, nil); err != nil {
		t.Fatalf("move host with confirmed password: %v", err)
	}

	// 重置主机密钥：密码凭据必填；私钥凭据不要
	_, err = f.hosts.ResetHostKey(ctx, f.actor, host.ID, "", nil)
	wantCode(t, err, CodeInvalidArgument)
	if _, err := f.hosts.ResetHostKey(ctx, f.actor, host.ID, pwSSH, nil); err != nil {
		t.Fatalf("ResetHostKey with confirmed password: %v", err)
	}
	keyHost := f.createHost(t, hostAddrB, "key-b")
	if _, err := f.hosts.ResetHostKey(ctx, f.actor, keyHost.ID, "", nil); err != nil {
		t.Fatalf("ResetHostKey with key credential: %v", err)
	}

	// 给服务器绑定 SSH：新目标要求
	in := serverInput("manual")
	in.BaseURL = urlHTTPHostA
	srv, err := f.panels.CreateServer(ctx, f.actor, in, nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	bind := HostBindInput{Address: "203.0.113.12", CredentialID: pc}
	_, err = f.hosts.BindServer(ctx, f.actor, srv.ID, bind, nil)
	wantCode(t, err, CodeInvalidArgument)
	bind.CredentialSecret = pwSSH
	if _, err := f.hosts.BindServer(ctx, f.actor, srv.ID, bind, nil); err != nil {
		t.Fatalf("BindServer with confirmed password: %v", err)
	}

	// 审计：记下是否确认过，绝不含密码
	logs, err := f.rbac.ListAuditLogs(ctx, PageQuery{PageSize: 100})
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	confirmed := 0
	for _, l := range logs.Items {
		if bytes.Contains(l.Detail, []byte(pwSSH)) {
			t.Fatalf("audit %s leaks the password: %s", l.Action, l.Detail)
		}
		// jsonb 输出带空格，两种写法都认
		if bytes.Contains(l.Detail, []byte(`"secretConfirmed": true`)) || bytes.Contains(l.Detail, []byte(`"secretConfirmed":true`)) {
			confirmed++
		}
	}
	if confirmed != 4 { // 新建、改端口、重置主机密钥、绑定
		t.Fatalf("audits with secretConfirmed = %d, want 4", confirmed)
	}
}

// 密码凭据的确认输错计入频控：按管理员 5 次锁定，锁定期间正确密码也拒（Ruling-97）。
func TestCredentialSecretConfirmationRateLimited(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	pc := newPasswordCredential(t, f)
	in := HostTestInput{Address: hostAddrA, CredentialID: pc, ProviderType: fakeType, CredentialSecret: "nope"}
	for i := 0; i < defaultConfirmMaxFailures; i++ {
		_, err := f.hosts.TestHost(ctx, f.actor, in)
		wantCode(t, err, CodeInvalidArgument)
	}
	in.CredentialSecret = pwSSH
	_, err := f.hosts.TestHost(ctx, f.actor, in)
	wantCode(t, err, CodeRateLimited)
	f.clock.advance(defaultConfirmWindow + time.Second)
	if _, err := f.hosts.TestHost(ctx, f.actor, in); err != nil {
		t.Fatalf("TestHost after lockout: %v", err)
	}
}

// 连接测试按目标已固定的主机密钥校验（Ruling-107）：同一地址换成冒充主机后，私钥与密码凭据的连接测试都失败（host_key_changed），
// 密码凭据缺密码时根本不拨号；没有主机行的目标照旧首次信任。
func TestTestHostUsesPinnedHostKey(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	pc := newPasswordCredential(t, f)
	f.createHost(t, hostAddrA, "pinned-a")
	wantTask(t, f.runNext(t), HostTaskSucceeded, deploy.ReasonNone)

	// 冒充主机：同一地址，主机密钥变了
	f.runner.HostKey = "SHA256:impostorimpostorimpostorimpostorimpostorimpo"
	dialed := len(f.dialer.Targets())
	_, err := f.hosts.TestHost(ctx, f.actor, HostTestInput{Address: hostAddrA, CredentialID: pc, ProviderType: fakeType})
	wantCode(t, err, CodeInvalidArgument)
	if n := len(f.dialer.Targets()); n != dialed {
		t.Fatalf("must not dial without the password, dialed %d more", n-dialed)
	}
	for _, in := range []HostTestInput{
		{Address: hostAddrA, CredentialID: pc, ProviderType: fakeType, CredentialSecret: pwSSH},
		{Address: hostAddrA, CredentialID: f.credID, ProviderType: fakeType},
	} {
		_, err = f.hosts.TestHost(ctx, f.actor, in)
		var se *Error
		if !errors.As(err, &se) || se.Code != CodeUpstream || !strings.Contains(se.Message, "主机密钥") {
			t.Fatalf("TestHost on an impostor = %v, want upstream host_key_changed", err)
		}
		targets := f.dialer.Targets()
		if got := targets[len(targets)-1].HostKeySHA256; got != hostKeyFP {
			t.Fatalf("test dial pinned %q, want %q", got, hostKeyFP)
		}
		f.clock.advance(time.Minute + time.Second) // 越过连接测试的频控窗口
	}
	// 没有主机行的目标：不带固定密钥
	if _, err := f.hosts.TestHost(ctx, f.actor, HostTestInput{Address: hostAddrB, CredentialID: f.credID, ProviderType: fakeType}); err != nil {
		t.Fatalf("TestHost on a new target: %v", err)
	}
	targets := f.dialer.Targets()
	if got := targets[len(targets)-1].HostKeySHA256; got != "" {
		t.Fatalf("new target pinned %q, want none", got)
	}
}
