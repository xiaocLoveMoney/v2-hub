import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App as AntApp, ConfigProvider } from 'antd'
import { RouterProvider, createMemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { routes } from './routes'
import { SessionProvider } from './auth/SessionProvider'
import { MENU, PERMISSIONS, ROUTES, visibleMenu } from './menu'

const CSRF = 'csrf-from-server'

function sessionBody(permissions: string[], isTotpSetupRequired = false) {
  return {
    code: '0',
    message: 'ok',
    data: {
      admin: { id: 2, username: 'viewer1' },
      permissions,
      csrfToken: CSRF,
      totpEnabled: !isTotpSetupRequired,
      menuOrder: [],
      isSuperAdmin: isTotpSetupRequired,
      isOwner: false,
      totpSetupRequired: isTotpSetupRequired,
    },
  }
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
      <AntApp>
        <SessionProvider>
          <RouterProvider router={router} />
        </SessionProvider>
      </AntApp>
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
    expect(paths).toEqual([ROUTES.DASHBOARD, ROUTES.NODES, ROUTES.CHAIN_EXITS, ROUTES.SECURITY])
  })

  it('按菜单顺序设置排：列到的在前，没列到的按默认顺序在后，分组跟着组内最靠前的项走，不认识的键忽略（Ruling-57）', () => {
    const all = () => true
    const order = ['security', 'nope', 'chain-exits', 'servers', 'tickets']
    const keys = visibleMenu(all, order).map((e) => e.key)
    // 系统组因 security 排第一而整组提前；节点组次之，组内 chain-exits、servers 在前；我的组第三，tickets 在组内最前
    expect(keys.slice(0, 8)).toEqual(['security', 'admins', 'roles', 'audit-logs', 'settings', 'menu-order', 'mail-outbox', 'chain-exits'])
    expect(keys.slice(8, 12)).toEqual(['servers', 'hosts', 'nodes', 'chain-routes'])
    expect(keys.slice(12, 17)).toEqual(['tickets', 'dashboard', 'announcements', 'tutorials', 'shared-accounts'])
    expect(keys).toHaveLength(MENU.length)
    expect(visibleMenu(all).map((e) => e.key)).toEqual(MENU.map((e) => e.key))
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
    expect(await screen.findByRole('tab', { name: '节点目录' })).toBeDefined()
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

  it('开启两步验证的账号：密码通过后显示验证码框，带验证码再次登录成功', async () => {
    let isLoggedIn = false
    let loginHasCode = false
    const calls = mockServer({
      'GET /api/admin/auth/me': () =>
        isLoggedIn ? Response.json(sessionBody([PERMISSIONS.NODE_READ])) : Response.json(unauthenticated, { status: 401 }),
      'POST /api/admin/auth/login': () => {
        if (!loginHasCode) {
          return Response.json({ code: 'totp_required', message: '请输入两步验证码' }, { status: 401 })
        }
        isLoggedIn = true
        return Response.json(sessionBody([PERMISSIONS.NODE_READ]))
      },
    })
    renderAt(ROUTES.LOGIN)

    fireEvent.change(await screen.findByLabelText('用户名'), { target: { value: 'root' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'any-input-value' } })
    fireEvent.click(screen.getByRole('button', { name: /登\s*录/ }))

    const codeInput = await screen.findByLabelText('两步验证码')
    expect(screen.queryByText('请输入两步验证码')).toBeNull()
    loginHasCode = true
    fireEvent.change(codeInput, { target: { value: '123456' } })
    fireEvent.click(screen.getByRole('button', { name: /验\s*证/ }))
    expect((await screen.findAllByText('总览')).length).toBeGreaterThan(0)
    const lastLogin = calls.filter((c) => new URL(c.url).pathname === '/api/admin/auth/login').pop()
    expect(await lastLogin?.clone().json()).toMatchObject({ username: 'root', totpCode: '123456' })
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

  it('超管未开启两步验证：任何页面都跳到账号安全页，侧栏只剩账号安全并提示；开启后刷新会话恢复（Ruling-98）', async () => {
    let isSetupRequired = true
    mockServer({
      'GET /api/admin/auth/me': () => Response.json(sessionBody([PERMISSIONS.NODE_READ, PERMISSIONS.ADMIN_READ], isSetupRequired)),
      'POST /api/admin/auth/totp/setup': () => Response.json({ code: '0', message: 'ok', data: { secret: 'JBSWY3DPEHPK3PXP', otpauthUri: 'otpauth://totp/v2-hub:viewer1' } }),
      'POST /api/admin/auth/totp/enable': () => {
        isSetupRequired = false
        return Response.json({ code: '0', message: 'ok' })
      },
    })
    renderAt(ROUTES.NODES)

    expect(await screen.findByText('超级管理员必须先开启两步验证')).toBeDefined()
    expect(screen.getByRole('heading', { name: '账号安全' })).toBeDefined()
    const menuItems = screen.getAllByRole('menuitem').map((el) => el.textContent)
    expect(menuItems).toEqual(['账号安全'])
    expect(screen.queryByRole('tab', { name: '节点目录' })).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: /开启两步验证/ }))
    fireEvent.change(await screen.findByPlaceholderText('6 位验证码'), { target: { value: '123456' } })
    fireEvent.click(screen.getByRole('button', { name: /确认开启/ }))
    await waitFor(() => expect(screen.queryByText('超级管理员必须先开启两步验证')).toBeNull())
    expect(screen.getAllByRole('menuitem').map((el) => el.textContent)).toContain('节点')
  })

  it('会话里没标强制绑定、但接口回 totp_setup_required：刷新会话后跳到账号安全页（Ruling-98）', async () => {
    let isSetupRequired = false
    vi.stubGlobal(
      'fetch',
      vi.fn(async (req: Request) => {
        if (new URL(req.url).pathname === '/api/admin/auth/me') {
          return Response.json(sessionBody([PERMISSIONS.NODE_READ], isSetupRequired))
        }
        // 后端发现该超管还没开启两步验证：除豁免接口外一律 403
        isSetupRequired = true
        return Response.json({ code: 'totp_setup_required', message: '请先开启两步验证' }, { status: 403 })
      }),
    )
    renderAt(ROUTES.NODES)
    expect(await screen.findByText('超级管理员必须先开启两步验证')).toBeDefined()
    expect(screen.getAllByRole('menuitem').map((el) => el.textContent)).toEqual(['账号安全'])
  })
})
