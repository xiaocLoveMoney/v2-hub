import { cleanup, fireEvent, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { PERMISSIONS } from '../menu'
import { mockServer, ok, renderWith } from '../test/harness'
import { DashboardPage } from './DashboardPage'
import { FinanceStatsPage } from './shop/FinanceStatsPage'

const GIB = 1024 ** 3
const days = ['2026-11-08', '2026-11-09', '2026-11-10', '2026-11-11', '2026-11-12', '2026-11-13', '2026-11-14'].map((date, i) => ({ date, bytes: i * GIB }))
const dashboard = {
  users: { total: 120, active: 98, newToday: 3, new7d: 21, online: 17 },
  traffic: { today: 6 * GIB, days },
  servers: { total: 3, online: 2, nodeCount: 9, offline: [{ id: 2, name: 'jp-1', status: 'offline', offlineSince: '2026-11-14T00:00:00Z' }] },
  finance: { todaySpend: 990, todayTopup: 5000, monthSpend: 19800, monthTopup: 50000 },
  tickets: { open: 4 },
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('DashboardPage（docs/03 §5.20）', () => {
  it('显示各区块与不在线服务器；缺权限的区块不显示', async () => {
    mockServer({ 'GET /api/admin/dashboard': () => ok(dashboard) })
    renderWith([PERMISSIONS.USER_READ, PERMISSIONS.SERVER_READ, PERMISSIONS.FINANCE_READ, PERMISSIONS.TICKET_READ], <MemoryRouter><DashboardPage /></MemoryRouter>)
    expect(await screen.findByText('用户总数')).toBeDefined()
    expect(screen.getByText('2 / 3')).toBeDefined()
    expect(screen.getByText('1 台服务器不在线')).toBeDefined()
    expect(screen.getByRole('link', { name: 'jp-1' }).getAttribute('href')).toBe('/servers/2')
    expect(screen.getByRole('img', { name: '近 7 日流量' })).toBeDefined()
    expect(screen.getByText('待回复工单')).toBeDefined()
    cleanup()

    mockServer({ 'GET /api/admin/dashboard': () => ok({ users: null, traffic: null, servers: dashboard.servers, finance: null, tickets: null }) })
    renderWith([PERMISSIONS.SERVER_READ], <MemoryRouter><DashboardPage /></MemoryRouter>)
    expect(await screen.findByText('服务器在线')).toBeDefined()
    expect(screen.queryByText('用户总数')).toBeNull()
    expect(screen.queryByText('本月消费 / 充值')).toBeNull()
  })
})

describe('FinanceStatsPage 按月与导出', () => {
  it('切到按月显示月份；导出 CSV 走同源请求并交给浏览器下载', async () => {
    const month = { granularity: 'month', days: [{ date: '2026-10-01', topup: 100, spend: 0, rebate: 0, orderCount: 0 }, { date: '2026-11-01', topup: 5000, spend: 990, rebate: 0, orderCount: 1 }],
      totals: { balance: 4110, topup: 5100, spend: 990, rebate: 0 } }
    const calls = mockServer({
      'GET /api/admin/finance/stats': () => ok(month),
      'GET /api/admin/finance/stats.csv': () => new Response('﻿日期\n', { headers: { 'Content-Type': 'text/csv', 'Content-Disposition': 'attachment; filename=finance-month-2026-11-14.csv' } }),
    })
    const createURL = vi.fn(() => 'blob:finance')
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: createURL, revokeObjectURL: vi.fn() }))
    renderWith([PERMISSIONS.FINANCE_READ], <FinanceStatsPage />)
    fireEvent.click(await screen.findByText('按月'))
    expect(await screen.findByText('2026-11')).toBeDefined()
    expect(screen.getAllByText('月份').length).toBeGreaterThan(0)
    fireEvent.click(screen.getByRole('button', { name: /导出 CSV/ }))
    await waitFor(() => expect(calls.some((c) => c.path === '/api/admin/finance/stats.csv')).toBe(true))
    await waitFor(() => expect(createURL).toHaveBeenCalled())
  })
})
