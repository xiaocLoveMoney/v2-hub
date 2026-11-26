import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { CONFIRM, TEST_CSRF, mockServer, ok, renderWith } from '../../test/harness'
import { BYTES_PER_GB } from '../users/quota'
import { AgentInviteCodesPage, AgentLedgerPage, AgentOrdersPage, AgentTicketsPage } from './AgentRecordPages'
import { AgentSummaryPage } from './AgentSummaryPage'
import { AgentUsersPage } from './AgentUsersPage'
import { checkQuotaLowering } from './quota-lowering'

const AGENT = { kind: 'agent' as const, admin: { id: 40, username: 'reseller' } }
const NOW = '2026-11-25T00:00:00Z'
/** 表单里输入的初始密码（仅测试用）。 */
const typed = 'long-enough-value'

const plan = { id: 5, name: 'monthly-100g', trafficLimit: 100 * BYTES_PER_GB, durationDays: 30, trafficReset: 'never', limitIp: 0, poolCost: 100 * BYTES_PER_GB }
const summary = { maxUsers: 10, userCount: 3, trafficPoolTotal: 1000 * BYTES_PER_GB, trafficPoolUsed: 500 * BYTES_PER_GB }
const user = {
  id: 21, username: 'carol', email: null, remark: '', status: 'active', isDisabled: false, planId: 5, planName: 'monthly-100g',
  trafficLimit: 100 * BYTES_PER_GB, trafficUsed: 10 * BYTES_PER_GB, expiresAt: '2026-12-25T00:00:00Z', pendingDays: 0, balance: 0,
  totpEnabled: false, createdAt: NOW, lastUsedAt: null,
}

const conflict = (code: string, data?: unknown, message = 'raw') => () => Response.json({ code, message, data }, { status: 409 })
/** 后端在这两种错误里直接给出完整文案，前端原样显示。 */
const USER_LIMIT_MESSAGE = '名下用户数已达上限，不能再新建；请联系平台调整上限'
const RATE_LIMITED_MESSAGE = '操作过于频繁，请稍后再试'

/** 名下用户页的基础应答；extra 覆盖或追加。 */
function usersServer(extra: Record<string, () => Response> = {}) {
  return mockServer({
    'GET /api/admin/agent/users': () => ok({ items: [user], total: 1 }),
    'GET /api/admin/agent/plans': () => ok([plan]),
    'GET /api/admin/agent/summary': () => ok(summary),
    ...extra,
  })
}

async function pickOption(dialog: HTMLElement, label: string, option: string) {
  fireEvent.mouseDown(within(dialog).getByLabelText(label))
  const matches = await screen.findAllByTitle(option)
  fireEvent.click(matches[matches.length - 1])
}

const PLAN_OPTION = 'monthly-100g（100.00 GB · 30 天，扣减 100.00 GB）'

