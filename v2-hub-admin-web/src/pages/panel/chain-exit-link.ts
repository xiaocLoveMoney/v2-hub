/**
 * 出口表单与分享链接的互转（docs/05 M9 页面、docs/06 §5 M9 出口弹窗）。与用户端 src/pages/chain/exit-link.ts 同一套逻辑，
 * 两个模块互不引用，各存一份；改动时两边一起改。
 * 提交给后端的始终是一条分享链接；这里只管“表单能表达的那部分”，取值是否合法以后端为准（docs/03 §5.19 出站字段校验）。
 * 链接含凭据：本文件不打日志，也不把内容写进任何存储。
 */

/** http / https 是 HTTP 代理，https 即带 TLS 的 HTTP 代理（Ruling-51）。 */
export const EXIT_PROTOCOLS = ['vless', 'vmess', 'trojan', 'shadowsocks', 'socks', 'http', 'https'] as const
export type ExitProtocol = (typeof EXIT_PROTOCOLS)[number]

export const EXIT_NETWORKS = ['tcp', 'ws', 'grpc', 'httpupgrade', 'xhttp'] as const
export type ExitNetwork = (typeof EXIT_NETWORKS)[number]

export const EXIT_SECURITIES = ['none', 'tls', 'reality'] as const
export type ExitSecurity = (typeof EXIT_SECURITIES)[number]

/** 各下拉的取值，与后端的白名单一致（v2-hub-server/internal/service/chain_guard.go）。 */
export const EXIT_FLOWS = ['xtls-rprx-vision', 'xtls-rprx-vision-udp443'] as const
export const EXIT_VMESS_CIPHERS = ['auto', 'aes-128-gcm', 'chacha20-poly1305', 'none', 'zero'] as const
export const EXIT_SS_METHODS = [
  'aes-128-gcm',
  'aes-256-gcm',
  'chacha20-poly1305',
  'chacha20-ietf-poly1305',
  'xchacha20-poly1305',
  'xchacha20-ietf-poly1305',
  '2022-blake3-aes-128-gcm',
  '2022-blake3-aes-256-gcm',
  '2022-blake3-chacha20-poly1305',
] as const
export const EXIT_FINGERPRINTS = ['chrome', 'firefox', 'safari', 'ios', 'android', 'edge', '360', 'qq', 'random', 'randomized'] as const
export const EXIT_ALPNS = ['h2', 'http/1.1', 'h3'] as const
export const EXIT_XHTTP_MODES = ['auto', 'packet-up', 'stream-up', 'stream-one'] as const

export const MIN_EXIT_PORT = 1
export const MAX_EXIT_PORT = 65535

const DEFAULT_PORT = '443'
const DEFAULT_VMESS_CIPHER = 'auto'
const DEFAULT_SS_METHOD = 'aes-256-gcm'
const DEFAULT_XHTTP_MODE = 'auto'
const DEFAULT_REALITY_FINGERPRINT = 'chrome'

const SCHEME_BY_PROTOCOL: Record<ExitProtocol, string> = {
  vless: 'vless',
  vmess: 'vmess',
  trojan: 'trojan',
  shadowsocks: 'ss',
  socks: 'socks5',
  http: 'http',
  https: 'https',
}
const PROTOCOL_BY_SCHEME: Record<string, ExitProtocol> = {
  vless: 'vless',
  vmess: 'vmess',
  trojan: 'trojan',
  ss: 'shadowsocks',
  socks5: 'socks',
  socks: 'socks',
  http: 'http',
  https: 'https',
}

const SCHEME_PATTERN = /^([a-z0-9]+):\/\/(.+)$/i
const PORT_PATTERN = /^[0-9]{1,5}$/
const HOST_PORT_PATTERN = /^(\[[^\]]+\]|[^:]+):([0-9]{1,5})$/
const BASE64_BLOCK = 4
const VMESS_VERSION = '2'
const VMESS_TLS = 'tls'
const VMESS_HEADER_NONE = 'none'
/** TCP 伪装类型里表单能表达的取值；其余（http 伪装）只能用链接方式。 */
const PLAIN_HEADER_TYPES: readonly string[] = ['', 'none']
const TRUTHY = '1'
const TRUTHY_WORD = 'true'
const CREDENTIAL_SEPARATOR = ':'
const CSV_SEPARATOR = ','

