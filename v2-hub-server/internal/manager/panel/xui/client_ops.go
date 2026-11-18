package xui

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"v2-hub-server/internal/manager/panel"
)

// 3x-ui client 的键名。
const (
	keyTotalGB = "totalGB"
	keyLimitIP = "limitIp"
	keyEmail   = "email"
	keyFlow    = "flow"
)

// clientRow 是 /clients/list 返回的一行 client（平台用到的字段）；traffic 是面板累计的流量计数。
type clientRow struct {
	Email      string  `json:"email"`
	Enable     bool    `json:"enable"`
	LimitIP    int     `json:"limitIp"`
	Flow       string  `json:"flow"`
	InboundIDs []int64 `json:"inboundIds"`
	Traffic    *struct {
		Up   int64 `json:"up"`
		Down int64 `json:"down"`
	} `json:"traffic"`
}

func refsToIDs(refs []string) ([]int64, error) {
	ids := make([]int64, 0, len(refs))
	for _, ref := range refs {
		id, err := strconv.ParseInt(ref, 10, 64)
		if err != nil {
			return nil, &panel.Error{Kind: panel.KindRejected, Message: "入站标识不合法"}
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// ListClients 实现 panel.Provider。
func (c *Client) ListClients(ctx context.Context) ([]panel.Client, error) {
	var rows []clientRow
	if err := c.call(ctx, http.MethodGet, "/clients/list", nil, &rows); err != nil {
		return nil, err
	}
	out := make([]panel.Client, 0, len(rows))
	for _, r := range rows {
		refs := make([]string, 0, len(r.InboundIDs))
		for _, id := range r.InboundIDs {
			refs = append(refs, strconv.FormatInt(id, 10))
		}
		cl := panel.Client{Email: r.Email, Enabled: r.Enable, LimitIP: r.LimitIP, Flow: r.Flow, InboundRefs: refs}
		if r.Traffic != nil {
			cl.Up, cl.Down = r.Traffic.Up, r.Traffic.Down
		}
		out = append(out, cl)
	}
	return out, nil
}

// AddClient 实现 panel.Provider：额度与到期一律为 0（不限），配额由平台判定（docs/03 §5.2）。
func (c *Client) AddClient(ctx context.Context, spec panel.ClientSpec) error {
	ids, err := refsToIDs(spec.InboundRefs)
	if err != nil {
		return err
	}
	body := map[string]any{
		"client": map[string]any{
			keyEmail: spec.Email, "id": spec.UUID, "password": spec.PasswordOrAuth(), "auth": spec.Auth, keyFlow: spec.Flow,
			keyTotalGB: 0, keyExpiryTime: 0, keyLimitIP: spec.LimitIP, keyEnable: spec.Enabled,
		},
		"inboundIds": ids,
	}
	return c.call(ctx, http.MethodPost, "/clients/add", body, nil)
}

// UpdateClient 实现 panel.Provider：面板按字段合并，未给出的凭据与挂载保持不变；flow 总是给出（空串即去掉）。
func (c *Client) UpdateClient(ctx context.Context, spec panel.ClientSpec) error {
	body := map[string]any{keyEmail: spec.Email, keyLimitIP: spec.LimitIP, keyEnable: spec.Enabled, keyFlow: spec.Flow}
	return c.call(ctx, http.MethodPost, "/clients/update/"+url.PathEscape(spec.Email), body, nil)
}

func (c *Client) mountClient(ctx context.Context, email, action string, refs []string) error {
	ids, err := refsToIDs(refs)
	if err != nil {
		return err
	}
	return c.call(ctx, http.MethodPost, "/clients/"+url.PathEscape(email)+"/"+action, map[string]any{"inboundIds": ids}, nil)
}

// AttachClient 实现 panel.Provider。
func (c *Client) AttachClient(ctx context.Context, email string, inboundRefs []string) error {
	return c.mountClient(ctx, email, "attach", inboundRefs)
}

// DetachClient 实现 panel.Provider。
func (c *Client) DetachClient(ctx context.Context, email string, inboundRefs []string) error {
	return c.mountClient(ctx, email, "detach", inboundRefs)
}

// SetClientsEnabled 实现 panel.Provider。
func (c *Client) SetClientsEnabled(ctx context.Context, emails []string, enabled bool) error {
	if len(emails) == 0 {
		return nil
	}
	path := "/clients/bulkDisable"
	if enabled {
		path = "/clients/bulkEnable"
	}
	return c.call(ctx, http.MethodPost, path, map[string]any{"emails": emails}, nil)
}

// DeleteClient 实现 panel.Provider。面板对不存在的 client 返回失败，调用方应先按 ListClients 判断。
func (c *Client) DeleteClient(ctx context.Context, email string) error {
	return c.call(ctx, http.MethodPost, "/clients/del/"+url.PathEscape(email), nil, nil)
}

// SetClientCredential 实现 panel.Provider：面板按字段合并，未给出的挂载保持不变。
func (c *Client) SetClientCredential(ctx context.Context, spec panel.ClientSpec) error {
	body := map[string]any{
		keyEmail: spec.Email, "id": spec.UUID, "password": spec.PasswordOrAuth(), "auth": spec.Auth,
		keyLimitIP: spec.LimitIP, keyEnable: spec.Enabled, keyFlow: spec.Flow,
	}
	return c.call(ctx, http.MethodPost, "/clients/update/"+url.PathEscape(spec.Email), body, nil)
}

// ClientLinks 实现 panel.Provider。
func (c *Client) ClientLinks(ctx context.Context, email string) ([]string, error) {
	var links []string
	if err := c.call(ctx, http.MethodGet, "/clients/links/"+url.PathEscape(email), nil, &links); err != nil {
		return nil, err
	}
	return links, nil
}

// OnlineClients 实现 panel.Provider。
func (c *Client) OnlineClients(ctx context.Context) ([]string, error) {
	var emails []string
	if err := c.call(ctx, http.MethodPost, "/clients/onlines", nil, &emails); err != nil {
		return nil, err
	}
	return emails, nil
}
