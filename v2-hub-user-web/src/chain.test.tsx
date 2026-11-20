import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { visibleMenu } from './menu'
import { buildChainFlow, curvePath, truncateByWidth } from './pages/chain/chain-flow'
import { entryState, isValidChainName, routeState } from './pages/chain/labels'
import { TEST_CSRF, enT, mockServer, ok, renderApp, zhT } from './test/harness'

const NOW = '2026-11-14T00:00:00Z'
const ROUTES_PATH = '/api/user/chain/routes'
const site = {
  siteName: '测试站', menuOrder: [], registerMode: 'invite', emailVerifyRequired: false, mailEnabled: false, checkinEnabled: false, ticketEnabled: false, sharedEnabled: false,
  shopEnabled: false, currency: 'CNY',
}
const features = { clientChain: true, customNode: true, serverChain: true }
const session = { user: { id: 7, username: 'alice', status: 'active', email: null, isEmailVerified: false }, csrfToken: TEST_CSRF, totpEnabled: false, impersonated: false, features }
const nodes = [
  { id: 1, name: '香港 A', region: 'HK', protocol: 'vless', canBeExit: true, canBeServerEntry: true },
  { id: 2, name: '美国 B', region: 'US', protocol: 'trojan', canBeExit: true, canBeServerEntry: true },
  { id: 3, name: '日本 Hy2', region: 'JP', protocol: 'hysteria2', canBeExit: false, canBeServerEntry: false },
  { id: 4, name: '新加坡 D', region: 'SG', protocol: 'vmess', canBeExit: true, canBeServerEntry: true },
]
const ownExit = { id: 9, name: '自家 VPS', protocol: 'socks', address: '203.0.114.5', port: 1080, isEnabled: true, routeCount: 1, createdAt: NOW }
const platformExits = [
  { id: 30, name: '东京出口', protocol: 'vless', allowServer: true, allowClient: false },
  { id: 31, name: '公共出口', protocol: 'trojan', allowServer: false, allowClient: true },
]
const entry = (nodeId: number, name: string, state: string, coveredBy = 0) => ({ nodeId, name, state, coveredBy })
const serverTop = {
  id: 21, kind: 'server', position: 1, name: '', displayName: '东京出口',
  entries: [entry(1, '香港 A', 'applied')], exit: { kind: 'platform', id: 30, name: '东京出口', protocol: 'vless' },
  isEnabled: true, status: 'ok', blockedReason: '', createdAt: NOW,
}
const serverWide = {
  id: 22, kind: 'server', position: 2, name: '回家', displayName: '回家',
  entries: [entry(1, '香港 A', 'shadowed', 1), entry(4, '新加坡 D', 'pending')], exit: { kind: 'own', id: 9, name: '自家 VPS', protocol: 'socks' },
  isEnabled: true, status: 'ok', blockedReason: '', createdAt: NOW,
}
const clientRoute = {
  id: 41, kind: 'client', position: 1, name: '', displayName: '美国 B',
  entries: [entry(1, '香港 A', 'ok')], exit: { kind: 'node', id: 2, name: '美国 B', protocol: 'trojan' },
  isEnabled: true, status: 'blocked', blockedReason: 'exit', createdAt: NOW,
}
const overview = {
  allowed: true, exitAllowed: true, serverAllowed: true, maxClientRoutes: 20, maxServerRoutes: 10, maxRouteEntries: 1000, maxExits: 10,
  clientRoutes: [clientRoute], serverRoutes: [serverTop, serverWide], exits: [ownExit], platformExits, nodes,
}

/** 已登录用户进链式代理页会发的请求。 */
function signedIn(chain: unknown, me: unknown = session) {
  return {
    'GET /api/user/site': () => ok(site),
    'GET /api/user/auth/me': () => ok(me),
    'GET /api/user/chain': () => ok(chain),
  }
}

/** 规则所在的列表项。 */
function routeItem(text: string): HTMLElement {
  const item = screen.getByText(text).closest('li')
  if (item === null) {
    throw new Error(`route "${text}" not rendered`)
  }
  return item
}

