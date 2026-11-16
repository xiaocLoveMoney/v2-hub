package acmecert

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

const testDomain = "node.example.com"

// fakeCA 是进程内的 ACME 服务端：只实现一张单域名订单走 DNS-01 所需的接口，不校验 JWS 签名。
// 收到“开始验证”时去 txt 里查验证值，对了才放行，所以能验出客户端算的值与写的名字是否正确。
type fakeCA struct {
	t      *testing.T
	srv    *httptest.Server
	caKey  *ecdsa.PrivateKey
	caCert *x509.Certificate
	txt    *txtStore

	mu          sync.Mutex
	thumbprint  string
	accounts    int
	registered  bool
	authzStatus string
	orderStatus string
	leafPEM     []byte
	rateLimited bool
}

type txtStore struct {
	mu      sync.Mutex
	values  map[string]string
	removed int
}

func (s *txtStore) get(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[name]
}

const (
	testToken   = "tok-abc"
	statusValid = "valid"
)

func newFakeCA(t *testing.T) *fakeCA {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake acme ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("ca cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse ca cert: %v", err)
	}
	ca := &fakeCA{
		t: t, caKey: caKey, caCert: caCert, txt: &txtStore{values: map[string]string{}},
		authzStatus: "pending", orderStatus: "pending",
	}
	ca.srv = httptest.NewServer(http.HandlerFunc(ca.serve))
	t.Cleanup(ca.srv.Close)
	return ca
}

func (ca *fakeCA) url(path string) string { return ca.srv.URL + path }

type jws struct {
	Protected string `json:"protected"`
	Payload   string `json:"payload"`
}

func decodeSegment(t *testing.T, segment string, out any) {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		t.Errorf("decode jws segment: %v", err)
		return
	}
	if len(raw) == 0 {
		return
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Errorf("unmarshal jws segment: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (ca *fakeCA) order() map[string]any {
	o := map[string]any{
		"status":         ca.orderStatus,
		"identifiers":    []map[string]string{{"type": "dns", "value": testDomain}},
		"authorizations": []string{ca.url("/authz/1")},
		"finalize":       ca.url("/order/1/finalize"),
	}
	if ca.orderStatus == statusValid {
		o["certificate"] = ca.url("/cert/1")
	}
	return o
}

func (ca *fakeCA) challenge(status string) map[string]any {
	return map[string]any{"type": "dns-01", "url": ca.url("/chal/1"), "token": testToken, "status": status}
}

func (ca *fakeCA) serve(w http.ResponseWriter, r *http.Request) {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	w.Header().Set("Replay-Nonce", "nonce")
	var body jws
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			ca.t.Errorf("decode jws: %v", err)
		}
	}
	switch r.URL.Path {
	case "/directory":
		writeJSON(w, http.StatusOK, map[string]any{
			"newNonce": ca.url("/nonce"), "newAccount": ca.url("/acct"), "newOrder": ca.url("/order"),
			"meta": map[string]string{"termsOfService": ca.url("/tos")},
		})
	case "/nonce":
		w.WriteHeader(http.StatusOK)
	case "/acct":
		var protected struct {
			JWK struct{ Crv, Kty, X, Y string } `json:"jwk"`
		}
		decodeSegment(ca.t, body.Protected, &protected)
		var payload struct {
			TermsAgreed bool `json:"termsOfServiceAgreed"`
		}
		decodeSegment(ca.t, body.Payload, &payload)
		if !payload.TermsAgreed {
			ca.t.Errorf("account registered without agreeing to the terms")
		}
		jwk := protected.JWK
		sum := sha256.Sum256(fmt.Appendf(nil, `{"crv":%q,"kty":%q,"x":%q,"y":%q}`, jwk.Crv, jwk.Kty, jwk.X, jwk.Y))
		ca.thumbprint = base64.RawURLEncoding.EncodeToString(sum[:])
		ca.accounts++
		status := http.StatusCreated
		if ca.registered {
			status = http.StatusOK
		}
		ca.registered = true
		w.Header().Set("Location", ca.url("/acct/1"))
		writeJSON(w, status, map[string]string{"status": statusValid})
	case "/order":
		if ca.rateLimited {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "urn:ietf:params:acme:error:rateLimited", "detail": "too many certificates", "status": http.StatusTooManyRequests})
			return
		}
		ca.authzStatus, ca.orderStatus = "pending", "pending"
		w.Header().Set("Location", ca.url("/order/1"))
		writeJSON(w, http.StatusCreated, ca.order())
	case "/order/1":
		writeJSON(w, http.StatusOK, ca.order())
	case "/authz/1":
		authz := map[string]any{
			"status":     ca.authzStatus,
			"identifier": map[string]string{"type": "dns", "value": testDomain},
			"challenges": []map[string]any{{"type": "http-01", "url": ca.url("/chal/0"), "token": "x", "status": "pending"}, ca.challenge("pending")},
		}
		if ca.authzStatus == "invalid" {
			failed := ca.challenge("invalid")
			failed["error"] = map[string]any{"type": "urn:ietf:params:acme:error:unauthorized", "detail": "Incorrect TXT record", "status": http.StatusForbidden}
			authz["challenges"] = []map[string]any{failed}
		}
		writeJSON(w, http.StatusOK, authz)
	case "/chal/1":
		keyAuth := sha256.Sum256([]byte(testToken + "." + ca.thumbprint))
		want := base64.RawURLEncoding.EncodeToString(keyAuth[:])
		if ca.txt.get("_acme-challenge."+testDomain) == want {
			ca.authzStatus, ca.orderStatus = statusValid, "ready"
		} else {
			ca.authzStatus, ca.orderStatus = "invalid", "invalid"
		}
		writeJSON(w, http.StatusOK, ca.challenge("processing"))
	case "/order/1/finalize":
		var payload struct {
			CSR string `json:"csr"`
		}
		decodeSegment(ca.t, body.Payload, &payload)
		ca.leafPEM = ca.sign(payload.CSR)
		ca.orderStatus = statusValid
		writeJSON(w, http.StatusOK, ca.order())
	case "/cert/1":
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		chain := append([]byte{}, ca.leafPEM...)
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.caCert.Raw})...)
		_, _ = w.Write(chain)
	default:
		http.NotFound(w, r)
	}
}

