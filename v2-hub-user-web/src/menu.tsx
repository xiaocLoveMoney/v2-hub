import { BookOpen, Gift, GitFork, History, KeyRound, LayoutDashboard, LifeBuoy, Megaphone, Server, ShoppingBag, UserRound, Wallet } from 'lucide-react'
import type { ReactNode } from 'react'
import type { SiteInfo, UserFeatures } from './api/client'

/** 前端路由路径常量。 */
export const ROUTES = {
  LOGIN: '/login',
  REGISTER: '/register',
  RESET: '/reset',
  IMPERSONATE: '/impersonate',
  DASHBOARD: '/',
  SHOP: '/shop',
  WALLET: '/wallet',
  ANNOUNCEMENTS: '/announcements',
  NODES: '/nodes',
  ACCOUNT: '/account',
  INVITE: '/invite',
  TUTORIALS: '/tutorials',
  SUBSCRIBE_LOGS: '/subscribe-logs',
  SHARED_ACCOUNTS: '/shared-accounts',
  TICKETS: '/tickets',
  TICKET_DETAIL: '/tickets/:id',
  CHAIN: '/chain',
} as const

const ICON_SIZE = 18

/** 一个菜单项。 */
export interface MenuEntry {
  path: string
  label: string
  icon: ReactNode
  /** 所属分组标题，未设置的放在顶层。 */
  group?: string
  /** 按站点设置与当前套餐开的功能决定是否显示；未设置的总是显示（docs/06 §3 不变式 1）。 */
  isVisible?: (site: SiteInfo, features: UserFeatures) => boolean
}

const GROUP_MINE = '我的'
const GROUP_USE = '使用'

/** 左侧菜单表（docs/06 §5）：增减菜单只改这张表。 */
export const MENU: readonly MenuEntry[] = [
  { path: ROUTES.DASHBOARD, label: '仪表盘', icon: <LayoutDashboard size={ICON_SIZE} /> },
  { path: ROUTES.SHOP, label: '商店', icon: <ShoppingBag size={ICON_SIZE} />, isVisible: (site) => site.shopEnabled === true },
  { path: ROUTES.ANNOUNCEMENTS, label: '公告', icon: <Megaphone size={ICON_SIZE} /> },
  { path: ROUTES.ACCOUNT, label: '我的账号', icon: <UserRound size={ICON_SIZE} />, group: GROUP_MINE },
  { path: ROUTES.WALLET, label: '我的钱包', icon: <Wallet size={ICON_SIZE} />, group: GROUP_MINE, isVisible: (site) => site.shopEnabled === true },
  { path: ROUTES.INVITE, label: '邀请注册', icon: <Gift size={ICON_SIZE} />, group: GROUP_MINE },
  { path: ROUTES.NODES, label: '节点列表', icon: <Server size={ICON_SIZE} />, group: GROUP_USE },
  { path: ROUTES.TUTORIALS, label: '下载和使用', icon: <BookOpen size={ICON_SIZE} />, group: GROUP_USE },
  { path: ROUTES.SUBSCRIBE_LOGS, label: '订阅记录', icon: <History size={ICON_SIZE} />, group: GROUP_USE },
  { path: ROUTES.SHARED_ACCOUNTS, label: '共享账号', icon: <KeyRound size={ICON_SIZE} />, group: GROUP_USE, isVisible: (site) => site.sharedEnabled },
  { path: ROUTES.TICKETS, label: '工单支持', icon: <LifeBuoy size={ICON_SIZE} />, group: GROUP_USE, isVisible: (site) => site.ticketEnabled },
  { path: ROUTES.CHAIN, label: '链式代理', icon: <GitFork size={ICON_SIZE} />, group: GROUP_USE, isVisible: (_site, features) => features.clientChain },
]

/** 会话里没有 features 时（旧版后端）按全部关闭处理。 */
export const NO_FEATURES: UserFeatures = { clientChain: false, customNode: false }

/** 按站点设置与套餐功能过滤菜单表，返回新数组。 */
export function visibleMenu(site: SiteInfo, features: UserFeatures = NO_FEATURES): MenuEntry[] {
  return MENU.filter((entry) => entry.isVisible === undefined || entry.isVisible(site, features))
}
