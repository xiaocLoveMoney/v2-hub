/**
 * IP 质量检测的枚举码 → 中文与标签颜色（docs/03 §5.23，Ruling-81）。
 * 报告里存的都是枚举码，显示文字只在这里；不认识的码原样显示。
 */

export const EMPTY = '—'

/** 一个取值的显示：文字 + 可选的标签颜色。 */
export interface Label {
  text: string
  color?: string
}

function pick(map: Record<string, Label>, code: string | null | undefined): Label {
  if (code === null || code === undefined || code === '') {
    return { text: EMPTY }
  }
  return map[code] ?? { text: code }
}

export const CHECK_STATUS: Record<string, Label> = {
  queued: { text: '排队中', color: 'blue' },
  running: { text: '检测中', color: 'processing' },
  succeeded: { text: '完成', color: 'green' },
  failed: { text: '失败', color: 'red' },
}

export const CHECK_ORIGIN: Record<string, Label> = {
  manual: { text: '手动' },
  auto: { text: '定期复检' },
}

/** 服务器侧的情况；同时用作流媒体与邮局两段“没做”的原因。 */
export const HOST_PROBE: Record<string, Label> = {
  done: { text: '已检测', color: 'green' },
  no_host: { text: '需绑定 SSH' },
  no_permission: { text: '只做平台侧（没有 host:write）' },
  host_unverified: { text: '主机密钥未固定：先在主机上做一次部署或校验连接', color: 'orange' },
  ssh_failed: { text: 'SSH 连不上', color: 'red' },
  no_curl: { text: '主机上没有 curl', color: 'orange' },
}

export const IP_SOURCE: Record<string, Label> = {
  host: { text: '服务器出口' },
  panel: { text: '面板报告' },
  address: { text: '面板地址' },
}

export const IP_KIND: Record<string, Label> = {
  native: { text: '原生 IP', color: 'green' },
  broadcast: { text: '广播 IP', color: 'orange' },
  unknown: { text: '未知' },
}

export const NET_TYPE: Record<string, Label> = {
  hosting: { text: '机房', color: 'orange' },
  residential: { text: '家宽', color: 'green' },
  mobile: { text: '手机', color: 'green' },
  business: { text: '商业', color: 'blue' },
  education: { text: '教育' },
  government: { text: '政府' },
  military: { text: '军队' },
  organization: { text: '组织' },
  library: { text: '图书馆' },
  banking: { text: '银行' },
  cdn: { text: 'CDN' },
  crawler: { text: '爬虫' },
  reserved: { text: '保留' },
  unknown: { text: '未知' },
}

export const RISK_LEVEL: Record<string, Label> = {
  low: { text: '低', color: 'green' },
  medium: { text: '中', color: 'gold' },
  high: { text: '高', color: 'orange' },
  very_high: { text: '极高', color: 'red' },
}

export const SOURCE_NAME: Record<string, string> = {
  ip2location: 'IP2Location.io',
  cymru: 'Team Cymru',
  tor: 'Tor 出口',
  ipapiis: 'ipapi.is',
  abuseipdb: 'AbuseIPDB',
  ipqs: 'IPQS',
}

export const SOURCE_STATUS: Record<string, Label> = {
  ok: { text: '正常', color: 'green' },
  not_configured: { text: '未配置' },
  rate_limited: { text: '额度用完', color: 'orange' },
  auth_failed: { text: 'key 无效', color: 'red' },
  error: { text: '出错', color: 'red' },
}

export const DNSBL_RESULT: Record<string, Label> = {
  clean: { text: '未上榜', color: 'green' },
  listed: { text: '上榜', color: 'red' },
  policy: { text: '策略类', color: 'gold' },
  unavailable: { text: '不可用' },
}

export const MEDIA_NAME: Record<string, string> = {
  netflix: 'Netflix',
  youtube_premium: 'YouTube Premium',
  prime_video: 'Prime Video',
  tiktok: 'TikTok',
  reddit: 'Reddit',
  chatgpt: 'ChatGPT',
}

