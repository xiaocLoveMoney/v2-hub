import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { PERMISSIONS } from '../../menu'
import { amountToCents, centsToAmount, formatMoney } from '../../money'
import { TEST_CSRF, mockServer, ok, renderWith } from '../../test/harness'
import { UsersPage } from '../users/UsersPage'
import { CouponsPage } from './CouponsPage'
import { FinanceStatsPage } from './FinanceStatsPage'
import { LedgerPage, OrdersPage } from './RecordPages'
import { TopupCodesPage } from './TopupCodesPage'
import { TrafficPacksPage } from './TrafficPacksPage'

const NOW = '2026-11-13T00:00:00Z'
const GIB = 1024 ** 3
const SHOP_ALL = [PERMISSIONS.SHOP_READ, PERMISSIONS.SHOP_WRITE]
const FINANCE_ALL = [PERMISSIONS.FINANCE_READ, PERMISSIONS.FINANCE_WRITE]
/** 后端返回的充值码明文（仅测试用）。 */
const generated = ['ABCDEFGH23456789', 'JKMNPQRSTUVWXYZ2']

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('money', () => {
  it('元与分互转不带浮点尾数，显示按币种格式化', () => {
    expect(amountToCents(19.9)).toBe(1990)
    expect(amountToCents(0.1 + 0.2)).toBe(30)
    expect(centsToAmount(990)).toBe(9.9)
    expect(formatMoney(990)).toBe('¥9.90')
    expect(formatMoney(-1250, 'USD')).toContain('12.50')
  })
})

