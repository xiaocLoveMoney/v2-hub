import { Alert, Progress, Segmented, Space, Table, Tag, type TableProps } from 'antd'
import { useCallback, useMemo, useState } from 'react'
import { Link } from 'react-router'
import { api, type AnalyticsServerLoad, type AnalyticsServers, type MetricRange, type ReachProblem } from '../../api/client'
import { EChart } from '../../components/EChart'
import { ErrorAlert } from '../../components/ErrorAlert'
import { useRemote } from '../../hooks/use-remote'
import { ROUTES } from '../../menu'
import { ReachTag } from '../panel/reach'
import { formatTime } from '../system/format'
import { lineOption } from './chart-options'
import { CHART_HEIGHT, EMPTY_TEXT } from './analytics-format'
import { Panel, Section } from './shared'

type Availability = AnalyticsServers['availability'][number]
type ReachSummary = NonNullable<AnalyticsServers['reach']>

const DEFAULT_RANGE: MetricRange = '24h'
const RANGE_OPTIONS: { value: MetricRange; label: string }[] = [
  { value: '24h', label: '近 24 小时' },
  { value: '7d', label: '近 7 天' },
]
const GOOD_AVAILABILITY = 99
const CPU_DECIMALS = 1
const formatCount = (v: number) => String(v)
const serverPath = (id: number) => ROUTES.SERVER_DETAIL.replace(':id', String(id))

const dayTimeFormatter = new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false })
const timeFormatter = new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false })

const availabilityColumns: TableProps<Availability>['columns'] = [
  { title: '服务器', dataIndex: 'name', render: (name: string, s: Availability) => <Link to={serverPath(s.serverId)}>{name}</Link> },
  {
    title: '近 7 日可用率',
    dataIndex: 'percent',
    render: (v: number) => <Progress percent={v} size="small" status={v >= GOOD_AVAILABILITY ? 'success' : 'exception'} format={(p) => `${p ?? 0}%`} />,
  },
  { title: '采样', key: 'samples', align: 'right', render: (_: unknown, s: Availability) => `${s.samples} / ${s.expected}` },
]

const loadColumns: TableProps<AnalyticsServerLoad>['columns'] = [
  { title: '服务器', dataIndex: 'name', render: (name: string, s: AnalyticsServerLoad) => <Link to={serverPath(s.serverId)}>{name}</Link> },
  { title: 'CPU', dataIndex: 'cpu', align: 'right', render: (v: number | null) => (v === null ? EMPTY_TEXT : `${v.toFixed(CPU_DECIMALS)}%`) },
  { title: '在线', dataIndex: 'onlineClients', align: 'right' },
]

const problemColumns: TableProps<ReachProblem>['columns'] = [
  { title: '地址', key: 'target', render: (_: unknown, p: ReachProblem) => `${p.address}:${p.port}` },
  { title: '结论', key: 'verdict', render: (_: unknown, p: ReachProblem) => <ReachTag verdict={p.verdict} scope={p.scope} cnOk={p.cnOk} cnTotal={p.cnTotal} /> },
  { title: '服务器', dataIndex: 'servers', render: (v: string[]) => v.join('、') },
  { title: '节点', key: 'nodes', render: (_: unknown, p: ReachProblem) => `${p.nodes.join('、')}${p.nodeCount > p.nodes.length ? ` 等 ${p.nodeCount} 个` : ''}` },
  { title: '检测时间', dataIndex: 'checkedAt', render: (v: string | null) => formatTime(v) },
]

function ReachPanel({ reach }: { reach: ReachSummary }) {
  const c = reach.counts
  return (
    <Panel title="大陆连通" extra="由第三方探测点从大陆与境外各测一次">
      <Space wrap className="reach-counts">
        <Tag color="green">正常 {c.ok}</Tag>
        <Tag color="red">被墙 {c.blocked}</Tag>
        <Tag color="orange">部分不通 {c.partial}</Tag>
        <Tag color="gold">端口不通 {c.portDown}</Tag>
        <Tag>未知 {c.unknown}</Tag>
        <Tag>未检测 {c.pending}</Tag>
      </Space>
      {reach.problems.length === 0 ? (
        <Alert type="success" showIcon title="没有大陆连不上的节点地址" />
      ) : (
        <Table<ReachProblem>
          size="small"
          rowKey={(p) => `${p.address}:${p.port}/${p.transport}`}
          columns={problemColumns}
          dataSource={reach.problems}
          pagination={false}
        />
      )}
    </Panel>
  )
}

/** 总览的服务器分区（server:read）：近 7 日可用率、在线连接数曲线、负载排行与大陆连通汇总。 */
export function ServersSection() {
  const [range, setRange] = useState<MetricRange>(DEFAULT_RANGE)
  const load = useCallback((signal: AbortSignal) => api.GET('/api/admin/analytics/servers', { params: { query: { range } }, signal }), [range])
  const state = useRemote<AnalyticsServers>(load)
  const d = state.data

  const online = useMemo(() => {
    if (d === null) {
      return null
    }
    const formatter = range === '7d' ? dayTimeFormatter : timeFormatter
    return lineOption({
      labels: d.online.map((p) => formatter.format(new Date(p.at))),
      series: [{ name: '在线连接数', values: d.online.map((p) => p.connections), color: '--hub-primary' }],
      format: formatCount,
    })
  }, [d, range])
  const toolbar = <Segmented<MetricRange> aria-label="在线曲线范围" options={RANGE_OPTIONS} value={range} onChange={setRange} />

  if (d === null || online === null) {
    return (
      <Section toolbar={toolbar}>
        <ErrorAlert error={state.error} />
      </Section>
    )
  }
  return (
    <Section toolbar={toolbar}>
      <ErrorAlert error={state.error} />
      {d.reach !== null && <ReachPanel reach={d.reach} />}
      <Panel title="在线连接数" extra="各服务器在线 client 数之和；同一用户连多台会重复计">
        {d.online.length === 0 ? <div className="chart-empty">这段时间没有负载采样</div> : <EChart option={online} height={CHART_HEIGHT} ariaLabel="全站在线连接数曲线" />}
      </Panel>
      <Panel title="可用率" extra="按负载采样的时段数算；停用过的时段也算不可用">
        <Table<Availability> size="small" rowKey="serverId" columns={availabilityColumns} dataSource={d.availability} pagination={false} />
      </Panel>
      <div className="analytics-grid">
        <Panel title="CPU 最高" extra="最近 15 分钟的采样">
          <Table<AnalyticsServerLoad> size="small" rowKey="serverId" columns={loadColumns} dataSource={d.topCpu} pagination={false} />
        </Panel>
        <Panel title="在线最多" extra="最近 15 分钟的采样">
          <Table<AnalyticsServerLoad> size="small" rowKey="serverId" columns={loadColumns} dataSource={d.topOnline} pagination={false} />
        </Panel>
      </div>
    </Section>
  )
}
