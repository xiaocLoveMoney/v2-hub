package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/daotest"
	"v2-hub-server/internal/manager/crypto"
	"v2-hub-server/internal/service"
)

const (
	portalLoginPath = "/api/user/auth/login"
	portalMePath    = "/api/user/auth/me"
	// insertEndUser 直接落一行最小的终端用户（凭据列放占位密文，本组用例不涉及下发）。
	insertEndUser = `INSERT INTO end_user (username, password_hash, email_code, client_cred_enc, is_disabled, status)
VALUES ($1, $2, 'abc123', '\x00', $3, $4)`
)

func newPortalAPIServer(t *testing.T) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	pool := daotest.NewPool(t)
	sealer, err := crypto.NewSealer("KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio=")
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	authCfg := service.AuthConfig{
		SessionIdleTimeout: 2 * time.Hour, SessionMaxAge: 24 * time.Hour, SessionTouchInterval: time.Minute,
		LoginMaxFailures: 5, LoginWindow: 15 * time.Minute, LoginLockout: 15 * time.Minute, LoginMaxTrackedKeys: 100,
	}
	auth, err := service.NewAuthService(pool, authCfg, sealer, nil)
	if err != nil {
		t.Fatalf("NewAuthService: %v", err)
	}
	portal, err := service.NewPortalAuthService(pool, service.PortalAuthConfig{
		SessionIdleTimeout: 2 * time.Hour, SessionMaxAge: 24 * time.Hour, SessionTouchInterval: time.Minute,
		LoginMaxFailures: 5, LoginWindow: 15 * time.Minute, LoginLockout: 15 * time.Minute, LoginMaxTrackedKeys: 100,
		LoginLogRetention: 24 * time.Hour,
	}, sealer, nil)
	if err != nil {
		t.Fatalf("NewPortalAuthService: %v", err)
	}
	settings := service.NewSettingsService(pool, service.SubscriptionConfig{InfoNodes: true}, nil)
	mails := service.NewMailService(pool, sealer, nil, settings, nil)
	srv := httptest.NewServer(NewRouter(Deps{
		Database: pool, PingTimeout: time.Second, Auth: auth, RBAC: service.NewRBACService(pool), PortalAuth: portal,
		Content: service.NewContentService(pool, sealer, settings, mails, nil), Tickets: service.NewTicketService(pool, settings, mails, nil, nil),
		// 本组用例不触碰面板，UserService 不需要 PanelService
		Portal: service.NewUserService(pool, sealer, nil, service.UserConfig{Subscription: service.SubscriptionConfig{InfoNodes: true}}, nil),
		Chain:  service.NewChainService(pool, sealer, nil, service.ChainOptions{}),
		Cookie: CookieConfig{Secure: false}, TrustedProxies: []netip.Prefix{},
	}))
	t.Cleanup(srv.Close)
	return srv, pool
}

func seedEndUser(t *testing.T, pool *pgxpool.Pool, username string, disabled bool) {
	t.Helper()
	hash, err := crypto.HashPassword(cred)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	status := "active"
	if disabled {
		status = "disabled"
	}
	if _, err := pool.Exec(context.Background(), insertEndUser, username, hash, disabled, status); err != nil {
		t.Fatalf("seed end user %s: %v", username, err)
	}
}

func (c *apiClient) portalLogin(username string) userSessionVO {
	c.t.Helper()
	res := c.do(http.MethodPost, portalLoginPath, map[string]string{"username": username, "password": cred})
	if res.Status != http.StatusOK {
		c.t.Fatalf("portal login %s: %d %+v", username, res.Status, res.Body)
	}
	var s userSessionVO
	if err := json.Unmarshal(res.Data, &s); err != nil {
		c.t.Fatalf("decode user session: %v", err)
	}
	c.csrf = s.CSRFToken
	return s
}

func TestPortalAuthEndpoints(t *testing.T) {
	srv, pool := newPortalAPIServer(t)
	seedEndUser(t, pool, "alice", false)
	c := newClient(t, srv.URL)

	expectStatus(t, c.do(http.MethodGet, portalMePath, nil), http.StatusUnauthorized, string(service.CodeUnauthenticated))
	res := c.do(http.MethodPost, portalLoginPath, map[string]string{"username": "alice", "password": "wrong-guess-123"})
	expectStatus(t, res, http.StatusUnauthorized, string(service.CodeUnauthenticated))

	s := c.portalLogin("alice")
	if s.User.Username != "alice" || s.User.Status != "active" || s.CSRFToken == "" {
		t.Fatalf("session = %+v", s)
	}
	expectStatus(t, c.do(http.MethodGet, portalMePath, nil), http.StatusOK, codeOK)
	expectStatus(t, c.do(http.MethodGet, "/api/user/login-logs", nil), http.StatusOK, codeOK)
	// 用户会话 Cookie 只在 /api/user 下有效，不能当管理员会话用
	expectStatus(t, c.do(http.MethodGet, "/api/admin/auth/me", nil), http.StatusUnauthorized, string(service.CodeUnauthenticated))

	// 非 GET 请求必须带 CSRF 头
	token := c.csrf
	c.csrf = ""
	body := map[string]string{"oldPassword": cred, "newPassword": "another-horse-staple"}
	expectStatus(t, c.do(http.MethodPut, "/api/user/auth/password", body), http.StatusForbidden, string(service.CodePermissionDenied))
	c.csrf = token
	expectStatus(t, c.do(http.MethodPut, "/api/user/auth/password", body), http.StatusOK, codeOK)

	expectStatus(t, c.do(http.MethodPost, "/api/user/auth/logout", nil), http.StatusOK, codeOK)
	expectStatus(t, c.do(http.MethodGet, portalMePath, nil), http.StatusUnauthorized, string(service.CodeUnauthenticated))
}

