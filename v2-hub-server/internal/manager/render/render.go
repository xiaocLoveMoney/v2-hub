package render

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// 订阅格式（与 hub-api.yaml 的 SubscriptionFormat 逐项对应）。
const (
	FormatBase64  = "base64"
	FormatMihomo  = "mihomo"
	FormatSingBox = "singbox"
	FormatXray    = "xray"
)

// 完整配置里的固定名称与取值。
const (
	GroupSelect = "节点选择"
	GroupAuto   = "自动选择"
	tagDirect   = "direct"

	probeURL         = "https://www.gstatic.com/generate_204"
	probeIntervalSec = 300

	// 说明节点指向本机一个不会有服务的端口，只为在客户端列表里显示一行文字。
	infoAddress = "127.0.0.1"
	infoPort    = 1
	infoMethod  = "aes-128-gcm"
	infoSecret  = "info"
)

// privateCIDRs 是直连的私有与保留网段。
var privateCIDRs = []string{
	"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "100.64.0.0/10",
	"::1/128", "fc00::/7", "fe80::/10",
}

// InfoNode 返回一条不可连通的说明节点（docs/03 §5.4 信息节点）。
func InfoNode(text string) ProxyNode {
	return ProxyNode{
		Name: text, Protocol: ProtoShadowsocks, Address: infoAddress, Port: infoPort,
		Method: infoMethod, Password: infoSecret, Network: NetTCP, Security: SecNone, Info: true,
	}
}

// InfoLink 返回说明节点的分享链接（Base64 订阅用）。
func InfoLink(text string) string {
	userinfo := base64.RawURLEncoding.EncodeToString([]byte(infoMethod + ":" + infoSecret))
	link, err := Rewrite(fmt.Sprintf("%s%s%s@%s:%d", schemeSS, schemeSep, userinfo, infoAddress, infoPort), text, "")
	if err != nil {
		// 输入是本包拼出的固定形状，不会走到这里
		panic(fmt.Sprintf("render: build info link: %v", err))
	}
	return link
}

// Base64 把分享链接按行拼接后做标准 Base64（v2rayN 等客户端的通用订阅格式）。
func Base64(links []string) []byte {
	joined := strings.Join(links, "\n")
	out := make([]byte, base64.StdEncoding.EncodedLen(len(joined)))
	base64.StdEncoding.Encode(out, []byte(joined))
	return out
}

// uniqueNames 返回与 nodes 等长的名称列表：空名补默认名，重名追加序号（完整配置要求名称唯一）。
func uniqueNames(nodes []ProxyNode) []string {
	seen := make(map[string]int, len(nodes))
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		name := strings.TrimSpace(n.Name)
		if name == "" {
			name = fmt.Sprintf("%s-%s", n.Protocol, n.Address)
		}
		seen[name]++
		if c := seen[name]; c > 1 {
			name = fmt.Sprintf("%s %d", name, c)
			seen[name]++
		}
		out = append(out, name)
	}
	return out
}

// filterNodes 返回 keep 认可的节点及其唯一名称，说明节点与可连通节点的名称分开列出。
func filterNodes(nodes []ProxyNode, keep func(ProxyNode) bool) (kept []ProxyNode, names, realNames []string) {
	kept = make([]ProxyNode, 0, len(nodes))
	for _, n := range nodes {
		if keep(n) {
			kept = append(kept, n)
		}
	}
	names = uniqueNames(kept)
	realNames = make([]string, 0, len(kept))
	for i, n := range kept {
		if !n.Info {
			realNames = append(realNames, names[i])
		}
	}
	return kept, names, realNames
}
