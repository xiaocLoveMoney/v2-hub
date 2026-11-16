import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import type { ReactNode } from 'react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Pollable } from '../../hooks/use-polling'
import { PERMISSIONS } from '../../menu'
import { TEST_CSRF, mockServer, ok, renderWith } from '../../test/harness'
import { CREDENTIAL, CREDENTIAL_OPTION, hostOf, serverOf, taskOf } from '../../test/host-fixtures'
import { HostOpsCard } from './HostOpsCard'
import { ServerDetailPage } from './ServerDetailPage'
import { ServersPage } from './ServersPage'

// 轮询间隔缩短，测试用真实计时器等它；默认的 2 秒间隔由 use-polling 的单测覆盖
const { POLL_MS } = vi.hoisted(() => ({ POLL_MS: 20 }))
vi.mock('../../hooks/use-polling', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../hooks/use-polling')>()
  return { ...actual, usePollRemote: (remotes: readonly Pollable[], isActive: boolean) => actual.usePollRemote(remotes, isActive, POLL_MS) }
})

const READ_ONLY = [PERMISSIONS.HOST_READ]
const READ_WRITE = [PERMISSIONS.HOST_READ, PERMISSIONS.HOST_WRITE]
// Popconfirm 的确认按钮：测试里没配语言包时是 OK，配了中文是“确定”
const CONFIRM = /^(OK|确\s*定)$/
const HOST_PATH = '/api/admin/server-hosts/5'
const WRITE_BUTTONS = ['重启面板', '重启内核', '升级 / 修复', '重签令牌', '查看状态与日志', '重置面板登录口令']
const HTTP_URL = 'http://203.0.113.10:2053/p'
const noop = () => undefined

function renderCard(permissions: string[], node: ReactNode) {
  return renderWith(permissions, <MemoryRouter>{node}</MemoryRouter>)
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('HostOpsCard：没绑定主机', () => {
  it('显示说明与“绑定 SSH”；弹窗地址默认取面板地址里的主机名、端口 22，提交后通知刷新并打开任务抽屉', async () => {
    const verify = taskOf({ id: 20, kind: 'verify', status: 'succeeded' })
    const bound = hostOf({ name: 'hk-1', address: 'panel.example.com', lastTask: verify })
    const calls = mockServer({
      'GET /api/admin/ssh-credentials': () => ok([CREDENTIAL]),
      'PUT /api/admin/servers/1/host': () => ok(bound),
      [`GET ${HOST_PATH}/tasks`]: () => ok([verify]),
      'GET /api/admin/host-tasks/20': () => ok({ ...verify, log: 'verify ok' }),
    })
    const onChanged = vi.fn()
    renderCard(READ_WRITE, <HostOpsCard server={serverOf()} onChanged={onChanged} />)
    expect(screen.getByText(/这台服务器还没有绑定 SSH/)).toBeDefined()
    // 没绑定时不去取主机
    expect(calls.some((c) => c.path.startsWith('/api/admin/server-hosts'))).toBe(false)

    fireEvent.click(screen.getByRole('button', { name: '绑定 SSH' }))
    const dialog = await screen.findByRole('dialog')
    expect((within(dialog).getByLabelText('地址') as HTMLInputElement).value).toBe('panel.example.com')
    expect((within(dialog).getByLabelText('SSH 端口') as HTMLInputElement).value).toBe('22')
    fireEvent.mouseDown(within(dialog).getByLabelText('SSH 凭据'))
    fireEvent.click(await screen.findByTitle(CREDENTIAL_OPTION))
    fireEvent.click(within(dialog).getByRole('button', { name: '绑定并校验' }))

    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    const put = calls.find((c) => c.method === 'PUT')
    expect(put?.path).toBe('/api/admin/servers/1/host')
    expect(put?.csrf).toBe(TEST_CSRF)
    expect(put?.body).toEqual({ address: 'panel.example.com', sshPort: 22, credentialId: 3 })
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1))
    expect(await screen.findByText('verify ok')).toBeDefined()
  })

  it('没有 host:write 时只有说明，没有绑定按钮', () => {
    mockServer({})
    renderCard(READ_ONLY, <HostOpsCard server={serverOf()} onChanged={noop} />)
    expect(screen.getByText(/这台服务器还没有绑定 SSH/)).toBeDefined()
    expect(screen.queryByRole('button', { name: '绑定 SSH' })).toBeNull()
  })
})

