import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { App as AntApp, ConfigProvider } from 'antd'
import type { ReactNode } from 'react'
import { RouterProvider, createMemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setCsrfToken } from '../../api/client'
import { SessionContext, type SessionContextValue } from '../../auth/session-context'
import { PERMISSIONS } from '../../menu'
import { PlansPage } from './PlansPage'
import { UsersPage } from './UsersPage'
import { BYTES_PER_GB, renewFieldsOf, renewModeOf } from './quota'

const CSRF = 'csrf-test-token'
const ALL = [PERMISSIONS.USER_READ, PERMISSIONS.USER_WRITE, PERMISSIONS.PLAN_READ, PERMISSIONS.PLAN_WRITE]
const ok = (data: unknown) => Response.json({ code: '0', message: 'ok', data })

interface Recorded {
  method: string
  path: string
  csrf: string | null
  body: unknown
}

function mockServer(handlers: Record<string, () => Response>) {
  const calls: Recorded[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (req: Request) => {
      const path = new URL(req.url).pathname
      const text = await req.clone().text()
      calls.push({ method: req.method, path, csrf: req.headers.get('X-CSRF-Token'), body: text === '' ? null : JSON.parse(text) })
      const handler = handlers[`${req.method} ${path}`]
      return handler === undefined ? Response.json({ code: 'not_found', message: '' }, { status: 404 }) : handler()
    }),
  )
  return calls
}

