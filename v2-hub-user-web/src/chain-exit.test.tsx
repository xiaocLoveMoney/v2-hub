import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { buildExitLink, emptyExitFields, exitFieldProblems, parseExitLink, type ExitFields } from './pages/chain/exit-link'
import { TEST_CSRF, fail, mockServer, ok, renderApp } from './test/harness'

const NOW = '2026-11-14T00:00:00Z'
const EXITS_PATH = '/api/user/chain/exits'
const TEST_ID = '11111111-2222-3333-4444-555555555555'
const TEST_PUBLIC_KEY = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
const site = {
  siteName: '测试站', registerMode: 'invite', emailVerifyRequired: false, mailEnabled: false, checkinEnabled: false, ticketEnabled: false, sharedEnabled: false,
  shopEnabled: false, currency: 'CNY',
}
const features = { clientChain: true, customNode: true, serverChain: true }
const session = { user: { id: 7, username: 'alice', status: 'active', email: null, isEmailVerified: false }, csrfToken: TEST_CSRF, totpEnabled: false, impersonated: false, features }
const ownExit = { id: 9, name: '自家 VPS', protocol: 'vless', address: 'exit.example.com', port: 8443, isEnabled: true, routeCount: 0, createdAt: NOW }
const overview = {
  allowed: true, exitAllowed: true, serverAllowed: true, maxClientRoutes: 20, maxServerRoutes: 10, maxRouteEntries: 20, maxExits: 10,
  clientRoutes: [], serverRoutes: [], exits: [ownExit], platformExits: [], nodes: [],
}

function signedIn() {
  return {
    'GET /api/user/site': () => ok(site),
    'GET /api/user/auth/me': () => ok(session),
    'GET /api/user/chain': () => ok(overview),
  }
}

function fieldsOf(patch: Partial<ExitFields>): ExitFields {
  return { ...emptyExitFields(), address: 'exit.example.com', ...patch }
}

/** 拼出链接再解析回来，应当得到同一份字段与名称。 */
function expectRoundTrip(fields: ExitFields, name = '出口 A') {
  expect(exitFieldProblems(fields).size).toBe(0)
  expect(parseExitLink(buildExitLink(fields, name))).toEqual({ fields, name })
}

async function openExitsCard(): Promise<HTMLElement> {
  renderApp('/chain')
  return (await screen.findByText(/我的出口（1 \/ 10）/)).closest('section') as HTMLElement
}

