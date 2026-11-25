import { Spin } from 'antd'
import { Navigate, Outlet, useLocation } from 'react-router'
import { useSession } from '../auth/session-context'
import { ROUTES } from '../menu'
import { ForbiddenPage } from '../pages/Placeholders'
import { AdminShell, AgentShell } from './AdminLayout'

/** 员工访问代理路径时 403 页的说明文字（Ruling-101）。 */
const STAFF_FORBIDDEN_TEXT = '代理界面只供代理账号使用'

/**
 * 代理的布局（docs/03 §5.23）：未登录跳到登录页；员工账号不渲染任何代理页面，显示 403（与代理访问员工页面同一做法）；
 * 超管未开启两步验证时照旧停到账号安全页（Ruling-98）。
 */
export function AgentLayout() {
  const { status, session } = useSession()
  const location = useLocation()

  if (status === 'loading') {
    return <Spin fullscreen />
  }
  if (status === 'anonymous') {
    return <Navigate to={ROUTES.LOGIN} replace state={{ from: location.pathname }} />
  }
  if (session?.kind !== 'agent') {
    if (session?.totpSetupRequired === true) {
      return <Navigate to={ROUTES.SECURITY} replace />
    }
    return (
      <AdminShell>
        <ForbiddenPage subTitle={STAFF_FORBIDDEN_TEXT} homePath={ROUTES.DASHBOARD} homeLabel="返回总览" />
      </AdminShell>
    )
  }
  return (
    <AgentShell>
      <Outlet />
    </AgentShell>
  )
}
