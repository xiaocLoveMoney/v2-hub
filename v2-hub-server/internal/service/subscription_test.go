package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"v2-hub-server/internal/manager/panel/paneltest"
	"v2-hub-server/internal/manager/render"
)

const (
	subRequestBase = "https://user.example.com"
	fakePanelHost  = "panel-a.example.com" // fakeBase 的主机名：直连节点对外用它
)

type subFixture struct {
	*userFixture
	user      *User
	principal *UserPrincipal
}

// newSubFixture：一个 10 GiB / 30 天套餐的用户，已对账到内存面板（两个直连节点：vless:443、trojan:8443）。
func newSubFixture(t *testing.T) *subFixture {
	t.Helper()
	uf := newUserFixture(t)
	plan := uf.plan(t, "basic", PlanQuota{TrafficLimit: 10 * gib, DurationDays: 30})
	u := uf.newUser(t, "alice", plan.ID)
	uf.reconcile(t)
	return &subFixture{userFixture: uf, user: u, principal: &UserPrincipal{UserID: u.ID, Username: u.Username, Status: UserStatusActive}}
}

func (f *subFixture) token(t *testing.T) string {
	t.Helper()
	info, err := f.users.GetSubscription(context.Background(), f.principal, subRequestBase)
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	prefix := subRequestBase + "/sub/"
	if !strings.HasPrefix(info.URL, prefix) || len(info.Formats) != 4 || info.UpdateIntervalHours != 12 {
		t.Fatalf("subscription info = %+v", info)
	}
	return strings.TrimPrefix(info.URL, prefix)
}

func (f *subFixture) render(t *testing.T, token, format, ua string) *SubscriptionResult {
	t.Helper()
	res, err := f.users.RenderSubscription(context.Background(), SubscriptionRequest{Token: token, Format: format, UserAgent: ua})
	if err != nil {
		t.Fatalf("RenderSubscription(%s, %q): %v", format, ua, err)
	}
	return res
}

