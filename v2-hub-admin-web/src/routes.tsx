import type { RouteObject } from 'react-router'
import { AdminLayout } from './layout/AdminLayout'
import { MENU, ROUTES } from './menu'
import { LoginPage } from './pages/LoginPage'
import { ComingSoonPage, DashboardPage, NotFoundPage, RequirePermission } from './pages/Placeholders'

const MILESTONE_BY_PATH: Record<string, string> = {
  [ROUTES.SERVERS]: 'M1',
  [ROUTES.NODES]: 'M1',
  [ROUTES.ADMINS]: 'M0 后续任务',
  [ROUTES.ROLES]: 'M0 后续任务',
  [ROUTES.AUDIT_LOGS]: 'M0 后续任务',
}

/** 由菜单表生成受权限保护的子路由，菜单与路由守卫共用同一份权限配置。 */
const menuRoutes: RouteObject[] = MENU.filter((entry) => entry.path !== ROUTES.DASHBOARD).map((entry) => {
  const page = <ComingSoonPage title={entry.label} milestone={MILESTONE_BY_PATH[entry.path] ?? '后续里程碑'} />
  return {
    path: entry.path,
    element: entry.permission === undefined ? page : <RequirePermission permission={entry.permission}>{page}</RequirePermission>,
  }
})

export const routes: RouteObject[] = [
  { path: ROUTES.LOGIN, element: <LoginPage /> },
  {
    path: ROUTES.DASHBOARD,
    element: <AdminLayout />,
    children: [{ index: true, element: <DashboardPage /> }, ...menuRoutes, { path: '*', element: <NotFoundPage /> }],
  },
]
