package api

import (
	"bytes"
	"context"
	"encoding/base32"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/daotest"
	"v2-hub-server/internal/manager/crypto"
	"v2-hub-server/internal/manager/deploy"
	"v2-hub-server/internal/manager/deploy/deploytest"
	"v2-hub-server/internal/manager/dns"
	"v2-hub-server/internal/manager/dns/dnstest"
	"v2-hub-server/internal/manager/panel"
	"v2-hub-server/internal/manager/pay"
	"v2-hub-server/internal/manager/pay/paytest"
	"v2-hub-server/internal/manager/remote/remotetest"
	"v2-hub-server/internal/manager/totp"
	"v2-hub-server/internal/service"
)

const (
	cred = "correct-horse-battery"
	// testDeployType 是接口用例里注册的假部署类型；面板注册表里没有它，所以建主机会被拒，连接测试可用。
	testDeployType = "fake"
	// testDNSType 是接口用例里注册的假 DNS 服务商，令牌校验一律通过。
	testDNSType = "fakedns"
	testHostKey = "SHA256:apitesthostkeyapitesthostkeyapitesthostkey0"
)

type apiClient struct {
	t    *testing.T
	base string
	http *http.Client
	csrf string
	// clock 是该测试服务器上认证服务的时钟，用来算 TOTP 验证码、推进步长
	clock *apiClock
}

// apiClock 是接口用例里认证服务的时钟：真实时间加一个可推进的偏移，跨 TOTP 步长防重放时推进它而不是 sleep。
type apiClock struct{ offset atomic.Int64 }

func (c *apiClock) now() time.Time { return time.Now().Add(time.Duration(c.offset.Load())) }

func (c *apiClock) advance(d time.Duration) { c.offset.Add(int64(d)) }

// apiClocks 按测试服务器地址登记时钟；apiTOTPSeeds 按“服务器地址 + 用户名”记下 loginSuper 绑定的 TOTP 密钥。
var (
	apiClocks    sync.Map // string → *apiClock
	apiTOTPSeeds sync.Map // string → []byte
)

// testReauthWindow 是接口用例里的二次验证有效期（同生产默认值）。
const testReauthWindow = 5 * time.Minute

// testAgentWriteRate 是接口用例里代理写操作的每小时上限：放得很宽，越权矩阵不会被频控打断；频控另有专门用例。
const testAgentWriteRate = 100000

type apiResult struct {
	Status int
	Body   envelope
	Data   json.RawMessage
}

func newAPIServer(t *testing.T) (*httptest.Server, *service.RBACService) {
	t.Helper()
	srv, rbac, _ := newAPIServerWithPool(t)
	return srv, rbac
}

