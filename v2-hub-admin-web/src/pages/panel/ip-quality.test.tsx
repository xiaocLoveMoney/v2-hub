import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { IpCheck, IpCheckDetail, IpCheckResult } from '../../api/client'
import { PERMISSIONS } from '../../menu'
import { TEST_CSRF, mockServer, ok, renderWith } from '../../test/harness'
import { serverOf } from '../../test/host-fixtures'
import { IpQualityCard } from './IpQualityCard'
import { diffResults, flattenResult } from './ip-quality-diff'

const LIST_PATH = '/api/admin/servers/1/ip-checks'
const SERVER_RW = [PERMISSIONS.SERVER_READ, PERMISSIONS.SERVER_WRITE]

function checkOf(over: Partial<IpCheck> = {}): IpCheck {
  return {
    id: 1, serverId: 1, status: 'succeeded', origin: 'manual', scope: 'full', hostProbe: 'done', ip: '9.9.9.9', ipSource: 'host',
    country: 'DE', regCountry: 'US', ipKind: 'broadcast', asn: 64500, asOrg: 'Example Hosting', usageType: 'hosting', riskLevel: 'medium',
    dnsblListed: 1, dnsblTotal: 15, mediaOk: 4, mediaTotal: 6, port25: 'blocked', errorCode: '', lastError: '', createdByName: 'root',
    createdAt: '2026-11-19T02:00:00Z', startedAt: '2026-11-19T02:00:01Z', finishedAt: '2026-11-19T02:00:30Z',
    ...over,
  }
}

