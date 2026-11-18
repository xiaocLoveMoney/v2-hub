import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { InboundPresetPreview } from '../../api/client'
import { PERMISSIONS } from '../../menu'
import { TEST_CSRF, mockServer, ok, renderWith } from '../../test/harness'
import { serverOf, taskOf } from '../../test/host-fixtures'
import { InboundPresetsModal } from './InboundPresetsModal'
import { ServerDetailPage } from './ServerDetailPage'

const PREVIEW_PATH = '/api/admin/servers/1/inbound-presets/preview'
const DEPLOY_PATH = '/api/admin/servers/1/inbound-presets'
const PRESET_WRITE = [PERMISSIONS.NODE_READ, PERMISSIONS.NODE_WRITE, PERMISSIONS.HOST_READ, PERMISSIONS.HOST_WRITE]
const NO_DNS = '主机没有 DNS 账号，申请不了证书'

function previewOf(over: Partial<InboundPresetPreview> = {}): InboundPresetPreview {
  const keys = ['vless_reality_vision', 'vless_reality_mlkem', 'vless_xhttp_tls', 'vmess_ws_tls', 'trojan_tls', 'shadowsocks_2022', 'hysteria2_salamander'] as const
  const tls = new Set<string>(['vless_xhttp_tls', 'vmess_ws_tls', 'trojan_tls', 'hysteria2_salamander'])
  return {
    hostId: 5,
    domain: 'node.example.com',
    certReady: true,
    items: keys.map((key, i) => ({ key, available: true, reason: '', port: 41001 + i, needsCert: tls.has(key) })),
    ...over,
  }
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('InboundPresetsModal', () => {
  it('默认勾选全部可用的预设与建议端口；改端口、去掉一项、选节点组后提交', async () => {
    const calls = mockServer({
      [`POST ${PREVIEW_PATH}`]: () => ok(previewOf()),
      'GET /api/admin/node-groups': () => ok([{ id: 9, name: 'basic', remark: '', nodeIds: [] }]),
      [`POST ${DEPLOY_PATH}`]: () => ok(taskOf({ id: 30, kind: 'deploy_inbounds', status: 'queued' })),
    })
    const onStarted = vi.fn()
    renderWith(PRESET_WRITE, <InboundPresetsModal serverId={1} isOpen onClose={() => undefined} onStarted={onStarted} />)
    expect(await screen.findByText('Hysteria2 + Salamander')).toBeDefined()
    expect(screen.getByRole('button', { name: /部署 7 个入站/ })).toBeDefined()
    expect(calls.find((c) => c.path === PREVIEW_PATH)?.csrf).toBe(TEST_CSRF)
    expect(screen.getByText(/打开这台服务器的 VLESS 流控/)).toBeDefined()

    fireEvent.click(screen.getByRole('checkbox', { name: /VLESS \+ REALITY \+ ML-KEM/ }))
    fireEvent.change(screen.getByLabelText('Trojan + TCP + TLS 端口'), { target: { value: '8443' } })
    fireEvent.mouseDown(screen.getByRole('combobox'))
    fireEvent.click(await screen.findByText('basic'))
    fireEvent.click(screen.getByRole('button', { name: /部署 6 个入站/ }))

    await waitFor(() => expect(onStarted).toHaveBeenCalled())
    const body = calls.find((c) => c.method === 'POST' && c.path === DEPLOY_PATH)?.body as { items: { key: string; port: number }[]; nodeGroupIds: number[] }
    expect(body.items.map((i) => i.key)).not.toContain('vless_reality_mlkem')
    expect(body.items.find((i) => i.key === 'trojan_tls')?.port).toBe(8443)
    expect(body.items.find((i) => i.key === 'shadowsocks_2022')?.port).toBe(41006)
    expect(body.nodeGroupIds).toEqual([9])
    expect(onStarted.mock.calls[0][1]).toBe(5)
  })

  it('不可用的预设置灰并写原因；主机还没证书时提示会先申请', async () => {
    const preview = previewOf({ certReady: false })
    preview.items = preview.items.map((it) => (it.key === 'trojan_tls' ? { ...it, available: false, reason: NO_DNS } : it))
    mockServer({ [`POST ${PREVIEW_PATH}`]: () => ok(preview), 'GET /api/admin/node-groups': () => ok([]) })
    renderWith(PRESET_WRITE, <InboundPresetsModal serverId={1} isOpen onClose={() => undefined} onStarted={() => undefined} />)
    expect(await screen.findByText(NO_DNS)).toBeDefined()
    expect((screen.getByRole('checkbox', { name: /Trojan \+ TCP \+ TLS/ }) as HTMLInputElement).disabled).toBe(true)
    expect(screen.getByText(/还没有证书，部署时会先申请/)).toBeDefined()
    expect(screen.getByRole('button', { name: /部署 6 个入站/ })).toBeDefined()
  })
})

describe('服务器详情页：一键部署与 VLESS 流控', () => {
  const page = (
    <MemoryRouter initialEntries={['/servers/1']}>
      <Routes>
        <Route path="/servers/:id" element={<ServerDetailPage />} />
      </Routes>
    </MemoryRouter>
  )

  it('没绑 SSH 时按钮置灰；没有 host:write 时不出现', async () => {
    mockServer({ 'GET /api/admin/servers/1': () => ok(serverOf()), 'GET /api/admin/servers/1/inbounds': () => ok([]) })
    const view = renderWith([PERMISSIONS.NODE_READ, PERMISSIONS.NODE_WRITE, PERMISSIONS.HOST_WRITE], page)
    const button = await screen.findByRole('button', { name: '一键部署入站' })
    expect((button as HTMLButtonElement).disabled).toBe(true)
    view.unmount()

    mockServer({ 'GET /api/admin/servers/1': () => ok(serverOf({ hostId: 5 })), 'GET /api/admin/servers/1/inbounds': () => ok([]) })
    renderWith([PERMISSIONS.NODE_READ, PERMISSIONS.NODE_WRITE], page)
    expect(await screen.findByRole('button', { name: /新建入站/ })).toBeDefined()
    expect(screen.queryByRole('button', { name: '一键部署入站' })).toBeNull()
  })

  it('VLESS 流控开关调接口', async () => {
    const calls = mockServer({
      'GET /api/admin/servers/1': () => ok(serverOf({ hostId: 5 })),
      'GET /api/admin/servers/1/inbounds': () => ok([]),
      'PUT /api/admin/servers/1/client-flow': () => ok(serverOf({ hostId: 5, clientFlow: 'xtls-rprx-vision' })),
    })
    renderWith([PERMISSIONS.NODE_READ, PERMISSIONS.SERVER_WRITE], page)
    const toggle = await screen.findByRole('switch', { name: 'VLESS 流控' })
    fireEvent.click(toggle)
    await waitFor(() => expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({ clientFlow: 'xtls-rprx-vision' }))
    expect(await within(document.body).findByText(/已打开 VLESS 流控/)).toBeDefined()
  })
})
