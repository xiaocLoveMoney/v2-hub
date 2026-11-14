import {
  AccountBookOutlined,
  AuditOutlined,
  BarChartOutlined,
  BookOutlined,
  BranchesOutlined,
  ClusterOutlined,
  CreditCardOutlined,
  CustomerServiceOutlined,
  DashboardOutlined,
  DeploymentUnitOutlined,
  ExportOutlined,
  GiftOutlined,
  HistoryOutlined,
  KeyOutlined,
  LockOutlined,
  MailOutlined,
  NotificationOutlined,
  OrderedListOutlined,
  PayCircleOutlined,
  PercentageOutlined,
  ProfileOutlined,
  RocketOutlined,
  SafetyOutlined,
  SettingOutlined,
  ShoppingCartOutlined,
  TagsOutlined,
  TeamOutlined,
  UserOutlined,
} from '@ant-design/icons'
import type { ReactNode } from 'react'
import { applyMenuOrder } from './menu-order'

/** 前端路由路径常量。 */
export const ROUTES = {
  LOGIN: '/login',
  DASHBOARD: '/',
  ANNOUNCEMENTS: '/announcements',
  TUTORIALS: '/tutorials',
  SHARED_ACCOUNTS: '/shared-accounts',
  TICKETS: '/tickets',
  SERVERS: '/servers',
  SERVER_DETAIL: '/servers/:id',
  NODES: '/nodes',
  CHAIN_EXITS: '/chain-exits',
  CHAIN_ROUTES: '/chain-routes',
  USERS: '/users',
  PLANS: '/plans',
  INVITE_CODES: '/invite-codes',
  SUBSCRIBE_LOGS: '/subscribe-logs',
  TRAFFIC_PACKS: '/shop/traffic-packs',
  COUPONS: '/shop/coupons',
  TOPUP_CODES: '/shop/topup-codes',
  ORDERS: '/shop/orders',
  WALLET_LEDGER: '/shop/ledger',
  PAYMENTS: '/shop/payments',
  REBATES: '/shop/rebates',
  FINANCE_STATS: '/shop/stats',
  ADMINS: '/system/admins',
  ROLES: '/system/roles',
  AUDIT_LOGS: '/system/audit-logs',
  SETTINGS: '/system/settings',
  MENU_ORDER: '/system/menu-order',
  MAIL_OUTBOX: '/system/mail-outbox',
  SECURITY: '/account/security',
} as const

/** 权限码，与 docs/03 §4 权限码表一致。 */
export const PERMISSIONS = {
  ADMIN_READ: 'admin:read',
  ADMIN_WRITE: 'admin:write',
  AUDIT_READ: 'audit:read',
  SERVER_READ: 'server:read',
  SERVER_WRITE: 'server:write',
  NODE_READ: 'node:read',
  NODE_WRITE: 'node:write',
  USER_READ: 'user:read',
  USER_WRITE: 'user:write',
  PLAN_READ: 'plan:read',
  PLAN_WRITE: 'plan:write',
  SETTING_READ: 'setting:read',
  SETTING_WRITE: 'setting:write',
  CONTENT_READ: 'content:read',
  CONTENT_WRITE: 'content:write',
  TICKET_READ: 'ticket:read',
  TICKET_WRITE: 'ticket:write',
  SHOP_READ: 'shop:read',
  SHOP_WRITE: 'shop:write',
  FINANCE_READ: 'finance:read',
  FINANCE_WRITE: 'finance:write',
  IMPERSONATE_WRITE: 'impersonate:write',
} as const

/** 一个菜单项；permission 为空表示登录即可见。 */
export interface MenuEntry {
  /** 菜单键（docs/03 §5.6 菜单键目录），菜单顺序设置按它排（Ruling-57）。 */
  key: string
  path: string
  label: string
  icon: ReactNode
  permission?: string
  /** 所属分组标题，未设置的放在顶层。 */
  group?: string
}

// 分组照 docs/05：我的 / 节点 / 用户 / 交易 / 系统
const GROUP_MINE = '我的'
const GROUP_NODES = '节点'
const GROUP_USERS = '用户'
const GROUP_SHOP = '交易'
const GROUP_SYSTEM = '系统'

