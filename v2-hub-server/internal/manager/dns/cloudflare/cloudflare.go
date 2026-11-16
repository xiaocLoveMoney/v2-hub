// Package cloudflare 是 Cloudflare 的 dns.Provider 实现（docs/03 §5.22，Ruling-72）。
// 只用 DNS 记录接口；令牌只放在请求头里，不进日志与错误信息。
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"v2-hub-server/internal/manager/dns"
)

const (
	// ProviderType 是 dns_account.provider_type 的取值。
	ProviderType = "cloudflare"
	// DefaultBaseURL 是 Cloudflare API 的根地址。
	DefaultBaseURL = "https://api.cloudflare.com/client/v4"

	defaultTimeout   = 20 * time.Second
	maxResponseBytes = 1 << 20
	// txtTTL 是验证用 TXT 记录的 TTL（秒），Cloudflare 允许的最小值。
	txtTTL = 60
	// autoTTL 让 Cloudflare 自己定 TTL。
	autoTTL = 1
	// recordComment 标在平台建的记录上，方便在 Cloudflare 后台认出来。
	recordComment = "v2-hub"
	// listPageSize 是列记录时的每页条数；同一个名字下的记录远小于它。
	listPageSize = "100"

	typeA     = "A"
	typeAAAA  = "AAAA"
	typeCNAME = "CNAME"
	typeTXT   = "TXT"

	headerAuthorization = "Authorization"
	headerContentType   = "Content-Type"
	contentTypeJSON     = "application/json"
	bearerPrefix        = "Bearer "

	msgUnreachable = "连不上 Cloudflare 接口"
	msgBadResponse = "Cloudflare 接口返回了无法解析的内容"
	msgAuth        = "Cloudflare 拒绝了令牌：令牌无效，或没有这个域名的 DNS 编辑权限"
	msgNoZone      = "用这个令牌在 Cloudflare 上找不到这个域名，请确认根域名与令牌的授权范围"
	msgRejected    = "Cloudflare 拒绝了请求"
)

// Config 是 Cloudflare 接口的参数。
type Config struct {
	// BaseURL 为空时用 DefaultBaseURL；只在测试里改。
	BaseURL string
	// Timeout 是单次请求的时限，0 表示用默认值。
	Timeout time.Duration
}

// NewFactory 返回按账号创建 Cloudflare Provider 的工厂；所有 Provider 共用一个 HTTP 客户端。
func NewFactory(cfg Config) dns.Factory {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	httpClient := &http.Client{Timeout: timeout}
	return func(acc dns.Account) (dns.Provider, error) {
		if acc.Zone == "" || acc.Token == "" {
			return nil, fmt.Errorf("cloudflare: zone and token are required")
		}
		return &client{base: base, zone: acc.Zone, token: acc.Token, http: httpClient}, nil
	}
}

type client struct {
	base  string
	zone  string
	token string
	http  *http.Client

	mu     sync.Mutex
	zoneID string
}

var _ dns.Provider = (*client)(nil)

// envelope 是 Cloudflare 接口统一的响应外壳。
type envelope struct {
	Success bool            `json:"success"`
	Errors  []apiError      `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type record struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment,omitempty"`
}

// call 发一次请求并把 result 解到 out（out 为 nil 时不解）。
func (c *client) call(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("cloudflare: encode request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return fmt.Errorf("cloudflare: build request: %w", err)
	}
	req.Header.Set(headerAuthorization, bearerPrefix+c.token)
	if body != nil {
		req.Header.Set(headerContentType, contentTypeJSON)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// *url.Error 只带方法与地址，不含请求头里的令牌
		return &dns.Error{Kind: dns.KindUnreachable, Message: msgUnreachable, Err: err}
	}
	defer func() {
		_ = resp.Body.Close() // 只读响应，关闭失败不影响结果
	}()
	var env envelope
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&env)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &dns.Error{Kind: dns.KindAuth, Message: msgAuth}
	}
	if decodeErr != nil {
		return &dns.Error{Kind: dns.KindUnreachable, Message: fmt.Sprintf("%s（HTTP %d）", msgBadResponse, resp.StatusCode), Err: decodeErr}
	}
	if !env.Success {
		return &dns.Error{Kind: dns.KindRejected, Message: rejectedMessage(env.Errors, resp.StatusCode)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return &dns.Error{Kind: dns.KindUnreachable, Message: msgBadResponse, Err: err}
	}
	return nil
}

