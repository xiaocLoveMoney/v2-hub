import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { ReactNode } from 'react'
import { RouterProvider, createMemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SessionProvider } from './auth/SessionProvider'
import { ToastProvider } from './components/ToastProvider'
import { daysLeft, formatBytes, regionFlag, usedPercent } from './format'
import { maskSecretUrl } from './pages/subscription'
import { routes } from './routes'

const CSRF = 'csrf-test-token'
const GIB = 1024 ** 3
const ok = (data: unknown) => Response.json({ code: '0', message: 'ok', data })
const fail = (status: number, code: string, message: string) => Response.json({ code, message }, { status })

interface Recorded {
  method: string
  path: string
  csrf: string | null
  body: unknown
}

/** 按「方法 路径」返回预设响应并记录请求；未登记的路径返回 404。 */
function mockServer(handlers: Record<string, () => Response>) {
  const calls: Recorded[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (req: Request) => {
      const path = new URL(req.url).pathname
      const text = await req.clone().text()
      calls.push({ method: req.method, path, csrf: req.headers.get('X-CSRF-Token'), body: text === '' ? null : JSON.parse(text) })
      const handler = handlers[`${req.method} ${path}`]
      return handler === undefined ? fail(404, 'not_found', '不存在') : handler()
    }),
  )
  return calls
}

function renderApp(initialPath: string): ReactNode {
  const router = createMemoryRouter(routes, { initialEntries: [initialPath] })
  render(
    <ToastProvider>
      <SessionProvider>
        <RouterProvider router={router} />
      </SessionProvider>
    </ToastProvider>,
  )
  return null
}

const session = { user: { id: 7, username: 'alice', status: 'active' }, csrfToken: CSRF, totpEnabled: false }
const dashboard = {
  status: 'active', planName: '基础版', trafficLimit: 10 * GIB, trafficUsed: 4 * GIB, trafficToday: GIB, expiresAt: '2099-01-01T00:00:00Z',
  pendingDays: 0, limitIp: 3, onlineDevices: 1, lastUsedAt: null, nodeCount: 2,
}
const subUrl = 'https://user.example.com/sub/abcdefghijklmnopqrstuvwxyz012345'
const subscription = {
  url: subUrl, updateIntervalHours: 12,
  formats: ['base64', 'mihomo', 'singbox', 'xray'].map((format) => ({ format, url: `${subUrl}?format=${format}` })),
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('format helpers', () => {
  it('字节、百分比、剩余天数、国旗与地址打码', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(1536)).toBe('1.50 KB')
    expect(formatBytes(10 * GIB)).toBe('10.00 GB')
    expect(usedPercent(4 * GIB, 10 * GIB)).toBe(40)
    expect(usedPercent(5, 0)).toBe(0)
    expect(daysLeft('2026-01-11T00:00:00Z', Date.parse('2026-01-01T12:00:00Z'))).toBe(10)
    expect(daysLeft('2020-01-01T00:00:00Z', Date.parse('2026-01-01T00:00:00Z'))).toBe(0)
    expect(regionFlag('us')).toBe('🇺🇸')
    expect(regionFlag('Hong Kong')).toBe('')
    const masked = maskSecretUrl(subUrl)
    expect(masked.startsWith('https://user.example.com/sub/')).toBe(true)
    expect(masked.endsWith('012345')).toBe(true)
    expect(masked).not.toContain('abcdefghij')
  })
})

describe('登录', () => {
  it('未登录跳到登录页；密码通过后要求两步验证码，带验证码再次提交后进入仪表盘', async () => {
    let attempts = 0
    const calls = mockServer({
      'GET /api/user/auth/me': () => fail(401, 'unauthenticated', '请先登录'),
      'POST /api/user/auth/login': () => {
        attempts += 1
        return attempts === 1 ? fail(401, 'totp_required', '请输入两步验证码') : ok(session)
      },
      'GET /api/user/dashboard': () => ok(dashboard),
      'GET /api/user/traffic': () => ok([]),
      'GET /api/user/subscription': () => ok(subscription),
    })
    renderApp('/nodes')
    fireEvent.change(await screen.findByLabelText('用户名'), { target: { value: 'alice' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'correct-horse-battery' } })
    fireEvent.click(screen.getByRole('button', { name: '登录' }))

    const code = await screen.findByLabelText('两步验证码')
    fireEvent.change(code, { target: { value: '12a3456' } })
    expect((code as HTMLInputElement).value).toBe('123456')
    fireEvent.click(screen.getByRole('button', { name: '验证并登录' }))

    await waitFor(() => expect(calls.filter((c) => c.path === '/api/user/auth/login')).toHaveLength(2))
    expect(calls.filter((c) => c.path === '/api/user/auth/login')[1]?.body).toMatchObject({ username: 'alice', totpCode: '123456' })
  })

  it('密码错误时显示后端给的提示，不进入后台', async () => {
    mockServer({
      'GET /api/user/auth/me': () => fail(401, 'unauthenticated', '请先登录'),
      'POST /api/user/auth/login': () => fail(401, 'unauthenticated', '用户名或密码错误'),
    })
    renderApp('/')
    fireEvent.change(await screen.findByLabelText('用户名'), { target: { value: 'alice' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'wrong-guess-123' } })
    fireEvent.click(screen.getByRole('button', { name: '登录' }))
    expect(await screen.findByText('用户名或密码错误')).toBeDefined()
    expect(screen.queryByText('仪表盘')).toBeNull()
  })
})