// newAPIServerWithPool 同 newAPIServer，另返回连接池，供需要直接核对库里数据的用例（如代理的越权矩阵）使用。
func newAPIServerWithPool(t *testing.T) (*httptest.Server, *service.RBACService, *pgxpool.Pool) {
	t.Helper()
	pool := daotest.NewPool(t)
	sealer, err := crypto.NewSealer(testSealerKey)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	clock := &apiClock{}
	auth, err := service.NewAuthService(pool, service.AuthConfig{
		SessionIdleTimeout: 2 * time.Hour, SessionMaxAge: 24 * time.Hour, SessionTouchInterval: time.Minute,
		LoginMaxFailures: 5, LoginWindow: 15 * time.Minute, LoginLockout: 15 * time.Minute, LoginMaxTrackedKeys: 100,
		ReauthWindow: testReauthWindow,
	}, sealer, clock.now)
	if err != nil {
		t.Fatalf("NewAuthService: %v", err)
	}
	rbac := service.NewRBACService(pool)
	// 不接 SMTP：邮件相关接口应整体返回 feature_disabled（Ruling-24）
	settings := service.NewSettingsService(pool, service.SubscriptionConfig{}, nil)
	mails := service.NewMailService(pool, sealer, nil, settings, nil)
	portal, err := service.NewPortalAuthService(pool, service.PortalAuthConfig{
		SessionIdleTimeout: 2 * time.Hour, SessionMaxAge: 24 * time.Hour, SessionTouchInterval: time.Minute,
		LoginMaxFailures: 5, LoginWindow: 15 * time.Minute, LoginLockout: 15 * time.Minute, LoginMaxTrackedKeys: 100,
		LoginLogRetention: 24 * time.Hour,
	}, sealer, nil)
	if err != nil {
		t.Fatalf("NewPortalAuthService: %v", err)
	}
	// 本组用例不触碰面板，UserService 不需要 PanelService
	users := service.NewUserService(pool, sealer, nil, service.UserConfig{Settings: settings}, nil)
	account := service.NewAccountService(pool, sealer, service.AccountDeps{Settings: settings, Mail: mails, Users: users, Portal: portal}, nil)
	shop := service.NewShopService(pool, settings, users, mails, nil)
	// 空注册表：没有任何面板类型支持改内核配置，服务端链式的下发接口只读数据库
	panels := service.NewPanelService(pool, sealer, panel.NewRegistry(map[string]panel.Factory{}), service.PanelConfig{RequestTimeout: time.Second}, nil)
	applier := service.NewChainApplier(pool, sealer, users, panels, service.ChainApplyConfig{}, nil)
	chains := service.NewChainService(pool, sealer, applier, service.ChainOptions{})
	// 没配 Telegram token：测试消息接口返回 feature_disabled
	telegramSvc := service.NewTelegramService(pool, nil, settings, nil)
	ops := service.NewOpsService(pool, settings, mails, telegramSvc, nil)
	// 不接探测服务：大陆连通检测只能看结论，手动检测返回 upstream_unavailable
	reachSvc := service.NewReachService(pool, settings, ops, nil, service.ReachOptions{})
	// SSH 托管：假拨号器 + 假部署实现，接口用例不发真实 SSH（docs/03 §5.22）
	hosts := service.NewHostService(pool, sealer, service.HostDeps{
		Dialer: &remotetest.Dialer{Runner: &remotetest.Runner{Privileged: true, HostKey: testHostKey}},
		Deployers: deploy.NewRegistry(map[string]deploy.Deployer{testDeployType: &deploytest.Fake{
			Detection: deploy.Detection{OSID: "debian", Arch: "x86_64", IsPrivileged: true, PanelState: deploy.PanelNone, Plan: deploy.PlanInstall},
		}}),
		DNS: dns.NewRegistry(map[string]dns.Factory{testDNSType: dnstest.New().Factory()}), Panels: panels,
	}, service.HostConfig{
		CommandTimeout: time.Second, InstallTimeout: time.Second, TestLimit: 10, TestWindow: time.Minute,
		ActionLimit: 10, ActionWindow: time.Minute, MaxBatch: 5, PanelWaitAttempts: 1, PanelWaitInterval: time.Millisecond,
	})
	tickets := service.NewTicketService(pool, settings, mails, nil, nil)
	agents := service.NewAgentService(pool, users, tickets, service.AgentServiceConfig{WriteRatePerHour: testAgentWriteRate})
	srv := httptest.NewServer(NewRouter(Deps{
		Database: pool, PingTimeout: time.Second, Auth: auth, RBAC: rbac, Hosts: hosts, Agents: agents,
		Settings: settings, Mail: mails, Account: account, Users: users, PortalAuth: portal, Portal: users,
		Content: service.NewContentService(pool, sealer, settings, mails, nil), Tickets: tickets,
		Wallet: service.NewWalletService(pool, sealer, settings, nil), Shop: shop, Finance: shop, Pay: service.NewPayService(pool, settings, testGateways(t), nil, nil),
		Chain: chains, ChainAdmin: chains, ChainApply: applier,
		Telegram: telegramSvc, Ops: ops,
		Analytics: service.NewAnalyticsService(pool, settings, reachSvc, 0, nil), Reach: reachSvc, Panels: panels,
		Cookie: CookieConfig{Secure: false}, TrustedProxies: []netip.Prefix{},
	}))
	t.Cleanup(srv.Close)
	apiClocks.Store(srv.URL, clock)
	t.Cleanup(func() { apiClocks.Delete(srv.URL) })
	return srv, rbac, pool
}

func newClient(t *testing.T, base string) *apiClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	c := &apiClient{t: t, base: base, http: &http.Client{Jar: jar}, clock: &apiClock{}}
	if v, ok := apiClocks.Load(base); ok {
		c.clock = v.(*apiClock)
	}
	return c
}

