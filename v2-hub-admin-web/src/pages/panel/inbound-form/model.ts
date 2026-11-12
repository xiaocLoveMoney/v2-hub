/**
 * 入站表单的数据模型（Ruling-16）：草稿即 3x-ui / Xray 的入站形状（settings / streamSettings / sniffing），
 * 各协议、传输、安全层的默认值与可用性规则对照 3x-ui v3.11.0 的表单行为自行实现。
 * clients 不在表单里：入站上的用户由 M2 下发对账维护，编辑时服务端保留面板现有 clients（Ruling-14）。
 */

export type JsonObject = Record<string, unknown>
export type Path = readonly (string | number)[]

export const PROTOCOLS = ['vless', 'vmess', 'trojan', 'shadowsocks', 'hysteria', 'wireguard', 'http', 'mixed', 'tunnel'] as const
export type Protocol = (typeof PROTOCOLS)[number]

export const NETWORKS = ['tcp', 'kcp', 'ws', 'grpc', 'httpupgrade', 'xhttp'] as const
export type Network = (typeof NETWORKS)[number]
export const NETWORK_LABELS: Record<Network, string> = {
  tcp: 'RAW',
  kcp: 'mKCP',
  ws: 'WebSocket',
  grpc: 'gRPC',
  httpupgrade: 'HTTPUpgrade',
  xhttp: 'XHTTP',
}

export const SECURITY_NONE = 'none'
export const SECURITY_TLS = 'tls'
export const SECURITY_REALITY = 'reality'
export type Security = typeof SECURITY_NONE | typeof SECURITY_TLS | typeof SECURITY_REALITY

export const NETWORK_HYSTERIA = 'hysteria'
export const TLS_KEY = 'tlsSettings'
export const REALITY_KEY = 'realitySettings'

/** 传输层设置所在的键；hysteria 的传输固定为 hysteria。 */
const NETWORK_KEYS: Record<string, string> = {
  tcp: 'tcpSettings',
  kcp: 'kcpSettings',
  ws: 'wsSettings',
  grpc: 'grpcSettings',
  httpupgrade: 'httpupgradeSettings',
  xhttp: 'xhttpSettings',
  [NETWORK_HYSTERIA]: 'hysteriaSettings',
}

export const SS_METHODS = [
  '2022-blake3-aes-256-gcm',
  '2022-blake3-aes-128-gcm',
  'aes-256-gcm',
  'chacha20-poly1305',
  'chacha20-ietf-poly1305',
  'xchacha20-ietf-poly1305',
] as const
const SS2022_PREFIX = '2022'
const SS2022_AES128 = '2022-blake3-aes-128-gcm'
const SS2022_KEY_BYTES_AES128 = 16
const SS2022_KEY_BYTES_DEFAULT = 32
const SS_LEGACY_SECRET_LEN = 16

export const UTLS_FINGERPRINTS = ['chrome', 'firefox', 'safari', 'ios', 'android', 'edge', '360', 'qq', 'random', 'randomized', 'randomizednoalpn', 'unsafe'] as const
export const TLS_VERSIONS = ['1.0', '1.1', '1.2', '1.3'] as const
export const ALPN_VALUES = ['h3', 'h2', 'http/1.1'] as const
export const SNIFFING_DESTS = ['http', 'tls', 'quic', 'fakedns'] as const
export const XHTTP_MODES = ['auto', 'packet-up', 'stream-up', 'stream-one'] as const
export const TRANSPORT_NETWORKS = ['tcp', 'udp', 'tcp,udp'] as const
export const CERT_USAGES = ['encipherment', 'verify', 'issue'] as const

/** VLESS encryption 的认证方式（面板 getNewVlessEnc 返回的 auths[].id）。 */
export const VLESS_AUTHS = [
  { value: 'x25519', label: 'X25519（非抗量子）' },
  { value: 'mlkem768', label: 'ML-KEM-768（抗量子）' },
  { value: 'x25519_xorpub', label: 'X25519 xorpub' },
  { value: 'x25519_random', label: 'X25519 random' },
  { value: 'mlkem768_xorpub', label: 'ML-KEM-768 xorpub' },
  { value: 'mlkem768_random', label: 'ML-KEM-768 random' },
] as const