describe('TrafficPacksPage', () => {
  it('新建流量包：流量按 GB 换成字节、价格按元换成分', async () => {
    const pack = { id: 1, name: '加油包', trafficBytes: 10 * GIB, price: 300, isOnSale: true, sortOrder: 0, createdAt: NOW }
    const calls = mockServer({ 'GET /api/admin/traffic-packs': () => ok([pack]), 'POST /api/admin/traffic-packs': () => ok(pack) })
    renderWith(SHOP_ALL, <TrafficPacksPage />)
    expect(await screen.findByText('10.00 GB')).toBeDefined()
    expect(screen.getByText('¥3.00')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: /新建流量包/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('名称'), { target: { value: '大包' } })
    fireEvent.change(within(dialog).getByLabelText('流量（GB）'), { target: { value: '50' } })
    fireEvent.change(within(dialog).getByLabelText('价格'), { target: { value: '12.5' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /创\s*建/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    const post = calls.find((c) => c.method === 'POST')
    expect(post?.csrf).toBe(TEST_CSRF)
    expect(post?.body).toEqual({ name: '大包', trafficBytes: 50 * GIB, price: 1250, isOnSale: true, sortOrder: 0 })
  })
})

describe('CouponsPage', () => {
  const used = {
    id: 1, code: 'WELCOME10', kind: 'percent', value: 10, allPlans: true, planIds: [], forTrafficPack: false, minAmount: 0, maxUses: 0, maxUsesPerUser: 1,
    startsAt: null, endsAt: null, isDisabled: false, remark: '', usedCount: 3, createdAt: NOW,
  }

  it('用过的优惠码不给删除；新建时码转大写、比例原样提交', async () => {
    const calls = mockServer({ 'GET /api/admin/coupons': () => ok({ items: [used], total: 1 }), 'POST /api/admin/coupons': () => ok(used) })
    renderWith(SHOP_ALL, <CouponsPage />)
    const row = (await screen.findByText('WELCOME10')).closest('tr') as HTMLElement
    expect(within(row).getByText('10%')).toBeDefined()
    expect(within(row).getByText('3 / 不限')).toBeDefined()
    expect(within(row).queryByRole('button', { name: /删\s*除/ })).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: /新建优惠码/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('优惠码'), { target: { value: 'spring20' } })
    fireEvent.change(within(dialog).getByLabelText('减免比例（%）'), { target: { value: '20' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /创\s*建/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')?.body).toMatchObject({
      code: 'SPRING20', kind: 'percent', value: 20, allPlans: true, planIds: [], forTrafficPack: false, minAmount: 0, maxUses: 0, maxUsesPerUser: 1,
      startsAt: null, endsAt: null, isDisabled: false,
    })
  })
})

describe('TopupCodesPage', () => {
  const unused = {
    id: 2, codeTail: 'XYZ2', amount: 1000, batch: '双十一', status: 'unused', expiresAt: null, isDisabled: false, usedByUserId: null, usedByUsername: '',
    usedAt: null, createdAt: NOW,
  }
  const usedCode = { ...unused, id: 1, codeTail: '6789', status: 'used', usedByUserId: 9, usedByUsername: 'alice', usedAt: NOW }

  it('列表只有末 4 位；生成后一次性展示整批明文；已使用的不能停用或删除', async () => {
    const calls = mockServer({
      'GET /api/admin/topup-codes': () => ok({ items: [unused, usedCode], total: 2 }),
      'POST /api/admin/topup-codes': () => ok({ amount: 1000, codes: generated }),
    })
    renderWith(FINANCE_ALL, <TopupCodesPage />)
    const usedRow = (await screen.findByText('alice')).closest('tr') as HTMLElement
    expect(within(usedRow).getByText('···· 6789')).toBeDefined()
    expect(within(usedRow).queryByRole('button', { name: /停\s*用/ })).toBeNull()
    expect(within(usedRow).queryByRole('button', { name: /删\s*除/ })).toBeNull()
    expect(screen.queryByText(/ABCD EFGH/)).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: /生成充值码/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('面值'), { target: { value: '10' } })
    fireEvent.change(within(dialog).getByLabelText('数量'), { target: { value: '2' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /^生\s*成$/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')?.body).toEqual({ amount: 1000, count: 2, expiresAt: null, batch: '' })

    const shown = (await screen.findByLabelText('生成的充值码')) as HTMLTextAreaElement
    expect(shown.value).toBe('ABCD EFGH 2345 6789\nJKMN PQRS TUVW XYZ2')
    expect(screen.getByText(/已生成 2 个充值码（面值 ¥10\.00）/)).toBeDefined()
  })

  it('查看明文要单独请求；只有 finance:read 时没有生成与操作', async () => {
    const calls = mockServer({
      'GET /api/admin/topup-codes': () => ok({ items: [unused], total: 1 }),
      'GET /api/admin/topup-codes/2/code': () => ok({ code: generated[1] }),
    })
    const view = renderWith(FINANCE_ALL, <TopupCodesPage />)
    fireEvent.click(await screen.findByRole('button', { name: /查\s*看/ }))
    expect(await screen.findByText('JKMN PQRS TUVW XYZ2')).toBeDefined()
    expect(calls.filter((c) => c.path.endsWith('/code'))).toHaveLength(1)

    view.unmount()
    renderWith([PERMISSIONS.FINANCE_READ], <TopupCodesPage />)
    await screen.findByText('···· XYZ2')
    expect(screen.queryByRole('button', { name: /生成充值码/ })).toBeNull()
    expect(screen.queryByText('操作')).toBeNull()
  })
})

describe('记录页与统计', () => {
  it('订单与流水按币种显示金额；用户已删除时只显示用户名快照', async () => {
    const order = {
      id: 1, orderNo: 'O20261113000000123456', kind: 'plan', itemName: '标准版', durationDays: 30, trafficBytes: GIB, isTrial: false, amountOriginal: 990,
      amountDiscount: 99, amountPaid: 891, couponCode: 'WELCOME10', createdAt: NOW, userId: null, username: 'gone',
    }
    const entry = { id: 1, kind: 'admin_adjust', amount: -500, balanceAfter: 1500, remark: '扣回', createdAt: NOW, userId: 9, username: 'alice', adminUsername: 'root' }
    mockServer({
      'GET /api/admin/orders': () => ok({ items: [order], total: 1 }),
      'GET /api/admin/wallet-ledger': () => ok({ items: [entry], total: 1 }),
      'GET /api/admin/settings': () => ok({ shop: { enabled: true, currency: 'USD' } }),
    })
    const view = renderWith([PERMISSIONS.FINANCE_READ, PERMISSIONS.SETTING_READ], <OrdersPage />)
    expect(await screen.findByText('gone（已删除）')).toBeDefined()
    expect(await screen.findByText('US$8.91')).toBeDefined()
    expect(screen.getByText('US$0.99（WELCOME10）')).toBeDefined()

    view.unmount()
    renderWith([PERMISSIONS.FINANCE_READ], <LedgerPage />)
    expect(await screen.findByText('alice #9')).toBeDefined()
    expect(screen.getByText('-¥5.00')).toBeDefined()
    expect(screen.getByText('管理员调整')).toBeDefined()
    expect(screen.getByText('root')).toBeDefined()
  })

  it('收支统计显示区间合计，日期新的在前', async () => {
    const stats = {
      days: [
        { date: '2026-11-12', topup: 2000, spend: 0, rebate: 0, orderCount: 0 },
        { date: '2026-11-13', topup: 0, spend: 990, rebate: 99, orderCount: 1 },
      ],
      totals: { balance: 1109, topup: 2000, spend: 990, rebate: 99 },
    }
    mockServer({ 'GET /api/admin/finance/stats': () => ok(stats) })
    renderWith([PERMISSIONS.FINANCE_READ], <FinanceStatsPage />)
    expect(await screen.findByText('¥11.09')).toBeDefined()
    const dates = screen.getAllByText(/^2026-11-1\d$/).map((el) => el.textContent)
    expect(dates).toEqual(['2026-11-13', '2026-11-12'])
  })
})

describe('UsersPage 的余额与切换身份', () => {
  const user = {
    id: 9, username: 'alice', remark: '', planId: null, planName: '', status: 'active', isDisabled: false, trafficLimit: 0, trafficUsed: 0, expiresAt: null,
    pendingDays: 0, limitIp: 0, renewEveryDays: 0, renewMonthDay: 0, renewWeekday: 0, renewMax: 0, renewCount: 0, trafficReset: 'never', trafficResetDay: 1,
    provisions: { synced: 0, pending: 0, failed: 0 }, createdAt: NOW, balance: 2000, autoRenew: false,
  }

  it('调整余额按分提交并带备注；切换身份在新窗口打开带票据片段的门户地址', async () => {
    const calls = mockServer({
      'GET /api/admin/users': () => ok({ items: [user], total: 1 }),
      'POST /api/admin/users/9/balance': () => ok({ ...user, balance: 1500 }),
      'POST /api/admin/users/9/impersonate': () => ok({ ticket: 'ticket-value-for-test', expiresAt: NOW, portalUrl: 'https://user.example.com' }),
    })
    const opened = vi.fn()
    vi.stubGlobal('open', opened)
    renderWith([PERMISSIONS.USER_READ, PERMISSIONS.FINANCE_WRITE, PERMISSIONS.IMPERSONATE_WRITE], <UsersPage />)
    expect(await screen.findByText('¥20.00')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: /调整余额/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('当前余额：¥20.00')).toBeDefined()
    fireEvent.change(within(dialog).getByLabelText('调整金额'), { target: { value: '-5' } })
    fireEvent.change(within(dialog).getByLabelText('备注'), { target: { value: '扣回多发的' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /确认调整/ }))
    await waitFor(() => expect(calls.some((c) => c.path === '/api/admin/users/9/balance')).toBe(true))
    const post = calls.find((c) => c.path === '/api/admin/users/9/balance')
    expect(post?.csrf).toBe(TEST_CSRF)
    expect(post?.body).toEqual({ amount: -500, remark: '扣回多发的' })

    fireEvent.click(screen.getByRole('button', { name: /切换身份/ }))
    expect(calls.some((c) => c.path.endsWith('/impersonate'))).toBe(false)
    fireEvent.click(await screen.findByRole('button', { name: /^(OK|确\s*定)$/ }))
    await waitFor(() => expect(opened).toHaveBeenCalledTimes(1))
    expect(opened.mock.calls[0]?.[0]).toBe('https://user.example.com/impersonate#ticket=ticket-value-for-test')
    expect(opened.mock.calls[0]?.[2]).toContain('noopener')
  })

  it('没有 finance:write 与 impersonate:write 时不出现这两个入口', async () => {
    mockServer({ 'GET /api/admin/users': () => ok({ items: [user], total: 1 }) })
    renderWith([PERMISSIONS.USER_READ, PERMISSIONS.USER_WRITE], <UsersPage />)
    await screen.findByText('alice')
    expect(screen.queryByRole('button', { name: /调整余额/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /切换身份/ })).toBeNull()
  })
})