function typeInto(dialog: HTMLElement, label: string, value: string) {
  fireEvent.change(within(dialog).getByLabelText(label), { target: { value } })
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

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
    expectRoundTrip(fieldsOf({ protocol: 'vless', id: TEST_ID, network: 'xhttp', path: '/x', mode: 'packet-up', security: 'tls', sni: 'a.example.com' }))
    expectRoundTrip(fieldsOf({ protocol: 'trojan', passphrase: 'p@ss w/ord#1', network: 'grpc', serviceName: 'grpc-svc', security: 'tls', sni: 'a.example.com' }))
    expectRoundTrip(fieldsOf({ protocol: 'vmess', id: TEST_ID, cipher: 'aes-128-gcm', network: 'ws', host: 'h.example.com', path: '/v', security: 'tls', sni: 'h.example.com' }))
    expectRoundTrip(fieldsOf({ protocol: 'vmess', id: TEST_ID, network: 'grpc', serviceName: 'svc' }))
    expectRoundTrip(fieldsOf({ protocol: 'shadowsocks', method: 'chacha20-ietf-poly1305', passphrase: 'a:b/c+d?e' }))
    expectRoundTrip(fieldsOf({ protocol: 'socks', port: '1080' }), '')
    expectRoundTrip(fieldsOf({ protocol: 'socks', port: '1080', username: 'us er', passphrase: 'p:w@x' }))
    expectRoundTrip(fieldsOf({ protocol: 'socks', port: '1080', username: 'only-user' }))
    expectRoundTrip(fieldsOf({ protocol: 'vless', id: TEST_ID, address: '2001:4860:4860::8888' }))
    // HTTP / HTTPS 代理（Ruling-51）：默认端口也要原样保留
    expectRoundTrip(fieldsOf({ protocol: 'http', port: '80' }), '')
    expectRoundTrip(fieldsOf({ protocol: 'http', port: '8080', username: 'us er', passphrase: 'p:w@x' }))
    expectRoundTrip(fieldsOf({ protocol: 'https', port: '443', username: 'only-user', sni: 'front.example.com', isInsecure: true }))
    expectRoundTrip(fieldsOf({ protocol: 'https', port: '8443' }))
  })

  it('拼出的形状与后端解析一致：SIP002 用 URL 安全的 Base64，socks 有用户名时总带冒号，IPv6 带方括号', () => {
    expect(buildExitLink(fieldsOf({ protocol: 'shadowsocks', method: 'aes-256-gcm', passphrase: '???>>>' }), '')).toBe(
      'ss://YWVzLTI1Ni1nY206Pz8_Pj4-@exit.example.com:443',
    )
    expect(buildExitLink(fieldsOf({ protocol: 'socks', port: '1080', username: 'u' }), 'n')).toBe('socks5://u:@exit.example.com:1080#n')
    expect(buildExitLink(fieldsOf({ protocol: 'vless', id: TEST_ID, address: '::1' }), '')).toContain('@[::1]:443?')
    expect(buildExitLink(fieldsOf({ protocol: 'http', port: '8080', username: 'u', passphrase: 'p' }), 'n')).toBe('http://u:p@exit.example.com:8080#n')
    expect(buildExitLink(fieldsOf({ protocol: 'https', sni: 'front.example.com', isInsecure: true }), '')).toBe(
      'https://exit.example.com:443?sni=front.example.com&allowInsecure=1',
    )
    // http 代理不带 sni；带路径的网页地址不是代理地址
    expect(buildExitLink(fieldsOf({ protocol: 'http', port: '3128', sni: 'x' }), '')).toBe('http://exit.example.com:3128')
    expect(parseExitLink('https://exit.example.com/index.html')).toBeNull()
    expect(parseExitLink('http://exit.example.com:8080/path')).toBeNull()
    expect(parseExitLink('http://only-user@exit.example.com:8080/')?.fields).toMatchObject({ protocol: 'http', username: 'only-user', passphrase: '', port: '8080' })
    // 与当前选择无关的字段不进链接：传输是 RAW 时不带 host / path，没有安全层时不带 sni
    const link = buildExitLink(fieldsOf({ protocol: 'vless', id: TEST_ID, host: 'x', path: '/y', sni: 'z', flow: 'xtls-rprx-vision' }), '')
    expect(link).toBe(`vless://${TEST_ID}@exit.example.com:443?type=tcp&security=none&encryption=none`)
  })

  it('认得常见的外来写法：旧式 ss、Base64 凭据的 socks、3x-ui 的多余参数', () => {
    expect(parseExitLink(`ss://${btoa('aes-128-gcm:pw@198.51.100.7:8388')}#old`)?.fields).toMatchObject({
      protocol: 'shadowsocks', method: 'aes-128-gcm', passphrase: 'pw', address: '198.51.100.7', port: '8388',
    })
    expect(parseExitLink(`socks://${btoa('bob:pw')}@198.51.100.7:1080`)?.fields).toMatchObject({ username: 'bob', passphrase: 'pw' })
    const parsed = parseExitLink(`vless://${TEST_ID}@a.example.com:443?type=tcp&security=tls&headerType=none&encryption=none&extra=1&sni=a.example.com#x`)
    expect(parsed?.fields).toMatchObject({ protocol: 'vless', security: 'tls', sni: 'a.example.com' })
  })

  it('表单表达不了的链接返回 null', () => {
    expect(parseExitLink('hysteria2://pw@a.example.com:443')).toBeNull()
    expect(parseExitLink(`vless://${TEST_ID}@a.example.com:443?type=tcp&headerType=http`)).toBeNull()
    expect(parseExitLink(`vless://${TEST_ID}@a.example.com:443?type=kcp`)).toBeNull()
    expect(parseExitLink(`vless://${TEST_ID}@a.example.com:70000`)).toBeNull()
    expect(parseExitLink(`vmess://${btoa(JSON.stringify({ add: 'a.example.com', port: 443, id: TEST_ID, aid: 2 }))}`)).toBeNull()
    expect(parseExitLink('vmess://not-base64!')).toBeNull()
    expect(parseExitLink('not a link')).toBeNull()
  })

  it('缺项检查：只查必填与端口', () => {
    expect([...exitFieldProblems(emptyExitFields())].sort()).toEqual(['address', 'id'])
    expect([...exitFieldProblems(fieldsOf({ protocol: 'trojan', port: '0' }))].sort()).toEqual(['passphrase', 'port'])
    expect([...exitFieldProblems(fieldsOf({ protocol: 'socks', passphrase: 'x' }))]).toEqual(['username'])
    expect([...exitFieldProblems(fieldsOf({ protocol: 'https', passphrase: 'x' }))]).toEqual(['username'])
    expect([...exitFieldProblems(fieldsOf({ protocol: 'vless', id: TEST_ID, security: 'reality' }))].sort()).toEqual(['publicKey', 'sni'])
  })
})

