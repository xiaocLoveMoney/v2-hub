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

func xrayOutbound(n ProxyNode) map[string]any {
	o := map[string]any{"tag": xrayProxyTag, "streamSettings": xrayStream(n)}
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
	default:
		// xraySupports 已排除其余协议
	}
	return o
}

// Xray 渲染一个 JSON 数组，每个节点一份完整的 Xray 客户端配置（remarks 为节点名），
// 与 v2rayN / v2rayNG 的 JSON 订阅形状一致；说明节点与 Xray 不支持的协议不输出。
func Xray(nodes []ProxyNode) ([]byte, error) {
	kept, names, _ := filterNodes(nodes, xraySupports)
	configs := make([]map[string]any, 0, len(kept))
	for i, n := range kept {
		configs = append(configs, map[string]any{
			"remarks": names[i],
			"log":     map[string]any{"loglevel": "warning"},
			"inbounds": []map[string]any{
				{"tag": "socks", "listen": xrayListen, "port": xraySocksPort, "protocol": "socks", "settings": map[string]any{"udp": true}},
				{"tag": "http", "listen": xrayListen, "port": xrayHTTPPort, "protocol": "http"},
			},
			"outbounds": []map[string]any{
				xrayOutbound(n),
				{"tag": tagDirect, "protocol": "freedom"},
			},
			"routing": map[string]any{
				"domainStrategy": "IPIfNonMatch",
				"rules": []map[string]any{
					{"type": "field", "ip": []string{"geoip:private", "geoip:cn"}, "outboundTag": tagDirect},
					{"type": "field", "domain": []string{"geosite:cn"}, "outboundTag": tagDirect},
				},
			},
		})
	}
	out, err := json.MarshalIndent(configs, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal xray configs: %w", err)
	}
	return out, nil
}
