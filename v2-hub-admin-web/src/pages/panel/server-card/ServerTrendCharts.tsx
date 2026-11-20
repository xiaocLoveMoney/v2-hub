import { Card, Segmented } from 'antd'
import dayjs from 'dayjs'
import { useCallback, useState } from 'react'
import { api, type LatencyPoint, type MetricRange, type ServerMetrics } from '../../../api/client'
import { EChart } from '../../../components/EChart'
import { ErrorAlert } from '../../../components/ErrorAlert'
import { useRemote } from '../../../hooks/use-remote'
import { lineOption } from '../../dashboard/chart-options'
import { formatPercent, formatRate } from './server-card-format'

const RANGE_24H: MetricRange = '24h'
const RANGE_7D: MetricRange = '7d'
const RANGE_OPTIONS = [
  { value: RANGE_24H, label: '近 24 小时' },
  { value: RANGE_7D, label: '近 7 天' },
]
const CHART_HEIGHT = 220
const PERCENT_FULL = 100
const LABEL_FORMAT_24H = 'HH:mm'
const LABEL_FORMAT_7D = 'MM-DD HH:00'

/** 横轴文字：24 小时只写“时:分”，7 天写“月-日 时”。按本地时区。 */
function axisLabel(at: string, range: MetricRange): string {
  return dayjs(at).format(range === RANGE_24H ? LABEL_FORMAT_24H : LABEL_FORMAT_7D)
}

function ratio(used: number | null, total: number | null): number | null {
  if (used === null || total === null || total <= 0) {
    return null
  }
  return (used / total) * PERCENT_FULL
}

/** 把三家运营商的点按时间对齐到同一组横轴上。 */
function alignLatency(series: Record<'telecom' | 'unicom' | 'mobile', LatencyPoint[]>, range: MetricRange) {
  const times = [...new Set([...series.telecom, ...series.unicom, ...series.mobile].map((p) => p.at))].sort()
  const valueOf = (points: LatencyPoint[]) => {
    const byTime = new Map(points.map((p) => [p.at, p.avgMs]))
    return times.map((t) => byTime.get(t) ?? null)
  }
  return { labels: times.map((t) => axisLabel(t, range)), telecom: valueOf(series.telecom), unicom: valueOf(series.unicom), mobile: valueOf(series.mobile) }
}

function Charts({ data }: { data: ServerMetrics }) {
  const labels = data.points.map((p) => axisLabel(p.at, data.range))
  const usage = lineOption({
    labels,
    format: formatPercent,
    series: [
      { name: 'CPU', values: data.points.map((p) => p.cpu), color: '--hub-primary' },
      { name: '内存', values: data.points.map((p) => ratio(p.memUsed, p.memTotal)), color: '--hub-info' },
      { name: '磁盘', values: data.points.map((p) => ratio(p.diskUsed, p.diskTotal)), color: '--hub-warning' },
    ],
  })
  const net = lineOption({
    labels,
    format: formatRate,
    series: [
      { name: '上行', values: data.points.map((p) => p.netUpRate), color: '--hub-warning' },
      { name: '下行', values: data.points.map((p) => p.netDownRate), color: '--hub-success' },
    ],
  })
  const conns = lineOption({
    labels,
    format: (v) => v.toFixed(0),
    series: [
      { name: '在线用户', values: data.points.map((p) => p.onlineClients), color: '--hub-primary' },
      { name: 'TCP 连接', values: data.points.map((p) => p.tcpCount), color: '--hub-info' },
    ],
  })
  const latency = data.latency === null ? null : alignLatency(data.latency, data.range)
  return (
    <div className="server-trend-grid">
      <div>
        <div className="server-trend-title">资源占用</div>
        <EChart option={usage} height={CHART_HEIGHT} ariaLabel="CPU、内存、磁盘占用趋势" />
      </div>
      <div>
        <div className="server-trend-title">网络速率</div>
        <EChart option={net} height={CHART_HEIGHT} ariaLabel="上下行速率趋势" />
      </div>
      <div>
        <div className="server-trend-title">在线与连接</div>
        <EChart option={conns} height={CHART_HEIGHT} ariaLabel="在线用户与 TCP 连接数趋势" />
      </div>
      {latency !== null && (
        <div>
          <div className="server-trend-title">三网延迟</div>
          <EChart
            option={lineOption({
              labels: latency.labels,
              format: (v) => `${v.toFixed(0)} ms`,
              series: [
                { name: '电信', values: latency.telecom, color: '--hub-primary' },
                { name: '联通', values: latency.unicom, color: '--hub-danger' },
                { name: '移动', values: latency.mobile, color: '--hub-success' },
              ],
            })}
            height={CHART_HEIGHT}
            ariaLabel="电信、联通、移动延迟趋势"
          />
        </div>
      )}
    </div>
  )
}

/** 服务器详情页的趋势（Ruling-83、Ruling-84）：近 24 小时为 5 分钟原值，近 7 天按小时平均；三网延迟检测没开时不出延迟图。 */
export function ServerTrendCharts({ serverId }: { serverId: number }) {
  const [range, setRange] = useState<MetricRange>(RANGE_24H)
  const load = useCallback(
    (signal: AbortSignal) => api.GET('/api/admin/servers/{id}/metrics', { params: { path: { id: serverId }, query: { range } }, signal }),
    [serverId, range],
  )
  const metrics = useRemote<ServerMetrics>(load)
  return (
    <Card
      title="趋势"
      className="page-alert server-trend-card"
      extra={<Segmented aria-label="时间范围" options={RANGE_OPTIONS} value={range} onChange={(v) => setRange(v as MetricRange)} />}
      loading={metrics.isLoading && metrics.data === null}
    >
      <ErrorAlert error={metrics.error} />
      {metrics.data !== null && (metrics.data.points.length === 0 ? <div className="server-card-sub">还没有采样（服务器在线时每 5 分钟记一次）</div> : <Charts data={metrics.data} />)}
    </Card>
  )
}
