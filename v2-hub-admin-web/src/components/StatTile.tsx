import type { ReactNode } from 'react'

/** 统计卡左侧色块的配色，对应 index.css 的 .stat-tile-*。 */
export type StatTone = 'primary' | 'success' | 'warning' | 'info' | 'danger'

interface StatTileProps {
  tone: StatTone
  icon: ReactNode
  label: string
  value: ReactNode
  /** 卡片底部一行补充信息。 */
  foot: ReactNode
}

/** 一张统计卡（照客户后台仪表盘）：色块图标 + 标题 + 大号数值 + 底部补充。 */
export function StatTile({ tone, icon, label, value, foot }: StatTileProps) {
  return (
    <div className={`stat-tile stat-tile-${tone}`}>
      <span className="stat-tile-icon" aria-hidden="true">
        {icon}
      </span>
      <span className="stat-tile-label">{label}</span>
      <span className="stat-tile-value">{value}</span>
      <div className="stat-tile-foot">{foot}</div>
    </div>
  )
}