func TestPortalDisabledAccount(t *testing.T) {
	srv, pool := newPortalAPIServer(t)
	seedEndUser(t, pool, "bob", true)
	c := newClient(t, srv.URL)

	s := c.portalLogin("bob")
	if s.User.Status != "disabled" {
		t.Fatalf("status = %q, want disabled", s.User.Status)
	}
	expectStatus(t, c.do(http.MethodGet, portalMePath, nil), http.StatusOK, codeOK)
	expectStatus(t, c.do(http.MethodGet, "/api/user/login-logs", nil), http.StatusForbidden, string(service.CodeAccountDisabled))
	expectStatus(t, c.do(http.MethodPost, "/api/user/auth/totp/setup", nil), http.StatusForbidden, string(service.CodeAccountDisabled))
	expectStatus(t, c.do(http.MethodPost, "/api/user/auth/logout", nil), http.StatusOK, codeOK)
}

func TestSubscriptionEndpoint(t *testing.T) {
	srv, pool := newPortalAPIServer(t)
	seedEndUser(t, pool, "carol", false)
	c := newClient(t, srv.URL)
	c.portalLogin("carol")

	res := c.do(http.MethodGet, "/api/user/subscription", nil)
	expectStatus(t, res, http.StatusOK, codeOK)
	var info subscriptionInfoVO
	if err := json.Unmarshal(res.Data, &info); err != nil {
		t.Fatalf("decode subscription info: %v", err)
	}
	prefix := srv.URL + "/sub/"
	if !strings.HasPrefix(info.URL, prefix) || len(info.Formats) != 4 {
		t.Fatalf("subscription info = %+v", info)
	}
	expectStatus(t, c.do(http.MethodGet, "/api/user/dashboard", nil), http.StatusOK, codeOK)
	expectStatus(t, c.do(http.MethodGet, "/api/user/nodes", nil), http.StatusOK, codeOK)
	expectStatus(t, c.do(http.MethodGet, "/api/user/traffic?hours=0", nil), http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, c.do(http.MethodGet, "/api/user/nodes/1/link", nil), http.StatusNotFound, string(service.CodeNotFound))

	// 订阅端点不需要登录，不走响应信封
	anon := &http.Client{}
	get := func(url string) (*http.Response, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		resp, err := anon.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		defer func() {
			if cerr := resp.Body.Close(); cerr != nil {
				t.Errorf("close body: %v", cerr)
			}
		}()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		return resp, string(body)
	}
	resp, body := get(info.URL)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Subscription-Userinfo") == "" || resp.Header.Get(headerCache) != cacheNoStore ||
		!strings.HasPrefix(resp.Header.Get(headerContent), "text/plain") || body == "" {
		t.Fatalf("GET sub: %d headers=%v body=%q", resp.StatusCode, resp.Header, body)
	}
	if resp, _ := get(info.URL + "?format=mihomo"); resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get(headerContent), "application/yaml") {
		t.Fatalf("mihomo: %d %q", resp.StatusCode, resp.Header.Get(headerContent))
	}
	if resp, body := get(info.URL + "?format=surge"); resp.StatusCode != http.StatusBadRequest || body != string(service.CodeInvalidArgument) {
		t.Fatalf("bad format: %d %q", resp.StatusCode, body)
	}
	if resp, body := get(prefix + "unknown-token-0000000000000000"); resp.StatusCode != http.StatusNotFound || body != string(service.CodeNotFound) {
		t.Fatalf("unknown token: %d %q", resp.StatusCode, body)
	}
	if resp, _ := get(prefix + "short"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("short token: %d", resp.StatusCode)
	}

	// 本人重置后旧地址失效
	res = c.do(http.MethodPost, "/api/user/subscription/reset", nil)
	expectStatus(t, res, http.StatusOK, codeOK)
	if resp, _ := get(info.URL); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("old url after reset: %d", resp.StatusCode)
	}
	res = c.do(http.MethodGet, "/api/user/subscribe-logs", nil)
	expectStatus(t, res, http.StatusOK, codeOK)
	var page subscribeLogPageVO
	if err := json.Unmarshal(res.Data, &page); err != nil || page.Total != 2 {
		t.Fatalf("subscribe logs = %+v, %v; want the 2 successful pulls", page, err)
	}
}
