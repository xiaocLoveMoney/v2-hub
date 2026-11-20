import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TEST_CSRF, fail, mockServer, ok, renderApp } from './test/harness'

const NOW = '2026-11-13T00:00:00Z'
const GIB = 1024 ** 3
const MIB = 1024 ** 2
/** 表单里输入的密码（仅测试用）。 */
const typed = 'correct-horse-battery'
const POPUP_KEY = 'hub.popup.dismissed'

const site = {
  siteName: '测试站', menuOrder: [], registerMode: 'invite', emailVerifyRequired: false, mailEnabled: false, checkinEnabled: false, ticketEnabled: true, sharedEnabled: true,
}
const session = { user: { id: 7, username: 'alice', status: 'active', email: null, isEmailVerified: false }, csrfToken: TEST_CSRF, totpEnabled: false }
const dashboard = {
  status: 'active', planName: '基础版', trafficLimit: 10 * GIB, trafficUsed: 4 * GIB, trafficToday: GIB, expiresAt: '2099-01-01T00:00:00Z',
  pendingDays: 0, limitIp: 3, onlineDevices: 1, lastUsedAt: null, nodeCount: 2, announcement: null, popupAnnouncement: null,
}
const anonymous = () => fail(401, 'unauthenticated', '请先登录')
/** 已登录用户进仪表盘时会发的请求。 */
const signedIn = {
  'GET /api/user/auth/me': () => ok(session),
  'GET /api/user/dashboard': () => ok(dashboard),
  'GET /api/user/traffic': () => ok([]),
  'GET /api/user/subscription': () => fail(404, 'not_found', '不存在'),
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  globalThis.localStorage.clear()
})