async function openCreate(): Promise<HTMLElement> {
  fireEvent.click(await screen.findByRole('button', { name: /新建用户/ }))
  const dialog = await screen.findByRole('dialog')
  fireEvent.change(within(dialog).getByLabelText('用户名'), { target: { value: 'dave' } })
  fireEvent.change(within(dialog).getByLabelText('初始密码'), { target: { value: typed } })
  return dialog
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('checkQuotaLowering（docs/03 调低的判定）', () => {
  const finite = { trafficLimit: 100, expiresAt: '2026-12-25T00:00:00.000Z', pendingDays: 0 }

  it('额度须大于 0 且不高于当前；相等视为不变', () => {
    expect(checkQuotaLowering(finite, { trafficLimit: 0, expiresAt: null }).error).toMatch(/大于 0/)
    expect(checkQuotaLowering(finite, { trafficLimit: 101, expiresAt: null }).error).toMatch(/只能调低/)
    expect(checkQuotaLowering(finite, { trafficLimit: 100, expiresAt: null }).error).toMatch(/没有改动/)
    expect(checkQuotaLowering(finite, { trafficLimit: 50, expiresAt: null }).body).toEqual({ trafficLimit: 50 })
    // 当前不限时任何正数都算调低
    expect(checkQuotaLowering({ ...finite, trafficLimit: 0 }, { trafficLimit: 999, expiresAt: null }).body).toEqual({ trafficLimit: 999 })
  })

  it('到期时间只能提前；当前不过期时任何时间都算提前；等待首次使用时不能改', () => {
    expect(checkQuotaLowering(finite, { trafficLimit: null, expiresAt: '2026-12-26T00:00:00.000Z' }).error).toMatch(/只能提前/)
    expect(checkQuotaLowering(finite, { trafficLimit: null, expiresAt: '2026-12-25T00:00:00.000Z' }).error).toMatch(/没有改动/)
    expect(checkQuotaLowering(finite, { trafficLimit: null, expiresAt: '2026-01-01T00:00:00.000Z' }).body).toEqual({ expiresAt: '2026-01-01T00:00:00.000Z' })
    const never = { trafficLimit: 0, expiresAt: null, pendingDays: 0 }
    expect(checkQuotaLowering(never, { trafficLimit: null, expiresAt: '2030-01-01T00:00:00.000Z' }).body).toEqual({ expiresAt: '2030-01-01T00:00:00.000Z' })
    const pending = { trafficLimit: 100, expiresAt: null, pendingDays: 30 }
    expect(checkQuotaLowering(pending, { trafficLimit: null, expiresAt: '2026-01-01T00:00:00.000Z' }).error).toMatch(/首次使用/)
  })
})

describe('AgentUsersPage', () => {
  it('新建必须选白名单套餐，选后显示扣减量与池里剩余；提交内容只走代理接口', async () => {
    const calls = usersServer({ 'POST /api/admin/agent/users': () => ok({ ...user, id: 22, username: 'dave' }) })
    renderWith([], <AgentUsersPage />, AGENT)
    expect(await screen.findByText('carol')).toBeDefined()
    const dialog = await openCreate()

    fireEvent.click(within(dialog).getByRole('button', { name: /创\s*建/ }))
    expect(await within(dialog).findByText('请选择套餐')).toBeDefined()
    expect(calls.some((c) => c.method === 'POST')).toBe(false)

    await pickOption(dialog, '套餐', PLAN_OPTION)
    expect(await within(dialog).findByText('本次从流量池扣减 100.00 GB，流量池剩余 500.00 GB')).toBeDefined()
    fireEvent.click(within(dialog).getByRole('button', { name: /创\s*建/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    const post = calls.find((c) => c.method === 'POST')
    expect(post?.path).toBe('/api/admin/agent/users')
    expect(post?.csrf).toBe(TEST_CSRF)
    expect(post?.body).toEqual({ username: 'dave', password: typed, remark: '', planId: 5 })
    expect(calls.every((c) => c.path.startsWith('/api/admin/agent/'))).toBe(true)
  })

  it('池里不够时开通前就警告；后端回 agent_pool_insufficient 时用 data 里的两个量提示', async () => {
    mockServer({
      'GET /api/admin/agent/users': () => ok({ items: [user], total: 1 }),
      'GET /api/admin/agent/plans': () => ok([plan]),
      'GET /api/admin/agent/summary': () => ok({ ...summary, trafficPoolUsed: 950 * BYTES_PER_GB }),
      'POST /api/admin/agent/users': conflict('agent_pool_insufficient', { poolCost: 100 * BYTES_PER_GB, poolRemaining: 50 * BYTES_PER_GB }),
    })
    renderWith([], <AgentUsersPage />, AGENT)
    await screen.findByText('carol')
    const dialog = await openCreate()
    await pickOption(dialog, '套餐', PLAN_OPTION)
    const text = '本次需要 100.00 GB，流量池只剩 50.00 GB，请联系平台补充'
    expect(await within(dialog).findByText(text)).toBeDefined()
    fireEvent.click(within(dialog).getByRole('button', { name: /创\s*建/ }))
    await waitFor(() => expect(within(dialog).getAllByText(text)).toHaveLength(2))
  })

  it('后端回 agent_user_limit 时提示已达名下用户数上限', async () => {
    usersServer({ 'POST /api/admin/agent/users': conflict('agent_user_limit', undefined, USER_LIMIT_MESSAGE) })
    renderWith([], <AgentUsersPage />, AGENT)
    await screen.findByText('carol')
    const dialog = await openCreate()
    await pickOption(dialog, '套餐', PLAN_OPTION)
    fireEvent.click(within(dialog).getByRole('button', { name: /创\s*建/ }))
    expect(await within(dialog).findByText('名下用户数已达上限，不能再新建；请联系平台调整上限')).toBeDefined()
  })

  it('分配套餐提交所选套餐并显示扣减量；429 提示操作过于频繁', async () => {
    const calls = usersServer({ 'POST /api/admin/agent/users/21/plan': () => Response.json({ code: 'rate_limited', message: RATE_LIMITED_MESSAGE }, { status: 429 }) })
    renderWith([], <AgentUsersPage />, AGENT)
    fireEvent.click(await screen.findByRole('button', { name: /分配套餐/ }))
    const dialog = await screen.findByRole('dialog')
    expect(await within(dialog).findByText('本次从流量池扣减 100.00 GB，流量池剩余 500.00 GB')).toBeDefined()
    fireEvent.click(within(dialog).getByRole('button', { name: /分\s*配/ }))
    await waitFor(() => expect(calls.some((c) => c.path === '/api/admin/agent/users/21/plan')).toBe(true))
    expect(calls.find((c) => c.path === '/api/admin/agent/users/21/plan')?.body).toEqual({ planId: 5 })
    expect(await within(dialog).findByText('操作过于频繁，请稍后再试')).toBeDefined()
  })

  it('调低额度：调高被前端拦下不提交；调低只提交改了的额度（GB 换算成字节）', async () => {
    const calls = usersServer({ 'PUT /api/admin/agent/users/21/quota': () => ok({ ...user, trafficLimit: 50 * BYTES_PER_GB }) })
    renderWith([], <AgentUsersPage />, AGENT)
    fireEvent.click(await screen.findByRole('button', { name: /调低额度/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('只能调低，不能调高或设为不限')).toBeDefined()
    const input = within(dialog).getByLabelText('流量额度（GB）')

    fireEvent.change(input, { target: { value: '200' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /保\s*存/ }))
    expect(await within(dialog).findByText('只能调低：新额度不能高于当前的 100.00 GB')).toBeDefined()
    expect(calls.some((c) => c.method === 'PUT')).toBe(false)

    fireEvent.change(input, { target: { value: '50' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /保\s*存/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({ trafficLimit: 50 * BYTES_PER_GB })
  })

  it('等待首次使用的用户不能改到期时间；什么都没改时不提交', async () => {
    const calls = usersServer({ 'GET /api/admin/agent/users': () => ok({ items: [{ ...user, expiresAt: null, pendingDays: 30 }], total: 1 }) })
    renderWith([], <AgentUsersPage />, AGENT)
    expect(await screen.findByText('首次使用后 30 天')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: /调低额度/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(/用户还在等首次使用/)).toBeDefined()
    expect(within(dialog).getByLabelText('到期时间')).toHaveProperty('disabled', true)
    fireEvent.click(within(dialog).getByRole('button', { name: /保\s*存/ }))
    expect(await within(dialog).findByText('没有改动：请调低流量额度或提前到期时间')).toBeDefined()
    expect(calls.some((c) => c.method === 'PUT')).toBe(false)
  })

  it('编辑改备注与停用；删除要确认后才提交', async () => {
    const calls = usersServer({
      'PUT /api/admin/agent/users/21': () => ok({ ...user, remark: 'vip', isDisabled: true }),
      'DELETE /api/admin/agent/users/21': () => ok(null),
    })
    renderWith([], <AgentUsersPage />, AGENT)
    fireEvent.click(await screen.findByRole('button', { name: /编\s*辑/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('备注'), { target: { value: 'vip' } })
    fireEvent.click(within(dialog).getByRole('switch'))
    fireEvent.click(within(dialog).getByRole('button', { name: /保\s*存/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({ remark: 'vip', isDisabled: true })

    fireEvent.click(screen.getByRole('button', { name: /删\s*除/ }))
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false)
    fireEvent.click(await screen.findByRole('button', { name: CONFIRM }))
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.path === '/api/admin/agent/users/21')).toBe(true))
  })
})

describe('AgentSummaryPage', () => {
  it('显示流量池、名下用户数、白名单套餐的扣减量与开通记录', async () => {
    const calls = mockServer({
      'GET /api/admin/agent/summary': () => ok(summary),
      'GET /api/admin/agent/plans': () => ok([plan]),
      'GET /api/admin/agent/allocations': () =>
        ok({ items: [{ id: 1, agentId: 40, userId: null, username: 'gone', planId: 5, planName: 'monthly-100g', trafficBytes: 100 * BYTES_PER_GB, durationDays: 30, kind: 'create', createdAt: NOW }], total: 1 }),
    })
    renderWith([], <AgentSummaryPage />, AGENT)
    expect(await screen.findByText('500.00 GB')).toBeDefined()
    expect(screen.getByText('已用 500.00 GB / 总量 1000.00 GB')).toBeDefined()
    expect(screen.getByText('3 / 10')).toBeDefined()
    expect(await screen.findByText('gone（已删除）')).toBeDefined()
    expect(screen.getByText('新建用户')).toBeDefined()
    expect(screen.getAllByText('100.00 GB').length).toBeGreaterThan(1)
    expect(calls.every((c) => c.path.startsWith('/api/admin/agent/'))).toBe(true)
  })
})

describe('代理的邀请码、工单、订单、流水', () => {
  const code = { id: 3, code: 'ABCDEFGH23', kind: 'agent', ownerUserId: null, ownerUsername: '', agentId: 40, agentUsername: 'reseller', maxUses: 5, usedCount: 1, expiresAt: null, isDisabled: false, remark: '', createdAt: NOW }

  it('邀请码新建与删除走代理接口', async () => {
    const calls = mockServer({
      'GET /api/admin/agent/invite-codes': () => ok({ items: [code], total: 1 }),
      'POST /api/admin/agent/invite-codes': () => ok({ ...code, id: 4 }),
      'DELETE /api/admin/agent/invite-codes/3': () => ok(null),
    })
    renderWith([], <AgentInviteCodesPage />, AGENT)
    expect(await screen.findByText('ABCDEFGH23')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: /新建邀请码/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: /创\s*建/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')?.body).toEqual({ maxUses: 1, expiresAt: null, remark: '', isDisabled: false })

    fireEvent.click(screen.getByRole('button', { name: /删\s*除/ }))
    fireEvent.click(await screen.findByRole('button', { name: CONFIRM }))
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.path === '/api/admin/agent/invite-codes/3')).toBe(true))
  })

  it('工单详情里员工的回复显示为平台客服、不带账号名；回复走代理接口', async () => {
    const ticket = { id: 8, userId: 21, username: 'carol', subject: '连不上', status: 'open', lastReplyAt: NOW, createdAt: NOW }
    const detail = {
      ...ticket,
      messages: [
        { id: 1, authorKind: 'user', content: '帮忙看看', createdAt: NOW },
        { id: 2, authorKind: 'admin', authorName: '', content: '已处理', createdAt: NOW },
        { id: 3, authorKind: 'admin', authorName: 'reseller', content: '好的', createdAt: NOW },
      ],
    }
    const calls = mockServer({
      'GET /api/admin/agent/tickets': () => ok({ items: [ticket], total: 1 }),
      'GET /api/admin/agent/tickets/8': () => ok(detail),
      'POST /api/admin/agent/tickets/8/messages': () => ok({ ...detail, status: 'answered' }),
    })
    renderWith([], <AgentTicketsPage />, AGENT)
    fireEvent.click(await screen.findByRole('button', { name: /查\s*看/ }))
    expect(await screen.findByText('平台客服')).toBeDefined()
    expect(screen.getByText('代理 reseller')).toBeDefined()
    fireEvent.change(screen.getByLabelText('回复'), { target: { value: '收到' } })
    fireEvent.click(screen.getByRole('button', { name: /发送回复/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')).toMatchObject({ path: '/api/admin/agent/tickets/8/messages', body: { content: '收到' } })
  })

  it('订单与流水只读、走代理接口；流水不显示操作人列', async () => {
    const calls = mockServer({
      'GET /api/admin/agent/orders': () => ok({ items: [], total: 0 }),
      'GET /api/admin/agent/wallet-ledger': () => ok({ items: [], total: 0 }),
    })
    renderWith([], <AgentOrdersPage />, AGENT)
    expect((await screen.findAllByText('实付')).length).toBeGreaterThan(0)
    cleanup()
    renderWith([], <AgentLedgerPage />, AGENT)
    expect((await screen.findAllByText('变动后余额')).length).toBeGreaterThan(0)
    expect(screen.queryByText('操作人')).toBeNull()
    await waitFor(() => expect(calls.map((c) => c.path)).toEqual(['/api/admin/agent/orders', '/api/admin/agent/wallet-ledger']))
  })
})
