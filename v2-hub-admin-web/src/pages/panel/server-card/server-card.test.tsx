import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { LatencyPoint, Server, ServerTrend } from '../../../api/client'
import { PERMISSIONS } from '../../../menu'
import { TEST_CSRF, mockServer, ok, openTab, renderWith } from '../../../test/harness'
import { serverOf } from '../../../test/host-fixtures'
import { ServersPage } from '../ServersPage'
import { ServerStatusCard } from './ServerStatusCard'
import { daysBetween, formatBytes, formatPrice, formatRate, latencyLevel, percentOf, usageLevel } from './server-card-format'

const GIB = 1024 ** 3
const noop = () => undefined

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

/** 一台在线、资源与账单都齐全的服务器。 */
function busyServer(over: Partial<Server> = {}): Server {
  return serverOf({
    name: 'dmit-us',
    region: 'US',
    publicIpv4: '203.0.113.7',
    publicIpv6: '2001:db8::7',
    onlineClients: 12,
    resources: {
      cpu: 3.27, cpuCores: 1, memUsed: 309 * 1024 ** 2, memTotal: 965 * 1024 ** 2, swapUsed: 0, swapTotal: 0,
      diskUsed: 3.3 * GIB, diskTotal: 19.6 * GIB, load1: 0, load5: 0.1, load15: 0.05, uptimeSeconds: 83 * 86400,
      tcpCount: 42, udpCount: 3, netUpRate: 29 * 1024, netDownRate: 29.5 * 1024, netSent: GIB, netRecv: 2 * GIB,
      coreState: 'running', coreVersion: '26.10.21',
    },
    billing: {
      priceCents: 4990, priceCurrency: 'USD', billingCycle: 'year', expiresOn: '2099-01-01',
      trafficQuotaBytes: 1024 * GIB, trafficResetDay: 1, trafficMode: 'both',
    },
    traffic: { cycleStart: '2026-11-01', nextResetOn: '2099-01-01', sentBytes: 83 * GIB, recvBytes: 90 * GIB, usedBytes: 173 * GIB },
    ...over,
  })
}

function point(at: string, avgMs: number | null, lossPercent = 0): LatencyPoint {
  return { at, avgMs, lossPercent }
}

describe('格式化', () => {
  it('字节、速率、百分比与价格', () => {
    expect(formatBytes(309 * 1024 ** 2)).toBe('309 MB')
    expect(formatBytes(1024 * GIB)).toBe('1.00 TB')
    expect(formatRate(29 * 1024)).toBe('29.0 KB/s')
    expect(percentOf(1, 0)).toBe(0)
    expect(percentOf(3, 2)).toBe(100)
    expect(usageLevel(95)).toBe('danger')
    expect(usageLevel(75)).toBe('warn')
    expect(formatPrice(4990, 'USD', 'year')).toContain('49.90')
    expect(formatPrice(4990, 'USD', 'year')).toMatch(/\/年$/)
    expect(daysBetween('2026-11-20', '2026-12-01')).toBe(11)
  })

  it('延迟状态：全丢与高延迟为红，有丢包为橙，没数据为空', () => {
    expect(latencyLevel(undefined)).toBe('empty')
    expect(latencyLevel(point('t', null, 100))).toBe('danger')
    expect(latencyLevel(point('t', 350))).toBe('danger')
    expect(latencyLevel(point('t', 130, 2))).toBe('warn')
    expect(latencyLevel(point('t', 130))).toBe('ok')
  })
})

