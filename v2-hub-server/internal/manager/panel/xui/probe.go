package xui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
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
