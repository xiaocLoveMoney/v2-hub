import { describe, expect, it } from 'vitest'
import { buildExitLink, emptyExitFields, exitFieldProblems, parseExitLink, type ExitFields } from './chain-exit-link'

const TEST_ID = '11111111-2222-3333-4444-555555555555'
const TEST_PUBLIC_KEY = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'

function fieldsOf(patch: Partial<ExitFields>): ExitFields {
  return { ...emptyExitFields(), address: 'exit.example.com', ...patch }
}

/** 拼出链接再解析回来，应当得到同一份字段与名称。 */
function expectRoundTrip(fields: ExitFields, name = '出口 A') {
  expect(exitFieldProblems(fields).size).toBe(0)
  expect(parseExitLink(buildExitLink(fields, name))).toEqual({ fields, name })
}

describe('出口表单与分享链接互转', () => {
  it('各协议拼出的链接能原样解析回来', () => {
    expectRoundTrip(fieldsOf({ protocol: 'vless', id: TEST_ID }))
    expectRoundTrip(
      fieldsOf({
        protocol: 'vless', id: TEST_ID, network: 'ws', host: 'cdn.example.com', path: '/ws?ed=2048', security: 'tls', sni: 'cdn.example.com',
        fingerprint: 'chrome', alpn: ['h2', 'http/1.1'], isInsecure: true,
      }),
    )
    expectRoundTrip(
      fieldsOf({
        protocol: 'vless', id: TEST_ID, flow: 'xtls-rprx-vision', security: 'reality', sni: 'www.example.com', fingerprint: 'firefox',
        publicKey: TEST_PUBLIC_KEY, shortId: '0a1b', spiderX: '/',
      }),
    )
    expectRoundTrip(fieldsOf({ protocol: 'trojan', passphrase: 'p@ss w/ord#1', network: 'grpc', serviceName: 'grpc-svc', security: 'tls', sni: 'a.example.com' }))
    expectRoundTrip(fieldsOf({ protocol: 'vmess', id: TEST_ID, cipher: 'aes-128-gcm', network: 'ws', host: 'h.example.com', path: '/v', security: 'tls', sni: 'h.example.com' }))
    expectRoundTrip(fieldsOf({ protocol: 'shadowsocks', method: 'chacha20-ietf-poly1305', passphrase: 'a:b/c+d?e' }))
    expectRoundTrip(fieldsOf({ protocol: 'socks', port: '1080', username: 'us er', passphrase: 'p:w@x' }))
    expectRoundTrip(fieldsOf({ protocol: 'socks', port: '1080', username: 'only-user' }))
    expectRoundTrip(fieldsOf({ protocol: 'vless', id: TEST_ID, address: '2001:4860:4860::8888' }))
    // HTTP / HTTPS 代理（Ruling-51）：默认端口也要原样保留
    expectRoundTrip(fieldsOf({ protocol: 'http', port: '80', username: 'u', passphrase: 'p:w@x' }))
    expectRoundTrip(fieldsOf({ protocol: 'https', port: '443', sni: 'front.example.com', isInsecure: true }), '')
  })

  it('表单表达不了的链接返回 null', () => {
    expect(parseExitLink('hysteria2://pw@a.example.com:443')).toBeNull()
    expect(parseExitLink(`vless://${TEST_ID}@a.example.com:443?type=tcp&headerType=http`)).toBeNull()
    expect(parseExitLink(`vless://${TEST_ID}@a.example.com:70000`)).toBeNull()
    expect(parseExitLink('https://exit.example.com/index.html')).toBeNull()
    expect(parseExitLink('not a link')).toBeNull()
  })

  it('缺项检查：只查必填与端口', () => {
    expect([...exitFieldProblems(emptyExitFields())].sort()).toEqual(['address', 'id'])
    expect([...exitFieldProblems(fieldsOf({ protocol: 'vless', id: TEST_ID, security: 'reality' }))].sort()).toEqual(['publicKey', 'sni'])
  })
})
