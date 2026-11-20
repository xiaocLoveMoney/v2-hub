// Package xui 是 3x-ui（v3.x）面板的 panel.Provider 实现（Ruling-7、Ruling-8）。
// 接口形状以 contract/upstream/3x-ui/openapi-v3.11.0.json 为准；面板专有路径与字段只出现在本包。
package xui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"v2-hub-server/internal/manager/panel"
)

// ProviderType 是 server.provider_type 中 3x-ui 的取值。
const ProviderType = "xui"

const (
	apiPrefix        = "/panel/api"
	headerAuth       = "Authorization"
	headerContent    = "Content-Type"
	headerAccept     = "Accept"
	contentTypeJSON  = "application/json"
	contentTypeForm  = "application/x-www-form-urlencoded"
	maxResponseBytes = 8 << 20 // 8 MiB，防止异常面板撑爆内存
	maxPanelMsgLen   = 200
)

// envelope 是 3x-ui 的统一响应 {success, msg, obj}。
type envelope struct {
	Success bool            `json:"success"`
	Msg     string          `json:"msg"`
	Obj     json.RawMessage `json:"obj"`
}

// Client 是一台 3x-ui 面板的客户端，实现 panel.Provider。并发安全。
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

// New 按连接信息创建客户端；可直接作为 panel.Factory 注册。
func New(cfg panel.ConnConfig) (panel.Provider, error) {
	base, err := panel.ParseBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	if cfg.Token == "" {
		return nil, errors.New("xui: api token is required")
	}
	hc, err := panel.NewHTTPClient(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{base: base, token: cfg.Token, http: hc}, nil
}

// Capabilities 实现 panel.Provider。
func (c *Client) Capabilities() panel.Capabilities {
	return panel.Capabilities{CoreType: panel.CoreXray, Endpoints: true, OnlineClients: true, CoreConfig: true}
}

// call 发起一次 API 调用，校验信封并把 obj 解到 out（out 为 nil 时忽略）。
// 错误一律转成 *panel.Error，Message 不含 token 与完整地址。
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	if body == nil {
		return c.send(ctx, method, path, "", nil, out)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("xui: marshal request: %w", err)
	}
	return c.send(ctx, method, path, contentTypeJSON, bytes.NewReader(raw), out)
}

// callForm 以表单提交（3x-ui 部分接口只收表单字段），其余同 call。
func (c *Client) callForm(ctx context.Context, path string, form url.Values, out any) error {
	return c.send(ctx, http.MethodPost, path, contentTypeForm, bytes.NewReader([]byte(form.Encode())), out)
}

// send 发请求并解信封；contentType 为空表示没有请求体。
func (c *Client) send(ctx context.Context, method, path, contentType string, reader io.Reader, out any) error {
	return c.sendVia(ctx, c.http, method, path, contentType, reader, out)
}

// callFormSlow 同 callForm，但用 timeout 作请求时限（个别接口比普通调用慢）；只改本次调用的副本，不动共享的 HTTP 客户端。
func (c *Client) callFormSlow(ctx context.Context, path string, form url.Values, timeout time.Duration, out any) error {
	slow := *c.http
	slow.Timeout = timeout
	return c.sendVia(ctx, &slow, http.MethodPost, path, contentTypeForm, bytes.NewReader([]byte(form.Encode())), out)
}

func (c *Client) sendVia(ctx context.Context, hc *http.Client, method, path, contentType string, reader io.Reader, out any) (retErr error) {
	target := c.base.JoinPath(apiPrefix, path)
	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return fmt.Errorf("xui: build request: %w", err)
	}
	req.Header.Set(headerAuth, "Bearer "+c.token)
	req.Header.Set(headerAccept, contentTypeJSON)
	if contentType != "" {
		req.Header.Set(headerContent, contentType)
	}

	resp, err := hc.Do(req)
	if err != nil {
		return &panel.Error{Kind: panel.KindUnreachable, Message: "无法连接面板（网络、TLS 或超时）", Err: err}
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && retErr == nil {
			retErr = fmt.Errorf("xui: close body: %w", cerr)
		}
	}()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return &panel.Error{Kind: panel.KindUnreachable, Message: "读取面板响应失败", Err: err}
	}
	if kindErr := statusError(resp.StatusCode); kindErr != nil {
		return kindErr
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return &panel.Error{Kind: panel.KindUnreachable, Message: "面板响应不是预期的 JSON", Err: err}
	}
	if !env.Success {
		return &panel.Error{Kind: panel.KindRejected, Message: "面板拒绝了请求：" + truncate(env.Msg)}
	}
	if out == nil || len(env.Obj) == 0 || string(env.Obj) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Obj, out); err != nil {
		return &panel.Error{Kind: panel.KindUnreachable, Message: "面板返回的数据格式与预期不符", Err: err}
	}
	return nil
}