func (c *apiClient) do(method, path string, body any) apiResult {
	c.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			c.t.Fatalf("encode body: %v", err)
		}
	}
	req, err := http.NewRequestWithContext(context.Background(), method, c.base+path, &buf)
	if err != nil {
		c.t.Fatalf("new request: %v", err)
	}
	req.Header.Set(headerContent, contentTypeJSON)
	if c.csrf != "" {
		req.Header.Set(headerCSRF, c.csrf)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			c.t.Errorf("close body: %v", cerr)
		}
	}()
	var raw struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		c.t.Fatalf("%s %s decode: %v", method, path, err)
	}
	return apiResult{Status: resp.StatusCode, Body: envelope{Code: raw.Code, Message: raw.Message}, Data: raw.Data}
}

func (c *apiClient) login(username string) sessionVO {
	c.t.Helper()
	res := c.do(http.MethodPost, "/api/admin/auth/login", map[string]string{"username": username, "password": cred})
	if res.Status != http.StatusOK {
		c.t.Fatalf("login %s: %d %+v", username, res.Status, res.Body)
	}
	var s sessionVO
	if err := json.Unmarshal(res.Data, &s); err != nil {
		c.t.Fatalf("decode session: %v", err)
	}
	c.csrf = s.CSRFToken
	return s
}

// loginSuper 以超管身份登录并保证已开启两步验证（R10）：第一次登录后经 HTTP 走完 setup → enable
// （开启成功即视为刚做过二次验证），之后的登录把时钟推进一个步长再带验证码，避开防重放。
func (c *apiClient) loginSuper(username string) sessionVO {
	c.t.Helper()
	key := c.base + "|" + username
	if v, ok := apiTOTPSeeds.Load(key); ok {
		c.clock.advance(totp.Period)
		code := totp.CodeAt(v.([]byte), totp.Step(c.clock.now()))
		res := c.do(http.MethodPost, "/api/admin/auth/login", map[string]string{"username": username, "password": cred, "totpCode": code})
		return c.adoptSession(res, username)
	}
	c.login(username)
	seed := c.bindTOTP()
	apiTOTPSeeds.Store(key, seed)
	return c.me()
}

// bindTOTP 为当前会话的管理员开启两步验证，返回原始密钥。
func (c *apiClient) bindTOTP() []byte {
	c.t.Helper()
	setup := c.do(http.MethodPost, "/api/admin/auth/totp/setup", nil)
	expectStatus(c.t, setup, http.StatusOK, codeOK)
	var vo totpSetupVO
	if err := json.Unmarshal(setup.Data, &vo); err != nil {
		c.t.Fatalf("decode totp setup: %v", err)
	}
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(vo.Secret)
	if err != nil {
		c.t.Fatalf("decode totp key: %v", err)
	}
	code := totp.CodeAt(raw, totp.Step(c.clock.now()))
	expectStatus(c.t, c.do(http.MethodPost, "/api/admin/auth/totp/enable", map[string]string{"code": code}), http.StatusOK, codeOK)
	return raw
}

// me 取当前会话信息。
func (c *apiClient) me() sessionVO {
	c.t.Helper()
	return c.adoptSession(c.do(http.MethodGet, "/api/admin/auth/me", nil), "me")
}

// adoptSession 校验登录 / me 的响应并记下 CSRF token。
func (c *apiClient) adoptSession(res apiResult, who string) sessionVO {
	c.t.Helper()
	if res.Status != http.StatusOK {
		c.t.Fatalf("session %s: %d %+v", who, res.Status, res.Body)
	}
	var s sessionVO
	if err := json.Unmarshal(res.Data, &s); err != nil {
		c.t.Fatalf("decode session: %v", err)
	}
	c.csrf = s.CSRFToken
	return s
}

// bootstrapRoot 用命令行同款入口建出超管 root（它是所有者），返回已登录并开启两步验证的客户端。
func bootstrapRoot(t *testing.T, srv *httptest.Server, rbac *service.RBACService) *apiClient {
	t.Helper()
	if _, err := rbac.BootstrapSuperAdmin(context.Background(), "root", cred); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	root := newClient(t, srv.URL)
	root.loginSuper("root")
	return root
}