function renderWith(node: ReactNode) {
  const granted = new Set<string>(ALL)
  const value: SessionContextValue = {
    status: 'authenticated',
    session: { admin: { id: 1, username: 'root' }, kind: 'staff', permissions: ALL, csrfToken: CSRF, totpEnabled: false, menuOrder: [], isSuperAdmin: false, isOwner: false, totpSetupRequired: false },
    login: async () => null,
    logout: async () => undefined,
    refresh: async () => undefined,
    can: (p) => granted.has(p),
  }
  setCsrfToken(CSRF)
  const router = createMemoryRouter([{ path: '/', element: node }], { initialEntries: ['/'] })
  return render(
    <ConfigProvider>
      <AntApp>
        <SessionContext.Provider value={value}>
          <RouterProvider router={router} />
        </SessionContext.Provider>
      </AntApp>
    </ConfigProvider>,
  )
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const plan = {
  id: 3, name: 'basic', trafficLimit: 100 * BYTES_PER_GB, durationDays: 30, startOnFirstUse: false, limitIp: 2,
  trafficReset: 'monthly', trafficResetDay: 1, allowClientChain: false, allowCustomNode: false,
  remark: '', nodeGroupIds: [7], userCount: 1, createdAt: '2026-11-12T00:00:00Z',
  isOnSale: true, isTrial: false, sortOrder: 0, description: '', prices: [{ id: 11, durationDays: 30, price: 990 }],
}

const user = {
  id: 9, username: 'alice@example.com', remark: 'vip', planId: 3, planName: 'basic', status: 'limited', isDisabled: false,
  trafficLimit: 100 * BYTES_PER_GB, trafficUsed: 100 * BYTES_PER_GB, expiresAt: '2026-12-12T00:00:00Z', pendingDays: 0, limitIp: 2,
  renewEveryDays: 0, renewMonthDay: 0, renewWeekday: 0, renewMax: 0, renewCount: 0, trafficReset: 'monthly', trafficResetDay: 1,
  provisions: { synced: 1, pending: 0, failed: 1 }, agent: null, createdAt: '2026-11-12T00:00:00Z',
}

describe('quota helpers', () => {
  it('自动续期三选一与接口字段互转', () => {
    expect(renewFieldsOf('month', 15)).toEqual({ renewEveryDays: 0, renewMonthDay: 15, renewWeekday: 0 })
    expect(renewModeOf({ renewEveryDays: 0, renewMonthDay: 0, renewWeekday: 3 })).toBe('week')
    expect(renewModeOf({ renewEveryDays: 0, renewMonthDay: 0, renewWeekday: 0 })).toBe('none')
  })
})

describe('PlansPage', () => {
  it('列表显示额度、售价与上架状态；新建套餐按 GB 换算成字节、价格按元换算成分，且不再带免费续期字段', async () => {
    const calls = mockServer({
      'GET /api/admin/plans': () => ok([plan]),
      'GET /api/admin/node-groups': () => ok([{ id: 7, name: 'hk', remark: '', nodeIds: [] }]),
      'POST /api/admin/plans': () => ok({ ...plan, id: 4, name: 'pro' }),
    })
    renderWith(<PlansPage />)
    expect(await screen.findByText('100.00 GB')).toBeDefined()
    expect(screen.getByText(/30 天 ¥9\.90/)).toBeDefined()
    expect(screen.getByText('上架')).toBeDefined()
    expect(await screen.findByText('hk')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: /新建套餐/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('名称'), { target: { value: 'pro' } })
    fireEvent.change(within(dialog).getByLabelText('流量额度（GB）'), { target: { value: '50' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /添加时长选项/ }))
    fireEvent.change(await within(dialog).findByLabelText('天数'), { target: { value: '90' } })
    fireEvent.change(within(dialog).getByLabelText('价格'), { target: { value: '19.9' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /创\s*建/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    const post = calls.find((c) => c.method === 'POST')
    expect(post?.csrf).toBe(CSRF)
    expect(post?.body).toMatchObject({
      name: 'pro', trafficLimit: 50 * BYTES_PER_GB, durationDays: 30, trafficReset: 'never', isOnSale: false, isTrial: false,
      prices: [{ durationDays: 90, price: 1990 }],
    })
    expect(post?.body).not.toHaveProperty('renewEveryDays')
  })
})

describe('UsersPage', () => {
  it('列表显示状态与下发；改用量里清零已用后提交', async () => {
    const calls = mockServer({
      'GET /api/admin/users': () => ok({ items: [user], total: 1 }),
      'GET /api/admin/plans': () => ok([plan]),
      'PUT /api/admin/users/9/quota': () => ok({ ...user, status: 'active', trafficUsed: 0 }),
    })
    renderWith(<UsersPage />)
    expect(await screen.findByText('流量用尽')).toBeDefined()
    expect(screen.getByText('失败 1')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: /改用量/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: /清\s*零/ }))
    fireEvent.click(within(dialog).getByRole('button', { name: /保\s*存/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    expect(calls.find((c) => c.method === 'PUT')?.body).toMatchObject({
      trafficUsed: 0, trafficLimit: 100 * BYTES_PER_GB, limitIp: 2, trafficReset: 'monthly', trafficResetDay: 1, renewEveryDays: 0,
      expiresAt: '2026-12-12T00:00:00.000Z',
    })
  })

  it('新建用户可同时选套餐；续费提交所选套餐', async () => {
    const calls = mockServer({
      'GET /api/admin/users': () => ok({ items: [{ ...user, status: 'expired' }], total: 1 }),
      'GET /api/admin/plans': () => ok([plan]),
      'POST /api/admin/users': () => ok({ ...user, id: 10, username: 'bob' }),
      'POST /api/admin/users/9/plan': () => ok({ ...user, status: 'active' }),
    })
    renderWith(<UsersPage />)
    await screen.findByText('已到期')

    fireEvent.click(screen.getByRole('button', { name: /新建用户/ }))
    let dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('用户名'), { target: { value: 'bob' } })
    fireEvent.change(within(dialog).getByLabelText('初始密码'), { target: { value: 'correct-horse-battery' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /创\s*建/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.path === '/api/admin/users')).toBe(true))
    expect(calls.find((c) => c.path === '/api/admin/users' && c.method === 'POST')?.body).toMatchObject({ username: 'bob', remark: '' })

    fireEvent.click(screen.getByRole('button', { name: /续\s*费/ }))
    const dialogs = await screen.findAllByRole('dialog')
    dialog = dialogs[dialogs.length - 1]
    fireEvent.click(within(dialog).getByRole('button', { name: /续\s*费/ }))
    await waitFor(() => expect(calls.some((c) => c.path === '/api/admin/users/9/plan')).toBe(true))
    expect(calls.find((c) => c.path === '/api/admin/users/9/plan')?.body).toEqual({ planId: 3 })
  })

  it('重置订阅与清除两步验证都要二次确认后才提交', async () => {
    const calls = mockServer({
      'GET /api/admin/users': () => ok({ items: [{ ...user, status: 'active', totpEnabled: true, menuOrder: [] }], total: 1 }),
      'GET /api/admin/plans': () => ok([plan]),
      'POST /api/admin/users/9/subscription/reset': () => ok(null),
      'DELETE /api/admin/users/9/totp': () => ok(null),
    })
    renderWith(<UsersPage />)
    await screen.findByText('alice@example.com')

    fireEvent.click(screen.getByRole('button', { name: /重置订阅/ }))
    expect(calls.some((c) => c.method === 'POST')).toBe(false)
    fireEvent.click(await screen.findByRole('button', { name: /^重\s*置$/ }))
    await waitFor(() => expect(calls.some((c) => c.path === '/api/admin/users/9/subscription/reset')).toBe(true))
    expect(calls.find((c) => c.path === '/api/admin/users/9/subscription/reset')?.csrf).toBe(CSRF)

    fireEvent.click(screen.getByRole('button', { name: /清除两步验证/ }))
    fireEvent.click(await screen.findByRole('button', { name: /^清\s*除$/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.path === '/api/admin/users/9/totp')).toBe(true))
  })
})
