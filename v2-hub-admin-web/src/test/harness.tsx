import { fireEvent, render, screen } from '@testing-library/react'
import { App as AntApp, ConfigProvider } from 'antd'
import type { ReactNode } from 'react'
import { vi } from 'vitest'
import { setCsrfToken } from '../api/client'
import { SessionContext, type SessionContextValue } from '../auth/session-context'

/** 测试里统一使用的 CSRF token，断言写请求是否带头时用。 */
export const TEST_CSRF = 'csrf-test-token'
const SELF_ID = 1

/** 成功响应信封。 */
export const ok = (data: unknown) => Response.json({ code: '0', message: 'ok', data })

function sessionWith(permissions: string[]): SessionContextValue {
  const granted = new Set(permissions)
  return {
    status: 'authenticated',
    session: { admin: { id: SELF_ID, username: 'root' }, permissions, csrfToken: TEST_CSRF, totpEnabled: false, menuOrder: [] },
    login: async () => null,
    logout: async () => undefined,
    refresh: async () => undefined,
    can: (p) => granted.has(p),
  }
}

/** 以给定权限码渲染页面（带 antd App 与会话上下文）。 */
export function renderWith(permissions: string[], node: ReactNode) {
  setCsrfToken(TEST_CSRF)
  return render(
    <ConfigProvider>
      <AntApp>
        <SessionContext.Provider value={sessionWith(permissions)}>{node}</SessionContext.Provider>
      </AntApp>
    </ConfigProvider>,
  )
}

/** 一次被拦截的请求。 */
export interface Recorded {
  method: string
  path: string
  /** 查询串（含开头的 ?，没有则为空串）。 */
  query: string
  csrf: string | null
  body: unknown
}

/**
 * 替换全局 fetch：按 “METHOD 路径” 查表应答，没有登记的返回 404；返回请求记录供断言。
 * 调用方在 afterEach 里 vi.unstubAllGlobals()。
 */
export function mockServer(handlers: Record<string, () => Response>): Recorded[] {
  const calls: Recorded[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (req: Request) => {
      const url = new URL(req.url)
      const path = url.pathname
      const text = await req.clone().text()
      calls.push({ method: req.method, path, query: url.search, csrf: req.headers.get('X-CSRF-Token'), body: text === '' ? null : JSON.parse(text) })
      const handler = handlers[`${req.method} ${path}`]
      return handler === undefined ? Response.json({ code: 'not_found', message: '' }, { status: 404 }) : handler()
    }),
  )
  return calls
}

/** 点开页面上名为 name 的页签（服务器列表与详情页按页签分栏，Ruling-88）。 */
export async function openTab(name: string): Promise<void> {
  fireEvent.click(await screen.findByRole('tab', { name }))
}
