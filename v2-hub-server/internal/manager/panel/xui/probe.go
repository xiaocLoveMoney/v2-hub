package xui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"v2-hub-server/internal/manager/panel"
)

// 3x-ui 出站测试与网页证书路径（contract/upstream/3x-ui/openapi-v3.11.0.json 的 Xray / Server 两组）。
const (
	pathTestOutbound = "/xray/testOutbound"
	pathWebCertFiles = "/server/getWebCertFiles"
	formOutbound     = "outbound"
	formMode         = "mode"
	// 面板的 mode：tcp、real，其余（含空串）是默认的 HTTP 探测。
	xuiModeTCP  = "tcp"
	xuiModeReal = "real"
	xuiModeHTTP = ""
	// outboundTestTimeout 是一次测试的请求时限：面板的 HTTP 探测冷、热请求各 10 秒，外加出口 IP 查询 3 秒。
	outboundTestTimeout = 30 * time.Second
)

var (
	_ panel.OutboundTester = (*Client)(nil)
	_ panel.CertLocator    = (*Client)(nil)
)

// testOutboundResp 是 POST /xray/testOutbound 的 obj（3x-ui v3.11.0 的 TestOutboundResult）。
type testOutboundResp struct {
	Success    bool   `json:"success"`
	Delay      int64  `json:"delay"`
	Error      string `json:"error"`
	Mode       string `json:"mode"`
	HTTPStatus int    `json:"httpStatus"`
	Egress     *struct {
		IPv4    string `json:"ipv4"`
		IPv6    string `json:"ipv6"`
		Country string `json:"country"`
	} `json:"egress"`
}

func xuiMode(mode panel.OutboundTestMode) string {
	switch mode {
	case panel.OutboundTestTCP:
		return xuiModeTCP
	case panel.OutboundTestReal:
		return xuiModeReal
	default:
		return xuiModeHTTP
	}
}

func neutralMode(reported string, asked panel.OutboundTestMode) panel.OutboundTestMode {
	switch panel.OutboundTestMode(reported) {
	case panel.OutboundTestTCP, panel.OutboundTestHTTP, panel.OutboundTestReal:
		return panel.OutboundTestMode(reported)
	default:
		return asked
	}
}

// TestOutbound 实现 panel.OutboundTester。面板以“请求被拒绝”回应时（出站不能测、另一项测试在跑等）也算一次失败的结果，
// 只有面板连不上或 token 无效才返回错误。
func (c *Client) TestOutbound(ctx context.Context, outbound json.RawMessage, mode panel.OutboundTestMode) (panel.OutboundTestResult, error) {
	form := url.Values{formOutbound: {string(outbound)}, formMode: {xuiMode(mode)}}
	slow := *c.http
	slow.Timeout = outboundTestTimeout
	var resp testOutboundResp
	err := c.sendWith(ctx, &slow, http.MethodPost, pathTestOutbound, contentTypeForm, []byte(form.Encode()), &resp)
	if pe := panel.AsError(err); pe != nil && pe.Kind == panel.KindRejected {
		return panel.OutboundTestResult{Mode: mode, Message: pe.Message}, nil
	}
	if err != nil {
		return panel.OutboundTestResult{}, err
	}
	out := panel.OutboundTestResult{
		OK: resp.Success, Mode: neutralMode(resp.Mode, mode), HTTPStatus: resp.HTTPStatus, Message: truncate(resp.Error),
	}
	if resp.Success {
		out.DelayMs = resp.Delay
	}
	if resp.Egress != nil {
		out.EgressIP, out.EgressRegion = resp.Egress.IPv4, resp.Egress.Country
		if out.EgressIP == "" {
			out.EgressIP = resp.Egress.IPv6
		}
	}
	return out, nil
}

// webCertResp 是 GET /server/getWebCertFiles 的 obj。
type webCertResp struct {
	WebCertFile string `json:"webCertFile"`
	WebKeyFile  string `json:"webKeyFile"`
}

