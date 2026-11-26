import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { PERMISSIONS } from '../../menu'
import { CONFIRM, TEST_CSRF, mockServer, ok, renderWith } from '../../test/harness'
import { BYTES_PER_GB } from '../users/quota'
import { UsersPage } from '../users/UsersPage'
import { AgentsPage } from './AgentsPage'

const NOW = '2026-11-25T00:00:00Z'
/** 表单里输入的初始密码（仅测试用）。 */
const typed = 'long-enough-value'
/** 不是整 GB 的总量：没改流量池时要原样提交，不能被两位小数的回显舍入改掉。 */
const ODD_TOTAL = 1000 * BYTES_PER_GB + 12345
const SUPER = [PERMISSIONS.ADMIN_READ, PERMISSIONS.ADMIN_WRITE, PERMISSIONS.PLAN_READ, PERMISSIONS.USER_READ, PERMISSIONS.USER_WRITE]

const agent = {
  id: 7, username: 'reseller', remark: '华东', isDisabled: false, totpEnabled: true, lastLoginAt: null, createdAt: NOW,
  maxUsers: 10, userCount: 3, trafficPoolTotal: ODD_TOTAL, trafficPoolUsed: 200 * BYTES_PER_GB, planIds: [3],
}
const basePlan = {
  durationDays: 30, startOnFirstUse: false, limitIp: 0, trafficReset: 'never', trafficResetDay: 1, allowClientChain: false, allowCustomNode: false,
  remark: '', nodeGroupIds: [], userCount: 0, createdAt: NOW, isOnSale: true, isTrial: false, sortOrder: 0, description: '', prices: [],
}
const plans = [
  { ...basePlan, id: 3, name: 'basic', trafficLimit: 100 * BYTES_PER_GB },
  { ...basePlan, id: 4, name: 'unlimited', trafficLimit: 0 },
]

function agentsServer(extra: Record<string, () => Response> = {}) {
  return mockServer({
    'GET /api/admin/agents': () => ok({ items: [agent], total: 1 }),
    'GET /api/admin/plans': () => ok(plans),
    ...extra,
  })
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('AgentsPage', () => {
  it('列表显示状态、名下用户数 / 上限、流量池已用 / 总量；新建只列有限套餐，流量池按 GB 换算成字节', async () => {
    const calls = agentsServer({ 'POST /api/admin/agents': () => ok({ ...agent, id: 8, username: 'east' }) })
    renderWith(SUPER, <AgentsPage />)
    expect(await screen.findByText('reseller')).toBeDefined()
    expect(screen.getByText('3 / 10')).toBeDefined()
    expect(screen.getByText('200.00 GB / 1000.00 GB')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: /新建代理/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('用户名'), { target: { value: 'east' } })
    fireEvent.change(within(dialog).getByLabelText('初始密码'), { target: { value: typed } })
    fireEvent.change(within(dialog).getByLabelText('名下用户数上限'), { target: { value: '5' } })
    fireEvent.change(within(dialog).getByLabelText('流量池总量（GB）'), { target: { value: '1.5' } })
    fireEvent.mouseDown(within(dialog).getByLabelText('可分配的套餐'))
    fireEvent.click((await screen.findAllByTitle('basic（100.00 GB · 30 天）')).pop() as HTMLElement)
    expect(screen.queryByTitle(/unlimited/)).toBeNull()
    fireEvent.click(within(dialog).getByRole('button', { name: /创\s*建/ }))

    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    const post = calls.find((c) => c.method === 'POST')
    expect(post?.csrf).toBe(TEST_CSRF)
    expect(post?.body).toEqual({ username: 'east', password: typed, remark: '', maxUsers: 5, trafficPoolTotal: Math.round(1.5 * BYTES_PER_GB), planIds: [3] })
  })

  it('编辑：没改流量池时原样提交字节数；调到低于已用被拦下；调高按 GB 换算', async () => {
    const calls = agentsServer({ 'PUT /api/admin/agents/7': () => ok(agent) })
    renderWith(SUPER, <AgentsPage />)
    fireEvent.click(await screen.findByRole('button', { name: /编\s*辑/ }))
    let dialog = await screen.findByRole('dialog')
    expect((within(dialog).getByLabelText('流量池总量（GB）') as HTMLInputElement).value).toBe('1000.00')
    fireEvent.click(within(dialog).getByRole('switch'))
    fireEvent.click(within(dialog).getByRole('button', { name: /保\s*存/ }))
    await waitFor(() => expect(calls.filter((c) => c.method === 'PUT')).toHaveLength(1))
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({ remark: '华东', maxUsers: 10, trafficPoolTotal: ODD_TOTAL, planIds: [3], isDisabled: true })

    fireEvent.click(await screen.findByRole('button', { name: /编\s*辑/ }))
    const dialogs = await screen.findAllByRole('dialog')
    dialog = dialogs[dialogs.length - 1]
    const pool = within(dialog).getByLabelText('流量池总量（GB）')
    fireEvent.change(pool, { target: { value: '100' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /保\s*存/ }))
    expect(await within(dialog).findByText('不能低于已用的 200.00 GB')).toBeDefined()
    expect(calls.filter((c) => c.method === 'PUT')).toHaveLength(1)

    fireEvent.change(pool, { target: { value: '2000' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /保\s*存/ }))
    await waitFor(() => expect(calls.filter((c) => c.method === 'PUT')).toHaveLength(2))
    expect(calls.filter((c) => c.method === 'PUT')[1]?.body).toMatchObject({ trafficPoolTotal: 2000 * BYTES_PER_GB })
  })

  it('开通记录按代理分页查看；重置密码与清除两步验证沿用管理员接口', async () => {
    const calls = agentsServer({
      'GET /api/admin/agents/7/allocations': () =>
        ok({ items: [{ id: 1, agentId: 7, userId: 21, username: 'carol', planId: 3, planName: 'basic', trafficBytes: 100 * BYTES_PER_GB, durationDays: 30, kind: 'assign', createdAt: NOW }], total: 1 }),
      'PUT /api/admin/admins/7/password': () => ok(null),
      'DELETE /api/admin/admins/7/totp': () => ok(null),
    })
    renderWith(SUPER, <AgentsPage />)
    fireEvent.click(await screen.findByRole('button', { name: /开通记录/ }))
    expect(await screen.findByText('carol #21')).toBeDefined()
    expect(screen.getByText('分配 / 续期')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: /close|关闭/i }))

    fireEvent.click(screen.getByRole('button', { name: /重置密码/ }))
    const dialog = await screen.findByRole('dialog', { name: /重置密码/ })
    fireEvent.change(within(dialog).getByLabelText('新密码'), { target: { value: typed } })
    fireEvent.click(within(dialog).getByRole('button', { name: /重\s*置/ }))
    await waitFor(() => expect(calls.some((c) => c.path === '/api/admin/admins/7/password')).toBe(true))

    fireEvent.click(screen.getByRole('button', { name: /清除两步验证/ }))
    fireEvent.click(await screen.findByRole('button', { name: CONFIRM }))
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.path === '/api/admin/admins/7/totp')).toBe(true))
  })

  it('只有 admin:read 时没有新建与写操作按钮，仍能看开通记录', async () => {
    agentsServer()
    renderWith([PERMISSIONS.ADMIN_READ], <AgentsPage />)
    expect(await screen.findByText('reseller')).toBeDefined()
    expect(screen.queryByRole('button', { name: /新建代理/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /编\s*辑/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /重置密码/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /清除两步验证/ })).toBeNull()
    expect(screen.getByRole('button', { name: /开通记录/ })).toBeDefined()
  })
})

