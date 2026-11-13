import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { describeError } from './i18n/errors'
import { LOCALE_STORAGE_KEY, detectLocale, initialLocale } from './i18n/locale'
import { en } from './i18n/messages/en'
import { zh } from './i18n/messages/zh'
import { fail, mockServer, ok, renderApp } from './test/harness'

const GIB = 1024 ** 3
/** 表单里输入的口令（仅测试用）。 */
const typed = 'correct-horse-battery'
const PLACEHOLDER = /\{(\w+)\}/g
const SUPPORT_URL = 'https://chat.example.com/room'

const site = {
  siteName: '测试站', registerMode: 'open', emailVerifyRequired: false, mailEnabled: false, checkinEnabled: false, ticketEnabled: false, sharedEnabled: false,
  shopEnabled: true, currency: 'CNY',
  landing: { enabled: true, headline: '快速稳定的线路', intro: '## 多地区节点\n\n[了解更多](https://example.com/about)' },
  support: { url: SUPPORT_URL, label: '联系我们' },
}
const plans = [
  {
    id: 1, name: '基础版', description: '解锁流媒体', trafficLimit: 100 * GIB, trafficReset: 'monthly', limitIp: 3, nodeCount: 5,
    prices: [{ id: 11, durationDays: 30, price: 1500 }, { id: 12, durationDays: 365, price: 15000 }],
  },
]
const anonymous = () => fail(401, 'unauthenticated', '请先登录')

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  globalThis.localStorage.clear()
})

describe('落地页与客服入口', () => {
  it('未登录访问根地址且开启时显示落地页：标题、介绍、公开套餐、注册入口与客服按钮', async () => {
    mockServer({
      'GET /api/user/site': () => ok(site),
      'GET /api/user/auth/me': anonymous,
      'GET /api/user/site/plans': () => ok(plans),
    })
    renderApp('/')
    expect(await screen.findByRole('heading', { name: '快速稳定的线路' })).toBeDefined()
    expect(screen.getByRole('heading', { name: '多地区节点' })).toBeDefined()
    const about = screen.getByRole('link', { name: '了解更多' })
    expect(about.getAttribute('rel')).toBe('noopener noreferrer')

    const card = (await screen.findByRole('heading', { name: '基础版' })).closest('section') as HTMLElement
    expect(within(card).getByText('¥15.00')).toBeDefined()
    expect(within(card).getByText('每月 100.00 GB')).toBeDefined()
    expect(within(card).getByText('最多 3 台设备同时在线')).toBeDefined()
    expect(within(card).getByText('解锁流媒体')).toBeDefined()
    fireEvent.click(within(card).getByRole('radio', { name: '1 年' }))
    expect(within(card).getByText('¥150.00')).toBeDefined()
    expect(within(card).getByRole('link', { name: '购买' }).getAttribute('href')).toBe('/register')

    const support = screen.getByRole('link', { name: '联系我们' })
    expect(support.getAttribute('href')).toBe(SUPPORT_URL)
    expect(support.getAttribute('target')).toBe('_blank')
    expect(support.getAttribute('rel')).toBe('noopener noreferrer')
  })

  it('落地页关闭时根地址照旧去登录页；客服地址不是 https 的不显示', async () => {
    mockServer({
      'GET /api/user/site': () => ok({ ...site, landing: { enabled: false, headline: '', intro: '' }, support: { url: 'http://chat.example.com', label: '联系我们' } }),
      'GET /api/user/auth/me': anonymous,
    })
    renderApp('/')
    expect(await screen.findByRole('button', { name: '登录' })).toBeDefined()
    expect(screen.queryByRole('link', { name: '联系我们' })).toBeNull()
  })
})

describe('中英双语', () => {
  it('按浏览器语言选默认，记住的选择优先', () => {
    expect(detectLocale('zh-CN')).toBe('zh')
    expect(detectLocale('zh-TW')).toBe('zh')
    expect(detectLocale('en-US')).toBe('en')
    expect(detectLocale('ja')).toBe('en')
    expect(detectLocale(undefined)).toBe('en')
    globalThis.localStorage.setItem(LOCALE_STORAGE_KEY, 'zh')
    expect(initialLocale()).toBe('zh')
    globalThis.localStorage.setItem(LOCALE_STORAGE_KEY, 'fr')
    expect(initialLocale()).toBe(detectLocale(globalThis.navigator.language))
  })

  it('英文词条的占位符与中文一致', () => {
    const names = (text: string) => [...text.matchAll(PLACEHOLDER)].map((m) => m[1]).sort()
    for (const key of Object.keys(zh) as (keyof typeof zh)[]) {
      expect(names(en[key]), key).toEqual(names(zh[key]))
    }
  })

  it('英文界面显示英文；页脚切到中文后界面跟着变并记住选择', async () => {
    mockServer({ 'GET /api/user/site': () => ok(site), 'GET /api/user/auth/me': anonymous })
    renderApp('/login', 'en')
    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeDefined()
    expect(screen.getByLabelText('Username')).toBeDefined()
    expect(document.documentElement.lang).toBe('en-US')

    fireEvent.click(screen.getByRole('button', { name: '中文' }))
    expect(await screen.findByRole('button', { name: '登录' })).toBeDefined()
    expect(globalThis.localStorage.getItem(LOCALE_STORAGE_KEY)).toBe('zh')
    expect(document.documentElement.lang).toBe('zh-CN')
  })

  it('英文界面的错误按错误码给通用说明，后端原文附在括号里', async () => {
    mockServer({
      'GET /api/user/site': () => ok(site),
      'GET /api/user/auth/me': anonymous,
      'POST /api/user/auth/login': () => fail(400, 'invalid_argument', '用户名或口令错误'),
    })
    renderApp('/login', 'en')
    fireEvent.change(await screen.findByLabelText('Username'), { target: { value: 'alice' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: typed } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    await waitFor(() => expect(screen.getByText('Invalid input (用户名或口令错误)')).toBeDefined())
  })

  it('错误说明：中文原样，英文未知错误码用通用说明，网络错误不重复附中文', () => {
    expect(describeError('zh', { code: 'conflict', message: '用户名已被占用' })).toBe('用户名已被占用')
    expect(describeError('en', { code: 'something_new', message: '新错误' })).toBe('Request failed (新错误)')
    expect(describeError('en', { code: 'network', message: '网络异常，请稍后重试' })).toBe('Network error. Please try again later.')
  })
})
