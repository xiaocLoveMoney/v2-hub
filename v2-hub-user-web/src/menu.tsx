import { History, LayoutDashboard, Server, UserRound } from 'lucide-react'
import type { ReactNode } from 'react'

/** 前端路由路径常量。 */
export const ROUTES = {
  LOGIN: '/login',
  DASHBOARD: '/',
  NODES: '/nodes',
  ACCOUNT: '/account',
  SUBSCRIBE_LOGS: '/subscribe-logs',
} as const

const ICON_SIZE = 18

/** 一个菜单项。 */
export interface MenuEntry {
  path: string
  label: string
  icon: ReactNode
  /** 所属分组标题，未设置的放在顶层。 */
  group?: string
}

const GROUP_MINE = '我的'
const GROUP_USE = '使用'

/** 左侧菜单表（docs/06 §5）：增减菜单只改这张表；商店、钱包、工单等随 M4 / M5 加入。 */
export const MENU: readonly MenuEntry[] = [
  { path: ROUTES.DASHBOARD, label: '仪表盘', icon: <LayoutDashboard size={ICON_SIZE} /> },
  { path: ROUTES.ACCOUNT, label: '我的账号', icon: <UserRound size={ICON_SIZE} />, group: GROUP_MINE },
  { path: ROUTES.NODES, label: '节点列表', icon: <Server size={ICON_SIZE} />, group: GROUP_USE },
  { path: ROUTES.SUBSCRIBE_LOGS, label: '订阅记录', icon: <History size={ICON_SIZE} />, group: GROUP_USE },
]