// WebCertFiles 实现 panel.CertLocator；面板没配网页证书时两个路径都是空串。
func (c *Client) WebCertFiles(ctx context.Context) (panel.CertFiles, error) {
	var resp webCertResp
	if err := c.call(ctx, http.MethodGet, pathWebCertFiles, nil, &resp); err != nil {
		return panel.CertFiles{}, err
	}
	return panel.CertFiles{CertFile: resp.WebCertFile, KeyFile: resp.WebKeyFile}, nil
}

// 3x-ui REALITY 目标扫描（Server 组）。allowPrivate 从不发送：面板默认不探测内网地址。
const (
	pathScanRealityTarget  = "/server/scanRealityTarget"
	pathScanRealityTargets = "/server/scanRealityTargets"
	formTarget             = "target"
	formTargets            = "targets"
	formSNI                = "sni"
	formXver               = "xver"
	targetSeparator        = ","
	// realityScanTimeout 是一次扫描的请求时限：候选站点逐个握手，比普通调用慢。
	realityScanTimeout = 45 * time.Second
)

var _ panel.RealityScanner = (*Client)(nil)

// realityScanResp 是面板的 RealityScanResult（只取平台用到的字段）。
type realityScanResp struct {
	Target        string   `json:"target"`
	Host          string   `json:"host"`
	Port          int      `json:"port"`
	IP            string   `json:"ip"`
	Feasible      bool     `json:"feasible"`
	Reason        string   `json:"reason"`
	LatencyMs     int64    `json:"latencyMs"`
	TLSVersion    string   `json:"tlsVersion"`
	ALPN          string   `json:"alpn"`
	CertValid     bool     `json:"certValid"`
	CertSubject   string   `json:"certSubject"`
	CertIssuer    string   `json:"certIssuer"`
	ServerNames   []string `json:"serverNames"`
	PrivateTarget bool     `json:"privateTarget"`
}

func (r realityScanResp) neutral() panel.RealityScan {
	names := make([]string, 0, len(r.ServerNames))
	for _, n := range r.ServerNames {
		if n != "" {
			names = append(names, n)
		}
	}
	return panel.RealityScan{
		Target: r.Target, Host: r.Host, Port: r.Port, IP: r.IP, Feasible: r.Feasible, Reason: truncate(r.Reason), LatencyMs: r.LatencyMs,
		TLSVersion: r.TLSVersion, ALPN: r.ALPN, CertValid: r.CertValid, CertSubject: r.CertSubject, CertIssuer: r.CertIssuer,
		ServerNames: names, PrivateTarget: r.PrivateTarget,
	}
}

func (c *Client) scanForm(ctx context.Context, path string, form url.Values, out any) error {
	slow := *c.http
	slow.Timeout = realityScanTimeout
	return c.sendWith(ctx, &slow, http.MethodPost, path, contentTypeForm, []byte(form.Encode()), out)
}

// ScanRealityTarget 实现 panel.RealityScanner。
func (c *Client) ScanRealityTarget(ctx context.Context, target, sni string, xver int) (panel.RealityScan, error) {
	form := url.Values{formTarget: {target}}
	if sni != "" {
		form.Set(formSNI, sni)
	}
	if xver > 0 {
		form.Set(formXver, strconv.Itoa(xver))
	}
	var resp realityScanResp
	if err := c.scanForm(ctx, pathScanRealityTarget, form, &resp); err != nil {
		return panel.RealityScan{}, err
	}
	return resp.neutral(), nil
}

// ScanRealityTargets 实现 panel.RealityScanner；targets 为空时面板探测自己的候选站点。
func (c *Client) ScanRealityTargets(ctx context.Context, targets []string) ([]panel.RealityScan, error) {
	form := url.Values{}
	if len(targets) > 0 {
		form.Set(formTargets, strings.Join(targets, targetSeparator))
	}
	var resp []realityScanResp
	if err := c.scanForm(ctx, pathScanRealityTargets, form, &resp); err != nil {
		return nil, err
	}
	out := make([]panel.RealityScan, 0, len(resp))
	for _, r := range resp {
		out = append(out, r.neutral())
	}
	return out, nil
}
