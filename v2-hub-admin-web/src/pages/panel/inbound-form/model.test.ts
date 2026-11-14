import { describe, expect, it } from 'vitest'
import {
  REALITY_KEY,
  SECURITY_NONE,
  SECURITY_REALITY,
  SECURITY_TLS,
  TLS_KEY,
  canReality,
  canTls,
  fromPanelConfig,
  getAt,
  newDraft,
  prefillNetwork,
  prefillSecurity,
  REALITY_TARGET_CANDIDATES,
  securityOf,
  serverDomainOf,
  setAt,
  ssServerKey,
  toConfig,
  withNetwork,
  withProtocol,
  withSecurity,
  withWebCert,
  wireguardKeypair,
  wireguardPublicKey,
} from './model'

const SS2022_AES256_KEY_BYTES = 32
const SS2022_AES128_KEY_BYTES = 16

function decodedLength(b64: string): number {
  return atob(b64).length
}

describe('inbound form model（Ruling-16，对照 3x-ui 的表单规则）', () => {
  it('新建 vless 默认 RAW + 无安全层，不带 clients', () => {
    const d = newDraft()
    expect(d.protocol).toBe('vless')
    expect(d.streamSettings).toMatchObject({ network: 'tcp', security: SECURITY_NONE, tcpSettings: { header: { type: 'none' } } })
    expect(d.settings).toEqual({ decryption: 'none', encryption: 'none', fallbacks: [] })
    expect(d.port).toBeGreaterThanOrEqual(10000)
  })

  it('TLS / Reality 的可用性随协议与传输变化', () => {
    expect(canReality('vless', 'tcp')).toBe(true)
    expect(canReality('vless', 'ws')).toBe(false)
    expect(canReality('vmess', 'tcp')).toBe(false)
    expect(canTls('vmess', 'ws')).toBe(true)
    expect(canTls('vless', 'kcp')).toBe(false)
    expect(canTls('hysteria', 'hysteria')).toBe(true)
  })

  it('换传输只留新传输的设置，不再可用的安全层退回 none', () => {
    const reality = withSecurity(newDraft(), SECURITY_REALITY)
    expect(securityOf(reality)).toBe(SECURITY_REALITY)
    const ws = withNetwork(reality, 'ws')
    expect(ws.streamSettings.tcpSettings).toBeUndefined()
    expect(ws.streamSettings.wsSettings).toMatchObject({ path: '/' })
    expect(securityOf(ws)).toBe(SECURITY_NONE)
    expect(ws.streamSettings[REALITY_KEY]).toBeUndefined()
    const grpc = withNetwork(reality, 'grpc')
    expect(securityOf(grpc)).toBe(SECURITY_REALITY)
  })

  it('换协议重置 settings 与传输；hysteria 固定 hysteria 传输 + TLS', () => {
    const h = withProtocol(newDraft(), 'hysteria')
    expect(h.streamSettings).toMatchObject({ network: 'hysteria', security: SECURITY_TLS })
    expect((h.streamSettings[TLS_KEY] as { alpn: string[] }).alpn).toEqual(['h3'])
    const http = withProtocol(newDraft(), 'http')
    expect(http.streamSettings).toEqual({})
    expect(toConfig(http).streamSettings).toEqual({})
  })

  it('SS2022 服务端密钥按加密方式取长度', () => {
    expect(decodedLength(ssServerKey('2022-blake3-aes-256-gcm'))).toBe(SS2022_AES256_KEY_BYTES)
    expect(decodedLength(ssServerKey('2022-blake3-aes-128-gcm'))).toBe(SS2022_AES128_KEY_BYTES)
    const ss = withProtocol(newDraft(), 'shadowsocks')
    expect(decodedLength(String(ss.settings.password))).toBe(SS2022_AES256_KEY_BYTES)
  })

  it('证书的 useFile 只在表单里用，提交时去掉并只保留所选来源', () => {
    let d = withSecurity(newDraft(), SECURITY_TLS)
    d = setAt(d, ['streamSettings', TLS_KEY, 'certificates', 0, 'certificateFile'], '/c.pem')
    d = setAt(d, ['streamSettings', TLS_KEY, 'certificates', 0, 'certificate'], ['stale'])
    const cert = (toConfig(d).streamSettings[TLS_KEY] as { certificates: Record<string, unknown>[] }).certificates[0]
    expect(cert).toMatchObject({ certificateFile: '/c.pem', usage: 'encipherment' })
    expect(cert).not.toHaveProperty('useFile')
    expect(cert).not.toHaveProperty('certificate')
  })

  it('面板配置回显：内联证书识别为“证书内容”，嗅探补默认值', () => {
    const d = fromPanelConfig({
      protocol: 'trojan', port: 443, listen: '', remark: 'r', enabled: true,
      config: { settings: { fallbacks: [] }, streamSettings: { network: 'tcp', security: 'tls', tlsSettings: { certificates: [{ certificate: ['A'], key: ['B'] }] } }, sniffing: { enabled: true } },
    })
    expect(d.protocol).toBe('trojan')
    expect((d.streamSettings[TLS_KEY] as { certificates: { useFile: boolean }[] }).certificates[0].useFile).toBe(false)
    expect(d.sniffing).toMatchObject({ enabled: true, routeOnly: false })
  })

  it('WireGuard 密钥对：私钥能推出同一个公钥', async () => {
    const pair = await wireguardKeypair()
    expect(decodedLength(pair.privateKey)).toBe(SS2022_AES256_KEY_BYTES)
    expect(await wireguardPublicKey(pair.privateKey)).toBe(pair.publicKey)
    expect(await wireguardPublicKey('not-a-key')).toBe('')
  })
})

