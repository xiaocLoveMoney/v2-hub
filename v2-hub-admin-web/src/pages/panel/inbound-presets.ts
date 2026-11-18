import type { InboundPresetKey } from '../../api/client'

/** 一键部署预设的显示名与说明（docs/03 §5.1 “一键部署入站”，Ruling-76）。 */
export const INBOUND_PRESET_LABELS: Record<InboundPresetKey, { name: string; detail: string }> = {
  vless_reality_vision: { name: 'VLESS + REALITY + Vision', detail: 'TCP，不需要域名证书' },
  vless_reality_mlkem: { name: 'VLESS + REALITY + ML-KEM', detail: '同上再加 ML-KEM-768 后量子加密；sing-box 订阅里没有它' },
  vless_xhttp_tls: { name: 'VLESS + XHTTP + TLS', detail: '主机域名证书；sing-box 订阅里没有它' },
  vmess_ws_tls: { name: 'VMess + WebSocket + TLS', detail: '主机域名证书' },
  trojan_tls: { name: 'Trojan + TCP + TLS', detail: '主机域名证书' },
  shadowsocks_2022: { name: 'Shadowsocks 2022', detail: '2022-blake3-aes-256-gcm，TCP 与 UDP' },
  hysteria2_salamander: { name: 'Hysteria2 + Salamander', detail: '主机域名证书，UDP' },
}

/** 选中预设的“VLESS 流控”提示：建了 REALITY 预设时服务器会打开 Vision 流控（Ruling-77）。 */
export const VISION_PRESETS: readonly InboundPresetKey[] = ['vless_reality_vision', 'vless_reality_mlkem']
