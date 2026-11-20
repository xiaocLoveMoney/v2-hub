import { BookOpen, Gift, GitFork, History, KeyRound, LayoutDashboard, LifeBuoy, Megaphone, Server, ShoppingBag, UserRound, Wallet } from 'lucide-react'
import type { ReactNode } from 'react'
import type { SiteInfo, UserFeatures } from './api/client'
import type { MessageKey } from './i18n/i18n-context'
import { applyMenuOrder } from './menu-order'

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
  TUTORIAL_DETAIL: '/tutorials/:id',
  SUBSCRIBE_LOGS: '/subscribe-logs',
  SHARED_ACCOUNTS: '/shared-accounts',
  TICKETS: '/tickets',
  TICKET_DETAIL: '/tickets/:id',
  CHAIN: '/chain',
} as const

const ICON_SIZE = 18

/** 一个菜单项。 */
export interface MenuEntry {
  /** 菜单键（docs/03 §5.6 菜单键目录），菜单顺序设置按它排（Ruling-57）。 */
  key: string
  path: string
  /** 菜单文字的词条键，渲染时再翻译。 */
  labelKey: MessageKey
  icon: ReactNode
  /** 所属分组标题的词条键，未设置的放在顶层。 */
  group?: MessageKey
  /** 按站点设置与当前套餐开的功能决定是否显示；未设置的总是显示（docs/06 §3 不变式 1）。 */
  isVisible?: (site: SiteInfo, features: UserFeatures) => boolean
}

const GROUP_MINE: MessageKey = 'menu.groupMine'
const GROUP_USE: MessageKey = 'menu.groupUse'

/** 左侧菜单表（docs/06 §5，默认顺序）：增减菜单只改这张表，并同步 docs/03 §5.6 菜单键目录与管理端的客户门户菜单目录。 */
export const MENU: readonly MenuEntry[] = [
  { key: 'dashboard', path: ROUTES.DASHBOARD, labelKey: 'menu.dashboard', icon: <LayoutDashboard size={ICON_SIZE} /> },
  { key: 'shop', path: ROUTES.SHOP, labelKey: 'menu.shop', icon: <ShoppingBag size={ICON_SIZE} />, isVisible: (site) => site.shopEnabled === true },
  { key: 'announcements', path: ROUTES.ANNOUNCEMENTS, labelKey: 'menu.announcements', icon: <Megaphone size={ICON_SIZE} /> },
  { key: 'account', path: ROUTES.ACCOUNT, labelKey: 'menu.account', icon: <UserRound size={ICON_SIZE} />, group: GROUP_MINE },
  { key: 'wallet', path: ROUTES.WALLET, labelKey: 'menu.wallet', icon: <Wallet size={ICON_SIZE} />, group: GROUP_MINE, isVisible: (site) => site.shopEnabled === true },
  { key: 'invite', path: ROUTES.INVITE, labelKey: 'menu.invite', icon: <Gift size={ICON_SIZE} />, group: GROUP_MINE },
  { key: 'nodes', path: ROUTES.NODES, labelKey: 'menu.nodes', icon: <Server size={ICON_SIZE} />, group: GROUP_USE },
  { key: 'tutorials', path: ROUTES.TUTORIALS, labelKey: 'menu.tutorials', icon: <BookOpen size={ICON_SIZE} />, group: GROUP_USE },
  { key: 'subscribe-logs', path: ROUTES.SUBSCRIBE_LOGS, labelKey: 'menu.subscribeLogs', icon: <History size={ICON_SIZE} />, group: GROUP_USE },
  { key: 'shared-accounts', path: ROUTES.SHARED_ACCOUNTS, labelKey: 'menu.sharedAccounts', icon: <KeyRound size={ICON_SIZE} />, group: GROUP_USE, isVisible: (site) => site.sharedEnabled },
  { key: 'tickets', path: ROUTES.TICKETS, labelKey: 'menu.tickets', icon: <LifeBuoy size={ICON_SIZE} />, group: GROUP_USE, isVisible: (site) => site.ticketEnabled },
  { key: 'chain', path: ROUTES.CHAIN, labelKey: 'menu.chain', icon: <GitFork size={ICON_SIZE} />, group: GROUP_USE, isVisible: (_site, features) => features.clientChain || features.serverChain },
]

/** 会话里没有 features 时（旧版后端）按全部关闭处理。 */
export const NO_FEATURES: UserFeatures = { clientChain: false, customNode: false, serverChain: false }

/** 按站点信息里的菜单顺序排好（Ruling-57），再按站点设置与套餐功能过滤菜单表，返回新数组；没有顺序时按默认顺序。 */
export function visibleMenu(site: SiteInfo, features: UserFeatures = NO_FEATURES): MenuEntry[] {
  return applyMenuOrder(MENU, site.menuOrder ?? []).filter((entry) => entry.isVisible === undefined || entry.isVisible(site, features))
}
