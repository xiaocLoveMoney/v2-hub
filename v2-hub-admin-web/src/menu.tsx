import {
  AuditOutlined,
  ClusterOutlined,
  DashboardOutlined,
  DeploymentUnitOutlined,
  LockOutlined,
  SafetyOutlined,
  TeamOutlined,
} from '@ant-design/icons'
import type { ReactNode } from 'react'

/** 前端路由路径常量。 */
export const ROUTES = {
  LOGIN: '/login',
  DASHBOARD: '/',
  SERVERS: '/servers',
  NODES: '/nodes',
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
  NODE_READ: 'node:read',
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

const GROUP_SYSTEM = '系统'
const GROUP_ACCOUNT = '账号'

/** 左侧菜单表：增减菜单只改这张表，菜单显示与路由守卫都以它为准。 */
export const MENU: readonly MenuEntry[] = [
  { path: ROUTES.DASHBOARD, label: '总览', icon: <DashboardOutlined /> },
  { path: ROUTES.SERVERS, label: '服务器', icon: <ClusterOutlined />, permission: PERMISSIONS.SERVER_READ },
  { path: ROUTES.NODES, label: '节点', icon: <DeploymentUnitOutlined />, permission: PERMISSIONS.NODE_READ },
  { path: ROUTES.ADMINS, label: '管理员', icon: <TeamOutlined />, permission: PERMISSIONS.ADMIN_READ, group: GROUP_SYSTEM },
  { path: ROUTES.ROLES, label: '角色与权限', icon: <SafetyOutlined />, permission: PERMISSIONS.ADMIN_READ, group: GROUP_SYSTEM },
  { path: ROUTES.AUDIT_LOGS, label: '审计日志', icon: <AuditOutlined />, permission: PERMISSIONS.AUDIT_READ, group: GROUP_SYSTEM },
  { path: ROUTES.SECURITY, label: '账号安全', icon: <LockOutlined />, group: GROUP_ACCOUNT },
]

/** 按权限过滤菜单表，返回新数组。 */
export function visibleMenu(can: (permission: string) => boolean): MenuEntry[] {
  return MENU.filter((entry) => entry.permission === undefined || can(entry.permission))
}
