import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { visibleMenu } from './menu'
import { chainStatusLabel, isValidChainName } from './pages/chain/labels'
import { TEST_CSRF, mockServer, ok, renderApp } from './test/harness'

const NOW = '2026-11-13T00:00:00Z'
const site = {
  siteName: '测试站', registerMode: 'invite', emailVerifyRequired: false, mailEnabled: false, checkinEnabled: false, ticketEnabled: false, sharedEnabled: false,
  shopEnabled: false, currency: 'CNY',
}
const features = { clientChain: true, customNode: true }
const session = { user: { id: 7, username: 'alice', status: 'active', email: null, isEmailVerified: false }, csrfToken: TEST_CSRF, totpEnabled: false, impersonated: false, features }
const nodes = [
  { id: 1, name: '香港 A', region: 'HK', protocol: 'vless', canBeExit: true },
  { id: 2, name: '美国 B', region: 'US', protocol: 'trojan', canBeExit: true },
  { id: 3, name: '日本 Hy2', region: 'JP', protocol: 'hysteria2', canBeExit: false },
]
const customNode = { id: 9, name: '自家 VPS', protocol: 'socks', address: '203.0.113.5', port: 1080, ruleCount: 1, createdAt: NOW }
const okRule = {
  id: 21, name: '', displayName: '香港 A → 美国 B', entry: { nodeId: 1, name: '香港 A' }, exit: { kind: 'node', id: 2, name: '美国 B', protocol: 'trojan' },
  isEnabled: true, status: 'ok', blockedReason: '', createdAt: NOW,
}
const blockedRule = {
  id: 22, name: '回家', displayName: '回家', entry: { nodeId: 1, name: '香港 A' }, exit: { kind: 'custom', id: 9, name: '自家 VPS', protocol: 'socks' },
  isEnabled: true, status: 'blocked', blockedReason: 'exit', createdAt: NOW,
}
const overview = { allowed: true, customNodeAllowed: true, maxRules: 20, maxCustomNodes: 10, rules: [okRule, blockedRule], customNodes: [customNode], nodes }

