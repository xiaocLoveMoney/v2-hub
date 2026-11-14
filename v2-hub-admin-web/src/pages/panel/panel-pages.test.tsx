import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { App as AntApp, ConfigProvider } from 'antd'
import type { ReactNode } from 'react'
import { RouterProvider, createMemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setCsrfToken } from '../../api/client'
import { SessionContext, type SessionContextValue } from '../../auth/session-context'
import { PERMISSIONS } from '../../menu'
import { NodesPage } from './NodesPage'
import { ChainExitsPage } from './ChainExitsPage'
import { ChainRoutesPage } from './ChainRoutesPage'
import { ServerDetailPage } from './ServerDetailPage'
import { ServersPage } from './ServersPage'

const CSRF = 'csrf-test-token'
const ALL = [PERMISSIONS.SERVER_READ, PERMISSIONS.SERVER_WRITE, PERMISSIONS.NODE_READ, PERMISSIONS.NODE_WRITE]
const ok = (data: unknown) => Response.json({ code: '0', message: 'ok', data })

const server = {
  id: 1, name: 'hk-1', providerType: 'xui', baseUrl: 'https://panel.example.com:2053/p', tlsMode: 'verify', certPinSha256: null,
  region: 'HK', remark: '', isDisabled: false, status: 'auth_failed', lastError: '面板拒绝了 API token', lastSeenAt: null,
  lastSyncedAt: null, panelVersion: '3.11.0', statusSnapshot: null, inboundCount: 1, createdAt: '2026-11-12T00:00:00Z',
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

function renderWith(node: ReactNode, path = '/', routePath = '/') {
  const granted = new Set<string>(ALL)
  const value: SessionContextValue = {
    status: 'authenticated',
    session: { admin: { id: 1, username: 'root' }, permissions: ALL, csrfToken: CSRF, totpEnabled: false },
    login: async () => null,
    logout: async () => undefined,
    refresh: async () => undefined,
    can: (p) => granted.has(p),
  }
  setCsrfToken(CSRF)
  const router = createMemoryRouter([{ path: routePath, element: node }], { initialEntries: [path] })
  return render(
    <ConfigProvider>
      <AntApp>
        <SessionContext.Provider value={value}>
          <RouterProvider router={router} />
        </SessionContext.Provider>
      </AntApp>
    </ConfigProvider>,
  )
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('ServersPage', () => {
  it('列表显示状态与原因；接入表单提交正确内容并带 CSRF 头', async () => {
    const calls = mockServer({
      'GET /api/admin/servers': () => ok([server]),
      'POST /api/admin/servers': () => ok({ ...server, id: 2, name: 'jp-1', status: 'online', lastError: '' }),
    })
    renderWith(<ServersPage />)
    expect(await screen.findByText('token 失效')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: /接入服务器/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('名称'), { target: { value: 'jp-1' } })
    fireEvent.change(within(dialog).getByLabelText('面板地址'), { target: { value: 'https://jp.example.com:2053/x' } })
    fireEvent.change(within(dialog).getByLabelText('API token'), { target: { value: 'tok' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /接\s*入/ }))

    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.path === '/api/admin/servers')).toBe(true))
    const post = calls.find((c) => c.method === 'POST' && c.path === '/api/admin/servers')
    expect(post?.csrf).toBe(CSRF)
    expect(post?.body).toMatchObject({ name: 'jp-1', providerType: 'xui', baseUrl: 'https://jp.example.com:2053/x', tlsMode: 'verify', certPinSha256: '' })
  })

  it('连续打开表单时不带出上一次的值：编辑 A → 编辑 B → 接入', async () => {
    const other = { ...server, id: 2, name: 'jp-1', baseUrl: 'https://jp.example.com:2053/b' }
    mockServer({ 'GET /api/admin/servers': () => ok([server, other]) })
    renderWith(<ServersPage />)
    await screen.findByText('hk-1')
    const openDialog = async (trigger: HTMLElement) => {
      fireEvent.click(trigger)
      const dialogs = await screen.findAllByRole('dialog')
      return dialogs[dialogs.length - 1]
    }
    const valueOf = (dialog: HTMLElement, label: string) => (within(dialog).getByLabelText(label) as HTMLInputElement).value
    const closeDialog = (dialog: HTMLElement) => fireEvent.click(within(dialog).getByRole('button', { name: /close/i }))
    const editButtons = () => screen.getAllByRole('button', { name: /编\s*辑/ })

    let dialog = await openDialog(editButtons()[0])
    expect(valueOf(dialog, '名称')).toBe('hk-1')
    fireEvent.change(within(dialog).getByLabelText('API token'), { target: { value: 'typed-for-hk' } })
    closeDialog(dialog)

    dialog = await openDialog(editButtons()[1])
    expect(valueOf(dialog, '名称')).toBe('jp-1')
    expect(valueOf(dialog, '面板地址')).toBe('https://jp.example.com:2053/b')
    expect(valueOf(dialog, 'API token')).toBe('')
    closeDialog(dialog)

    dialog = await openDialog(screen.getByRole('button', { name: /接入服务器/ }))
    expect(valueOf(dialog, '名称')).toBe('')
    expect(valueOf(dialog, '面板地址')).toBe('')
    expect(valueOf(dialog, 'API token')).toBe('')
  })
})

