import { Button, Result } from 'antd'
import type { ReactNode } from 'react'
import { useNavigate } from 'react-router'
import { useSession } from '../auth/session-context'
import { ROUTES } from '../menu'

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

interface ForbiddenPageProps {
  subTitle: string
  /** “返回”按钮去的首页。 */
  homePath: string
  homeLabel: string
}

/** 账号类型不对的页面（代理访问员工页面、员工访问代理页面，Ruling-101）：只显示 403，不渲染页面、不发页面的请求。 */
export function ForbiddenPage({ subTitle, homePath, homeLabel }: ForbiddenPageProps) {
  const navigate = useNavigate()
  return <Result status="403" title="没有权限" subTitle={subTitle} extra={<Button onClick={() => void navigate(homePath)}>{homeLabel}</Button>} />
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
