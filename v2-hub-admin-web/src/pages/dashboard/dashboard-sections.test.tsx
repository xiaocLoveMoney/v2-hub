import { cleanup, fireEvent, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { PanelNode } from '../../api/client'
import { PERMISSIONS } from '../../menu'
import { mockServer, ok, renderWith, TEST_CSRF } from '../../test/harness'
import { SERVER_STATUS_FIELDS } from '../../test/host-fixtures'
import { DashboardPage } from '../DashboardPage'
import { NodeReachButton } from '../panel/reach'
import { ServersPage } from '../panel/ServersPage'
import { BacklogSection } from './BacklogSection'
import { FinanceSection } from './FinanceSection'
import { ServersSection } from './ServersSection'
import { TrafficSection } from './TrafficSection'
import { UsersSection } from './UsersSection'

// 图表库在 jsdom 里画不出来：换成只带说明文字的占位，断言图表有没有出现即可
vi.mock('../../components/EChart', () => ({
  EChart: ({ ariaLabel }: { ariaLabel: string }) => <div role="img" aria-label={ariaLabel} />,
}))

const GIB = 1024 ** 3
const DATES = ['2026-11-13', '2026-11-14', '2026-11-15']
const emptyDashboard = { users: null, traffic: null, servers: null, finance: null, tickets: null }

const users = {
  growth: DATES.map((date, i) => ({ date, newUsers: i, totalUsers: 100 + i })),
  activeDays: DATES.map((date) => ({ date, users: 40 })),
  dau: 41,
  wau: 66,
  mau: 88,
  status: { active: 90, limited: 4, expired: 7, disabled: 1 },
  expiry: { expired: 7, within7d: 12, within30d: 30, later: 40, never: 13, within7dNoAutoRenew: 9 },
  plans: [
    { planId: 1, name: '基础版', users: 60, activeUsers: 55 },
    { planId: null, name: '', users: 5, activeUsers: 0 },
  ],
  silentUsers: 17,
  topUsers: [{ userId: 3, username: 'alice', planName: '基础版', bytes: 5 * GIB }],
  subscription: {
    days: DATES.map((date) => ({ date, pulls: 20, users: 10 })),
    clients: [
      { client: 'clash', pulls: 50, users: 25 },
      { client: 'other', pulls: 3, users: 2 },
    ],
    formats: [{ format: 'mihomo', pulls: 50 }],
  },
}

const traffic = {
  days: DATES.map((date) => ({ date, up: GIB, down: 3 * GIB })),
  hours: Array.from({ length: 24 }, (_, hour) => ({ hour, bytes: hour * 1024 })),
  servers: [{ serverId: 2, name: 'jp-1', up: GIB, down: 2 * GIB }],
}

const reachProblem = {
  address: 'jp.example.com', port: 443, transport: 'tcp', verdict: 'blocked', scope: 'port', cnOk: 0, cnTotal: 3, ipReachable: true,
  checkedAt: '2026-11-15T08:00:00Z', blockedSince: '2026-11-15T07:00:00Z', servers: ['jp-1'], nodes: ['日本 01', '日本 02'], nodeCount: 4,
}
const servers = {
  availability: [{ serverId: 2, name: 'jp-1', percent: 97.5, samples: 1966, expected: 2016 }],
  online: [{ at: '2026-11-15T08:00:00Z', connections: 31 }],
  topCpu: [{ serverId: 2, name: 'jp-1', cpu: 63.25, onlineClients: 31 }],
  topOnline: [{ serverId: 2, name: 'jp-1', cpu: null, onlineClients: 31 }],
  reach: { counts: { pending: 1, ok: 5, partial: 0, blocked: 1, portDown: 0, unknown: 0 }, problems: [reachProblem] },
}

const finance = {
  days: DATES.map((date) => ({ date, orderPaid: 1000, topup: 5000 })),
  orderKinds: [
    { kind: 'plan', count: 2, paid: 2000 },
    { kind: 'renew', count: 1, paid: 1000 },
    { kind: 'traffic_pack', count: 0, paid: 0 },
  ],
  trialCount: 6,
  plans: [{ planId: null, name: '', count: 3, paid: 3000 }],
  funnel: { registered: 20, trial: 10, paid: 5 },
  payers: { users: 3, paid: 3000 },
  balanceTotal: 123456,
  payments: { paid: 3, expired: 1, pending: 2 },
  coupons: [{ code: 'SAVE20', count: 2, discount: 400, paid: 1600 }],
}

const backlog = {
  tickets: { open: 4, oldestWaitingSince: '2026-11-14T00:00:00Z', avgFirstReplyMinutes: 95 },
  provisions: { failed: 3, servers: [{ serverId: 2, name: 'jp-1', failed: 3 }] },
  panels: { chainFailed: [{ serverId: 2, name: 'jp-1', failCount: 3, lastAttemptAt: '2026-11-15T08:00:00Z' }], driftInbounds: 2, missingInbounds: 1 },
  outbox: { mailPending: 5, mailFailed24h: 1, telegramPending: 0, telegramFailed24h: 0 },
}

const withRouter = (node: React.ReactNode) => <MemoryRouter>{node}</MemoryRouter>

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('总览分区（docs/03 §5.20 总览分析，Ruling-66）', () => {
  it('分区按权限码出现；切到分区才请求', async () => {
    const calls = mockServer({
      'GET /api/admin/dashboard': () => ok(emptyDashboard),
      'GET /api/admin/analytics/servers': () => ok({ ...servers, reach: null }),
    })
    renderWith([PERMISSIONS.SERVER_READ], withRouter(<DashboardPage />))
    expect(await screen.findByRole('tab', { name: '概览' })).toBeDefined()
    expect(screen.getByRole('tab', { name: '服务器' })).toBeDefined()
    expect(screen.getByRole('tab', { name: '待处理' })).toBeDefined()
    expect(screen.queryByRole('tab', { name: '用户' })).toBeNull()
    expect(screen.queryByRole('tab', { name: '流量' })).toBeNull()
    expect(screen.queryByRole('tab', { name: '财务' })).toBeNull()
    expect(calls.some((c) => c.path.startsWith('/api/admin/analytics/'))).toBe(false)

    fireEvent.click(screen.getByRole('tab', { name: '服务器' }))
    expect(await screen.findByText('可用率')).toBeDefined()
    expect(calls.filter((c) => c.path === '/api/admin/analytics/servers').length).toBe(1)
  })

  it('用户分区：指标、分布与排行；切换时间范围重新请求', async () => {
    const calls = mockServer({ 'GET /api/admin/analytics/users': () => ok(users) })
    renderWith([PERMISSIONS.USER_READ], <UsersSection />)
    expect(await screen.findByText('今日活跃')).toBeDefined()
    expect(screen.getByText('41')).toBeDefined()
    expect(screen.getByText('现有 102 人')).toBeDefined()
    expect(screen.getByRole('img', { name: '累计用户曲线' })).toBeDefined()
    expect(screen.getByText('流量用完')).toBeDefined()
    expect(screen.getByText('Clash / Mihomo')).toBeDefined()
    expect(screen.getByText('25 人 · 50 次')).toBeDefined()
    expect(screen.getByText('没有套餐')).toBeDefined()
    expect(screen.getByText('alice')).toBeDefined()
    expect(screen.getByText('5.00 GB')).toBeDefined()
    expect(calls[0]?.query).toBe('?days=30')

    fireEvent.click(screen.getByText('近 7 日'))
    await waitFor(() => expect(calls.some((c) => c.query === '?days=7')).toBe(true))
  })

  it('流量分区：没有 server:read 时不出按服务器的那一块', async () => {
    mockServer({ 'GET /api/admin/analytics/traffic': () => ok(traffic) })
    renderWith([PERMISSIONS.USER_READ, PERMISSIONS.SERVER_READ], <TrafficSection />)
    expect(await screen.findByText('下行 9.00 GB · 上行 3.00 GB')).toBeDefined()
    expect(screen.getByRole('img', { name: '每日上行与下行流量' })).toBeDefined()
    expect(screen.getByText('按服务器')).toBeDefined()
    expect(screen.getByText('jp-1')).toBeDefined()
    cleanup()

    mockServer({ 'GET /api/admin/analytics/traffic': () => ok({ ...traffic, servers: null }) })
    renderWith([PERMISSIONS.USER_READ], <TrafficSection />)
    expect(await screen.findByText('时段分布')).toBeDefined()
    expect(screen.queryByText('按服务器')).toBeNull()
  })

  it('服务器分区：可用率、负载与大陆连通的问题地址', async () => {
    const calls = mockServer({ 'GET /api/admin/analytics/servers': () => ok(servers) })
    renderWith([PERMISSIONS.SERVER_READ], withRouter(<ServersSection />))
    expect(await screen.findByText('jp.example.com:443')).toBeDefined()
    expect(screen.getByText('被墙 1')).toBeDefined()
    expect(screen.getByText('被墙 0/3')).toBeDefined()
    expect(screen.getByText('日本 01、日本 02 等 4 个')).toBeDefined()
    expect(screen.getByText('97.5%')).toBeDefined()
    expect(screen.getByText('63.3%')).toBeDefined()
    expect(screen.getAllByRole('link', { name: 'jp-1' })[0]?.getAttribute('href')).toBe('/servers/2')

    fireEvent.click(screen.getByText('近 7 天'))
    await waitFor(() => expect(calls.some((c) => c.query === '?range=7d')).toBe(true))
  })

  it('财务分区：金额按分换算，转化带占比', async () => {
    mockServer({ 'GET /api/admin/analytics/finance': () => ok(finance) })
    renderWith([PERMISSIONS.FINANCE_READ], <FinanceSection />)
    expect(await screen.findByText('客单价')).toBeDefined()
    expect(screen.getByText('¥10.00')).toBeDefined()
    expect(screen.getByText('¥1,234.56')).toBeDefined()
    expect(screen.getByText('75.0%')).toBeDefined()
    expect(screen.getByText('订单实付 ¥30.00 · 充值入账 ¥150.00')).toBeDefined()
    expect(screen.getByText('¥20.00 · 2 笔')).toBeDefined()
    expect(screen.getByText('5 人 · 25.0%')).toBeDefined()
    expect(screen.getByText('已删除的套餐')).toBeDefined()
    expect(screen.getByText('SAVE20')).toBeDefined()
  })

  it('待处理分区：缺权限的块不显示', async () => {
    mockServer({ 'GET /api/admin/analytics/backlog': () => ok(backlog) })
    renderWith([], withRouter(<BacklogSection />))
    expect(await screen.findByText('待回复')).toBeDefined()
    expect(screen.getByText('1 小时 35 分钟')).toBeDefined()
    expect(screen.getByText('失败的 client')).toBeDefined()
    expect(screen.getByText('连续失败 3 次', { exact: false })).toBeDefined()
    expect(screen.getByText('邮件待发')).toBeDefined()
    cleanup()

    mockServer({ 'GET /api/admin/analytics/backlog': () => ok({ ...backlog, tickets: null, panels: null, outbox: null }) })
    renderWith([], withRouter(<BacklogSection />))
    expect(await screen.findByText('失败的 client')).toBeDefined()
    expect(screen.queryByText('待回复')).toBeNull()
    expect(screen.queryByText('邮件待发')).toBeNull()
  })
})

describe('大陆连通检测（Ruling-67）', () => {
  const node: PanelNode = {
    id: 9, inboundId: 4, endpointId: null, serverId: 2, serverName: 'jp-1', protocol: 'vless', address: 'jp.example.com', port: 443,
    displayName: '日本 01', region: 'JP', tags: [], sortOrder: 0, isEnabled: true, isMissing: false, groupIds: [],
    reach: { verdict: 'blocked', scope: 'port', cnOk: 0, cnTotal: 3, checkedAt: '2026-11-15T08:00:00Z' },
  }
  const detail = {
    address: 'jp.example.com', port: 443, transport: 'tcp', verdict: 'blocked', scope: 'port', cnOk: 0, cnTotal: 3, controlOk: 2, controlTotal: 2,
    ipReachable: true, checkedAt: '2026-11-15T08:00:00Z', blockedSince: '2026-11-15T07:00:00Z', lastError: '',
    probes: [
      { isMainland: true, country: 'CN', city: 'Shanghai', network: 'China Telecom', isOk: false, rttMs: null },
      { isMainland: false, country: 'JP', city: 'Tokyo', network: 'NTT', isOk: true, rttMs: 2.04 },
    ],
  }

  it('点开结论看各探测点；立即检测带 CSRF 头并刷新列表', async () => {
    const onChecked = vi.fn()
    const calls = mockServer({
      'GET /api/admin/nodes/9/reach': () => ok(detail),
      'POST /api/admin/nodes/9/reach/check': () => ok({ ...detail, verdict: 'ok', cnOk: 3, ipReachable: null, blockedSince: null }),
    })
    renderWith([PERMISSIONS.NODE_READ, PERMISSIONS.NODE_WRITE], <NodeReachButton node={node} canWrite onChecked={onChecked} />)
    fireEvent.click(screen.getByRole('button', { name: '日本 01 的大陆连通：被墙' }))
    expect(await screen.findByText('jp.example.com:443（TCP）')).toBeDefined()
    expect(screen.getByText('0 / 3 连通')).toBeDefined()
    expect(screen.getByText('大陆还能 ping 通，只有这个端口不通')).toBeDefined()
    expect(screen.getByText('大陆 · CN Shanghai')).toBeDefined()
    expect(screen.getByText('2.0 ms')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: /立即检测/ }))
    await waitFor(() => expect(onChecked).toHaveBeenCalled())
    expect(calls.find((c) => c.method === 'POST')?.csrf).toBe(TEST_CSRF)
    expect(await screen.findByText('3 / 3 连通')).toBeDefined()
  })

  it('服务器列表：检测开着才出“大陆连通”列，没测出结论的不写成正常', async () => {
    const base = {
      providerType: 'xui', baseUrl: 'https://p.example.com', tlsMode: 'verify', certPinSha256: null, region: 'JP', remark: '', isDisabled: false,
      status: 'online', lastError: '', lastSeenAt: null, lastSyncedAt: null, panelVersion: '3.11.0', inboundCount: 1,
      createdAt: '2026-11-12T00:00:00Z', ...SERVER_STATUS_FIELDS,
    }
    const list = [
      { ...base, id: 1, name: 'jp-1', reach: { total: 3, ok: 1, blocked: 1, partial: 1 } },
      { ...base, id: 2, name: 'jp-2', reach: { total: 2, ok: 2, blocked: 0, partial: 0 } },
      { ...base, id: 3, name: 'jp-3', reach: { total: 2, ok: 1, blocked: 0, partial: 0 } },
      { ...base, id: 4, name: 'jp-4', reach: { total: 2, ok: 0, blocked: 0, partial: 0 } },
    ]
    mockServer({ 'GET /api/admin/servers': () => ok(list) })
    renderWith([PERMISSIONS.SERVER_READ], withRouter(<ServersPage />))
    expect(await screen.findByText('被墙 1')).toBeDefined()
    expect(screen.getByText('部分不通 1')).toBeDefined()
    expect(screen.getByText('正常')).toBeDefined()
    expect(screen.getByText('正常 1/2')).toBeDefined()
    expect(screen.getByText('暂无结论')).toBeDefined()
    expect(screen.getAllByText('大陆连通').length).toBeGreaterThan(0)
    cleanup()

    mockServer({ 'GET /api/admin/servers': () => ok(list.map((s) => ({ ...s, reach: null }))) })
    renderWith([PERMISSIONS.SERVER_READ], withRouter(<ServersPage />))
    expect(await screen.findByText('jp-1')).toBeDefined()
    expect(screen.queryByText('大陆连通')).toBeNull()
  })

  it('没有 node:write 不出检测按钮；不在检测范围的节点显示占位符', async () => {
    mockServer({ 'GET /api/admin/nodes/9/reach': () => ok(detail) })
    renderWith([PERMISSIONS.NODE_READ], <NodeReachButton node={node} canWrite={false} onChecked={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: '日本 01 的大陆连通：被墙' }))
    expect(await screen.findByText('jp.example.com:443（TCP）')).toBeDefined()
    expect(screen.queryByRole('button', { name: /立即检测/ })).toBeNull()
    cleanup()

    renderWith([PERMISSIONS.NODE_READ], <NodeReachButton node={{ ...node, reach: null }} canWrite onChecked={vi.fn()} />)
    expect(screen.getByText('—')).toBeDefined()
    expect(screen.queryByRole('button')).toBeNull()
  })
})