function optionTexts(select: HTMLElement): (string | null)[] {
  return [...(select as HTMLSelectElement).options].map((o) => o.textContent)
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('链式代理文案与菜单', () => {
  it('规则状态带阻断原因；起飞节点状态写明被哪条覆盖；名称拒绝超长与换行；菜单随套餐开关显隐', () => {
    expect(routeState(zhT, serverTop as never).label).toBe('通畅')
    expect(routeState(zhT, clientRoute as never).label).toBe('阻断 · 落地不可用')
    expect(routeState(zhT, { ...serverTop, status: 'blocked', blockedReason: 'unsafe' } as never).label).toBe('阻断 · 出口地址不允许')
    expect(routeState(zhT, { ...serverTop, status: 'blocked', blockedReason: 'quarantined' } as never).label).toBe('阻断 · 下发失败，已暂停')
    expect(routeState(zhT, { ...serverTop, isEnabled: false } as never).label).toBe('已停用')
    expect(entryState(zhT, { state: 'shadowed', coveredBy: 1 })?.label).toBe('被第 1 条覆盖')
    expect(entryState(zhT, { state: 'duplicate', coveredBy: 2 })?.label).toBe('与第 2 条重复')
    expect(entryState(enT, { state: 'shadowed', coveredBy: 3 })?.label).toBe('Overridden by #3')
    expect(entryState(zhT, { state: 'applied', coveredBy: 0 })?.label).toBe('生效')
    expect(entryState(zhT, { state: 'inactive', coveredBy: 0 })).toBeNull()
    expect(isValidChainName('一'.repeat(64))).toBe(true)
    expect(isValidChainName('一'.repeat(65))).toBe(false)
    expect(isValidChainName('a\nb')).toBe(false)
    const labels = (f?: typeof features) => visibleMenu(site as never, f).map((m) => zhT(m.labelKey))
    expect(labels(features)).toContain('链式代理')
    expect(labels({ clientChain: false, customNode: true, serverChain: false })).not.toContain('链式代理')
    expect(labels({ clientChain: false, customNode: false, serverChain: true })).toContain('链式代理')
    expect(labels()).not.toContain('链式代理')
    // 菜单顺序（Ruling-57）：“使用”组因 nodes 排第一而整组提前，组内 chain 次之；没列到的按默认顺序在后；不认识的键忽略
    const ordered = visibleMenu({ ...site, menuOrder: ['nodes', 'unknown', 'chain', 'account'] } as never, features).map((m) => m.key)
    expect(ordered.slice(0, 3)).toEqual(['nodes', 'chain', 'tutorials'])
    expect(ordered.slice(-4)).toEqual(['account', 'invite', 'dashboard', 'announcements'])
  })
})

describe('链路示意（docs/06 链路示意卡）', () => {
  it('起飞节点与落地去重；规则通畅且起飞节点生效才算生效，被覆盖的连线为虚线', () => {
    const flow = buildChainFlow([serverTop, serverWide] as never)
    expect(flow.entries.map((e) => [e.name, e.isActive])).toEqual([['香港 A', true], ['新加坡 D', true]])
    expect(flow.exits.map((e) => [e.name, e.isActive])).toEqual([['东京出口', true], ['自家 VPS', true]])
    expect(flow.links).toEqual([
      { from: 'entry:1', to: 'exit:platform:30', isActive: true },
      { from: 'entry:1', to: 'exit:own:9', isActive: false },
      { from: 'entry:4', to: 'exit:own:9', isActive: true },
    ])
    // 阻断的规则整条不生效
    const blocked = buildChainFlow([clientRoute] as never)
    expect(blocked.entries[0]?.isActive).toBe(false)
    expect(blocked.links[0]?.isActive).toBe(false)
    expect(curvePath(0, 10, 100, 50)).toBe('M 0 10 C 50 10, 50 50, 100 50')
    expect(truncateByWidth('香港 A 节点很长很长', 10)).toBe('香港 A 节…')
    expect(truncateByWidth('short', 10)).toBe('short')
  })

  it('页面底部画出当前页签的链路，切页签跟着换', async () => {
    mockServer({ ...signedIn(overview), 'GET /api/user/dashboard': () => ok({ onlineDevices: 3 }) })
    renderApp('/chain')
    expect(await screen.findByRole('img', { name: '链路示意：2 个起飞节点、2 个落地' })).toBeDefined()
    expect(screen.getByText('链路示意 · 服务端链式')).toBeDefined()
    expect(await screen.findByText('在线 3 台')).toBeDefined()
    fireEvent.click(screen.getByRole('tab', { name: '客户端链式（1 / 20）' }))
    expect(await screen.findByRole('img', { name: '链路示意：1 个起飞节点、1 个落地' })).toBeDefined()
    expect(screen.getByText('链路示意 · 客户端链式')).toBeDefined()
  })
})

describe('链式代理页：服务端链式', () => {
  it('按顺序列出规则，每个起飞节点带自己的状态；有下发中的给说明', async () => {
    mockServer(signedIn(overview))
    renderApp('/chain')
    const list = (await screen.findByText('回家')).closest('ol')
    if (list === null) {
      throw new Error('route list not rendered')
    }
    const top = within(list).getAllByRole('listitem')
    expect(top).toHaveLength(2)
    expect(within(top[0]).getByText('第 1 条')).toBeDefined()
    expect(within(top[0]).getByText('生效')).toBeDefined()
    expect(within(top[0]).getByText('平台出口')).toBeDefined()
    expect(within(top[1]).getByText('被第 1 条覆盖')).toBeDefined()
    expect(within(top[1]).getByText('下发中')).toBeDefined()
    expect(within(top[1]).getByText('我的出口')).toBeDefined()
    expect(screen.getByText(/同一台服务器两次下发至少间隔 60 秒/)).toBeDefined()
    expect(screen.getByRole('tab', { name: '服务端链式（2 / 10）' }).getAttribute('aria-selected')).toBe('true')
    // 第一条不能再上移，最后一条不能再下移
    expect(within(top[0]).getByRole('button', { name: '把第 1 条上移' })).toHaveProperty('disabled', true)
    expect(within(top[1]).getByRole('button', { name: '把第 2 条下移' })).toHaveProperty('disabled', true)
  })

  it('上移提交新的顺序并整体重取；停用提交整条规则', async () => {
    let loads = 0
    const calls = mockServer({
      ...signedIn(overview),
      'GET /api/user/chain': () => {
        loads += 1
        return ok(overview)
      },
      [`PUT ${ROUTES_PATH}/order`]: () => ok([serverWide, serverTop]),
      [`PUT ${ROUTES_PATH}/21`]: () => ok({ ...serverTop, isEnabled: false }),
    })
    renderApp('/chain')
    await screen.findByText('回家')
    fireEvent.click(within(routeItem('回家')).getByRole('button', { name: '把第 2 条上移' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT' && c.path === `${ROUTES_PATH}/order`)).toBe(true))
    const order = calls.find((c) => c.path === `${ROUTES_PATH}/order`)
    expect(order?.csrf).toBe(TEST_CSRF)
    expect(order?.body).toEqual({ kind: 'server', ids: [22, 21] })
    await waitFor(() => expect(loads).toBe(2))

    fireEvent.click(within(routeItem('第 1 条')).getByRole('button', { name: '停用' }))
    await waitFor(() => expect(calls.some((c) => c.path === `${ROUTES_PATH}/21`)).toBe(true))
    expect(calls.find((c) => c.path === `${ROUTES_PATH}/21`)?.body).toEqual({ entryNodeIds: [1], exitKind: 'platform', exitId: 30, name: '', isEnabled: false })
  })

  it('添加规则：起飞节点下拉多选，不可选的置灰写原因；落地分三组；提交后整体重取', async () => {
    let loads = 0
    const base = { ...overview, serverRoutes: [serverTop] }
    const calls = mockServer({
      ...signedIn(base),
      'GET /api/user/chain': () => {
        loads += 1
        return ok(base)
      },
      [`POST ${ROUTES_PATH}`]: () => ok({ ...serverWide, id: 23 }),
    })
    renderApp('/chain')
    await screen.findByText('第 1 条')
    fireEvent.click(screen.getByRole('button', { name: '添加一条' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByRole('button', { name: '添加' })).toHaveProperty('disabled', true)

    // 展开多选：面板不支持的节点不可选
    fireEvent.click(within(dialog).getByRole('button', { name: '起飞节点' }))
    const hy2 = within(dialog).getByRole('checkbox', { name: /日本 Hy2/ })
    expect(hy2).toHaveProperty('disabled', true)
    expect(within(dialog).getByText('（面板不支持）')).toBeDefined()
    fireEvent.click(within(dialog).getByRole('checkbox', { name: /美国 B/ }))
    fireEvent.click(within(dialog).getByRole('checkbox', { name: /新加坡 D/ }))
    expect(within(dialog).getByRole('button', { name: '起飞节点' }).textContent).toContain('美国 B, 新加坡 D')

    // 落地：只列允许服务端的平台出口；已选作起飞、或已是别的服务端规则起飞的节点不可选；hysteria 不出现
    const exitSelect = within(dialog).getByLabelText('落地出口')
    expect(optionTexts(exitSelect)).toEqual(['请选择', '东京出口', '自家 VPS', '香港 A（已作起飞）', '美国 B（已作起飞）', '新加坡 D（已作起飞）'])
    fireEvent.change(exitSelect, { target: { value: 'own:9' } })
    fireEvent.change(within(dialog).getByLabelText('名称（可选）'), { target: { value: ' 回家 ' } })
    fireEvent.click(within(dialog).getByRole('button', { name: '添加' }))

    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.path === ROUTES_PATH)).toBe(true))
    const post = calls.find((c) => c.method === 'POST' && c.path === ROUTES_PATH)
    expect(post?.csrf).toBe(TEST_CSRF)
    expect(post?.body).toEqual({ kind: 'server', entryNodeIds: [2, 4], exitKind: 'own', exitId: 9, name: '回家', isEnabled: true })
    await waitFor(() => expect(loads).toBe(2))
  })

  it('起飞节点可全选（只选可选的项）与取消全选；超过 20 个也能提交（Ruling-87）', async () => {
    const many = Array.from({ length: 25 }, (_, i) => ({ id: 100 + i, name: `节点 ${i + 1}`, region: 'HK', protocol: 'vless', canBeExit: true, canBeServerEntry: true }))
    const base = { ...overview, serverRoutes: [serverTop], nodes: [...nodes, ...many] }
    const calls = mockServer({ ...signedIn(base), [`POST ${ROUTES_PATH}`]: () => ok({ ...serverWide, id: 23 }) })
    renderApp('/chain')
    await screen.findByText('第 1 条')
    fireEvent.click(screen.getByRole('button', { name: '添加一条' }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: '起飞节点' }))
    expect(within(dialog).getByText('已选 0 / 29')).toBeDefined()

    fireEvent.click(within(dialog).getByRole('button', { name: '全选' }))
    expect(within(dialog).getByText('已选 28 / 29')).toBeDefined()
    expect(within(dialog).getByRole('checkbox', { name: /日本 Hy2/ })).toHaveProperty('checked', false)
    expect(within(dialog).getByRole('checkbox', { name: /节点 25/ })).toHaveProperty('checked', true)

    fireEvent.click(within(dialog).getByRole('button', { name: '取消全选' }))
    expect(within(dialog).getByText('已选 0 / 29')).toBeDefined()
    expect(within(dialog).getByRole('checkbox', { name: /香港 A/ })).toHaveProperty('checked', false)

    fireEvent.click(within(dialog).getByRole('button', { name: '全选' }))
    fireEvent.change(within(dialog).getByLabelText('落地出口'), { target: { value: 'own:9' } })
    fireEvent.click(within(dialog).getByRole('button', { name: '添加' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.path === ROUTES_PATH)).toBe(true))
    const body = calls.find((c) => c.method === 'POST')?.body as { entryNodeIds: number[] }
    expect(body.entryNodeIds).toEqual([1, 2, 4, ...many.map((n) => n.id)])
  })

  it('编辑带出原来的起飞节点与落地；删除前确认并写明后果', async () => {
    const calls = mockServer({
      ...signedIn(overview),
      [`PUT ${ROUTES_PATH}/22`]: () => ok(serverWide),
      [`DELETE ${ROUTES_PATH}/22`]: () => ok(null),
    })
    renderApp('/chain')
    await screen.findByText('回家')
    fireEvent.click(within(routeItem('回家')).getByRole('button', { name: '编辑' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByRole('button', { name: '起飞节点' }).textContent).toContain('香港 A, 新加坡 D')
    expect((within(dialog).getByLabelText('落地出口') as HTMLSelectElement).value).toBe('own:9')
    fireEvent.click(within(dialog).getByRole('button', { name: '起飞节点' }))
    fireEvent.click(within(dialog).getByRole('checkbox', { name: /香港 A/ }))
    fireEvent.click(within(dialog).getByRole('button', { name: '保存' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT' && c.path === `${ROUTES_PATH}/22`)).toBe(true))
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({ entryNodeIds: [4], exitKind: 'own', exitId: 9, name: '回家', isEnabled: true })

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    fireEvent.click(within(routeItem('回家')).getByRole('button', { name: '删除' }))
    const confirm = await screen.findByRole('dialog')
    expect(within(confirm).getByText('确定删除第 2 条“回家”？')).toBeDefined()
    expect(within(confirm).getByText(/恢复直接出网/)).toBeDefined()
    fireEvent.click(within(confirm).getByRole('button', { name: '删除' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.path === `${ROUTES_PATH}/22`)).toBe(true))
  })
})

describe('链式代理页：客户端链式与我的出口', () => {
  it('切到客户端页签：列出规则与阻断原因；添加时只列允许客户端的平台出口，起飞节点不受面板限制', async () => {
    const calls = mockServer({ ...signedIn(overview), [`POST ${ROUTES_PATH}`]: () => ok(clientRoute) })
    renderApp('/chain')
    await screen.findByText('第 1 条')
    fireEvent.click(screen.getByRole('tab', { name: '客户端链式（1 / 20）' }))
    expect(await screen.findByText('阻断 · 落地不可用')).toBeDefined()
    expect(screen.getByText(/每个“起飞 → 落地”组合都会出现在订阅里/)).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: '添加一条' }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: '起飞节点' }))
    expect(within(dialog).getByRole('checkbox', { name: /日本 Hy2/ })).toHaveProperty('disabled', false)
    fireEvent.click(within(dialog).getByRole('checkbox', { name: /香港 A/ }))
    expect(optionTexts(within(dialog).getByLabelText('落地出口'))).toEqual(['请选择', '公共出口', '自家 VPS', '香港 A（已作起飞）', '美国 B', '新加坡 D'])
    fireEvent.change(within(dialog).getByLabelText('落地出口'), { target: { value: 'platform:31' } })
    fireEvent.click(within(dialog).getByRole('button', { name: '添加' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')?.body).toEqual({ kind: 'client', entryNodeIds: [1], exitKind: 'platform', exitId: 31, name: '', isEnabled: true })
  })

  it('我的出口：被管理员停用的标出来且不能再选；查看链接才单独请求；删除时提示连带的规则数', async () => {
    const link = 'socks5://203.0.114.5:1080'
    const disabled = { ...overview, exits: [{ ...ownExit, isEnabled: false }] }
    const calls = mockServer({
      ...signedIn(disabled),
      'GET /api/user/chain/exits/9/link': () => ok({ link }),
      'DELETE /api/user/chain/exits/9': () => ok(null),
    })
    renderApp('/chain')
    const card = (await screen.findByText(/我的出口（1 \/ 10）/)).closest('section') as HTMLElement
    expect(within(card).getByText('已被管理员停用')).toBeDefined()
    expect(calls.some((c) => c.path.endsWith('/link'))).toBe(false)
    fireEvent.click(within(card).getByRole('button', { name: '查看链接' }))
    expect(await within(card).findByText(link)).toBeDefined()
    fireEvent.click(within(card).getByRole('button', { name: '隐藏' }))
    expect(within(card).queryByText(link)).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: '添加一条' }))
    const dialog = await screen.findByRole('dialog')
    const exitOption = [...(within(dialog).getByLabelText('落地出口') as HTMLSelectElement).options].find((o) => o.value === 'own:9')
    expect(exitOption?.disabled).toBe(true)
    expect(exitOption?.textContent).toBe('自家 VPS（已被管理员停用）')
    fireEvent.click(within(dialog).getByRole('button', { name: '取消' }))

    fireEvent.click(within(card).getByRole('button', { name: '删除' }))
    const confirm = await screen.findByRole('dialog')
    expect(within(confirm).getByText('有 1 条规则以它为落地，会一并删除。')).toBeDefined()
    fireEvent.click(within(confirm).getByRole('button', { name: '删除' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.path === '/api/user/chain/exits/9')).toBe(true))
  })

  it('我的出口卡里也列本人可用的平台出口：置灰只读、不显示地址、不占名额，引用数按规则数出', async () => {
    mockServer(signedIn(overview))
    renderApp('/chain')
    const card = (await screen.findByText(/我的出口（1 \/ 10）/)).closest('section') as HTMLElement
    const platform = within(card).getByRole('region', { name: '平台提供（2）' })
    expect(within(platform).getByText(/由管理员添加并分配给你/)).toBeDefined()

    const tokyo = within(platform).getByText('东京出口').closest('.hub-chain-exit-readonly') as HTMLElement
    expect(within(tokyo).getByText('vless')).toBeDefined()
    expect(within(tokyo).getByText('平台提供')).toBeDefined()
    expect(within(tokyo).getByText('可用于服务端')).toBeDefined()
    expect(within(tokyo).queryByText('可用于客户端')).toBeNull()
    expect(within(tokyo).getByText('地址不公开 · 被 1 条规则引用')).toBeDefined()
    const shared = within(platform).getByText('公共出口').closest('.hub-chain-exit-readonly') as HTMLElement
    expect(within(shared).getByText('可用于客户端')).toBeDefined()
    expect(within(shared).getByText('地址不公开 · 被 0 条规则引用')).toBeDefined()

    // 查看链接、测试、编辑、删除只对自己的出口有
    expect(within(platform).queryByRole('button')).toBeNull()
    expect(within(card).getAllByRole('button', { name: '删除' })).toHaveLength(1)
  })

  it('没有自己的出口、套餐也不允许添加时：平台出口照常列出，标题名额仍是 0，不显示空状态', async () => {
    mockServer(signedIn({ ...overview, exitAllowed: false, exits: [], serverRoutes: [serverTop], clientRoutes: [] }))
    renderApp('/chain')
    const card = (await screen.findByText(/我的出口（0 \/ 10）/)).closest('section') as HTMLElement
    expect(within(card).getByRole('region', { name: '平台提供（2）' })).toBeDefined()
    expect(within(card).queryByText(/还没有自己的出口/)).toBeNull()
    expect(within(card).getByRole('button', { name: '添加' })).toHaveProperty('disabled', true)
  })

  it('没有平台出口时不出现“平台提供”一段；英文界面有对应文案', async () => {
    mockServer(signedIn({ ...overview, platformExits: [], serverRoutes: [serverWide] }))
    const view = renderApp('/chain')
    const card = (await screen.findByText(/我的出口（1 \/ 10）/)).closest('section') as HTMLElement
    expect(within(card).queryByText(/平台提供/)).toBeNull()
    view.unmount()

    mockServer(signedIn(overview))
    renderApp('/chain', 'en')
    const platform = await screen.findByRole('region', { name: 'Provided by the platform (2)' })
    expect(within(platform).getByText('Address not shown · used by 1 rule(s)')).toBeDefined()
    expect(within(platform).getAllByText('Platform')).toHaveLength(2)
  })

  it('套餐都不允许时给横幅，禁用添加、调顺序与编辑，删除仍可用；菜单不显示', async () => {
    const off = { ...overview, allowed: false, exitAllowed: false, serverAllowed: false }
    mockServer(signedIn(off, { ...session, features: { clientChain: false, customNode: false, serverChain: false } }))
    renderApp('/chain')
    expect(await screen.findByText(/当前套餐不支持链式代理/)).toBeDefined()
    expect(screen.queryByRole('link', { name: /链式代理/ })).toBeNull()
    expect(screen.getByRole('button', { name: '添加一条' })).toHaveProperty('disabled', true)
    expect(screen.getByRole('button', { name: '添加' })).toHaveProperty('disabled', true)
    const item = routeItem('回家')
    expect(within(item).getByRole('button', { name: '把第 2 条上移' })).toHaveProperty('disabled', true)
    expect(within(item).getByRole('button', { name: '编辑' })).toHaveProperty('disabled', true)
    expect(within(item).getByRole('button', { name: '删除' })).toHaveProperty('disabled', false)
  })

  it('英文界面：页签、状态与多选的提示都是英文', async () => {
    mockServer(signedIn(overview))
    renderApp('/chain', 'en')
    expect(await screen.findByRole('tab', { name: 'Server-side (2 / 10)' })).toBeDefined()
    expect(screen.getByText('Overridden by #1')).toBeDefined()
    expect(screen.getByText('Applying')).toBeDefined()
    expect(screen.getByRole('button', { name: 'Move rule #2 up' })).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Add a rule' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Choose entry nodes')).toBeDefined()
  })
})
