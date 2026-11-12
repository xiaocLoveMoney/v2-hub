import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { ConfigProvider } from 'antd'
import { RouterProvider, createMemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { routes } from './routes'
import { SessionProvider } from './auth/SessionProvider'
import { PERMISSIONS, ROUTES, visibleMenu } from './menu'

const CSRF = 'csrf-from-server'

function sessionBody(permissions: string[]) {
  return { code: '0', message: 'ok', data: { admin: { id: 2, username: 'viewer1' }, permissions, csrfToken: CSRF } }
}

const unauthenticated = { code: 'unauthenticated', message: '请先登录' }

/** 按路径返回预设响应，并记录每次请求，便于断言 CSRF 头。 */
function mockServer(handlers: Record<string, () => Response>) {
  const calls: Request[] = []
  const fetchMock = vi.fn(async (input: Request) => {
    calls.push(input)
    const path = new URL(input.url).pathname
    const handler = handlers[`${input.method} ${path}`]
    return handler === undefined ? Response.json({ code: 'not_found', message: '' }, { status: 404 }) : handler()
  })
  vi.stubGlobal('fetch', fetchMock)
  return calls
}

function renderAt(path: string) {
  const router = createMemoryRouter(routes, { initialEntries: [path] })
  return render(
    <ConfigProvider>
      <SessionProvider>
        <RouterProvider router={router} />
      </SessionProvider>
    </ConfigProvider>,
  )
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('visibleMenu', () => {
  it('按权限码过滤菜单，无权限要求的项始终可见', () => {
    const paths = visibleMenu((p) => p === PERMISSIONS.NODE_READ).map((e) => e.path)
    expect(paths).toEqual([ROUTES.DASHBOARD, ROUTES.NODES])
  })
})

describe('管理端登录流程', () => {
  it('未登录访问受保护页面会跳到登录页，登录后只显示有权限的菜单', async () => {
    let isLoggedIn = false
    mockServer({
      'GET /api/admin/auth/me': () =>
        isLoggedIn ? Response.json(sessionBody([PERMISSIONS.NODE_READ])) : Response.json(unauthenticated, { status: 401 }),
      'POST /api/admin/auth/login': () => {
        isLoggedIn = true
        return Response.json(sessionBody([PERMISSIONS.NODE_READ]))
      },
    })
    renderAt(ROUTES.NODES)

    expect(await screen.findByText('v2-hub 管理端')).toBeDefined()
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'viewer1' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'any-input-value' } })
    fireEvent.click(screen.getByRole('button', { name: /登\s*录/ }))

    // 登录后回到原本想访问的节点页
    expect(await screen.findByText('该页面在 M1 交付')).toBeDefined()
    expect(screen.getAllByText('节点').length).toBeGreaterThan(0)
    expect(screen.queryByText('管理员')).toBeNull()
    expect(screen.queryByText('审计日志')).toBeNull()
  })

  it('登录失败显示后端返回的提示', async () => {
    mockServer({
      'GET /api/admin/auth/me': () => Response.json(unauthenticated, { status: 401 }),
      'POST /api/admin/auth/login': () => Response.json({ code: 'unauthenticated', message: '用户名或密码错误' }, { status: 401 }),
    })
    renderAt(ROUTES.LOGIN)

    fireEvent.change(await screen.findByLabelText('用户名'), { target: { value: 'root' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'wrong-input' } })
    fireEvent.click(screen.getByRole('button', { name: /登\s*录/ }))
    expect(await screen.findByText('用户名或密码错误')).toBeDefined()
  })

  it('无权限的页面显示 403，登出请求带 CSRF 头', async () => {
    const calls = mockServer({
      'GET /api/admin/auth/me': () => Response.json(sessionBody([PERMISSIONS.NODE_READ])),
      'POST /api/admin/auth/logout': () => Response.json({ code: '0', message: 'ok' }),
    })
    renderAt(ROUTES.ADMINS)

    expect(await screen.findByText('没有权限')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: /退\s*出/ }))
    await waitFor(() => expect(calls.some((c) => new URL(c.url).pathname === '/api/admin/auth/logout')).toBe(true))
    const logoutCall = calls.find((c) => new URL(c.url).pathname === '/api/admin/auth/logout')
    expect(logoutCall?.headers.get('X-CSRF-Token')).toBe(CSRF)
    expect(await screen.findByText('v2-hub 管理端')).toBeDefined()
  })
})