function resultOf(over: { netflix?: 'yes' | 'no'; listed?: number; ipqs?: 'ok' | 'not_configured' } = {}): IpCheckResult {
  return {
    schema: 1,
    stacks: {
      v4: {
        ip: '9.9.9.9', ipSource: 'host',
        basics: { country: 'DE', region: 'Hessen', city: 'Frankfurt', timezone: '+01:00', latitude: 50.1, longitude: 8.6, asn: 64500, asOrg: 'Example', prefix: '9.9.9.0/24', regCountry: 'US', registry: 'arin', ipKind: 'broadcast' },
        sources: [
          { key: 'cymru', status: 'ok', usageType: '', companyType: '', score: null, factors: { country: 'US', proxy: null, tor: null, vpn: null, server: null, abuser: null, robot: null } },
          { key: 'ipqs', status: over.ipqs ?? 'ok', usageType: 'hosting', companyType: '', score: { value: 80, level: 'medium' }, factors: { country: 'DE', proxy: true, tor: false, vpn: false, server: true, abuser: false, robot: false } },
        ],
        dnsbl: { listed: over.listed ?? 0, policy: 0, clean: 15, unavailable: 2, items: [{ key: 'bl.spamcop.net', result: (over.listed ?? 0) > 0 ? 'listed' : 'clean', answer: '' }] },
        egress: { ip: '9.9.9.9', country: 'DE' },
        media: { status: 'done', items: [{ key: 'netflix', status: over.netflix ?? 'yes', region: 'DE', method: 'native', evidence: { 'nfo.code': '200' } }] },
        mail: { status: 'done', port25: 'blocked', items: [{ key: 'gmail', mx: 'gmail-smtp-in.l.google.com', result: 'timeout', code: '' }] },
      },
    },
  }
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('ip-quality-diff', () => {
  it('压平后路径稳定，数据源与服务按 key 成行', () => {
    const flat = flattenResult(resultOf())
    const byPath = new Map(flat.map((it) => [it.path, it]))
    expect(byPath.get('v4.basics.ipKind')?.text).toBe('广播 IP')
    expect(byPath.get('v4.sources.ipqs.factors.proxy')?.text).toBe('是')
    expect(byPath.get('v4.media.netflix.status')?.text).toBe('可用')
    expect(byPath.get('v4.mail.gmail')?.text).toBe('超时')
    // 证据不参与对比
    expect(flat.some((it) => it.path.includes('evidence'))).toBe(false)
    expect(flattenResult(null)).toEqual([])
  })

  it('只标出变化的项，一边缺的项也算变化', () => {
    const rows = diffResults(resultOf(), resultOf({ netflix: 'no', listed: 1, ipqs: 'not_configured' }))
    const changed = rows.filter((r) => r.isChanged).map((r) => r.path)
    expect(changed).toContain('v4.media.netflix.status')
    expect(changed).toContain('v4.dnsbl.listed')
    expect(changed).toContain('v4.sources.ipqs.status')
    expect(changed).not.toContain('v4.basics.country')
    const netflix = rows.find((r) => r.path === 'v4.media.netflix.status')
    expect(netflix).toMatchObject({ older: '可用', newer: '不可用' })
    const empty = diffResults(null, resultOf())
    expect(empty.every((r) => r.isChanged && r.older === '—')).toBe(true)
  })
})

describe('IpQualityCard', () => {
  it('只有 server:read 时没有立即检测，能看摘要', async () => {
    mockServer({ [`GET ${LIST_PATH}`]: () => ok({ items: [checkOf()], total: 1 }) })
    renderWith([PERMISSIONS.SERVER_READ], <IpQualityCard server={serverOf()} />)
    expect(await screen.findByText('9.9.9.9')).toBeDefined()
    expect(screen.queryByRole('button', { name: '立即检测' })).toBeNull()
    expect(screen.getByText('广播 IP')).toBeDefined()
    expect(screen.getByText('4/6 可用')).toBeDefined()
    expect(screen.getByText('被封')).toBeDefined()
  })

  it('立即检测带 CSRF；没有 host:write 时说明只做平台侧', async () => {
    const items: IpCheck[] = []
    const calls = mockServer({
      [`GET ${LIST_PATH}`]: () => ok({ items, total: items.length }),
      [`POST ${LIST_PATH}`]: () => {
        items.push(checkOf({ status: 'queued', scope: 'platform', hostProbe: 'no_permission' }))
        return ok(items[0])
      },
    })
    renderWith(SERVER_RW, <IpQualityCard server={serverOf()} />)
    expect(await screen.findByText('还没有检测过。')).toBeDefined()
    expect(screen.getByText(/没有 host:write，只做平台侧/)).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: '立即检测' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')?.csrf).toBe(TEST_CSRF)
    const busy = await screen.findByText('检测中…')
    expect(busy.closest('button')).toHaveProperty('disabled', true)
  })

  it('历史里勾选两条完成的检测可逐项对比，默认只看变化项', async () => {
    const older: IpCheckDetail = { ...checkOf({ id: 1, createdAt: '2026-11-10T02:00:00Z' }), result: resultOf() }
    const newer: IpCheckDetail = { ...checkOf({ id: 2, createdAt: '2026-11-19T02:00:00Z' }), result: resultOf({ netflix: 'no' }) }
    const running = checkOf({ id: 3, status: 'running', createdAt: '2026-11-19T03:00:00Z' })
    mockServer({
      [`GET ${LIST_PATH}`]: () => ok({ items: [running, newer, older], total: 3 }),
      'GET /api/admin/ip-checks/1': () => ok(older),
      'GET /api/admin/ip-checks/2': () => ok(newer),
      'GET /api/admin/ip-checks/3': () => ok({ ...running, result: null }),
    })
    renderWith([PERMISSIONS.SERVER_READ], <IpQualityCard server={serverOf()} />)
    fireEvent.click(await screen.findByRole('button', { name: '历史与对比' }))
    const drawer = await screen.findByRole('dialog')
    // 没结束的检测不能选来对比
    const boxes = await within(drawer).findAllByRole('checkbox')
    expect(boxes[0]).toHaveProperty('disabled', true)
    const compare = within(drawer).getByRole('button', { name: '对比所选' })
    expect(compare).toHaveProperty('disabled', true)
    fireEvent.click(boxes[1])
    fireEvent.click(boxes[2])
    fireEvent.click(compare)
    expect(await within(drawer).findByText('Netflix · 状态')).toBeDefined()
    expect(within(drawer).getByText('变化')).toBeDefined()
    expect(within(drawer).getByText(/1 项有变化/)).toBeDefined()
    expect(within(drawer).queryByText('注册机构')).toBeNull()
  })
})
