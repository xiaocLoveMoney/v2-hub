import { Spin } from 'antd'
import type { ReactNode } from 'react'
import { Navigate, useLocation } from 'react-router'
import type { AdminKind } from '../api/client'
import { useSession } from '../auth/session-context'
import { ROUTES } from '../menu'

interface SessionGuardProps {
  /** 这个布局服务的账号类型。 */
  kind: AdminKind
  /** 账号类型不对时渲染的内容（403 页或跳转）；不渲染 children、不发页面的请求（Ruling-101）。 */
  wrongKind: ReactNode
  /** 已登录且账号类型正确时渲染的外壳与页面。 */
  children: ReactNode
}

/**
 * 员工与代理两个布局共用的会话守卫，依次判断：加载中 → 未登录跳登录页（记下原路径）→
 * 超管未开启两步验证时只能停在账号安全页（Ruling-98）→ 账号类型不对显示 wrongKind（Ruling-101）→ 渲染 children。
 */
export function SessionGuard({ kind, wrongKind, children }: SessionGuardProps) {
  const { status, session } = useSession()
  const location = useLocation()

  if (status === 'loading') {
    return <Spin fullscreen />
  }
  // 会话层保证 authenticated 时 session 不为空，这里一并收窄
  if (status === 'anonymous' || session === null) {
    return <Navigate to={ROUTES.LOGIN} replace state={{ from: location.pathname }} />
  }
  // 只针对员工账号（超管）；开启后刷新会话即恢复
  if (session.kind !== 'agent' && session.totpSetupRequired && location.pathname !== ROUTES.SECURITY) {
    return <Navigate to={ROUTES.SECURITY} replace />
  }
  if (session.kind !== kind) {
    return wrongKind
  }
  return children
}
