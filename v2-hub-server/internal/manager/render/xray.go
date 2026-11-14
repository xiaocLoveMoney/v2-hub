package render

import (
	"encoding/json"
	"fmt"
)

const (
	xraySocksPort = 10808
	xrayHTTPPort  = 10809
	xrayListen    = "127.0.0.1"
	xrayProxyTag  = "proxy"
	// xrayChainEntryTag 是链路配置里入口出站的 tag（docs/03 §6）。
	xrayChainEntryTag = "chain-entry"
)

func xraySupports(n ProxyNode) bool {
	if n.Info || n.Protocol == ProtoHysteria2 {
		return false
	}
	switch n.Network {
	case NetTCP, NetWS, NetGRPC, NetHTTPUpgrade, NetXHTTP:
		return true
	default:
		return false
	}
}

func xrayStream(n ProxyNode) map[string]any {
	s := map[string]any{"network": n.Network, "security": n.Security}
	switch n.Security {
	case SecTLS:
		tls := map[string]any{"serverName": n.SNI, "allowInsecure": n.AllowInsecure}
		if n.Fingerprint != "" {
			tls["fingerprint"] = n.Fingerprint
		}
		if len(n.ALPN) > 0 {
			tls["alpn"] = n.ALPN
		}
		s["tlsSettings"] = tls
	case SecReality:
		s["realitySettings"] = map[string]any{
			"serverName": n.SNI, "fingerprint": orDefault(n.Fingerprint, "chrome"),
			"publicKey": n.PublicKey, "shortId": n.ShortID, "spiderX": n.SpiderX,
		}
	default:
		// 无传输层安全
	}
	switch n.Network {
	case NetWS:
		s["wsSettings"] = map[string]any{"path": orDefault(n.Path, "/"), "host": n.Host}
	case NetHTTPUpgrade:
		s["httpupgradeSettings"] = map[string]any{"path": orDefault(n.Path, "/"), "host": n.Host}
	case NetXHTTP:
		s["xhttpSettings"] = map[string]any{"path": orDefault(n.Path, "/"), "host": n.Host, "mode": orDefault(n.Mode, "auto")}
	case NetGRPC:
		s["grpcSettings"] = map[string]any{"serviceName": n.ServiceName}
	default:
		// tcp 无额外设置
	}
	return s
}

func xrayOutbound(n ProxyNode, tag string) map[string]any {
	o := map[string]any{"tag": tag, "streamSettings": xrayStream(n)}
	switch n.Protocol {
	case ProtoVLESS:
		user := map[string]any{"id": n.UUID, "encryption": "none"}
		if n.Flow != "" {
			user["flow"] = n.Flow
		}
		o["protocol"] = "vless"
		o["settings"] = map[string]any{"vnext": []map[string]any{{"address": n.Address, "port": n.Port, "users": []map[string]any{user}}}}
	case ProtoVMess:
		user := map[string]any{"id": n.UUID, "alterId": n.AlterID, "security": orDefault(n.Method, vmessDefaultCipher)}
		o["protocol"] = "vmess"
		o["settings"] = map[string]any{"vnext": []map[string]any{{"address": n.Address, "port": n.Port, "users": []map[string]any{user}}}}
	case ProtoTrojan:
		o["protocol"] = "trojan"
		o["settings"] = map[string]any{"servers": []map[string]any{{"address": n.Address, "port": n.Port, "password": n.Password}}}
	case ProtoShadowsocks:
		o["protocol"] = "shadowsocks"
		o["settings"] = map[string]any{"servers": []map[string]any{{"address": n.Address, "port": n.Port, "method": n.Method, "password": n.Password}}}
	case ProtoSocks:
		server := map[string]any{"address": n.Address, "port": n.Port}
		if n.Username != "" {
			server["users"] = []map[string]any{{"user": n.Username, "pass": n.Password}}
		}
		o["protocol"] = "socks"
		o["settings"] = map[string]any{"servers": []map[string]any{server}}
	case ProtoHTTP:
		server := map[string]any{"address": n.Address, "port": n.Port}
		if n.Username != "" {
			server["users"] = []map[string]any{{"user": n.Username, "pass": n.Password}}
		}
		o["protocol"] = "http"
		o["settings"] = map[string]any{"servers": []map[string]any{server}}
	default:
		// xraySupports 已排除其余协议
	}
	return o
}

// Xray 渲染一个 JSON 数组，每个节点一份完整的 Xray 客户端配置（remarks 为节点名），
// xrayConfig 拼一份完整的客户端配置；proxies 的第一个是默认出站。
func xrayConfig(remarks string, proxies ...map[string]any) map[string]any {
	outbounds := make([]map[string]any, 0, len(proxies)+1)
	outbounds = append(outbounds, proxies...)
	outbounds = append(outbounds, map[string]any{"tag": tagDirect, "protocol": "freedom"})
	return map[string]any{
		"remarks": remarks,
		"log":     map[string]any{"loglevel": "warning"},
		"inbounds": []map[string]any{
			{"tag": "socks", "listen": xrayListen, "port": xraySocksPort, "protocol": "socks", "settings": map[string]any{"udp": true}},
			{"tag": "http", "listen": xrayListen, "port": xrayHTTPPort, "protocol": "http"},
		},
		"outbounds": outbounds,
		"routing": map[string]any{
			"domainStrategy": "IPIfNonMatch",
			"rules": []map[string]any{
				{"type": "field", "ip": []string{"geoip:private", "geoip:cn"}, "outboundTag": tagDirect},
				{"type": "field", "domain": []string{"geosite:cn"}, "outboundTag": tagDirect},
			},
		},
	}
}

// 与 v2rayN / v2rayNG 的 JSON 订阅形状一致；说明节点与 Xray 不支持的协议不输出。
// 每条链路也是单独一份配置：落地出站的 sockopt.dialerProxy 指向同一份配置里的入口出站（docs/03 §5.18）。
func Xray(nodes []ProxyNode, chains []Chain) ([]byte, error) {
	sel := selectNodes(nodes, chains, xraySupports)
	configs := make([]map[string]any, 0, len(sel.nodes)+len(sel.chains))
	for i, n := range sel.nodes {
		configs = append(configs, xrayConfig(sel.names[i], xrayOutbound(n, xrayProxyTag)))
	}
	for _, c := range sel.chains {
		exit := xrayOutbound(c.exit, xrayProxyTag)
		stream, ok := exit["streamSettings"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("xray chain %q: unexpected stream settings type %T", c.name, exit["streamSettings"])
		}
		stream["sockopt"] = map[string]any{"dialerProxy": xrayChainEntryTag}
		configs = append(configs, xrayConfig(c.name, exit, xrayOutbound(c.entry, xrayChainEntryTag)))
	}
	out, err := json.MarshalIndent(configs, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal xray configs: %w", err)
	}
	return out, nil
}

// XrayOutbound 返回节点对应的一个 Xray 出站（服务端链式在入口面板上用它连落地，docs/03 §5.19）；
// 节点的协议或传输 Xray 表达不了时第二个返回值为 false。
func XrayOutbound(n ProxyNode, tag string) (map[string]any, bool) {
	if !xraySupports(n) {
		return nil, false
	}
	return xrayOutbound(n, tag), true
}
