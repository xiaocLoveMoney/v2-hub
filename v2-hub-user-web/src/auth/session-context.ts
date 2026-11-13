import { createContext, useContext } from 'react'
import type { ApiError, UserSession } from '../api/client'

/** 会话状态：loading 表示首次 /me 还没回来。 */
export type SessionStatus = 'loading' | 'anonymous' | 'authenticated'

export interface SessionContextValue {
  status: SessionStatus
  session: UserSession | null
  /** 登录；成功返回 null，失败返回可展示的错误（code 为 totp_required 时需补交验证码再调一次）。 */
  login: (username: string, credential: string, totpCode?: string) => Promise<ApiError | null>
  /** 直接采用接口返回的新会话（注册成功、换绑邮箱后），省一次 /me。 */
  adopt: (session: UserSession) => void
  /** 清掉本地会话而不调登出接口（注销账号后服务端会话已不存在）。 */
  clear: () => void
  /** 重新拉取 /me（如开启或关闭两步验证后）。 */
  refresh: () => Promise<void>
  logout: () => Promise<void>
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