/** 左侧菜单表（默认顺序）：增减菜单只改这张表并同步 docs/03 §5.6 菜单键目录，菜单显示与路由守卫都以它为准。 */
export const MENU: readonly MenuEntry[] = [
  { key: 'dashboard', path: ROUTES.DASHBOARD, label: '总览', icon: <DashboardOutlined />, group: GROUP_MINE },
  { key: 'announcements', path: ROUTES.ANNOUNCEMENTS, label: '公告', icon: <NotificationOutlined />, permission: PERMISSIONS.CONTENT_READ, group: GROUP_MINE },
  { key: 'tutorials', path: ROUTES.TUTORIALS, label: '教程', icon: <BookOutlined />, permission: PERMISSIONS.CONTENT_READ, group: GROUP_MINE },
  { key: 'shared-accounts', path: ROUTES.SHARED_ACCOUNTS, label: '共享账号', icon: <KeyOutlined />, permission: PERMISSIONS.CONTENT_READ, group: GROUP_MINE },
  { key: 'tickets', path: ROUTES.TICKETS, label: '工单', icon: <CustomerServiceOutlined />, permission: PERMISSIONS.TICKET_READ, group: GROUP_MINE },
  { key: 'servers', path: ROUTES.SERVERS, label: '服务器', icon: <ClusterOutlined />, permission: PERMISSIONS.SERVER_READ, group: GROUP_NODES },
  { key: 'nodes', path: ROUTES.NODES, label: '节点', icon: <DeploymentUnitOutlined />, permission: PERMISSIONS.NODE_READ, group: GROUP_NODES },
  { key: 'chain-exits', path: ROUTES.CHAIN_EXITS, label: '链式出口', icon: <ExportOutlined />, permission: PERMISSIONS.NODE_READ, group: GROUP_NODES },
  { key: 'chain-routes', path: ROUTES.CHAIN_ROUTES, label: '链式规则', icon: <BranchesOutlined />, permission: PERMISSIONS.SERVER_READ, group: GROUP_NODES },
  { key: 'users', path: ROUTES.USERS, label: '用户', icon: <UserOutlined />, permission: PERMISSIONS.USER_READ, group: GROUP_USERS },
  { key: 'plans', path: ROUTES.PLANS, label: '套餐', icon: <ProfileOutlined />, permission: PERMISSIONS.PLAN_READ, group: GROUP_USERS },
  { key: 'invite-codes', path: ROUTES.INVITE_CODES, label: '邀请码', icon: <GiftOutlined />, permission: PERMISSIONS.USER_READ, group: GROUP_USERS },
  { key: 'subscribe-logs', path: ROUTES.SUBSCRIBE_LOGS, label: '订阅记录', icon: <HistoryOutlined />, permission: PERMISSIONS.USER_READ, group: GROUP_USERS },
  { key: 'traffic-packs', path: ROUTES.TRAFFIC_PACKS, label: '流量包', icon: <RocketOutlined />, permission: PERMISSIONS.SHOP_READ, group: GROUP_SHOP },
  { key: 'coupons', path: ROUTES.COUPONS, label: '优惠码', icon: <TagsOutlined />, permission: PERMISSIONS.SHOP_READ, group: GROUP_SHOP },
  { key: 'topup-codes', path: ROUTES.TOPUP_CODES, label: '充值码', icon: <CreditCardOutlined />, permission: PERMISSIONS.FINANCE_READ, group: GROUP_SHOP },
  { key: 'orders', path: ROUTES.ORDERS, label: '订单', icon: <ShoppingCartOutlined />, permission: PERMISSIONS.FINANCE_READ, group: GROUP_SHOP },
  { key: 'wallet-ledger', path: ROUTES.WALLET_LEDGER, label: '余额流水', icon: <AccountBookOutlined />, permission: PERMISSIONS.FINANCE_READ, group: GROUP_SHOP },
  { key: 'payments', path: ROUTES.PAYMENTS, label: '充值单', icon: <PayCircleOutlined />, permission: PERMISSIONS.FINANCE_READ, group: GROUP_SHOP },
  { key: 'rebates', path: ROUTES.REBATES, label: '返利', icon: <PercentageOutlined />, permission: PERMISSIONS.FINANCE_READ, group: GROUP_SHOP },
  { key: 'finance-stats', path: ROUTES.FINANCE_STATS, label: '收支统计', icon: <BarChartOutlined />, permission: PERMISSIONS.FINANCE_READ, group: GROUP_SHOP },
  { key: 'admins', path: ROUTES.ADMINS, label: '管理员', icon: <TeamOutlined />, permission: PERMISSIONS.ADMIN_READ, group: GROUP_SYSTEM },
  { key: 'roles', path: ROUTES.ROLES, label: '角色与权限', icon: <SafetyOutlined />, permission: PERMISSIONS.ADMIN_READ, group: GROUP_SYSTEM },
  { key: 'audit-logs', path: ROUTES.AUDIT_LOGS, label: '审计日志', icon: <AuditOutlined />, permission: PERMISSIONS.AUDIT_READ, group: GROUP_SYSTEM },
  { key: 'settings', path: ROUTES.SETTINGS, label: '系统设置', icon: <SettingOutlined />, permission: PERMISSIONS.SETTING_READ, group: GROUP_SYSTEM },
  { key: 'menu-order', path: ROUTES.MENU_ORDER, label: '菜单顺序', icon: <OrderedListOutlined />, permission: PERMISSIONS.SETTING_READ, group: GROUP_SYSTEM },
  { key: 'mail-outbox', path: ROUTES.MAIL_OUTBOX, label: '发件记录', icon: <MailOutlined />, permission: PERMISSIONS.SETTING_READ, group: GROUP_SYSTEM },
  { key: 'security', path: ROUTES.SECURITY, label: '账号安全', icon: <LockOutlined />, group: GROUP_SYSTEM },
]

/** 按菜单顺序设置排好（Ruling-57）、再按权限过滤菜单表，返回新数组；order 为空即默认顺序。 */
export function visibleMenu(can: (permission: string) => boolean, order: readonly string[] = []): MenuEntry[] {
  return applyMenuOrder(MENU, order).filter((entry) => entry.permission === undefined || can(entry.permission))
}