describe('注册', () => {
  it('仅邀请时从链接预填邀请码；提交后直接登录进仪表盘', async () => {
    const calls = mockServer({
      ...signedIn,
      'GET /api/user/site': () => ok(site),
      'GET /api/user/auth/me': anonymous,
      'POST /api/user/auth/register': () => ok(session),
    })
    renderApp('/register?code=ABCDEFGH23')
    const invite = (await screen.findByLabelText('邀请码')) as HTMLInputElement
    expect(invite.value).toBe('ABCDEFGH23')
    expect(screen.queryByLabelText('邮箱验证码')).toBeNull()

    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'bad@name' } })
    expect(screen.getByText('只能用 3–64 位字母、数字或 _ . -')).toBeDefined()
    expect((screen.getByRole('button', { name: '注册' }) as HTMLButtonElement).disabled).toBe(true)

    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'alice' } })
    fireEvent.change(screen.getByLabelText('邮箱'), { target: { value: 'alice@example.com' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: typed } })
    fireEvent.change(screen.getByLabelText('再输一次密码'), { target: { value: typed } })
    fireEvent.click(screen.getByRole('button', { name: '注册' }))

    await waitFor(() => expect(calls.some((c) => c.path === '/api/user/auth/register')).toBe(true))
    expect(calls.find((c) => c.path === '/api/user/auth/register')?.body).toEqual({
      username: 'alice', email: 'alice@example.com', password: typed, inviteCode: 'ABCDEFGH23',
    })
    expect(await screen.findByText('套餐剩余')).toBeDefined()
  })

  it('要求验证邮箱时先发验证码，发出后按钮进入倒计时', async () => {
    const calls = mockServer({
      'GET /api/user/site': () => ok({ ...site, registerMode: 'open', emailVerifyRequired: true, mailEnabled: true }),
      'GET /api/user/auth/me': anonymous,
      'POST /api/user/auth/email-code': () => ok(null),
    })
    renderApp('/register')
    const send = (await screen.findByRole('button', { name: '发送验证码' })) as HTMLButtonElement
    expect(send.disabled).toBe(true)
    expect(screen.getByLabelText('邀请码（可选）')).toBeDefined()

    fireEvent.change(screen.getByLabelText('邮箱'), { target: { value: 'alice@example.com' } })
    fireEvent.click(screen.getByRole('button', { name: '发送验证码' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')?.body).toEqual({ email: 'alice@example.com', purpose: 'register' })
    expect(await screen.findByRole('button', { name: /秒后可重发/ })).toHaveProperty('disabled', true)
  })

  it('注册关闭时只显示说明；登录页也不给注册与找回入口', async () => {
    mockServer({
      'GET /api/user/site': () => ok({ ...site, registerMode: 'closed' }),
      'GET /api/user/auth/me': anonymous,
    })
    const view = renderApp('/register')
    expect(await screen.findByText('当前不开放注册，请联系管理员开通账号。')).toBeDefined()
    expect(screen.queryByLabelText('用户名')).toBeNull()

    view.unmount()
    renderApp('/login')
    await screen.findByLabelText('用户名')
    expect(screen.queryByText('注册账号')).toBeNull()
    expect(screen.queryByText('忘记密码')).toBeNull()
  })
})

describe('找回密码', () => {
  it('邮件可用时发 reset 验证码并提交新密码，成功后回登录页', async () => {
    const calls = mockServer({
      'GET /api/user/site': () => ok({ ...site, mailEnabled: true }),
      'GET /api/user/auth/me': anonymous,
      'POST /api/user/auth/email-code': () => ok(null),
      'POST /api/user/auth/password/reset': () => ok(null),
    })
    renderApp('/login')
    fireEvent.click(await screen.findByText('忘记密码'))
    fireEvent.change(await screen.findByLabelText('邮箱'), { target: { value: 'alice@example.com' } })
    fireEvent.click(screen.getByRole('button', { name: '发送验证码' }))
    await waitFor(() => expect(calls.some((c) => c.path === '/api/user/auth/email-code')).toBe(true))
    expect(calls.find((c) => c.path === '/api/user/auth/email-code')?.body).toEqual({ email: 'alice@example.com', purpose: 'reset' })

    fireEvent.change(screen.getByLabelText('邮箱验证码'), { target: { value: '12x3456' } })
    fireEvent.change(screen.getByLabelText('新密码'), { target: { value: typed } })
    fireEvent.change(screen.getByLabelText('再输一次新密码'), { target: { value: typed } })
    fireEvent.click(screen.getByRole('button', { name: '重设密码' }))
    await waitFor(() => expect(calls.some((c) => c.path === '/api/user/auth/password/reset')).toBe(true))
    expect(calls.find((c) => c.path === '/api/user/auth/password/reset')?.body).toEqual({ email: 'alice@example.com', emailCode: '123456', newPassword: typed })
    expect(await screen.findByRole('button', { name: '登录' })).toBeDefined()
  })

  it('邮件不可用时提示找管理员', async () => {
    mockServer({ 'GET /api/user/site': () => ok(site), 'GET /api/user/auth/me': anonymous })
    renderApp('/reset')
    expect(await screen.findByText(/无法自助找回/)).toBeDefined()
    expect(screen.queryByLabelText('邮箱验证码')).toBeNull()
  })
})

describe('菜单与仪表盘', () => {
  it('菜单按站点开关显示；站点信息取不到时按全部关闭', async () => {
    mockServer({ ...signedIn, 'GET /api/user/site': () => ok({ ...site, ticketEnabled: false }) })
    const view = renderApp('/')
    const menu = await screen.findByRole('navigation', { name: '主菜单' })
    expect(await within(menu).findByText('共享账号')).toBeDefined()
    expect(within(menu).queryByText('工单支持')).toBeNull()
    expect(within(menu).getByText('邀请注册')).toBeDefined()

    view.unmount()
    mockServer(signedIn)
    renderApp('/')
    const fallback = await screen.findByRole('navigation', { name: '主菜单' })
    expect(within(fallback).queryByText('共享账号')).toBeNull()
    expect(within(fallback).getByText('下载和使用')).toBeDefined()
  })

  it('公告按 Markdown 渲染且不执行原始 HTML；弹窗公告关掉后同一条不再弹', async () => {
    const latest = {
      id: 3, title: '维护通知', isPinned: false, isPopup: false, publishedAt: NOW,
      content: '今晚 **停机** 维护\n\n<img src=x onerror=alert(1)>\n\n[官网](https://example.com) [坏链接](javascript:alert(1))',
    }
    const popup = { id: 9, title: '重要提醒', content: '请尽快续费', isPinned: false, isPopup: true, publishedAt: NOW }
    mockServer({ ...signedIn, 'GET /api/user/site': () => ok(site), 'GET /api/user/dashboard': () => ok({ ...dashboard, announcement: latest, popupAnnouncement: popup }) })
    const view = renderApp('/')

    expect((await screen.findByText('停机')).tagName).toBe('STRONG')
    expect(document.querySelector('img[src="x"]')).toBeNull()
    const link = screen.getByText('官网').closest('a')
    expect(link?.getAttribute('href')).toBe('https://example.com')
    expect(link?.getAttribute('rel')).toBe('noopener noreferrer')
    expect(screen.getByText('坏链接').closest('a')?.getAttribute('href') ?? '').not.toContain('javascript')

    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText('请尽快续费')).toBeDefined()
    fireEvent.click(within(dialog).getByRole('button', { name: '知道了' }))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(globalThis.localStorage.getItem(POPUP_KEY)).toBe('9')

    view.unmount()
    renderApp('/')
    await screen.findByText('停机')
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('签到带 CSRF 头，成功后提示奖励并刷新状态', async () => {
    let checked = false
    const calls = mockServer({
      ...signedIn,
      'GET /api/user/site': () => ok({ ...site, checkinEnabled: true }),
      'GET /api/user/checkin': () =>
        ok({ enabled: true, canCheckin: !checked, reason: '', checkedToday: checked, logs: checked ? [{ date: '2026-11-13', rewardBytes: 100 * MIB }] : [] }),
      'POST /api/user/checkin': () => {
        checked = true
        return ok({ rewardBytes: 100 * MIB, trafficLimit: 10 * GIB + 100 * MIB })
      },
    })
    renderApp('/')
    fireEvent.click(await screen.findByRole('button', { name: '签到' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.path === '/api/user/checkin')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')?.csrf).toBe(TEST_CSRF)
    expect(await screen.findByText('签到成功，获得 100.00 MB')).toBeDefined()
    expect(await screen.findByText('今日已签到')).toBeDefined()
    expect(screen.getByRole('button', { name: '明天再来' })).toHaveProperty('disabled', true)
  })
})

describe('邀请、教程与共享账号', () => {
  it('邀请链接指向注册页并带邀请码；重置须二次确认', async () => {
    const calls = mockServer({
      ...signedIn,
      'GET /api/user/site': () => ok(site),
      'GET /api/user/invite': () => ok({ enabled: true, code: 'ABCDEFGH23', maxUses: 0, usedCount: 2, invitees: [{ username: 'bo***', createdAt: NOW }] }),
      'POST /api/user/invite/reset': () => ok({ enabled: true, code: 'JKMNPQRS45', maxUses: 0, usedCount: 2, invitees: [] }),
    })
    renderApp('/invite')
    const link = (await screen.findByLabelText('邀请链接')) as HTMLInputElement
    expect(link.value).toMatch(/\/register\?code=ABCDEFGH23$/)
    expect(screen.getByText('bo***')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: '重置邀请码' }))
    expect(calls.some((c) => c.method === 'POST')).toBe(false)
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: '重置' }))
    await waitFor(() => expect(calls.some((c) => c.path === '/api/user/invite/reset')).toBe(true))
    expect(calls.find((c) => c.path === '/api/user/invite/reset')?.csrf).toBe(TEST_CSRF)
  })

  it('教程按平台分段，列表只显示概要卡；点开进详情页，不是 https 的下载链接不渲染；返回时保持平台', async () => {
    const tutorials = [
      {
        id: 1, platform: 'windows', clientName: 'Win 客户端', summary: '电脑用', sortOrder: 0, isPublished: true, content: '# 安装步骤',
        links: [{ label: '官方下载', url: 'https://example.com/win.exe' }, { label: '镜像下载', url: 'http://example.com/win.exe' }],
      },
      { id: 2, platform: 'ios', clientName: 'iOS 客户端', summary: '', sortOrder: 0, isPublished: true, content: '# iOS 步骤', links: [] },
    ]
    mockServer({ ...signedIn, 'GET /api/user/site': () => ok(site), 'GET /api/user/tutorials': () => ok(tutorials) })
    renderApp('/tutorials')
    expect(await screen.findByText('Win 客户端')).toBeDefined()
    expect(screen.getByText('电脑用')).toBeDefined()
    expect(screen.queryByText('官方下载')).toBeNull()
    expect(screen.queryByText('安装步骤')).toBeNull()
    expect(screen.queryByRole('tab', { name: 'Android' })).toBeNull()

    fireEvent.click(screen.getByRole('tab', { name: 'iOS' }))
    expect(screen.queryByText('Win 客户端')).toBeNull()
    fireEvent.click(screen.getByRole('link', { name: /iOS 客户端/ }))
    expect(await screen.findByText('iOS 步骤')).toBeDefined()
    fireEvent.click(screen.getByRole('link', { name: '返回教程列表' }))
    expect(await screen.findByRole('tab', { name: 'iOS', selected: true })).toBeDefined()
    expect(screen.getByText('iOS 客户端')).toBeDefined()

    fireEvent.click(screen.getByRole('tab', { name: 'Windows' }))
    fireEvent.click(screen.getByRole('link', { name: /Win 客户端/ }))
    expect(await screen.findByText('安装步骤')).toBeDefined()
    expect(screen.getByText('官方下载').getAttribute('href')).toBe('https://example.com/win.exe')
    expect(screen.queryByText('镜像下载')).toBeNull()
  })

  it('不存在或未发布的教程详情显示 404', async () => {
    mockServer({ ...signedIn, 'GET /api/user/site': () => ok(site), 'GET /api/user/tutorials': () => ok([]) })
    renderApp('/tutorials/99')
    expect(await screen.findByText('页面不存在')).toBeDefined()
  })

  it('共享账号的密码点“显示”才请求', async () => {
    /** 后端返回的共享密码（仅测试用）。 */
    const revealed = 'shared-value-for-test'
    const calls = mockServer({
      ...signedIn,
      'GET /api/user/site': () => ok(site),
      'GET /api/user/shared-accounts': () => ok([{ id: 5, name: '流媒体 A', category: '流媒体', account: 'share@example.com', remark: '勿改资料', sortOrder: 0 }]),
      'GET /api/user/shared-accounts/5/credential': () => ok({ credential: revealed }),
    })
    renderApp('/shared-accounts')
    expect(await screen.findByText('share@example.com')).toBeDefined()
    expect(calls.some((c) => c.path.endsWith('/credential'))).toBe(false)
    fireEvent.click(screen.getByRole('button', { name: '显示' }))
    expect(await screen.findByText(revealed)).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: '隐藏' }))
    expect(screen.queryByText(revealed)).toBeNull()
  })
})

