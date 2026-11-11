import { Button, Result, Typography } from 'antd'
import type { ReactNode } from 'react'
import { useNavigate } from 'react-router'
import { useSession } from '../auth/session-context'
import { ROUTES } from '../menu'

/** 总览（管理门户首页）；大盘指标在 M6 接入。 */
export function DashboardPage() {
  const { session } = useSession()
  return (
    <>
      <Typography.Title level={3}>总览</Typography.Title>
      <Typography.Paragraph>欢迎，{session?.admin.username}。服务器、节点与用户指标会在后续里程碑接入。</Typography.Paragraph>
    </>
  )
}

interface ComingSoonProps {
  title: string
  milestone: string
}

/** 尚未实现的页面占位，标明在哪个里程碑交付。 */
export function ComingSoonPage({ title, milestone }: ComingSoonProps) {
  return <Result status="info" title={title} subTitle={`该页面在 ${milestone} 交付`} />
}

interface RequirePermissionProps {
  permission: string
  children: ReactNode
}

/** 没有权限码时显示 403，而不是渲染页面（鉴权以后端为准）。 */
export function RequirePermission({ permission, children }: RequirePermissionProps) {
  const { can } = useSession()
  const navigate = useNavigate()
  if (!can(permission)) {
    return (
      <Result
        status="403"
        title="没有权限"
        subTitle="请联系管理员分配相应角色"
        extra={<Button onClick={() => void navigate(ROUTES.DASHBOARD)}>返回总览</Button>}
      />
    )
  }
  return children
}

/** 未匹配到路由。 */
export function NotFoundPage() {
  const navigate = useNavigate()
  return (
    <Result
      status="404"
      title="页面不存在"
      extra={<Button onClick={() => void navigate(ROUTES.DASHBOARD)}>返回总览</Button>}
    />
  )
}
