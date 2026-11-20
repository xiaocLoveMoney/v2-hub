import type { Level } from './server-card-format'
import { usageLevel } from './server-card-format'

/** 默认格数，与参考设计一致。 */
const DEFAULT_SEGMENTS = 20
const PERCENT_FULL = 100

interface SegmentBarProps {
  /** 0–100 的百分比。 */
  percent: number
  /** 格数，默认 20。 */
  segments?: number
  /** 点亮的格子的颜色；不传按百分比阈值取（正常 / 偏高 / 过高）。 */
  tone?: 'cpu' | 'mem' | 'disk' | 'quota'
  ariaLabel: string
}

/**
 * 分段进度条：按百分比点亮前若干格（不足一格但大于 0 的点亮一格）。
 * 有 tone 时点亮的格子用这一项的颜色，超过阈值仍按阈值变色。
 */
export function SegmentBar({ percent, segments = DEFAULT_SEGMENTS, tone, ariaLabel }: SegmentBarProps) {
  const clamped = Math.min(Math.max(percent, 0), PERCENT_FULL)
  const lit = clamped > 0 ? Math.max(1, Math.round((clamped / PERCENT_FULL) * segments)) : 0
  const level = usageLevel(clamped)
  const litClass = level === 'ok' && tone !== undefined ? `segment-${tone}` : `segment-${level}`
  return (
    <div className="segment-bar" role="meter" aria-label={ariaLabel} aria-valuemin={0} aria-valuemax={PERCENT_FULL} aria-valuenow={Math.round(clamped)}>
      {Array.from({ length: segments }, (_, i) => (
        <span key={i} className={i < lit ? `segment ${litClass}` : 'segment'} />
      ))}
    </div>
  )
}

/** 历史条里的一格：key 取检测时间，保证增删时格子不错位。 */
export interface HistoryCell {
  key: string
  level: Level
  title: string
}

interface HistoryBarProps {
  /** 从旧到新的各格；超过 slots 只取最近的，不足时左边补空格。 */
  cells: HistoryCell[]
  slots: number
  ariaLabel: string
}

/** 历史条：每次检测一格，绿 / 橙 / 红，没有数据的格子为灰。 */
export function HistoryBar({ cells, slots, ariaLabel }: HistoryBarProps) {
  const recent = cells.slice(-slots)
  const padding = slots - recent.length
  return (
    <div className="segment-bar segment-history" role="img" aria-label={ariaLabel}>
      {Array.from({ length: padding }, (_, i) => (
        <span key={`pad-${i}`} className="segment" />
      ))}
      {recent.map((cell) => (
        <span key={cell.key} className={`segment segment-${cell.level}`} title={cell.title} />
      ))}
    </div>
  )
}
