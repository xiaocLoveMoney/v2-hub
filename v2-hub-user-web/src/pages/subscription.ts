import type { SubscriptionFormat, SubscriptionInfo } from '../api/client'

/** 各客户端的一键导入入口（docs/06 §5 仪表盘）：品牌渐变色与导入地址的拼法。 */
export interface ImportTarget {
  key: string
  label: string
  format: SubscriptionFormat
  /** 渐变起止色，只用于该客户端的按钮。 */
  gradient: readonly [string, string]
  /** 由订阅地址拼出客户端的导入 scheme；返回 null 表示只能复制地址。 */
  buildUrl: ((subUrl: string, title: string) => string) | null
}

/** URL 安全的 Base64（Shadowrocket 的订阅导入要求）。 */
function base64Url(text: string): string {
  return btoa(text).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

export const IMPORT_TARGETS: readonly ImportTarget[] = [
  {
    key: 'mihomo',
    label: 'Clash / Mihomo',
    format: 'mihomo',
    gradient: ['#49bcfc', '#3b92f8'],
    buildUrl: (u, title) => `clash://install-config?url=${encodeURIComponent(u)}&name=${encodeURIComponent(title)}`,
  },
  {
    key: 'singbox',
    label: 'sing-box',
    format: 'singbox',
    gradient: ['#5c97f0', '#b769f3'],
    buildUrl: (u, title) => `sing-box://import-remote-profile?url=${encodeURIComponent(u)}#${encodeURIComponent(title)}`,
  },
  {
    key: 'shadowrocket',
    label: 'Shadowrocket',
    format: 'base64',
    gradient: ['#4f8fe0', '#3671b9'],
    buildUrl: (u, title) => `shadowrocket://add/sub://${base64Url(u)}?remark=${encodeURIComponent(title)}`,
  },
  { key: 'v2ray', label: 'v2rayN / v2rayNG', format: 'base64', gradient: ['#df268f', '#a73178'], buildUrl: null },
  { key: 'xray', label: 'Xray JSON', format: 'xray', gradient: ['#f2885b', '#e83c9a'], buildUrl: null },
]

/** 取某一格式的订阅地址；契约保证四种格式都在，找不到时退回通用地址。 */
export function urlOfFormat(info: SubscriptionInfo, format: SubscriptionFormat): string {
  return info.formats.find((f) => f.format === format)?.url ?? info.url
}

const MASK_KEEP = 6
const MASK = '••••••••••••'

/** 订阅地址属于凭据，默认只露出末尾几位（docs/06 §3 不变式 2）。 */
export function maskSecretUrl(url: string): string {
  const i = url.lastIndexOf('/')
  if (i < 0 || url.length - i - 1 <= MASK_KEEP) {
    return MASK
  }
  return `${url.slice(0, i + 1)}${MASK}${url.slice(-MASK_KEEP)}`
}
