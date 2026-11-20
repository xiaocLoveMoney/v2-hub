import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { PERMISSIONS } from '../../menu'
import { TEST_CSRF, mockServer, ok, renderWith } from '../../test/harness'
import { AnnouncementsPage } from './AnnouncementsPage'
import { altFromFileName, insertImage } from './markdown'

const IMAGE_URL = '/uploads/202611/0123456789abcdef0123456789abcdef.png'

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('insertImage', () => {
  it('图片引用独占一行，不粘连前后文字', () => {
    expect(insertImage('', 0, '图', IMAGE_URL)).toBe(`![图](${IMAGE_URL})\n`)
    expect(insertImage('第一步第二步', 3, '图', IMAGE_URL)).toBe(`第一步\n![图](${IMAGE_URL})\n第二步`)
    expect(insertImage('行一\n', 3, '图', IMAGE_URL)).toBe(`行一\n![图](${IMAGE_URL})\n`)
    expect(insertImage('abc', 99, '图', IMAGE_URL)).toBe(`abc\n![图](${IMAGE_URL})\n`)
  })

  it('图片说明取自文件名并去掉 Markdown 特殊字符', () => {
    expect(altFromFileName('链式规则[1].png')).toBe('链式规则 1')
    expect(altFromFileName('![]().png')).toBe('图片')
  })
})

describe('MarkdownEditor 插入图片', () => {
  it('选图后带 CSRF 以 multipart 上传，正文里插入图片引用并随表单提交', async () => {
    const calls = mockServer({
      'GET /api/admin/announcements': () => ok({ items: [], total: 0 }),
      'POST /api/admin/uploads': () => ok({ url: IMAGE_URL, contentType: 'image/png', size: 4 }),
      'POST /api/admin/announcements': () =>
        ok({ id: 1, title: '迁移', content: '', isPinned: false, isPopup: false, publishedAt: null, mailedAt: null, createdAt: '2026-11-21T00:00:00Z' }),
    })
    renderWith([PERMISSIONS.CONTENT_READ, PERMISSIONS.CONTENT_WRITE], <AnnouncementsPage />)
    fireEvent.click(await screen.findByRole('button', { name: /新建公告/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('标题'), { target: { value: '迁移' } })
    fireEvent.change(within(dialog).getByLabelText('正文'), { target: { value: '看图：' } })

    const file = new File(['\x89PNG'], '截图.png', { type: 'image/png' })
    fireEvent.change(within(dialog).getByLabelText('选择图片'), { target: { files: [file] } })
    await waitFor(() => expect(calls.some((c) => c.path === '/api/admin/uploads')).toBe(true))
    const upload = calls.find((c) => c.path === '/api/admin/uploads')
    expect(upload?.csrf).toBe(TEST_CSRF)
    expect(String(upload?.body)).toContain('name="file"')
    await waitFor(() => expect((within(dialog).getByLabelText('正文') as HTMLTextAreaElement).value).toContain(`![截图](${IMAGE_URL})`))

    fireEvent.click(within(dialog).getByRole('button', { name: /创\s*建/ }))
    await waitFor(() => expect(calls.some((c) => c.path === '/api/admin/announcements' && c.method === 'POST')).toBe(true))
    const created = calls.find((c) => c.path === '/api/admin/announcements' && c.method === 'POST')?.body as { content: string }
    expect(created.content).toContain(`![截图](${IMAGE_URL})`)
  })

  it('超过 5 MiB 的图片不上传', async () => {
    const calls = mockServer({ 'GET /api/admin/announcements': () => ok({ items: [], total: 0 }) })
    renderWith([PERMISSIONS.CONTENT_READ, PERMISSIONS.CONTENT_WRITE], <AnnouncementsPage />)
    fireEvent.click(await screen.findByRole('button', { name: /新建公告/ }))
    const dialog = await screen.findByRole('dialog')
    const big = new File([new Uint8Array(5 * 1024 * 1024 + 1)], 'big.png', { type: 'image/png' })
    fireEvent.change(within(dialog).getByLabelText('选择图片'), { target: { files: [big] } })
    expect(await screen.findByText('图片不能超过 5 MiB')).toBeDefined()
    expect(calls.some((c) => c.path === '/api/admin/uploads')).toBe(false)
  })
})
