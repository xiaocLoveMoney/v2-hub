import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { PERMISSIONS } from '../../menu'
import { TEST_CSRF, mockServer, ok, renderWith } from '../../test/harness'
import { serverOf } from '../../test/host-fixtures'
import { ServerFormModal } from './ServerFormModal'

const WRITE = [PERMISSIONS.SERVER_READ, PERMISSIONS.SERVER_WRITE]
const SERVER_PATH = '/api/admin/servers/1'
const TOKEN_REQUIRED = '请输入 API token'
const ENDPOINT_CHANGED_HINT = /面板类型或面板地址已修改，须重新填写 API token/
/** 表单里输入的 token（仅测试用）。 */
const typed = 'typed-token'
const noop = () => undefined

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

async function openEdit() {
  const onSaved = vi.fn()
  renderWith(WRITE, <ServerFormModal server={serverOf()} isOpen onClose={noop} onSaved={onSaved} />)
  return { dialog: await screen.findByRole('dialog'), onSaved }
}

describe('ServerFormModal：API token 的必填条件（Ruling-97）', () => {
  it('编辑时地址与面板类型都没变：token 可留空，提交空串表示沿用', async () => {
    const calls = mockServer({ [`PUT ${SERVER_PATH}`]: () => ok(serverOf()) })
    const { dialog, onSaved } = await openEdit()
    expect(within(dialog).getByText('留空表示沿用原 token')).toBeDefined()
    fireEvent.click(within(dialog).getByRole('button', { name: /保\s*存/ }))
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1))
    const put = calls.find((c) => c.method === 'PUT')
    expect(put?.csrf).toBe(TEST_CSRF)
    expect(put?.body).toMatchObject({ baseUrl: 'https://panel.example.com:2053/p', apiToken: '' })
  })

  it('编辑时改了面板地址：token 变为必填并说明原因，不填不提交；填了才提交', async () => {
    const calls = mockServer({ [`PUT ${SERVER_PATH}`]: () => ok(serverOf()) })
    const { dialog, onSaved } = await openEdit()
    fireEvent.change(within(dialog).getByLabelText('面板地址'), { target: { value: 'https://evil.example.net:2053/p' } })
    expect(await within(dialog).findByText(ENDPOINT_CHANGED_HINT)).toBeDefined()
    fireEvent.click(within(dialog).getByRole('button', { name: /保\s*存/ }))
    expect(await within(dialog).findByText(TOKEN_REQUIRED)).toBeDefined()
    expect(calls.some((c) => c.method === 'PUT')).toBe(false)

    fireEvent.change(within(dialog).getByLabelText('API token'), { target: { value: typed } })
    fireEvent.click(within(dialog).getByRole('button', { name: /保\s*存/ }))
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1))
    expect(calls.find((c) => c.method === 'PUT')?.body).toMatchObject({ baseUrl: 'https://evil.example.net:2053/p', apiToken: typed })
  })

  it('地址改回原值后 token 又可留空', async () => {
    mockServer({})
    const { dialog } = await openEdit()
    const input = within(dialog).getByLabelText('面板地址')
    fireEvent.change(input, { target: { value: 'https://other.example.com/p' } })
    expect(await within(dialog).findByText(ENDPOINT_CHANGED_HINT)).toBeDefined()
    fireEvent.change(input, { target: { value: 'https://panel.example.com:2053/p' } })
    expect(await within(dialog).findByText('留空表示沿用原 token')).toBeDefined()
  })

  it('新建时 token 始终必填', async () => {
    const calls = mockServer({})
    renderWith(WRITE, <ServerFormModal server={null} isOpen onClose={noop} onSaved={noop} />)
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('名称'), { target: { value: 'jp-1' } })
    fireEvent.change(within(dialog).getByLabelText('面板地址'), { target: { value: 'https://jp.example.com:2053/x' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /接\s*入/ }))
    expect(await within(dialog).findByText(TOKEN_REQUIRED)).toBeDefined()
    expect(calls.some((c) => c.method === 'POST')).toBe(false)
  })
})
