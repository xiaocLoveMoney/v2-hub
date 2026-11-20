import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { PanelNode } from '../../api/client'
import { PERMISSIONS } from '../../menu'
import { TEST_CSRF, mockServer, ok, renderWith } from '../../test/harness'
import { serverOf } from '../../test/host-fixtures'
import { ServerNodeAffixModal } from './NodeAffixModal'
import { NodesPage } from './NodesPage'
import { ServerDetailPage } from './ServerDetailPage'
import { MAX_NODE_NAME_LEN, affixName, charCount, isAffixEmpty, isAffixedNameTooLong, normalizeAffix } from './node-affix'

const NODES_PATH = '/api/admin/nodes'
const AFFIX_PATH = '/api/admin/nodes/affix'
const NODE_WRITE = [PERMISSIONS.NODE_READ, PERMISSIONS.NODE_WRITE]

function nodeOf(over: Partial<PanelNode> = {}): PanelNode {
  return {
    id: 1, inboundId: 10, endpointId: null, serverId: 1, serverName: 'hk-1', protocol: 'vless', address: 'node.example.com', port: 443,
    displayName: 'VLESS-REALITY', region: 'HK', tags: [], sortOrder: 0, isEnabled: true, isMissing: false, groupIds: [], reach: null,
    ...over,
  }
}

const pageOf = (items: PanelNode[]) => ok({ items, total: items.length })

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('node-affix', () => {
  it('前缀去开头的空白、后缀去结尾的空白，中间的分隔空格保留', () => {
    expect(normalizeAffix({ prefix: '  香港 ', suffix: ' | 01  ' })).toEqual({ prefix: '香港 ', suffix: ' | 01' })
    expect(affixName('Trojan-TLS', { prefix: ' 香港 ', suffix: '-01 ' })).toBe('香港 Trojan-TLS-01')
    expect(isAffixEmpty({ prefix: '  ', suffix: ' ' })).toBe(true)
    expect(isAffixEmpty({ prefix: '', suffix: 'x' })).toBe(false)
  })

  it('长度按码点算：😀 算一个，国旗算两个', () => {
    expect(charCount('🇭🇰')).toBe(2)
    const name = '节'.repeat(MAX_NODE_NAME_LEN - 1)
    expect(isAffixedNameTooLong(name, { prefix: '😀', suffix: '' })).toBe(false)
    expect(isAffixedNameTooLong(name, { prefix: 'ab', suffix: '' })).toBe(true)
  })
})

describe('ServerNodeAffixModal', () => {
  it('默认全选并预览新显示名；去掉一个节点后提交选中的节点与整理过的前后缀', async () => {
    const calls = mockServer({
      [`GET ${NODES_PATH}`]: () => pageOf([nodeOf(), nodeOf({ id: 2, displayName: 'Trojan-TLS', port: 8443 })]),
      [`POST ${AFFIX_PATH}`]: () => ok({ updated: 1 }),
    })
    const onDone = vi.fn()
    renderWith(NODE_WRITE, <ServerNodeAffixModal serverId={1} isOpen onClose={() => undefined} onDone={onDone} />)
    expect(await screen.findByText('Trojan-TLS')).toBeDefined()
    expect(calls.find((c) => c.path === NODES_PATH)?.query).toContain('serverId=1')
    const submit = () => screen.getByRole('button', { name: /修改 \d+ 个节点/ }) as HTMLButtonElement
    // 没填前后缀时不能提交
    expect(submit().disabled).toBe(true)

    fireEvent.change(screen.getByLabelText('显示名前缀'), { target: { value: ' 香港 ' } })
    fireEvent.change(screen.getByLabelText('显示名后缀'), { target: { value: '-01 ' } })
    expect(screen.getByText('香港 VLESS-REALITY-01')).toBeDefined()
    expect(screen.getByText('香港 Trojan-TLS-01')).toBeDefined()
    expect(screen.getByRole('button', { name: /修改 2 个节点/ })).toBeDefined()

    // 表头是全选框，第 1、2 行依次是两个节点
    fireEvent.click(screen.getAllByRole('checkbox')[2])
    expect(screen.queryByText('香港 Trojan-TLS-01')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: /修改 1 个节点/ }))

    await waitFor(() => expect(onDone).toHaveBeenCalled())
    const sent = calls.find((c) => c.method === 'POST' && c.path === AFFIX_PATH)
    expect(sent?.csrf).toBe(TEST_CSRF)
    expect(sent?.body).toEqual({ nodeIds: [1], prefix: '香港 ', suffix: '-01' })
  })

  it('有节点加上后超过长度上限时标出并禁止提交', async () => {
    mockServer({ [`GET ${NODES_PATH}`]: () => pageOf([nodeOf({ displayName: 'n'.repeat(MAX_NODE_NAME_LEN - 1) })]) })
    renderWith(NODE_WRITE, <ServerNodeAffixModal serverId={1} isOpen onClose={() => undefined} onDone={() => undefined} />)
    await screen.findByText(/^n+$/)
    fireEvent.change(screen.getByLabelText('显示名前缀'), { target: { value: 'HK-' } })
    expect(screen.getByText(/有 1 个节点加上后超过 128 个字符/)).toBeDefined()
    expect((screen.getByRole('button', { name: /修改 1 个节点/ }) as HTMLButtonElement).disabled).toBe(true)
  })
})

