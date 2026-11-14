import { useMemo } from 'react'
import type { TrafficPoint } from '../api/client'
import { formatBytes } from '../format'
import { useI18n } from '../i18n/i18n-context'
import { LOCALE_TAG } from '../i18n/locale'
import { cssColor } from './css-color'
import { EChart } from './EChart'

/** 图表：环形图直接画 SVG；按小时用量的气泡图用 ECharts（按需加载）。 */

const DONUT_SIZE = 180
const DONUT_STROKE = 22
const DONUT_RADIUS = (DONUT_SIZE - DONUT_STROKE) / 2
const DONUT_CIRCUMFERENCE = 2 * Math.PI * DONUT_RADIUS
const QUARTER_TURN = -90

export interface DonutSegment {
  label: string
  value: number
  /** CSS 颜色（设计变量）。 */
  color: string
}

interface DonutProps {
  segments: DonutSegment[]
  centerTitle: string
  centerValue: string
}

/** 环形图：各段按 value 占比依次排布；全为 0 时只画底环。 */
export function Donut({ segments, centerTitle, centerValue }: DonutProps) {
  const total = segments.reduce((sum, s) => sum + s.value, 0)
  const center = DONUT_SIZE / 2
  const arcs = segments.reduce<{ label: string; color: string; length: number; offset: number }[]>((acc, s) => {
    const prev = acc[acc.length - 1]
    const offset = prev === undefined ? 0 : prev.offset + prev.length
    const length = total === 0 ? 0 : (s.value / total) * DONUT_CIRCUMFERENCE
    return [...acc, { label: s.label, color: s.color, length, offset }]
  }, [])
  return (
    <div className="hub-donut">
      <svg width={DONUT_SIZE} height={DONUT_SIZE} viewBox={`0 0 ${DONUT_SIZE} ${DONUT_SIZE}`} role="img" aria-label={`${centerTitle} ${centerValue}`}>
        <circle cx={center} cy={center} r={DONUT_RADIUS} fill="none" stroke="var(--hub-divider)" strokeWidth={DONUT_STROKE} />
        {arcs.map((a) => (
          <circle
            key={a.label}
            cx={center}
            cy={center}
            r={DONUT_RADIUS}
            fill="none"
            stroke={a.color}
            strokeWidth={DONUT_STROKE}
            strokeDasharray={`${a.length} ${DONUT_CIRCUMFERENCE - a.length}`}
            strokeDashoffset={-a.offset}
            transform={`rotate(${QUARTER_TURN} ${center} ${center})`}
          />
        ))}
        <text x={center} y={center - 6} textAnchor="middle" fontSize="12" fill="var(--hub-muted)">
          {centerTitle}
        </text>
        <text x={center} y={center + 16} textAnchor="middle" fontSize="18" fontWeight="700" fill="var(--hub-heading)">
          {centerValue}
        </text>
      </svg>
      <div className="hub-legend">
        {segments.map((s) => (
          <span key={s.label}>
            <span className="hub-legend-dot" style={{ background: s.color }} />
            {s.label} {formatBytes(s.value)}
          </span>
        ))}
      </div>
    </div>
  )
}

const MS_PER_HOUR = 3_600_000
const BUBBLE_CHART_HEIGHT = 200
const BUBBLE_MIN_SIZE = 6
const BUBBLE_MAX_SIZE = 30
const BUBBLE_OPACITY = 0.72
const AXIS_LABEL_WIDTH = 64

interface HourBubblesProps {
  points: TrafficPoint[]
  hours: number
  /** 当前时刻（毫秒），由调用方传入便于测试。 */
  now: number
}

/** 最近 hours 小时的用量散点气泡图：上行、下行各一组，纵轴与气泡大小都表示用量；没有用量的小时不画点。 */
export function HourBubbles({ points, hours, now }: HourBubblesProps) {
  const { t, locale } = useI18n()
  const option = useMemo(() => {
    const start = (Math.floor(now / MS_PER_HOUR) - hours + 1) * MS_PER_HOUR
    const inRange = points.filter((p) => new Date(p.hour).getTime() >= start)
    const peak = Math.max(...inRange.map((p) => Math.max(p.up, p.down)), 0)
    const bubbleSize = (value: number) => (peak === 0 ? BUBBLE_MIN_SIZE : BUBBLE_MIN_SIZE + (BUBBLE_MAX_SIZE - BUBBLE_MIN_SIZE) * Math.sqrt(value / peak))
    const seriesOf = (name: string, color: string, pick: (p: TrafficPoint) => number) => ({
      name,
      type: 'scatter',
      data: inRange.filter((p) => pick(p) > 0).map((p) => [new Date(p.hour).getTime(), pick(p)]),
      symbolSize: (value: number[]) => bubbleSize(value[1] ?? 0),
      itemStyle: { color, opacity: BUBBLE_OPACITY },
    })
    const timeText = (ms: number) => new Date(ms).toLocaleString(LOCALE_TAG[locale], { hour12: false, month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' })
    return {
      grid: { left: AXIS_LABEL_WIDTH, right: 16, top: 36, bottom: 28 },
      legend: { top: 0, right: 0, textStyle: { color: cssColor('--hub-muted', '#98a6ad') } },
      tooltip: {
        trigger: 'item',
        formatter: (p: { seriesName: string; value: number[] }) => `${p.seriesName}<br/>${timeText(p.value[0] ?? 0)}  ${formatBytes(p.value[1] ?? 0)}`,
      },
      xAxis: { type: 'time', min: start, max: now, splitLine: { show: false } },
      yAxis: { type: 'value', axisLabel: { formatter: (v: number) => formatBytes(v) }, splitLine: { lineStyle: { type: 'dashed', color: cssColor('--hub-divider', '#f1f3f6') } } },
      series: [
        seriesOf(t('chart.upload'), cssColor('--hub-primary', '#6777ef'), (p) => p.up),
        seriesOf(t('chart.download'), cssColor('--hub-info', '#3abaf4'), (p) => p.down),
      ],
    }
  }, [points, hours, now, t, locale])
  const total = points.reduce((sum, p) => sum + p.up + p.down, 0)
  return <EChart option={option} height={BUBBLE_CHART_HEIGHT} ariaLabel={t('chart.hoursUsage', { hours, amount: formatBytes(total) })} />
}
