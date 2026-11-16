import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MIN_PAGE_RETURN_INTERVAL_MS } from './hooks/use-page-return'
import { TEST_CSRF, fail, mockServer, ok, renderApp, type Recorded } from './test/harness'

const NOW = '2026-11-15T00:00:00Z'
const GIB = 1024 ** 3
const ME_PATH = '/api/user/auth/me'
const SHOP_PATH = '/api/user/shop'
const CHAIN_MENU = '链式代理'
/** 等失败应答被处理完的时间：mock 的 fetch 立即返回，留出几轮事件循环即可。 */
const SETTLE_MS = 30
const site = {
  siteName: '测试站', menuOrder: [], registerMode: 'invite', emailVerifyRequired: false, mailEnabled: false, checkinEnabled: false, ticketEnabled: false, sharedEnabled: false,
  shopEnabled: true, currency: 'CNY',
}
const NO_CHAIN = { clientChain: false, customNode: false, serverChain: false }
const WITH_CHAIN = { clientChain: true, customNode: true, serverChain: true }
const sessionOf = (features: typeof NO_CHAIN) => ({
  user: { id: 7, username: 'alice', status: 'active', email: null, isEmailVerified: false }, csrfToken: TEST_CSRF, totpEnabled: false, impersonated: false, features,
})
const planOf = (id: number, name: string, isCurrent: boolean) => ({
  id, name, description: '', trafficLimit: 100 * GIB, trafficReset: 'monthly', limitIp: 3, nodeCount: 8, isTrial: false, isCurrent, canBuy: true, reason: '',
  prices: [{ id: id * 10, durationDays: 30, price: 990 }],
})
/** 商店数据：current 指定哪个套餐是当前套餐。 */
const shopOf = (current: 'basic' | 'chain') => ({
  balance: 2000, plans: [planOf(1, '基础版', current === 'basic'), planOf(2, '链式版', current === 'chain')], trafficPacks: [], canBuyTrafficPack: false, trafficPackReason: '',
})
const quote = {
  amountOriginal: 990, amountDiscount: 0, amountPayable: 990, balance: 2000, shortfall: 0, isRenewal: false, expiresAt: '2099-02-01T00:00:00Z', trafficLimit: 100 * GIB, couponCode: '',
}
const order = {
  id: 1, orderNo: 'O1', kind: 'plan', itemName: '链式版', durationDays: 30, trafficBytes: 100 * GIB, isTrial: false, amountOriginal: 990, amountDiscount: 0, amountPaid: 990,
  couponCode: '', createdAt: NOW,
}

const countOf = (calls: Recorded[], path: string) => calls.filter((c) => c.method === 'GET' && c.path === path).length
/** 当前套餐那张价格卡的套餐名。 */
const currentPlanName = () => within(screen.getByText('当前套餐').closest('section') as HTMLElement).getByRole('heading').textContent
/** 模拟用户切回本页：窗口重新获得焦点。 */
const returnToPage = () => fireEvent(window, new Event('focus'))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('套餐变了页面自己跟上（docs/06 §3 第 6 条）', () => {
  it('在商店买了带链式的套餐：下单成功后重取会话，菜单立刻出现“链式代理”', async () => {
    let hasBought = false
    const calls = mockServer({
      'GET /api/user/site': () => ok(site),
      [`GET ${ME_PATH}`]: () => ok(sessionOf(hasBought ? WITH_CHAIN : NO_CHAIN)),
      [`GET ${SHOP_PATH}`]: () => ok(shopOf(hasBought ? 'chain' : 'basic')),
      'POST /api/user/shop/quote': () => ok(quote),
      'POST /api/user/orders': () => {
        hasBought = true
        return ok(order)
      },
    })
    renderApp('/shop')
    const card = (await screen.findByText('链式版')).closest('section') as HTMLElement
    expect(screen.queryByRole('link', { name: CHAIN_MENU })).toBeNull()

    fireEvent.click(within(card).getByRole('button', { name: '购买' }))
    const dialog = await screen.findByRole('dialog')
    const pay = within(dialog).getByRole('button', { name: '确认支付' })
    await waitFor(() => expect(pay).toHaveProperty('disabled', false))
    fireEvent.click(pay)

    expect(await screen.findByRole('link', { name: CHAIN_MENU })).toBeDefined()
    expect(countOf(calls, ME_PATH)).toBe(2)
    await waitFor(() => expect(currentPlanName()).toBe('链式版'))
  })

  it('套餐在别处变了（管理员改）：切回页面时重取会话与当前页数据，短时间内重复切回不再发请求', async () => {
    let isUpgraded = false
    const calls = mockServer({
      'GET /api/user/site': () => ok(site),
      [`GET ${ME_PATH}`]: () => ok(sessionOf(isUpgraded ? WITH_CHAIN : NO_CHAIN)),
      [`GET ${SHOP_PATH}`]: () => ok(shopOf(isUpgraded ? 'chain' : 'basic')),
    })
    renderApp('/shop')
    await waitFor(() => expect(currentPlanName()).toBe('基础版'))
    expect(screen.queryByRole('link', { name: CHAIN_MENU })).toBeNull()

    isUpgraded = true
    returnToPage()
    expect(await screen.findByRole('link', { name: CHAIN_MENU })).toBeDefined()
    await waitFor(() => expect(currentPlanName()).toBe('链式版'))
    expect([countOf(calls, ME_PATH), countOf(calls, SHOP_PATH)]).toEqual([2, 2])

    returnToPage()
    document.dispatchEvent(new Event('visibilitychange'))
    expect([countOf(calls, ME_PATH), countOf(calls, SHOP_PATH)]).toEqual([2, 2])
  })

  it('切回页面时的重取失败不打扰：数据与登录状态保持；后端明确回 401 才回到登录页', async () => {
    let meStatus = 200
    let isShopDown = false
    const calls = mockServer({
      'GET /api/user/site': () => ok(site),
      [`GET ${ME_PATH}`]: () => (meStatus === 200 ? ok(sessionOf(WITH_CHAIN)) : fail(meStatus, meStatus === 401 ? 'unauthenticated' : 'internal', '出错了')),
      [`GET ${SHOP_PATH}`]: () => (isShopDown ? fail(500, 'internal', '商店出错了') : ok(shopOf('chain'))),
    })
    const clock = vi.spyOn(Date, 'now')
    let now = MIN_PAGE_RETURN_INTERVAL_MS
    clock.mockImplementation(() => now)
    renderApp('/shop')
    await waitFor(() => expect(currentPlanName()).toBe('链式版'))

    meStatus = 500
    isShopDown = true
    returnToPage()
    await waitFor(() => expect([countOf(calls, ME_PATH), countOf(calls, SHOP_PATH)]).toEqual([2, 2]))
    // 等后台请求的应答处理完：页面仍是原来的数据，没有错误提示，也没有被踢回登录页
    await new Promise((resolve) => setTimeout(resolve, SETTLE_MS))
    expect(screen.queryByText('商店出错了')).toBeNull()
    expect(currentPlanName()).toBe('链式版')
    expect(screen.getByRole('link', { name: CHAIN_MENU })).toBeDefined()

    meStatus = 401
    now += MIN_PAGE_RETURN_INTERVAL_MS
    returnToPage()
    expect(await screen.findByRole('button', { name: '登录' })).toBeDefined()
  })
})
