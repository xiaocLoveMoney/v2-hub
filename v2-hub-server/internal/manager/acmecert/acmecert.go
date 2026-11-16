// Package acmecert 向 ACME 签发方（RFC 8555，默认 Let's Encrypt）申请域名证书，用 DNS-01 验证（docs/03 §5.22，Ruling-73）。
// 本包只管 ACME 协议与“等 TXT 记录生效”，不认识任何 DNS 服务商：写记录的事经 Solver 交给调用方。
package acmecert

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/acme"
)

const (
	// DefaultDirectoryURL 是 Let's Encrypt 正式环境的目录地址。
	DefaultDirectoryURL = acme.LetsEncryptURL

	challengeDNS01  = "dns-01"
	challengePrefix = "_acme-challenge."
	userAgent       = "v2-hub"
	dnsPort         = "53"
	// maxNameServers 是确认生效时最多查的权威服务器数。
	maxNameServers = 4
	// maxRetries / maxRetryDelay 是一次请求失败后的重试次数与间隔上限（签发方 5xx 或 nonce 过期时）。
	maxRetries    = 4
	maxRetryDelay = 10 * time.Second
	// cleanupTimeout 是删 TXT 记录的时限；此时申请的 ctx 可能已超时，所以另起一个。
	cleanupTimeout = 30 * time.Second

	defaultPropagationTimeout  = 3 * time.Minute
	defaultPropagationInterval = 5 * time.Second
	defaultHTTPTimeout         = 30 * time.Second

	pemTypeCertificate = "CERTIFICATE"
	pemTypePrivateKey  = "PRIVATE KEY"

	msgBadAccountKey  = "ACME 账号密钥无法解析"
	msgCAUnreachable  = "连不上证书签发方"
	msgCARejected     = "证书签发方拒绝了请求"
	msgCARateLimited  = "证书签发方限流，请晚些再试"
	msgNoDNSChallenge = "签发方没有给出 DNS 验证方式"
	msgValidation     = "域名验证没有通过"
	msgOrderFailed    = "签发方没有签出证书"
	msgNotPropagated  = "验证用的 TXT 记录迟迟没有在权威服务器上生效"
	msgNoNameServers  = "查不到这个域名的权威服务器"
	msgBadCertificate = "签发方返回的证书无法解析"
	statusTooManyReqs = http.StatusTooManyRequests
)

// ErrorKind 是申请失败的大类。
type ErrorKind string

const (
	// KindDNS 表示验证记录没有生效（记录本身写失败由 Solver 返回它自己的错误）。
	KindDNS ErrorKind = "dns"
	// KindCA 表示签发方拒绝、验证没过或连不上签发方。
	KindCA ErrorKind = "ca"
)

// Error 是带大类的申请错误；Message 可直接展示。
type Error struct {
	Kind    ErrorKind
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("acme %s: %s: %v", e.Kind, e.Message, e.Err)
	}
	return fmt.Sprintf("acme %s: %s", e.Kind, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// AsError 从错误链里取出 *Error；不是申请错误返回 nil。
func AsError(err error) *Error {
	var ae *Error
	if errors.As(err, &ae) {
		return ae
	}
	return nil
}

// Solver 负责把 DNS-01 的验证值写成 TXT 记录。
type Solver interface {
	// Present 在 name（形如 _acme-challenge.node.example.com）下写一条值为 value 的 TXT 记录，
	// 返回删掉它的函数。写失败时返回的错误原样交还给 Issue 的调用方。
	Present(ctx context.Context, name, value string) (cleanup func(ctx context.Context) error, err error)
}

// PropagationCheck 报告 name 下值为 value 的 TXT 记录是否已经能查到。
type PropagationCheck func(ctx context.Context, name, value string) (bool, error)

// Config 是签发参数。
type Config struct {
	// DirectoryURL 是签发方的目录地址，空串用 DefaultDirectoryURL。
	DirectoryURL string
	// PropagationTimeout / PropagationInterval 是等 TXT 记录生效的总时限与查询间隔；0 用默认值。
	PropagationTimeout  time.Duration
	PropagationInterval time.Duration
	// Resolvers 非空时向这些 host:port 查 TXT，而不是域名的权威服务器；只给测试环境用。
	Resolvers []string
	// Check 非 nil 时代替默认的生效检查；只给测试用。
	Check PropagationCheck
	// HTTPClient 为 nil 时用带超时的默认客户端。
	HTTPClient *http.Client
}

// Certificate 是一次申请的结果。KeyPEM 是新生成的私钥，调用方用完即弃，本包不保存。
type Certificate struct {
	// CertPEM 是证书链，叶子在前。
	CertPEM  []byte
	KeyPEM   []byte
	NotAfter time.Time
	// Leaf 是叶子证书的 DER。
	Leaf []byte
}

// Issuer 申请证书；无状态，可并发使用。
type Issuer struct {
	directoryURL string
	timeout      time.Duration
	interval     time.Duration
	check        PropagationCheck
	http         *http.Client
}

// New 按配置创建 Issuer。
func New(cfg Config) *Issuer {
	i := &Issuer{
		directoryURL: cfg.DirectoryURL, timeout: cfg.PropagationTimeout, interval: cfg.PropagationInterval,
		check: cfg.Check, http: cfg.HTTPClient,
	}
	if i.directoryURL == "" {
		i.directoryURL = DefaultDirectoryURL
	}
	if i.timeout <= 0 {
		i.timeout = defaultPropagationTimeout
	}
	if i.interval <= 0 {
		i.interval = defaultPropagationInterval
	}
	if i.http == nil {
		i.http = &http.Client{Timeout: defaultHTTPTimeout}
	}
	if i.check == nil {
		i.check = nameServerCheck(cfg.Resolvers)
	}
	return i
}

// DirectoryURL 返回签发方的目录地址；账号密钥按它区分。
func (i *Issuer) DirectoryURL() string { return i.directoryURL }

// NewAccountKey 生成一把 ACME 账号密钥（ECDSA P-256），返回 PKCS#8 的 DER。
func NewAccountKey() ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate acme account key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal acme account key: %w", err)
	}
	return der, nil
}

