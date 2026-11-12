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
  newDraft,
  securityOf,
  setAt,
  ssServerKey,
  toConfig,
  withNetwork,
  withProtocol,
  withSecurity,
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
