package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"v2-hub-server/internal/manager/render"
)

// 自有落地节点的示例链接；用户名、口令与 UUID 都是示例值，不对应任何真实节点。
const (
	chainSocksLink   = "socks5://sample-user:sample-auth-0001@home.example.net:1080#home-socks"
	chainVLESSLink   = "vless://11111111-1111-4111-8111-111111111111@exit.example.net:443?type=tcp&security=tls&sni=exit.example.net#own-vps"
	chainHy2Link     = "hysteria2://sample-auth-0001@quic.example.net:8443?sni=quic.example.net#hy2"
	chainCustomName  = "家宽落地"
	sqlSetPlanFlags  = `UPDATE plan SET allow_client_chain = $2, allow_custom_node = $3 WHERE id = (SELECT plan_id FROM end_user WHERE id = $1)`
	sqlSetNodeEnable = `UPDATE node SET is_enabled = $2 WHERE id = $1`
	sqlCountChain    = `SELECT (SELECT COUNT(*) FROM chain_rule WHERE user_id = $1) + (SELECT COUNT(*) FROM custom_node WHERE user_id = $1)`
)

type chainFixture struct {
	*subFixture
	chain *ChainService
}

func newChainFixture(t *testing.T) *chainFixture {
	t.Helper()
	sf := newSubFixture(t)
	return &chainFixture{subFixture: sf, chain: NewChainService(sf.pool, testSealer(t), nil)}
}

// setFlags 直接改用户当前套餐的两个开关。
func (f *chainFixture) setFlags(t *testing.T, chain, customNode bool) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), sqlSetPlanFlags, f.user.ID, chain, customNode); err != nil {
		t.Fatalf("set plan flags: %v", err)
	}
}

func (f *chainFixture) rules(t *testing.T) []ChainRule {
	t.Helper()
	o, err := f.chain.Overview(context.Background(), f.principal)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	return o.Rules
}

// dialers 渲染 Mihomo 订阅，返回“链路名 → 入口名”。
func (f *chainFixture) dialers(t *testing.T, token string) map[string]string {
	t.Helper()
	var cfg struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(f.render(t, token, render.FormatMihomo, "").Body, &cfg); err != nil {
		t.Fatalf("mihomo output is not valid YAML: %v", err)
	}
	out := make(map[string]string, len(cfg.Proxies))
	for _, p := range cfg.Proxies {
		if d, ok := p["dialer-proxy"].(string); ok {
			out[fmt.Sprint(p["name"])] = d
		}
	}
	return out
}

func wantErr(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: err = %v, want %v", what, err, want)
	}
}

