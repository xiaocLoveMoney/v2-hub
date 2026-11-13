import type { ReactNode } from 'react'
import type { RouteObject } from 'react-router'
import { AdminLayout } from './layout/AdminLayout'
import { MENU, PERMISSIONS, ROUTES } from './menu'
import { LoginPage } from './pages/LoginPage'
import { ComingSoonPage, DashboardPage, NotFoundPage, RequirePermission } from './pages/Placeholders'
import { AdminsPage } from './pages/system/AdminsPage'
import { AuditLogsPage } from './pages/system/AuditLogsPage'
import { RolesPage } from './pages/system/RolesPage'
import { SecurityPage } from './pages/SecurityPage'
import { NodesPage } from './pages/panel/NodesPage'
import { ServerDetailPage } from './pages/panel/ServerDetailPage'
import { ServersPage } from './pages/panel/ServersPage'
import { PlansPage } from './pages/users/PlansPage'
import { UsersPage } from './pages/users/UsersPage'

/** 已实现的页面；菜单表里其余路径显示交付里程碑占位。 */
const PAGE_BY_PATH: Record<string, ReactNode> = {
  [ROUTES.SERVERS]: <ServersPage />,
  [ROUTES.NODES]: <NodesPage />,
  [ROUTES.USERS]: <UsersPage />,
  [ROUTES.PLANS]: <PlansPage />,
  [ROUTES.ADMINS]: <AdminsPage />,
  [ROUTES.ROLES]: <RolesPage />,
  [ROUTES.AUDIT_LOGS]: <AuditLogsPage />,
  [ROUTES.SECURITY]: <SecurityPage />,
}

const MILESTONE_BY_PATH: Record<string, string> = {}

/** 由菜单表生成受权限保护的子路由，菜单与路由守卫共用同一份权限配置。 */
const menuRoutes: RouteObject[] = MENU.filter((entry) => entry.path !== ROUTES.DASHBOARD).map((entry) => {
  const page = PAGE_BY_PATH[entry.path] ?? <ComingSoonPage title={entry.label} milestone={MILESTONE_BY_PATH[entry.path] ?? '后续里程碑'} />
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
    children: [
      { index: true, element: <DashboardPage /> },
      ...menuRoutes,
      // 不在左侧菜单里的详情页，权限与入站列表一致
      { path: ROUTES.SERVER_DETAIL, element: <RequirePermission permission={PERMISSIONS.NODE_READ}><ServerDetailPage /></RequirePermission> },
      { path: '*', element: <NotFoundPage /> },
    ],
  },
]