describe('UsersPage 的归属（docs/03 §5.23）', () => {
  const baseUser = {
    id: 9, username: 'alice', remark: '', planId: null, planName: '', status: 'active', isDisabled: false, trafficLimit: 0, trafficUsed: 0,
    expiresAt: null, pendingDays: 0, limitIp: 0, renewEveryDays: 0, renewMonthDay: 0, renewWeekday: 0, renewMax: 0, renewCount: 0,
    trafficReset: 'never', trafficResetDay: 1, provisions: { synced: 0, pending: 0, failed: 0 }, agent: null, createdAt: NOW,
  }
  const owned = { ...baseUser, id: 10, username: 'bob', agent: { id: 7, username: 'reseller' } }

  it('归属列：无归属显示平台；按代理筛选带 agentId；划转可收回为平台用户', async () => {
    const calls = mockServer({
      'GET /api/admin/users': () => ok({ items: [baseUser, owned], total: 2 }),
      'GET /api/admin/plans': () => ok(plans),
      'GET /api/admin/agents': () => ok({ items: [agent], total: 1 }),
      'PUT /api/admin/users/10/agent': () => ok({ ...owned, agent: null }),
    })
    renderWith(SUPER, <UsersPage />)
    expect(await screen.findByText('代理 reseller')).toBeDefined()
    expect(screen.getByText('平台')).toBeDefined()

    fireEvent.mouseDown(screen.getByRole('combobox', { name: '按代理过滤' }))
    fireEvent.click((await screen.findAllByTitle('reseller')).pop() as HTMLElement)
    await waitFor(() => expect(calls.some((c) => c.path === '/api/admin/users' && c.query.includes('agentId=7'))).toBe(true))

    const row = screen.getByText('bob').closest('tr') as HTMLElement
    fireEvent.click(within(row).getByRole('button', { name: /划\s*转/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('代理 reseller')).toBeDefined()
    fireEvent.click(within(dialog).getByText('平台（收回）'))
    fireEvent.click(within(dialog).getByRole('button', { name: /保\s*存/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    expect(calls.find((c) => c.method === 'PUT')).toMatchObject({ path: '/api/admin/users/10/agent', body: { agentId: null } })
  })

  it('代理筛选下拉展开时才请求代理列表', async () => {
    const calls = mockServer({
      'GET /api/admin/users': () => ok({ items: [baseUser], total: 1 }),
      'GET /api/admin/plans': () => ok(plans),
      'GET /api/admin/agents': () => ok({ items: [agent], total: 1 }),
    })
    renderWith(SUPER, <UsersPage />)
    expect(await screen.findByText('alice')).toBeDefined()
    expect(calls.some((c) => c.path === '/api/admin/agents')).toBe(false)

    fireEvent.mouseDown(screen.getByRole('combobox', { name: '按代理过滤' }))
    expect((await screen.findAllByTitle('reseller')).length).toBeGreaterThan(0)
    expect(calls.filter((c) => c.path === '/api/admin/agents')).toHaveLength(1)
  })

  it('没有 admin:read / admin:write 时不列代理、不出现划转，也不请求代理列表', async () => {
    const calls = mockServer({
      'GET /api/admin/users': () => ok({ items: [owned], total: 1 }),
    })
    renderWith([PERMISSIONS.USER_READ, PERMISSIONS.USER_WRITE], <UsersPage />)
    expect(await screen.findByText('代理 reseller')).toBeDefined()
    expect(screen.queryByRole('combobox', { name: '按代理过滤' })).toBeNull()
    expect(screen.queryByRole('button', { name: /划\s*转/ })).toBeNull()
    expect(calls.some((c) => c.path === '/api/admin/agents')).toBe(false)
  })
})
