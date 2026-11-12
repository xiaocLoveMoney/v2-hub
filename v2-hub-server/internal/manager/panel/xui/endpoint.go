package xui

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"v2-hub-server/internal/manager/panel"
)

// hostSecuritySame 表示 Host 沿用入站自身的 TLS 设置。
const hostSecuritySame = "same"

// hostRow 是 /hosts/raw 返回的一行 Host（平台用到的字段）。
type hostRow struct {
	GroupID    string `json:"groupId"`
	InboundID  int64  `json:"inboundId"`
	Address    string `json:"address"`
	Port       int    `json:"port"`
	Remark     string `json:"remark"`
	IsDisabled bool   `json:"isDisabled"`
}

// ListEndpoints 实现 panel.Provider。
func (c *Client) ListEndpoints(ctx context.Context) ([]panel.Endpoint, error) {
	var rows []hostRow
	if err := c.call(ctx, http.MethodGet, "/hosts/raw", nil, &rows); err != nil {
		return nil, err
	}
	out := make([]panel.Endpoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, panel.Endpoint{
			GroupRef: r.GroupID, InboundRef: strconv.FormatInt(r.InboundID, 10),
			Address: r.Address, Port: r.Port, Remark: r.Remark, Disabled: r.IsDisabled,
		})
	}
	return out, nil
}

// CreateEndpoints 实现 panel.Provider：在面板上新建一个 Host 组，挂到给定入站。
func (c *Client) CreateEndpoints(ctx context.Context, spec panel.EndpointSpec) error {
	ids := make([]int64, 0, len(spec.InboundRefs))
	for _, ref := range spec.InboundRefs {
		id, err := strconv.ParseInt(ref, 10, 64)
		if err != nil {
			return &panel.Error{Kind: panel.KindRejected, Message: "入站标识不合法"}
		}
		ids = append(ids, id)
	}
	body := map[string]any{
		"inboundIds": ids,
		"remark":     spec.Remark,
		"hosts":      spec.Addresses,
		"port":       spec.Port,
		"security":   hostSecuritySame,
	}
	return c.call(ctx, http.MethodPost, "/hosts/add", body, nil)
}

// DeleteEndpointGroup 实现 panel.Provider。
func (c *Client) DeleteEndpointGroup(ctx context.Context, groupRef string) error {
	if groupRef == "" {
		return &panel.Error{Kind: panel.KindNotFound, Message: "Host 组标识为空"}
	}
	return c.call(ctx, http.MethodPost, "/hosts/del/"+url.PathEscape(groupRef), nil, nil)
}
