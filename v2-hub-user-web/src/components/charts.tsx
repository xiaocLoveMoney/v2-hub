import type { TrafficPoint } from '../api/client'
import { formatBytes } from '../format'

/** 轻量图表：环形图与按小时柱状图，直接画 SVG，不引入图表库。 */

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

const BARS_WIDTH = 720
const BARS_HEIGHT = 180
const BARS_PADDING_BOTTOM = 4
const BAR_GAP_RATIO = 0.25
const MIN_BAR_HEIGHT = 1
const MS_PER_HOUR = 3_600_000

interface HourBarsProps {
  points: TrafficPoint[]
  hours: number
  /** 当前时刻（毫秒），由调用方传入便于测试。 */
  now: number
}

/** 最近 hours 小时的用量柱状图；没有用量的小时留空。 */
export function HourBars({ points, hours, now }: HourBarsProps) {
  const currentHour = Math.floor(now / MS_PER_HOUR)
  const byHour = new Map(points.map((p) => [Math.floor(new Date(p.hour).getTime() / MS_PER_HOUR), p.up + p.down]))
  const values = Array.from({ length: hours }, (_, i) => byHour.get(currentHour - (hours - 1 - i)) ?? 0)
  const peak = Math.max(...values, 0)
  const slot = BARS_WIDTH / hours
  const barWidth = slot * (1 - BAR_GAP_RATIO)
  const usable = BARS_HEIGHT - BARS_PADDING_BOTTOM
  const total = values.reduce((sum, v) => sum + v, 0)
  return (
    <svg className="hub-bars" viewBox={`0 0 ${BARS_WIDTH} ${BARS_HEIGHT}`} preserveAspectRatio="none" role="img" aria-label={`最近 ${hours} 小时用量 ${formatBytes(total)}`}>
      <line x1="0" y1={usable} x2={BARS_WIDTH} y2={usable} stroke="var(--hub-border)" strokeWidth="1" />
      {values.map((v, i) => {
        if (v === 0 || peak === 0) {
          return null
        }
        const h = Math.max((v / peak) * (usable - MIN_BAR_HEIGHT), MIN_BAR_HEIGHT)
        const hourStart = (currentHour - (hours - 1 - i)) * MS_PER_HOUR
        return (
          <rect key={hourStart} x={i * slot + (slot - barWidth) / 2} y={usable - h} width={barWidth} height={h} rx="1" fill="var(--hub-primary)">
            <title>{`${new Date(hourStart).toLocaleString('zh-CN', { hour12: false })}  ${formatBytes(v)}`}</title>
          </rect>
        )
      })}
    </svg>
  )
}