describe('新建入站的预填（Ruling-53）', () => {
  const hints = { domain: 'hk.example.com' }
  const RANDOM_PATH = /^\/[a-z0-9]{10}$/
  const wsPath = ['streamSettings', 'wsSettings']
  const tlsPath = ['streamSettings', TLS_KEY]
  const realityPath = ['streamSettings', REALITY_KEY]

  it('服务器的域名：取第一个域名，IP、单段主机名与坏地址不算', () => {
    expect(serverDomainOf(['203.0.114.9', 'HK.Example.com', 'jp.example.com'])).toBe('hk.example.com')
    expect(serverDomainOf(['', 'https://panel.example.com:2053/base'])).toBe('panel.example.com')
    expect(serverDomainOf(['203.0.114.9', 'https://203.0.114.9:2053', 'localhost', 'http://[::1]:2053', '://bad'])).toBe('')
  })

  it('WebSocket / HTTPUpgrade / XHTTP：路径给随机串，Host 给服务器域名；已填的不动', () => {
    for (const network of ['ws', 'httpupgrade', 'xhttp'] as const) {
      const d = prefillNetwork(withNetwork(newDraft(), network), hints)
      const base = ['streamSettings', `${network}Settings`]
      expect(getAt(d, [...base, 'path'])).toMatch(RANDOM_PATH)
      expect(getAt(d, [...base, 'host'])).toBe('hk.example.com')
    }
    const typed = setAt(setAt(withNetwork(newDraft(), 'ws'), [...wsPath, 'path'], '/mine'), [...wsPath, 'host'], 'cdn.example.net')
    expect(getAt(prefillNetwork(typed, hints), wsPath)).toMatchObject({ path: '/mine', host: 'cdn.example.net' })
    // 服务器只有 IP：路径照样随机，Host 留空
    const noDomain = prefillNetwork(withNetwork(newDraft(), 'ws'), { domain: '' })
    expect(getAt(noDomain, [...wsPath, 'host'])).toBe('')
    expect(getAt(noDomain, [...wsPath, 'path'])).toMatch(RANDOM_PATH)
  })

  it('gRPC 的 serviceName 给随机串；RAW 没有可预填的', () => {
    const grpc = prefillNetwork(withNetwork(newDraft(), 'grpc'), hints)
    expect(getAt(grpc, ['streamSettings', 'grpcSettings', 'serviceName'])).toMatch(/^[a-z0-9]{10}$/)
    const raw = newDraft()
    expect(prefillNetwork(raw, hints)).toEqual(raw)
  })

  it('TLS：SNI 给服务器域名、指纹 chrome；ws 下 ALPN 只留 http/1.1，先选 TLS 再换传输也一样', () => {
    const tls = prefillSecurity(withSecurity(newDraft(), SECURITY_TLS), hints)
    expect(getAt(tls, tlsPath)).toMatchObject({ serverName: 'hk.example.com', alpn: ['h2', 'http/1.1'], settings: { fingerprint: 'chrome' } })
    const wsFirst = prefillSecurity(withSecurity(prefillNetwork(withNetwork(newDraft(), 'ws'), hints), SECURITY_TLS), hints)
    expect(getAt(wsFirst, [...tlsPath, 'alpn'])).toEqual(['http/1.1'])
    const tlsFirst = prefillNetwork(withNetwork(tls, 'ws'), hints)
    expect(getAt(tlsFirst, [...tlsPath, 'alpn'])).toEqual(['http/1.1'])
    expect(getAt(tlsFirst, [...tlsPath, 'serverName'])).toBe('hk.example.com')
    // 没有域名时 SNI 留空；hysteria 的 TLS 不设指纹
    expect(getAt(prefillSecurity(withSecurity(newDraft(), SECURITY_TLS), { domain: '' }), [...tlsPath, 'serverName'])).toBe('')
    expect(getAt(prefillSecurity(newDraft('hysteria'), hints), [...tlsPath, 'settings', 'fingerprint'])).toBe('')
  })

  it('REALITY：目标与 SNI 取 3x-ui 的一个默认候选，SpiderX 给随机串；已填的目标不动', () => {
    const d = prefillSecurity(withSecurity(newDraft(), SECURITY_REALITY), hints)
    const target = getAt(d, [...realityPath, 'target']) as string
    expect(REALITY_TARGET_CANDIDATES as readonly string[]).toContain(target)
    expect(getAt(d, [...realityPath, 'serverNames'])).toEqual([target.replace(':443', '')])
    expect(getAt(d, [...realityPath, 'settings', 'spiderX'])).toMatch(/^\/[a-z0-9]{15}$/)
    const typed = setAt(withSecurity(newDraft(), SECURITY_REALITY), [...realityPath, 'target'], 'example.org:443')
    const kept = prefillSecurity(typed, hints)
    expect(getAt(kept, [...realityPath, 'target'])).toBe('example.org:443')
    expect(getAt(kept, [...realityPath, 'serverNames'])).toEqual([])
  })

  it('面板的网页证书路径只填进空着的文件证书；面板没配证书时不动', () => {
    const tls = withSecurity(newDraft(), SECURITY_TLS)
    const certs = [...tlsPath, 'certificates']
    const filled = withWebCert(tls, '/root/cert/fullchain.pem', '/root/cert/privkey.pem')
    expect(filled.isFilled).toBe(true)
    expect(getAt(filled.draft, [...certs, 0])).toMatchObject({ certificateFile: '/root/cert/fullchain.pem', keyFile: '/root/cert/privkey.pem' })
    expect(withWebCert(tls, '', '')).toEqual({ draft: tls, isFilled: false })
    const typed = setAt(tls, [...certs, 0, 'certificateFile'], '/etc/ssl/mine.pem')
    expect(withWebCert(typed, '/root/cert/fullchain.pem', '/root/cert/privkey.pem')).toEqual({ draft: typed, isFilled: false })
    const inline = setAt(tls, [...certs, 0, 'useFile'], false)
    expect(withWebCert(inline, '/a', '/b').isFilled).toBe(false)
  })
})
