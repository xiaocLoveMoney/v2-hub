package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
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
	srv := httptest.NewServer(NewRouter(Deps{
		Database: pool, PingTimeout: time.Second, Auth: auth, RBAC: service.NewRBACService(pool), PortalAuth: portal,
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