const TLS_PROTOCOLS: readonly string[] = ['vmess', 'vless', 'trojan', 'shadowsocks']
const TLS_NETWORKS: readonly string[] = ['tcp', 'ws', 'grpc', 'httpupgrade', 'xhttp']
const REALITY_PROTOCOLS: readonly string[] = ['vless', 'trojan']
const REALITY_NETWORKS: readonly string[] = ['tcp', 'grpc', 'xhttp']
/** 可选传输层的协议；hysteria 传输固定，其余协议没有传输层设置。 */
const NETWORK_PROTOCOLS: readonly string[] = ['vless', 'vmess', 'trojan', 'shadowsocks']
const FALLBACK_PROTOCOLS: readonly string[] = ['vless', 'trojan']

const PORT_MIN_RANDOM = 10000
const PORT_SPAN_RANDOM = 50000
const BYTE_BITS = 8
const ACCOUNT_USER_LEN = 8
const ACCOUNT_SECRET_LEN = 12
const SHORT_ID_COUNT = 8
const SHORT_ID_MAX_BYTES = 8
const X25519_KEY_BYTES = 32
const ALPHANUM = 'abcdefghijklmnopqrstuvwxyz0123456789'

/** 表单草稿：基础字段 + 面板配置三段。 */
export interface InboundDraft {
  enabled: boolean
  remark: string
  protocol: Protocol
  listen: string
  port: number
  settings: JsonObject
  streamSettings: JsonObject
  sniffing: JsonObject
}

// ---- 能力判定（对照 3x-ui 的表单规则） --------------------------------------

export function networkOf(d: InboundDraft): string {
  const n = d.streamSettings.network
  return typeof n === 'string' ? n : 'tcp'
}

export function securityOf(d: InboundDraft): Security {
  const s = d.streamSettings.security
  return s === SECURITY_TLS || s === SECURITY_REALITY ? s : SECURITY_NONE
}

export function hasNetworkChoice(protocol: string): boolean {
  return NETWORK_PROTOCOLS.includes(protocol)
}

export function hasStream(protocol: string): boolean {
  return hasNetworkChoice(protocol) || protocol === 'hysteria'
}

export function canTls(protocol: string, network: string): boolean {
  if (protocol === 'hysteria') {
    return true
  }
  return TLS_PROTOCOLS.includes(protocol) && TLS_NETWORKS.includes(network)
}

export function canReality(protocol: string, network: string): boolean {
  return REALITY_PROTOCOLS.includes(protocol) && REALITY_NETWORKS.includes(network)
}

export function hasFallbacks(d: InboundDraft): boolean {
  return FALLBACK_PROTOCOLS.includes(d.protocol) && networkOf(d) === 'tcp' && securityOf(d) !== SECURITY_NONE
}

export function isSS2022(method: unknown): boolean {
  return typeof method === 'string' && method.startsWith(SS2022_PREFIX)
}

export function isKnownProtocol(protocol: string): protocol is Protocol {
  return (PROTOCOLS as readonly string[]).includes(protocol)
}

// ---- 随机值（浏览器安全随机数） ---------------------------------------------

function randomBytes(n: number): Uint8Array {
  const out = new Uint8Array(n)
  crypto.getRandomValues(out)
  return out
}

function toBase64(bytes: Uint8Array): string {
  return btoa(String.fromCharCode(...bytes))
}

function fromBase64(text: string): Uint8Array {
  return Uint8Array.from(atob(text), (c) => c.charCodeAt(0))
}

export function randomHex(bytes: number): string {
  return Array.from(randomBytes(bytes), (b) => b.toString(16).padStart(2, '0')).join('')
}

export function randomAlphanum(len: number): string {
  return Array.from(randomBytes(len), (b) => ALPHANUM[b % ALPHANUM.length]).join('')
}

export function randomPort(): number {
  const [hi, lo] = randomBytes(2)
  return PORT_MIN_RANDOM + (((hi << BYTE_BITS) | lo) % PORT_SPAN_RANDOM)
}

