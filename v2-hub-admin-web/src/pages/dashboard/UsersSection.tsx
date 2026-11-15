import { Table, type TableProps } from 'antd'
import { useCallback, useMemo, useState } from 'react'
import { api, type AnalyticsUsers } from '../../api/client'
import { EChart } from '../../components/EChart'
import { ErrorAlert } from '../../components/ErrorAlert'
import { useRemote } from '../../hooks/use-remote'
import { formatBytes } from '../users/quota'
import { barOption, lineOption } from './chart-options'
import { CHART_HEIGHT, DEFAULT_DAYS, shortDate, type AnalyticsDays } from './analytics-format'
import { BarList, DaysSwitch, Figure, Panel, Section, type BarRow } from './shared'

type PlanRow = AnalyticsUsers['plans'][number]
type TopUser = AnalyticsUsers['topUsers'][number]
type SubscribeClient = AnalyticsUsers['subscription']['clients'][number]['client']

const NO_PLAN = '没有套餐'
const NO_PLAN_KEY = 'none'
const formatCount = (v: number) => String(v)

/** 订阅客户端归类的显示名，键与 hub-api.yaml 的枚举一致。 */
const CLIENT_LABELS: Record<SubscribeClient, string> = {
  clash: 'Clash / Mihomo',
  singbox: 'sing-box',
  v2rayn: 'v2rayN',
  v2rayng: 'v2rayNG',
  shadowrocket: 'Shadowrocket',
  stash: 'Stash',
  quantumult: 'Quantumult',
  surge: 'Surge',
  loon: 'Loon',
  hiddify: 'Hiddify',
  nekobox: 'NekoBox',
  other: '其他',
}

const planColumns: TableProps<PlanRow>['columns'] = [
  { title: '套餐', dataIndex: 'name', render: (name: string, p: PlanRow) => (p.planId === null ? NO_PLAN : name) },
  { title: '用户', dataIndex: 'users', align: 'right' },
  { title: '其中有效', dataIndex: 'activeUsers', align: 'right' },
]

const topUserColumns: TableProps<TopUser>['columns'] = [
  { title: '用户', dataIndex: 'username' },
  { title: '套餐', dataIndex: 'planName', render: (v: string) => (v === '' ? NO_PLAN : v) },
  { title: '流量', dataIndex: 'bytes', align: 'right', render: (v: number) => formatBytes(v) },
]