/** 出口表单的全部字段；与当前协议无关的字段保持默认值，拼链接时不带。 */
export interface ExitFields {
  protocol: ExitProtocol
  address: string
  /** 端口按输入框的文本保存，提交前校验。 */
  port: string
  /** vless / vmess 的用户 ID。 */
  id: string
  /** vless 的 flow；空 = 不用。 */
  flow: string
  /** vmess 的加密方式。 */
  cipher: string
  /** shadowsocks 的加密方式。 */
  method: string
  /** trojan / shadowsocks / socks / http / https 的密码。 */
  passphrase: string
  /** socks / http / https 的用户名。 */
  username: string
  network: ExitNetwork
  host: string
  path: string
  serviceName: string
  /** xhttp 的模式。 */
  mode: string
  security: ExitSecurity
  /** TLS / REALITY 的 SNI；HTTPS 代理也用它。 */
  sni: string
  fingerprint: string
  alpn: string[]
  isInsecure: boolean
  /** REALITY 公钥、short id 与 spiderX。 */
  publicKey: string
  shortId: string
  spiderX: string
}

export type ExitFieldKey = keyof ExitFields

/** 一张空表单：协议 vless、端口 443、传输 RAW、不加密层，与 3x-ui 出站表单的初值一致。 */
export function emptyExitFields(): ExitFields {
  return {
    protocol: 'vless',
    address: '',
    port: DEFAULT_PORT,
    id: '',
    flow: '',
    cipher: DEFAULT_VMESS_CIPHER,
    method: DEFAULT_SS_METHOD,
    passphrase: '',
    username: '',
    network: 'tcp',
    host: '',
    path: '',
    serviceName: '',
    mode: DEFAULT_XHTTP_MODE,
    security: 'none',
    sni: '',
    fingerprint: '',
    alpn: [],
    isInsecure: false,
    publicKey: '',
    shortId: '',
    spiderX: '',
  }
}

/** 这个协议有没有传输层与安全层可选（shadowsocks、socks 没有）。 */
export function hasStream(protocol: ExitProtocol): boolean {
  return protocol === 'vless' || protocol === 'vmess' || protocol === 'trojan'
}

/** 这个协议是不是“地址 + 可选的用户名密码”形状的代理（socks、http、https）。 */
export function isPlainProxy(protocol: ExitProtocol): boolean {
  return protocol === 'socks' || protocol === 'http' || protocol === 'https'
}

/** 这个协议可选的安全层：vmess 的分享链接表达不了 REALITY。 */
export function securitiesOf(protocol: ExitProtocol): readonly ExitSecurity[] {
  if (!hasStream(protocol)) {
    return ['none']
  }
  return protocol === 'vmess' ? ['none', 'tls'] : EXIT_SECURITIES
}

/** flow 只在 vless + RAW + TLS / REALITY 时有意义。 */
export function hasFlow(fields: Pick<ExitFields, 'protocol' | 'network' | 'security'>): boolean {
  return fields.protocol === 'vless' && fields.network === 'tcp' && fields.security !== 'none'
}

/**
 * 换协议、传输或安全层之后把字段收拾到自洽：新协议不支持的安全层退回“无”，没有传输层的协议固定 RAW，
 * 选 REALITY 而没选指纹时补默认指纹（REALITY 必须有指纹）。
 */
export function normalizeExitFields(fields: ExitFields): ExitFields {
  const next = { ...fields }
  if (!hasStream(next.protocol)) {
    next.network = 'tcp'
  }
  if (!securitiesOf(next.protocol).includes(next.security)) {
    next.security = 'none'
  }
  if (next.security === 'reality' && next.fingerprint === '') {
    next.fingerprint = DEFAULT_REALITY_FINGERPRINT
  }
  return next
}

function isValidPort(port: string): boolean {
  if (!PORT_PATTERN.test(port)) {
    return false
  }
  const n = Number(port)
  return n >= MIN_EXIT_PORT && n <= MAX_EXIT_PORT
}

