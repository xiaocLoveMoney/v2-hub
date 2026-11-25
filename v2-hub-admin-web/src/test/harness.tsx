import { fireEvent, render, screen } from '@testing-library/react'
import { App as AntApp, ConfigProvider } from 'antd'
import type { ReactNode } from 'react'
import { vi } from 'vitest'
import { setCsrfToken, type Session } from '../api/client'
import { SessionContext, type SessionContextValue } from '../auth/session-context'

/** 测试里统一使用的 CSRF token，断言写请求是否带头时用。 */
export const TEST_CSRF = 'csrf-test-token'
const SELF_ID = 1

/** 成功响应信封。 */
export const ok = (data: unknown) => Response.json({ code: '0', message: 'ok', data })

/**
 * 以给定权限码构造会话上下文（默认员工账号）；over 覆盖会话里的其它字段（如超管、所有者、强制绑定两步验证、代理账号 kind）。
 * @param permissions 权限码
 * @param over 覆盖的会话字段
 */
export function sessionWith(permissions: string[], over: Partial<Session> = {}): SessionContextValue {
  const granted = new Set(permissions)
  return {
    status: 'authenticated',
    session: {
      admin: { id: SELF_ID, username: 'root' },
      kind: 'staff',
      permissions,
      csrfToken: TEST_CSRF,
      totpEnabled: false,
      menuOrder: [],
      isSuperAdmin: false,
      isOwner: false,
      totpSetupRequired: false,
      ...over,
    },
    login: async () => null,
    logout: async () => undefined,
    refresh: async () => undefined,
    can: (p) => granted.has(p),
  }
}

/** 以给定权限码渲染页面（带 antd App 与会话上下文）；over 覆盖会话里的其它字段。 */
export function renderWith(permissions: string[], node: ReactNode, over: Partial<Session> = {}) {
  setCsrfToken(TEST_CSRF)
  return render(
    <ConfigProvider>
      <AntApp>
        <SessionContext.Provider value={sessionWith(permissions, over)}>{node}</SessionContext.Provider>
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
  /** JSON 请求体解析后的值；非 JSON（如 multipart）为原文。 */
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
      // multipart（图片上传）不是 JSON，按原文记下
      const isJson = (req.headers.get('Content-Type') ?? '').includes('json')
      const body = text === '' ? null : isJson ? JSON.parse(text) : text
      calls.push({ method: req.method, path, query: url.search, csrf: req.headers.get('X-CSRF-Token'), body })
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
