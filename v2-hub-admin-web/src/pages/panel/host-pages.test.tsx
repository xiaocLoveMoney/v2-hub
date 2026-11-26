import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import type { ReactNode } from 'react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Pollable } from '../../hooks/use-polling'
import { PERMISSIONS } from '../../menu'
import { CONFIRM, TEST_CSRF, mockServer, ok, renderWith } from '../../test/harness'
import { CREDENTIAL, CREDENTIAL_OPTION, DNS_ACCOUNT, DNS_ACCOUNT_OPTION, QUIET_MS, hostOf, sleep, taskOf } from '../../test/host-fixtures'
import { DnsAccountsModal } from './DnsAccountsModal'
import { HostDetectionResult } from './HostDetectionResult'
import { HostTaskDrawer } from './HostTaskDrawer'
import { HostsPage } from './HostsPage'
import { SshCredentialsModal } from './SshCredentialsModal'

// 轮询间隔缩短，测试用真实计时器等它；默认的 2 秒间隔由 use-polling 的单测覆盖
const { POLL_MS } = vi.hoisted(() => ({ POLL_MS: 20 }))
vi.mock('../../hooks/use-polling', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../hooks/use-polling')>()
  return { ...actual, usePollRemote: (remotes: readonly Pollable[], isActive: boolean) => actual.usePollRemote(remotes, isActive, POLL_MS) }
})

const READ_ONLY = [PERMISSIONS.HOST_READ]
const READ_WRITE = [PERMISSIONS.HOST_READ, PERMISSIONS.HOST_WRITE, PERMISSIONS.NODE_READ]
const HOSTS_PATH = '/api/admin/server-hosts'
const CREDENTIALS_PATH = '/api/admin/ssh-credentials'
const DNS_ACCOUNTS_PATH = '/api/admin/dns-accounts'
const conflict = (message: string) => Response.json({ code: 'conflict', message }, { status: 409 })
const noop = () => undefined

const failedTask = taskOf({ id: 10, hostId: 6, status: 'failed', step: 'register', errorCode: 'panel_unreachable', lastError: 'dial tcp: i/o timeout' })
const linkedHost = hostOf()
const freshHost = hostOf({ id: 6, name: 'jp-01', address: '2001:db8::6', serverId: null, serverName: '', osId: '', arch: '', lastTask: failedTask })

function renderPage(permissions: string[], node: ReactNode) {
  return renderWith(permissions, <MemoryRouter>{node}</MemoryRouter>)
}

function rowOf(text: string): HTMLElement {
  const row = screen.getByText(text).closest('tr')
  if (row === null) {
    throw new Error(`row not rendered: ${text}`)
  }
  return row
}