func TestChainRulesAndCustomNodes(t *testing.T) {
	f := newChainFixture(t)
	ctx := context.Background()
	entry, exit := f.nodes[0], f.nodes[1]

	// 套餐没开：总览照常返回，新建被拒
	o, err := f.chain.Overview(ctx, f.principal)
	if err != nil || o.Flags.Chain || o.Flags.CustomNode || len(o.Nodes) != 2 || !o.Nodes[0].CanBeExit || len(o.Rules) != 0 {
		t.Fatalf("overview before enabling = %+v, %v", o, err)
	}
	_, err = f.chain.CreateRule(ctx, f.principal, ChainRuleInput{EntryNodeID: entry.ID, ExitNodeID: exit.ID, Enabled: true})
	wantErr(t, "create rule without plan flag", err, ErrFeatureDisabled)
	_, err = f.chain.CreateCustomNode(ctx, f.principal, chainSocksLink, "")
	wantErr(t, "create custom node without plan flag", err, ErrFeatureDisabled)

	// 只开链式：平台节点作落地
	f.setFlags(t, true, false)
	rule, err := f.chain.CreateRule(ctx, f.principal, ChainRuleInput{EntryNodeID: entry.ID, ExitNodeID: exit.ID, Enabled: true})
	if err != nil || rule.Status != ChainOK || rule.ExitKind != ChainExitNode || rule.DisplayName != render.ChainDefaultName(entry.DisplayName, exit.DisplayName) {
		t.Fatalf("create rule = %+v, %v", rule, err)
	}
	for what, in := range map[string]ChainRuleInput{
		"same node":      {EntryNodeID: entry.ID, ExitNodeID: entry.ID},
		"no exit":        {EntryNodeID: entry.ID},
		"both exits":     {EntryNodeID: entry.ID, ExitNodeID: exit.ID, ExitCustomNodeID: 1},
		"unknown entry":  {EntryNodeID: exit.ID + 999, ExitNodeID: exit.ID},
		"unknown exit":   {EntryNodeID: entry.ID, ExitNodeID: exit.ID + 999},
		"name with \\n":  {EntryNodeID: exit.ID, ExitNodeID: entry.ID, Name: "a\nb"},
		"name too long":  {EntryNodeID: exit.ID, ExitNodeID: entry.ID, Name: strings.Repeat("长", maxChainNameLen+1)},
		"missing entry":  {ExitNodeID: exit.ID},
		"negative entry": {EntryNodeID: -1, ExitNodeID: exit.ID},
	} {
		_, err := f.chain.CreateRule(ctx, f.principal, in)
		wantErr(t, what, err, ErrInvalidArgument)
	}
	_, err = f.chain.CreateRule(ctx, f.principal, ChainRuleInput{EntryNodeID: entry.ID, ExitNodeID: exit.ID})
	wantErr(t, "duplicate route", err, ErrConflict)

	// 自有落地节点要另一个开关
	_, err = f.chain.CreateCustomNode(ctx, f.principal, chainSocksLink, "")
	wantErr(t, "custom node needs its own flag", err, ErrFeatureDisabled)
	f.setFlags(t, true, true)
	for what, link := range map[string]string{
		"empty": "  ", "garbage": "not a link", "hysteria2": chainHy2Link, "too long": chainSocksLink + strings.Repeat("x", maxCustomNodeLinkLen),
	} {
		_, err := f.chain.CreateCustomNode(ctx, f.principal, link, "")
		wantErr(t, "custom node link "+what, err, ErrInvalidArgument)
	}
	socks, err := f.chain.CreateCustomNode(ctx, f.principal, chainSocksLink, "")
	if err != nil || socks.Name != "home-socks" || socks.Protocol != render.ProtoSocks || socks.Address != "home.example.net" || socks.Port != 1080 || socks.RuleCount != 0 {
		t.Fatalf("create custom node = %+v, %v", socks, err)
	}
	_, err = f.chain.CreateCustomNode(ctx, f.principal, chainVLESSLink, "home-socks")
	wantErr(t, "duplicate custom node name", err, ErrConflict)
	if link, err := f.chain.CustomNodeLink(ctx, f.principal, socks.ID); err != nil || link != chainSocksLink {
		t.Fatalf("CustomNodeLink mismatch, err = %v", err)
	}
	renamed, err := f.chain.UpdateCustomNode(ctx, f.principal, socks.ID, chainCustomName, chainVLESSLink)
	if err != nil || renamed.Name != chainCustomName || renamed.Protocol != render.ProtoVLESS || renamed.Address != "exit.example.net" || renamed.Port != 443 {
		t.Fatalf("update custom node = %+v, %v", renamed, err)
	}
	if kept, err := f.chain.UpdateCustomNode(ctx, f.principal, socks.ID, chainCustomName, ""); err != nil || kept.Protocol != render.ProtoVLESS {
		t.Fatalf("update without link should keep the link: %+v, %v", kept, err)
	}
	_, err = f.chain.UpdateCustomNode(ctx, f.principal, socks.ID, " ", "")
	wantErr(t, "empty custom node name", err, ErrInvalidArgument)

	customRule, err := f.chain.CreateRule(ctx, f.principal, ChainRuleInput{EntryNodeID: exit.ID, ExitCustomNodeID: socks.ID, Name: " 回家 ", Enabled: true})
	if err != nil || customRule.Status != ChainOK || customRule.ExitKind != ChainExitCustom || customRule.Name != "回家" || customRule.ExitName != chainCustomName {
		t.Fatalf("create rule with custom exit = %+v, %v", customRule, err)
	}

	// 归属（R8）：别人的规则与自有节点一律 404，自己的列表里也看不到别人的
	bob := f.newUser(t, "bob", *f.user.PlanID)
	other := &UserPrincipal{UserID: bob.ID, Username: bob.Username, Status: UserStatusActive}
	_, err = f.chain.UpdateRule(ctx, other, rule.ID, "x", true)
	wantErr(t, "update other's rule", err, ErrNotFound)
	wantErr(t, "delete other's rule", f.chain.DeleteRule(ctx, other, rule.ID), ErrNotFound)
	_, err = f.chain.CustomNodeLink(ctx, other, socks.ID)
	wantErr(t, "read other's custom node link", err, ErrNotFound)
	_, err = f.chain.UpdateCustomNode(ctx, other, socks.ID, "x", "")
	wantErr(t, "update other's custom node", err, ErrNotFound)
	wantErr(t, "delete other's custom node", f.chain.DeleteCustomNode(ctx, other, socks.ID), ErrNotFound)
	_, err = f.chain.CreateRule(ctx, other, ChainRuleInput{EntryNodeID: entry.ID, ExitCustomNodeID: socks.ID})
	wantErr(t, "use other's custom node as exit", err, ErrNotFound)
	if o, err := f.chain.Overview(ctx, other); err != nil || len(o.Rules) != 0 || len(o.CustomNodes) != 0 {
		t.Fatalf("other user's overview = %+v, %v", o, err)
	}

	// 阻断原因：自有落地开关关掉 → plan；入口停用 → entry；落地停用 → exit；全关 → 都是 plan
	reversed, err := f.chain.CreateRule(ctx, f.principal, ChainRuleInput{EntryNodeID: exit.ID, ExitNodeID: entry.ID, Enabled: true})
	if err != nil {
		t.Fatalf("create reversed rule: %v", err)
	}
	reasons := func() map[int64]string {
		out := make(map[int64]string, 3)
		for _, r := range f.rules(t) {
			out[r.ID] = r.Status + ":" + r.BlockedReason
		}
		return out
	}
	f.setFlags(t, true, false)
	if got := reasons(); got[rule.ID] != "ok:" || got[customRule.ID] != "blocked:plan" {
		t.Fatalf("reasons without custom-node flag = %v", got)
	}
	f.setFlags(t, true, true)
	if _, err := f.pool.Exec(ctx, sqlSetNodeEnable, entry.ID, false); err != nil {
		t.Fatalf("disable node: %v", err)
	}
	if got := reasons(); got[rule.ID] != "blocked:entry" || got[reversed.ID] != "blocked:exit" || got[customRule.ID] != "ok:" {
		t.Fatalf("reasons with entry node disabled = %v", got)
	}
	if _, err := f.pool.Exec(ctx, sqlSetNodeEnable, entry.ID, true); err != nil {
		t.Fatalf("enable node: %v", err)
	}
	f.setFlags(t, false, false)
	if got := reasons(); got[rule.ID] != "blocked:plan" || got[reversed.ID] != "blocked:plan" {
		t.Fatalf("reasons without chain flag = %v", got)
	}
	_, err = f.chain.UpdateRule(ctx, f.principal, rule.ID, "x", true)
	wantErr(t, "update rule without plan flag", err, ErrFeatureDisabled)
	// 套餐没开时仍然可以删
	if err := f.chain.DeleteRule(ctx, f.principal, reversed.ID); err != nil {
		t.Fatalf("delete rule without plan flag: %v", err)
	}

	// 删自有节点连带删引用它的规则；删用户清掉剩下的
	if nodes, err := listCustomNodes(ctx, f.chain.q, f.user.ID, socks.ID); err != nil || nodes[0].RuleCount != 1 {
		t.Fatalf("rule count of custom node = %+v, %v", nodes, err)
	}
	if err := f.chain.DeleteCustomNode(ctx, f.principal, socks.ID); err != nil {
		t.Fatalf("DeleteCustomNode: %v", err)
	}
	if left := f.rules(t); len(left) != 1 || left[0].ID != rule.ID {
		t.Fatalf("rules after deleting custom node = %+v", left)
	}
	if err := f.users.DeleteUser(ctx, f.actor, f.user.ID, nil); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	var left int
	if err := f.pool.QueryRow(ctx, sqlCountChain, f.user.ID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("chain rows left after deleting user = %d, %v", left, err)
	}
}

