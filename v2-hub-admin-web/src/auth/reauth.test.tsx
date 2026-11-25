import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { App as AntApp, ConfigProvider } from 'antd'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, csrfHeader, registerReauthHandler, registerTotpSetupHandler, setCsrfToken, toApiError, type Session } from '../api/client'
import { TEST_CSRF, mockServer, ok, sessionWith } from '../test/harness'
import { ReauthProvider } from './ReauthProvider'
import { SessionContext } from './session-context'

const ADMIN_PATH = '/api/admin/admins/2'
const HOSTS_PATH = '/api/admin/server-hosts'
const REAUTH_PATH = '/api/admin/auth/reauth'
const ME_PATH = '/api/admin/auth/me'
/** 验证框里输入的密码（仅测试用）。 */
const typed = 'typed-in-dialog'
const reauthRequired = () => Response.json({ code: 'reauth_required', message: '请先验证身份' }, { status: 403 })
const reauthOk = () => ok({ reauthUntil: '2026-11-25T08:05:00Z' })

/** 第一次回 reauth_required，之后回 ok(data)。 */
function firstReauthThen(data: unknown): () => Response {
  let count = 0
  return () => {
    count += 1
    return count === 1 ? reauthRequired() : ok(data)
  }
}

function renderProvider(over: Partial<Session> = {}) {
  setCsrfToken(TEST_CSRF)
  return render(
    <ConfigProvider>
      <AntApp>
        <SessionContext.Provider value={sessionWith([], over)}>
          <ReauthProvider>
            <div>page</div>
          </ReauthProvider>
        </SessionContext.Provider>
      </AntApp>
    </ConfigProvider>,
  )
}

const updateAdmin = () =>
  api.PUT('/api/admin/admins/{id}', { params: { ...csrfHeader(), path: { id: 2 } }, body: { roles: ['operator'], isDisabled: false } })

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('二次验证拦截（Ruling-100）', () => {
  it('收到 reauth_required → 弹框 → 验证成功后原样重发（请求体与 CSRF 头一致），调用方拿到重发的结果', async () => {
    const calls = mockServer({
      [`PUT ${ADMIN_PATH}`]: firstReauthThen({ id: 2 }),
      [`POST ${REAUTH_PATH}`]: reauthOk,
    })
    renderProvider()
    const pending = updateAdmin()

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('验证身份')).toBeDefined()
    fireEvent.change(within(dialog).getByLabelText('当前密码'), { target: { value: typed } })
    fireEvent.click(within(dialog).getByRole('button', { name: /验证并继续/ }))

    const result = await pending
    expect(result.error).toBeUndefined()
    expect(result.data?.data).toEqual({ id: 2 })
    const puts = calls.filter((c) => c.method === 'PUT' && c.path === ADMIN_PATH)
    expect(puts).toHaveLength(2)
    expect(puts[1].body).toEqual(puts[0].body)
    expect(puts[1].body).toEqual({ roles: ['operator'], isDisabled: false })
    expect(puts[0].csrf).toBe(TEST_CSRF)
    expect(puts[1].csrf).toBe(TEST_CSRF)
    const reauth = calls.find((c) => c.path === REAUTH_PATH)
    expect(reauth?.body).toEqual({ password: typed })
    expect(reauth?.csrf).toBe(TEST_CSRF)
  })

  it('取消验证：不重发，原 403 原样交还调用方', async () => {
    const calls = mockServer({ [`PUT ${ADMIN_PATH}`]: firstReauthThen({ id: 2 }) })
    renderProvider()
    const pending = updateAdmin()
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: /取\s*消/ }))

    const result = await pending
    expect(toApiError(result.error).code).toBe('reauth_required')
    expect(calls.filter((c) => c.method === 'PUT')).toHaveLength(1)
    expect(calls.some((c) => c.path === REAUTH_PATH)).toBe(false)
  })

  it('已开启两步验证时输入 6 位验证码；验证失败（400）在框内提示并可重试，频控（429）提示稍后再试', async () => {
    let attempt = 0
    const calls = mockServer({
      [`PUT ${ADMIN_PATH}`]: firstReauthThen({ id: 2 }),
      [`POST ${REAUTH_PATH}`]: () => {
        attempt += 1
        if (attempt === 1) {
          return Response.json({ code: 'invalid_argument', message: '验证码错误' }, { status: 400 })
        }
        if (attempt === 2) {
          return Response.json({ code: 'rate_limited', message: 'locked for 900s' }, { status: 429 })
        }
        return reauthOk()
      },
    })
    renderProvider({ totpEnabled: true })
    const pending = updateAdmin()
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).queryByLabelText('当前密码')).toBeNull()

    const submitCode = (code: string) => {
      fireEvent.change(within(dialog).getByPlaceholderText('6 位验证码'), { target: { value: code } })
      fireEvent.click(within(dialog).getByRole('button', { name: /验证并继续/ }))
    }
    submitCode('111111')
    expect(await within(dialog).findByText('验证码错误')).toBeDefined()
    submitCode('222222')
    expect(await within(dialog).findByText('尝试次数过多，请稍后再试')).toBeDefined()
    submitCode('333333')

    const result = await pending
    expect(result.error).toBeUndefined()
    expect(calls.filter((c) => c.path === REAUTH_PATH).map((c) => c.body)).toEqual([
      { totpCode: '111111' },
      { totpCode: '222222' },
      { totpCode: '333333' },
    ])
    expect(calls.filter((c) => c.method === 'PUT')).toHaveLength(2)
  })

  it('并发的多个请求同时遇到 reauth_required：只弹一次框、只验证一次，GET 也一起重发', async () => {
    const calls = mockServer({
      [`PUT ${ADMIN_PATH}`]: firstReauthThen({ id: 2 }),
      [`GET ${HOSTS_PATH}`]: firstReauthThen([]),
      [`POST ${REAUTH_PATH}`]: reauthOk,
    })
    renderProvider()
    const first = updateAdmin()
    const second = api.GET('/api/admin/server-hosts')

    const dialog = await screen.findByRole('dialog')
    await waitFor(() => expect(calls.filter((c) => c.method === 'GET')).toHaveLength(1))
    expect(screen.getAllByRole('dialog')).toHaveLength(1)
    fireEvent.change(within(dialog).getByLabelText('当前密码'), { target: { value: typed } })
    fireEvent.click(within(dialog).getByRole('button', { name: /验证并继续/ }))

    const [a, b] = await Promise.all([first, second])
    expect(a.data?.data).toEqual({ id: 2 })
    expect(b.data?.data).toEqual([])
    expect(calls.filter((c) => c.path === REAUTH_PATH)).toHaveLength(1)
    expect(calls.filter((c) => c.method === 'GET' && c.path === HOSTS_PATH)).toHaveLength(2)
  })
})