async function lastDialog(): Promise<HTMLElement> {
  const dialogs = await screen.findAllByRole('dialog')
  return dialogs[dialogs.length - 1]
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('HostsPage', () => {
  it('列出主机：地址与端口、凭据、系统与架构、关联服务器、最近任务与失败原因说明', async () => {
    const calls = mockServer({
      [`GET ${HOSTS_PATH}`]: () => ok([linkedHost, freshHost]),
      [`GET ${HOSTS_PATH}/6/tasks`]: () => ok([failedTask]),
      'GET /api/admin/host-tasks/10': () => ok({ ...failedTask, log: 'probe failed' }),
    })
    renderPage(READ_WRITE, <HostsPage />)
    expect(await screen.findByText('203.0.113.10:22')).toBeDefined()
    expect(screen.getByText('[2001:db8::6]:22')).toBeDefined()
    expect(screen.getAllByText('root-key')).toHaveLength(2)
    expect(screen.getByText('debian / amd64')).toBeDefined()
    expect(screen.getByRole('link', { name: 'hk-01' }).getAttribute('href')).toBe('/servers/1')
    expect(within(rowOf('jp-01')).getByText('未接入')).toBeDefined()
    expect(within(rowOf('jp-01')).getByText('失败')).toBeDefined()
    expect(within(rowOf('jp-01')).getByText(/面板端口从平台连不上，请检查防火墙或云厂商安全组后重新部署/)).toBeDefined()
    expect(within(rowOf('203.0.113.10:22')).getByText('成功')).toBeDefined()
    // 部署只对还没关联服务器的主机出现
    expect(screen.getAllByRole('button', { name: '重新部署' })).toHaveLength(1)
    expect(screen.getByRole('button', { name: /新建主机/ })).toBeDefined()
    expect(screen.getByRole('button', { name: 'SSH 凭据' })).toBeDefined()

    // 点最近任务打开任务抽屉并选中那条
    fireEvent.click(within(rowOf('jp-01')).getByRole('button', { name: '部署' }))
    expect(await screen.findByText('probe failed')).toBeDefined()
    expect(calls.some((c) => c.path === `${HOSTS_PATH}/6/tasks`)).toBe(true)
  })

  it('没有 host:write 时不出现任何写按钮，只能查看任务与凭据；没有 node:read 时关联服务器不是链接', async () => {
    mockServer({ [`GET ${HOSTS_PATH}`]: () => ok([linkedHost, freshHost]) })
    renderPage(READ_ONLY, <HostsPage />)
    expect(await screen.findByText('203.0.113.10:22')).toBeDefined()
    expect(screen.getAllByRole('button', { name: '查看任务' })).toHaveLength(2)
    expect(screen.getByRole('button', { name: 'SSH 凭据' })).toBeDefined()
    for (const name of [/新建主机/, /编\s*辑/, /删\s*除/, '重置主机密钥', '重新部署']) {
      expect(screen.queryByRole('button', { name })).toBeNull()
    }
    expect(screen.queryByRole('link', { name: 'hk-01' })).toBeNull()
  })

  it('删除要二次确认并说明只删平台记录；确认后带 CSRF 头', async () => {
    const calls = mockServer({
      [`GET ${HOSTS_PATH}`]: () => ok([freshHost]),
      [`DELETE ${HOSTS_PATH}/6`]: () => ok(null),
    })
    renderPage(READ_WRITE, <HostsPage />)
    await screen.findByText('jp-01')
    fireEvent.click(screen.getByRole('button', { name: /删\s*除/ }))
    expect(await screen.findByText(/只删除平台里的 SSH 记录与任务历史，不动这台机器/)).toBeDefined()
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false)
    fireEvent.click(await screen.findByRole('button', { name: CONFIRM }))
    await waitFor(() => expect(calls.find((c) => c.method === 'DELETE' && c.path === `${HOSTS_PATH}/6`)?.csrf).toBe(TEST_CSRF))
  })

  it('重新部署发 provision 动作，并打开任务抽屉看进度', async () => {
    const queued = taskOf({ id: 11, hostId: 6, status: 'succeeded' })
    const calls = mockServer({
      [`GET ${HOSTS_PATH}`]: () => ok([freshHost]),
      [`POST ${HOSTS_PATH}/6/actions`]: () => ok(queued),
      [`GET ${HOSTS_PATH}/6/tasks`]: () => ok([queued, failedTask]),
      'GET /api/admin/host-tasks/11': () => ok({ ...queued, log: 'adopt done' }),
    })
    renderPage(READ_WRITE, <HostsPage />)
    await screen.findByText('jp-01')
    fireEvent.click(screen.getByRole('button', { name: '重新部署' }))
    fireEvent.click(await screen.findByRole('button', { name: CONFIRM }))
    await waitFor(() => {
      const post = calls.find((c) => c.method === 'POST' && c.path === `${HOSTS_PATH}/6/actions`)
      expect(post?.csrf).toBe(TEST_CSRF)
      expect(post?.body).toEqual({ action: 'provision' })
    })
    expect(await screen.findByText('adopt done')).toBeDefined()
  })

  it('有主机的最近任务没结束时轮询列表，结束即停；都结束时不轮询', async () => {
    let current = hostOf({ lastTask: taskOf({ status: 'running', step: 'install', finishedAt: null }) })
    const calls = mockServer({ [`GET ${HOSTS_PATH}`]: () => ok([current]) })
    const listCalls = () => calls.filter((c) => c.path === HOSTS_PATH).length
    renderPage(READ_ONLY, <HostsPage />)
    expect(await screen.findByText('执行中')).toBeDefined()
    await waitFor(() => expect(listCalls()).toBeGreaterThanOrEqual(3))

    current = hostOf()
    expect(await screen.findByText('成功')).toBeDefined()
    // 页面显示“成功”到定时器被清掉之间还可能多发一次，先等这一次落地再记数
    await sleep(QUIET_MS)
    const settled = listCalls()
    await sleep(QUIET_MS)
    expect(listCalls()).toBe(settled)
  })

  it('新建主机：清单逐行校验并指出行号；测试连接只测第一行；提交整批', async () => {
    const calls = mockServer({
      [`GET ${HOSTS_PATH}`]: () => ok([]),
      [`GET ${CREDENTIALS_PATH}`]: () => ok([CREDENTIAL]),
      [`POST ${HOSTS_PATH}/test`]: () =>
        ok({ hostKeySha256: 'SHA256:probe-example', osId: 'debian', arch: 'amd64', isPrivileged: true, panelState: 'standard', panelVersion: 'v3.11.0', plan: 'adopt', reason: '' }),
      [`POST ${HOSTS_PATH}`]: () => ok([]),
    })
    renderPage(READ_WRITE, <HostsPage />)
    fireEvent.click(await screen.findByRole('button', { name: /新建主机/ }))
    const dialog = await screen.findByRole('dialog')
    expect((within(dialog).getByLabelText('SSH 端口') as HTMLInputElement).value).toBe('22')
    fireEvent.mouseDown(within(dialog).getByLabelText('SSH 凭据'))
    fireEvent.click(await screen.findByTitle(CREDENTIAL_OPTION))

    const list = within(dialog).getByLabelText('主机清单')
    fireEvent.change(list, { target: { value: '203.0.113.20 sg-01\n203.0.113.21:22 sg-02' } })
    fireEvent.click(within(dialog).getByRole('button', { name: '新建并部署' }))
    expect(await within(dialog).findByText(/第 2 行：/)).toBeDefined()
    expect(calls.some((c) => c.method === 'POST')).toBe(false)

    fireEvent.change(list, { target: { value: '203.0.113.20 sg-01\nsg2.example.com' } })
    fireEvent.click(within(dialog).getByRole('button', { name: '测试连接' }))
    expect(await within(dialog).findByText('SHA256:probe-example')).toBeDefined()
    expect(within(dialog).getByText('接管')).toBeDefined()
    expect(within(dialog).getByText('标准安装的面板（v3.11.0）')).toBeDefined()
    const test = calls.find((c) => c.path === `${HOSTS_PATH}/test`)
    expect(test?.csrf).toBe(TEST_CSRF)
    expect(test?.body).toEqual({ address: '203.0.113.20', sshPort: 22, credentialId: 3, providerType: 'xui' })

    fireEvent.click(within(dialog).getByRole('button', { name: '新建并部署' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.path === HOSTS_PATH)).toBe(true))
    const post = calls.find((c) => c.method === 'POST' && c.path === HOSTS_PATH)
    expect(post?.csrf).toBe(TEST_CSRF)
    expect(post?.body).toEqual({
      credentialId: 3,
      sshPort: 22,
      providerType: 'xui',
      panelPort: null,
      dnsAccountId: null,
      hosts: [
        { address: '203.0.113.20', name: 'sg-01' },
        { address: 'sg2.example.com', name: 'sg2.example.com' },
      ],
    })
  })

  it('新建主机时选了 DNS 账号：整批带上它，域名交给平台生成', async () => {
    const calls = mockServer({
      [`GET ${HOSTS_PATH}`]: () => ok([]),
      [`GET ${CREDENTIALS_PATH}`]: () => ok([CREDENTIAL]),
      [`GET ${DNS_ACCOUNTS_PATH}`]: () => ok([DNS_ACCOUNT]),
      [`POST ${HOSTS_PATH}`]: () => ok([]),
    })
    renderPage(READ_WRITE, <HostsPage />)
    fireEvent.click(await screen.findByRole('button', { name: /新建主机/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.mouseDown(within(dialog).getByLabelText('SSH 凭据'))
    fireEvent.click(await screen.findByTitle(CREDENTIAL_OPTION))
    fireEvent.mouseDown(within(dialog).getByLabelText('DNS 账号'))
    fireEvent.click(await screen.findByTitle(DNS_ACCOUNT_OPTION))
    fireEvent.change(within(dialog).getByLabelText('主机清单'), { target: { value: '203.0.113.20 sg-01' } })
    fireEvent.click(within(dialog).getByRole('button', { name: '新建并部署' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.path === HOSTS_PATH)).toBe(true))
    expect(calls.find((c) => c.method === 'POST' && c.path === HOSTS_PATH)?.body).toMatchObject({
      dnsAccountId: 7,
      hosts: [{ address: '203.0.113.20', name: 'sg-01' }],
    })
  })

  it('列表显示域名与证书到期日；没申请到证书的写明，没配 DNS 账号的留空', async () => {
    const issued = hostOf({ dnsAccountId: 7, dnsAccountName: 'cf-main', domain: 'hk-01.example.com', certNotAfter: '2027-02-14T08:00:00Z' })
    const pending = hostOf({ id: 8, name: 'us-01', address: '203.0.113.30', dnsAccountId: 7, dnsAccountName: 'cf-main', domain: 'us-01.example.com' })
    mockServer({ [`GET ${HOSTS_PATH}`]: () => ok([issued, pending, freshHost]) })
    renderPage(READ_ONLY, <HostsPage />)
    expect(within(await screen.findByText('hk-01.example.com').then(() => rowOf('hk-01.example.com'))).getByText('证书到期 2027-02-14')).toBeDefined()
    expect(within(rowOf('us-01.example.com')).getByText('还没申请到证书')).toBeDefined()
    expect(within(rowOf('jp-01')).queryByText(/证书/)).toBeNull()
  })

  it('编辑主机：选了 DNS 账号才出现域名；清掉账号时域名一并清空提交', async () => {
    const withDomain = hostOf({ dnsAccountId: 7, dnsAccountName: 'cf-main', domain: 'hk-01.example.com', lastTask: null })
    const calls = mockServer({
      [`GET ${HOSTS_PATH}`]: () => ok([withDomain]),
      [`GET ${CREDENTIALS_PATH}`]: () => ok([CREDENTIAL]),
      [`GET ${DNS_ACCOUNTS_PATH}`]: () => ok([DNS_ACCOUNT]),
      [`PUT ${HOSTS_PATH}/5`]: () => ok(withDomain),
    })
    renderPage(READ_WRITE, <HostsPage />)
    fireEvent.click(await screen.findByRole('button', { name: /编\s*辑/ }))
    const dialog = await screen.findByRole('dialog')
    const domain = (await within(dialog).findByLabelText('域名')) as HTMLInputElement
    expect(domain.value).toBe('hk-01.example.com')
    fireEvent.change(domain, { target: { value: ' HK1.example.com ' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /保\s*存/ }))
    await waitFor(() => expect(calls.filter((c) => c.method === 'PUT')).toHaveLength(1))
    expect(calls.find((c) => c.method === 'PUT')?.body).toMatchObject({ dnsAccountId: 7, domain: 'HK1.example.com' })
  })

  it('没有凭据时新建主机的弹窗提示先去新建', async () => {
    mockServer({ [`GET ${HOSTS_PATH}`]: () => ok([]), [`GET ${CREDENTIALS_PATH}`]: () => ok([]) })
    renderPage(READ_WRITE, <HostsPage />)
    fireEvent.click(await screen.findByRole('button', { name: /新建主机/ }))
    expect(await screen.findByText(/还没有 SSH 凭据/)).toBeDefined()
  })
})

describe('HostDetectionResult', () => {
  it('不能部署时显示原因大类的说明', () => {
    renderPage(
      READ_ONLY,
      <HostDetectionResult
        address="203.0.113.30"
        detection={{ hostKeySha256: 'SHA256:probe-example', osId: 'alpine', arch: 'arm64', isPrivileged: false, panelState: 'none', panelVersion: '', plan: '', reason: 'no_root' }}
      />,
    )
    expect(screen.getByText('不能部署')).toBeDefined()
    expect(screen.getByText('没有 root 权限')).toBeDefined()
    expect(screen.getByText(/登录用户不是 root，也不能免密 sudo/)).toBeDefined()
  })
})

describe('SshCredentialsModal', () => {
  const KEY = 'example-key-text'

  it('编辑时不回显私钥与口令；留空提交的请求体不带这两个字段，填了才带', async () => {
    const calls = mockServer({
      [`GET ${CREDENTIALS_PATH}`]: () => ok([CREDENTIAL]),
      [`PUT ${CREDENTIALS_PATH}/3`]: () => ok(CREDENTIAL),
    })
    const onChanged = vi.fn()
    renderPage(READ_WRITE, <SshCredentialsModal isOpen canWrite onClose={noop} onChanged={onChanged} />)
    expect(await screen.findByText('SHA256:fingerprint-example')).toBeDefined()
    expect(screen.getByText('2 台')).toBeDefined()
    expect(screen.getByText('私钥')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: /编\s*辑/ }))
    let form = await lastDialog()
    const keyInput = within(form).getByLabelText('私钥', { selector: 'textarea' }) as HTMLTextAreaElement
    expect(keyInput.value).toBe('')
    expect(keyInput.placeholder).toContain('留空 = 沿用原值')
    expect((within(form).getByLabelText('私钥口令') as HTMLInputElement).value).toBe('')
    fireEvent.change(within(form).getByLabelText('备注'), { target: { value: 'rotated' } })
    fireEvent.click(within(form).getByRole('button', { name: /保\s*存/ }))
    await waitFor(() => expect(calls.filter((c) => c.method === 'PUT')).toHaveLength(1))
    const first = calls.find((c) => c.method === 'PUT')
    expect(first?.csrf).toBe(TEST_CSRF)
    expect(first?.body).toEqual({ name: 'root-key', username: 'root', authType: 'key', remark: 'rotated' })
    await waitFor(() => expect(onChanged).toHaveBeenCalled())

    // 再次打开仍是空的；这次填上新私钥
    fireEvent.click(screen.getByRole('button', { name: /编\s*辑/ }))
    form = await lastDialog()
    expect((within(form).getByLabelText('私钥', { selector: 'textarea' }) as HTMLTextAreaElement).value).toBe('')
    fireEvent.change(within(form).getByLabelText('私钥', { selector: 'textarea' }), { target: { value: KEY } })
    fireEvent.click(within(form).getByRole('button', { name: /保\s*存/ }))
    await waitFor(() => expect(calls.filter((c) => c.method === 'PUT')).toHaveLength(2))
    const second = calls.filter((c) => c.method === 'PUT')[1]
    expect(second?.body).toMatchObject({ secret: KEY })
    expect(second?.body).not.toHaveProperty('passphrase')
  })

  it('编辑时换了认证方式就必须重新填，不填不提交', async () => {
    const calls = mockServer({ [`GET ${CREDENTIALS_PATH}`]: () => ok([CREDENTIAL]) })
    renderPage(READ_WRITE, <SshCredentialsModal isOpen canWrite onClose={noop} onChanged={noop} />)
    fireEvent.click(await screen.findByRole('button', { name: /编\s*辑/ }))
    const form = await lastDialog()
    fireEvent.click(within(form).getByRole('radio', { name: '密码' }))
    await waitFor(() => expect(within(form).queryByLabelText('私钥口令')).toBeNull())
    fireEvent.click(within(form).getByRole('button', { name: /保\s*存/ }))
    expect(await within(form).findByText('请输入密码')).toBeDefined()
    expect(calls.some((c) => c.method === 'PUT')).toBe(false)
  })

  it('新建必须填私钥；提交内容带 CSRF 头', async () => {
    const calls = mockServer({
      [`GET ${CREDENTIALS_PATH}`]: () => ok([]),
      [`POST ${CREDENTIALS_PATH}`]: () => ok(CREDENTIAL),
    })
    renderPage(READ_WRITE, <SshCredentialsModal isOpen canWrite onClose={noop} onChanged={noop} />)
    fireEvent.click(await screen.findByRole('button', { name: /新建凭据/ }))
    const form = await lastDialog()
    fireEvent.change(within(form).getByLabelText('名称'), { target: { value: 'ops-key' } })
    fireEvent.click(within(form).getByRole('button', { name: /新\s*建/ }))
    expect(await within(form).findByText('请输入私钥')).toBeDefined()
    expect(calls.some((c) => c.method === 'POST')).toBe(false)

    fireEvent.change(within(form).getByLabelText('私钥', { selector: 'textarea' }), { target: { value: KEY } })
    fireEvent.click(within(form).getByRole('button', { name: /新\s*建/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    const post = calls.find((c) => c.method === 'POST')
    expect(post?.csrf).toBe(TEST_CSRF)
    expect(post?.body).toEqual({ name: 'ops-key', username: 'root', authType: 'key', remark: '', secret: KEY })
  })

  it('删除被引用的凭据时把后端的 409 说明提示出来', async () => {
    mockServer({
      [`GET ${CREDENTIALS_PATH}`]: () => ok([CREDENTIAL]),
      [`DELETE ${CREDENTIALS_PATH}/3`]: () => conflict('凭据仍被 2 台主机引用，不能删除'),
    })
    renderPage(READ_WRITE, <SshCredentialsModal isOpen canWrite onClose={noop} onChanged={noop} />)
    fireEvent.click(await screen.findByRole('button', { name: /删\s*除/ }))
    fireEvent.click(await screen.findByRole('button', { name: CONFIRM }))
    expect(await screen.findByText('凭据仍被 2 台主机引用，不能删除')).toBeDefined()
  })

  it('没有 host:write 时只能看列表', async () => {
    mockServer({ [`GET ${CREDENTIALS_PATH}`]: () => ok([CREDENTIAL]) })
    renderPage(READ_ONLY, <SshCredentialsModal isOpen canWrite={false} onClose={noop} onChanged={noop} />)
    expect(await screen.findByText('root-key')).toBeDefined()
    for (const name of [/新建凭据/, /编\s*辑/, /删\s*除/]) {
      expect(screen.queryByRole('button', { name })).toBeNull()
    }
  })
})

describe('DnsAccountsModal', () => {
  const TOKEN = 'example-token-text'

  it('列出账号；编辑时不回显令牌，留空提交的请求体不带 token，被引用时根域名不能改', async () => {
    const calls = mockServer({
      [`GET ${DNS_ACCOUNTS_PATH}`]: () => ok([DNS_ACCOUNT]),
      [`PUT ${DNS_ACCOUNTS_PATH}/7`]: () => ok(DNS_ACCOUNT),
    })
    const onChanged = vi.fn()
    renderPage(READ_WRITE, <DnsAccountsModal isOpen canWrite onClose={noop} onChanged={onChanged} />)
    expect(await screen.findByText('example.com')).toBeDefined()
    expect(screen.getByText('Cloudflare')).toBeDefined()
    expect(screen.getByText('1 台')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: /编\s*辑/ }))
    const form = await lastDialog()
    const token = within(form).getByLabelText('API 令牌') as HTMLInputElement
    expect(token.value).toBe('')
    expect(token.placeholder).toContain('留空 = 沿用原值')
    expect((within(form).getByLabelText('根域名') as HTMLInputElement).disabled).toBe(true)
    fireEvent.change(within(form).getByLabelText('备注'), { target: { value: '主域名' } })
    fireEvent.click(within(form).getByRole('button', { name: /保\s*存/ }))
    await waitFor(() => expect(calls.filter((c) => c.method === 'PUT')).toHaveLength(1))
    const put = calls.find((c) => c.method === 'PUT')
    expect(put?.csrf).toBe(TEST_CSRF)
    expect(put?.body).toEqual({ name: 'cf-main', providerType: 'cloudflare', zone: 'example.com', remark: '主域名' })
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
  })

  it('新建必须填令牌与合法的根域名；后端校验令牌失败时把原因显示出来', async () => {
    const calls = mockServer({
      [`GET ${DNS_ACCOUNTS_PATH}`]: () => ok([]),
      [`POST ${DNS_ACCOUNTS_PATH}`]: () => Response.json({ code: 'upstream_unavailable', message: 'Cloudflare 拒绝了令牌' }, { status: 502 }),
    })
    renderPage(READ_WRITE, <DnsAccountsModal isOpen canWrite onClose={noop} onChanged={noop} />)
    fireEvent.click(await screen.findByRole('button', { name: /新建 DNS 账号/ }))
    const form = await lastDialog()
    fireEvent.change(within(form).getByLabelText('名称'), { target: { value: 'cf' } })
    fireEvent.change(within(form).getByLabelText('根域名'), { target: { value: 'not a zone' } })
    fireEvent.click(within(form).getByRole('button', { name: /新\s*建/ }))
    expect(await within(form).findByText('请输入 API 令牌')).toBeDefined()
    expect(within(form).getByText('请输入根域名，如 example.com')).toBeDefined()
    expect(calls.some((c) => c.method === 'POST')).toBe(false)

    fireEvent.change(within(form).getByLabelText('根域名'), { target: { value: 'example.com' } })
    fireEvent.change(within(form).getByLabelText('API 令牌'), { target: { value: TOKEN } })
    fireEvent.click(within(form).getByRole('button', { name: /新\s*建/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')?.body).toEqual({ name: 'cf', providerType: 'cloudflare', zone: 'example.com', remark: '', token: TOKEN })
    expect(await within(form).findByText('Cloudflare 拒绝了令牌')).toBeDefined()
  })

  it('没有 host:write 时只能看列表', async () => {
    mockServer({ [`GET ${DNS_ACCOUNTS_PATH}`]: () => ok([DNS_ACCOUNT]) })
    renderPage(READ_ONLY, <DnsAccountsModal isOpen canWrite={false} onClose={noop} onChanged={noop} />)
    expect(await screen.findByText('cf-main')).toBeDefined()
    for (const name of [/新建 DNS 账号/, /编\s*辑/, /删\s*除/]) {
      expect(screen.queryByRole('button', { name })).toBeNull()
    }
  })
})

describe('HostTaskDrawer', () => {
  const HOST = { id: 6, name: 'jp-01' }
  const TASKS_PATH = `${HOSTS_PATH}/6/tasks`

  it('显示步骤、发起人、错误大类说明、失败摘要与纯文本日志', async () => {
    const log = 'connect ok\n<img src=x onerror=alert(1)>\nprobe https://203.0.113.9:2053 failed'
    mockServer({
      [`GET ${TASKS_PATH}`]: () => ok([failedTask]),
      'GET /api/admin/host-tasks/10': () => ok({ ...failedTask, log }),
    })
    renderPage(READ_WRITE, <HostTaskDrawer host={HOST} onClose={noop} onChanged={noop} />)
    const drawer = await screen.findByRole('dialog')
    expect(await within(drawer).findByText(/面板端口从平台连不上/)).toBeDefined()
    expect(within(drawer).getByText('dial tcp: i/o timeout')).toBeDefined()
    expect(within(drawer).getByText('接入平台')).toBeDefined()
    expect(within(drawer).getAllByText('root').length).toBeGreaterThan(0)
    const pre = within(drawer).getByLabelText('任务日志')
    expect(pre.tagName).toBe('PRE')
    expect(pre.textContent).toBe(log)
    // 日志里的标签只是文本，没有变成元素
    expect(pre.querySelector('img')).toBeNull()
    expect(within(drawer).queryByRole('button', { name: '取消任务' })).toBeNull()
  })

  it('排队中的任务会轮询，可以取消；取消后停止轮询并通知外面', async () => {
    let status: 'queued' | 'canceled' = 'queued'
    const current = () => taskOf({ id: 11, hostId: 6, status, step: '', startedAt: null, finishedAt: null })
    const calls = mockServer({
      [`GET ${TASKS_PATH}`]: () => ok([current()]),
      'GET /api/admin/host-tasks/11': () => ok({ ...current(), log: '' }),
      'POST /api/admin/host-tasks/11/cancel': () => {
        status = 'canceled'
        return ok(current())
      },
    })
    const detailCalls = () => calls.filter((c) => c.path === '/api/admin/host-tasks/11').length
    const onChanged = vi.fn()
    renderPage(READ_WRITE, <HostTaskDrawer host={HOST} initialTaskId={11} onClose={noop} onChanged={onChanged} />)
    const cancel = await screen.findByRole('button', { name: '取消任务' })
    expect(screen.getByText('未开始')).toBeDefined()
    expect(screen.getByText('（暂无日志）')).toBeDefined()
    await waitFor(() => expect(detailCalls()).toBeGreaterThanOrEqual(3))
    expect(onChanged).not.toHaveBeenCalled()

    fireEvent.click(cancel)
    await waitFor(() => expect(calls.find((c) => c.path === '/api/admin/host-tasks/11/cancel')?.csrf).toBe(TEST_CSRF))
    await waitFor(() => expect(screen.queryByRole('button', { name: '取消任务' })).toBeNull())
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    await sleep(QUIET_MS)
    const settled = detailCalls()
    await sleep(QUIET_MS)
    expect(detailCalls()).toBe(settled)
  })

  it('没有 host:write 时排队中的任务也没有取消按钮', async () => {
    const queued = taskOf({ id: 11, hostId: 6, status: 'queued', step: '', startedAt: null, finishedAt: null })
    mockServer({
      [`GET ${TASKS_PATH}`]: () => ok([queued]),
      'GET /api/admin/host-tasks/11': () => ok({ ...queued, log: '' }),
    })
    renderPage(READ_ONLY, <HostTaskDrawer host={HOST} onClose={noop} onChanged={noop} />)
    expect(await screen.findByText('（暂无日志）')).toBeDefined()
    expect(screen.queryByRole('button', { name: '取消任务' })).toBeNull()
  })
})