func TestChainSubscriptionOutput(t *testing.T) {
	f := newChainFixture(t)
	ctx := context.Background()
	entry, exit := f.nodes[0], f.nodes[1]
	token := f.token(t)
	f.setFlags(t, true, true)
	socks, err := f.chain.CreateCustomNode(ctx, f.principal, chainSocksLink, chainCustomName)
	if err != nil {
		t.Fatalf("CreateCustomNode: %v", err)
	}
	nodeRule, err := f.chain.CreateRule(ctx, f.principal, ChainRuleInput{EntryNodeID: entry.ID, ExitNodeID: exit.ID, Enabled: true})
	if err != nil {
		t.Fatalf("CreateRule(node exit): %v", err)
	}
	if _, err := f.chain.CreateRule(ctx, f.principal, ChainRuleInput{EntryNodeID: exit.ID, ExitCustomNodeID: socks.ID, Name: "回家", Enabled: true}); err != nil {
		t.Fatalf("CreateRule(custom exit): %v", err)
	}

	// Mihomo：两条链路各指向自己的入口；默认名是“入口名 → 落地名”
	want := map[string]string{nodeRule.DisplayName: entry.DisplayName, "回家": exit.DisplayName}
	if got := f.dialers(t, token); len(got) != 2 || got[nodeRule.DisplayName] != want[nodeRule.DisplayName] || got["回家"] != want["回家"] {
		t.Fatalf("dialer-proxy = %v, want %v", got, want)
	}
	// sing-box：两个带 detour 的出站；Xray：两个节点 + 两条链路各一份配置；Base64 不带链路
	var sb struct {
		Outbounds []struct {
			Type   string `json:"type"`
			Detour string `json:"detour"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(f.render(t, token, render.FormatSingBox, "").Body, &sb); err != nil {
		t.Fatalf("sing-box output: %v", err)
	}
	detours, socksOut := 0, 0
	for _, o := range sb.Outbounds {
		if o.Detour != "" {
			detours++
			if o.Type == "socks" {
				socksOut++
			}
		}
	}
	if detours != 2 || socksOut != 1 {
		t.Fatalf("sing-box detours = %d (socks %d), want 2 (1)", detours, socksOut)
	}
	var xr []struct {
		Remarks   string `json:"remarks"`
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(f.render(t, token, render.FormatXray, "").Body, &xr); err != nil || len(xr) != 4 || len(xr[3].Outbounds) != 3 || xr[3].Remarks != "回家" {
		t.Fatalf("xray configs = %+v, %v", xr, err)
	}
	if nodes := decodeBase64Sub(t, f.render(t, token, render.FormatBase64, "").Body); len(nodes) != 4 {
		t.Fatalf("base64 should carry no chains: %q", nodeNames(nodes))
	}

	// 用户自己关掉的规则不输出
	if _, err := f.chain.UpdateRule(ctx, f.principal, nodeRule.ID, "", false); err != nil {
		t.Fatalf("UpdateRule: %v", err)
	}
	if got := f.dialers(t, token); len(got) != 1 || got["回家"] == "" {
		t.Fatalf("dialers after disabling one rule = %v", got)
	}
	// 阻断的规则保留但不输出；条件恢复后自动回来
	f.setFlags(t, true, false)
	if got := f.dialers(t, token); len(got) != 0 {
		t.Fatalf("blocked rule must not be rendered: %v", got)
	}
	if rules := f.rules(t); len(rules) != 2 {
		t.Fatalf("blocked rules must be kept: %+v", rules)
	}
	f.setFlags(t, true, true)
	if got := f.dialers(t, token); len(got) != 1 {
		t.Fatalf("rule should come back once allowed again: %v", got)
	}
}

func TestChainLimits(t *testing.T) {
	f := newChainFixture(t)
	ctx := context.Background()
	f.setFlags(t, true, true)
	customIDs := make([]int64, 0, MaxCustomNodes)
	for i := range MaxCustomNodes {
		n, err := f.chain.CreateCustomNode(ctx, f.principal, chainSocksLink, fmt.Sprintf("落地 %d", i))
		if err != nil {
			t.Fatalf("CreateCustomNode #%d: %v", i, err)
		}
		customIDs = append(customIDs, n.ID)
	}
	_, err := f.chain.CreateCustomNode(ctx, f.principal, chainSocksLink, "多出来的")
	wantErr(t, "custom node over limit", err, ErrInvalidArgument)

	// 2 个入口 × 10 个自有落地 = 20 条，正好到上限；第 21 条（平台节点作落地）被拒
	for _, entry := range f.nodes {
		for _, id := range customIDs {
			if _, err := f.chain.CreateRule(ctx, f.principal, ChainRuleInput{EntryNodeID: entry.ID, ExitCustomNodeID: id, Enabled: true}); err != nil {
				t.Fatalf("CreateRule: %v", err)
			}
		}
	}
	if n := len(f.rules(t)); n != MaxChainRules {
		t.Fatalf("rules = %d, want %d", n, MaxChainRules)
	}
	_, err = f.chain.CreateRule(ctx, f.principal, ChainRuleInput{EntryNodeID: f.nodes[0].ID, ExitNodeID: f.nodes[1].ID, Enabled: true})
	wantErr(t, "rule over limit", err, ErrInvalidArgument)
}
