import { Typography } from 'antd'
import type { ReactNode } from 'react'

interface CardPageProps {
  title: string
  /** 标题栏右侧的操作按钮。 */
  extra?: ReactNode
  children: ReactNode
}

/**
 * 卡片式页面（照客户后台仪表盘）：标题栏单独一张卡，内容是灰底上的若干卡片。
 * 外层的白色内容框由样式 `.admin-content:has(> .card-page)` 去掉，页面自己用 `.card-grid` 排卡片。
 */
export function CardPage({ title, extra, children }: CardPageProps) {
  return (
    <div className="card-page">
      <div className="card-page-header">
        <Typography.Title level={3} className="card-page-title">
          {title}
        </Typography.Title>
        {extra}
      </div>
      {children}
    </div>
  )
}
