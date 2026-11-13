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

// Chain 是一条两跳链路：落地 Exit 经入口节点拨出（docs/03 §5.18，Ruling-36）。
type Chain struct {
	// Name 是链路在客户端里显示的名称；为空时用“入口名 → 落地名”。
	Name string
	// Entry 是入口节点在同一次渲染的 nodes 里的下标；越界、指向说明节点或该格式不支持的节点时，这条链路不输出。
	Entry int
	// Exit 是落地节点（平台节点或用户的自有落地节点）。
	Exit ProxyNode
}

// chainNameSep 连接入口名与落地名，作为没有起名的链路的默认名称。
const chainNameSep = " → "

// namer 给出不重复的名称：重名的追加序号（完整配置要求名称唯一）。
type namer map[string]int

func (m namer) unique(name string) string {
	m[name]++
	if c := m[name]; c > 1 {
		name = fmt.Sprintf("%s %d", name, c)
		m[name]++
	}
	return name
}

// displayName 返回节点的显示名，空名补“协议-地址”。
func displayName(n ProxyNode) string {
	if name := strings.TrimSpace(n.Name); name != "" {
		return name
	}
	return fmt.Sprintf("%s-%s", n.Protocol, n.Address)
}

// resolvedChain 是某个格式下确定要输出的一条链路。
type resolvedChain struct {
	name      string
	entryName string
	entry     ProxyNode
	exit      ProxyNode
}

// selection 是某个格式下要输出的内容：保留的节点及其唯一名称（说明节点与可连通节点的名称分开列出），以及能输出的链路。
type selection struct {
	nodes     []ProxyNode
	names     []string
	realNames []string
	chains    []resolvedChain
}

// chainNames 返回链路的名称列表（进手动选择组，不进自动测速组）。
func (s selection) chainNames() []string {
	out := make([]string, 0, len(s.chains))
	for _, c := range s.chains {
		out = append(out, c.name)
	}
	return out
}

// selectNodes 按 keep 筛出该格式支持的节点并定好名称；链路只在入口与落地都被 keep 认可、且入口是可连通节点时保留。
func selectNodes(nodes []ProxyNode, chains []Chain, keep func(ProxyNode) bool) selection {
	sel := selection{
		nodes: make([]ProxyNode, 0, len(nodes)), names: make([]string, 0, len(nodes)),
		realNames: make([]string, 0, len(nodes)), chains: make([]resolvedChain, 0, len(chains)),
	}
	names := make(namer, len(nodes)+len(chains))
	nameByIndex := make(map[int]string, len(nodes))
	for i, n := range nodes {
		if !keep(n) {
			continue
		}
		name := names.unique(displayName(n))
		sel.nodes, sel.names = append(sel.nodes, n), append(sel.names, name)
		if !n.Info {
			sel.realNames = append(sel.realNames, name)
			nameByIndex[i] = name
		}
	}
	for _, c := range chains {
		entryName, ok := nameByIndex[c.Entry]
		if !ok || c.Exit.Info || !keep(c.Exit) {
			continue
		}
		name := strings.TrimSpace(c.Name)
		if name == "" {
			name = entryName + chainNameSep + displayName(c.Exit)
		}
		sel.chains = append(sel.chains, resolvedChain{name: names.unique(name), entryName: entryName, entry: nodes[c.Entry], exit: c.Exit})
	}
	return sel
}
