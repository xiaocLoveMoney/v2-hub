import { Space, Typography } from 'antd'
import type { ReactNode } from 'react'

/** 页面标题默认的级别；区块小标题传 4 / 5。 */
const DEFAULT_TITLE_LEVEL = 3

interface PageHeaderProps {
  title: ReactNode
  /** 标题右侧的操作（新建按钮等）；没有权限时传 false / undefined 即不渲染。 */
  action?: ReactNode
  level?: 1 | 2 | 3 | 4 | 5
}

/**
 * 标题栏：标题 + 右侧操作，样式由 `.page-header` 负责。
 * @param title 标题
 * @param action 标题右侧的操作
 * @param level 标题级别，默认 3
 */
export function PageHeader({ title, action, level = DEFAULT_TITLE_LEVEL }: PageHeaderProps) {
  return (
    <Space className="page-header">
      <Typography.Title level={level}>{title}</Typography.Title>
      {action}
    </Space>
  )
}
