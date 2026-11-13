package render

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

const (
	mihomoMixedPort = 7890
	mihomoGeoCN     = "GEOIP,CN," + mihomoDirect
	mihomoDirect    = "DIRECT"
)

func mihomoSupports(n ProxyNode) bool {
	switch n.Network {
	case NetTCP, NetWS, NetGRPC, NetHTTPUpgrade:
	default:
		return false
	}
	if n.Protocol == ProtoHysteria2 || n.Protocol == ProtoShadowsocks {
		return true
	}
	return n.Security != SecReality || n.Protocol == ProtoVLESS || n.Protocol == ProtoTrojan
}

// mihomoTransport 写入传输层选项；httpupgrade 在 Mihomo 里是 ws 的一个变体。
func mihomoTransport(p map[string]any, n ProxyNode) {
	switch n.Network {
	case NetWS, NetHTTPUpgrade:
		p["network"] = NetWS
		opts := map[string]any{"path": orDefault(n.Path, "/")}
		if n.Host != "" {
			opts["headers"] = map[string]any{"Host": n.Host}
		}
		if n.Network == NetHTTPUpgrade {
			opts["v2ray-http-upgrade"] = true
		}
		p["ws-opts"] = opts
	case NetGRPC:
		p["network"] = NetGRPC
		p["grpc-opts"] = map[string]any{"grpc-service-name": n.ServiceName}
	default:
		p["network"] = NetTCP
	}
}

// mihomoTLS 写入 TLS / Reality 选项；sniKey 因协议而异（vless、vmess 用 servername，trojan 用 sni）。
func mihomoTLS(p map[string]any, n ProxyNode, sniKey string) {
	if n.Security == SecNone {
		return
	}
	p["tls"] = true
	if n.SNI != "" {
		p[sniKey] = n.SNI
	}
	if len(n.ALPN) > 0 {
		p["alpn"] = n.ALPN
	}
	if n.Fingerprint != "" {
		p["client-fingerprint"] = n.Fingerprint
	}
	if n.AllowInsecure {
		p["skip-cert-verify"] = true
	}
	if n.Security == SecReality {
		p["reality-opts"] = map[string]any{"public-key": n.PublicKey, "short-id": n.ShortID}
	}
}

func mihomoProxy(n ProxyNode, name string) map[string]any {
	p := map[string]any{"name": name, "server": n.Address, "port": n.Port, "udp": true}
	switch n.Protocol {
	case ProtoVLESS:
		p["type"], p["uuid"] = "vless", n.UUID
		if n.Flow != "" {
			p["flow"] = n.Flow
		}
		mihomoTransport(p, n)
		mihomoTLS(p, n, "servername")
	case ProtoVMess:
		p["type"], p["uuid"], p["alterId"], p["cipher"] = "vmess", n.UUID, n.AlterID, orDefault(n.Method, vmessDefaultCipher)
		mihomoTransport(p, n)
		mihomoTLS(p, n, "servername")
	case ProtoTrojan:
		p["type"], p["password"] = "trojan", n.Password
		mihomoTransport(p, n)
		mihomoTLS(p, n, "sni")
		delete(p, "tls") // trojan 恒为 TLS，Mihomo 不接受该键
	case ProtoShadowsocks:
		p["type"], p["cipher"], p["password"] = "ss", n.Method, n.Password
	case ProtoHysteria2:
		p["type"], p["password"] = "hysteria2", n.Password
		if n.SNI != "" {
			p["sni"] = n.SNI
		}
		if n.AllowInsecure {
			p["skip-cert-verify"] = true
		}
		if n.Obfs != "" {
			p["obfs"], p["obfs-password"] = n.Obfs, n.ObfsPassword
		}
	default:
		// mihomoSupports 已排除其余协议
	}
	return p
}

// Mihomo 渲染可直接运行的 Mihomo（Clash Meta）配置：一个手动选择组、一个自动测速组，
// 规则只有私有网段直连、中国大陆地址直连、其余走代理（docs/03 §5.4）。
func Mihomo(nodes []ProxyNode) ([]byte, error) {
	kept, names, realNames := filterNodes(nodes, mihomoSupports)
	proxies := make([]map[string]any, 0, len(kept))
	for i, n := range kept {
		proxies = append(proxies, mihomoProxy(n, names[i]))
	}
	selectable := make([]string, 0, len(names)+1)
	groups := make([]map[string]any, 0, 2)
	if len(realNames) > 0 {
		selectable = append(selectable, GroupAuto)
	}
	selectable = append(selectable, names...)
	if len(selectable) == 0 {
		selectable = append(selectable, mihomoDirect)
	}
	groups = append(groups, map[string]any{"name": GroupSelect, "type": "select", "proxies": selectable})
	if len(realNames) > 0 {
		groups = append(groups, map[string]any{
			"name": GroupAuto, "type": "url-test", "url": probeURL, "interval": probeIntervalSec, "proxies": realNames,
		})
	}
	rules := make([]string, 0, len(privateCIDRs)+2)
	for _, cidr := range privateCIDRs {
		kind := "IP-CIDR"
		if isIPv6CIDR(cidr) {
			kind = "IP-CIDR6"
		}
		rules = append(rules, fmt.Sprintf("%s,%s,%s,no-resolve", kind, cidr, mihomoDirect))
	}
	rules = append(rules, mihomoGeoCN, "MATCH,"+GroupSelect)
	cfg := map[string]any{
		"mixed-port": mihomoMixedPort, "allow-lan": false, "mode": "rule", "log-level": "info",
		"proxies": proxies, "proxy-groups": groups, "rules": rules,
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal mihomo config: %w", err)
	}
	return out, nil
}

func isIPv6CIDR(cidr string) bool {
	for _, c := range cidr {
		if c == ':' {
			return true
		}
	}
	return false
}