describe('ServerStatusCard', () => {
  it('显示资源四宫格、速率、剩余流量、到期、价格与 V4 / V6', () => {
    const trend: ServerTrend = {
      serverId: 1,
      net: [],
      latency: {
        telecom: [point('2026-11-20T01:00:00Z', 128), point('2026-11-20T01:30:00Z', 130, 2)],
        unicom: [point('2026-11-20T01:30:00Z', 135)],
        mobile: [],
      },
    }
    renderWith([PERMISSIONS.SERVER_READ], (
      <MemoryRouter>
        <ServerStatusCard server={busyServer()} trend={trend} canEditBilling={false} onEditBilling={noop} />
      </MemoryRouter>
    ))
    expect(screen.getByText('dmit-us')).toBeDefined()
    expect(screen.getByText('V4')).toBeDefined()
    expect(screen.getByText('V6')).toBeDefined()
    expect(screen.getByText('1 核')).toBeDefined()
    expect(screen.getByText('309 MB / 965 MB')).toBeDefined()
    expect(screen.getByText('29.0 KB/s')).toBeDefined()
    expect(screen.getByText(/剩余 851 GB/)).toBeDefined()
    expect(screen.getByText('173 GB / 1.00 TB')).toBeDefined()
    expect(screen.getByText('83')).toBeDefined()
    expect(screen.getByText(/49\.90.*\/年/)).toBeDefined()
    // 三网：电信取最近一次 130 ms、丢包 2%；移动没有数据
    expect(screen.getByText('130')).toBeDefined()
    expect(screen.getByText('135')).toBeDefined()
    expect(screen.getByRole('img', { name: '移动延迟历史' })).toBeDefined()
    // 没有 server:write 不出编辑账单按钮
    expect(screen.queryByRole('button', { name: '账单与流量额度' })).toBeNull()
  })

  it('没有资源快照、没填额度、延迟检测关着时给出说明，不出延迟区', () => {
    renderWith([PERMISSIONS.SERVER_READ], (
      <MemoryRouter>
        <ServerStatusCard server={serverOf({ status: 'offline', lastSeenAt: '2026-11-20T00:00:00Z' })} canEditBilling={false} onEditBilling={noop} />
      </MemoryRouter>
    ))
    expect(screen.getByText('面板还没有报过资源数据')).toBeDefined()
    expect(screen.getByText('未设额度')).toBeDefined()
    expect(screen.getByText(/最近在线/)).toBeDefined()
    expect(screen.queryByText('电信')).toBeNull()
    expect(document.querySelector('.server-card-offline')).not.toBeNull()
  })
})

describe('服务器页的页签（Ruling-88）', () => {
  it('默认是管理表格；运行状态页签是只读卡片，只能改账单，账单弹窗提交换算后的内容并带 CSRF 头', async () => {
    const srv = busyServer()
    const calls = mockServer({
      'GET /api/admin/servers': () => ok([srv]),
      'GET /api/admin/servers/trends': () => ok([]),
      'PUT /api/admin/servers/1/billing': () => ok(srv),
    })
    renderWith([PERMISSIONS.SERVER_READ, PERMISSIONS.SERVER_WRITE], (
      <MemoryRouter>
        <ServersPage />
      </MemoryRouter>
    ))
    expect(await screen.findByRole('columnheader', { name: 'CPU / 内存' })).toBeDefined()
    expect(calls.some((c) => c.path === '/api/admin/servers/trends')).toBe(false)

    await openTab('运行状态')
    const card = (await screen.findByText('dmit-us')).closest('.ant-card') as HTMLElement
    expect(within(card).queryByRole('button', { name: /编\s*辑/ })).toBeNull()
    expect(within(card).queryByRole('button', { name: /移\s*除/ })).toBeNull()
    await waitFor(() => expect(calls.some((c) => c.path === '/api/admin/servers/trends')).toBe(true))

    fireEvent.click(within(card).getByRole('button', { name: '账单与流量额度' }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('价格'), { target: { value: '59.9' } })
    fireEvent.change(within(dialog).getByLabelText('校准本周期已用（GB）'), { target: { value: '200' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /保\s*存/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    const put = calls.find((c) => c.method === 'PUT')
    expect(put?.csrf).toBe(TEST_CSRF)
    expect(put?.body).toMatchObject({
      priceCents: 5990, priceCurrency: 'USD', billingCycle: 'year', expiresOn: '2099-01-01',
      trafficQuotaBytes: 1024 * GIB, trafficResetDay: 1, trafficMode: 'both', trafficUsedBytes: 200 * GIB,
    })
  })
})