describe('我的出口：添加与编辑弹窗', () => {
  it('添加：默认是表单，缺项点保存才标出；按协议、传输、安全层显示字段；提交拼好的链接', async () => {
    const calls = mockServer({ ...signedIn(), [`POST ${EXITS_PATH}`]: () => ok({ ...ownExit, id: 10 }) })
    const card = await openExitsCard()
    fireEvent.click(within(card).getByRole('button', { name: '添加' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByRole('button', { name: '表单' }).getAttribute('aria-pressed')).toBe('true')
    expect((within(dialog).getByLabelText('端口') as HTMLInputElement).value).toBe('443')
    expect(within(dialog).queryByLabelText('SNI')).toBeNull()
    expect(within(dialog).queryByLabelText('路径')).toBeNull()

    fireEvent.click(within(dialog).getByRole('button', { name: '保存' }))
    expect(within(dialog).getAllByText('必填')).toHaveLength(2)
    expect(calls.some((c) => c.method === 'POST')).toBe(false)

    typeInto(dialog, '地址', 'exit.example.com')
    typeInto(dialog, 'ID', TEST_ID)
    typeInto(dialog, '传输', 'ws')
    typeInto(dialog, '路径', '/ws')
    fireEvent.click(within(dialog).getByRole('button', { name: 'TLS' }))
    typeInto(dialog, 'SNI', 'exit.example.com')
    fireEvent.click(within(dialog).getByLabelText('h2'))
    typeInto(dialog, '名称（可选）', '东京')
    fireEvent.click(within(dialog).getByRole('button', { name: '保存' }))

    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.path === EXITS_PATH)).toBe(true))
    const sent = calls.find((c) => c.method === 'POST' && c.path === EXITS_PATH)
    expect(sent?.csrf).toBe(TEST_CSRF)
    const body = sent?.body as { link: string; name: string }
    expect(body.name).toBe('东京')
    expect(parseExitLink(body.link)).toEqual({
      name: '东京',
      fields: fieldsOf({ protocol: 'vless', id: TEST_ID, network: 'ws', path: '/ws', security: 'tls', sni: 'exit.example.com', alpn: ['h2'] }),
    })
  })

  it('表单与分享链接可来回切换；转不成表单的链接留在链接方式并原样提交', async () => {
    const calls = mockServer({ ...signedIn(), [`POST ${EXITS_PATH}`]: () => ok({ ...ownExit, id: 10 }) })
    const card = await openExitsCard()
    fireEvent.click(within(card).getByRole('button', { name: '添加' }))
    const dialog = await screen.findByRole('dialog')

    fireEvent.click(within(dialog).getByRole('button', { name: '分享链接' }))
    typeInto(dialog, '出口链接', 'trojan://pw-1@t.example.com:8443?type=grpc&serviceName=svc&security=tls&sni=t.example.com#特洛伊')
    fireEvent.click(within(dialog).getByRole('button', { name: '表单' }))
    expect((within(dialog).getByLabelText('协议') as HTMLSelectElement).value).toBe('trojan')
    expect((within(dialog).getByLabelText('口令') as HTMLInputElement).value).toBe('pw-1')
    expect((within(dialog).getByLabelText('serviceName') as HTMLInputElement).value).toBe('svc')
    expect((within(dialog).getByLabelText('名称（可选）') as HTMLInputElement).value).toBe('特洛伊')

    typeInto(dialog, '端口', '9443')
    fireEvent.click(within(dialog).getByRole('button', { name: '分享链接' }))
    expect((within(dialog).getByLabelText('出口链接') as HTMLTextAreaElement).value).toContain('@t.example.com:9443?')

    const hysteria = 'hysteria2://pw-2@h.example.com:443#hy'
    typeInto(dialog, '出口链接', hysteria)
    fireEvent.click(within(dialog).getByRole('button', { name: '表单' }))
    expect(within(dialog).getByText(/这条链接转不成表单/)).toBeDefined()
    expect(within(dialog).getByRole('button', { name: '分享链接' }).getAttribute('aria-pressed')).toBe('true')
    fireEvent.click(within(dialog).getByRole('button', { name: '保存' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')?.body).toMatchObject({ link: hysteria })
  })

  it('编辑：默认不动连接参数，只改名时不带链接也不取链接', async () => {
    const calls = mockServer({ ...signedIn(), [`PUT ${EXITS_PATH}/9`]: () => ok(ownExit) })
    const card = await openExitsCard()
    fireEvent.click(within(card).getByRole('button', { name: '编辑' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('连接参数不变。')).toBeDefined()
    expect(within(dialog).queryByLabelText('地址')).toBeNull()
    typeInto(dialog, '名称', '新名字')
    fireEvent.click(within(dialog).getByRole('button', { name: '保存' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({ name: '新名字' })
    expect(calls.some((c) => c.path.endsWith('/link'))).toBe(false)
  })

  it('编辑：点“修改连接参数”才取现有链接填进表单，保存时带上新链接', async () => {
    const current = `vless://${TEST_ID}@exit.example.com:8443?type=tcp&security=reality&sni=www.example.com&fp=chrome&pbk=${TEST_PUBLIC_KEY}&sid=0a1b&flow=xtls-rprx-vision#old`
    const calls = mockServer({
      ...signedIn(),
      [`GET ${EXITS_PATH}/9/link`]: () => ok({ link: current }),
      [`PUT ${EXITS_PATH}/9`]: () => ok(ownExit),
    })
    const card = await openExitsCard()
    fireEvent.click(within(card).getByRole('button', { name: '编辑' }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: '修改连接参数' }))
    expect(((await within(dialog).findByLabelText('公钥')) as HTMLInputElement).value).toBe(TEST_PUBLIC_KEY)
    expect((within(dialog).getByLabelText('Flow') as HTMLSelectElement).value).toBe('xtls-rprx-vision')
    expect((within(dialog).getByLabelText('名称') as HTMLInputElement).value).toBe('自家 VPS')

    typeInto(dialog, '地址', 'new.example.com')
    fireEvent.click(within(dialog).getByRole('button', { name: '保存' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    const body = calls.find((c) => c.method === 'PUT')?.body as { name: string; link: string }
    expect(body.name).toBe('自家 VPS')
    expect(parseExitLink(body.link)?.fields).toMatchObject({ address: 'new.example.com', port: '8443', security: 'reality', shortId: '0a1b' })
  })
})

describe('我的出口：HTTP 代理与延迟测试', () => {
  const nodes = [
    { id: 1, name: '香港 A', region: 'HK', protocol: 'vless', canBeExit: true, canBeServerEntry: true },
    { id: 2, name: '日本 Hy2', region: 'JP', protocol: 'hysteria2', canBeExit: false, canBeServerEntry: false },
    { id: 3, name: '美国 B', region: 'US', protocol: 'trojan', canBeExit: true, canBeServerEntry: true },
  ]
  const withNodes = (list: unknown[]) => ({ ...signedIn(), 'GET /api/user/chain': () => ok({ ...overview, nodes: list }) })
  const TEST_PATH = `${EXITS_PATH}/9/test`

  it('添加 HTTPS 代理：选协议后只剩地址、端口、用户名、口令、SNI 与允许不安全', async () => {
    const calls = mockServer({ ...signedIn(), [`POST ${EXITS_PATH}`]: () => ok({ ...ownExit, id: 10, protocol: 'http' }) })
    const card = await openExitsCard()
    fireEvent.click(within(card).getByRole('button', { name: '添加' }))
    const dialog = await screen.findByRole('dialog')
    typeInto(dialog, '协议', 'https')
    expect(within(dialog).queryByLabelText('ID')).toBeNull()
    expect(within(dialog).queryByLabelText('传输')).toBeNull()
    typeInto(dialog, '地址', 'proxy.example.com')
    typeInto(dialog, '端口', '8443')
    typeInto(dialog, '口令（可选）', 'pw')
    fireEvent.click(within(dialog).getByRole('button', { name: '保存' }))
    // 只填口令不填用户名：标出用户名
    expect(within(dialog).getAllByText('必填')).toHaveLength(1)
    typeInto(dialog, '用户名（可选）', 'bob')
    typeInto(dialog, 'SNI', 'front.example.com')
    fireEvent.click(within(dialog).getByRole('button', { name: '保存' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')?.body).toEqual({ link: 'https://bob:pw@proxy.example.com:8443?sni=front.example.com', name: '' })
  })

  it('测试延迟：只列面板支持的节点，按所选节点与方式提交，显示延迟、HTTP 状态与出口 IP', async () => {
    const calls = mockServer({
      ...withNodes(nodes),
      [`POST ${TEST_PATH}`]: () =>
        ok({ ok: true, mode: 'http', reason: 'none', delayMs: 87, httpStatus: 204, egressIp: '198.51.100.7', egressRegion: 'JP' }),
    })
    const card = await openExitsCard()
    expect(within(card).queryByLabelText('起点节点')).toBeNull()
    fireEvent.click(within(card).getByRole('button', { name: '测试延迟' }))
    const from = within(card).getByLabelText('起点节点') as HTMLSelectElement
    expect([...from.options].map((o) => o.textContent)).toEqual(['香港 A', '美国 B'])
    fireEvent.change(from, { target: { value: '3' } })
    fireEvent.click(within(card).getByRole('button', { name: 'HTTP 请求' }))
    fireEvent.click(within(card).getByRole('button', { name: '开始测试' }))
    expect(await within(card).findByText('87 ms')).toBeDefined()
    expect(within(card).getByText('HTTP 204')).toBeDefined()
    expect(within(card).getByText(/出口 IP 198\.51\.100\.7/).textContent).toBe('出口 IP 198.51.100.7 (JP)')
    const sent = calls.find((c) => c.method === 'POST' && c.path === TEST_PATH)
    expect(sent?.csrf).toBe(TEST_CSRF)
    expect(sent?.body).toEqual({ nodeId: 3, mode: 'http' })
  })

  it('测试延迟：连不上按原因大类说明；被拒绝时显示后端的说明；没有可用起点时给提示', async () => {
    const failed = (reason: string) => ok({ ok: false, mode: 'tcp', reason, delayMs: 0, httpStatus: 0, egressIp: '', egressRegion: '' })
    let reply = () => failed('unknown')
    mockServer({ ...withNodes(nodes), [`POST ${TEST_PATH}`]: () => reply() })
    const card = await openExitsCard()
    fireEvent.click(within(card).getByRole('button', { name: '测试延迟' }))
    fireEvent.click(within(card).getByRole('button', { name: '开始测试' }))
    expect(await within(card).findByText('连接失败或超时')).toBeDefined()
    expect(within(card).queryByText(/出口 IP/)).toBeNull()

    // 归得了类的给标题和排查提示（Ruling-56）
    reply = () => failed('proxy_no_response')
    fireEvent.click(within(card).getByRole('button', { name: '开始测试' }))
    expect(await within(card).findByText('代理没有回应')).toBeDefined()
    expect(within(card).getByText(/不在代理的白名单里/)).toBeDefined()
    expect(within(card).queryByText('连接失败或超时')).toBeNull()
    reply = () => failed('refused')
    fireEvent.click(within(card).getByRole('button', { name: '开始测试' }))
    expect(await within(card).findByText('出口拒绝连接')).toBeDefined()
    expect(within(card).queryByText(/不在代理的白名单里/)).toBeNull()

    reply = () => fail(429, 'rate_limited', '测试太频繁，请稍后再试')
    fireEvent.click(within(card).getByRole('button', { name: '开始测试' }))
    expect(await within(card).findByText('测试太频繁，请稍后再试')).toBeDefined()
    expect(within(card).queryByText('连接失败或超时')).toBeNull()
    cleanup()
    vi.unstubAllGlobals()

    mockServer(withNodes([nodes[1]]))
    const empty = await openExitsCard()
    fireEvent.click(within(empty).getByRole('button', { name: '测试延迟' }))
    expect(within(empty).getByText(/没有可用的起点节点/)).toBeDefined()
  })
})
