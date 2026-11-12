import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { App as AntApp, ConfigProvider } from 'antd'
import type { ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setCsrfToken } from '../../api/client'
import { SessionContext, type SessionContextValue } from '../../auth/session-context'
import { PERMISSIONS } from '../../menu'
import { AdminsPage } from './AdminsPage'
import { AuditLogsPage } from './AuditLogsPage'
import { RolesPage } from './RolesPage'

const CSRF = 'csrf-test-token'
const SELF_ID = 1
/** 表单里输入的初始口令（仅测试用）。 */
const typed = 'long-enough-value'
const ok = (data: unknown) => Response.json({ code: '0', message: 'ok', data })

function sessionWith(permissions: string[]): SessionContextValue {
  const granted = new Set(permissions)
  return {
    status: 'authenticated',
    session: { admin: { id: SELF_ID, username: 'root' }, permissions, csrfToken: CSRF, totpEnabled: false },
    login: async () => null,
    logout: async () => undefined,
    refresh: async () => undefined,
    can: (p) => granted.has(p),
  }
}

function renderWith(permissions: string[], node: ReactNode) {
  setCsrfToken(CSRF)
  return render(
    <ConfigProvider>
      <AntApp>
        <SessionContext.Provider value={sessionWith(permissions)}>{node}</SessionContext.Provider>
      </AntApp>
    </ConfigProvider>,
  )
}

interface Recorded {
  method: string
  path: string
  csrf: string | null
  body: unknown
}

function mockServer(handlers: Record<string, () => Response>) {
  const calls: Recorded[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (req: Request) => {
      const path = new URL(req.url).pathname
      const text = await req.clone().text()
      calls.push({ method: req.method, path, csrf: req.headers.get('X-CSRF-Token'), body: text === '' ? null : JSON.parse(text) })
      const handler = handlers[`${req.method} ${path}`]
      return handler === undefined ? Response.json({ code: 'not_found', message: '' }, { status: 404 }) : handler()
    }),
  )
  return calls
}

const roles = [
  { id: 1, code: 'super_admin', name: '超级管理员', isBuiltin: true, permissions: ['admin:read', 'admin:write'] },
  { id: 2, code: 'viewer', name: '只读', isBuiltin: false, permissions: ['node:read'] },
]

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('AdminsPage', () => {
  it('列出管理员；自己那行不能编辑；新建时带 CSRF 头与表单内容', async () => {
    const admins = [
      { id: SELF_ID, username: 'root', isDisabled: false, totpEnabled: false, roles: ['super_admin'], lastLoginAt: null, createdAt: '2026-11-11T00:00:00Z' },
      { id: 2, username: 'ops', isDisabled: true, totpEnabled: true, roles: [], lastLoginAt: null, createdAt: '2026-11-11T00:00:00Z' },
    ]
    const calls = mockServer({
      'GET /api/admin/admins': () => ok({ items: admins, total: 2 }),
      'GET /api/admin/roles': () => ok(roles),
      'POST /api/admin/admins': () => ok({ ...admins[1], id: 3, username: 'newbie' }),
    })
    renderWith([PERMISSIONS.ADMIN_READ, PERMISSIONS.ADMIN_WRITE], <AdminsPage />)

    const rootRow = (await screen.findByText('root')).closest('tr')
    expect(rootRow).not.toBeNull()
    expect(within(rootRow as HTMLElement).getByRole('button', { name: /编\s*辑/ })).toHaveProperty('disabled', true)
    expect(screen.getByText('已禁用')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: /新建管理员/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('用户名'), { target: { value: 'newbie' } })
    fireEvent.change(within(dialog).getByLabelText('初始口令'), { target: { value: typed } })
    fireEvent.click(within(dialog).getByRole('button', { name: /创\s*建/ }))

    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    const post = calls.find((c) => c.method === 'POST')
    expect(post?.csrf).toBe(CSRF)
    expect(post?.body).toEqual({ username: 'newbie', password: typed, roles: [] })
  })

  it('只有 admin:read 时不显示新建与操作列', async () => {
    mockServer({
      'GET /api/admin/admins': () => ok({ items: [], total: 0 }),
      'GET /api/admin/roles': () => ok(roles),
    })
    renderWith([PERMISSIONS.ADMIN_READ], <AdminsPage />)
    expect(await screen.findByText('管理员')).toBeDefined()
    expect(screen.queryByRole('button', { name: /新建管理员/ })).toBeNull()
    expect(screen.queryByText('操作')).toBeNull()
  })
})

describe('RolesPage', () => {
  it('内置角色不可修改，普通角色可编辑删除', async () => {
    mockServer({
      'GET /api/admin/roles': () => ok(roles),
      'GET /api/admin/permissions': () => ok([{ code: 'node:read', name: '查看节点' }]),
    })
    renderWith([PERMISSIONS.ADMIN_READ, PERMISSIONS.ADMIN_WRITE], <RolesPage />)
    expect(await screen.findByText('不可修改')).toBeDefined()
    const viewerRow = screen.getByText('viewer').closest('tr') as HTMLElement
    expect(within(viewerRow).getByRole('button', { name: /删\s*除/ })).toBeDefined()
    await waitFor(() => expect(within(viewerRow).getByText('查看节点')).toBeDefined())
  })
})

describe('AuditLogsPage', () => {
  it('详情按纯文本展示，不解析 HTML', async () => {
    const logs = [
      {
        id: 1, actorType: 'admin', actorId: 1, action: 'admin.login_failed', targetType: null, targetId: null,
        detail: { username: '<img src=x onerror=alert(1)>' }, clientIp: '203.0.113.7', createdAt: '2026-11-11T00:00:00Z',
      },
    ]
    mockServer({ 'GET /api/admin/audit-logs': () => ok({ items: logs, total: 1 }) })
    const { container } = renderWith([PERMISSIONS.AUDIT_READ], <AuditLogsPage />)
    expect(await screen.findByText('登录失败')).toBeDefined()
    expect(screen.getByText('203.0.113.7')).toBeDefined()
    expect(container.querySelector('img[src="x"]')).toBeNull()
  })
})
