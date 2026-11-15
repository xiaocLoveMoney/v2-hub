import { Card, Segmented, Typography } from 'antd'
import type { CSSProperties, ReactNode } from 'react'
import { DAY_OPTIONS, FULL_PERCENT, type AnalyticsDays } from './analytics-format'

/** 总览各分析分区共用的小组件（docs/03 §5.20 总览分析，Ruling-66）。 */

const MIN_BAR_PERCENT = 1

interface DaysSwitchProps {
  value: AnalyticsDays
  onChange: (days: AnalyticsDays) => void
}

/** 时间范围切换：近 7 / 30 / 90 日。 */
export function DaysSwitch({ value, onChange }: DaysSwitchProps) {
  return <Segmented<AnalyticsDays> aria-label="时间范围" options={DAY_OPTIONS} value={value} onChange={onChange} />
}

interface SectionProps {
  /** 分区顶部右侧的控件（时间范围等）。 */
  toolbar?: ReactNode
  children: ReactNode
}

/** 一个分析分区：顶部一行控件，下面是灰底上的卡片。 */
export function Section({ toolbar, children }: SectionProps) {
  return (
    <div className="analytics-section">
      {toolbar !== undefined && <div className="analytics-toolbar">{toolbar}</div>}
      {children}
    </div>
  )
}

interface PanelProps {
  title: string
  /** 标题右侧的补充说明（合计等）。 */
  extra?: ReactNode
  children: ReactNode
}

/** 分区里的一张卡片。 */
export function Panel({ title, extra, children }: PanelProps) {
  return (
    <Card title={title} extra={extra === undefined ? undefined : <Typography.Text type="secondary">{extra}</Typography.Text>}>
      {children}
    </Card>
  )
}

/** 条形列表的一行。 */
export interface BarRow {
  key: string
  label: ReactNode
  value: number
  /** 行尾显示的文字，缺省为数值本身。 */
  text?: string
}

/**
 * 条形列表：每行一个名字、一根按最大值折算的横条和行尾的数值。
 * 用来看少量类目的大小对比（状态分布、客户端分布等），数值始终以文字给出。
 */
export function BarList({ rows, emptyText = '暂无数据' }: { rows: BarRow[]; emptyText?: string }) {
  if (rows.length === 0) {
    return <div className="chart-empty">{emptyText}</div>
  }
  const peak = Math.max(...rows.map((r) => r.value), 1)
  return (
    <ul className="bar-list">
      {rows.map((r) => {
        const width = r.value === 0 ? 0 : Math.max((r.value / peak) * FULL_PERCENT, MIN_BAR_PERCENT)
        const style = { '--bar-width': `${width}%` } as CSSProperties
        return (
          <li key={r.key}>
            <span className="bar-list-label">{r.label}</span>
            <span className="bar-list-track" aria-hidden="true">
              <span className="bar-list-fill" style={style} />
            </span>
            <span className="bar-list-value">{r.text ?? r.value}</span>
          </li>
        )
      })}
    </ul>
  )
}

interface FigureProps {
  label: string
  value: ReactNode
  /** 数值下面的一行说明。 */
  hint?: ReactNode
}

/** 一个带标题的数字（分区里的小指标）。 */
export function Figure({ label, value, hint }: FigureProps) {
  return (
    <div className="figure">
      <span className="figure-label">{label}</span>
      <span className="figure-value">{value}</span>
      {hint !== undefined && <span className="figure-hint">{hint}</span>}
    </div>
  )
}
