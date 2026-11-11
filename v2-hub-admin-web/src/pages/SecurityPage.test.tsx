import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App as AntApp, ConfigProvider } from 'antd'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setCsrfToken } from '../api/client'
import { SessionContext, type SessionContextValue } from '../auth/session-context'
import { SecurityPage } from './SecurityPage'

const CSRF = 'csrf-test-token'
/** RFC 6238 文档常用的示例 Base32 密钥，仅测试展示用。 */
const DEMO = 'JBSWY3DPEHPK3PXP'
const ok = (data: unknown) => Response.json({ code: '0', message: 'ok', data })

function renderPage(totpEnabled: boolean, refresh: () => Promise<void>) {
  const value: SessionContextValue = {
    status: 'authenticated',
    session: { admin: { id: 1, username: 'root' }, permissions: [], csrfToken: CSRF, totpEnabled },
    login: async () => null,
    logout: async () => undefined,
    refresh,
    can: () => false,
  }
  setCsrfToken(CSRF)
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

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('SecurityPage', () => {
  it('开启流程：生成密钥 → 显示密钥 → 提交验证码 → 刷新会话', async () => {
    const bodies: Record<string, unknown> = {}
    vi.stubGlobal(
      'fetch',
      vi.fn(async (req: Request) => {
        const path = new URL(req.url).pathname
        const text = await req.clone().text()
        bodies[path] = text === '' ? null : JSON.parse(text)
        if (path === '/api/admin/auth/totp/setup') {
          return ok({ secret: DEMO, otpauthUri: 'otpauth://totp/v2-hub:root' })
        }
        return ok(null)
      }),
    )
    const refresh = vi.fn(async () => undefined)
    renderPage(false, refresh)

    expect(screen.getByText('未开启')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: /开启两步验证/ }))
    expect(await screen.findByText(DEMO)).toBeDefined()

    fireEvent.change(screen.getByPlaceholderText('6 位验证码'), { target: { value: '123456' } })
    fireEvent.click(screen.getByRole('button', { name: /确认开启/ }))
    await waitFor(() => expect(refresh).toHaveBeenCalled())
    expect(bodies['/api/admin/auth/totp/enable']).toEqual({ code: '123456' })
  })

  it('已开启时显示关闭表单，验证码错误时展示后端提示', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => Response.json({ code: 'invalid_argument', message: '验证码错误' }, { status: 400 })),
    )
    const refresh = vi.fn(async () => undefined)
    renderPage(true, refresh)

    expect(screen.getByText('已开启')).toBeDefined()
    fireEvent.change(screen.getByPlaceholderText('6 位验证码'), { target: { value: '000000' } })
    fireEvent.click(screen.getByRole('button', { name: /关闭两步验证/ }))
    expect(await screen.findByText('验证码错误')).toBeDefined()
    expect(refresh).not.toHaveBeenCalled()
  })
})
