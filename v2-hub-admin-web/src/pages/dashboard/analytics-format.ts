/** 总览分析分区共用的常量与格式化（docs/03 §5.20 总览分析，Ruling-66）。 */

/** 时间范围只有这三档，与 hub-api.yaml 的 AnalyticsDays 一致。 */
export type AnalyticsDays = 7 | 30 | 90
export const DEFAULT_DAYS: AnalyticsDays = 30
export const DAY_OPTIONS: { value: AnalyticsDays; label: string }[] = [
  { value: 7, label: '近 7 日' },
  { value: 30, label: '近 30 日' },
  { value: 90, label: '近 90 日' },
]
const MONTH_DAY_START = 5 // yyyy-MM-dd 里 MM-dd 的起点
export const FULL_PERCENT = 100
const PERCENT_DECIMALS = 1
export const CHART_HEIGHT = 260
export const EMPTY_TEXT = '—'

/** 把 yyyy-MM-dd 截成 MM-dd，给图表横轴用。 */
export function shortDate(date: string): string {
  return date.slice(MONTH_DAY_START)
}

/** 占比写成百分数；分母为 0 时显示占位符。 */
export function formatPercent(part: number, whole: number): string {
  return whole === 0 ? EMPTY_TEXT : `${((part / whole) * FULL_PERCENT).toFixed(PERCENT_DECIMALS)}%`
}