describe('api 客户端的拦截边界', () => {
  it('GET 遇到 reauth_required 时也在验证后重发', async () => {
    const calls = mockServer({ [`GET ${HOSTS_PATH}`]: firstReauthThen([]) })
    const handler = vi.fn(async () => true)
    const unregister = registerReauthHandler(handler)
    try {
      const result = await api.GET('/api/admin/server-hosts')
      expect(result.data?.data).toEqual([])
      expect(handler).toHaveBeenCalledTimes(1)
      expect(calls.filter((c) => c.method === 'GET')).toHaveLength(2)
    } finally {
      unregister()
    }
  })

  it('/api/admin/auth/ 下的请求不拦截', async () => {
    const calls = mockServer({ [`GET ${ME_PATH}`]: reauthRequired })
    const handler = vi.fn(async () => true)
    const unregister = registerReauthHandler(handler)
    try {
      const result = await api.GET('/api/admin/auth/me')
      expect(toApiError(result.error).code).toBe('reauth_required')
      expect(handler).not.toHaveBeenCalled()
      expect(calls).toHaveLength(1)
    } finally {
      unregister()
    }
  })

  it('重发后再次 reauth_required：直接交还调用方，不再弹框、不循环', async () => {
    const calls = mockServer({ [`PUT ${ADMIN_PATH}`]: reauthRequired })
    const handler = vi.fn(async () => true)
    const unregister = registerReauthHandler(handler)
    try {
      const result = await updateAdmin()
      expect(toApiError(result.error).code).toBe('reauth_required')
      expect(handler).toHaveBeenCalledTimes(1)
      expect(calls.filter((c) => c.method === 'PUT')).toHaveLength(2)
    } finally {
      unregister()
    }
  })

  it('没有登记处理器、或是普通的 permission_denied 时原样返回', async () => {
    const calls = mockServer({
      [`PUT ${ADMIN_PATH}`]: reauthRequired,
      [`GET ${HOSTS_PATH}`]: () => Response.json({ code: 'permission_denied', message: '无权限' }, { status: 403 }),
    })
    expect(toApiError((await updateAdmin()).error).code).toBe('reauth_required')
    const handler = vi.fn(async () => true)
    const unregister = registerReauthHandler(handler)
    try {
      expect(toApiError((await api.GET('/api/admin/server-hosts')).error).code).toBe('permission_denied')
      expect(handler).not.toHaveBeenCalled()
      expect(calls).toHaveLength(2)
    } finally {
      unregister()
    }
  })

  it('totp_setup_required 时通知会话层刷新，错误照常交还调用方（Ruling-98）', async () => {
    mockServer({ [`GET ${HOSTS_PATH}`]: () => Response.json({ code: 'totp_setup_required', message: '请先开启两步验证' }, { status: 403 }) })
    const onSetupRequired = vi.fn()
    const unregister = registerTotpSetupHandler(onSetupRequired)
    try {
      const result = await api.GET('/api/admin/server-hosts')
      expect(toApiError(result.error).code).toBe('totp_setup_required')
      expect(onSetupRequired).toHaveBeenCalledTimes(1)
    } finally {
      unregister()
    }
  })
})
