import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { App as AntApp, ConfigProvider } from 'antd'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setCsrfToken, type Session } from '../api/client'
import { SessionContext } from '../auth/session-context'
import { TEST_CSRF, mockServer, ok, sessionWith } from '../test/harness'
import { SecurityPage } from './SecurityPage'

/** RFC 6238 文档常用的示例 Base32 密钥，仅测试展示用。 */
const DEMO = 'JBSWY3DPEHPK3PXP'
/** 表单里输入的旧 / 新密码（仅测试用）。 */
const before = 'old-value-123456'
const after = 'new-value-123456'
const PASSWORD_PATH = '/api/admin/auth/password'

function renderPage(over: Partial<Session>, refresh: () => Promise<void> = async () => undefined) {
  const value = { ...sessionWith([], over), refresh }
  setCsrfToken(TEST_CSRF)
  return render(
    <ConfigProvider>
      <AntApp>
        <SessionContext.Provider value={value}>
          <SecurityPage />
        </SessionContext.Provider>
      </AntApp>
    </ConfigProvider>,
  )
}

function passwordCard(): HTMLElement {
  const card = screen.getByText('修改密码', { selector: '.ant-card-head-title' }).closest('.ant-card')
  if (card === null) {
    throw new Error('change password card not rendered')
  }
  return card as HTMLElement
}

function fillPasswordForm(card: HTMLElement, confirm: string) {
  fireEvent.change(within(card).getByLabelText('旧密码'), { target: { value: before } })
  fireEvent.change(within(card).getByLabelText('新密码'), { target: { value: after } })
  fireEvent.change(within(card).getByLabelText('确认新密码'), { target: { value: confirm } })
  fireEvent.click(within(card).getByRole('button', { name: /修改密码/ }))
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('SecurityPage：两步验证', () => {
  it('开启流程：生成密钥 → 显示密钥 → 提交验证码 → 刷新会话', async () => {
    const calls = mockServer({
      'POST /api/admin/auth/totp/setup': () => ok({ secret: DEMO, otpauthUri: 'otpauth://totp/v2-hub:root' }),
      'POST /api/admin/auth/totp/enable': () => ok(null),
    })
    const refresh = vi.fn(async () => undefined)
    renderPage({ totpEnabled: false }, refresh)

    expect(screen.getByText('未开启')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: /开启两步验证/ }))
    expect(await screen.findByText(DEMO)).toBeDefined()

    fireEvent.change(screen.getByPlaceholderText('6 位验证码'), { target: { value: '123456' } })
    fireEvent.click(screen.getByRole('button', { name: /确认开启/ }))
    await waitFor(() => expect(refresh).toHaveBeenCalled())
    expect(calls.find((c) => c.path === '/api/admin/auth/totp/enable')?.body).toEqual({ code: '123456' })
  })

  it('已开启时显示关闭表单，验证码错误时展示后端提示', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => Response.json({ code: 'invalid_argument', message: '验证码错误' }, { status: 400 })),
    )
    const refresh = vi.fn(async () => undefined)
    renderPage({ totpEnabled: true }, refresh)

    expect(screen.getByText('已开启')).toBeDefined()
    fireEvent.change(screen.getByPlaceholderText('6 位验证码'), { target: { value: '000000' } })
    fireEvent.click(screen.getByRole('button', { name: /关闭两步验证/ }))
    expect(await screen.findByText('验证码错误')).toBeDefined()
    expect(refresh).not.toHaveBeenCalled()
  })

  it('超管不显示关闭两步验证，并写明原因与遗失设备的处理（Ruling-98）', () => {
    renderPage({ totpEnabled: true, isSuperAdmin: true })
    expect(screen.getByText('已开启')).toBeDefined()
    expect(screen.queryByRole('button', { name: /关闭两步验证/ })).toBeNull()
    expect(screen.queryByPlaceholderText('6 位验证码')).toBeNull()
    expect(screen.getByText(/超级管理员必须开启两步验证，不能关闭/)).toBeDefined()
    expect(screen.getByText(/hub admin-totp-reset/)).toBeDefined()
  })
})

describe('SecurityPage：修改密码（R9）', () => {
  it('成功：提交旧密码与新密码、带 CSRF 头，提示其它设备已下线并清空表单', async () => {
    const calls = mockServer({ [`PUT ${PASSWORD_PATH}`]: () => ok(null) })
    renderPage({})
    const card = passwordCard()
    fillPasswordForm(card, after)

    expect(await screen.findByText('密码已修改，其它设备上的登录已下线')).toBeDefined()
    const put = calls.find((c) => c.method === 'PUT' && c.path === PASSWORD_PATH)
    expect(put?.csrf).toBe(TEST_CSRF)
    expect(put?.body).toEqual({ oldPassword: before, newPassword: after })
    await waitFor(() => expect((within(card).getByLabelText('旧密码') as HTMLInputElement).value).toBe(''))
  })

  it('旧密码错误（400）在卡片里就地提示', async () => {
    mockServer({ [`PUT ${PASSWORD_PATH}`]: () => Response.json({ code: 'invalid_argument', message: '旧密码错误' }, { status: 400 }) })
    renderPage({})
    const card = passwordCard()
    fillPasswordForm(card, after)
    expect(await within(card).findByText('旧密码错误')).toBeDefined()
  })

  it('两次新密码不一致、新密码太短时不提交', async () => {
    const calls = mockServer({ [`PUT ${PASSWORD_PATH}`]: () => ok(null) })
    renderPage({})
    const card = passwordCard()
    fillPasswordForm(card, 'something-else-1')
    expect(await within(card).findByText('两次输入的新密码不一致')).toBeDefined()
    fireEvent.change(within(card).getByLabelText('新密码'), { target: { value: 'short' } })
    expect(await within(card).findByText(/密码长度须为 12–128 个字符/)).toBeDefined()
    expect(calls.some((c) => c.method === 'PUT')).toBe(false)
  })
})