describe('HostOpsCard：已绑定主机', () => {
  const boundServer = serverOf({ hostId: 5 })

  it('显示主机地址、凭据、最近任务与运维动作；HTTPS 面板没有“配置证书”', async () => {
    mockServer({ [`GET ${HOST_PATH}`]: () => ok(hostOf()) })
    renderCard(READ_WRITE, <HostOpsCard server={boundServer} onChanged={noop} />)
    expect(await screen.findByText('203.0.113.10:22')).toBeDefined()
    expect(screen.getByText('root-key')).toBeDefined()
    expect(screen.getByText('成功')).toBeDefined()
    for (const name of WRITE_BUTTONS) {
      expect(screen.getByRole('button', { name })).toBeDefined()
    }
    expect(screen.queryByRole('button', { name: '配置证书' })).toBeNull()
    expect(screen.queryByRole('button', { name: '绑定 SSH' })).toBeNull()
  })

  it('没有 host:write 时只能查看任务，没有任何动作按钮', async () => {
    mockServer({ [`GET ${HOST_PATH}`]: () => ok(hostOf()) })
    renderCard(READ_ONLY, <HostOpsCard server={serverOf({ hostId: 5, baseUrl: HTTP_URL })} onChanged={noop} />)
    expect(await screen.findByText('203.0.113.10:22')).toBeDefined()
    expect(screen.getByRole('button', { name: '查看任务' })).toBeDefined()
    for (const name of [...WRITE_BUTTONS, '配置证书']) {
      expect(screen.queryByRole('button', { name })).toBeNull()
    }
  })

  it('明文 HTTP 的面板才出现“配置证书”；动作要二次确认并写明影响，确认后发起并打开任务抽屉', async () => {
    const restart = taskOf({ id: 21, kind: 'restart_panel', status: 'succeeded', step: 'run' })
    const calls = mockServer({
      [`GET ${HOST_PATH}`]: () => ok(hostOf()),
      [`POST ${HOST_PATH}/actions`]: () => ok(restart),
      [`GET ${HOST_PATH}/tasks`]: () => ok([restart]),
      'GET /api/admin/host-tasks/21': () => ok({ ...restart, log: 'panel restarted' }),
    })
    renderCard(READ_WRITE, <HostOpsCard server={serverOf({ hostId: 5, baseUrl: HTTP_URL })} onChanged={noop} />)
    expect(await screen.findByRole('button', { name: '配置证书' })).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: '重启面板' }))
    expect(await screen.findByText('会连带重启内核，在线连接会断一下')).toBeDefined()
    expect(calls.some((c) => c.method === 'POST')).toBe(false)
    fireEvent.click(await screen.findByRole('button', { name: CONFIRM }))
    await waitFor(() => {
      const post = calls.find((c) => c.method === 'POST')
      expect(post?.path).toBe(`${HOST_PATH}/actions`)
      expect(post?.csrf).toBe(TEST_CSRF)
      expect(post?.body).toEqual({ action: 'restart_panel' })
    })
    expect(await screen.findByText('panel restarted')).toBeDefined()
  })

  it('重置面板登录口令：结果只在弹窗里显示一次，关闭即丢，不进任何前端存储', async () => {
    const calls = mockServer({
      [`GET ${HOST_PATH}`]: () => ok(hostOf()),
      [`POST ${HOST_PATH}/panel-login/reset`]: () => ok({ username: 'u-7f3a', password: 'p-9c2e' }),
    })
    renderCard(READ_WRITE, <HostOpsCard server={boundServer} onChanged={noop} />)
    fireEvent.click(await screen.findByRole('button', { name: '重置面板登录口令' }))
    expect(await screen.findByText(/原来的面板登录信息作废/)).toBeDefined()
    fireEvent.click(await screen.findByRole('button', { name: CONFIRM }))

    expect(await screen.findByText('p-9c2e')).toBeDefined()
    expect(screen.getByText('u-7f3a')).toBeDefined()
    expect(screen.getByText(/只显示这一次/)).toBeDefined()
    const reset = calls.filter((c) => c.path === `${HOST_PATH}/panel-login/reset`)
    expect(reset).toHaveLength(1)
    expect(reset[0]?.csrf).toBe(TEST_CSRF)
    expect(localStorage.length).toBe(0)
    expect(sessionStorage.length).toBe(0)

    fireEvent.click(screen.getByRole('button', { name: '我已保存' }))
    await waitFor(() => expect(screen.queryByText('p-9c2e')).toBeNull())
    expect(screen.queryByText('u-7f3a')).toBeNull()
    // 关掉后没有再去取，也没有别的入口能再看到
    expect(calls.filter((c) => c.path === `${HOST_PATH}/panel-login/reset`)).toHaveLength(1)
  })

  it('任务没结束时轮询主机且动作不可点；任务结束后通知详情页刷新一次', async () => {
    let current = hostOf({ lastTask: taskOf({ id: 22, kind: 'upgrade', status: 'running', step: 'run', finishedAt: null }) })
    const calls = mockServer({ [`GET ${HOST_PATH}`]: () => ok(current) })
    const onChanged = vi.fn()
    renderCard(READ_WRITE, <HostOpsCard server={boundServer} onChanged={onChanged} />)
    expect(await screen.findByText('执行中')).toBeDefined()
    expect((screen.getByRole('button', { name: '重启面板' }) as HTMLButtonElement).disabled).toBe(true)
    await waitFor(() => expect(calls.filter((c) => c.path === HOST_PATH).length).toBeGreaterThanOrEqual(3))
    expect(onChanged).not.toHaveBeenCalled()

    current = hostOf({ lastTask: taskOf({ id: 22, kind: 'upgrade', status: 'succeeded', step: 'run' }) })
    expect(await screen.findByText('成功')).toBeDefined()
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1))
    expect((screen.getByRole('button', { name: '重启面板' }) as HTMLButtonElement).disabled).toBe(false)
  })
})

