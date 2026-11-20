// Package objectstore 把文件写进 S3 兼容的对象存储（docs/03 §5.9 图片，Ruling-89）。
// 平台只需要“写一个对象”这一件事，所以不引入 SDK：用标准库实现 AWS Signature V4 的单次 PUT，
// 地址用 path-style（{endpoint}/{bucket}/{key}），对 MinIO 用。
package objectstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	// DefaultRegion 是未配置区域时用的区域名；MinIO 默认就是它。
	DefaultRegion  = "us-east-1"
	defaultTimeout = 30 * time.Second
	// maxDrainBytes 是读出并丢弃的响应体上限，只为复用连接，内容不进错误信息。
	maxDrainBytes = 4 << 10

	sigAlgorithm   = "AWS4-HMAC-SHA256"
	sigService     = "s3"
	sigTerminator  = "aws4_request"
	sigKeyPrefix   = "AWS4"
	amzDateLayout  = "20060102T150405Z"
	amzDayLayout   = "20060102"
	headerHost     = "host"
	headerAmzDate  = "x-amz-date"
	headerAmzHash  = "x-amz-content-sha256"
	headerAuth     = "Authorization"
	headerType     = "Content-Type"
	headerCache    = "Cache-Control"
	uriUnreserved  = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~"
	hexDigitsUpper = "0123456789ABCDEF"
)

var (
	// ErrBadConfig 表示地址、桶或凭据不合法。
	ErrBadConfig = errors.New("object store config is invalid")
	// ErrUnavailable 表示写入失败：连不上、超时或对象存储返回非 2xx。
	ErrUnavailable = errors.New("object store unavailable")
)

// Config 是对象存储配置。凭据由调用方从环境变量读入，不进配置文件。
type Config struct {
	// Endpoint 形如 http://minio:9000，不带路径。
	Endpoint  string
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
	Timeout   time.Duration
}

// Object 是要写入的一个对象。
type Object struct {
	// Key 是桶内对象名，可含 /。
	Key          string
	Body         []byte
	ContentType  string
	CacheControl string
}

// Store 能把一个对象写进存储。
type Store interface {
	// Put 写入（覆盖）一个对象。失败返回包装了 ErrUnavailable 的错误。
	Put(ctx context.Context, obj Object) error
}

// Client 是 S3 兼容存储的 Store 实现，并发安全。
type Client struct {
	endpoint  *url.URL
	bucket    string
	region    string
	accessKey string
	secretKey string
	http      *http.Client
	now       func() time.Time
}

// New 按配置创建客户端。地址必须是 http(s) 且不带路径，桶与凭据必填。
func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.Trim(u.Path, "/") != "" {
		return nil, fmt.Errorf("%w: endpoint must be an http(s) url without path", ErrBadConfig)
	}
	if cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("%w: bucket and credentials are required", ErrBadConfig)
	}
	region := cfg.Region
	if region == "" {
		region = DefaultRegion
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	u.Path = ""
	return &Client{
		endpoint: u, bucket: cfg.Bucket, region: region, accessKey: cfg.AccessKey, secretKey: cfg.SecretKey,
		http: &http.Client{Timeout: timeout}, now: time.Now,
	}, nil
}

// Put 见 Store.Put。
func (c *Client) Put(ctx context.Context, obj Object) error {
	if obj.Key == "" {
		return fmt.Errorf("%w: empty key", ErrBadConfig)
	}
	path := "/" + uriEncodePath(c.bucket+"/"+obj.Key)
	sum := sha256.Sum256(obj.Body)
	payloadHash := hex.EncodeToString(sum[:])
	now := c.now().UTC()
	signed := map[string]string{
		headerHost:    c.endpoint.Host,
		headerAmzDate: now.Format(amzDateLayout),
		headerAmzHash: payloadHash,
	}
	auth := authorization(signRequest{
		method: http.MethodPut, path: path, headers: signed, payloadHash: payloadHash,
		at: now, region: c.region, accessKey: c.accessKey, secretKey: c.secretKey,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.endpoint.String()+path, bytes.NewReader(obj.Body))
	if err != nil {
		return fmt.Errorf("%w: build request: %v", ErrUnavailable, err)
	}
	req.Header.Set(headerAmzDate, signed[headerAmzDate])
	req.Header.Set(headerAmzHash, payloadHash)
	req.Header.Set(headerAuth, auth)
	if obj.ContentType != "" {
		req.Header.Set(headerType, obj.ContentType)
	}
	if obj.CacheControl != "" {
		req.Header.Set(headerCache, obj.CacheControl)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("%w: put object returned http %d", ErrUnavailable, resp.StatusCode)
	}
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes)); err != nil {
		return fmt.Errorf("%w: read response: %v", ErrUnavailable, err)
	}
	return nil
}

// signRequest 是一次 SigV4 签名的输入；headers 的键为小写，全部参与签名。
type signRequest struct {
	method      string
	path        string
	headers     map[string]string
	payloadHash string
	at          time.Time
	region      string
	accessKey   string
	secretKey   string
}

// authorization 按 AWS Signature V4 算出 Authorization 头（无查询串）。
func authorization(r signRequest) string {
	names := make([]string, 0, len(r.headers))
	for name := range r.headers {
		names = append(names, name)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, name := range names {
		canonHeaders.WriteString(name + ":" + strings.TrimSpace(r.headers[name]) + "\n")
	}
	signedHeaders := strings.Join(names, ";")
	canonical := strings.Join([]string{r.method, r.path, "", canonHeaders.String(), signedHeaders, r.payloadHash}, "\n")

	day := r.at.UTC().Format(amzDayLayout)
	scope := strings.Join([]string{day, r.region, sigService, sigTerminator}, "/")
	canonSum := sha256.Sum256([]byte(canonical))
	toSign := strings.Join([]string{sigAlgorithm, r.at.UTC().Format(amzDateLayout), scope, hex.EncodeToString(canonSum[:])}, "\n")

	key := hmacSHA256([]byte(sigKeyPrefix+r.secretKey), day)
	key = hmacSHA256(key, r.region)
	key = hmacSHA256(key, sigService)
	key = hmacSHA256(key, sigTerminator)
	signature := hex.EncodeToString(hmacSHA256(key, toSign))
	return fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s", sigAlgorithm, r.accessKey, scope, signedHeaders, signature)
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// uriEncodePath 按 SigV4 的规则编码路径：除未保留字符与 / 外一律 %XX（大写）。
func uriEncodePath(p string) string {
	var b strings.Builder
	b.Grow(len(p))
	for i := 0; i < len(p); i++ {
		ch := p[i]
		if ch == '/' || strings.IndexByte(uriUnreserved, ch) >= 0 {
			b.WriteByte(ch)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigitsUpper[ch>>4])
		b.WriteByte(hexDigitsUpper[ch&0x0f])
	}
	return b.String()
}