func (ca *fakeCA) sign(csrSegment string) []byte {
	raw, err := base64.RawURLEncoding.DecodeString(csrSegment)
	if err != nil {
		ca.t.Errorf("decode csr: %v", err)
		return nil
	}
	csr, err := x509.ParseCertificateRequest(raw)
	if err != nil {
		ca.t.Errorf("parse csr: %v", err)
		return nil
	}
	if err := csr.CheckSignature(); err != nil {
		ca.t.Errorf("csr signature: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: csr.Subject, DNSNames: csr.DNSNames,
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(90 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.caCert, csr.PublicKey, ca.caKey)
	if err != nil {
		ca.t.Errorf("sign leaf: %v", err)
		return nil
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// storeSolver 把验证值写进 txtStore；override 非空时写它而不是正确的值。
type storeSolver struct {
	store    *txtStore
	override string
	err      error
}

func (s storeSolver) Present(_ context.Context, name, value string) (func(context.Context) error, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.override != "" {
		value = s.override
	}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	s.store.values[name] = value
	return func(context.Context) error {
		s.store.mu.Lock()
		defer s.store.mu.Unlock()
		delete(s.store.values, name)
		s.store.removed++
		return nil
	}, nil
}

func newIssuer(ca *fakeCA, check PropagationCheck) *Issuer {
	if check == nil {
		check = func(_ context.Context, name, value string) (bool, error) { return ca.txt.get(name) == value, nil }
	}
	return New(Config{DirectoryURL: ca.url("/directory"), Check: check, PropagationTimeout: 200 * time.Millisecond, PropagationInterval: 10 * time.Millisecond})
}

func newKey(t *testing.T) []byte {
	t.Helper()
	key, err := NewAccountKey()
	if err != nil {
		t.Fatalf("account key: %v", err)
	}
	return key
}

func TestIssue(t *testing.T) {
	ca := newFakeCA(t)
	issuer := newIssuer(ca, nil)
	key := newKey(t)
	cert, err := issuer.Issue(context.Background(), key, testDomain, storeSolver{store: ca.txt})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	block, rest := pem.Decode(cert.CertPEM)
	if block == nil {
		t.Fatal("no certificate in CertPEM")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != testDomain {
		t.Fatalf("leaf names: %v", leaf.DNSNames)
	}
	if next, _ := pem.Decode(rest); next == nil {
		t.Fatal("chain should carry the issuer certificate after the leaf")
	}
	if !cert.NotAfter.Equal(leaf.NotAfter) || string(cert.Leaf) != string(leaf.Raw) {
		t.Fatal("NotAfter / Leaf do not describe the leaf certificate")
	}
	keyBlock, _ := pem.Decode(cert.KeyPEM)
	if keyBlock == nil {
		t.Fatal("no private key in KeyPEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	priv, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || !priv.PublicKey.Equal(leaf.PublicKey) {
		t.Fatal("private key does not match the certificate")
	}
	if got := ca.txt.get("_acme-challenge." + testDomain); got != "" || ca.txt.removed != 1 {
		t.Fatalf("txt record should be removed once, left %q removed %d", got, ca.txt.removed)
	}

	// 同一把账号密钥再申请一次：签发方回“账号已存在”，照常签出
	if _, err := issuer.Issue(context.Background(), key, testDomain, storeSolver{store: ca.txt}); err != nil {
		t.Fatalf("second Issue with the same account: %v", err)
	}
	if ca.accounts != 2 {
		t.Fatalf("want 2 account requests, got %d", ca.accounts)
	}
}

func TestIssueValidationFails(t *testing.T) {
	ca := newFakeCA(t)
	always := func(context.Context, string, string) (bool, error) { return true, nil }
	_, err := newIssuer(ca, always).Issue(context.Background(), newKey(t), testDomain, storeSolver{store: ca.txt, override: "wrong"})
	ae := AsError(err)
	if ae == nil || ae.Kind != KindCA {
		t.Fatalf("want KindCA, got %v", err)
	}
	if ae.Message != msgValidation+"：Incorrect TXT record" {
		t.Fatalf("message should carry the CA's detail, got %q", ae.Message)
	}
	if ca.txt.removed != 1 {
		t.Fatalf("txt record must be removed after a failed validation, removed %d", ca.txt.removed)
	}
}

func TestIssueNotPropagated(t *testing.T) {
	ca := newFakeCA(t)
	never := func(context.Context, string, string) (bool, error) { return false, nil }
	_, err := newIssuer(ca, never).Issue(context.Background(), newKey(t), testDomain, storeSolver{store: ca.txt})
	ae := AsError(err)
	if ae == nil || ae.Kind != KindDNS || ae.Message != msgNotPropagated {
		t.Fatalf("want KindDNS not propagated, got %v", err)
	}
	if ca.txt.removed != 1 {
		t.Fatalf("txt record must be removed, removed %d", ca.txt.removed)
	}
}

func TestIssueSolverErrorPassesThrough(t *testing.T) {
	ca := newFakeCA(t)
	sentinel := errors.New("provider says no")
	_, err := newIssuer(ca, nil).Issue(context.Background(), newKey(t), testDomain, storeSolver{store: ca.txt, err: sentinel})
	if !errors.Is(err, sentinel) || AsError(err) != nil {
		t.Fatalf("solver error should come back unchanged, got %v", err)
	}
}

func TestIssueRateLimited(t *testing.T) {
	ca := newFakeCA(t)
	ca.rateLimited = true
	_, err := newIssuer(ca, nil).Issue(context.Background(), newKey(t), testDomain, storeSolver{store: ca.txt})
	ae := AsError(err)
	if ae == nil || ae.Kind != KindCA || ae.Message != msgCARateLimited+"：too many certificates" {
		t.Fatalf("want rate limited, got %v", err)
	}
}

func TestIssueTimeoutKeepsDeadline(t *testing.T) {
	ca := newFakeCA(t)
	never := func(context.Context, string, string) (bool, error) { return false, nil }
	issuer := New(Config{DirectoryURL: ca.url("/directory"), Check: never, PropagationTimeout: time.Minute, PropagationInterval: 10 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := issuer.Issue(ctx, newKey(t), testDomain, storeSolver{store: ca.txt})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded in the chain, got %v", err)
	}
	if ca.txt.removed != 1 {
		t.Fatalf("txt record must be removed even after the deadline, removed %d", ca.txt.removed)
	}
}

func TestIssueBadAccountKey(t *testing.T) {
	ca := newFakeCA(t)
	_, err := newIssuer(ca, nil).Issue(context.Background(), []byte("not a key"), testDomain, storeSolver{store: ca.txt})
	if ae := AsError(err); ae == nil || ae.Message != msgBadAccountKey {
		t.Fatalf("want bad account key, got %v", err)
	}
}

func TestNameServerCheckUsesGivenResolvers(t *testing.T) {
	// 指到一个没人监听的端口：查询失败应返回“没查到 + 错误”，而不是当成已生效
	check := nameServerCheck([]string{"127.0.0.1:1"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	seen, err := check(ctx, "_acme-challenge."+testDomain, "v")
	if seen || err == nil {
		t.Fatalf("unreachable resolver: want not seen with an error, got seen=%v err=%v", seen, err)
	}
}
