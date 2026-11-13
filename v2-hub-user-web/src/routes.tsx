import type { RouteObject } from 'react-router'
import { UserLayout } from './layout/UserLayout'
import { ROUTES } from './menu'
import { AccountPage } from './pages/AccountPage'
import { AnnouncementsPage } from './pages/AnnouncementsPage'
import { RegisterPage } from './pages/auth/RegisterPage'
import { ResetPage } from './pages/auth/ResetPage'
import { DashboardPage } from './pages/DashboardPage'
import { InvitePage } from './pages/InvitePage'
import { LoginPage } from './pages/LoginPage'
import { NodesPage } from './pages/NodesPage'
import { NotFoundPage } from './pages/NotFoundPage'
import { SharedAccountsPage } from './pages/SharedAccountsPage'
import { SubscribeLogsPage } from './pages/SubscribeLogsPage'
import { TicketDetailPage } from './pages/TicketDetailPage'
import { TicketsPage } from './pages/TicketsPage'
import { TutorialsPage } from './pages/TutorialsPage'

/** 路由表：登录、注册、找回口令独立，其余都在已登录的外壳里。 */
export const routes: RouteObject[] = [
  { path: ROUTES.LOGIN, element: <LoginPage /> },
  { path: ROUTES.REGISTER, element: <RegisterPage /> },
  { path: ROUTES.RESET, element: <ResetPage /> },
  {
    path: ROUTES.DASHBOARD,
    element: <UserLayout />,
    children: [
      { index: true, element: <DashboardPage /> },
      { path: ROUTES.NODES, element: <NodesPage /> },
      { path: ROUTES.ACCOUNT, element: <AccountPage /> },
      { path: ROUTES.SUBSCRIBE_LOGS, element: <SubscribeLogsPage /> },
      { path: ROUTES.ANNOUNCEMENTS, element: <AnnouncementsPage /> },
      { path: ROUTES.INVITE, element: <InvitePage /> },
      { path: ROUTES.TUTORIALS, element: <TutorialsPage /> },
      { path: ROUTES.SHARED_ACCOUNTS, element: <SharedAccountsPage /> },
      { path: ROUTES.TICKETS, element: <TicketsPage /> },
      { path: ROUTES.TICKET_DETAIL, element: <TicketDetailPage /> },
      { path: '*', element: <NotFoundPage /> },
    ],
  },
]
