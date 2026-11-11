import { createContext, useContext } from 'react'
import type { ApiError, Session } from '../api/client'

/** 会话状态：loading 表示首次 /me 还没回来。 */
export type SessionStatus = 'loading' | 'anonymous' | 'authenticated'

export interface SessionContextValue {
  status: SessionStatus
  session: Session | null
  /** 登录；成功返回 null，失败返回可展示的错误。 */
  login: (username: string, credential: string) => Promise<ApiError | null>
  logout: () => Promise<void>
  /** 是否拥有权限码（只影响展示，鉴权以后端为准）。 */
  can: (permission: string) => boolean
}

export const SessionContext = createContext<SessionContextValue | null>(null)

/** 读取会话上下文，必须在 SessionProvider 内使用。 */
export function useSession(): SessionContextValue {
  const ctx = useContext(SessionContext)
  if (ctx === null) {
    throw new Error('useSession must be used within SessionProvider')
  }
  return ctx
}
