import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { App as AntApp, ConfigProvider } from 'antd'
import type { ReactNode } from 'react'
import { RouterProvider, createMemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setCsrfToken } from '../../api/client'
import { SessionContext, type SessionContextValue } from '../../auth/session-context'
import { PERMISSIONS } from '../../menu'
import { NodesPage } from './NodesPage'
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
})

describe('ServerDetailPage', () => {
  it('显示漂移与缺失标记；按模板新建入站只提交模板参数，不含任何密钥', async () => {
    const inbound = {
      id: 10, serverId: 1, remoteRef: '3', protocol: 'vless', port: 443, listen: '', tag: 't', remark: 'reality', isEnabled: true,
      network: 'tcp', security: 'reality', coreType: 'xray', config: {}, clientCount: 0, driftDetectedAt: '2026-11-12T00:00:00Z',
      isMissing: false, endpoints: [], createdAt: '2026-11-12T00:00:00Z',
    }
    const calls = mockServer({
      'GET /api/admin/servers/1': () => ok({ ...server, status: 'online', lastError: '' }),
      'GET /api/admin/servers/1/inbounds': () => ok([inbound, { ...inbound, id: 11, remoteRef: '4', remark: 'gone', isMissing: true, driftDetectedAt: null }]),
      'POST /api/admin/servers/1/inbounds': () => ok({ ...inbound, id: 12 }),
    })
    renderWith(<ServerDetailPage />, '/servers/1', '/servers/:id')
    expect(await screen.findByText('面板侧有改动')).toBeDefined()
    expect(screen.getByText('面板上已不存在')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: /新建入站/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('备注'), { target: { value: 'new-reality' } })
    fireEvent.change(within(dialog).getByLabelText('端口'), { target: { value: '8443' } })
    fireEvent.change(within(dialog).getByLabelText('伪装目标'), { target: { value: 'www.cloudflare.com:443' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /创\s*建/ }))

    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    const post = calls.find((c) => c.method === 'POST')
    expect(post?.body).toMatchObject({ remark: 'new-reality', port: 8443, template: 'vless-reality', templateParams: { target: 'www.cloudflare.com:443' } })
    expect(JSON.stringify(post?.body)).not.toContain('privateKey')
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