describe('明文 HTTP 面板的警告', () => {
  it('服务器列表给 http:// 的面板打“明文 HTTP”标签，https 的没有', async () => {
    mockServer({ 'GET /api/admin/servers': () => ok([serverOf({ id: 1, name: 'plain-1', baseUrl: HTTP_URL }), serverOf({ id: 2, name: 'tls-1' })]) })
    renderCard([PERMISSIONS.SERVER_READ], <ServersPage />)
    const plainRow = (await screen.findByText('plain-1')).closest('tr')
    const tlsRow = screen.getByText('tls-1').closest('tr')
    if (plainRow === null || tlsRow === null) {
      throw new Error('row not rendered')
    }
    expect(within(plainRow).getByText('明文 HTTP')).toBeDefined()
    expect(within(tlsRow).queryByText('明文 HTTP')).toBeNull()
  })

  it('服务器详情页给出警告；有 host:read 才出现 SSH 运维', async () => {
    const handlers = {
      'GET /api/admin/servers/1': () => ok(serverOf({ baseUrl: HTTP_URL })),
      'GET /api/admin/servers/1/inbounds': () => ok([]),
    }
    const page = (
      <MemoryRouter initialEntries={['/servers/1']}>
        <Routes>
          <Route path="/servers/:id" element={<ServerDetailPage />} />
        </Routes>
      </MemoryRouter>
    )
    mockServer(handlers)
    const first = renderWith([PERMISSIONS.NODE_READ], page)
    expect(await screen.findByText(/面板令牌以明文传输，建议在 SSH 运维里“配置证书”/)).toBeDefined()
    expect(screen.queryByText('SSH 运维')).toBeNull()
    first.unmount()

    mockServer(handlers)
    renderWith([PERMISSIONS.NODE_READ, PERMISSIONS.HOST_READ], page)
    expect(await screen.findByText('SSH 运维')).toBeDefined()
    expect(screen.getByText(/这台服务器还没有绑定 SSH/)).toBeDefined()
  })
})