/** 已登录用户进链式代理页会发的请求。 */
function signedIn(chain: unknown, me: unknown = session) {
  return {
    'GET /api/user/site': () => ok(site),
    'GET /api/user/auth/me': () => ok(me),
    'GET /api/user/chain': () => ok(chain),
  }
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('链式代理文案与菜单', () => {
  it('状态带阻断原因；名称拒绝超长与换行；菜单随套餐开关显隐', () => {
    expect(chainStatusLabel(okRule as never)).toBe('通畅')
    expect(chainStatusLabel(blockedRule as never)).toBe('阻断 · 落地不可用')
    expect(chainStatusLabel({ ...blockedRule, blockedReason: 'plan' } as never)).toBe('阻断 · 套餐不支持')
    expect(isValidChainName('入口 → 落地')).toBe(true)
    expect(isValidChainName('一'.repeat(64))).toBe(true)
    expect(isValidChainName('一'.repeat(65))).toBe(false)
    expect(isValidChainName('a\nb')).toBe(false)
    const labels = (f?: typeof features) => visibleMenu(site as never, f).map((m) => m.label)
    expect(labels(features)).toContain('链式代理')
    expect(labels({ clientChain: false, customNode: true })).not.toContain('链式代理')
    expect(labels()).not.toContain('链式代理')
  })
})

describe('链式代理页', () => {
  it('规则表显示名称、落地类型与状态；新建规则时落地排除入口与 hysteria，提交后整体重取', async () => {
    const created = { ...okRule, id: 23, displayName: '美国 B → 香港 A' }
    let loads = 0
    const calls = mockServer({
      ...signedIn(overview),
      'GET /api/user/chain': () => {
        loads += 1
        return ok(loads === 1 ? overview : { ...overview, rules: [...overview.rules, created] })
      },
      'POST /api/user/chain/rules': () => ok(created),
    })
    renderApp('/chain')
    expect(await screen.findByText('香港 A → 美国 B')).toBeDefined()
    expect(screen.getByText('阻断 · 落地不可用')).toBeDefined()
    expect(screen.getByText('自有')).toBeDefined()
    expect(screen.getByRole('link', { name: /链式代理/ })).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: '新建规则' }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('入口节点'), { target: { value: '2' } })
    const exitSelect = within(dialog).getByLabelText('落地节点') as HTMLSelectElement
    const exitOptions = [...exitSelect.options].map((o) => o.textContent)
    expect(exitOptions).toEqual(['请选择', '香港 A', '自家 VPS'])
    fireEvent.change(exitSelect, { target: { value: 'node:1' } })
    expect((within(dialog).getByLabelText('名称（可选）') as HTMLInputElement).placeholder).toBe('美国 B → 香港 A')
    fireEvent.click(within(dialog).getByRole('button', { name: '创建' }))

    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.path === '/api/user/chain/rules')).toBe(true))
    const post = calls.find((c) => c.method === 'POST' && c.path === '/api/user/chain/rules')
    expect(post?.csrf).toBe(TEST_CSRF)
    expect(post?.body).toEqual({ entryNodeId: 2, exitNodeId: 1, name: '', isEnabled: true })
    expect(await screen.findByText('美国 B → 香港 A')).toBeDefined()
    expect(loads).toBe(2)
  })

  it('停用即时保存，停用的显示已停用；删除自有节点时提示会连带删除的规则数', async () => {
    const paused = { ...okRule, id: 24, name: '停着的', displayName: '停着的', isEnabled: false }
    const calls = mockServer({
      ...signedIn({ ...overview, rules: [...overview.rules, paused] }),
      'PUT /api/user/chain/rules/21': () => ok({ ...okRule, isEnabled: false }),
      'DELETE /api/user/chain/custom-nodes/9': () => ok(null),
    })
    renderApp('/chain')
    await screen.findByText('香港 A → 美国 B')
    const row = screen.getByText('香港 A → 美国 B').closest('tr') as HTMLElement
    fireEvent.click(within(row).getByRole('button', { name: '停用' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({ name: '', isEnabled: false })
    // 停用的规则只显示“已停用”，不显示通畅 / 阻断
    const pausedRow = screen.getByText('停着的').closest('tr') as HTMLElement
    expect(within(pausedRow).getByText('已停用')).toBeDefined()
    expect(within(pausedRow).getByRole('button', { name: '启用' })).toBeDefined()

    const nodeCard = screen.getByText(/自有落地节点（1 \/ 10）/).closest('section') as HTMLElement
    fireEvent.click(within(nodeCard).getByRole('button', { name: '删除' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('有 1 条规则以它为落地，会一并删除。')).toBeDefined()
    fireEvent.click(within(dialog).getByRole('button', { name: '删除' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.path === '/api/user/chain/custom-nodes/9')).toBe(true))
  })

  it('查看链接才单独请求；套餐不允许时给横幅并禁用新建与添加，删除仍可用', async () => {
    const link = 'socks5://203.0.113.5:1080'
    const calls = mockServer({
      ...signedIn({ ...overview, allowed: false, customNodeAllowed: false }, { ...session, features: { clientChain: false, customNode: false } }),
      'GET /api/user/chain/custom-nodes/9/link': () => ok({ link }),
    })
    renderApp('/chain')
    expect(await screen.findByText(/当前套餐不支持链式代理/)).toBeDefined()
    expect(screen.getByRole('button', { name: '新建规则' })).toHaveProperty('disabled', true)
    expect(screen.getByRole('button', { name: '添加' })).toHaveProperty('disabled', true)
    expect(screen.queryByRole('link', { name: /链式代理/ })).toBeNull()
    expect(screen.queryByText(link)).toBeNull()
    expect(calls.some((c) => c.path.endsWith('/link'))).toBe(false)

    fireEvent.click(screen.getByRole('button', { name: '查看链接' }))
    expect(await screen.findByText(link)).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: '隐藏' }))
    expect(screen.queryByText(link)).toBeNull()
    expect(screen.getAllByRole('button', { name: '删除' })[0]).toHaveProperty('disabled', false)
  })
})