// statusError 按 HTTP 状态分类（docs/03 T2、T3）。3x-ui 对未认证与未知路径都可能返回 404。
func statusError(code int) error {
	switch {
	case code >= http.StatusOK && code < http.StatusMultipleChoices:
		return nil
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return &panel.Error{Kind: panel.KindAuth, Message: "面板拒绝了 API token"}
	case code == http.StatusNotFound:
		return &panel.Error{Kind: panel.KindNotFound, Message: "面板返回 404：地址或 base path 不对，或 token 无效"}
	case code >= http.StatusMultipleChoices && code < http.StatusBadRequest:
		return &panel.Error{Kind: panel.KindUnreachable, Message: fmt.Sprintf("面板返回重定向（HTTP %d），请检查地址与 base path", code)}
	case code >= http.StatusInternalServerError:
		return &panel.Error{Kind: panel.KindUnreachable, Message: fmt.Sprintf("面板内部错误（HTTP %d）", code)}
	default:
		return &panel.Error{Kind: panel.KindRejected, Message: fmt.Sprintf("面板拒绝了请求（HTTP %d）", code)}
	}
}

func truncate(s string) string {
	r := []rune(s)
	if len(r) <= maxPanelMsgLen {
		return s
	}
	return string(r[:maxPanelMsgLen])
}

// publicIPUnknown 是 3x-ui 取不到公网 IP 时填的占位。
const publicIPUnknown = "N/A"

// loadWindows 是 3x-ui 状态里 loads 数组的长度：1、5、15 分钟。
const loadWindows = 3

// usage 是 3x-ui 状态里“已用 / 总量”的一对（mem、swap、disk）。
type usage struct {
	Current int64 `json:"current"`
	Total   int64 `json:"total"`
}

// serverStatus 是 GET /server/status 的 obj（3x-ui v3.11.0 实测）里平台用到的字段。
// netIO 是每秒收发字节，netTraffic 是开机以来所有网卡累计收发字节。
type serverStatus struct {
	PanelVersion string    `json:"panelVersion"`
	PanelGUID    string    `json:"panelGuid"`
	CPU          *float64  `json:"cpu"`
	CPUCores     int       `json:"cpuCores"`
	Mem          usage     `json:"mem"`
	Swap         usage     `json:"swap"`
	Disk         usage     `json:"disk"`
	Uptime       int64     `json:"uptime"`
	Loads        []float64 `json:"loads"`
	TCPCount     int       `json:"tcpCount"`
	UDPCount     int       `json:"udpCount"`
	NetIO        struct {
		Up   int64 `json:"up"`
		Down int64 `json:"down"`
	} `json:"netIO"`
	NetTraffic struct {
		Sent int64 `json:"sent"`
		Recv int64 `json:"recv"`
	} `json:"netTraffic"`
	Xray struct {
		State    string `json:"state"`
		ErrorMsg string `json:"errorMsg"`
		Version  string `json:"version"`
	} `json:"xray"`
	PublicIP struct {
		IPv4 string `json:"ipv4"`
		IPv6 string `json:"ipv6"`
	} `json:"publicIP"`
}

// Probe 实现 panel.Provider：读取服务器状态，得到面板版本、GUID 与资源快照。
func (c *Client) Probe(ctx context.Context) (panel.PanelInfo, error) {
	var status serverStatus
	if err := c.call(ctx, http.MethodGet, "/server/status", nil, &status); err != nil {
		return panel.PanelInfo{}, err
	}
	info := panel.PanelInfo{
		Version: status.PanelVersion, GUID: status.PanelGUID, Resources: resourcesOf(status),
		PublicIPv4: reportedIP(status.PublicIP.IPv4), PublicIPv6: reportedIP(status.PublicIP.IPv6),
		CoreState: status.Xray.State, CoreError: status.Xray.ErrorMsg,
	}
	info.PublicIP = info.PublicIPv4
	if info.PublicIP == "" {
		info.PublicIP = info.PublicIPv6
	}
	return info, nil
}

// resourcesOf 把 3x-ui 的状态映射成中立模型（Ruling-83）；loads 不足三项时缺的按 0。
func resourcesOf(status serverStatus) *panel.Resources {
	loads := [loadWindows]float64{}
	copy(loads[:], status.Loads)
	return &panel.Resources{
		CPU: status.CPU, CPUCores: status.CPUCores,
		MemUsed: status.Mem.Current, MemTotal: status.Mem.Total,
		SwapUsed: status.Swap.Current, SwapTotal: status.Swap.Total,
		DiskUsed: status.Disk.Current, DiskTotal: status.Disk.Total,
		Load1: loads[0], Load5: loads[1], Load15: loads[2],
		UptimeSeconds: status.Uptime, TCPCount: status.TCPCount, UDPCount: status.UDPCount,
		NetUpRate: status.NetIO.Up, NetDownRate: status.NetIO.Down,
		NetSent: status.NetTraffic.Sent, NetRecv: status.NetTraffic.Recv,
		CoreState: status.Xray.State, CoreVersion: status.Xray.Version,
	}
}

// reportedIP 去掉 3x-ui 取不到公网 IP 时填的占位。
func reportedIP(ip string) string {
	if ip == publicIPUnknown {
		return ""
	}
	return ip
}

var _ panel.Provider = (*Client)(nil)