func expectStatus(t *testing.T, res apiResult, want int, wantCode string) {
	t.Helper()
	if res.Status != want || res.Body.Code != wantCode {
		t.Fatalf("got %d %q (%s), want %d %q", res.Status, res.Body.Code, res.Body.Message, want, wantCode)
	}
}

func TestAdminAuthFlow(t *testing.T) {
	srv, rbac := newAPIServer(t)
	if _, err := rbac.BootstrapSuperAdmin(context.Background(), "root", cred); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	c := newClient(t, srv.URL)

	expectStatus(t, c.do(http.MethodGet, "/api/admin/auth/me", nil), http.StatusUnauthorized, string(service.CodeUnauthenticated))
	expectStatus(t, c.do(http.MethodPost, "/api/admin/auth/login", map[string]string{"username": "root", "password": "nope"}),
		http.StatusUnauthorized, string(service.CodeUnauthenticated))
	expectStatus(t, c.do(http.MethodPost, "/api/admin/auth/login", map[string]any{"username": "root", "extra": 1}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))

	s := c.loginSuper("root")
	if len(s.Permissions) != len(service.AllPermissions) {
		t.Fatalf("super admin permissions = %v", s.Permissions)
	}
	expectStatus(t, c.do(http.MethodGet, "/api/admin/auth/me", nil), http.StatusOK, codeOK)

	// CSRF：缺头或错头的写请求被拒
	csrf := c.csrf
	c.csrf = ""
	expectStatus(t, c.do(http.MethodPost, "/api/admin/auth/logout", nil), http.StatusForbidden, string(service.CodePermissionDenied))
	c.csrf = "forged"
	expectStatus(t, c.do(http.MethodPost, "/api/admin/auth/logout", nil), http.StatusForbidden, string(service.CodePermissionDenied))
	c.csrf = csrf

	expectStatus(t, c.do(http.MethodPost, "/api/admin/auth/logout", nil), http.StatusOK, codeOK)
	expectStatus(t, c.do(http.MethodGet, "/api/admin/auth/me", nil), http.StatusUnauthorized, string(service.CodeUnauthenticated))
}