func parseAccountKey(der []byte) (crypto.Signer, error) {
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, &Error{Kind: KindCA, Message: msgBadAccountKey, Err: err}
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, &Error{Kind: KindCA, Message: msgBadAccountKey}
	}
	return signer, nil
}

// Issue 为 domain 申请一张证书：注册（或找回）账号 → 下单 → 经 solver 完成 DNS-01 验证 → 取证书。
// accountKey 是 NewAccountKey 生成的 DER。失败返回 *Error，或 solver 自己的错误；ctx 超时返回的错误链里带 context.DeadlineExceeded。
func (i *Issuer) Issue(ctx context.Context, accountKey []byte, domain string, solver Solver) (*Certificate, error) {
	signer, err := parseAccountKey(accountKey)
	if err != nil {
		return nil, err
	}
	client := &acme.Client{Key: signer, DirectoryURL: i.directoryURL, HTTPClient: i.http, UserAgent: userAgent, RetryBackoff: retryBackoff}
	if _, err := client.Register(ctx, &acme.Account{}, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return nil, caError(err)
	}
	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(domain))
	if err != nil {
		return nil, caError(err)
	}
	for _, authzURL := range order.AuthzURLs {
		if err := i.authorize(ctx, client, authzURL, solver); err != nil {
			return nil, err
		}
	}
	if order, err = client.WaitOrder(ctx, order.URI); err != nil {
		return nil, caError(err)
	}
	return finalize(ctx, client, order.FinalizeURL, domain)
}

// retryBackoff 决定一次失败的请求隔多久重试，返回 0 即不再重试。
// 限流（429）不重试：客户端默认会一直重试到 ctx 超时，任务最后只剩一句“超时”，看不出是被限流了。
func retryBackoff(n int, _ *http.Request, resp *http.Response) time.Duration {
	if n > maxRetries || (resp != nil && resp.StatusCode == statusTooManyReqs) {
		return 0
	}
	return min(time.Duration(1<<(n-1))*time.Second, maxRetryDelay)
}

// authorize 完成一个域名的 DNS-01 验证；TXT 记录无论成败都会删掉。
func (i *Issuer) authorize(ctx context.Context, client *acme.Client, authzURL string, solver Solver) error {
	authz, err := client.GetAuthorization(ctx, authzURL)
	if err != nil {
		return caError(err)
	}
	if authz.Status == acme.StatusValid {
		return nil
	}
	idx := slices.IndexFunc(authz.Challenges, func(c *acme.Challenge) bool { return c.Type == challengeDNS01 })
	if idx < 0 {
		return &Error{Kind: KindCA, Message: msgNoDNSChallenge}
	}
	challenge := authz.Challenges[idx]
	value, err := client.DNS01ChallengeRecord(challenge.Token)
	if err != nil {
		return caError(err)
	}
	name := challengePrefix + strings.TrimPrefix(authz.Identifier.Value, "*.")
	cleanup, err := solver.Present(ctx, name, value)
	if err != nil {
		return err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		// 记录删不掉不影响证书，下次申请会另写一条；错误交给 Solver 自己记
		_ = cleanup(cctx)
	}()
	if err := i.waitPropagated(ctx, name, value); err != nil {
		return err
	}
	if _, err := client.Accept(ctx, challenge); err != nil {
		return caError(err)
	}
	if _, err := client.WaitAuthorization(ctx, authz.URI); err != nil {
		return caError(err)
	}
	return nil
}