func decodeBase64Sub(t *testing.T, body []byte) []render.ProxyNode {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(string(body))
	if err != nil {
		t.Fatalf("subscription body is not base64: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	out := make([]render.ProxyNode, 0, len(lines))
	for _, l := range lines {
		n, err := render.ParseLink(l)
		if err != nil {
			t.Fatalf("unparsable link in subscription: %v", err)
		}
		out = append(out, n)
	}
	return out
}

func nodeNames(nodes []render.ProxyNode) []string {
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		names = append(names, n.Name)
	}
	return names
}

func TestSubscriptionRenderFormats(t *testing.T) {
	f := newSubFixture(t)
	ctx := context.Background()
	token := f.token(t)
	if again := f.token(t); again != token {
		t.Fatal("GetSubscription should return the same token until reset")
	}

	// 未指定格式：普通客户端得到 Base64，前两条是说明节点，后面是按节点目录命名、地址换成服务器地址的节点
	res := f.render(t, token, "", "v2rayN/6.42")
	nodes := decodeBase64Sub(t, res.Body)
	if len(nodes) != 4 {
		t.Fatalf("base64 nodes = %q", nodeNames(nodes))
	}
	if !strings.HasPrefix(nodes[0].Name, "剩余流量：10.00 GB") || !strings.HasPrefix(nodes[1].Name, "到期：") {
		t.Fatalf("info nodes = %q", nodeNames(nodes[:2]))
	}
	want := []string{f.nodes[0].DisplayName, f.nodes[1].DisplayName}
	slices.Sort(want)
	got := nodeNames(nodes[2:])
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("node names = %q, want %q", got, want)
	}
	for _, n := range nodes[2:] {
		if n.Address != fakePanelHost {
			t.Fatalf("direct node address = %q, want %q (not the panel's own %q)", n.Address, fakePanelHost, paneltest.FakeHost)
		}
	}
	if ui := res.Headers[headerUserinfo]; !strings.Contains(ui, "download=0") || !strings.Contains(ui, "total=10737418240") || !strings.Contains(ui, "expire=") {
		t.Fatalf("userinfo header = %q", ui)
	}
	if res.Headers[headerInterval] != "12" || !strings.HasPrefix(res.ContentType, "text/plain") {
		t.Fatalf("headers = %v, content type = %q", res.Headers, res.ContentType)
	}

	// 按 User-Agent 识别 Mihomo / sing-box；显式 format 优先
	if m := f.render(t, token, "", "clash-verge/v2.0.3"); !strings.Contains(string(m.Body), render.GroupSelect) || !strings.Contains(string(m.Body), want[0]) || !strings.HasPrefix(m.ContentType, "application/yaml") {
		t.Fatalf("mihomo output unexpected (content type %q)", m.ContentType)
	}
	var sb struct {
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(f.render(t, token, "", "SFA/1.11.0").Body, &sb); err != nil || len(sb.Outbounds) != 7 {
		t.Fatalf("sing-box outbounds = %+v, %v; want selector, urltest, 2 info, 2 nodes, direct", sb.Outbounds, err)
	}
	var xr []struct {
		Remarks string `json:"remarks"`
	}
	if err := json.Unmarshal(f.render(t, token, render.FormatXray, "clash").Body, &xr); err != nil || len(xr) != 2 {
		t.Fatalf("xray configs = %+v, %v", xr, err)
	}

	if _, err := f.users.RenderSubscription(ctx, SubscriptionRequest{Token: token, Format: "surge"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("bad format: err = %v, want invalid_argument", err)
	}
	if _, err := f.users.RenderSubscription(ctx, SubscriptionRequest{Token: "no-such-token-000000000000"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token: err = %v, want not_found", err)
	}

	// 四次成功拉取各记一条；本人只看到自己的
	logs, err := f.users.ListSubscribeLogs(ctx, f.user.ID, PageQuery{})
	if err != nil || logs.Total != 4 || logs.Items[0].Format != render.FormatXray || logs.Items[3].UserAgent != "v2rayN/6.42" {
		t.Fatalf("subscribe logs = %+v, %v", logs, err)
	}
	if other, err := f.users.ListSubscribeLogs(ctx, f.user.ID+999, PageQuery{}); err != nil || other.Total != 0 {
		t.Fatalf("other user's logs = %+v, %v", other, err)
	}
	f.clock.advance(31 * 24 * time.Hour)
	if n, err := f.users.PurgeSubscribeLogs(ctx); err != nil || n != 4 {
		t.Fatalf("PurgeSubscribeLogs = %d, %v", n, err)
	}
}

func TestSubscriptionResetRotatesCredential(t *testing.T) {
	f := newSubFixture(t)
	ctx := context.Background()
	oldToken := f.token(t)
	oldLink, err := f.users.GetNodeLink(ctx, f.principal, f.nodes[0].ID)
	if err != nil {
		t.Fatalf("GetNodeLink: %v", err)
	}

	info, err := f.users.ResetOwnSubscription(ctx, f.principal, subRequestBase, nil)
	if err != nil {
		t.Fatalf("ResetOwnSubscription: %v", err)
	}
	newToken := strings.TrimPrefix(info.URL, subRequestBase+"/sub/")
	if newToken == oldToken {
		t.Fatal("reset must issue a new token")
	}
	if _, err := f.users.RenderSubscription(ctx, SubscriptionRequest{Token: oldToken}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old token: err = %v, want not_found", err)
	}
	if ps, _ := f.users.ListUserProvisions(ctx, f.user.ID); len(ps) != 1 || ps[0].Status != ProvisionPending {
		t.Fatalf("provisions after reset = %+v, want pending", ps)
	}

	fake := f.fleet.get(fakeBase)
	f.reconcile(t)
	if !slices.Contains(fake.Calls, "SetClientCredential") {
		t.Fatalf("reconcile should rotate the credential on the panel; calls = %v", fake.Calls)
	}
	newLink, err := f.users.GetNodeLink(ctx, f.principal, f.nodes[0].ID)
	if err != nil || newLink == oldLink {
		t.Fatalf("link after rotation = %v (changed=%v)", err, newLink != oldLink)
	}
	if nodes := decodeBase64Sub(t, f.render(t, newToken, render.FormatBase64, "").Body); len(nodes) != 4 {
		t.Fatalf("nodes after reset = %q", nodeNames(nodes))
	}
	// 凭据已是最新：再对账不应重复换
	before := len(fake.Calls)
	f.reconcile(t)
	if slices.Contains(fake.Calls[before:], "SetClientCredential") || slices.Contains(fake.Calls[before:], "ClientLinks") {
		t.Fatalf("idle reconcile should not touch credentials or links; calls = %v", fake.Calls[before:])
	}

	if err := f.users.ResetUserSubscription(ctx, f.actor, f.user.ID, nil); err != nil {
		t.Fatalf("ResetUserSubscription: %v", err)
	}
	if _, err := f.users.RenderSubscription(ctx, SubscriptionRequest{Token: newToken}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("token after admin reset: err = %v, want not_found", err)
	}
	if err := f.users.ResetUserSubscription(ctx, f.actor, f.user.ID+999, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reset unknown user: err = %v, want not_found", err)
	}
}

func TestSubscriptionEndpointMatchingAndInactiveUser(t *testing.T) {
	f := newSubFixture(t)
	ctx := context.Background()
	token := f.token(t)
	vless := f.nodes[0]
	if vless.Protocol != "vless" {
		vless = f.nodes[1]
	}

	// 给 vless 入站加一个对外地址：面板从此只为它出对外地址的链接，直连节点没有链接可用
	if err := f.panels.CreateEndpoints(ctx, f.actor, f.server.ID, EndpointInput{InboundIDs: []int64{vless.InboundID}, Addresses: []string{"cdn.example.net"}, Remark: "cdn"}, nil); err != nil {
		t.Fatalf("CreateEndpoints: %v", err)
	}
	f.reconcile(t)
	nodes := decodeBase64Sub(t, f.render(t, token, render.FormatBase64, "").Body)
	if len(nodes) != 3 || nodes[2].Protocol != render.ProtoTrojan {
		t.Fatalf("with an unauthorized endpoint: nodes = %q; want 2 info + the trojan node only", nodeNames(nodes))
	}
	if _, err := f.users.GetNodeLink(ctx, f.principal, vless.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("direct node without link: err = %v, want not_found", err)
	}

	// 把对外地址节点加进节点组后，它按自己的地址输出
	all, err := f.panels.ListNodes(ctx, NodeFilter{ServerID: f.server.ID}, PageQuery{})
	if err != nil || all.Total != 3 {
		t.Fatalf("nodes = %+v, %v", all, err)
	}
	ids := make([]int64, 0, 3)
	for _, n := range all.Items {
		ids = append(ids, n.ID)
	}
	if err := f.panels.UpdateNodeGroup(ctx, f.actor, f.group.ID, NodeGroupInput{Name: f.group.Name, NodeIDs: ids}, nil); err != nil {
		t.Fatalf("UpdateNodeGroup: %v", err)
	}
	nodes = decodeBase64Sub(t, f.render(t, token, render.FormatBase64, "").Body)
	addrs := make([]string, 0, 2)
	for _, n := range nodes[2:] {
		addrs = append(addrs, n.Address)
	}
	slices.Sort(addrs)
	if !slices.Equal(addrs, []string{"cdn.example.net", fakePanelHost}) {
		t.Fatalf("addresses = %q, want the endpoint address and the server address", addrs)
	}

	// 流量用尽：只剩一条说明节点，节点链接不可看
	q := f.user.Quota
	if _, err := f.users.UpdateQuota(ctx, f.actor, f.user.ID, UserQuotaInput{
		TrafficLimit: q.TrafficLimit, TrafficUsed: q.TrafficLimit, ExpiresAt: q.ExpiresAt, TrafficReset: q.TrafficReset, TrafficResetDay: q.TrafficResetDay,
	}, nil); err != nil {
		t.Fatalf("UpdateQuota: %v", err)
	}
	nodes = decodeBase64Sub(t, f.render(t, token, render.FormatBase64, "").Body)
	if len(nodes) != 1 || nodes[0].Name != "流量已用尽" {
		t.Fatalf("limited user nodes = %q", nodeNames(nodes))
	}
	f.principal.Status = UserStatusLimited
	if _, err := f.users.GetNodeLink(ctx, f.principal, vless.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("link for limited user: err = %v, want conflict", err)
	}
}

func TestSubscriptionRateLimits(t *testing.T) {
	f := newSubFixture(t)
	ctx := context.Background()
	token := f.token(t)
	for i := range subTokenRatePerMinute {
		if _, err := f.users.RenderSubscription(ctx, SubscriptionRequest{Token: token}); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if _, err := f.users.RenderSubscription(ctx, SubscriptionRequest{Token: token}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("over token rate: err = %v, want rate_limited", err)
	}
	f.clock.advance(subRateWindow + time.Second)
	if _, err := f.users.RenderSubscription(ctx, SubscriptionRequest{Token: token}); err != nil {
		t.Fatalf("after window: %v", err)
	}

	// 同一 IP 连续猜 token 被锁后，正确的 token 也拒绝
	ip := netip.MustParseAddr("203.0.113.9")
	for i := range subMissMaxFailures {
		if _, err := f.users.RenderSubscription(ctx, SubscriptionRequest{Token: "guess-000000000000000000", ClientIP: &ip}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("guess %d: err = %v, want not_found", i, err)
		}
	}
	if _, err := f.users.RenderSubscription(ctx, SubscriptionRequest{Token: token, ClientIP: &ip}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("after guessing: err = %v, want rate_limited", err)
	}
}

func TestPortalDashboardNodesAndTraffic(t *testing.T) {
	f := newSubFixture(t)
	ctx := context.Background()
	fake := f.fleet.get(fakeBase)
	email := f.emailOf(t, f.user.ID)

	d, err := f.users.GetDashboard(ctx, f.principal)
	if err != nil || d.PlanName != "basic" || d.NodeCount != 2 || d.OnlineDevices != 0 || d.LastUsedAt != nil || d.TrafficLimit != 10*gib || d.ExpiresAt == nil {
		t.Fatalf("dashboard = %+v, %v", d, err)
	}

	fake.AddTraffic(email, 100, 400)
	fake.SetOnline(email, "someone-else")
	if err := f.users.CollectServerTraffic(ctx, f.server.ID); err != nil {
		t.Fatalf("CollectServerTraffic: %v", err)
	}
	d, err = f.users.GetDashboard(ctx, f.principal)
	if err != nil || d.TrafficUsed != 500 || d.TrafficToday != 500 || d.OnlineDevices != 1 || d.LastUsedAt == nil {
		t.Fatalf("dashboard after traffic = %+v, %v", d, err)
	}

	groups, err := f.users.ListPortalNodes(ctx, f.principal)
	if err != nil || len(groups) != 1 || groups[0].Name != "basic" || len(groups[0].Nodes) != 2 {
		t.Fatalf("portal nodes = %+v, %v", groups, err)
	}
	n := groups[0].Nodes[0]
	if !n.IsServerOnline || n.ServerLoad == nil || *n.ServerLoad != 1.5 || n.OnlineClients != 2 {
		t.Fatalf("portal node = %+v", n)
	}

	points, err := f.users.ListTraffic(ctx, f.principal, 0)
	if err != nil || len(points) != 1 || points[0].Up != 100 || points[0].Down != 400 {
		t.Fatalf("traffic = %+v, %v", points, err)
	}
	if _, err := f.users.ListTraffic(ctx, f.principal, maxTrafficHours+1); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("hours too large: err = %v, want invalid_argument", err)
	}
	// 另一个用户看不到别人的数据：身份只取自会话
	bob := f.newUser(t, "bob", 0)
	other := &UserPrincipal{UserID: bob.ID, Username: "bob", Status: UserStatusActive}
	if d, err := f.users.GetDashboard(ctx, other); err != nil || d.TrafficUsed != 0 || d.NodeCount != 0 || d.PlanName != "" {
		t.Fatalf("bob's dashboard = %+v, %v", d, err)
	}
	if _, err := f.users.GetNodeLink(ctx, other, f.nodes[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob reading alice's node link: err = %v, want not_found", err)
	}
}
