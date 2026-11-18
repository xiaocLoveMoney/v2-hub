package render

import (
	"encoding/json"
	"fmt"
)

const (
	singBoxMixedPort    = 2080
	singBoxListen       = "127.0.0.1"
	ruleSetGeoIPCN      = "geoip-cn"
	ruleSetGeoIPURL     = "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-cn.srs"
	singBoxSocksVersion = "5"
)

func singBoxSupports(n ProxyNode) bool {
	// sing-box 官方不支持 VLESS Encryption（Ruling-77）：当成不加密输出会连不上，不输出
	if n.hasVLESSEncryption() {
		return false
	}
	switch n.Network {
	case NetTCP, NetWS, NetGRPC, NetHTTPUpgrade:
	default:
		return false
	}
	return n.Security != SecReality || n.Protocol == ProtoVLESS || n.Protocol == ProtoTrojan
}

func singBoxTLS(n ProxyNode) map[string]any {
	if n.Security == SecNone {
		return nil
	}
	tls := map[string]any{"enabled": true}
	if n.SNI != "" {
		tls["server_name"] = n.SNI
	}
	if n.AllowInsecure {
		tls["insecure"] = true
	}
	if len(n.ALPN) > 0 {
		tls["alpn"] = n.ALPN
	}
	if n.Fingerprint != "" {
		tls["utls"] = map[string]any{"enabled": true, "fingerprint": n.Fingerprint}
	}
	if n.Security == SecReality {
		tls["reality"] = map[string]any{"enabled": true, "public_key": n.PublicKey, "short_id": n.ShortID}
	}
	return tls
}

func singBoxTransport(n ProxyNode) map[string]any {
	switch n.Network {
	case NetWS:
		t := map[string]any{"type": NetWS, "path": orDefault(n.Path, "/")}
		if n.Host != "" {
			t["headers"] = map[string]any{"Host": n.Host}
		}
		return t
	case NetHTTPUpgrade:
		t := map[string]any{"type": NetHTTPUpgrade, "path": orDefault(n.Path, "/")}
		if n.Host != "" {
			t["host"] = n.Host
		}
		return t
	case NetGRPC:
		return map[string]any{"type": NetGRPC, "service_name": n.ServiceName}
	default:
		return nil
	}
}

func singBoxOutbound(n ProxyNode, tag string) map[string]any {
	o := map[string]any{"tag": tag, "server": n.Address, "server_port": n.Port}
	withStream := func() {
		if tls := singBoxTLS(n); tls != nil {
			o["tls"] = tls
		}
		if tr := singBoxTransport(n); tr != nil {
			o["transport"] = tr
		}
	}
	switch n.Protocol {
	case ProtoVLESS:
		o["type"], o["uuid"] = "vless", n.UUID
		if n.Flow != "" {
			o["flow"] = n.Flow
		}
		withStream()
	case ProtoVMess:
		o["type"], o["uuid"], o["alter_id"], o["security"] = "vmess", n.UUID, n.AlterID, orDefault(n.Method, vmessDefaultCipher)
		withStream()
	case ProtoTrojan:
		o["type"], o["password"] = "trojan", n.Password
		withStream()
	case ProtoShadowsocks:
		o["type"], o["method"], o["password"] = "shadowsocks", n.Method, n.Password
	case ProtoSocks:
		o["type"], o["version"] = "socks", singBoxSocksVersion
		if n.Username != "" {
			o["username"], o["password"] = n.Username, n.Password
		}
	case ProtoHTTP:
		o["type"] = "http"
		if n.Username != "" {
			o["username"], o["password"] = n.Username, n.Password
		}
		if tls := singBoxTLS(n); tls != nil {
			o["tls"] = tls
		}
	case ProtoHysteria2:
		o["type"], o["password"] = "hysteria2", n.Password
		tls := map[string]any{"enabled": true}
		if n.SNI != "" {
			tls["server_name"] = n.SNI
		}
		if n.AllowInsecure {
			tls["insecure"] = true
		}
		o["tls"] = tls
		if n.Obfs != "" {
			o["obfs"] = map[string]any{"type": n.Obfs, "password": n.ObfsPassword}
		}
	default:
		// singBoxSupports 已排除其余协议
	}
	return o
}

// SingBox 渲染可直接运行的 sing-box 配置，分组与规则同 Mihomo（docs/03 §5.4）。
// 链路是带 detour 的落地出站，排在普通节点之后，只进手动选择组（docs/03 §5.18）。
func SingBox(nodes []ProxyNode, chains []Chain) ([]byte, error) {
	sel := selectNodes(nodes, chains, singBoxSupports)
	outbounds := make([]map[string]any, 0, len(sel.nodes)+len(sel.chains)+3)
	outbounds = append(outbounds, map[string]any{"type": "selector", "tag": GroupSelect, "outbounds": sel.selectorMembers(tagDirect)})
	if len(sel.realNames) > 0 {
		outbounds = append(outbounds, map[string]any{
			"type": "urltest", "tag": GroupAuto, "outbounds": sel.realNames, "url": probeURL, "interval": fmt.Sprintf("%ds", probeIntervalSec),
		})
	}
	for i, n := range sel.nodes {
		outbounds = append(outbounds, singBoxOutbound(n, sel.names[i]))
	}
	for _, c := range sel.chains {
		o := singBoxOutbound(c.exit, c.name)
		o["detour"] = c.entryName
		outbounds = append(outbounds, o)
	}
	outbounds = append(outbounds, map[string]any{"type": "direct", "tag": tagDirect})
	cfg := map[string]any{
		"log":       map[string]any{"level": "info"},
		"inbounds":  []map[string]any{{"type": "mixed", "tag": "mixed-in", "listen": singBoxListen, "listen_port": singBoxMixedPort}},
		"outbounds": outbounds,
		"route": map[string]any{
			"rules": []map[string]any{
				{"ip_is_private": true, "outbound": tagDirect},
				{"rule_set": []string{ruleSetGeoIPCN}, "outbound": tagDirect},
			},
			"rule_set": []map[string]any{{
				"tag": ruleSetGeoIPCN, "type": "remote", "format": "binary", "url": ruleSetGeoIPURL, "download_detour": tagDirect,
			}},
			"final":                 GroupSelect,
			"auto_detect_interface": true,
		},
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal sing-box config: %w", err)
	}
	return out, nil
}