// waitPropagated 反复检查直到 TXT 记录能查到或超时。
func (i *Issuer) waitPropagated(ctx context.Context, name, value string) error {
	deadline := time.NewTimer(i.timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(i.interval)
	defer ticker.Stop()
	var lastErr error
	for {
		seen, err := i.check(ctx, name, value)
		if seen {
			return nil
		}
		if err != nil {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for txt %s: %w", name, ctx.Err())
		case <-deadline.C:
			return &Error{Kind: KindDNS, Message: msgNotPropagated, Err: lastErr}
		case <-ticker.C:
		}
	}
}

// finalize 生成证书私钥与 CSR，让签发方签出证书。
func finalize(ctx context.Context, client *acme.Client, finalizeURL, domain string) (*Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate certificate key: %w", err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: domain}, DNSNames: []string{domain},
	}, key)
	if err != nil {
		return nil, fmt.Errorf("create csr: %w", err)
	}
	chain, _, err := client.CreateOrderCert(ctx, finalizeURL, csr, true)
	if err != nil {
		return nil, caError(err)
	}
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return nil, &Error{Kind: KindCA, Message: msgBadCertificate, Err: err}
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal certificate key: %w", err)
	}
	var certPEM []byte
	for _, der := range chain {
		certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{Type: pemTypeCertificate, Bytes: der})...)
	}
	return &Certificate{
		CertPEM: certPEM, KeyPEM: pem.EncodeToMemory(&pem.Block{Type: pemTypePrivateKey, Bytes: keyDER}),
		NotAfter: leaf.NotAfter, Leaf: chain[0],
	}, nil
}

// caError 把 ACME 客户端的错误归成可展示的申请错误；超时与取消原样带在错误链里。
func caError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("acme request: %w", err)
	}
	var authzErr *acme.AuthorizationError
	if errors.As(err, &authzErr) {
		return &Error{Kind: KindCA, Message: withDetail(msgValidation, authzDetail(authzErr)), Err: err}
	}
	var orderErr *acme.OrderError
	if errors.As(err, &orderErr) {
		detail := ""
		if orderErr.Problem != nil {
			detail = orderErr.Problem.Detail
		}
		return &Error{Kind: KindCA, Message: withDetail(msgOrderFailed, detail), Err: err}
	}
	var acmeErr *acme.Error
	if errors.As(err, &acmeErr) {
		message := msgCARejected
		if acmeErr.StatusCode == statusTooManyReqs {
			message = msgCARateLimited
		}
		return &Error{Kind: KindCA, Message: withDetail(message, acmeErr.Detail), Err: err}
	}
	return &Error{Kind: KindCA, Message: msgCAUnreachable, Err: err}
}

func authzDetail(err *acme.AuthorizationError) string {
	for _, e := range err.Errors {
		var acmeErr *acme.Error
		if errors.As(e, &acmeErr) && acmeErr.Detail != "" {
			return acmeErr.Detail
		}
	}
	return ""
}

func withDetail(message, detail string) string {
	if detail == "" {
		return message
	}
	return message + "：" + detail
}

// nameServerCheck 返回默认的生效检查：向 resolvers（为空则向域名的权威服务器）逐个查 TXT，全部查到才算生效。
// 不问公共递归解析器：它们会把“还没有这条记录”的结果缓存起来，反而拖慢后面的查询。
func nameServerCheck(resolvers []string) PropagationCheck {
	return func(ctx context.Context, name, value string) (bool, error) {
		servers := resolvers
		if len(servers) == 0 {
			found, err := authoritativeServers(ctx, strings.TrimPrefix(name, challengePrefix))
			if err != nil {
				return false, err
			}
			servers = found
		}
		for _, server := range servers {
			seen, err := hasTXT(ctx, server, name, value)
			if err != nil || !seen {
				return false, err
			}
		}
		return true, nil
	}
}

// authoritativeServers 从 domain 起逐级往上找到有 NS 记录的那一级，返回它的权威服务器（host:53）。
func authoritativeServers(ctx context.Context, domain string) ([]string, error) {
	for zone := domain; strings.Contains(zone, "."); _, zone, _ = strings.Cut(zone, ".") {
		records, err := net.DefaultResolver.LookupNS(ctx, zone+".")
		if err != nil || len(records) == 0 {
			continue
		}
		servers := make([]string, 0, maxNameServers)
		for _, ns := range records {
			if len(servers) == maxNameServers {
				break
			}
			servers = append(servers, net.JoinHostPort(strings.TrimSuffix(ns.Host, "."), dnsPort))
		}
		return servers, nil
	}
	return nil, &Error{Kind: KindDNS, Message: msgNoNameServers}
}

// hasTXT 直接向 server 查 name 的 TXT 记录里有没有 value；没有这条记录不算错误。
func hasTXT(ctx context.Context, server, name, value string) (bool, error) {
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, server)
		},
	}
	values, err := resolver.LookupTXT(ctx, name+".")
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lookup txt at %s: %w", server, err)
	}
	return slices.Contains(values, value), nil
}