/** 必填而没填、或格式明显不对的字段；空集合表示可以拼链接。只查必填与端口，其余取值由后端校验。 */
export function exitFieldProblems(fields: ExitFields): ReadonlySet<ExitFieldKey> {
  const problems = new Set<ExitFieldKey>()
  if (fields.address.trim() === '') {
    problems.add('address')
  }
  if (!isValidPort(fields.port.trim())) {
    problems.add('port')
  }
  const needsId = fields.protocol === 'vless' || fields.protocol === 'vmess'
  if (needsId && fields.id.trim() === '') {
    problems.add('id')
  }
  const needsPassphrase = fields.protocol === 'trojan' || fields.protocol === 'shadowsocks'
  if (needsPassphrase && fields.passphrase === '') {
    problems.add('passphrase')
  }
  // 代理只填密码不填用户名没有意义：内核只在有用户名时才带认证
  if (isPlainProxy(fields.protocol) && fields.passphrase !== '' && fields.username === '') {
    problems.add('username')
  }
  if (hasStream(fields.protocol) && fields.security === 'reality') {
    if (fields.sni.trim() === '') {
      problems.add('sni')
    }
    if (fields.publicKey.trim() === '') {
      problems.add('publicKey')
    }
  }
  return problems
}

function encodeBase64(text: string): string {
  let binary = ''
  for (const byte of new TextEncoder().encode(text)) {
    binary += String.fromCharCode(byte)
  }
  return btoa(binary)
}

/** 标准或 URL 安全的 Base64 都接受，有无填充都行；不是合法的 Base64 或不是 UTF-8 文本时返回 null。 */
function decodeBase64(text: string): string | null {
  const body = text.trim().replace(/=+$/, '').replaceAll('-', '+').replaceAll('_', '/')
  const padding = (BASE64_BLOCK - (body.length % BASE64_BLOCK)) % BASE64_BLOCK
  try {
    const binary = atob(body + '='.repeat(padding))
    const bytes = Uint8Array.from(binary, (ch) => ch.charCodeAt(0))
    return new TextDecoder('utf-8', { fatal: true }).decode(bytes)
  } catch {
    // 不是 Base64：调用方按“这条链接表单表达不了”处理
    return null
  }
}

function toBase64Url(base64: string): string {
  return base64.replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/, '')
}

/** IPv6 地址放进链接时要带方括号。 */
function hostForLink(address: string): string {
  const host = address.trim()
  return host.includes(':') && !host.startsWith('[') ? `[${host}]` : host
}

function stripBrackets(host: string): string {
  return host.startsWith('[') && host.endsWith(']') ? host.slice(1, -1) : host
}

function fragmentOf(name: string): string {
  return name === '' ? '' : `#${encodeURIComponent(name)}`
}

function setIfPresent(q: URLSearchParams, key: string, value: string) {
  if (value !== '') {
    q.set(key, value)
  }
}

function streamQuery(fields: ExitFields): URLSearchParams {
  const q = new URLSearchParams()
  q.set('type', fields.network)
  q.set('security', fields.security)
  if (fields.network === 'ws' || fields.network === 'httpupgrade' || fields.network === 'xhttp') {
    setIfPresent(q, 'host', fields.host.trim())
    setIfPresent(q, 'path', fields.path.trim())
  }
  if (fields.network === 'xhttp') {
    q.set('mode', fields.mode)
  }
  if (fields.network === 'grpc') {
    setIfPresent(q, 'serviceName', fields.serviceName.trim())
  }
  if (fields.security === 'none') {
    return q
  }
  setIfPresent(q, 'sni', fields.sni.trim())
  setIfPresent(q, 'fp', fields.fingerprint)
  if (fields.security === 'tls') {
    setIfPresent(q, 'alpn', fields.alpn.join(CSV_SEPARATOR))
    if (fields.isInsecure) {
      q.set('allowInsecure', TRUTHY)
    }
    return q
  }
  q.set('pbk', fields.publicKey.trim())
  setIfPresent(q, 'sid', fields.shortId.trim())
  setIfPresent(q, 'spx', fields.spiderX.trim())
  return q
}

function buildVMess(fields: ExitFields, name: string): string {
  const isGrpc = fields.network === 'grpc'
  const hasHostPath = fields.network !== 'tcp' && !isGrpc
  const isTls = fields.security === 'tls'
  const payload = {
    v: VMESS_VERSION,
    ps: name,
    add: fields.address.trim(),
    port: Number(fields.port.trim()),
    id: fields.id.trim(),
    aid: 0,
    scy: fields.cipher,
    net: fields.network,
    type: VMESS_HEADER_NONE,
    host: hasHostPath ? fields.host.trim() : '',
    path: isGrpc ? fields.serviceName.trim() : hasHostPath ? fields.path.trim() : '',
    tls: isTls ? VMESS_TLS : '',
    sni: isTls ? fields.sni.trim() : '',
    alpn: isTls ? fields.alpn.join(CSV_SEPARATOR) : '',
    fp: isTls ? fields.fingerprint : '',
  }
  return `vmess://${encodeBase64(JSON.stringify(payload))}`
}

