// Package reach 让第三方探测网络从中国大陆与境外同时去连一个地址，用来判断它是否被墙
// （docs/03 §5.20 大陆连通检测，Ruling-67）。hub-server 自己不连目标；只把公网地址发出去。
// 目前的实现是 Globalping 公共探测网络，接口见其 OpenAPI（POST /v1/measurements、GET /v1/measurements/{id}）。
package reach

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultAPIURL 是 Globalping 的官方接口地址；联调时可换成模拟端。
	DefaultAPIURL = "https://api.globalping.io"
	// MainlandCountry 是大陆探测点的国家码（港澳台在 Globalping 里是各自的代码）。
	MainlandCountry = "CN"
	// MainlandProbes 是每次检测取的大陆探测点数。
	MainlandProbes = 3
	// FreeHourlyLimit / TokenHourlyLimit 是不带令牌（按来源 IP）与带令牌时每小时的免费探测次数，一个探测点算一次。
	FreeHourlyLimit  = 250
	TokenHourlyLimit = 500

	defaultTimeout   = 40 * time.Second
	requestTimeout   = 10 * time.Second
	pollInterval     = time.Second
	maxResponseBytes = 1 << 20
	pingPackets      = 3
	// latencyPackets 是线路质量测量每个探测点发的包数（Ruling-86）；多发几个包丢包率才有区分度。
	latencyPackets = 8
	percentBase    = 100
	maxPort        = 65535

	pathMeasurements = "/v1/measurements"
	measurementPing  = "ping"
	protocolTCP      = "TCP"
	protocolICMP     = "ICMP"
	statusInProgress = "in-progress"
	statusFinished   = "finished"

	headerContent   = "Content-Type"
	headerAccept    = "Accept"
	headerAuth      = "Authorization"
	headerUserAgent = "User-Agent"
	contentTypeJSON = "application/json"
	bearerPrefix    = "Bearer "
	userAgent       = "v2-hub-reach/1"
)

// Mode 是探测方式。
type Mode string

const (
	// ModeTCP 测 TCP 连接到指定端口。
	ModeTCP Mode = "tcp"
	// ModeICMP 只测 ICMP（UDP 类入站与“封 IP 还是封端口”的补测用）。
	ModeICMP Mode = "icmp"
)

var (
	// ErrBadConfig 表示配置不合法。
	ErrBadConfig = errors.New("reach config is invalid")
	// ErrNotPublic 表示目标不是公网地址（内网、回环等），不发给探测服务。
	ErrNotPublic = errors.New("target is not a public address")
	// ErrRateLimited 表示探测服务限流（额度用完）。
	ErrRateLimited = errors.New("reach probe rate limited")
	// ErrRejected 表示探测服务拒绝了请求（参数不被接受、没有可用探测点等）。
	ErrRejected = errors.New("reach probe rejected")
	// ErrUnreachable 表示连不上探测服务、响应异常或在时限内没出结果。
	ErrUnreachable = errors.New("reach probe service unreachable")
)

// controlCountries 是境外对照点所在的国家，各取一个。
var controlCountries = []string{"JP", "US"}

// sharedAddressSpace 是运营商级 NAT 的共享地址段（RFC 6598），netip 不把它算作私网。
var sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")

// privateSuffixes 是只在内网或本机有意义的域名后缀（RFC 6761、RFC 6762、RFC 8375 等），这类地址不发给探测服务。
var privateSuffixes = []string{".local", ".localhost", ".internal", ".lan", ".home.arpa", ".test", ".invalid"}

// Request 是一次检测：从大陆与境外对照点去连 Address。Mode 为 ModeTCP 时连 Port。
// MainlandOnly 为真时不取境外对照点（补测用）。
type Request struct {
	Address      string
	Port         int
	Mode         Mode
	MainlandOnly bool
}

// ProbeResult 是一个探测点的结果。IsCounted 为假表示探测点自己出错或离线，不计入结论。
// RTTMs 是平均往返时间（毫秒），不通为 nil。
type ProbeResult struct {
	IsMainland bool
	IsCounted  bool
	IsOK       bool
	Country    string
	City       string
	Network    string
	RTTMs      *float64
}

// Prober 能从大陆与境外探测点检测一个地址。
type Prober interface {
	// Probe 发起一次检测并等到出结果；目标不是公网地址返回 ErrNotPublic，限流返回 ErrRateLimited，
	// 被拒绝返回 ErrRejected，连不上或超时返回 ErrUnreachable。
	Probe(ctx context.Context, req Request) ([]ProbeResult, error)
	// Cost 返回这次检测要用掉的探测次数（一个探测点一次）。
	Cost(req Request) int
}

