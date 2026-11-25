import type { ReactNode } from 'react'
import type { RouteObject } from 'react-router'
import { AGENT_ROUTES } from './agent-menu'
import { AdminLayout } from './layout/AdminLayout'
import { AgentLayout } from './layout/AgentLayout'
import { MENU, PERMISSIONS, ROUTES } from './menu'
import { LoginPage } from './pages/LoginPage'
import { DashboardPage } from './pages/DashboardPage'
import { ComingSoonPage, NotFoundPage, RequirePermission } from './pages/Placeholders'
import { AnnouncementsPage } from './pages/content/AnnouncementsPage'
import { SharedAccountsPage } from './pages/content/SharedAccountsPage'
import { TicketsPage } from './pages/content/TicketsPage'
import { TutorialsPage } from './pages/content/TutorialsPage'
import { CouponsPage } from './pages/shop/CouponsPage'
import { FinanceStatsPage } from './pages/shop/FinanceStatsPage'
import { LedgerPage, OrdersPage, PaymentsPage, RebatesPage } from './pages/shop/RecordPages'
import { TopupCodesPage } from './pages/shop/TopupCodesPage'
import { TrafficPacksPage } from './pages/shop/TrafficPacksPage'
import { AgentInviteCodesPage, AgentLedgerPage, AgentOrdersPage, AgentTicketsPage } from './pages/agent/AgentRecordPages'
import { AgentSummaryPage } from './pages/agent/AgentSummaryPage'
import { AgentUsersPage } from './pages/agent/AgentUsersPage'
import { AdminsPage } from './pages/system/AdminsPage'
import { AgentsPage } from './pages/system/AgentsPage'
import { AuditLogsPage } from './pages/system/AuditLogsPage'
import { MailOutboxPage } from './pages/system/MailOutboxPage'
import { MenuOrderPage } from './pages/system/MenuOrderPage'
import { RolesPage } from './pages/system/RolesPage'
import { SettingsPage } from './pages/system/SettingsPage'
import { SecurityPage } from './pages/SecurityPage'
import { NodesPage } from './pages/panel/NodesPage'
import { ChainExitsPage } from './pages/panel/ChainExitsPage'
import { ChainRoutesPage } from './pages/panel/ChainRoutesPage'
import { HostsPage } from './pages/panel/HostsPage'
import { ServerDetailPage } from './pages/panel/ServerDetailPage'
import { ServersPage } from './pages/panel/ServersPage'
import { InviteCodesPage } from './pages/users/InviteCodesPage'
import { PlansPage } from './pages/users/PlansPage'
import { SubscribeLogsPage } from './pages/users/SubscribeLogsPage'
import { UsersPage } from './pages/users/UsersPage'

/** 已实现的页面；菜单表里其余路径显示交付里程碑占位。 */
const PAGE_BY_PATH: Record<string, ReactNode> = {
  [ROUTES.ANNOUNCEMENTS]: <AnnouncementsPage />,
  [ROUTES.TUTORIALS]: <TutorialsPage />,
  [ROUTES.SHARED_ACCOUNTS]: <SharedAccountsPage />,
  [ROUTES.TICKETS]: <TicketsPage />,
  [ROUTES.SERVERS]: <ServersPage />,
  [ROUTES.HOSTS]: <HostsPage />,
  [ROUTES.NODES]: <NodesPage />,
  [ROUTES.CHAIN_EXITS]: <ChainExitsPage />,
  [ROUTES.CHAIN_ROUTES]: <ChainRoutesPage />,
  [ROUTES.USERS]: <UsersPage />,
  [ROUTES.PLANS]: <PlansPage />,
  [ROUTES.INVITE_CODES]: <InviteCodesPage />,
  [ROUTES.SUBSCRIBE_LOGS]: <SubscribeLogsPage />,
  [ROUTES.TRAFFIC_PACKS]: <TrafficPacksPage />,
  [ROUTES.COUPONS]: <CouponsPage />,
  [ROUTES.TOPUP_CODES]: <TopupCodesPage />,
  [ROUTES.ORDERS]: <OrdersPage />,
  [ROUTES.WALLET_LEDGER]: <LedgerPage />,
  [ROUTES.PAYMENTS]: <PaymentsPage />,
  [ROUTES.REBATES]: <RebatesPage />,
  [ROUTES.FINANCE_STATS]: <FinanceStatsPage />,
  [ROUTES.ADMINS]: <AdminsPage />,
  [ROUTES.ROLES]: <RolesPage />,
  [ROUTES.AGENTS]: <AgentsPage />,
  [ROUTES.AUDIT_LOGS]: <AuditLogsPage />,
  [ROUTES.SETTINGS]: <SettingsPage />,
  [ROUTES.MENU_ORDER]: <MenuOrderPage />,
  [ROUTES.MAIL_OUTBOX]: <MailOutboxPage />,
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

/** 代理界面的子路由（docs/03 §5.23）：固定菜单，不看权限码；员工进来由 AgentLayout 显示 403（Ruling-101）。 */
const agentRoutes: RouteObject[] = [
  { index: true, element: <AgentSummaryPage /> },
  { path: AGENT_ROUTES.USERS, element: <AgentUsersPage /> },
  { path: AGENT_ROUTES.INVITE_CODES, element: <AgentInviteCodesPage /> },
  { path: AGENT_ROUTES.TICKETS, element: <AgentTicketsPage /> },
  { path: AGENT_ROUTES.ORDERS, element: <AgentOrdersPage /> },
  { path: AGENT_ROUTES.WALLET_LEDGER, element: <AgentLedgerPage /> },
  { path: AGENT_ROUTES.SECURITY, element: <SecurityPage /> },
  { path: '*', element: <NotFoundPage /> },
]

export const routes: RouteObject[] = [
  { path: ROUTES.LOGIN, element: <LoginPage /> },
  { path: AGENT_ROUTES.HOME, element: <AgentLayout />, children: agentRoutes },
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