func rejectedMessage(errs []apiError, status int) string {
	if len(errs) == 0 {
		return fmt.Sprintf("%s（HTTP %d）", msgRejected, status)
	}
	return fmt.Sprintf("%s：%s（%d）", msgRejected, errs[0].Message, errs[0].Code)
}

// resolveZone 取根域名在 Cloudflare 上的 id，取到后缓存。
func (c *client) resolveZone(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.zoneID != "" {
		return c.zoneID, nil
	}
	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := c.call(ctx, http.MethodGet, "/zones", url.Values{"name": {c.zone}}, nil, &zones); err != nil {
		return "", err
	}
	for _, z := range zones {
		if strings.EqualFold(z.Name, c.zone) && z.ID != "" {
			c.zoneID = z.ID
			return z.ID, nil
		}
	}
	return "", &dns.Error{Kind: dns.KindZone, Message: msgNoZone}
}

// CheckZone 见 dns.Provider：找到根域名，并列一次记录确认有 DNS 权限。
func (c *client) CheckZone(ctx context.Context) error {
	_, err := c.listRecords(ctx, c.zone)
	return err
}

func (c *client) recordsPath(zoneID string) string {
	return "/zones/" + url.PathEscape(zoneID) + "/dns_records"
}

func (c *client) listRecords(ctx context.Context, name string) ([]record, error) {
	zoneID, err := c.resolveZone(ctx)
	if err != nil {
		return nil, err
	}
	var records []record
	err = c.call(ctx, http.MethodGet, c.recordsPath(zoneID), url.Values{"name": {name}, "per_page": {listPageSize}}, nil, &records)
	return records, err
}

func (c *client) createRecord(ctx context.Context, rec record) (string, error) {
	zoneID, err := c.resolveZone(ctx)
	if err != nil {
		return "", err
	}
	var created record
	if err := c.call(ctx, http.MethodPost, c.recordsPath(zoneID), nil, rec, &created); err != nil {
		return "", err
	}
	return created.ID, nil
}

func (c *client) deleteRecord(ctx context.Context, id string) error {
	zoneID, err := c.resolveZone(ctx)
	if err != nil {
		return err
	}
	return c.call(ctx, http.MethodDelete, c.recordsPath(zoneID)+"/"+url.PathEscape(id), nil, nil, nil)
}

// addressRecord 按 target 的形态决定记录类型与内容。
func addressRecord(name, target string) record {
	rec := record{Type: typeCNAME, Name: name, Content: target, TTL: autoTTL, Comment: recordComment}
	if addr, err := netip.ParseAddr(target); err == nil {
		addr = addr.Unmap()
		rec.Type, rec.Content = typeA, addr.String()
		if addr.Is6() {
			rec.Type = typeAAAA
		}
	}
	return rec
}

func isAddressType(recordType string) bool {
	return recordType == typeA || recordType == typeAAAA || recordType == typeCNAME
}

// PointTo 见 dns.Provider。
func (c *client) PointTo(ctx context.Context, name, target string) error {
	want := addressRecord(name, target)
	existing, err := c.listRecords(ctx, name)
	if err != nil {
		return err
	}
	found := false
	for _, rec := range existing {
		if !isAddressType(rec.Type) {
			continue
		}
		isSame := rec.Type == want.Type && strings.EqualFold(rec.Content, want.Content) && !rec.Proxied
		if isSame && !found {
			found = true
			continue
		}
		if err := c.deleteRecord(ctx, rec.ID); err != nil {
			return err
		}
	}
	if found {
		return nil
	}
	_, err = c.createRecord(ctx, want)
	return err
}

// SetTXT 见 dns.Provider。内容按 Cloudflare 的要求加上双引号（value 是 Base64URL，不含需要转义的字符）。
func (c *client) SetTXT(ctx context.Context, name, value string) (func(context.Context) error, error) {
	id, err := c.createRecord(ctx, record{Type: typeTXT, Name: name, Content: `"` + value + `"`, TTL: txtTTL, Comment: recordComment})
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) error { return c.deleteRecord(ctx, id) }, nil
}