/** SS2022 的服务端密钥是指定长度随机字节的 Base64；老算法用随机口令。 */
export function ssServerKey(method: string): string {
  if (!isSS2022(method)) {
    return randomAlphanum(SS_LEGACY_SECRET_LEN)
  }
  return toBase64(randomBytes(method === SS2022_AES128 ? SS2022_KEY_BYTES_AES128 : SS2022_KEY_BYTES_DEFAULT))
}

/** Reality short id：一组长度不同（1–8 字节）的十六进制串。 */
export function randomShortIds(): string[] {
  return Array.from(randomBytes(SHORT_ID_COUNT), (b) => randomHex((b % SHORT_ID_MAX_BYTES) + 1))
}

const PKCS8_X25519_PREFIX = Uint8Array.from([0x30, 0x2e, 0x02, 0x01, 0x00, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x6e, 0x04, 0x22, 0x04, 0x20])
const X25519 = { name: 'X25519' }

function base64UrlToStd(text: string): string {
  const std = text.replace(/-/g, '+').replace(/_/g, '/')
  return std + '='.repeat((4 - (std.length % 4)) % 4)
}

/** 生成 WireGuard 密钥对（X25519，标准 Base64）。浏览器不支持 X25519 时抛错，由调用方提示。 */
export async function wireguardKeypair(): Promise<{ privateKey: string; publicKey: string }> {
  const pair = (await crypto.subtle.generateKey(X25519, true, ['deriveBits'])) as CryptoKeyPair
  const pkcs8 = new Uint8Array(await crypto.subtle.exportKey('pkcs8', pair.privateKey))
  const pub = new Uint8Array(await crypto.subtle.exportKey('raw', pair.publicKey))
  return { privateKey: toBase64(pkcs8.slice(pkcs8.length - X25519_KEY_BYTES)), publicKey: toBase64(pub) }
}

/** 由 WireGuard 私钥推出公钥；私钥格式不对返回空串。 */
export async function wireguardPublicKey(privateKey: string): Promise<string> {
  try {
    const raw = fromBase64(privateKey)
    if (raw.length !== X25519_KEY_BYTES) {
      return ''
    }
    const der = new Uint8Array(PKCS8_X25519_PREFIX.length + raw.length)
    der.set(PKCS8_X25519_PREFIX)
    der.set(raw, PKCS8_X25519_PREFIX.length)
    const key = await crypto.subtle.importKey('pkcs8', der, X25519, true, ['deriveBits'])
    const jwk = await crypto.subtle.exportKey('jwk', key)
    return jwk.x === undefined ? '' : base64UrlToStd(jwk.x)
  } catch {
    // 私钥不是合法的 Base64 / X25519 密钥：界面上不显示公钥即可，提交时由面板校验
    return ''
  }
}

// ---- 默认值 ------------------------------------------------------------------

function randomAccount(): JsonObject {
  const pass = randomAlphanum(ACCOUNT_SECRET_LEN)
  return { user: randomAlphanum(ACCOUNT_USER_LEN), pass }
}

/** 各协议新建时的 settings（不含 clients）。wireguard 私钥为异步生成，由表单打开后补上。 */
export function defaultSettings(protocol: Protocol): JsonObject {
  switch (protocol) {
    case 'vless':
      return { decryption: 'none', encryption: 'none', fallbacks: [] }
    case 'trojan':
      return { fallbacks: [] }
    case 'shadowsocks': {
      const method = SS_METHODS[0]
      const key = ssServerKey(method)
      return { method, password: key, network: 'tcp,udp', ivCheck: false }
    }
    case 'hysteria':
      return { version: 2 }
    case 'wireguard':
      return { mtu: 1420, secretKey: '', noKernelTun: false, subnetIp: '10.0.0.0', subnetCidr: 24 }
    case 'http':
      return { accounts: [randomAccount()], allowTransparent: false }
    case 'mixed':
      return { auth: 'password', accounts: [randomAccount()], udp: false, ip: '127.0.0.1' }
    case 'tunnel':
      return { portMap: {}, allowedNetwork: 'tcp,udp', followRedirect: false }
    case 'vmess':
    default:
      return {}
  }
}

