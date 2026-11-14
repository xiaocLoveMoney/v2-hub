import { cleanup, fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TEST_CSRF, mockServer, ok, renderApp } from './test/harness'

const site = {
  siteName: '测试站', menuOrder: [], registerMode: 'invite', emailVerifyRequired: false, mailEnabled: false, checkinEnabled: false, ticketEnabled: false,
  sharedEnabled: false, shopEnabled: false, currency: 'CNY',
}
const session = {
  user: { id: 7, username: 'alice', status: 'active', email: null, isEmailVerified: false }, csrfToken: TEST_CSRF, totpEnabled: false, impersonated: false,
  features: { clientChain: false, customNode: false, serverChain: false },
}
const subscription = (hasCustomPassword: boolean) => ({
  url: 'http://localhost/abcdefghij12/tok', formats: [], updateIntervalHours: 12, hasCustomPassword,
})
const VALID = 'my-Pass_word.1'

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('订阅记录页：连接密码（Ruling-58）', () => {
  it('写法不对或两次不一致时不能保存；保存提交新密码；已自设时可恢复随机', async () => {
    let hasCustom = false
    const calls = mockServer({
      'GET /api/user/site': () => ok(site),
      'GET /api/user/auth/me': () => ok(session),
      'GET /api/user/subscribe-logs': () => ok({ items: [], total: 0 }),
      'GET /api/user/subscription': () => ok(subscription(hasCustom)),
      'PUT /api/user/subscription/password': () => {
        hasCustom = !hasCustom
        return ok(subscription(hasCustom))
      },
    })
    renderApp('/subscribe-logs')
    expect(await screen.findByText('平台随机生成')).toBeDefined()
    expect(screen.queryByRole('button', { name: '恢复随机密码' })).toBeNull()
    const save = screen.getByRole('button', { name: /保\s*存/ })
    fireEvent.change(screen.getByLabelText('新的连接密码'), { target: { value: 'bad pass' } })
    expect(screen.getAllByText('8–64 位，只能用字母、数字与 - _ . ~').length).toBeGreaterThan(0)
    expect(save).toHaveProperty('disabled', true)
    fireEvent.change(screen.getByLabelText('新的连接密码'), { target: { value: VALID } })
    fireEvent.change(screen.getByLabelText('再输一次'), { target: { value: 'other-value' } })
    expect(screen.getByText('两次输入不一致')).toBeDefined()
    expect(save).toHaveProperty('disabled', true)
    fireEvent.change(screen.getByLabelText('再输一次'), { target: { value: VALID } })
    fireEvent.click(save)
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    const put = calls.find((c) => c.method === 'PUT')
    expect(put?.body).toEqual({ password: VALID })
    expect(put?.csrf).toBe(TEST_CSRF)
    expect(await screen.findByText('已自设')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: '恢复随机密码' }))
    await waitFor(() => expect(calls.filter((c) => c.method === 'PUT')).toHaveLength(2))
    expect(calls.filter((c) => c.method === 'PUT')[1]?.body).toEqual({ password: '' })
    expect(await screen.findByText('平台随机生成')).toBeDefined()
  })
})
