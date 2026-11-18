package service

import (
	"encoding/json"
	"strings"
)

// 只用 UDP 的协议与传输方式（docs/03 §5.1 “主机端口检查”，Ruling-78）。
var (
	udpOnlyProtocols = map[string]bool{"hysteria": true, "hysteria2": true, "wireguard": true}
	udpOnlyNetworks  = map[string]bool{"kcp": true, "quic": true}
)

const (
	networkTCPToken = "tcp"
	networkUDPToken = "udp"
)

// InboundListen 是入站要在主机上占用的端口，以及占 TCP 还是 UDP（主机端口检查用，Ruling-78）。
type InboundListen struct {
	Port int
	TCP  bool
	UDP  bool
}

// NewInboundListen 按新建输入算出入站要占用的端口；输入不合法时返回与 CreateInbound 相同的错误。
func (s *PanelService) NewInboundListen(in InboundCreateInput) (InboundListen, error) {
	spec, err := s.buildCreateSpec(in)
	if err != nil {
		return InboundListen{}, err
	}
	tcp, udp := listenTransports(spec.Protocol, spec.Config)
	return InboundListen{Port: spec.Port, TCP: tcp, UDP: udp}, nil
}

// listenTransports 按协议与配置判断入站监听 TCP 还是 UDP：Hysteria、WireGuard、mKCP、QUIC 只用 UDP；
// settings.network 写明了的（Shadowsocks、任意门等）按它；其余 TCP。配置读不懂时按 TCP。
func listenTransports(protocol string, config json.RawMessage) (tcp, udp bool) {
	if udpOnlyProtocols[protocol] {
		return false, true
	}
	var cfg struct {
		Settings struct {
			Network string `json:"network"`
		} `json:"settings"`
		StreamSettings struct {
			Network string `json:"network"`
		} `json:"streamSettings"`
	}
	if len(config) > 0 && json.Unmarshal(config, &cfg) != nil {
		return true, false
	}
	if udpOnlyNetworks[cfg.StreamSettings.Network] {
		return false, true
	}
	if network := cfg.Settings.Network; network != "" {
		tcp, udp = strings.Contains(network, networkTCPToken), strings.Contains(network, networkUDPToken)
		if tcp || udp {
			return tcp, udp
		}
	}
	return true, false
}
