import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import type { ReactNode } from 'react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { PERMISSIONS } from '../../menu'
import { CONFIRM, TEST_CSRF, mockServer, ok, renderWith } from '../../test/harness'
import { CREDENTIAL, CREDENTIAL_OPTION, PASSWORD_CREDENTIAL, PASSWORD_CREDENTIAL_OPTION, hostOf, serverOf } from '../../test/host-fixtures'
import { HostBindModal } from './HostBindModal'
import { HostsPage } from './HostsPage'

// 密码凭据的确认（Ruling-97，docs/03 §5.22）：所选凭据是 password 类型时显示“再次输入该凭据的密码”并随请求提交

const READ_WRITE = [PERMISSIONS.HOST_READ, PERMISSIONS.HOST_WRITE, PERMISSIONS.NODE_READ]
const HOSTS_PATH = '/api/admin/server-hosts'
const CREDENTIALS_PATH = '/api/admin/ssh-credentials'
const SECRET_LABEL = '再次输入该凭据的密码'
const SECRET_REQUIRED = '请输入该凭据的密码'
/** 再次输入的密码（仅测试用）。 */
const typed = 'typed-again'
const DETECTION = { hostKeySha256: 'SHA256:probe-example', osId: 'debian', arch: 'amd64', isPrivileged: true, panelState: 'none', panelVersion: '', plan: 'install', reason: '' }
const noop = () => undefined

function renderPage(node: ReactNode) {
  return renderWith(READ_WRITE, <MemoryRouter>{node}</MemoryRouter>)
}

function rowOf(text: string): HTMLElement {
  const row = screen.getByText(text).closest('tr')
  if (row === null) {
    throw new Error(`row not rendered: ${text}`)
  }
  return row
}

async function pickCredential(dialog: HTMLElement, option: string) {
  fireEvent.mouseDown(within(dialog).getByLabelText('SSH 凭据'))
  const matches = await screen.findAllByTitle(option)
  fireEvent.click(matches[matches.length - 1])
}

