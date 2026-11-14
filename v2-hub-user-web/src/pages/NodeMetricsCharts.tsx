import { useMemo, useState } from 'react'
import { api, type NodeMetricPoint, type NodeMetrics } from '../api/client'
import { cssColor } from '../components/css-color'
import { EChart } from '../components/EChart'
import { Alert, Button, Loading } from '../components/ui'
import { useRemote } from '../hooks/use-remote'
import { useI18n } from '../i18n/i18n-context'
import { LOCALE_TAG } from '../i18n/locale'

type MetricRange = NodeMetrics['range']

const RANGE_24H: MetricRange = '24h'
const RANGE_7D: MetricRange = '7d'
const RANGES: readonly MetricRange[] = [RANGE_24H, RANGE_7D]
const MS_PER_MINUTE = 60_000
/** 两种范围的采样间隔；相邻两点隔得超过两个间隔就断开曲线（服务器那段时间不在线）。 */
const STEP_MS: Record<MetricRange, number> = { '24h': 5 * MS_PER_MINUTE, '7d': 60 * MS_PER_MINUTE }
const GAP_FACTOR = 2
const CHART_HEIGHT = 170
const AREA_OPACITY = 0.18
const PERCENT_MAX = 100

type Pick = (p: NodeMetricPoint) => number | null

/** 把点转成 [时间, 值] 序列；断档处插一个空值让折线断开。 */
function toSeries(points: NodeMetricPoint[], pick: Pick, step: number): [number, number | null][] {
  return points.reduce<[number, number | null][]>((acc, p, i) => {
    const at = new Date(p.at).getTime()
    const prev = points[i - 1]
    if (prev !== undefined && at - new Date(prev.at).getTime() > step * GAP_FACTOR) {
      acc.push([at - step, null])
    }
    acc.push([at, pick(p)])
    return acc
  }, [])
}

interface MetricChartProps {
  title: string
  points: NodeMetricPoint[]
  pick: Pick
  step: number
  color: string
  unit: string
  max?: number
}

/** 一张平滑折线面积图。 */
function MetricChart({ title, points, pick, step, color, unit, max }: MetricChartProps) {
  const { locale } = useI18n()
  const option = useMemo(() => {
    const timeText = (ms: number) => new Date(ms).toLocaleString(LOCALE_TAG[locale], { hour12: false, month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' })
    return {
      grid: { left: 40, right: 12, top: 12, bottom: 24 },
      tooltip: {
        trigger: 'axis',
        formatter: (items: { value: [number, number | null] }[]) => {
          const [at, value] = items[0]?.value ?? [0, null]
          return `${timeText(at)}<br/>${title}  ${value === null ? '—' : `${Math.round(value * 10) / 10}${unit}`}`
        },
      },
      xAxis: { type: 'time', splitLine: { show: false } },
      yAxis: { type: 'value', min: 0, max, minInterval: unit === '' ? 1 : undefined, splitLine: { lineStyle: { type: 'dashed', color: cssColor('--hub-divider', '#f1f3f6') } } },
      series: [
        {
          name: title,
          type: 'line',
          smooth: true,
          showSymbol: false,
          connectNulls: false,
          data: toSeries(points, pick, step),
          lineStyle: { color, width: 2 },
          itemStyle: { color },
          areaStyle: { color, opacity: AREA_OPACITY },
        },
      ],
    }
  }, [title, points, pick, step, color, unit, max, locale])
  return (
    <>
      <h3 className="hub-metric-title">{title}</h3>
      <EChart option={option} height={CHART_HEIGHT} ariaLabel={title} />
    </>
  )
}

const pickOnline: Pick = (p) => p.onlineClients
const pickLoad: Pick = (p) => p.load ?? null

/** 节点详情里的“在线人数”“负载”趋势（docs/03 §5.5，Ruling-65）：近 24 小时 / 近 7 天切换。 */
export function NodeMetricsCharts({ nodeId }: { nodeId: number }) {
  const { t } = useI18n()
  const [range, setRange] = useState<MetricRange>(RANGE_24H)
  const load = useMemo(
    () => (signal: AbortSignal) => api.GET('/api/user/nodes/{id}/metrics', { params: { path: { id: nodeId }, query: { range } }, signal }),
    [nodeId, range],
  )
  const metrics = useRemote<NodeMetrics>(load)
  const points = metrics.data?.range === range ? metrics.data.points : null
  return (
    <section aria-label={t('nodes.metrics')}>
      <div className="hub-metric-switch">
        {RANGES.map((r) => (
          <Button key={r} variant={r === range ? 'primary' : 'outline'} aria-pressed={r === range} onClick={() => setRange(r)}>
            {t(r === RANGE_24H ? 'nodes.range24h' : 'nodes.range7d')}
          </Button>
        ))}
      </div>
      {metrics.error !== null && <Alert tone="plain">{metrics.error.message}</Alert>}
      {metrics.error === null && points === null && <Loading isInline />}
      {points !== null && points.length === 0 && <div className="hub-empty">{t('nodes.noMetrics')}</div>}
      {points !== null && points.length > 0 && (
        <>
          <MetricChart title={t('nodes.chartOnline')} points={points} pick={pickOnline} step={STEP_MS[range]} color={cssColor('--hub-primary', '#6777ef')} unit="" />
          <MetricChart title={t('nodes.chartLoad')} points={points} pick={pickLoad} step={STEP_MS[range]} color={cssColor('--hub-warning', '#ffa426')} unit="%" max={PERCENT_MAX} />
        </>
      )}
    </section>
  )
}