export function defaultNetworkSettings(network: string): JsonObject {
  switch (network) {
    case 'kcp':
      return { mtu: 1350, tti: 20, uplinkCapacity: 5, downlinkCapacity: 20, cwndMultiplier: 1, maxSendingWindow: 2097152 }
    case 'ws':
      return { acceptProxyProtocol: false, path: '/', host: '', headers: {}, heartbeatPeriod: 0 }
    case 'grpc':
      return { serviceName: '', authority: '', multiMode: false }
    case 'httpupgrade':
      return { acceptProxyProtocol: false, path: '/', host: '', headers: {} }
    case 'xhttp':
      return { path: '/', host: '', mode: 'auto', xPaddingBytes: '100-1000', noSSEHeader: false, scMaxBufferedPosts: 30, scStreamUpServerSecs: '20-80', headers: {} }
    case NETWORK_HYSTERIA:
      return { version: 2, auth: '', udpIdleTimeout: 60 }
    case 'tcp':
    default:
      return { acceptProxyProtocol: false, header: { type: 'none' } }
  }
}

export function defaultCertificate(): JsonObject {
  return { useFile: true, certificateFile: '', keyFile: '', usage: 'encipherment', buildChain: false, ocspStapling: 0, oneTimeLoading: false }
}

export function defaultTlsSettings(protocol: string): JsonObject {
  return {
    serverName: '',
    minVersion: '1.2',
    maxVersion: '1.3',
    cipherSuites: '',
    rejectUnknownSni: false,
    disableSystemRoot: false,
    enableSessionResumption: false,
    certificates: [defaultCertificate()],
    alpn: protocol === 'hysteria' ? ['h3'] : ['h2', 'http/1.1'],
    echServerKeys: '',
    settings: { fingerprint: '', echConfigList: '' },
  }
}

export function defaultRealitySettings(): JsonObject {
  return {
    show: false,
    xver: 0,
    target: '',
    serverNames: [],
    privateKey: '',
    minClientVer: '',
    maxClientVer: '',
    maxTimediff: 0,
    shortIds: randomShortIds(),
    mldsa65Seed: '',
    settings: { publicKey: '', fingerprint: 'chrome', serverName: '', spiderX: '/', mldsa65Verify: '' },
  }
}

export function defaultSniffing(): JsonObject {
  return { enabled: false, destOverride: [...SNIFFING_DESTS], metadataOnly: false, routeOnly: false, ipsExcluded: [], domainsExcluded: [] }
}

export function defaultStream(protocol: Protocol): JsonObject {
  if (protocol === 'hysteria') {
    return { network: NETWORK_HYSTERIA, security: SECURITY_TLS, hysteriaSettings: defaultNetworkSettings(NETWORK_HYSTERIA), [TLS_KEY]: defaultTlsSettings(protocol) }
  }
  if (!hasNetworkChoice(protocol)) {
    return {}
  }
  return { network: 'tcp', security: SECURITY_NONE, tcpSettings: defaultNetworkSettings('tcp') }
}

export function newDraft(protocol: Protocol = 'vless'): InboundDraft {
  return {
    enabled: true,
    remark: '',
    protocol,
    listen: '',
    port: randomPort(),
    settings: defaultSettings(protocol),
    streamSettings: defaultStream(protocol),
    sniffing: defaultSniffing(),
  }
}

/** 换协议：settings 与传输层按新协议重置，基础字段保留。 */
export function withProtocol(d: InboundDraft, protocol: Protocol): InboundDraft {
  return { ...d, protocol, settings: defaultSettings(protocol), streamSettings: defaultStream(protocol) }
}

export function withSecurity(d: InboundDraft, security: Security): InboundDraft {
  const stream: JsonObject = { ...d.streamSettings, security }
  delete stream[TLS_KEY]
  delete stream[REALITY_KEY]
  if (security === SECURITY_TLS) {
    stream[TLS_KEY] = defaultTlsSettings(d.protocol)
  }
  if (security === SECURITY_REALITY) {
    stream[REALITY_KEY] = defaultRealitySettings()
  }
  return { ...d, streamSettings: stream }
}