describe('服务器详情页：批量改节点名', () => {
  const page = (
    <MemoryRouter initialEntries={['/servers/1']}>
      <Routes>
        <Route path="/servers/:id" element={<ServerDetailPage />} />
      </Routes>
    </MemoryRouter>
  )
  const inbound = {
    id: 10, serverId: 1, remoteRef: '1', remark: 'VLESS-REALITY', protocol: 'vless', port: 443, network: 'tcp', security: 'reality',
    isEnabled: true, isMissing: false, driftDetectedAt: null, clientCount: 0, endpoints: [],
  }

  it('有 node:write 时出现按钮，点开列出本服务器的节点；只读时没有', async () => {
    mockServer({
      'GET /api/admin/servers/1': () => ok(serverOf()),
      'GET /api/admin/servers/1/inbounds': () => ok([inbound]),
      [`GET ${NODES_PATH}`]: () => pageOf([nodeOf()]),
    })
    const view = renderWith(NODE_WRITE, page)
    fireEvent.click(await screen.findByRole('button', { name: '批量改节点名' }))
    expect(await screen.findByLabelText('显示名前缀')).toBeDefined()
    expect(await screen.findByRole('button', { name: /修改 1 个节点/ })).toBeDefined()
    view.unmount()

    renderWith([PERMISSIONS.NODE_READ], page)
    expect(await screen.findByRole('tab', { name: '入站与节点' })).toBeDefined()
    expect(screen.queryByRole('button', { name: '批量改节点名' })).toBeNull()
  })
})

describe('节点目录：批量改节点名', () => {
  it('勾选节点后打开弹窗，只改勾选的；改完清空勾选并刷新', async () => {
    const calls = mockServer({
      'GET /api/admin/servers': () => ok([serverOf()]),
      'GET /api/admin/node-groups': () => ok([]),
      [`GET ${NODES_PATH}`]: () => pageOf([nodeOf(), nodeOf({ id: 2, displayName: 'Trojan-TLS', port: 8443 })]),
      [`POST ${AFFIX_PATH}`]: () => ok({ updated: 1 }),
    })
    renderWith(NODE_WRITE, <NodesPage />)
    await screen.findByText('Trojan-TLS')
    const open = () => screen.getByRole('button', { name: /批量改节点名/ }) as HTMLButtonElement
    expect(open().disabled).toBe(true)

    // 表头是全选框，第 2 行是 Trojan-TLS
    fireEvent.click(screen.getAllByRole('checkbox')[2])
    expect(open().textContent).toContain('（1）')
    fireEvent.click(open())
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('显示名后缀'), { target: { value: ' | 日本' } })
    expect(within(dialog).getByText('Trojan-TLS | 日本')).toBeDefined()
    const listed = calls.filter((c) => c.path === NODES_PATH).length
    fireEvent.click(within(dialog).getByRole('button', { name: /修改 1 个节点/ }))

    await waitFor(() => expect(calls.find((c) => c.method === 'POST' && c.path === AFFIX_PATH)?.body).toEqual({ nodeIds: [2], prefix: '', suffix: ' | 日本' }))
    await waitFor(() => expect(calls.filter((c) => c.path === NODES_PATH).length).toBeGreaterThan(listed))
    await waitFor(() => expect(open().disabled).toBe(true))
  })

  it('只读时没有勾选框与按钮', async () => {
    mockServer({
      'GET /api/admin/servers': () => ok([serverOf()]),
      'GET /api/admin/node-groups': () => ok([]),
      [`GET ${NODES_PATH}`]: () => pageOf([nodeOf()]),
    })
    renderWith([PERMISSIONS.NODE_READ], <NodesPage />)
    await screen.findByText('VLESS-REALITY')
    expect(screen.queryByRole('button', { name: /批量改节点名/ })).toBeNull()
    expect(screen.queryAllByRole('checkbox')).toHaveLength(0)
  })
})