describe('仪表盘', () => {
  it('显示四张统计卡；订阅地址默认打码，重置须二次确认并带 CSRF 头', async () => {
    const nextUrl = 'https://user.example.com/sub/zyxwvutsrqponmlkjihgfedcba987654'
    const calls = mockServer({
      'GET /api/user/auth/me': () => ok(session),
      'GET /api/user/dashboard': () => ok(dashboard),
      'GET /api/user/traffic': () => ok([]),
      'GET /api/user/subscription': () => ok(subscription),
      'POST /api/user/subscription/reset': () => ok({ ...subscription, url: nextUrl }),
    })
    renderApp('/')
    // 剩余流量在统计卡与环形图里各出现一次
    expect((await screen.findAllByText('6.00 GB')).length).toBeGreaterThan(0)
    expect(screen.getByText('1 / 3')).toBeDefined()
    expect(screen.getByText('2 个')).toBeDefined()
    expect(screen.getByText(/今日已用 1\.00 GB · 已用 40%/)).toBeDefined()

    const urlBox = (await screen.findByLabelText('订阅地址')) as HTMLInputElement
    expect(urlBox.value).not.toContain('abcdefghij')
    fireEvent.click(screen.getByRole('button', { name: '显示订阅地址' }))
    expect(urlBox.value).toBe(subUrl)

    fireEvent.click(screen.getByRole('button', { name: /重置订阅/ }))
    expect(calls.some((c) => c.method === 'POST')).toBe(false)
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: '重置' }))
    await waitFor(() => expect(calls.some((c) => c.path === '/api/user/subscription/reset')).toBe(true))
    expect(calls.find((c) => c.path === '/api/user/subscription/reset')?.csrf).toBe(CSRF)
    await waitFor(() => expect((screen.getByLabelText('订阅地址') as HTMLInputElement).value).toContain('987654'))
    expect((screen.getByLabelText('订阅地址') as HTMLInputElement).value).not.toContain('zyxwvutsrq')
  })

  it('到期的用户看到到期横幅；停用的账号只看到停用提示', async () => {
    mockServer({
      'GET /api/user/auth/me': () => ok(session),
      'GET /api/user/dashboard': () => ok({ ...dashboard, status: 'expired' }),
      'GET /api/user/traffic': () => ok([]),
      'GET /api/user/subscription': () => ok(subscription),
    })
    renderApp('/')
    expect(await screen.findByText(/套餐已到期/)).toBeDefined()
    cleanup()

    mockServer({ 'GET /api/user/auth/me': () => ok({ ...session, user: { ...session.user, status: 'disabled' } }) })
    renderApp('/')
    expect(await screen.findByText(/账号已停用/)).toBeDefined()
    expect(screen.queryByText('仪表盘', { selector: 'h1' })).toBeNull()
  })
})

describe('节点列表', () => {
  it('按节点组分段；点查看才去取节点链接', async () => {
    const calls = mockServer({
      'GET /api/user/auth/me': () => ok(session),
      'GET /api/user/nodes': () =>
        ok([{ id: 1, name: '香港组', nodes: [{ id: 11, name: 'HK 01', region: 'HK', protocol: 'vless', isServerOnline: true, serverLoad: 12.4, onlineClients: 5 }] }]),
      'GET /api/user/nodes/11/link': () => ok({ link: 'vless://sample-uuid@hk.example.com:443#HK%2001' }),
    })
    renderApp('/nodes')
    expect(await screen.findByText('香港组')).toBeDefined()
    expect(screen.getByText('负载 12%')).toBeDefined()
    expect(calls.some((c) => c.path.endsWith('/link'))).toBe(false)

    fireEvent.click(screen.getByRole('button', { name: '查看' }))
    const link = (await screen.findByLabelText('节点链接')) as HTMLInputElement
    expect(link.value).toContain('hk.example.com')
  })
})

describe('我的账号', () => {
  it('两次新密码不一致不能提交；一致后带旧密码提交', async () => {
    const calls = mockServer({
      'GET /api/user/auth/me': () => ok(session),
      'GET /api/user/login-logs': () => ok([{ clientIp: '203.0.113.9', isSuccess: true, createdAt: '2026-11-13T00:00:00Z' }]),
      'PUT /api/user/auth/password': () => ok(null),
    })
    renderApp('/account')
    expect(await screen.findByText('203.0.113.9')).toBeDefined()
    fireEvent.change(screen.getByLabelText('当前密码'), { target: { value: 'correct-horse-battery' } })
    fireEvent.change(screen.getByLabelText('新密码'), { target: { value: 'another-horse-staple' } })
    fireEvent.change(screen.getByLabelText('再输一次新密码'), { target: { value: 'another-horse' } })
    expect(screen.getByText('两次输入不一致')).toBeDefined()
    expect((screen.getByRole('button', { name: '保存' }) as HTMLButtonElement).disabled).toBe(true)

    fireEvent.change(screen.getByLabelText('再输一次新密码'), { target: { value: 'another-horse-staple' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    const put = calls.find((c) => c.method === 'PUT')
    expect(put?.csrf).toBe(CSRF)
    expect(put?.body).toEqual({ oldPassword: 'correct-horse-battery', newPassword: 'another-horse-staple' })
  })
})
