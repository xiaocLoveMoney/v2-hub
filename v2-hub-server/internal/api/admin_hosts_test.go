package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"v2-hub-server/internal/service"
)

const (
	sshCredentialsPath = "/api/admin/ssh-credentials"
	serverHostsPath    = "/api/admin/server-hosts"
	testHostAddress    = "203.0.113.20"
)

func apiTestKey(t *testing.T) string {
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

// SSH 托管接口走 HTTP（docs/03 §5.22）：凭据只写不读、host:read 能看不能改、没有 host 权限的管理员碰不到、
// CSRF、连接测试、不存在的资源 404。部署与动作本身由 service 层的假执行器用例覆盖。
func TestHostEndpoints(t *testing.T) {
	srv, rbac := newAPIServer(t)
	if _, err := rbac.BootstrapSuperAdmin(context.Background(), "root", cred); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	root := newClient(t, srv.URL)
	root.login("root")
	key := apiTestKey(t)

	// 新建凭据：响应里没有私钥，也没有 secret / passphrase 字段
	expectStatus(t, root.do(http.MethodPost, sshCredentialsPath, map[string]any{"name": "k", "username": "root", "authType": "key", "secret": "garbage"}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))
	created := root.do(http.MethodPost, sshCredentialsPath, map[string]any{"name": "k", "username": "root", "authType": "key", "secret": key, "remark": "测试"})
	expectStatus(t, created, http.StatusOK, codeOK)
	var c sshCredentialVO
	if err := json.Unmarshal(created.Data, &c); err != nil || !strings.HasPrefix(c.KeyFingerprint, "SHA256:") || c.HostCount != 0 {
		t.Fatalf("credential = %+v, %v", c, err)
	}
	list := root.do(http.MethodGet, sshCredentialsPath, nil)
	expectStatus(t, list, http.StatusOK, codeOK)
	for _, raw := range []string{string(created.Data), string(list.Data)} {
		if strings.Contains(raw, "PRIVATE KEY") || strings.Contains(raw, "secret") || strings.Contains(raw, "passphrase") {
			t.Fatalf("credential response must not carry the secret: %s", raw)
		}
	}
	credPath := fmt.Sprintf("%s/%d", sshCredentialsPath, c.ID)
	expectStatus(t, root.do(http.MethodPost, sshCredentialsPath, map[string]any{"name": "k", "username": "root", "authType": "key", "secret": key}),
		http.StatusConflict, string(service.CodeConflict))
	// 留空沿用原值
	expectStatus(t, root.do(http.MethodPut, credPath, map[string]any{"name": "k2", "username": "root", "authType": "key"}), http.StatusOK, codeOK)
	expectStatus(t, root.do(http.MethodPut, sshCredentialsPath+"/999999", map[string]any{"name": "x", "username": "root", "authType": "key", "secret": key}),
		http.StatusNotFound, string(service.CodeNotFound))

	// 连接测试：返回主机密钥指纹与检测结果；回环地址 400
	test := root.do(http.MethodPost, serverHostsPath+"/test", map[string]any{"address": testHostAddress, "credentialId": c.ID, "providerType": testDeployType})
	expectStatus(t, test, http.StatusOK, codeOK)
	var det hostDetectionVO
	if err := json.Unmarshal(test.Data, &det); err != nil || det.HostKeySHA256 != testHostKey || det.Plan != "install" || det.PanelState != "none" || !det.IsPrivileged {
		t.Fatalf("detection = %+v, %v", det, err)
	}
	expectStatus(t, root.do(http.MethodPost, serverHostsPath+"/test", map[string]any{"address": "127.0.0.1", "credentialId": c.ID, "providerType": testDeployType}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, root.do(http.MethodPost, serverHostsPath+"/test", map[string]any{"address": testHostAddress, "credentialId": 999999, "providerType": testDeployType}),
		http.StatusNotFound, string(service.CodeNotFound))

	// 主机：空列表输出 []；这套用例的面板注册表是空的，建主机因面板类型不支持被拒；不存在的 404
	hosts := root.do(http.MethodGet, serverHostsPath, nil)
	expectStatus(t, hosts, http.StatusOK, codeOK)
	if string(hosts.Data) != "[]" {
		t.Fatalf("empty host list = %s", hosts.Data)
	}
	batch := map[string]any{"credentialId": c.ID, "providerType": testDeployType, "hosts": []map[string]any{{"address": testHostAddress, "name": "node"}}}
	expectStatus(t, root.do(http.MethodPost, serverHostsPath, batch), http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, root.do(http.MethodPost, serverHostsPath, map[string]any{"credentialId": c.ID, "providerType": testDeployType, "hosts": []any{}, "extra": 1}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))
	for _, path := range []string{serverHostsPath + "/999999", serverHostsPath + "/999999/tasks", "/api/admin/host-tasks/999999"} {
		expectStatus(t, root.do(http.MethodGet, path, nil), http.StatusNotFound, string(service.CodeNotFound))
	}
	expectStatus(t, root.do(http.MethodPost, serverHostsPath+"/999999/actions", map[string]any{"action": "diagnose"}), http.StatusNotFound, string(service.CodeNotFound))
	expectStatus(t, root.do(http.MethodPost, serverHostsPath+"/999999/actions", map[string]any{"action": "shell"}), http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, root.do(http.MethodPost, serverHostsPath+"/999999/panel-login/reset", nil), http.StatusNotFound, string(service.CodeNotFound))
	expectStatus(t, root.do(http.MethodPost, "/api/admin/host-tasks/999999/cancel", nil), http.StatusNotFound, string(service.CodeNotFound))
	expectStatus(t, root.do(http.MethodPut, "/api/admin/servers/999999/host", map[string]any{"address": testHostAddress, "credentialId": c.ID}),
		http.StatusNotFound, string(service.CodeNotFound))

	// host:read 能看不能改；server:write 不等于 host:write（Ruling-68）
	for code, perms := range map[string][]string{"host_viewer": {service.PermHostRead}, "server_admin": {service.PermServerRead, service.PermServerWrite}} {
		expectStatus(t, root.do(http.MethodPost, "/api/admin/roles", map[string]any{"code": code, "name": code, "permissions": perms}), http.StatusOK, codeOK)
		expectStatus(t, root.do(http.MethodPost, "/api/admin/admins", map[string]any{"username": code, "password": cred, "roles": []string{code}}), http.StatusOK, codeOK)
	}
	viewer := newClient(t, srv.URL)
	viewer.login("host_viewer")
	expectStatus(t, viewer.do(http.MethodGet, sshCredentialsPath, nil), http.StatusOK, codeOK)
	expectStatus(t, viewer.do(http.MethodGet, serverHostsPath, nil), http.StatusOK, codeOK)
	denied := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, sshCredentialsPath, map[string]any{"name": "v", "username": "root", "authType": "key", "secret": key}},
		{http.MethodPut, credPath, map[string]any{"name": "v", "username": "root", "authType": "key"}},
		{http.MethodDelete, credPath, nil},
		{http.MethodPost, serverHostsPath, batch},
		{http.MethodPost, serverHostsPath + "/test", map[string]any{"address": testHostAddress, "credentialId": c.ID, "providerType": testDeployType}},
		{http.MethodPost, serverHostsPath + "/1/actions", map[string]any{"action": "restart_panel"}},
		{http.MethodPost, serverHostsPath + "/1/panel-login/reset", nil},
		{http.MethodPost, serverHostsPath + "/1/reset-host-key", nil},
		{http.MethodPost, "/api/admin/host-tasks/1/cancel", nil},
		{http.MethodPut, "/api/admin/servers/1/host", map[string]any{"address": testHostAddress, "credentialId": c.ID}},
	}
	for _, d := range denied {
		expectStatus(t, viewer.do(d.method, d.path, d.body), http.StatusForbidden, string(service.CodePermissionDenied))
	}
	serverAdmin := newClient(t, srv.URL)
	serverAdmin.login("server_admin")
	expectStatus(t, serverAdmin.do(http.MethodGet, sshCredentialsPath, nil), http.StatusForbidden, string(service.CodePermissionDenied))
	expectStatus(t, serverAdmin.do(http.MethodGet, serverHostsPath, nil), http.StatusForbidden, string(service.CodePermissionDenied))
	expectStatus(t, serverAdmin.do(http.MethodPost, serverHostsPath, batch), http.StatusForbidden, string(service.CodePermissionDenied))

	// 不带 CSRF 的写请求被拦下；未登录 401
	noCSRF := newClient(t, srv.URL)
	noCSRF.login("root")
	noCSRF.csrf = ""
	expectStatus(t, noCSRF.do(http.MethodDelete, credPath, nil), http.StatusForbidden, string(service.CodePermissionDenied))
	expectStatus(t, newClient(t, srv.URL).do(http.MethodGet, serverHostsPath, nil), http.StatusUnauthorized, string(service.CodeUnauthenticated))

	expectStatus(t, root.do(http.MethodDelete, credPath, nil), http.StatusOK, codeOK)
	expectStatus(t, root.do(http.MethodDelete, credPath, nil), http.StatusNotFound, string(service.CodeNotFound))
}

