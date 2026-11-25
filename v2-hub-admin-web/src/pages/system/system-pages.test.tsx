import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import type { ReactNode } from 'react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { PERMISSIONS } from '../../menu'
import { TEST_CSRF as CSRF, mockServer, ok, renderWith as renderInHarness } from '../../test/harness'
import { AdminsPage } from './AdminsPage'
import { AuditLogsPage } from './AuditLogsPage'
import { RolesPage } from './RolesPage'

const SELF_ID = 1
/** 表单里输入的初始密码（仅测试用）。 */
const typed = 'long-enough-value'
const CREATED_AT = '2026-11-11T00:00:00Z'

function renderWith(permissions: string[], node: ReactNode) {
  return renderInHarness(permissions, <MemoryRouter>{node}</MemoryRouter>)
}

function rowOf(text: string): HTMLElement {
  const row = screen.getByText(text).closest('tr')
  if (row === null) {
    throw new Error(`row not rendered: ${text}`)
  }
  return row
}

const roles = [
  { id: 1, code: 'super_admin', name: '超级管理员', isBuiltin: true, permissions: [] },
  { id: 2, code: 'operator', name: '客服运营', isBuiltin: true, permissions: ['node:read', 'user:write'] },
  { id: 3, code: 'viewer', name: '只读', isBuiltin: false, permissions: ['node:read'] },
]

const permissionList = [
  { code: 'node:read', name: '查看节点', superOnly: false },
  { code: 'user:write', name: '管理用户', superOnly: false },
  { code: 'admin:write', name: '管理管理员', superOnly: true },
  { code: 'host:write', name: '管理 SSH 托管', superOnly: true },
]

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('AdminsPage', () => {
  it('列出管理员；自己那行只有去账号安全页的入口；新建时带 CSRF 头与表单内容', async () => {
    const admins = [
      { id: SELF_ID, username: 'root', isDisabled: false, totpEnabled: false, isOwner: false, roles: ['super_admin'], lastLoginAt: null, createdAt: CREATED_AT },
      { id: 2, username: 'ops', isDisabled: true, totpEnabled: true, isOwner: false, roles: [], lastLoginAt: null, createdAt: CREATED_AT },
    ]
    const calls = mockServer({
      'GET /api/admin/admins': () => ok({ items: admins, total: 2 }),
      'GET /api/admin/roles': () => ok(roles),
      'POST /api/admin/admins': () => ok({ ...admins[1], id: 3, username: 'newbie' }),
    })
    renderWith([PERMISSIONS.ADMIN_READ, PERMISSIONS.ADMIN_WRITE], <AdminsPage />)

    await screen.findByText('root')
    const rootRow = rowOf('root')
    // R4 / R9：自己那行不给编辑与重置密码，改密码去账号安全页
    expect(within(rootRow).queryByRole('button')).toBeNull()
    expect(within(rootRow).getByRole('link', { name: '去账号安全页改密码' }).getAttribute('href')).toBe('/account/security')
    expect(within(rowOf('ops')).getByRole('button', { name: /重置密码/ })).toBeDefined()
    expect(screen.getByText('已禁用')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: /新建管理员/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('用户名'), { target: { value: 'newbie' } })
    fireEvent.change(within(dialog).getByLabelText('初始密码'), { target: { value: typed } })
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

  it('所有者那行显示“所有者”标记，不给编辑、重置密码与清除两步验证（R11）', async () => {
    const admins = [
      { id: SELF_ID, username: 'root', isDisabled: false, totpEnabled: true, isOwner: false, roles: ['super_admin'], lastLoginAt: null, createdAt: CREATED_AT },
      { id: 5, username: 'boss', isDisabled: false, totpEnabled: true, isOwner: true, roles: ['super_admin'], lastLoginAt: null, createdAt: CREATED_AT },
      { id: 6, username: 'peer', isDisabled: false, totpEnabled: true, isOwner: false, roles: ['super_admin'], lastLoginAt: null, createdAt: CREATED_AT },
    ]
    mockServer({
      'GET /api/admin/admins': () => ok({ items: admins, total: admins.length }),
      'GET /api/admin/roles': () => ok(roles),
    })
    renderWith([PERMISSIONS.ADMIN_READ, PERMISSIONS.ADMIN_WRITE], <AdminsPage />)
    await screen.findByText('boss')
    const ownerRow = rowOf('boss')
    expect(within(ownerRow).getByText('所有者')).toBeDefined()
    expect(within(ownerRow).queryByRole('button')).toBeNull()
    expect(within(ownerRow).getByText('所有者不可修改')).toBeDefined()
    const peerRow = rowOf('peer')
    expect(within(peerRow).queryByText('所有者')).toBeNull()
    for (const name of [/编\s*辑/, /重置密码/, /清除两步验证/]) {
      expect(within(peerRow).getByRole('button', { name })).toBeDefined()
    }
  })
})

describe('RolesPage', () => {
  it('内置角色（含预设）显示“内置”“不可修改”并列出权限；普通角色可编辑删除', async () => {
    mockServer({
      'GET /api/admin/roles': () => ok(roles),
      'GET /api/admin/permissions': () => ok(permissionList),
    })
    renderWith([PERMISSIONS.ADMIN_READ, PERMISSIONS.ADMIN_WRITE], <RolesPage />)
    expect(await screen.findAllByText('不可修改')).toHaveLength(2)
    const operatorRow = rowOf('operator')
    expect(within(operatorRow).getByText('内置')).toBeDefined()
    await waitFor(() => expect(within(operatorRow).getByText('管理用户')).toBeDefined())
    expect(within(rowOf('super_admin')).getByText('全部权限')).toBeDefined()
    const viewerRow = rowOf('viewer')
    expect(within(viewerRow).getByRole('button', { name: /删\s*除/ })).toBeDefined()
    expect(within(viewerRow).getByText('查看节点')).toBeDefined()
  })

  it('新建角色的勾选项不列超管专属权限码，也不再按自己拥有的权限过滤（Ruling-95）', async () => {
    const calls = mockServer({
      'GET /api/admin/roles': () => ok(roles),
      'GET /api/admin/permissions': () => ok(permissionList),
      'POST /api/admin/roles': () => ok({ id: 9, code: 'support', name: '客服', isBuiltin: false, permissions: ['user:write'] }),
    })
    // 只给 admin:read / admin:write，勾选项仍列出全部非超管专属的权限码
    renderWith([PERMISSIONS.ADMIN_READ, PERMISSIONS.ADMIN_WRITE], <RolesPage />)
    await screen.findByText('viewer')
    fireEvent.click(screen.getByRole('button', { name: /新建角色/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByLabelText('查看节点（node:read）')).toBeDefined()
    expect(within(dialog).getByLabelText('管理用户（user:write）')).toBeDefined()
    expect(within(dialog).queryByText(/admin:write/)).toBeNull()
    expect(within(dialog).queryByText(/host:write/)).toBeNull()
    expect(within(dialog).queryByText(/只列出你拥有的权限/)).toBeNull()

    fireEvent.change(within(dialog).getByLabelText('编码'), { target: { value: 'support' } })
    fireEvent.change(within(dialog).getByLabelText('名称'), { target: { value: '客服' } })
    fireEvent.click(within(dialog).getByLabelText('管理用户（user:write）'))
    fireEvent.click(within(dialog).getByRole('button', { name: /创\s*建/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    const post = calls.find((c) => c.method === 'POST')
    expect(post?.csrf).toBe(CSRF)
    expect(post?.body).toEqual({ code: 'support', name: '客服', permissions: ['user:write'] })
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