describe('ServerDetailPage', () => {
  it('显示漂移与缺失标记；新建入站与 3x-ui 同构：切到 Reality 由面板生成密钥，提交完整配置', async () => {
    const inbound = {
      id: 10, serverId: 1, remoteRef: '3', protocol: 'vless', port: 443, listen: '', tag: 't', remark: 'reality', isEnabled: true,
      network: 'tcp', security: 'reality', coreType: 'xray', config: {}, clientCount: 0, driftDetectedAt: '2026-11-12T00:00:00Z',
      isMissing: false, endpoints: [], createdAt: '2026-11-12T00:00:00Z',
    }
    const calls = mockServer({
      'GET /api/admin/servers/1': () => ok({ ...server, status: 'online', lastError: '' }),
      'GET /api/admin/servers/1/inbounds': () => ok([inbound, { ...inbound, id: 11, remoteRef: '4', remark: 'gone', isMissing: true, driftDetectedAt: null }]),
      'POST /api/admin/servers/1/keys': () => ok({ privateKey: 'PANEL-PRIV', publicKey: 'PANEL-PUB' }),
      'POST /api/admin/servers/1/inbounds': () => ok({ ...inbound, id: 12 }),
    })
    renderWith(<ServerDetailPage />, '/servers/1', '/servers/:id')
    expect(await screen.findByText('面板侧有改动')).toBeDefined()
    expect(screen.getByText('面板上已不存在')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: /新建入站/ }))
    const dialog = await screen.findByRole('dialog')
    for (const tab of ['基础配置', '协议', '传输', '安全', '嗅探', '高级配置']) {
      expect(within(dialog).getByRole('tab', { name: tab })).toBeDefined()
    }
    fireEvent.change(within(dialog).getByLabelText('备注'), { target: { value: 'new-reality' } })
    fireEvent.change(within(dialog).getByLabelText('端口'), { target: { value: '8443' } })
    fireEvent.click(within(dialog).getByRole('tab', { name: '安全' }))
    fireEvent.click(await within(dialog).findByRole('radio', { name: 'Reality' }))
    await waitFor(() => expect((within(dialog).getByLabelText('私钥') as HTMLInputElement).value).toBe('PANEL-PRIV'))
    fireEvent.change(within(dialog).getByLabelText('目标'), { target: { value: 'www.cloudflare.com:443' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /创\s*建/ }))

    await waitFor(() => expect(calls.some((c) => c.path === '/api/admin/servers/1/inbounds' && c.method === 'POST')).toBe(true))
    expect(calls.find((c) => c.path === '/api/admin/servers/1/keys')?.body).toEqual({ kind: 'x25519' })
    const post = calls.find((c) => c.path === '/api/admin/servers/1/inbounds' && c.method === 'POST')
    expect(post?.csrf).toBe(CSRF)
    expect(post?.body).toMatchObject({
      remark: 'new-reality', port: 8443, protocol: 'vless', enabled: true,
      config: {
        settings: { decryption: 'none' },
        streamSettings: { network: 'tcp', security: 'reality', realitySettings: { target: 'www.cloudflare.com:443', privateKey: 'PANEL-PRIV', settings: { publicKey: 'PANEL-PUB' } } },
        sniffing: { enabled: false },
      },
    })
  })

  it('编辑入站时从面板读出完整配置（含私钥）回显，保存提交修改后的配置', async () => {
    const inbound = {
      id: 10, serverId: 1, remoteRef: '3', protocol: 'shadowsocks', port: 443, listen: '', tag: 't', remark: 'ss', isEnabled: true,
      network: 'tcp', security: 'none', coreType: 'xray', config: {}, clientCount: 0, driftDetectedAt: null,
      isMissing: false, endpoints: [], createdAt: '2026-11-12T00:00:00Z',
    }
    const panelConfig = {
      protocol: 'shadowsocks', port: 443, listen: '', remark: 'ss', enabled: true,
      config: { settings: { method: '2022-blake3-aes-256-gcm', password: 'PANEL-KEY', network: 'tcp,udp' }, streamSettings: { network: 'tcp', security: 'none', tcpSettings: {} }, sniffing: {} },
    }
    const calls = mockServer({
      'GET /api/admin/servers/1': () => ok({ ...server, status: 'online', lastError: '' }),
      'GET /api/admin/servers/1/inbounds': () => ok([inbound]),
      'GET /api/admin/inbounds/10/panel-config': () => ok(panelConfig),
      'PUT /api/admin/inbounds/10': () => ok(inbound),
    })
    renderWith(<ServerDetailPage />, '/servers/1', '/servers/:id')
    fireEvent.click(await screen.findByRole('button', { name: /编\s*辑/ }))
    const dialog = await screen.findByRole('dialog')
    await waitFor(() => expect((within(dialog).getByLabelText('备注') as HTMLInputElement).value).toBe('ss'))
    fireEvent.click(within(dialog).getByRole('tab', { name: '协议' }))
    expect((await within(dialog).findByLabelText('服务端密钥') as HTMLInputElement).value).toBe('PANEL-KEY')
    fireEvent.change(within(dialog).getByLabelText('服务端密钥'), { target: { value: 'NEW-KEY' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /保\s*存/ }))

    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    expect(calls.find((c) => c.method === 'PUT')?.body).toMatchObject({ remark: 'ss', port: 443, config: { settings: { password: 'NEW-KEY', method: '2022-blake3-aes-256-gcm' } } })
  })

  it('点对外地址的 × 先二次确认，确认后只删这一个地址', async () => {
    const endpoint = { id: 7, address: 'cdn-a.example.com', port: 443, remark: '', isDisabled: false, isMissing: false }
    const inbound = {
      id: 10, serverId: 1, remoteRef: '3', protocol: 'vless', port: 443, listen: '', tag: 't', remark: 'reality', isEnabled: true,
      network: 'tcp', security: 'reality', coreType: 'xray', config: {}, clientCount: 0, driftDetectedAt: null,
      isMissing: false, endpoints: [endpoint, { ...endpoint, id: 8, address: 'cdn-b.example.com' }], createdAt: '2026-11-12T00:00:00Z',
    }
    const calls = mockServer({
      'GET /api/admin/servers/1': () => ok({ ...server, status: 'online', lastError: '' }),
      'GET /api/admin/servers/1/inbounds': () => ok([inbound]),
      'DELETE /api/admin/endpoints/7': () => ok(null),
    })
    renderWith(<ServerDetailPage />, '/servers/1', '/servers/:id')
    const tag = await screen.findByText('cdn-a.example.com:443')
    fireEvent.click(within(tag.closest('.ant-tag') as HTMLElement).getAllByLabelText(/close/i)[0])

    expect(await screen.findByText('删除对外地址 cdn-a.example.com:443？')).toBeDefined()
    expect(screen.getByText('只删除入站「reality」上的这个地址，其它地址保留')).toBeDefined()
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false)

    fireEvent.click(screen.getByRole('button', { name: /确\s*定|OK/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE')).toBe(true))
    const del = calls.filter((c) => c.method === 'DELETE')
    expect(del).toHaveLength(1)
    expect(del[0].path).toBe('/api/admin/endpoints/7')
    expect(del[0].csrf).toBe(CSRF)
  })

  it('新建入站弹窗关闭后立刻再打开，不带出上一次填的值', async () => {
    mockServer({
      'GET /api/admin/servers/1': () => ok({ ...server, status: 'online', lastError: '' }),
      'GET /api/admin/servers/1/inbounds': () => ok([]),
    })
    renderWith(<ServerDetailPage />, '/servers/1', '/servers/:id')
    const openButton = await screen.findByRole('button', { name: /新建入站/ })
    await waitFor(() => expect((openButton as HTMLButtonElement).disabled).toBe(false))

    fireEvent.click(openButton)
    let dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('备注'), { target: { value: 'draft-remark' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /close/i }))

    fireEvent.click(openButton)
    const dialogs = await screen.findAllByRole('dialog')
    dialog = dialogs[dialogs.length - 1]
    expect((within(dialog).getByLabelText('备注') as HTMLInputElement).value).toBe('')
  })
})

