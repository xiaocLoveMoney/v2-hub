import type { RouteObject } from 'react-router'
import { UserLayout } from './layout/UserLayout'
import { ROUTES } from './menu'
import { AccountPage } from './pages/AccountPage'
import { DashboardPage } from './pages/DashboardPage'
import { LoginPage } from './pages/LoginPage'
import { NodesPage } from './pages/NodesPage'
import { NotFoundPage } from './pages/NotFoundPage'
import { SubscribeLogsPage } from './pages/SubscribeLogsPage'

/** 路由表：登录页独立，其余都在已登录的外壳里。 */
export const routes: RouteObject[] = [
  { path: ROUTES.LOGIN, element: <LoginPage /> },
  {
    path: ROUTES.DASHBOARD,
    element: <UserLayout />,
    children: [
      { index: true, element: <DashboardPage /> },
      { path: ROUTES.NODES, element: <NodesPage /> },
      { path: ROUTES.ACCOUNT, element: <AccountPage /> },
      { path: ROUTES.SUBSCRIBE_LOGS, element: <SubscribeLogsPage /> },
      { path: '*', element: <NotFoundPage /> },
    ],
  },
]