/** HTTPS 代理的附加参数（SNI、允许不安全）；其它代理没有参数。 */
function proxyQuery(fields: ExitFields): string {
  if (fields.protocol !== 'https') {
    return ''
  }
  const q = new URLSearchParams()
  setIfPresent(q, 'sni', fields.sni.trim())
  if (fields.isInsecure) {
    q.set('allowInsecure', TRUTHY)
  }
  const text = q.toString()
  return text === '' ? '' : `?${text}`
}

/**
 * 把表单拼成一条分享链接；调用前先用 exitFieldProblems 确认没有缺项。
 * name 写进链接的名称部分（空则不带）。形状与后端的解析一致（v2-hub-server/internal/manager/render/node.go）。
 */
export function buildExitLink(input: ExitFields, name: string): string {
  const fields = normalizeExitFields(input)
  const scheme = SCHEME_BY_PROTOCOL[fields.protocol]
  const authority = `${hostForLink(fields.address)}:${fields.port.trim()}`
  const fragment = fragmentOf(name)
  switch (fields.protocol) {
    case 'vmess':
      return buildVMess(fields, name)
    case 'vless': {
      const q = streamQuery(fields)
      q.set('encryption', 'none')
      if (hasFlow(fields)) {
        setIfPresent(q, 'flow', fields.flow)
      }
      return `${scheme}://${encodeURIComponent(fields.id.trim())}@${authority}?${q.toString()}${fragment}`
    }
    case 'trojan':
      return `${scheme}://${encodeURIComponent(fields.passphrase)}@${authority}?${streamQuery(fields).toString()}${fragment}`
    case 'shadowsocks': {
      const credential = toBase64Url(encodeBase64(`${fields.method}${CREDENTIAL_SEPARATOR}${fields.passphrase}`))
      return `${scheme}://${credential}@${authority}${fragment}`
    }
    case 'socks':
    case 'http':
    case 'https': {
      // 有用户名时总是带上“:密码”（密码可空）：socks 只有用户名的形状会被后端当成 Base64 去解
      const credential =
        fields.username === '' ? '' : `${encodeURIComponent(fields.username)}${CREDENTIAL_SEPARATOR}${encodeURIComponent(fields.passphrase)}@`
      return `${scheme}://${credential}${authority}${proxyQuery(fields)}${fragment}`
    }
    default:
      return ''
  }
}

/** 解析结果：表单字段与链接里带的名称。 */
export interface ParsedExitLink {
  fields: ExitFields
  name: string
}

function pick<T extends string>(allowed: readonly T[], value: string): T | null {
  return (allowed as readonly string[]).includes(value) ? (value as T) : null
}

function safeDecode(text: string): string | null {
  try {
    return decodeURIComponent(text)
  } catch {
    // 百分号编码不完整：按解析失败处理
    return null
  }
}

function splitCsv(text: string): string[] {
  return text
    .split(CSV_SEPARATOR)
    .map((s) => s.trim())
    .filter((s) => s !== '')
}

function isTruthy(text: string | null): boolean {
  return text === TRUTHY || text?.toLowerCase() === TRUTHY_WORD
}

/** 拆“地址:端口”；IPv6 带方括号。 */
function splitHostPort(text: string): { address: string; port: string } | null {
  const m = HOST_PORT_PATTERN.exec(text)
  if (m === null || !isValidPort(m[2])) {
    return null
  }
  return { address: stripBrackets(m[1]), port: String(Number(m[2])) }
}

function parseUrl(link: string): URL | null {
  try {
    return new URL(link)
  } catch {
    // 不是合法的 URL：按解析失败处理
    return null
  }
}