/** 总览的用户分区（user:read）：增长、活跃、状态与到期分布、套餐分布、流量排行、订阅客户端。 */
export function UsersSection() {
  const [days, setDays] = useState<AnalyticsDays>(DEFAULT_DAYS)
  const load = useCallback((signal: AbortSignal) => api.GET('/api/admin/analytics/users', { params: { query: { days } }, signal }), [days])
  const state = useRemote<AnalyticsUsers>(load)
  const d = state.data

  const charts = useMemo(() => {
    if (d === null) {
      return null
    }
    const labels = d.growth.map((g) => shortDate(g.date))
    return {
      total: lineOption({ labels, series: [{ name: '累计用户', values: d.growth.map((g) => g.totalUsers), color: '--hub-primary' }], format: formatCount }),
      added: barOption({ labels, series: [{ name: '新增用户', values: d.growth.map((g) => g.newUsers), color: '--hub-primary' }], format: formatCount }),
      active: lineOption({
        labels: d.activeDays.map((a) => shortDate(a.date)),
        series: [{ name: '活跃用户', values: d.activeDays.map((a) => a.users), color: '--hub-primary' }],
        format: formatCount,
      }),
      pulls: lineOption({
        labels: d.subscription.days.map((s) => shortDate(s.date)),
        series: [
          { name: '拉取次数', values: d.subscription.days.map((s) => s.pulls), color: '--hub-primary' },
          { name: '拉取用户', values: d.subscription.days.map((s) => s.users), color: '--hub-info' },
        ],
        format: formatCount,
      }),
    }
  }, [d])

  if (d === null || charts === null) {
    return (
      <Section toolbar={<DaysSwitch value={days} onChange={setDays} />}>
        <ErrorAlert error={state.error} />
      </Section>
    )
  }
  const statusRows: BarRow[] = [
    { key: 'active', label: '有效', value: d.status.active },
    { key: 'limited', label: '流量用完', value: d.status.limited },
    { key: 'expired', label: '已到期', value: d.status.expired },
    { key: 'disabled', label: '已停用', value: d.status.disabled },
  ]
  const expiryRows: BarRow[] = [
    { key: 'expired', label: '已过期', value: d.expiry.expired },
    { key: 'within7d', label: '7 日内到期', value: d.expiry.within7d },
    { key: 'within30d', label: '8–30 日到期', value: d.expiry.within30d },
    { key: 'later', label: '30 日以后', value: d.expiry.later },
    { key: 'never', label: '不过期', value: d.expiry.never },
  ]
  const clientRows: BarRow[] = d.subscription.clients.map((c) => ({
    key: c.client,
    label: CLIENT_LABELS[c.client],
    value: c.users,
    text: `${c.users} 人 · ${c.pulls} 次`,
  }))
  const formatRows: BarRow[] = d.subscription.formats.map((f) => ({ key: f.format, label: f.format, value: f.pulls, text: `${f.pulls} 次` }))
  const latest = d.growth[d.growth.length - 1]

  return (
    <Section toolbar={<DaysSwitch value={days} onChange={setDays} />}>
      <ErrorAlert error={state.error} />
      <div className="figure-grid">
        <Figure label="今日活跃" value={d.dau} hint="今天有流量的用户" />
        <Figure label="近 7 日活跃" value={d.wau} />
        <Figure label="近 30 日活跃" value={d.mau} />
        <Figure label="沉默用户" value={d.silentUsers} hint="有效但近 7 日没有流量" />
        <Figure label="7 日内到期且没开自动续费" value={d.expiry.within7dNoAutoRenew} hint="可能流失，值得提醒" />
      </div>
      <div className="analytics-grid">
        <Panel title="累计用户" extra={latest === undefined ? undefined : `现有 ${latest.totalUsers} 人`}>
          <EChart option={charts.total} height={CHART_HEIGHT} ariaLabel="累计用户曲线" />
        </Panel>
        <Panel title="每日新增">
          <EChart option={charts.added} height={CHART_HEIGHT} ariaLabel="每日新增用户" />
        </Panel>
        <Panel title="每日活跃" extra="当日有流量的用户数">
          <EChart option={charts.active} height={CHART_HEIGHT} ariaLabel="每日活跃用户" />
        </Panel>
        <Panel title="订阅拉取" extra="只有订阅记录保留期内的数据">
          <EChart option={charts.pulls} height={CHART_HEIGHT} ariaLabel="每日订阅拉取次数与用户数" />
        </Panel>
        <Panel title="状态分布">
          <BarList rows={statusRows} />
        </Panel>
        <Panel title="到期分布">
          <BarList rows={expiryRows} />
        </Panel>
        <Panel title="订阅客户端" extra="按 User-Agent 归类">
          <BarList rows={clientRows} emptyText="这段时间没有订阅拉取" />
        </Panel>
        <Panel title="订阅格式">
          <BarList rows={formatRows} emptyText="这段时间没有订阅拉取" />
        </Panel>
        <Panel title="套餐分布">
          <Table<PlanRow> size="small" rowKey={(p) => p.planId ?? NO_PLAN_KEY} columns={planColumns} dataSource={d.plans} pagination={false} />
        </Panel>
        <Panel title="流量排行" extra="所选时间内最多的 10 个用户">
          <Table<TopUser> size="small" rowKey="userId" columns={topUserColumns} dataSource={d.topUsers} pagination={false} />
        </Panel>
      </div>
    </Section>
  )
}