describe('工单', () => {
  const detail = {
    id: 4, subject: '连不上', status: 'answered', lastReplyAt: NOW, createdAt: NOW,
    messages: [
      { id: 1, authorKind: 'user', content: '<img src=x onerror=alert(1)>\n第二行', createdAt: NOW },
      { id: 2, authorKind: 'admin', content: '请重置订阅', createdAt: NOW },
    ],
  }

  it('新建后进入对话页；消息按纯文本显示，客服不显示管理员用户名；回复带 CSRF 头', async () => {
    const calls = mockServer({
      ...signedIn,
      'GET /api/user/site': () => ok(site),
      'GET /api/user/tickets': () => ok({ items: [], total: 0 }),
      'POST /api/user/tickets': () => ok(detail),
      'GET /api/user/tickets/4': () => ok(detail),
      'POST /api/user/tickets/4/messages': () => ok({ ...detail, status: 'open' }),
    })
    renderApp('/tickets')
    fireEvent.click(await screen.findByRole('button', { name: '新建工单' }))
    const dialog = screen.getByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('主题'), { target: { value: '连不上' } })
    fireEvent.change(within(dialog).getByLabelText('问题描述'), { target: { value: '节点全红' } })
    fireEvent.click(within(dialog).getByRole('button', { name: '提交' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.path === '/api/user/tickets')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')?.body).toEqual({ subject: '连不上', content: '节点全红' })

    expect(await screen.findByText(/onerror=alert/)).toBeDefined()
    expect(document.querySelector('img[src="x"]')).toBeNull()
    expect(screen.getByText('客服')).toBeDefined()
    expect(screen.getByText('已回复')).toBeDefined()

    fireEvent.change(screen.getByLabelText('回复'), { target: { value: '好了' } })
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    await waitFor(() => expect(calls.some((c) => c.path === '/api/user/tickets/4/messages')).toBe(true))
    const reply = calls.find((c) => c.path === '/api/user/tickets/4/messages')
    expect(reply?.csrf).toBe(TEST_CSRF)
    expect(reply?.body).toEqual({ content: '好了' })
  })

  it('工单号不是数字时显示 404，不发请求', async () => {
    const calls = mockServer({ ...signedIn, 'GET /api/user/site': () => ok(site) })
    renderApp('/tickets/abc')
    await screen.findByRole('navigation', { name: '主菜单' })
    expect(calls.some((c) => c.path.startsWith('/api/user/tickets'))).toBe(false)
  })
})

describe('我的账号', () => {
  it('注销账号须输入当前密码，成功后回到登录页', async () => {
    const calls = mockServer({
      ...signedIn,
      'GET /api/user/site': () => ok(site),
      'GET /api/user/login-logs': () => ok([]),
      'POST /api/user/account/delete': () => ok(null),
    })
    renderApp('/account')
    expect(await screen.findByText(/绑定或更换邮箱请联系管理员/)).toBeDefined()
    fireEvent.click(await screen.findByRole('button', { name: '注销账号' }))
    const dialog = screen.getByRole('dialog')
    const confirm = within(dialog).getByRole('button', { name: '永久注销' }) as HTMLButtonElement
    expect(confirm.disabled).toBe(true)
    fireEvent.change(within(dialog).getByLabelText('当前密码'), { target: { value: typed } })
    fireEvent.click(confirm)
    await waitFor(() => expect(calls.some((c) => c.path === '/api/user/account/delete')).toBe(true))
    const post = calls.find((c) => c.path === '/api/user/account/delete')
    expect(post?.csrf).toBe(TEST_CSRF)
    expect(post?.body).toEqual({ currentPassword: typed })
    expect(await screen.findByRole('button', { name: '登录' })).toBeDefined()
  })

  it('邮件可用时换绑邮箱：发码到新邮箱，带当前密码与验证码提交', async () => {
    const bound = { ...session, user: { ...session.user, email: 'new@example.com', isEmailVerified: true } }
    const calls = mockServer({
      ...signedIn,
      'GET /api/user/site': () => ok({ ...site, mailEnabled: true }),
      'GET /api/user/login-logs': () => ok([]),
      'POST /api/user/account/email-code': () => ok(null),
      'PUT /api/user/account/email': () => ok(bound),
    })
    renderApp('/account')
    fireEvent.change(await screen.findByLabelText('邮箱'), { target: { value: 'new@example.com' } })
    fireEvent.click(screen.getByRole('button', { name: '发送验证码' }))
    await waitFor(() => expect(calls.some((c) => c.path === '/api/user/account/email-code')).toBe(true))
    expect(calls.find((c) => c.path === '/api/user/account/email-code')?.body).toEqual({ email: 'new@example.com' })

    fireEvent.change(screen.getByLabelText('邮箱验证码'), { target: { value: '654321' } })
    fireEvent.change(screen.getByLabelText('当前密码（确认是本人）'), { target: { value: typed } })
    fireEvent.click(screen.getByRole('button', { name: '绑定' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({ currentPassword: typed, email: 'new@example.com', emailCode: '654321' })
    expect(await screen.findByText('当前邮箱：new@example.com')).toBeDefined()
    expect(screen.getByText('已验证')).toBeDefined()
  })
})
