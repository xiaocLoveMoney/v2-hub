import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { formatMoney, parseAmountToCents } from './money'
import { durationLabel, trafficLabel } from './pages/shop/labels'
import { TEST_CSRF, enT, fail, mockServer, ok, renderApp, zhT } from './test/harness'

const NOW = '2026-11-13T00:00:00Z'
const GIB = 1024 ** 3
const site = {
  siteName: '测试站', menuOrder: [], registerMode: 'invite', emailVerifyRequired: false, mailEnabled: false, checkinEnabled: false, ticketEnabled: false, sharedEnabled: false,
  shopEnabled: true, currency: 'CNY',
}
const session = { user: { id: 7, username: 'alice', status: 'active', email: null, isEmailVerified: false }, csrfToken: TEST_CSRF, totpEnabled: false, impersonated: false }
const dashboard = {
  status: 'active', planName: '标准版', trafficLimit: 10 * GIB, trafficUsed: GIB, trafficToday: 0, expiresAt: '2099-01-01T00:00:00Z', pendingDays: 0, limitIp: 3,
  onlineDevices: 1, lastUsedAt: null, nodeCount: 2, announcement: null, popupAnnouncement: null, balance: 2000,
}
const standard = {
  id: 1, name: '标准版', description: '解锁流媒体\n原生 IP', trafficLimit: 100 * GIB, trafficReset: 'monthly', limitIp: 3, nodeCount: 8, isTrial: false, isCurrent: true,
  canBuy: true, reason: '', prices: [{ id: 11, durationDays: 30, price: 990 }, { id: 12, durationDays: 365, price: 9900 }],
}
const trial = {
  id: 2, name: '试用', description: '', trafficLimit: GIB, trafficReset: 'never', limitIp: 1, nodeCount: 2, isTrial: true, isCurrent: false, canBuy: false,
  reason: '试用套餐每个账号只能领取一次', prices: [{ id: 21, durationDays: 3, price: 0 }],
}
const shop = {
  balance: 2000, plans: [standard, trial], trafficPacks: [{ id: 5, name: '加油包', trafficBytes: 10 * GIB, price: 300 }], canBuyTrafficPack: true, trafficPackReason: '',
}
const quote = {
  amountOriginal: 990, amountDiscount: 0, amountPayable: 990, balance: 2000, shortfall: 0, isRenewal: true, expiresAt: '2099-02-01T00:00:00Z', trafficLimit: 100 * GIB, couponCode: '',
}
const wallet = {
  balance: 2000, rebateTotal: 99, gateways: [],
  autoRenew: { enabled: false, available: true, reason: '', planName: '标准版', durationDays: 30, price: 990 },
}
/** 已登录用户进各页面时会发的请求。 */
const signedIn = {
  'GET /api/user/site': () => ok(site),
  'GET /api/user/auth/me': () => ok(session),
  'GET /api/user/dashboard': () => ok(dashboard),
  'GET /api/user/traffic': () => ok([]),
  'GET /api/user/subscription': () => fail(404, 'not_found', '不存在'),
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('金额与文案', () => {
  it('金额只做格式化与元转分；时长与流量按规则取说法', () => {
    expect(formatMoney(990)).toBe('¥9.90')
    expect(parseAmountToCents('19.9')).toBe(1990)
    expect(parseAmountToCents('0')).toBeNull()
    expect(parseAmountToCents('abc')).toBeNull()
    expect([30, 90, 365, 45].map((d) => durationLabel(zhT, d))).toEqual(['1 个月', '3 个月', '1 年', '45 天'])
    expect([30, 90, 365, 730, 1].map((d) => durationLabel(enT, d))).toEqual(['1 month', '3 months', '1 year', '2 years', '1 day'])
    expect(trafficLabel(zhT, 100 * GIB, 'monthly')).toBe('每月 100.00 GB')
    expect(trafficLabel(zhT, GIB, 'never')).toBe('有效期内共 1.00 GB')
    expect(trafficLabel(zhT, 0, 'monthly')).toBe('不限流量')
    expect(trafficLabel(enT, 100 * GIB, 'monthly')).toBe('100.00 GB per month')
    expect(formatMoney(990, 'USD', 'en-US')).toBe('$9.90')
  })
})

describe('商店', () => {
  it('价格卡按所选时长显示价格；结算框逐行列出账单，应用优惠码后重新询价，确认后下单', async () => {
    const discounted = { ...quote, amountDiscount: 99, amountPayable: 891, couponCode: 'WELCOME10' }
    let quotes = 0
    const calls = mockServer({
      ...signedIn,
      'GET /api/user/shop': () => ok(shop),
      'POST /api/user/shop/quote': () => {
        quotes += 1
        return ok(quotes === 1 ? quote : discounted)
      },
      'POST /api/user/orders': () => ok({ id: 1, orderNo: 'O1', kind: 'plan', itemName: '标准版', durationDays: 30, trafficBytes: 100 * GIB, isTrial: false,
        amountOriginal: 990, amountDiscount: 99, amountPaid: 891, couponCode: 'WELCOME10', createdAt: NOW }),
    })
    renderApp('/shop')
    const card = (await screen.findByText('当前套餐')).closest('section') as HTMLElement
    expect(within(card).getByText('¥9.90')).toBeDefined()
    expect(within(card).getByText('每月 100.00 GB')).toBeDefined()
    expect(within(card).getByText('原生 IP')).toBeDefined()
    fireEvent.click(within(card).getByRole('radio', { name: '1 年' }))
    expect(within(card).getByText('¥99.00')).toBeDefined()
    fireEvent.click(within(card).getByRole('radio', { name: '1 个月' }))

    // 试用已领过：按钮不可用并说明原因
    const trialCard = screen.getByText('试用套餐每个账号只能领取一次').closest('section') as HTMLElement
    expect(within(trialCard).getByRole('button', { name: '免费试用' })).toHaveProperty('disabled', true)

    fireEvent.click(within(card).getByRole('button', { name: '续费' }))
    const dialog = await screen.findByRole('dialog')
    await waitFor(() => expect(calls.some((c) => c.path === '/api/user/shop/quote')).toBe(true))
    expect(calls.find((c) => c.path === '/api/user/shop/quote')?.body).toEqual({ kind: 'plan', planPriceId: 11 })
    expect(await within(dialog).findByText(/续费：从当前到期日顺延/)).toBeDefined()

    fireEvent.change(within(dialog).getByLabelText('优惠码'), { target: { value: 'welcome10' } })
    fireEvent.click(within(dialog).getByRole('button', { name: '使用' }))
    expect(await within(dialog).findByText('¥8.91')).toBeDefined()
    expect(within(dialog).getByText('优惠（WELCOME10）')).toBeDefined()
    expect(calls.filter((c) => c.path === '/api/user/shop/quote')[1]?.body).toEqual({ kind: 'plan', planPriceId: 11, couponCode: 'welcome10' })

    fireEvent.click(within(dialog).getByRole('button', { name: '确认支付' }))
    await waitFor(() => expect(calls.some((c) => c.path === '/api/user/orders' && c.method === 'POST')).toBe(true))
    const order = calls.find((c) => c.path === '/api/user/orders')
    expect(order?.csrf).toBe(TEST_CSRF)
    expect(order?.body).toEqual({ kind: 'plan', planPriceId: 11, couponCode: 'welcome10' })
    expect(await screen.findByText('购买成功：标准版')).toBeDefined()
    await waitFor(() => expect(calls.filter((c) => c.path === '/api/user/shop')).toHaveLength(2))
  })

  it('余额不足时不给确认按钮，改为去充值；流量包说明不保留', async () => {
    const short = { ...quote, amountOriginal: 300, amountPayable: 300, balance: 100, shortfall: 200, isRenewal: false, trafficLimit: 110 * GIB }
    const calls = mockServer({ ...signedIn, 'GET /api/user/shop': () => ok({ ...shop, balance: 100 }), 'POST /api/user/shop/quote': () => ok(short) })
    renderApp('/shop')
    const pack = (await screen.findByText('加油包')).closest('section') as HTMLElement
    fireEvent.click(within(pack).getByRole('button', { name: '购买' }))
    const dialog = await screen.findByRole('dialog')
    expect(await within(dialog).findByText('还需充值')).toBeDefined()
    expect(within(dialog).getByText('¥2.00')).toBeDefined()
    expect(within(dialog).getByText(/流量包只增加流量额度/)).toBeDefined()
    expect(within(dialog).queryByRole('button', { name: '确认支付' })).toBeNull()
    expect(within(dialog).getByRole('link', { name: '去充值' }).getAttribute('href')).toBe('/wallet')
    expect(calls.find((c) => c.path === '/api/user/shop/quote')?.body).toEqual({ kind: 'traffic_pack', trafficPackId: 5 })
    expect(calls.some((c) => c.path === '/api/user/orders')).toBe(false)
  })

  it('商店关闭时菜单里没有商店与钱包，仪表盘第四张卡还是可用节点', async () => {
    mockServer({ ...signedIn, 'GET /api/user/site': () => ok({ ...site, shopEnabled: false }) })
    renderApp('/')
    const menu = await screen.findByRole('navigation', { name: '主菜单' })
    expect(await screen.findByText('可用节点')).toBeDefined()
    expect(within(menu).queryByText('商店')).toBeNull()
    expect(within(menu).queryByText('我的钱包')).toBeNull()
    expect(screen.queryByText('余额')).toBeNull()
  })
})

describe('钱包', () => {
  it('显示余额与返利；兑换充值码、开自动续费都带 CSRF 头；没有网关时不出现在线充值', async () => {
    let autoRenew = false
    const calls = mockServer({
      ...signedIn,
      'GET /api/user/wallet': () => ok({ ...wallet, autoRenew: { ...wallet.autoRenew, enabled: autoRenew } }),
      'GET /api/user/wallet/ledger': () => ok({ items: [{ id: 1, kind: 'purchase', amount: -990, balanceAfter: 2000, remark: '标准版', createdAt: NOW }], total: 1 }),
      'GET /api/user/orders': () => ok({ items: [], total: 0 }),
      'POST /api/user/wallet/redeem': () => ok({ amount: 1000, balance: 3000 }),
      'PUT /api/user/wallet/auto-renew': () => {
        autoRenew = true
        return ok({ ...wallet, autoRenew: { ...wallet.autoRenew, enabled: true } })
      },
    })
    renderApp('/wallet')
    expect(await screen.findByText('¥20.00')).toBeDefined()
    expect(screen.getByText(/累计获得邀请返利 ¥0\.99/)).toBeDefined()
    expect(await screen.findByText('-¥9.90')).toBeDefined()
    expect(screen.queryByText('在线充值')).toBeNull()

    const redeem = screen.getByRole('button', { name: '兑换' }) as HTMLButtonElement
    expect(redeem.disabled).toBe(true)
    fireEvent.change(screen.getByLabelText('充值码'), { target: { value: 'ABCD EFGH 2345 6789' } })
    fireEvent.click(redeem)
    await waitFor(() => expect(calls.some((c) => c.path === '/api/user/wallet/redeem')).toBe(true))
    const post = calls.find((c) => c.path === '/api/user/wallet/redeem')
    expect(post?.csrf).toBe(TEST_CSRF)
    expect(post?.body).toEqual({ code: 'ABCD EFGH 2345 6789' })
    expect(await screen.findByText('充值成功，到账 ¥10.00')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: '开启自动续费' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({ enabled: true })
    expect(await screen.findByRole('button', { name: '关闭自动续费' })).toBeDefined()
  })

  it('仪表盘第四张卡在商店开启时是余额', async () => {
    mockServer(signedIn)
    renderApp('/')
    expect(await screen.findByText('余额')).toBeDefined()
    expect(screen.getByText('¥20.00')).toBeDefined()
    expect(screen.queryByText('可用节点')).toBeNull()
  })
})

describe('切换为用户身份', () => {
  it('从地址片段取票据换会话，进入后顶部一直有只读提示，可以退出', async () => {
    const ticket = 'ticket-value-for-test-0123456789abcdef'
    const calls = mockServer({
      ...signedIn,
      'GET /api/user/auth/me': () => fail(401, 'unauthenticated', '请先登录'),
      'POST /api/user/auth/impersonate': () => ok({ ...session, impersonated: true }),
      'POST /api/user/auth/logout': () => ok(null),
    })
    renderApp(`/impersonate#ticket=${ticket}`)
    expect(await screen.findByText(/的身份查看（只读/)).toBeDefined()
    expect(calls.find((c) => c.path === '/api/user/auth/impersonate')?.body).toEqual({ ticket })
    expect(await screen.findByText('套餐剩余')).toBeDefined()

    const bar = screen.getByText(/的身份查看（只读/).closest('div') as HTMLElement
    fireEvent.click(within(bar).getByRole('button', { name: '退出' }))
    await waitFor(() => expect(calls.some((c) => c.path === '/api/user/auth/logout')).toBe(true))
    expect(await screen.findByRole('button', { name: '登录' })).toBeDefined()
  })

  it('没有票据或票据无效时给出说明，不进入后台', async () => {
    const calls = mockServer({
      'GET /api/user/site': () => ok(site),
      'GET /api/user/auth/me': () => fail(401, 'unauthenticated', '请先登录'),
      'POST /api/user/auth/impersonate': () => fail(400, 'invalid_argument', '票据无效或已过期'),
    })
    const view = renderApp('/impersonate')
    expect(await screen.findByText(/链接里没有票据/)).toBeDefined()
    expect(calls.some((c) => c.path === '/api/user/auth/impersonate')).toBe(false)

    view.unmount()
    renderApp('/impersonate#ticket=expired-ticket-0123456789abcdef0123')
    expect(await screen.findByText('票据无效或已过期')).toBeDefined()
    expect(screen.queryByText('套餐剩余')).toBeNull()
  })
})
