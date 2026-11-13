import {
  AuditOutlined,
  ClusterOutlined,
  DashboardOutlined,
  DeploymentUnitOutlined,
  HistoryOutlined,
  LockOutlined,
  ProfileOutlined,
  SafetyOutlined,
  TeamOutlined,
  UserOutlined,
} from '@ant-design/icons'
import type { ReactNode } from 'react'

/** 前端路由路径常量。 */
export const ROUTES = {
  LOGIN: '/login',
  DASHBOARD: '/',
  SERVERS: '/servers',
  SERVER_DETAIL: '/servers/:id',
  NODES: '/nodes',
  USERS: '/users',
  PLANS: '/plans',
  SUBSCRIBE_LOGS: '/subscribe-logs',
  ADMINS: '/system/admins',
  ROLES: '/system/roles',
  AUDIT_LOGS: '/system/audit-logs',
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
} as const

/** 一个菜单项；permission 为空表示登录即可见。 */
export interface MenuEntry {
  path: string
  label: string
  icon: ReactNode
  permission?: string
  /** 所属分组标题，未设置的放在顶层。 */
  group?: string
}

// 分组照 docs/05：我的 / 节点 / 用户 / 系统，交易分组随 M5 加入
const GROUP_MINE = '我的'
const GROUP_NODES = '节点'
const GROUP_USERS = '用户'
const GROUP_SYSTEM = '系统'

/** 左侧菜单表：增减菜单只改这张表，菜单显示与路由守卫都以它为准。 */
export const MENU: readonly MenuEntry[] = [
  { path: ROUTES.DASHBOARD, label: '总览', icon: <DashboardOutlined />, group: GROUP_MINE },
  { path: ROUTES.SERVERS, label: '服务器', icon: <ClusterOutlined />, permission: PERMISSIONS.SERVER_READ, group: GROUP_NODES },
  { path: ROUTES.NODES, label: '节点', icon: <DeploymentUnitOutlined />, permission: PERMISSIONS.NODE_READ, group: GROUP_NODES },
  { path: ROUTES.USERS, label: '用户', icon: <UserOutlined />, permission: PERMISSIONS.USER_READ, group: GROUP_USERS },
  { path: ROUTES.PLANS, label: '套餐', icon: <ProfileOutlined />, permission: PERMISSIONS.PLAN_READ, group: GROUP_USERS },
  { path: ROUTES.SUBSCRIBE_LOGS, label: '订阅记录', icon: <HistoryOutlined />, permission: PERMISSIONS.USER_READ, group: GROUP_USERS },
  { path: ROUTES.ADMINS, label: '管理员', icon: <TeamOutlined />, permission: PERMISSIONS.ADMIN_READ, group: GROUP_SYSTEM },
  { path: ROUTES.ROLES, label: '角色与权限', icon: <SafetyOutlined />, permission: PERMISSIONS.ADMIN_READ, group: GROUP_SYSTEM },
  { path: ROUTES.AUDIT_LOGS, label: '审计日志', icon: <AuditOutlined />, permission: PERMISSIONS.AUDIT_READ, group: GROUP_SYSTEM },
  { path: ROUTES.SECURITY, label: '账号安全', icon: <LockOutlined />, group: GROUP_SYSTEM },
]

/** 按权限过滤菜单表，返回新数组。 */
export function visibleMenu(can: (permission: string) => boolean): MenuEntry[] {
  return MENU.filter((entry) => entry.permission === undefined || can(entry.permission))
}