export const MEDIA_STATUS: Record<string, Label> = {
  yes: { text: '可用', color: 'green' },
  no: { text: '不可用', color: 'red' },
  originals_only: { text: '仅自制剧', color: 'gold' },
  web_only: { text: '仅网页', color: 'gold' },
  app_only: { text: '仅 App', color: 'gold' },
  failed: { text: '网络错误', color: 'orange' },
  unknown: { text: '无法判定' },
}

export const UNLOCK_METHOD: Record<string, Label> = {
  native: { text: '原生' },
  dns: { text: 'DNS 解锁', color: 'purple' },
  unknown: { text: EMPTY },
}

export const MAIL_NAME: Record<string, string> = {
  gmail: 'Gmail',
  outlook: 'Outlook',
  yahoo: 'Yahoo',
  icloud: 'Apple iCloud',
  qq: 'QQ',
  mailru: 'Mail.ru',
  aol: 'AOL',
  gmx: 'GMX',
  mailcom: 'Mail.com',
  netease163: '163',
  sohu: '搜狐',
  sina: '新浪',
}

export const MAIL_RESULT: Record<string, Label> = {
  ok: { text: '通', color: 'green' },
  rejected: { text: '被拒', color: 'orange' },
  refused: { text: '连接被拒', color: 'red' },
  timeout: { text: '超时', color: 'red' },
  dns_failed: { text: '查不到 MX' },
  no_tool: { text: '无法检测' },
  failed: { text: '出错', color: 'orange' },
}

export const PORT25: Record<string, Label> = {
  open: { text: '可用', color: 'green' },
  blocked: { text: '被封', color: 'red' },
  unknown: { text: '未知' },
}

export const ERROR_CODE: Record<string, Label> = {
  no_public_ip: { text: '取不到公网 IP' },
  interrupted: { text: '重启中断' },
  timeout: { text: '超时' },
  internal: { text: '内部错误' },
}

export const FACTOR_NAME: Record<string, string> = {
  country: '国家码',
  proxy: '代理',
  tor: 'Tor',
  vpn: 'VPN',
  server: '机房',
  abuser: '滥用',
  robot: '机器人',
}

/** 风险因子的顺序（与报告的字段一致）。 */
export const FACTOR_KEYS = ['country', 'proxy', 'tor', 'vpn', 'server', 'abuser', 'robot'] as const

export const labelOf = {
  checkStatus: (c: string) => pick(CHECK_STATUS, c),
  origin: (c: string) => pick(CHECK_ORIGIN, c),
  hostProbe: (c: string) => pick(HOST_PROBE, c),
  ipSource: (c: string) => pick(IP_SOURCE, c),
  ipKind: (c: string) => pick(IP_KIND, c),
  netType: (c: string) => pick(NET_TYPE, c),
  risk: (c: string) => pick(RISK_LEVEL, c),
  sourceStatus: (c: string) => pick(SOURCE_STATUS, c),
  dnsbl: (c: string) => pick(DNSBL_RESULT, c),
  media: (c: string) => pick(MEDIA_STATUS, c),
  method: (c: string) => pick(UNLOCK_METHOD, c),
  mail: (c: string) => pick(MAIL_RESULT, c),
  port25: (c: string) => pick(PORT25, c),
  error: (c: string) => pick(ERROR_CODE, c),
}

const ACTIVE_STATUSES = new Set(['queued', 'running'])

/** 检测是否还没结束（排队中或检测中）。 */
export function isCheckActive(check: { status: string } | null | undefined): boolean {
  return check !== null && check !== undefined && ACTIVE_STATUSES.has(check.status)
}

/** 可空布尔的显示：是 / 否 / —。 */
export function boolText(v: boolean | null | undefined): Label {
  if (v === null || v === undefined) {
    return { text: EMPTY }
  }
  return v ? { text: '是', color: 'red' } : { text: '否', color: 'green' }
}
