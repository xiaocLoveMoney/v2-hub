import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MENU, PERMISSIONS } from '../../menu'
import { TEST_CSRF, mockServer, ok, renderWith } from '../../test/harness'
import { MenuOrderPage } from './MenuOrderPage'
import { USER_MENU_CATALOG } from './user-menu-catalog'

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const cardOf = (title: string) => screen.getByText(title).closest('.ant-card') as HTMLElement
const groupTitles = (card: HTMLElement) => within(card).getAllByRole('region').map((r) => r.getAttribute('aria-label'))

describe('MenuOrderPage', () => {
  it('按已存顺序显示，调整分组与组内顺序后整张保存；恢复默认存空列表（Ruling-57）', async () => {
    const calls = mockServer({
      'GET /api/admin/settings': () => ok({ menu: { admin: [], user: ['nodes'] } }),
      'PUT /api/admin/settings': () => ok({ menu: { admin: [], user: [] } }),
    })
    renderWith([PERMISSIONS.SETTING_READ, PERMISSIONS.SETTING_WRITE], <MenuOrderPage />)
    expect(await screen.findByText('客户门户菜单')).toBeDefined()
    const userCard = cardOf('客户门户菜单')
    const adminCard = cardOf('管理端菜单')
    // 已存的 user 顺序把“使用”组提到最前；条件菜单带提示
    expect(groupTitles(userCard)).toEqual(['使用', '顶层', '我的'])
    expect(within(userCard).getAllByText('开启商店时显示')).toHaveLength(2)

    fireEvent.click(within(userCard).getByRole('button', { name: '下移 分组 使用' }))
    expect(groupTitles(userCard)).toEqual(['顶层', '使用', '我的'])
    expect(within(adminCard).getByRole('button', { name: '上移 总览' })).toHaveProperty('disabled', true)
    fireEvent.click(within(adminCard).getByRole('button', { name: '上移 公告' }))

    fireEvent.click(screen.getByRole('button', { name: /保\s*存/ }))
    await waitFor(() => expect(calls.filter((c) => c.method === 'PUT')).toHaveLength(1))
    const put = calls.find((c) => c.method === 'PUT')
    expect(put?.csrf).toBe(TEST_CSRF)
    const adminDefault = MENU.map((e) => e.key)
    expect(put?.body).toEqual({
      menu: {
        admin: ['announcements', 'dashboard', ...adminDefault.slice(2)],
        user: ['dashboard', 'shop', 'announcements', 'nodes', 'tutorials', 'subscribe-logs', 'shared-accounts', 'tickets', 'chain', 'account', 'wallet', 'invite'],
      },
    })
    expect(USER_MENU_CATALOG).toHaveLength(12)

    fireEvent.click(screen.getByRole('button', { name: '恢复默认' }))
    expect(groupTitles(cardOf('客户门户菜单'))).toEqual(['顶层', '我的', '使用'])
    fireEvent.click(screen.getByRole('button', { name: /保\s*存/ }))
    await waitFor(() => expect(calls.filter((c) => c.method === 'PUT')).toHaveLength(2))
    expect(calls.filter((c) => c.method === 'PUT')[1]?.body).toEqual({ menu: { admin: [], user: [] } })
  })

  it('只有 setting:read 时只能查看', async () => {
    mockServer({ 'GET /api/admin/settings': () => ok({ menu: { admin: [], user: [] } }) })
    renderWith([PERMISSIONS.SETTING_READ], <MenuOrderPage />)
    expect(await screen.findByText('管理端菜单')).toBeDefined()
    expect(screen.queryByRole('button', { name: /保\s*存/ })).toBeNull()
    expect(within(cardOf('管理端菜单')).getByRole('button', { name: '下移 总览' })).toHaveProperty('disabled', true)
  })
})
