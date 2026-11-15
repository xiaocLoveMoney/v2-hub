package xui

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"v2-hub-server/internal/manager/panel"
)

// hostSecuritySame 表示 Host 沿用入站自身的 TLS 设置。
const hostSecuritySame = "same"

// Host 组读写用到的字段名（/hosts/get 返回的组对象即 /hosts/update 的请求体）。
const (
	keyGroupID    = "groupId"
	keyInboundIDs = "inboundIds"
	keyHosts      = "hosts"
)

// hostRow 是 /hosts/raw、/hosts/add 返回的一行 Host（平台用到的字段）。
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
	rows, err := c.listHostRows(ctx)
	if err != nil {
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

func (c *Client) listHostRows(ctx context.Context) ([]hostRow, error) {
	var rows []hostRow
	if err := c.call(ctx, http.MethodGet, "/hosts/raw", nil, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// CreateEndpoints 实现 panel.Provider：在面板上新建一个 Host 组，挂到给定入站。
func (c *Client) CreateEndpoints(ctx context.Context, spec panel.EndpointSpec) error {
	ids, err := refsToIDs(spec.InboundRefs)
	if err != nil {
		return err
	}
	// 3x-ui 要求 Host 组有备注（空备注返回 request body failed validation），平台侧备注可选，缺省用第一个地址
	remark := spec.Remark
	if remark == "" && len(spec.Addresses) > 0 {
		remark = spec.Addresses[0]
	}
	body := map[string]any{
		keyInboundIDs: ids,
		"remark":      remark,
		keyHosts:      spec.Addresses,
		"port":        spec.Port,
		"security":    hostSecuritySame,
	}
	return c.call(ctx, http.MethodPost, "/hosts/add", body, nil)
}

// RemoveEndpoint 实现 panel.Provider。3x-ui 的 Host 组是“入站集合 × 地址集合”，没有删单行的接口，
// 所以按情况改写组（docs/03 §5.1）：只剩这一对就删组；组里只有这一个入站或这一个地址就缩小组；
// 否则拆成两组——先建新组（复制原组全部设置）再缩小原组，挑换组地址更少的拆法。
func (c *Client) RemoveEndpoint(ctx context.Context, key panel.EndpointKey) ([]panel.EndpointMove, error) {
	inboundID, err := strconv.ParseInt(key.InboundRef, 10, 64)
	if err != nil || key.GroupRef == "" {
		return nil, &panel.Error{Kind: panel.KindNotFound, Message: "对外地址标识不合法"}
	}
	rows, err := c.listHostRows(ctx)
	if err != nil {
		return nil, err
	}
	present := slices.ContainsFunc(rows, func(r hostRow) bool {
		return r.GroupID == key.GroupRef && r.InboundID == inboundID && r.Address == key.Address
	})
	if !present {
		return nil, &panel.Error{Kind: panel.KindNotFound, Message: "面板上已没有这个对外地址"}
	}
	groupPath := url.PathEscape(key.GroupRef)
	var group map[string]any
	if err := c.call(ctx, http.MethodGet, "/hosts/get/"+groupPath, nil, &group); err != nil {
		return nil, err
	}
	ids, hosts, err := groupMembers(group)
	if err != nil {
		return nil, err
	}
	otherIDs := slices.DeleteFunc(slices.Clone(ids), func(id int64) bool { return id == inboundID })
	var keptHosts, removedHosts []string
	for _, h := range hosts {
		if hostAddress(h) == key.Address {
			removedHosts = append(removedHosts, h)
		} else {
			keptHosts = append(keptHosts, h)
		}
	}
	switch {
	case len(otherIDs) == 0 && len(keptHosts) == 0:
		return nil, c.call(ctx, http.MethodPost, "/hosts/del/"+groupPath, nil, nil)
	case len(otherIDs) == 0:
		return nil, c.updateGroup(ctx, groupPath, withMembers(group, ids, keptHosts))
	case len(keptHosts) == 0:
		return nil, c.updateGroup(ctx, groupPath, withMembers(group, otherIDs, hosts))
	}
	// 拆组：要么把本入站的其余地址移走（换组 len(keptHosts) 个），要么把其它入站上的这个地址移走（换组 len(otherIDs) 个）
	moved, shrunk := withMembers(group, []int64{inboundID}, keptHosts), withMembers(group, otherIDs, hosts)
	if len(otherIDs) < len(keptHosts) {
		moved, shrunk = withMembers(group, otherIDs, removedHosts), withMembers(group, ids, keptHosts)
	}
	delete(moved, keyGroupID)
	var created []hostRow
	if err := c.call(ctx, http.MethodPost, "/hosts/add", moved, &created); err != nil {
		return nil, err
	}
	if len(created) == 0 || created[0].GroupID == "" {
		return nil, &panel.Error{Kind: panel.KindUnreachable, Message: "面板新建 Host 组后没有返回组标识"}
	}
	if err := c.updateGroup(ctx, groupPath, shrunk); err != nil {
		// 回滚新组，避免同一地址在两组里重复；回滚也失败时两个错误一起上报，由管理员到面板核对
		rollbackErr := c.call(ctx, http.MethodPost, "/hosts/del/"+url.PathEscape(created[0].GroupID), nil, nil)
		return nil, errors.Join(err, rollbackErr)
	}
	moves := make([]panel.EndpointMove, 0, len(created))
	for _, r := range created {
		moves = append(moves, panel.EndpointMove{
			InboundRef: strconv.FormatInt(r.InboundID, 10), Address: r.Address, NewGroupRef: r.GroupID,
		})
	}
	return moves, nil
}

func (c *Client) updateGroup(ctx context.Context, groupPath string, group map[string]any) error {
	return c.call(ctx, http.MethodPost, "/hosts/update/"+groupPath, group, nil)
}

// groupMembers 取出组对象里的入站 id 与地址（地址形如 host[:port]）。
func groupMembers(group map[string]any) ([]int64, []string, error) {
	rawIDs, okIDs := group[keyInboundIDs].([]any)
	rawHosts, okHosts := group[keyHosts].([]any)
	if !okIDs || !okHosts {
		return nil, nil, &panel.Error{Kind: panel.KindUnreachable, Message: "面板返回的 Host 组格式与预期不符"}
	}
	ids := make([]int64, 0, len(rawIDs))
	for _, v := range rawIDs {
		f, ok := v.(float64)
		if !ok {
			return nil, nil, &panel.Error{Kind: panel.KindUnreachable, Message: "面板返回的 Host 组入站 id 格式异常"}
		}
		ids = append(ids, int64(f))
	}
	hosts := make([]string, 0, len(rawHosts))
	for _, v := range rawHosts {
		s, ok := v.(string)
		if !ok {
			return nil, nil, &panel.Error{Kind: panel.KindUnreachable, Message: "面板返回的 Host 组地址格式异常"}
		}
		hosts = append(hosts, s)
	}
	return ids, hosts, nil
}

// withMembers 复制组对象（保留 SNI、路径等全部设置），只替换入站与地址。
func withMembers(group map[string]any, ids []int64, hosts []string) map[string]any {
	out := make(map[string]any, len(group))
	for k, v := range group {
		out[k] = v
	}
	out[keyInboundIDs] = ids
	out[keyHosts] = hosts
	return out
}

// hostAddress 去掉组地址里的端口：host:port、[v6]:port 取主机部分，不带端口的原样返回。
func hostAddress(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}
