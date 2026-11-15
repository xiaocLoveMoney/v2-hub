package chain

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

const regexpPrefix = "regexp:"

// Xray 按 Go 正则编译 regexp: 条件，编译不过内核起不来：这里先编译一遍。
func TestBlockDomainsCompile(t *testing.T) {
	count := 0
	for _, d := range blockDomains {
		pattern, ok := strings.CutPrefix(d, regexpPrefix)
		if !ok {
			continue
		}
		count++
		if _, err := regexp.Compile(pattern); err != nil {
			t.Fatalf("%s 编译不过：%v", d, err)
		}
	}
	if count != 7 {
		t.Fatalf("应有 7 条正则，实际 %d", count)
	}
	re := regexp.MustCompile(strings.TrimPrefix(blockDomains[3], regexpPrefix))
	if !re.MatchString("www.360.cn") || re.MatchString("example.com") {
		t.Fatal("360 的正则应匹配 www.360.cn、不匹配 example.com")
	}
}

func TestWithBlockMerge(t *testing.T) {
	sec, _, err := Build([]Entry{{RuleID: 3, InboundTags: []string{"in-443-tcp"}, UserEmail: "u1-def456", Exit: exitNode("uuid-a")}})
	if err != nil {
		t.Fatal(err)
	}
	withBlock, err := sec.WithBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(sec.Rules) != 1 || len(sec.Outbounds) != 1 {
		t.Fatal("WithBlock 不应改动原条目")
	}
	merged, err := Merge(json.RawMessage(template), withBlock)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tags(t, merged, "outbounds"), ","); got != "direct,blocked,hub-block,hub-r3" {
		t.Fatalf("拦截出站应在平台出站最前：%s", got)
	}
	if got := strings.Join(tags(t, merged, "rules"), ","); got != "api,hub-block,hub-r3,blocked" {
		t.Fatalf("拦截路由应紧跟 api、在链式路由之前：%s", got)
	}
	if !strings.Contains(string(merged), `{"protocol":"blackhole","settings":{},"tag":"hub-block"}`) {
		t.Fatalf("拦截出站应是平台自己的黑洞出站：%s", merged)
	}

	// 面板上已是期望的样子：哈希一致；去掉拦截后再合并即撤掉
	current, err := Extract(merged)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := withBlock.Hash()
	got, _ := current.Hash()
	if want != got {
		t.Fatal("提取出的平台条目应与带拦截的期望一致")
	}
	plainHash, _ := sec.Hash()
	if plainHash == want {
		t.Fatal("带不带拦截的期望哈希应不同")
	}
	removed, err := Merge(merged, sec)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tags(t, removed, "rules"), ","); got != "api,hub-r3,blocked" {
		t.Fatalf("去掉拦截后规则不对：%s", got)
	}
}