/** 换传输：只保留新传输的设置；当前安全层在新传输下不可用时退回 none。 */
export function withNetwork(d: InboundDraft, network: Network): InboundDraft {
  const stream: JsonObject = { ...d.streamSettings }
  for (const key of Object.values(NETWORK_KEYS)) {
    delete stream[key]
  }
  stream.network = network
  stream[NETWORK_KEYS[network]] = defaultNetworkSettings(network)
  const next = { ...d, streamSettings: stream }
  const sec = securityOf(next)
  const isAllowed = sec === SECURITY_NONE || (sec === SECURITY_TLS ? canTls(d.protocol, network) : canReality(d.protocol, network))
  return isAllowed ? next : withSecurity(next, SECURITY_NONE)
}

export function networkKey(network: string): string {
  return NETWORK_KEYS[network] ?? NETWORK_KEYS.tcp
}

// ---- 路径读写（不可变） -------------------------------------------------------

/** 由路径生成稳定的输入框 id，让 label 与输入框关联（读屏与测试按 label 找输入框）。 */
export function fieldId(path: Path): string {
  return `inbound-${path.join('-')}`
}

export function getAt(root: unknown, path: Path): unknown {
  let cur = root
  for (const key of path) {
    if (cur === null || typeof cur !== 'object') {
      return undefined
    }
    cur = (cur as Record<string | number, unknown>)[key]
  }
  return cur
}

export function setAt<T>(root: T, path: Path, value: unknown): T {
  if (path.length === 0) {
    return value as T
  }
  const [head, ...rest] = path
  const container: unknown = root ?? (typeof head === 'number' ? [] : {})
  const copy = (Array.isArray(container) ? [...container] : { ...(container as JsonObject) }) as Record<string | number, unknown>
  copy[head] = setAt(copy[head], rest, value)
  return copy as T
}

// ---- 与接口互转 ---------------------------------------------------------------

function asObject(v: unknown): JsonObject {
  return v !== null && typeof v === 'object' && !Array.isArray(v) ? (v as JsonObject) : {}
}

/** 证书项在表单里多一个 useFile 开关，提交前去掉，并只保留所选来源的字段。 */
function certForWire(cert: unknown): JsonObject {
  const { useFile, certificateFile, keyFile, certificate, key, ...rest } = asObject(cert)
  return useFile === false ? { ...rest, certificate, key } : { ...rest, certificateFile, keyFile }
}

function certFromWire(cert: unknown): JsonObject {
  const c = asObject(cert)
  const hasInline = (Array.isArray(c.certificate) && c.certificate.length > 0) || (Array.isArray(c.key) && c.key.length > 0)
  return { ...c, useFile: Boolean(c.certificateFile) || Boolean(c.keyFile) || !hasInline }
}

function mapCerts(stream: JsonObject, map: (c: unknown) => JsonObject): JsonObject {
  if (stream[TLS_KEY] === undefined) {
    return stream
  }
  const tls = asObject(stream[TLS_KEY])
  const certs = Array.isArray(tls.certificates) ? tls.certificates : []
  return { ...stream, [TLS_KEY]: { ...tls, certificates: certs.map(map) } }
}

export interface InboundConfigPayload extends JsonObject {
  settings: JsonObject
  streamSettings: JsonObject
  sniffing: JsonObject
}

/** 草稿转成提交给 hub-server 的 config。 */
export function toConfig(d: InboundDraft): InboundConfigPayload {
  return { settings: d.settings, streamSettings: hasStream(d.protocol) ? mapCerts(d.streamSettings, certForWire) : {}, sniffing: d.sniffing }
}

export interface PanelConfig {
  protocol: string
  port: number
  listen: string
  remark: string
  enabled: boolean
  config: JsonObject
}

/** 面板配置（GET panel-config 的结果）转成草稿；不认识的协议回退成 vless，由调用方提示改用高级 JSON。 */
export function fromPanelConfig(cfg: PanelConfig): InboundDraft {
  return {
    enabled: cfg.enabled,
    remark: cfg.remark,
    protocol: isKnownProtocol(cfg.protocol) ? cfg.protocol : 'vless',
    listen: cfg.listen,
    port: cfg.port,
    settings: asObject(cfg.config.settings),
    streamSettings: mapCerts(asObject(cfg.config.streamSettings), certFromWire),
    sniffing: { ...defaultSniffing(), ...asObject(cfg.config.sniffing) },
  }
}