function secretInput(dialog: HTMLElement): HTMLInputElement {
  return within(dialog).getByLabelText(SECRET_LABEL) as HTMLInputElement
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('新建主机（批量）与连接测试', () => {
  it('私钥凭据不出现该栏；密码凭据总是要求，测试连接与新建都带上，提交后清空', async () => {
    const calls = mockServer({
      [`GET ${HOSTS_PATH}`]: () => ok([]),
      [`GET ${CREDENTIALS_PATH}`]: () => ok([CREDENTIAL, PASSWORD_CREDENTIAL]),
      [`POST ${HOSTS_PATH}/test`]: () => ok(DETECTION),
      [`POST ${HOSTS_PATH}`]: () => ok([]),
    })
    renderPage(<HostsPage />)
    fireEvent.click(await screen.findByRole('button', { name: /新建主机/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('主机清单'), { target: { value: '203.0.113.20 sg-01' } })

    await pickCredential(dialog, CREDENTIAL_OPTION)
    expect(within(dialog).queryByLabelText(SECRET_LABEL)).toBeNull()

    await pickCredential(dialog, PASSWORD_CREDENTIAL_OPTION)
    const input = await within(dialog).findByLabelText(SECRET_LABEL)
    expect(input.getAttribute('type')).toBe('password')
    expect(input.getAttribute('autocomplete')).toBe('off')

    // 不填不能测试
    fireEvent.click(within(dialog).getByRole('button', { name: '测试连接' }))
    expect(await within(dialog).findByText(SECRET_REQUIRED)).toBeDefined()
    expect(calls.some((c) => c.path === `${HOSTS_PATH}/test`)).toBe(false)

    fireEvent.change(secretInput(dialog), { target: { value: typed } })
    fireEvent.click(within(dialog).getByRole('button', { name: '测试连接' }))
    await waitFor(() => expect(calls.some((c) => c.path === `${HOSTS_PATH}/test`)).toBe(true))
    expect(calls.find((c) => c.path === `${HOSTS_PATH}/test`)?.body).toEqual({
      address: '203.0.113.20',
      sshPort: 22,
      credentialId: PASSWORD_CREDENTIAL.id,
      providerType: 'xui',
      credentialSecret: typed,
    })
    await waitFor(() => expect(secretInput(dialog).value).toBe(''))

    fireEvent.change(secretInput(dialog), { target: { value: typed } })
    fireEvent.click(within(dialog).getByRole('button', { name: '新建并部署' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.path === HOSTS_PATH)).toBe(true))
    const post = calls.find((c) => c.method === 'POST' && c.path === HOSTS_PATH)
    expect(post?.csrf).toBe(TEST_CSRF)
    expect(post?.body).toMatchObject({ credentialId: PASSWORD_CREDENTIAL.id, credentialSecret: typed, hosts: [{ address: '203.0.113.20', name: 'sg-01' }] })
  })
})

describe('修改主机', () => {
  const passwordHost = hostOf({ credentialId: PASSWORD_CREDENTIAL.id, credentialName: PASSWORD_CREDENTIAL.name, lastTask: null })

  it('密码凭据：凭据、地址、端口都没变时不要求，请求体不带 credentialSecret', async () => {
    const calls = mockServer({
      [`GET ${HOSTS_PATH}`]: () => ok([passwordHost]),
      [`GET ${CREDENTIALS_PATH}`]: () => ok([CREDENTIAL, PASSWORD_CREDENTIAL]),
      [`PUT ${HOSTS_PATH}/5`]: () => ok(passwordHost),
    })
    renderPage(<HostsPage />)
    fireEvent.click(await screen.findByRole('button', { name: /编\s*辑/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('名称'), { target: { value: 'hk-renamed' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /保\s*存/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    expect(within(dialog).queryByLabelText(SECRET_LABEL)).toBeNull()
    expect(calls.find((c) => c.method === 'PUT')?.body).not.toHaveProperty('credentialSecret')
  })

  it('密码凭据：改了端口就要求再输一次密码并提交；私钥凭据改地址也不要求', async () => {
    const calls = mockServer({
      [`GET ${HOSTS_PATH}`]: () => ok([passwordHost]),
      [`GET ${CREDENTIALS_PATH}`]: () => ok([CREDENTIAL, PASSWORD_CREDENTIAL]),
      [`PUT ${HOSTS_PATH}/5`]: () => ok(passwordHost),
    })
    renderPage(<HostsPage />)
    fireEvent.click(await screen.findByRole('button', { name: /编\s*辑/ }))
    const dialog = await screen.findByRole('dialog')
    // 凭据列表取到后才知道是密码类型
    await within(dialog).findByTitle(PASSWORD_CREDENTIAL_OPTION)
    expect(within(dialog).queryByLabelText(SECRET_LABEL)).toBeNull()
    fireEvent.change(within(dialog).getByLabelText('SSH 端口'), { target: { value: '2222' } })
    expect(await within(dialog).findByLabelText(SECRET_LABEL)).toBeDefined()

    fireEvent.click(within(dialog).getByRole('button', { name: /保\s*存/ }))
    expect(await within(dialog).findByText(SECRET_REQUIRED)).toBeDefined()
    expect(calls.some((c) => c.method === 'PUT')).toBe(false)
    fireEvent.change(secretInput(dialog), { target: { value: typed } })
    fireEvent.click(within(dialog).getByRole('button', { name: /保\s*存/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    expect(calls.find((c) => c.method === 'PUT')?.body).toMatchObject({ sshPort: 2222, credentialSecret: typed })

    // 换成私钥凭据：组合变了也不要求
    await pickCredential(dialog, CREDENTIAL_OPTION)
    await waitFor(() => expect(within(dialog).queryByLabelText(SECRET_LABEL)).toBeNull())
  })
})

describe('给服务器绑定 SSH', () => {
  it('密码凭据时必须再输一次密码，随请求提交', async () => {
    const calls = mockServer({
      [`GET ${CREDENTIALS_PATH}`]: () => ok([CREDENTIAL, PASSWORD_CREDENTIAL]),
      'PUT /api/admin/servers/1/host': () => ok(hostOf()),
    })
    const onBound = vi.fn()
    renderPage(<HostBindModal server={serverOf()} isOpen onClose={noop} onBound={onBound} />)
    const dialog = await screen.findByRole('dialog')
    await pickCredential(dialog, PASSWORD_CREDENTIAL_OPTION)
    await within(dialog).findByLabelText(SECRET_LABEL)
    fireEvent.click(within(dialog).getByRole('button', { name: '绑定并校验' }))
    expect(await within(dialog).findByText(SECRET_REQUIRED)).toBeDefined()
    expect(calls.some((c) => c.method === 'PUT')).toBe(false)

    fireEvent.change(secretInput(dialog), { target: { value: typed } })
    fireEvent.click(within(dialog).getByRole('button', { name: '绑定并校验' }))
    await waitFor(() => expect(onBound).toHaveBeenCalledTimes(1))
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({
      address: 'panel.example.com',
      sshPort: 22,
      credentialId: PASSWORD_CREDENTIAL.id,
      credentialSecret: typed,
    })
  })
})

describe('重置主机密钥', () => {
  it('密码凭据的主机：弹出带密码输入框的确认，提交 credentialSecret；私钥凭据的主机仍是行内确认、不带请求体', async () => {
    const passwordHost = hostOf({ id: 7, name: 'pw-01', address: '203.0.113.70', credentialId: PASSWORD_CREDENTIAL.id, credentialName: PASSWORD_CREDENTIAL.name })
    const keyHost = hostOf()
    const calls = mockServer({
      [`GET ${HOSTS_PATH}`]: () => ok([keyHost, passwordHost]),
      [`GET ${CREDENTIALS_PATH}`]: () => ok([CREDENTIAL, PASSWORD_CREDENTIAL]),
      [`POST ${HOSTS_PATH}/7/reset-host-key`]: () => ok(passwordHost),
      [`POST ${HOSTS_PATH}/5/reset-host-key`]: () => ok(keyHost),
    })
    renderPage(<HostsPage />)
    await screen.findByText('pw-01')
    await waitFor(() => expect(calls.some((c) => c.path === CREDENTIALS_PATH)).toBe(true))

    fireEvent.click(within(rowOf('pw-01')).getByRole('button', { name: '重置主机密钥' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('重置 pw-01 的主机密钥')).toBeDefined()
    fireEvent.click(within(dialog).getByRole('button', { name: /重置主机密钥/ }))
    expect(await within(dialog).findByText(SECRET_REQUIRED)).toBeDefined()
    fireEvent.change(secretInput(dialog), { target: { value: typed } })
    fireEvent.click(within(dialog).getByRole('button', { name: /重置主机密钥/ }))
    await waitFor(() => expect(calls.some((c) => c.path === `${HOSTS_PATH}/7/reset-host-key`)).toBe(true))
    const reset = calls.find((c) => c.path === `${HOSTS_PATH}/7/reset-host-key`)
    expect(reset?.csrf).toBe(TEST_CSRF)
    expect(reset?.body).toEqual({ credentialSecret: typed })

    fireEvent.click(within(rowOf('203.0.113.10:22')).getByRole('button', { name: '重置主机密钥' }))
    fireEvent.click(await screen.findByRole('button', { name: CONFIRM }))
    await waitFor(() => expect(calls.some((c) => c.path === `${HOSTS_PATH}/5/reset-host-key`)).toBe(true))
    expect(calls.find((c) => c.path === `${HOSTS_PATH}/5/reset-host-key`)?.body).toBeNull()
  })
})
