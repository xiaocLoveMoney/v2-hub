package xui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"v2-hub-server/internal/manager/panel"
)

// 3x-ui 内核配置相关的路径与字段（contract/upstream/3x-ui/openapi-v3.11.0.json 的 Xray / Server 两组）。
const (
	pathXrayTemplate     = "/xray/"
	pathXrayUpdate       = "/xray/update"
	pathXrayRestart      = "/server/restartXrayService"
	pathServerStatus     = "/server/status"
	formXraySetting      = "xraySetting"
	formOutboundTestURL  = "outboundTestUrl"
	xrayStateRunning     = "running"
	extraOutboundTestURL = formOutboundTestURL
)

var _ panel.CoreConfigurator = (*Client)(nil)

// xrayTemplateResp 是 POST /xray/ 的 obj（面板把整个对象再编码成 JSON 字符串返回）。
// xraySetting 实际是对象，上游示例里是字符串，两种都接受。
type xrayTemplateResp struct {
	XraySetting     json.RawMessage `json:"xraySetting"`
	OutboundTestURL string          `json:"outboundTestUrl"`
}

// GetCoreTemplate 实现 panel.CoreConfigurator。
func (c *Client) GetCoreTemplate(ctx context.Context) (panel.CoreTemplate, error) {
	var encoded string
	if err := c.call(ctx, http.MethodPost, pathXrayTemplate, nil, &encoded); err != nil {
		return panel.CoreTemplate{}, err
	}
	var resp xrayTemplateResp
	if err := json.Unmarshal([]byte(encoded), &resp); err != nil {
		return panel.CoreTemplate{}, &panel.Error{Kind: panel.KindUnreachable, Message: "面板返回的内核配置不是预期的 JSON", Err: err}
	}
	config, err := unwrapJSONString(resp.XraySetting)
	if err != nil {
		return panel.CoreTemplate{}, &panel.Error{Kind: panel.KindUnreachable, Message: "面板返回的内核配置不是预期的 JSON", Err: err}
	}
	return panel.CoreTemplate{Config: config, Extra: map[string]string{extraOutboundTestURL: resp.OutboundTestURL}}, nil
}

// unwrapJSONString 把“JSON 字符串里装着 JSON”的值解开一层；本来就是对象的原样返回。
func unwrapJSONString(raw json.RawMessage) (json.RawMessage, error) {
	var inner string
	if err := json.Unmarshal(raw, &inner); err != nil {
		var probe map[string]json.RawMessage
		if objErr := json.Unmarshal(raw, &probe); objErr != nil {
			return nil, objErr
		}
		return raw, nil
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(inner), &probe); err != nil {
		return nil, err
	}
	return json.RawMessage(inner), nil
}

// SetCoreTemplate 实现 panel.CoreConfigurator：表单提交整份模板，并带回读出时的出站测试地址，避免被重置为默认值。
func (c *Client) SetCoreTemplate(ctx context.Context, tpl panel.CoreTemplate) error {
	form := url.Values{formXraySetting: {string(tpl.Config)}}
	if v := tpl.Extra[extraOutboundTestURL]; v != "" {
		form.Set(formOutboundTestURL, v)
	}
	return c.callForm(ctx, pathXrayUpdate, form, nil)
}

// RestartCore 实现 panel.CoreConfigurator。
func (c *Client) RestartCore(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, pathXrayRestart, nil, nil)
}

// CoreState 实现 panel.CoreConfigurator：取 /server/status 里的 xray 段。
func (c *Client) CoreState(ctx context.Context) (panel.CoreState, error) {
	var status struct {
		Xray struct {
			State    string `json:"state"`
			ErrorMsg string `json:"errorMsg"`
		} `json:"xray"`
	}
	if err := c.call(ctx, http.MethodGet, pathServerStatus, nil, &status); err != nil {
		return panel.CoreState{}, err
	}
	return panel.CoreState{
		Running: status.Xray.State == xrayStateRunning,
		State:   status.Xray.State,
		Message: truncate(status.Xray.ErrorMsg),
	}, nil
}