describe('NodesPage', () => {
  it('节点目录显示 Host 节点与所属组；新建节点组提交成员', async () => {
    const node = {
      id: 5, inboundId: 10, endpointId: 7, serverId: 1, serverName: 'hk-1', protocol: 'vless', address: 'cdn.example.com', port: 8443,
      displayName: '香港 CDN', region: 'HK', tags: ['premium'], sortOrder: 0, isEnabled: true, isMissing: false, groupIds: [3],
    }
    const calls = mockServer({
      'GET /api/admin/servers': () => ok([server]),
      'GET /api/admin/node-groups': () => ok([{ id: 3, name: 'basic', remark: '', nodeIds: [5] }]),
      'GET /api/admin/nodes': () => ok({ items: [node], total: 1 }),
      'POST /api/admin/node-groups': () => ok({ id: 4, name: 'vip', remark: '', nodeIds: [] }),
    })
    renderWith(<NodesPage />)
    expect(await screen.findByText('cdn.example.com:8443（Host）')).toBeDefined()
    expect(await screen.findByText('basic')).toBeDefined()

    fireEvent.click(screen.getByRole('tab', { name: '节点组' }))
    fireEvent.click(await screen.findByRole('button', { name: /新建节点组/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('名称'), { target: { value: 'vip' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /保\s*存/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.path === '/api/admin/node-groups')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')?.body).toEqual({ name: 'vip', remark: '', nodeIds: [] })
  })
})

describe('服务端链式（docs/03 §5.19）', () => {
  const applyFailed = {
    serverId: 1, isSupported: true, status: 'failed', ruleCount: 2, appliedRuleCount: 1, failCount: 3, isRetryStopped: true,
    lastError: '重启后 Xray 未能运行，已回滚', lastAttemptAt: '2026-11-14T00:00:00Z', nextAttemptAt: null, appliedAt: '2026-11-13T00:00:00Z',
  }

  it('服务器详情显示下发状态与最近错误；停止自动重试后可点重试，带 CSRF 头', async () => {
    const retried = { ...applyFailed, status: 'pending', failCount: 0, isRetryStopped: false, lastError: '' }
    let current: unknown = applyFailed
    const calls = mockServer({
      'GET /api/admin/servers/1': () => ok({ ...server, status: 'online', lastError: '' }),
      'GET /api/admin/servers/1/inbounds': () => ok([]),
      'GET /api/admin/servers/1/chain-apply': () => ok(current),
      'POST /api/admin/servers/1/chain-apply/retry': () => {
        current = retried
        return ok(retried)
      },
    })
    renderWith(<ServerDetailPage />, '/servers/1', '/servers/:id')
    expect(await screen.findByText('服务端链式下发')).toBeDefined()
    expect(screen.getByText('下发失败')).toBeDefined()
    expect(screen.getByText('1 / 2')).toBeDefined()
    expect(screen.getByText(/已停止自动重试/)).toBeDefined()
    expect(screen.getByText('重启后 Xray 未能运行，已回滚')).toBeDefined()
    // antd 会在两个汉字的按钮文字中间插入空格
    fireEvent.click(screen.getByRole('button', { name: /重\s?试/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.path === '/api/admin/servers/1/chain-apply/retry')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')?.csrf).toBe(CSRF)
    expect(await screen.findByText('待下发')).toBeDefined()
  })

  it('面板不支持时只给说明', async () => {
    mockServer({
      'GET /api/admin/servers/1': () => ok({ ...server, status: 'online', lastError: '' }),
      'GET /api/admin/servers/1/inbounds': () => ok([]),
      'GET /api/admin/servers/1/chain-apply': () => ok({ ...applyFailed, isSupported: false, status: 'none' }),
    })
    renderWith(<ServerDetailPage />, '/servers/1', '/servers/:id')
    expect(await screen.findByText(/不支持改内核配置/)).toBeDefined()
    expect(screen.queryByText('服务端链式下发')).toBeNull()
  })

  it('全站规则列表按起飞节点显示状态，换起飞服务器过滤后重新拉取', async () => {
    const route = {
      id: 5, user: { id: 7, username: 'alice' }, kind: 'server', position: 1, name: '',
      entries: [
        { nodeId: 3, name: 'hk-1 · a', serverId: 1, serverName: 'hk-1', state: 'applied', coveredBy: 0 },
        { nodeId: 4, name: 'hk-1 · b', serverId: 1, serverName: 'hk-1', state: 'shadowed', coveredBy: 2 },
      ],
      exit: { kind: 'platform', id: 9, name: '东京出口', protocol: 'vless' },
      isEnabled: true, status: 'ok', blockedReason: '', createdAt: '2026-11-14T00:00:00Z',
    }
    const blocked = {
      ...route, id: 6, kind: 'client', position: 2, status: 'blocked', blockedReason: 'unsafe',
      entries: [{ nodeId: 3, name: 'hk-1 · a', serverId: 1, serverName: 'hk-1', state: 'inactive', coveredBy: 0 }],
      exit: { kind: 'own', id: 12, name: '家宽', protocol: 'socks' },
    }
    const calls = mockServer({
      'GET /api/admin/servers': () => ok([server]),
      'GET /api/admin/chain-routes': () => ok({ items: [route, blocked], total: 2 }),
    })
    renderWith(<ChainRoutesPage />)
    expect(await screen.findAllByText('alice #7')).toHaveLength(2)
    expect(screen.getByText('生效')).toBeDefined()
    expect(screen.getByText('被覆盖（第 2 条）')).toBeDefined()
    expect(screen.getByText('通畅')).toBeDefined()
    expect(screen.getByText('阻断 · 出口地址不允许')).toBeDefined()
    expect(screen.getByText('平台出口')).toBeDefined()
    expect(screen.getByText('用户出口')).toBeDefined()
    fireEvent.mouseDown(screen.getByRole('combobox', { name: '按起飞服务器过滤' }))
    fireEvent.click(await screen.findByTitle('hk-1'))
    await waitFor(() => expect(calls.filter((c) => c.path === '/api/admin/chain-routes')).toHaveLength(2))
  })
})

describe('链式出口', () => {
  // 链接里的 UUID 是示例值，不对应任何真实节点
  const LINK = 'vless://11111111-1111-4111-8111-111111111111@exit.example.net:443?type=tcp&security=tls#tokyo'
  const platform = {
    id: 9, owner: 'platform', user: null, name: '东京出口', protocol: 'vless', address: 'exit.example.net', port: 443, remark: '',
    isEnabled: true, allowServer: true, allowClient: false, nodeGroups: [{ id: 2, name: 'basic' }], routeCount: 0,
    createdAt: '2026-11-14T00:00:00Z', updatedAt: '2026-11-14T00:00:00Z',
  }
  const own = {
    ...platform, id: 12, owner: 'user', user: { id: 7, username: 'alice' }, name: '家宽', protocol: 'socks', address: '203.0.114.20', port: 1080,
    nodeGroups: [], routeCount: 2,
  }
  const handlers = () => ({
    'GET /api/admin/node-groups': () => ok([{ id: 2, name: 'basic', remark: '', nodeIds: [], createdAt: '2026-11-14T00:00:00Z' }]),
    'GET /api/admin/chain-exits': () => ok({ items: [own, platform], total: 2 }),
  })

  it('平台出口可编辑与查看链接；用户出口只有停用与删除，删除前写明连带的规则数', async () => {
    const calls = mockServer({
      ...handlers(),
      'GET /api/admin/chain-exits/9/link': () => ok({ link: LINK }),
      'PUT /api/admin/chain-exits/12/enabled': () => ok({ ...own, isEnabled: false }),
      'DELETE /api/admin/chain-exits/12': () => ok(null),
    })
    renderWith(<ChainExitsPage />)
    const ownRow = (await screen.findByText('家宽')).closest('tr')
    const platformRow = screen.getByText('东京出口').closest('tr')
    if (ownRow === null || platformRow === null) {
      throw new Error('rows not rendered')
    }
    expect(within(ownRow).getByText('alice #7')).toBeDefined()
    expect(within(ownRow).queryByRole('button', { name: '编 辑' })).toBeNull()
    expect(within(ownRow).queryByRole('button', { name: '查看链接' })).toBeNull()
    expect(within(platformRow).getByText('服务端')).toBeDefined()
    expect(within(platformRow).queryByText('客户端')).toBeNull()

    // 链接点了才取
    expect(calls.some((c) => c.path === '/api/admin/chain-exits/9/link')).toBe(false)
    fireEvent.click(within(platformRow).getByRole('button', { name: '查看链接' }))
    expect(await screen.findByText(LINK)).toBeDefined()

    fireEvent.click(within(ownRow).getByRole('button', { name: '停 用' }))
    fireEvent.click(await screen.findByRole('button', { name: /确\s*定|OK/ }))
    await waitFor(() => {
      const call = calls.find((c) => c.method === 'PUT' && c.path === '/api/admin/chain-exits/12/enabled')
      expect(call?.csrf).toBe(CSRF)
      expect(call?.body).toEqual({ isEnabled: false })
    })

    fireEvent.click(within(ownRow).getByRole('button', { name: '删 除' }))
    expect(await screen.findByText('会连带删除引用它的 2 条规则。')).toBeDefined()
  })

  it('新建平台出口提交表单；修改时链接留空不提交', async () => {
    const calls = mockServer({
      ...handlers(),
      'POST /api/admin/chain-exits': () => ok(platform),
      'PUT /api/admin/chain-exits/9': () => ok(platform),
    })
    renderWith(<ChainExitsPage />)
    await screen.findByText('东京出口')
    fireEvent.click(screen.getByRole('button', { name: /新建平台出口/ }))
    fireEvent.change(await screen.findByLabelText('名称'), { target: { value: '大阪出口' } })
    fireEvent.change(screen.getByLabelText('链接'), { target: { value: LINK } })
    fireEvent.click(screen.getByRole('button', { name: '创 建' }))
    await waitFor(() => {
      const call = calls.find((c) => c.method === 'POST' && c.path === '/api/admin/chain-exits')
      expect(call?.csrf).toBe(CSRF)
      expect(call?.body).toEqual({ name: '大阪出口', link: LINK, remark: '', isEnabled: true, allowServer: true, allowClient: false, nodeGroupIds: [] })
    })

    const platformRow = screen.getByText('东京出口').closest('tr')
    if (platformRow === null) {
      throw new Error('row not rendered')
    }
    fireEvent.click(within(platformRow).getByRole('button', { name: '编 辑' }))
    fireEvent.click(await screen.findByRole('button', { name: '保 存' }))
    await waitFor(() => {
      const call = calls.find((c) => c.method === 'PUT' && c.path === '/api/admin/chain-exits/9')
      expect(call?.body).toEqual({ name: '东京出口', remark: '', isEnabled: true, allowServer: true, allowClient: false, nodeGroupIds: [2] })
    })
  })
})
