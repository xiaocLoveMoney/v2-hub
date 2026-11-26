import { Outlet } from 'react-router'
import { ROUTES } from '../menu'
import { ForbiddenPage } from '../pages/Placeholders'
import { AdminShell, AgentShell } from './AdminLayout'
import { SessionGuard } from './SessionGuard'

/** 员工访问代理路径时 403 页的说明文字（Ruling-101）。 */
const STAFF_FORBIDDEN_TEXT = '代理界面只供代理账号使用'

/**
 * 代理的布局（docs/03 §5.23）：会话判断见 SessionGuard；员工账号不渲染任何代理页面，显示 403（与代理访问员工页面同一做法）。
 */
export function AgentLayout() {
  const wrongKind = (
    <AdminShell>
      <ForbiddenPage subTitle={STAFF_FORBIDDEN_TEXT} homePath={ROUTES.DASHBOARD} homeLabel="返回总览" />
    </AdminShell>
  )
  return (
    <SessionGuard kind="agent" wrongKind={wrongKind}>
      <AgentShell>
        <Outlet />
      </AgentShell>
    </SessionGuard>
  )
}