// Config 是 Globalping 客户端配置。Token 可空（空 = 按来源 IP 的免费额度）。
type Config struct {
	APIURL  string
	Token   string
	Timeout time.Duration
}

// Client 是基于 Globalping 的 Prober，并发安全。
type Client struct {
	base    string
	token   string
	timeout time.Duration
	http    *http.Client
	sleep   func(ctx context.Context, d time.Duration) error
}

// New 按配置创建客户端；APIURL 为空取官方地址，不是 http(s) 地址返回 ErrBadConfig。
func New(cfg Config) (*Client, error) {
	base := cfg.APIURL
	if base == "" {
		base = DefaultAPIURL
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("%w: api url must be an http(s) url", ErrBadConfig)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{
		base: strings.TrimRight(base, "/"), token: cfg.Token, timeout: timeout,
		http: &http.Client{Timeout: requestTimeout}, sleep: sleepContext,
	}, nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// IsPublicTarget 报告地址能不能发给探测服务：IP 须是公网单播地址；域名须是带点的公网域名
// （单标签主机名与 .local、.internal 这类内网后缀不算），由探测点自己解析。
func IsPublicTarget(address string) bool {
	ip, err := netip.ParseAddr(address)
	if err != nil {
		return isPublicHostname(address)
	}
	ip = ip.Unmap()
	isSpecial := ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || sharedAddressSpace.Contains(ip)
	return ip.IsGlobalUnicast() && !isSpecial
}

func isPublicHostname(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if !strings.Contains(host, ".") {
		return false
	}
	for _, suffix := range privateSuffixes {
		if strings.HasSuffix(host, suffix) {
			return false
		}
	}
	return true
}

// Cost 实现 Prober。
func (c *Client) Cost(req Request) int {
	if req.MainlandOnly {
		return MainlandProbes
	}
	return MainlandProbes + len(controlCountries)
}

type location struct {
	Country string `json:"country"`
	ASN     int    `json:"asn,omitempty"`
	Limit   int    `json:"limit"`
}

type pingOptions struct {
	Packets  int    `json:"packets"`
	Protocol string `json:"protocol"`
	Port     int    `json:"port,omitempty"`
}

type measurementRequest struct {
	Type      string      `json:"type"`
	Target    string      `json:"target"`
	Locations []location  `json:"locations"`
	Options   pingOptions `json:"measurementOptions"`
}

type measurementCreated struct {
	ID string `json:"id"`
}

type measurement struct {
	Status  string `json:"status"`
	Results []struct {
		Probe struct {
			Country string `json:"country"`
			City    string `json:"city"`
			Network string `json:"network"`
			ASN     int    `json:"asn"`
		} `json:"probe"`
		Result struct {
			Status string `json:"status"`
			Stats  struct {
				Avg   *float64 `json:"avg"`
				Rcv   int      `json:"rcv"`
				Total int      `json:"total"`
			} `json:"stats"`
		} `json:"result"`
	} `json:"results"`
}

func buildRequest(req Request) (measurementRequest, error) {
	if !IsPublicTarget(req.Address) {
		return measurementRequest{}, ErrNotPublic
	}
	out := measurementRequest{
		Type: measurementPing, Target: req.Address,
		Locations: make([]location, 0, 1+len(controlCountries)),
		Options:   pingOptions{Packets: pingPackets, Protocol: protocolICMP},
	}
	out.Locations = append(out.Locations, location{Country: MainlandCountry, Limit: MainlandProbes})
	if !req.MainlandOnly {
		for _, country := range controlCountries {
			out.Locations = append(out.Locations, location{Country: country, Limit: 1})
		}
	}
	switch req.Mode {
	case ModeICMP:
	case ModeTCP:
		if req.Port < 1 || req.Port > maxPort {
			return measurementRequest{}, fmt.Errorf("%w: port out of range", ErrRejected)
		}
		out.Options.Protocol, out.Options.Port = protocolTCP, req.Port
	default:
		return measurementRequest{}, fmt.Errorf("%w: unknown mode", ErrRejected)
	}
	return out, nil
}

// Probe 实现 Prober：建一个 ping 测量，轮询到它结束。
func (c *Client) Probe(ctx context.Context, req Request) ([]ProbeResult, error) {
	body, err := buildRequest(req)
	if err != nil {
		return nil, err
	}
	m, err := c.measure(ctx, body)
	if err != nil {
		return nil, err
	}
	return toResults(m), nil
}

// measure 建一个测量并轮询到它结束。
func (c *Client) measure(ctx context.Context, body measurementRequest) (measurement, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	raw, err := json.Marshal(body)
	if err != nil {
		return measurement{}, fmt.Errorf("reach: marshal request: %w", err)
	}
	var created measurementCreated
	if err := c.do(ctx, http.MethodPost, pathMeasurements, raw, &created); err != nil {
		return measurement{}, err
	}
	if created.ID == "" {
		return measurement{}, fmt.Errorf("%w: measurement id missing", ErrUnreachable)
	}
	path := pathMeasurements + "/" + url.PathEscape(created.ID)
	for {
		if err := c.sleep(ctx, pollInterval); err != nil {
			return measurement{}, fmt.Errorf("%w: measurement did not finish in time", ErrUnreachable)
		}
		var m measurement
		if err := c.do(ctx, http.MethodGet, path, nil, &m); err != nil {
			return measurement{}, err
		}
		if m.Status != statusInProgress {
			return m, nil
		}
	}
}

// LatencyRequest 是一次线路质量测量（Ruling-86）：从大陆按 AS 号各取一个探测点去 ping Address。
type LatencyRequest struct {
	Address string
	ASNs    []int
}

// LatencyResult 是一个探测点的线路质量。AvgMs 是平均往返时间（毫秒），全丢为 nil；LossPercent 是丢包率（0–100）。
type LatencyResult struct {
	ASN         int
	City        string
	AvgMs       *float64
	LossPercent float64
}

// LatencyProber 能从大陆指定网络的探测点测一个地址的延迟与丢包。
type LatencyProber interface {
	// Latency 发起一次测量并等到出结果；出错或离线的探测点、当时没有探测点的 AS 不在结果里。错误种类同 Prober.Probe。
	Latency(ctx context.Context, req LatencyRequest) ([]LatencyResult, error)
}

// Latency 实现 LatencyProber。
func (c *Client) Latency(ctx context.Context, req LatencyRequest) ([]LatencyResult, error) {
	if !IsPublicTarget(req.Address) {
		return nil, ErrNotPublic
	}
	if len(req.ASNs) == 0 {
		return nil, fmt.Errorf("%w: no network given", ErrRejected)
	}
	body := measurementRequest{
		Type: measurementPing, Target: req.Address,
		Locations: make([]location, 0, len(req.ASNs)),
		Options:   pingOptions{Packets: latencyPackets, Protocol: protocolICMP},
	}
	for _, asn := range req.ASNs {
		body.Locations = append(body.Locations, location{Country: MainlandCountry, ASN: asn, Limit: 1})
	}
	m, err := c.measure(ctx, body)
	if err != nil {
		return nil, err
	}
	out := make([]LatencyResult, 0, len(m.Results))
	for _, r := range m.Results {
		stats := r.Result.Stats
		if r.Result.Status != statusFinished || stats.Total <= 0 {
			continue
		}
		res := LatencyResult{ASN: r.Probe.ASN, City: r.Probe.City}
		res.LossPercent = float64(stats.Total-stats.Rcv) * percentBase / float64(stats.Total)
		if stats.Rcv > 0 {
			res.AvgMs = stats.Avg
		}
		out = append(out, res)
	}
	return out, nil
}

func toResults(m measurement) []ProbeResult {
	out := make([]ProbeResult, 0, len(m.Results))
	for _, r := range m.Results {
		res := ProbeResult{
			IsMainland: strings.EqualFold(r.Probe.Country, MainlandCountry),
			IsCounted:  r.Result.Status == statusFinished,
			Country:    strings.ToUpper(r.Probe.Country), City: r.Probe.City, Network: r.Probe.Network,
		}
		if res.IsCounted && r.Result.Stats.Rcv > 0 {
			res.IsOK, res.RTTMs = true, r.Result.Stats.Avg
		}
		out = append(out, res)
	}
	return out
}

// do 发一次请求并把 2xx 的 JSON 响应解到 out；错误里不带地址与令牌。
func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) (retErr error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return fmt.Errorf("%w: build request", ErrBadConfig)
	}
	req.Header.Set(headerAccept, contentTypeJSON)
	req.Header.Set(headerUserAgent, userAgent)
	if body != nil {
		req.Header.Set(headerContent, contentTypeJSON)
	}
	if c.token != "" {
		req.Header.Set(headerAuth, bearerPrefix+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("%w: %v", ErrUnreachable, ue.Err)
		}
		return ErrUnreachable
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && retErr == nil {
			retErr = fmt.Errorf("reach: close body: %w", cerr)
		}
	}()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("%w: read response", ErrUnreachable)
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return ErrRateLimited
	case resp.StatusCode >= http.StatusInternalServerError:
		return fmt.Errorf("%w: status %d", ErrUnreachable, resp.StatusCode)
	case resp.StatusCode >= http.StatusBadRequest:
		return fmt.Errorf("%w: status %d", ErrRejected, resp.StatusCode)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: decode response", ErrUnreachable)
	}
	return nil
}
