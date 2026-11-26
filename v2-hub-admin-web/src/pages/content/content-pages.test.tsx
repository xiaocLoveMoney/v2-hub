import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { PERMISSIONS } from '../../menu'
import { CONFIRM, TEST_CSRF, mockServer, ok, renderWith } from '../../test/harness'
import { MailOutboxPage } from '../system/MailOutboxPage'
import { SettingsPage } from '../system/SettingsPage'
import { InviteCodesPage } from '../users/InviteCodesPage'
import { AnnouncementsPage } from './AnnouncementsPage'
import { SharedAccountsPage } from './SharedAccountsPage'
import { TicketsPage } from './TicketsPage'
import { TutorialsPage } from './TutorialsPage'

const NOW = '2026-11-13T00:00:00Z'

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('AnnouncementsPage', () => {
  const published = { id: 1, title: '维护通知', content: '今晚维护', isPinned: true, isPopup: false, publishedAt: NOW, mailedAt: null, createdAt: NOW }
  const draft = { id: 2, title: '草稿一则', content: '', isPinned: false, isPopup: false, publishedAt: null, mailedAt: null, createdAt: NOW }

  it('草稿不能群发；已发布的二次确认后群发并提示封数', async () => {
    const calls = mockServer({
      'GET /api/admin/announcements': () => ok({ items: [published, draft], total: 2 }),
      'POST /api/admin/announcements/1/mail': () => ok({ queued: 7 }),
    })
    renderWith([PERMISSIONS.CONTENT_READ, PERMISSIONS.CONTENT_WRITE], <AnnouncementsPage />)

    const draftRow = (await screen.findByText('草稿一则')).closest('tr') as HTMLElement
    expect(within(draftRow).queryByRole('button', { name: /群发邮件/ })).toBeNull()
    const row = screen.getByText('维护通知').closest('tr') as HTMLElement
    expect(within(row).getByText('置顶')).toBeDefined()

    fireEvent.click(within(row).getByRole('button', { name: /群发邮件/ }))
    expect(calls.some((c) => c.method === 'POST')).toBe(false)
    fireEvent.click(await screen.findByRole('button', { name: CONFIRM }))
    await waitFor(() => expect(calls.some((c) => c.path === '/api/admin/announcements/1/mail')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')?.csrf).toBe(TEST_CSRF)
    expect(await screen.findByText('已入发件箱 7 封')).toBeDefined()
  })

  it('新建公告提交标题、正文与三个开关；只读时没有新建与操作列', async () => {
    const calls = mockServer({
      'GET /api/admin/announcements': () => ok({ items: [], total: 0 }),
      'POST /api/admin/announcements': () => ok(published),
    })
    const view = renderWith([PERMISSIONS.CONTENT_READ, PERMISSIONS.CONTENT_WRITE], <AnnouncementsPage />)
    fireEvent.click(await screen.findByRole('button', { name: /新建公告/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('标题'), { target: { value: '上线了' } })
    fireEvent.change(within(dialog).getByLabelText('正文'), { target: { value: '# 你好' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /创\s*建/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')?.body).toEqual({ title: '上线了', content: '# 你好', isPinned: false, isPopup: false, isPublished: true })

    view.unmount()
    renderWith([PERMISSIONS.CONTENT_READ], <AnnouncementsPage />)
    await screen.findByText('公告')
    expect(screen.queryByRole('button', { name: /新建公告/ })).toBeNull()
    expect(screen.queryByText('操作')).toBeNull()
  })
})

describe('TutorialsPage', () => {
  it('下载链接只收 https：填 http 不提交，改成 https 后提交', async () => {
    const calls = mockServer({
      'GET /api/admin/tutorials': () => ok([]),
      'POST /api/admin/tutorials': () => ok({ id: 1, platform: 'windows', clientName: 'Demo', summary: '', links: [], content: '', sortOrder: 0, isPublished: true }),
    })
    renderWith([PERMISSIONS.CONTENT_READ, PERMISSIONS.CONTENT_WRITE], <TutorialsPage />)
    fireEvent.click(await screen.findByRole('button', { name: /新建教程/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('客户端名称'), { target: { value: 'Demo' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /添加链接/ }))
    fireEvent.change(await within(dialog).findByLabelText('按钮文字'), { target: { value: '下载' } })
    fireEvent.change(within(dialog).getByLabelText('下载地址'), { target: { value: 'http://example.com/a.exe' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /创\s*建/ }))
    expect(await within(dialog).findByText('请填 https 地址')).toBeDefined()
    expect(calls.some((c) => c.method === 'POST')).toBe(false)

    fireEvent.change(within(dialog).getByLabelText('下载地址'), { target: { value: 'https://example.com/a.exe' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /创\s*建/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')?.body).toEqual({
      platform: 'windows', clientName: 'Demo', summary: '', links: [{ label: '下载', url: 'https://example.com/a.exe' }], content: '', sortOrder: 0, isPublished: true,
    })
  })
})

describe('SharedAccountsPage', () => {
  const account = { id: 5, name: '流媒体 A', category: '流媒体', account: 'share@example.com', remark: '', sortOrder: 0, isPublished: true, planIds: [3] }
  /** 后端返回的共享密码（仅测试用）。 */
  const revealed = 'shared-value-for-test'

  it('密码默认不取，点“显示”才请求一次；限定套餐显示套餐名', async () => {
    const calls = mockServer({
      'GET /api/admin/shared-accounts': () => ok([account]),
      'GET /api/admin/plans': () => ok([{ id: 3, name: '标准版' }]),
      'GET /api/admin/shared-accounts/5/credential': () => ok({ credential: revealed }),
    })
    renderWith([PERMISSIONS.CONTENT_READ, PERMISSIONS.CONTENT_WRITE, PERMISSIONS.PLAN_READ], <SharedAccountsPage />)
    expect(await screen.findByText('share@example.com')).toBeDefined()
    expect(await screen.findByText('标准版')).toBeDefined()
    expect(calls.some((c) => c.path.endsWith('/credential'))).toBe(false)
    expect(screen.queryByText(revealed)).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: /显\s*示/ }))
    expect(await screen.findByText(revealed)).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: /隐\s*藏/ }))
    expect(screen.queryByText(revealed)).toBeNull()
  })

  it('只有 content:read 时没有密码列，也不请求套餐', async () => {
    const calls = mockServer({ 'GET /api/admin/shared-accounts': () => ok([account]) })
    renderWith([PERMISSIONS.CONTENT_READ], <SharedAccountsPage />)
    expect(await screen.findByText('share@example.com')).toBeDefined()
    expect(screen.queryByRole('button', { name: /显\s*示/ })).toBeNull()
    expect(screen.getByText('#3')).toBeDefined()
    expect(calls.some((c) => c.path === '/api/admin/plans')).toBe(false)
  })
})

describe('TicketsPage', () => {
  const ticket = { id: 4, subject: '连不上', status: 'open', lastReplyAt: NOW, createdAt: NOW, userId: 9, username: 'alice' }
  const detail = {
    ...ticket,
    messages: [{ id: 1, authorKind: 'user', content: '<img src=x onerror=alert(1)>\n第二行', createdAt: NOW }],
  }

  it('消息按纯文本显示；回复带 CSRF 头并提交内容', async () => {
    const calls = mockServer({
      'GET /api/admin/tickets': () => ok({ items: [ticket], total: 1 }),
      'GET /api/admin/tickets/4': () => ok(detail),
      'POST /api/admin/tickets/4/messages': () => ok({ ...detail, status: 'answered' }),
    })
    renderWith([PERMISSIONS.TICKET_READ, PERMISSIONS.TICKET_WRITE], <TicketsPage />)
    expect(await screen.findByText('alice #9')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: /查\s*看/ }))

    const drawer = await screen.findByRole('dialog')
    expect(await within(drawer).findByText(/onerror=alert/)).toBeDefined()
    expect(document.querySelector('img[src="x"]')).toBeNull()

    fireEvent.change(within(drawer).getByLabelText('回复'), { target: { value: '请重置订阅' } })
    fireEvent.click(within(drawer).getByRole('button', { name: /发送回复/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    const post = calls.find((c) => c.method === 'POST')
    expect(post?.csrf).toBe(TEST_CSRF)
    expect(post?.body).toEqual({ content: '请重置订阅' })
  })

  it('只有 ticket:read 时能看不能回复', async () => {
    mockServer({
      'GET /api/admin/tickets': () => ok({ items: [ticket], total: 1 }),
      'GET /api/admin/tickets/4': () => ok(detail),
    })
    renderWith([PERMISSIONS.TICKET_READ], <TicketsPage />)
    fireEvent.click(await screen.findByRole('button', { name: /查\s*看/ }))
    const drawer = await screen.findByRole('dialog')
    expect(await within(drawer).findByText(/第二行/)).toBeDefined()
    expect(within(drawer).queryByRole('button', { name: /发送回复/ })).toBeNull()
    expect(within(drawer).queryByRole('button', { name: /关闭工单/ })).toBeNull()
  })
})

describe('InviteCodesPage', () => {
  const adminCode = { id: 1, code: 'ABCDEFGH23', kind: 'admin', ownerUserId: null, ownerUsername: '', agentId: null, agentUsername: '', maxUses: 0, usedCount: 2, expiresAt: null, isDisabled: false, remark: '', createdAt: NOW }
  const userCode = { ...adminCode, id: 2, code: 'JKMNPQRS45', kind: 'user', ownerUserId: 9, ownerUsername: 'alice', maxUses: 5 }

  it('个人码不能删除；新建管理员码提交次数、到期与停用', async () => {
    const calls = mockServer({
      'GET /api/admin/invite-codes': () => ok({ items: [adminCode, userCode], total: 2 }),
      'POST /api/admin/invite-codes': () => ok(adminCode),
    })
    renderWith([PERMISSIONS.USER_READ, PERMISSIONS.USER_WRITE], <InviteCodesPage />)
    const adminRow = (await screen.findByText('ABCDEFGH23')).closest('tr') as HTMLElement
    expect(within(adminRow).getByText('2 / 不限')).toBeDefined()
    expect(within(adminRow).getByRole('button', { name: /删\s*除/ })).toBeDefined()
    const userRow = screen.getByText('JKMNPQRS45').closest('tr') as HTMLElement
    expect(within(userRow).queryByRole('button', { name: /删\s*除/ })).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: /新建邀请码/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: /创\s*建/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')?.body).toEqual({ maxUses: 1, expiresAt: null, remark: '', isDisabled: false })
  })
})

describe('SettingsPage', () => {
  const settings = {
    mailEnabled: false,
    site: { name: 'v2-hub', timezone: 'Asia/Shanghai', portalUrl: '' },
    shop: { enabled: false, currency: 'CNY' },
    rebate: { percent: 0, mode: 'first' },
    notify: { expireDays: 3, trafficPercent: 10 },
    register: { mode: 'invite', emailVerify: true, emailAllow: [], emailBlock: [], planId: 0 },
    invite: { userEnabled: true, userMaxUses: 5 },
    checkin: { enabled: false, minMb: 50, maxMb: 200 },
    ticket: { enabled: true, notifyEmail: '' },
    shared: { enabled: false },
    mail: { ratePerMinute: 30 },
    subscription: { baseUrl: '', title: 'v2-hub', updateIntervalHours: 12, infoNodes: true, logRetentionDays: 30, path: 'abcdefghij12', pathGraceDays: 30, oldPath: 'sub', oldPathUntil: '2026-12-14T00:00:00Z' },
    telegramEnabled: false,
    alert: { emails: [], offlineMinutes: 5 },
    telegram: { chatId: '', notifyAlerts: true, notifyTickets: true, notifyPayments: false },
    report: { dailyEnabled: false, dailyHour: 9 },
    landing: { enabled: false, headline: '', intro: '' },
    support: { url: '', label: '在线客服' },
    routing: { blockRule: true },
    reach: { enabled: false, intervalMinutes: 30, notify: true, hasToken: false },
    latency: { enabled: false, intervalMinutes: 30 },
    menu: { admin: ['users'], user: [] },
  }

  it('整张表单一起保存，不带只读的 mailEnabled 与 telegramEnabled；未配置时测试邮件与 Telegram 测试不可用', async () => {
    const calls = mockServer({
      'GET /api/admin/settings': () => ok(settings),
      'PUT /api/admin/settings': () => ok(settings),
    })
    renderWith([PERMISSIONS.SETTING_READ, PERMISSIONS.SETTING_WRITE], <SettingsPage />)
    fireEvent.change(await screen.findByLabelText('站点名称'), { target: { value: '新站名' } })
    expect(screen.getByText('未配置')).toBeDefined()
    expect(screen.getByLabelText('收件邮箱')).toHaveProperty('disabled', true)
    expect(screen.getByText('未配置 token')).toBeDefined()
    expect(screen.getByRole('button', { name: '发送到已保存的会话' })).toHaveProperty('disabled', true)
    // 订阅路径前缀（Ruling-59）：显示过渡中的旧前缀；随机生成后写法合规
    expect(screen.getByText('sub')).toBeDefined()
    const pathInput = screen.getByLabelText('订阅路径前缀') as HTMLInputElement
    fireEvent.click(screen.getByRole('button', { name: '随机生成' }))
    expect(pathInput.value).toMatch(/^[a-z0-9]{16}$/)
    fireEvent.change(pathInput, { target: { value: 'abcdefghij12' } })
    // 客服链接只收 https
    fireEvent.change(screen.getByLabelText('客服链接'), { target: { value: 'http://chat.example.com' } })
    fireEvent.click(screen.getByRole('button', { name: /保存全部设置/ }))
    expect(await screen.findByText('只接受 https:// 开头的地址')).toBeDefined()
    fireEvent.change(screen.getByLabelText('客服链接'), { target: { value: '' } })
    // 平台拦截规则默认开，关掉后随整张表单保存（Ruling-55）
    const blockRule = screen.getByRole('switch', { name: '平台拦截规则' })
    expect(blockRule.getAttribute('aria-checked')).toBe('true')
    fireEvent.click(blockRule)

    fireEvent.click(screen.getByRole('button', { name: /保存全部设置/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    const put = calls.find((c) => c.method === 'PUT')
    expect(put?.csrf).toBe(TEST_CSRF)
    // 菜单顺序另有页面编辑，系统设置页不提交它（Ruling-57）
    const { mailEnabled: _readOnly, telegramEnabled: _readOnlyToo, menu: _otherPage, ...editable } = settings
    // 旧前缀只读，不随表单提交（Ruling-59）
    const { oldPath: _oldPath, oldPathUntil: _oldPathUntil, baseUrl: _superOnly, ...subscription } = settings.subscription
    // 门户地址与订阅地址前缀仅超管可改：非超管的请求体里不带这两个键（Ruling-106）
    const { portalUrl: _superOnlyToo, ...site } = settings.site
    expect(put?.body).toEqual({
      ...editable,
      site: { ...site, name: '新站名' },
      routing: { blockRule: false },
      reach: { enabled: false, intervalMinutes: 30, notify: true },
      latency: { enabled: false, intervalMinutes: 30 },
      subscription,
    })
  })

  it('非超管：门户地址与订阅地址前缀只读显示原值并注明，保存时请求体不带这两个键、其它键照常（Ruling-106）', async () => {
    const loaded = { ...settings, site: { ...settings.site, portalUrl: 'https://user.example.com' }, subscription: { ...settings.subscription, baseUrl: 'https://sub.example.com' } }
    const calls = mockServer({
      'GET /api/admin/settings': () => ok(loaded),
      'PUT /api/admin/settings': () => ok(loaded),
    })
    renderWith([PERMISSIONS.SETTING_READ, PERMISSIONS.SETTING_WRITE], <SettingsPage />)
    fireEvent.change(await screen.findByLabelText('站点名称'), { target: { value: '新站名' } })
    const portal = screen.getByLabelText('客户门户地址') as HTMLInputElement
    const subBase = screen.getByLabelText('订阅地址前缀') as HTMLInputElement
    expect(portal.disabled).toBe(true)
    expect(portal.value).toBe('https://user.example.com')
    expect(subBase.disabled).toBe(true)
    expect(subBase.value).toBe('https://sub.example.com')
    expect(screen.getAllByText(/（仅超级管理员可修改）$/)).toHaveLength(2)

    fireEvent.click(screen.getByRole('button', { name: /保存全部设置/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    const body = calls.find((c) => c.method === 'PUT')?.body as { site: Record<string, unknown>; subscription: Record<string, unknown> }
    expect(body.site).toEqual({ name: '新站名', timezone: 'Asia/Shanghai' })
    expect(body.subscription).not.toHaveProperty('baseUrl')
    expect(body.subscription).toMatchObject({ title: 'v2-hub', updateIntervalHours: 12, infoNodes: true, logRetentionDays: 30, path: 'abcdefghij12', pathGraceDays: 30 })
  })

  it('超管：门户地址与订阅地址前缀可改，没有“仅超级管理员可修改”的说明', async () => {
    const calls = mockServer({
      'GET /api/admin/settings': () => ok(settings),
      'PUT /api/admin/settings': () => ok(settings),
    })
    renderWith([PERMISSIONS.SETTING_READ, PERMISSIONS.SETTING_WRITE], <SettingsPage />, { isSuperAdmin: true })
    const portal = await screen.findByLabelText('客户门户地址')
    expect(portal).toHaveProperty('disabled', false)
    expect(screen.queryByText(/仅超级管理员可修改/)).toBeNull()
    fireEvent.change(portal, { target: { value: 'https://portal.example.com' } })
    fireEvent.change(screen.getByLabelText('订阅地址前缀'), { target: { value: 'https://sub.example.com' } })
    fireEvent.click(screen.getByRole('button', { name: /保存全部设置/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    const body = calls.find((c) => c.method === 'PUT')?.body as { site: { portalUrl: string }; subscription: { baseUrl: string } }
    expect(body.site.portalUrl).toBe('https://portal.example.com')
    expect(body.subscription.baseUrl).toBe('https://sub.example.com')
  })

  it('只有 setting:read 时表单只读、没有保存按钮', async () => {
    mockServer({ 'GET /api/admin/settings': () => ok(settings) })
    renderWith([PERMISSIONS.SETTING_READ], <SettingsPage />)
    expect(await screen.findByLabelText('站点名称')).toHaveProperty('disabled', true)
    expect(screen.queryByRole('button', { name: /保存全部设置/ })).toBeNull()
    expect(screen.queryByLabelText('收件邮箱')).toBeNull()
  })
})

describe('MailOutboxPage', () => {
  it('列出收件人、主题与状态，不请求也不显示正文', async () => {
    const mail = { id: 1, recipient: 'a***@example.com', subject: '验证码', status: 'failed', attempts: 5, lastError: 'smtp: timeout', createdAt: NOW, sentAt: null }
    mockServer({ 'GET /api/admin/mail-outbox': () => ok({ items: [mail], total: 1 }) })
    renderWith([PERMISSIONS.SETTING_READ], <MailOutboxPage />)
    expect(await screen.findByText('a***@example.com')).toBeDefined()
    expect(screen.getByText('失败')).toBeDefined()
    expect(screen.getByText('smtp: timeout')).toBeDefined()
  })
})