const dnsAccountsPath = "/api/admin/dns-accounts"

// DNS 账号接口走 HTTP（docs/03 §5.22，Ruling-72）：令牌只写不读、host:read 能看不能改、没有 host 权限的管理员碰不到。
func TestDNSAccountEndpoints(t *testing.T) {
	srv, rbac := newAPIServer(t)
	if _, err := rbac.BootstrapSuperAdmin(context.Background(), "root", cred); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	root := newClient(t, srv.URL)
	root.login("root")
	const apiValue = "cf-api-value-0123456789"
	body := map[string]any{"name": "cf-main", "providerType": testDNSType, "zone": "Example.COM", "token": apiValue, "remark": "主域名"}

	expectStatus(t, root.do(http.MethodPost, dnsAccountsPath, map[string]any{"name": "x", "providerType": "nope", "zone": "example.com", "token": apiValue}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, root.do(http.MethodPost, dnsAccountsPath, map[string]any{"name": "x", "providerType": testDNSType, "zone": "example.com"}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))
	created := root.do(http.MethodPost, dnsAccountsPath, body)
	expectStatus(t, created, http.StatusOK, codeOK)
	var a dnsAccountVO
	if err := json.Unmarshal(created.Data, &a); err != nil || a.Zone != "example.com" || a.ProviderType != testDNSType || a.HostCount != 0 {
		t.Fatalf("dns account = %+v, %v", a, err)
	}
	list := root.do(http.MethodGet, dnsAccountsPath, nil)
	expectStatus(t, list, http.StatusOK, codeOK)
	for _, raw := range []string{string(created.Data), string(list.Data)} {
		if strings.Contains(raw, apiValue) || strings.Contains(raw, "token") {
			t.Fatalf("dns account response must not carry the token: %s", raw)
		}
	}
	accountPath := fmt.Sprintf("%s/%d", dnsAccountsPath, a.ID)
	expectStatus(t, root.do(http.MethodPost, dnsAccountsPath, body), http.StatusConflict, string(service.CodeConflict))
	// 留空沿用原令牌
	expectStatus(t, root.do(http.MethodPut, accountPath, map[string]any{"name": "cf-2", "providerType": testDNSType, "zone": "example.com"}), http.StatusOK, codeOK)
	expectStatus(t, root.do(http.MethodPut, dnsAccountsPath+"/999999", body), http.StatusNotFound, string(service.CodeNotFound))

	for code, perms := range map[string][]string{"host_viewer": {service.PermHostRead}, "server_admin": {service.PermServerRead, service.PermServerWrite}} {
		expectStatus(t, root.do(http.MethodPost, "/api/admin/roles", map[string]any{"code": code, "name": code, "permissions": perms}), http.StatusOK, codeOK)
		expectStatus(t, root.do(http.MethodPost, "/api/admin/admins", map[string]any{"username": code, "password": cred, "roles": []string{code}}), http.StatusOK, codeOK)
	}
	viewer := newClient(t, srv.URL)
	viewer.login("host_viewer")
	expectStatus(t, viewer.do(http.MethodGet, dnsAccountsPath, nil), http.StatusOK, codeOK)
	expectStatus(t, viewer.do(http.MethodPost, dnsAccountsPath, body), http.StatusForbidden, string(service.CodePermissionDenied))
	expectStatus(t, viewer.do(http.MethodPut, accountPath, body), http.StatusForbidden, string(service.CodePermissionDenied))
	expectStatus(t, viewer.do(http.MethodDelete, accountPath, nil), http.StatusForbidden, string(service.CodePermissionDenied))
	serverAdmin := newClient(t, srv.URL)
	serverAdmin.login("server_admin")
	expectStatus(t, serverAdmin.do(http.MethodGet, dnsAccountsPath, nil), http.StatusForbidden, string(service.CodePermissionDenied))

	expectStatus(t, root.do(http.MethodDelete, accountPath, nil), http.StatusOK, codeOK)
	expectStatus(t, root.do(http.MethodDelete, accountPath, nil), http.StatusNotFound, string(service.CodeNotFound))
}