function nameOf(url: URL): string | null {
  return safeDecode(url.hash.replace(/^#/, ''))
}

/** vless / trojan：scheme://凭据@地址:端口?参数#名称。 */
function parseUrlLink(link: string, protocol: 'vless' | 'trojan'): ParsedExitLink | null {
  const url = parseUrl(link)
  if (url === null || !isValidPort(url.port) || url.hostname === '') {
    return null
  }
  const credential = safeDecode(url.username)
  const name = nameOf(url)
  const q = url.searchParams
  const network = pick(EXIT_NETWORKS, q.get('type') ?? 'tcp')
  const security = pick(securitiesOf(protocol), q.get('security') ?? 'none')
  const isPlain = PLAIN_HEADER_TYPES.includes(q.get('headerType') ?? '')
  if (credential === null || credential === '' || name === null || network === null || security === null || !isPlain) {
    return null
  }
  const fields: ExitFields = {
    ...emptyExitFields(),
    protocol,
    address: stripBrackets(url.hostname),
    port: url.port,
    network,
    security,
    host: q.get('host') ?? '',
    path: q.get('path') ?? '',
    serviceName: q.get('serviceName') ?? '',
    mode: q.get('mode') ?? DEFAULT_XHTTP_MODE,
    sni: q.get('sni') ?? '',
    fingerprint: q.get('fp') ?? '',
    alpn: splitCsv(q.get('alpn') ?? ''),
    isInsecure: isTruthy(q.get('allowInsecure')) || isTruthy(q.get('insecure')),
    publicKey: q.get('pbk') ?? '',
    shortId: q.get('sid') ?? '',
    spiderX: q.get('spx') ?? '',
  }
  if (protocol === 'vless') {
    fields.id = credential
    fields.flow = q.get('flow') ?? ''
  } else {
    fields.passphrase = credential
  }
  return { fields: normalizeExitFields(fields), name }
}

function textOf(value: unknown): string {
  return typeof value === 'string' || typeof value === 'number' ? String(value) : ''
}

function parseJsonObject(raw: string): Record<string, unknown> | null {
  try {
    const value: unknown = JSON.parse(raw)
    return typeof value === 'object' && value !== null ? (value as Record<string, unknown>) : null
  } catch {
    // 不是 JSON：按解析失败处理
    return null
  }
}

/** vmess：Base64 里一段 JSON。alterId 不是 0 的后端不收，这里也不转。 */
function parseVMess(payload: string): ParsedExitLink | null {
  const raw = decodeBase64(payload)
  const v = raw === null ? null : parseJsonObject(raw)
  if (v === null) {
    return null
  }
  const port = textOf(v.port)
  const network = pick(EXIT_NETWORKS, textOf(v.net) || 'tcp')
  const alterId = textOf(v.aid)
  const isPlain = (alterId === '' || alterId === '0') && PLAIN_HEADER_TYPES.includes(textOf(v.type))
  if (network === null || !isPlain || !isValidPort(port) || textOf(v.add) === '' || textOf(v.id) === '') {
    return null
  }
  const isGrpc = network === 'grpc'
  const fields: ExitFields = {
    ...emptyExitFields(),
    protocol: 'vmess',
    address: textOf(v.add),
    port,
    id: textOf(v.id),
    cipher: textOf(v.scy) || DEFAULT_VMESS_CIPHER,
    network,
    host: textOf(v.host),
    path: isGrpc ? '' : textOf(v.path),
    serviceName: isGrpc ? textOf(v.path) : '',
    security: textOf(v.tls) === VMESS_TLS ? 'tls' : 'none',
    sni: textOf(v.sni),
    alpn: splitCsv(textOf(v.alpn)),
    fingerprint: textOf(v.fp),
  }
  return { fields, name: textOf(v.ps) }
}

/** 取出 shadowsocks 链接里的“加密方式:密码”与“地址:端口”两段；旧形状是整体 Base64。 */
function splitShadowsocks(body: string): { credential: string; hostPort: string } | null {
  const at = body.lastIndexOf('@')
  if (at < 0) {
    const whole = decodeBase64(body)
    const wholeAt = whole?.lastIndexOf('@') ?? -1
    return whole === null || wholeAt < 0 ? null : { credential: whole.slice(0, wholeAt), hostPort: whole.slice(wholeAt + 1) }
  }
  const userinfo = safeDecode(body.slice(0, at))
  if (userinfo === null) {
    return null
  }
  const credential = userinfo.includes(CREDENTIAL_SEPARATOR) ? userinfo : decodeBase64(userinfo)
  return credential === null ? null : { credential, hostPort: body.slice(at + 1) }
}

/** shadowsocks：SIP002（凭据是 Base64 或明文的“加密方式:密码”），也接受整体 Base64 的旧形状。 */
function parseShadowsocks(rest: string): ParsedExitLink | null {
  const [beforeFragment, rawName = ''] = rest.split('#')
  const name = safeDecode(rawName)
  const parts = splitShadowsocks(beforeFragment.split('?')[0].replace(/\/$/, ''))
  const target = parts === null ? null : splitHostPort(parts.hostPort)
  if (name === null || parts === null || target === null) {
    return null
  }
  const cut = parts.credential.indexOf(CREDENTIAL_SEPARATOR)
  if (cut <= 0 || cut === parts.credential.length - 1) {
    return null
  }
  const fields: ExitFields = { ...emptyExitFields(), protocol: 'shadowsocks', ...target, method: parts.credential.slice(0, cut) }
  fields.passphrase = parts.credential.slice(cut + 1)
  return { fields, name }
}

/** 拆开“[凭据@]地址:端口[/][?参数][#名称]”；带路径的（网页地址）不是代理，返回 null。 */
function splitProxyRest(rest: string): { userinfo: string | null; target: { address: string; port: string }; query: URLSearchParams; name: string } | null {
  const [beforeFragment, rawName = ''] = rest.split('#')
  const [beforeQuery, rawQuery = ''] = beforeFragment.split('?')
  const name = safeDecode(rawName)
  const body = beforeQuery.replace(/\/$/, '')
  const at = body.lastIndexOf('@')
  const target = splitHostPort(body.slice(at + 1))
  if (name === null || target === null || body.slice(at + 1).includes('/')) {
    return null
  }
  return { userinfo: at < 0 ? null : body.slice(0, at), target, query: new URLSearchParams(rawQuery), name }
}

/**
 * 代理地址：socks5:// http:// https://[用户名:密码@]地址:端口。socks 另接受 socks://Base64(用户名:密码)@地址:端口；
 * http / https 接受只有用户名的写法。不用 URL 解析：浏览器会把 http 的 80、https 的 443 端口吞掉。
 */
function parseProxy(rest: string, protocol: 'socks' | 'http' | 'https'): ParsedExitLink | null {
  const parts = splitProxyRest(rest)
  if (parts === null) {
    return null
  }
  const fields: ExitFields = { ...emptyExitFields(), protocol, ...parts.target }
  if (protocol === 'https') {
    fields.sni = parts.query.get('sni') ?? ''
    fields.isInsecure = isTruthy(parts.query.get('allowInsecure')) || isTruthy(parts.query.get('insecure'))
  }
  if (parts.userinfo === null) {
    return { fields, name: parts.name }
  }
  const cut = parts.userinfo.indexOf(CREDENTIAL_SEPARATOR)
  if (cut >= 0) {
    const username = safeDecode(parts.userinfo.slice(0, cut))
    const passphrase = safeDecode(parts.userinfo.slice(cut + 1))
    if (username === null || passphrase === null || (username === '' && passphrase !== '')) {
      return null
    }
    return { fields: { ...fields, username, passphrase }, name: parts.name }
  }
  const single = safeDecode(parts.userinfo)
  if (single === null) {
    return null
  }
  if (protocol !== 'socks') {
    return { fields: { ...fields, username: single }, name: parts.name }
  }
  const decoded = decodeBase64(single)
  const decodedCut = decoded?.indexOf(CREDENTIAL_SEPARATOR) ?? -1
  if (decoded === null || decodedCut <= 0) {
    return null
  }
  fields.username = decoded.slice(0, decodedCut)
  fields.passphrase = decoded.slice(decodedCut + 1)
  return { fields, name: parts.name }
}

/**
 * 把一条分享链接或代理地址解析成表单。表单表达不了的（hysteria2、带 HTTP 伪装、形状不对）返回 null，调用方让用户改用链接方式。
 * 链接里表单没有的参数会被丢掉——后端同样不读它们。
 */
export function parseExitLink(link: string): ParsedExitLink | null {
  const text = link.trim()
  const m = SCHEME_PATTERN.exec(text)
  if (m === null) {
    return null
  }
  const protocol = PROTOCOL_BY_SCHEME[m[1].toLowerCase()] as ExitProtocol | undefined
  switch (protocol) {
    case 'vless':
    case 'trojan':
      return parseUrlLink(text, protocol)
    case 'vmess':
      return parseVMess(m[2])
    case 'shadowsocks':
      return parseShadowsocks(m[2])
    case 'socks':
    case 'http':
    case 'https':
      return parseProxy(m[2], protocol)
    default:
      return null
  }
}
