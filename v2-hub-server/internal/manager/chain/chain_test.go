package chain

import (
	"encoding/json"
	"strings"
	"testing"

	"v2-hub-server/internal/manager/render"
)

const template = `{"log":{"loglevel":"warning"},"outbounds":[{"tag":"direct","protocol":"freedom"},{"tag":"blocked","protocol":"blackhole"}],` +
	`"routing":{"domainStrategy":"AsIs","rules":[{"type":"field","inboundTag":["api"],"outboundTag":"api"},` +
	`{"type":"field","ip":["geoip:private"],"outboundTag":"blocked"}]}}`

func exitNode(uuid string) render.ProxyNode {
	return render.ProxyNode{Name: "落地", Protocol: render.ProtoVLESS, Address: "203.0.113.9", Port: 443, UUID: uuid,
		Network: render.NetTCP, Security: render.SecNone}
}

// tags 取出站或规则里某个字段，按顺序。
func tags(t *testing.T, config json.RawMessage, path string) []string {
	t.Helper()
	var doc struct {
		Outbounds []map[string]any `json:"outbounds"`
		Routing   struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(config, &doc); err != nil {
		t.Fatal(err)
	}
	var out []string
	if path == "outbounds" {
		for _, o := range doc.Outbounds {
			out = append(out, o["tag"].(string))
		}
		return out
	}
	for _, r := range doc.Routing.Rules {
		out = append(out, r["outboundTag"].(string))
	}
	return out
}

func TestBuildMergeExtract(t *testing.T) {
	entries := []Entry{
		{RuleID: 7, InboundTag: "in-443-tcp", UserEmail: "u2-abc123", Exit: exitNode("uuid-b")},
		{RuleID: 3, InboundTag: "in-443-tcp", UserEmail: "u1-def456", Exit: exitNode("uuid-a")},
		{RuleID: 9, InboundTag: "in-80-tcp", UserEmail: "u1-def456", Exit: render.ProxyNode{Protocol: render.ProtoHysteria2, Address: "x", Port: 1}},
	}
	sec, included, err := Build(entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(included) != 2 || included[0] != 3 || included[1] != 7 {
		t.Fatalf("应纳入 3、7，跳过 hysteria2：%v", included)
	}
	merged, err := Merge(json.RawMessage(template), sec)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tags(t, merged, "outbounds"), ","); got != "direct,blocked,hub-r3,hub-r7" {
		t.Fatalf("平台出站应追加在末尾：%s", got)
	}
	if got := strings.Join(tags(t, merged, "rules"), ","); got != "api,hub-r3,hub-r7,blocked" {
		t.Fatalf("平台规则应插在 api 之后：%s", got)
	}
	if !strings.Contains(string(merged), `"user":["u1-def456"]`) || !strings.Contains(string(merged), `"inboundTag":["in-443-tcp"]`) {
		t.Fatalf("路由应按入站与用户匹配：%s", merged)
	}
	if !strings.Contains(string(merged), `"domainStrategy":"AsIs"`) || !strings.Contains(string(merged), `"loglevel":"warning"`) {
		t.Fatal("模板其它部分应保持不变")
	}

	// 面板现状与期望一致：哈希相同；再合并一次结果不变（幂等）
	current, err := Extract(merged)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := sec.Hash()
	got, _ := current.Hash()
	if want != got {
		t.Fatal("提取出的平台条目应与期望一致")
	}
	again, err := Merge(merged, sec)
	if err != nil || string(again) != string(merged) {
		t.Fatalf("重复合并应幂等：%v", err)
	}

	// 清空：平台条目全部移除，手工加的 hub- 条目也算平台所有
	withManual := strings.Replace(string(merged), `{"tag":"blocked","protocol":"blackhole"}`, `{"tag":"blocked","protocol":"blackhole"},{"tag":"hub-manual","protocol":"freedom"}`, 1)
	cleared, err := Merge(json.RawMessage(withManual), Section{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tags(t, cleared, "outbounds"), ","); got != "direct,blocked" {
		t.Fatalf("清空后只剩原有出站：%s", got)
	}
	if got := strings.Join(tags(t, cleared, "rules"), ","); got != "api,blocked" {
		t.Fatalf("清空后只剩原有规则：%s", got)
	}
	empty, err := Extract(cleared)
	if err != nil || len(empty.Outbounds)+len(empty.Rules) != 0 {
		t.Fatalf("清空后不应再有平台条目：%v", err)
	}
}

func TestMergeWithoutRouting(t *testing.T) {
	sec, _, err := Build([]Entry{{RuleID: 1, InboundTag: "in-1", UserEmail: "u1-x", Exit: exitNode("u")}})
	if err != nil {
		t.Fatal(err)
	}
	merged, err := Merge(json.RawMessage(`{"outbounds":[{"tag":"direct","protocol":"freedom"}]}`), sec)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tags(t, merged, "rules"), ","); got != "hub-r1" {
		t.Fatalf("没有 routing 时应新建：%s", got)
	}
	if _, err := Merge(json.RawMessage(`[]`), sec); err == nil {
		t.Fatal("模板不是对象应报错")
	}
}
