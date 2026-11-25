import {
  AccountBookOutlined,
  CustomerServiceOutlined,
  DashboardOutlined,
  GiftOutlined,
  LockOutlined,
  ShoppingCartOutlined,
  UserOutlined,
} from '@ant-design/icons'
import type { ReactNode } from 'react'
import type { AdminKind } from './api/client'
import { ROUTES } from './menu'

/** 代理界面的路由路径（docs/03 §5.23，Ruling-101）；全部在 /agent 下，与员工页面互不重叠。 */
export const AGENT_ROUTES = {
  HOME: '/agent',
  USERS: '/agent/users',
  INVITE_CODES: '/agent/invite-codes',
  TICKETS: '/agent/tickets',
  ORDERS: '/agent/orders',
  WALLET_LEDGER: '/agent/ledger',
  SECURITY: '/agent/security',
} as const

/** 代理侧栏的一项。 */
export interface AgentMenuEntry {
  path: string
  label: string
  icon: ReactNode
}

/**
 * 代理的固定菜单（docs/03 §5.6 末尾）：不进员工的 MENU 表、不受菜单顺序设置影响、不按权限码过滤（代理的权限码恒为空）。
 */
export const AGENT_MENU: readonly AgentMenuEntry[] = [
  { path: AGENT_ROUTES.HOME, label: '配额概览', icon: <DashboardOutlined /> },
  { path: AGENT_ROUTES.USERS, label: '名下用户', icon: <UserOutlined /> },
  { path: AGENT_ROUTES.INVITE_CODES, label: '邀请码', icon: <GiftOutlined /> },
  { path: AGENT_ROUTES.TICKETS, label: '工单', icon: <CustomerServiceOutlined /> },
  { path: AGENT_ROUTES.ORDERS, label: '订单', icon: <ShoppingCartOutlined /> },
  { path: AGENT_ROUTES.WALLET_LEDGER, label: '余额流水', icon: <AccountBookOutlined /> },
  { path: AGENT_ROUTES.SECURITY, label: '账号安全', icon: <LockOutlined /> },
]

/** 路径是否属于代理界面。 */
export function isAgentPath(path: string): boolean {
  return path === AGENT_ROUTES.HOME || path.startsWith(`${AGENT_ROUTES.HOME}/`)
}

/**
 * 登录后的落地页：按账号类型区分（Ruling-101）。原本想去的页面属于本类账号时回到那里，否则去本类账号的首页。
 * @param kind 会话的账号类型
 * @param from 登录前想访问的路径
 * @returns 落地路径
 */
export function landingPath(kind: AdminKind, from: string | undefined): string {
  if (kind === 'agent') {
    return from !== undefined && isAgentPath(from) ? from : AGENT_ROUTES.HOME
  }
  return from !== undefined && !isAgentPath(from) ? from : ROUTES.DASHBOARD
}