func TestAdminRBACEndpoints(t *testing.T) {
	srv, rbac := newAPIServer(t)
	if _, err := rbac.BootstrapSuperAdmin(context.Background(), "root", cred); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	root := newClient(t, srv.URL)
	root.loginSuper("root")

	expectStatus(t, root.do(http.MethodPost, "/api/admin/roles",
		map[string]any{"code": "viewer", "name": "只读", "permissions": []string{service.PermNodeRead}}), http.StatusOK, codeOK)
	expectStatus(t, root.do(http.MethodPost, "/api/admin/admins",
		map[string]any{"username": "viewer1", "password": cred, "roles": []string{"viewer"}}), http.StatusOK, codeOK)
	expectStatus(t, root.do(http.MethodPost, "/api/admin/admins",
		map[string]any{"username": "viewer1", "password": cred, "roles": []string{}}), http.StatusConflict, string(service.CodeConflict))

	list := root.do(http.MethodGet, "/api/admin/admins?page=1&pageSize=10", nil)
	expectStatus(t, list, http.StatusOK, codeOK)
	var page pageVO[adminVO]
	if err := json.Unmarshal(list.Data, &page); err != nil || page.Total != 2 {
		t.Fatalf("admins page = %+v, %v", page, err)
	}
	expectStatus(t, root.do(http.MethodGet, "/api/admin/admins?pageSize=500", nil), http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, root.do(http.MethodPut, "/api/admin/admins/abc", map[string]any{"roles": []string{}, "isDisabled": false}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, root.do(http.MethodPut, "/api/admin/admins/9999", map[string]any{"roles": []string{}, "isDisabled": false}),
		http.StatusNotFound, string(service.CodeNotFound))

	// viewer1 只有 node:read：管理员与审计接口都应 403（R1）
	viewer := newClient(t, srv.URL)
	vs := viewer.login("viewer1")
	if len(vs.Permissions) != 1 || vs.Permissions[0] != service.PermNodeRead {
		t.Fatalf("viewer permissions = %v", vs.Permissions)
	}
	expectStatus(t, viewer.do(http.MethodGet, "/api/admin/admins", nil), http.StatusForbidden, string(service.CodePermissionDenied))
	expectStatus(t, viewer.do(http.MethodGet, "/api/admin/audit-logs", nil), http.StatusForbidden, string(service.CodePermissionDenied))

	logs := root.do(http.MethodGet, "/api/admin/audit-logs?pageSize=50", nil)
	expectStatus(t, logs, http.StatusOK, codeOK)
	var auditPage pageVO[auditLogVO]
	if err := json.Unmarshal(logs.Data, &auditPage); err != nil || auditPage.Total == 0 {
		t.Fatalf("audit page = %+v, %v", auditPage, err)
	}
}

func TestAdminTOTPEndpoints(t *testing.T) {
	srv, rbac := newAPIServer(t)
	root := bootstrapRoot(t, srv, rbac)
	expectStatus(t, root.do(http.MethodPost, "/api/admin/admins",
		map[string]any{"username": "agent", "password": cred, "roles": []string{service.RoleOperator}}), http.StatusOK, codeOK)
	c := newClient(t, srv.URL)
	if s := c.login("agent"); s.TOTPEnabled || s.TOTPSetupRequired {
		t.Fatalf("non-super admin without totp = %+v", s)
	}

	setup := c.do(http.MethodPost, "/api/admin/auth/totp/setup", nil)
	expectStatus(t, setup, http.StatusOK, codeOK)
	var vo totpSetupVO
	if err := json.Unmarshal(setup.Data, &vo); err != nil || vo.Secret == "" || vo.OtpauthURI == "" {
		t.Fatalf("setup = %+v, %v", vo, err)
	}
	expectStatus(t, c.do(http.MethodPost, "/api/admin/auth/totp/enable", map[string]string{"code": "abc"}),
		http.StatusBadRequest, string(service.CodeInvalidArgument))
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(vo.Secret)
	if err != nil {
		t.Fatalf("decode key: %v", err)
	}
	code := totp.CodeAt(raw, totp.Step(c.clock.now()))
	expectStatus(t, c.do(http.MethodPost, "/api/admin/auth/totp/enable", map[string]string{"code": code}), http.StatusOK, codeOK)

	// 新会话：密码正确但缺验证码 → 401 totp_required
	other := newClient(t, srv.URL)
	expectStatus(t, other.do(http.MethodPost, "/api/admin/auth/login", map[string]string{"username": "agent", "password": cred}),
		http.StatusUnauthorized, string(service.CodeTOTPRequired))
	if s := c.me(); !s.TOTPEnabled {
		t.Fatalf("me after enable = %+v", s)
	}
	// 非超管可以关闭自己的两步验证（超管不行，见 TestSuperAdminMustBindTOTP）
	c.clock.advance(totp.Period)
	code = totp.CodeAt(raw, totp.Step(c.clock.now()))
	expectStatus(t, c.do(http.MethodPost, "/api/admin/auth/totp/disable", map[string]string{"code": code}), http.StatusOK, codeOK)
}

func TestClientIP(t *testing.T) {
	trusted, err := ParseTrustedProxies([]string{"172.16.0.0/12"})
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}
	tests := []struct {
		name   string
		remote string
		xff    []string
		want   string
	}{
		{"direct client ignores xff", "203.0.113.9:5000", []string{"1.2.3.4"}, "203.0.113.9"},
		{"trusted proxy uses last xff", "172.18.0.5:5000", []string{"1.2.3.4, 198.51.100.7"}, "198.51.100.7"},
		{"trusted proxy without xff", "172.18.0.5:5000", nil, "172.18.0.5"},
		{"trusted proxy bad xff", "172.18.0.5:5000", []string{"garbage"}, "172.18.0.5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remote
			for _, v := range tt.xff {
				r.Header.Add(headerForwardedFor, v)
			}
			got := clientIP(r, trusted)
			if got == nil || got.String() != tt.want {
				t.Fatalf("clientIP = %v, want %s", got, tt.want)
			}
		})
	}
}

// testGateways 返回只有内存假网关的注册表，接口测试用它走通在线充值与回调。
func testGateways(t *testing.T) *pay.Registry {
	t.Helper()
	r, err := pay.NewRegistry(&paytest.Fake{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return r
}
