import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { api, setCsrfToken, toApiError, type ApiError, type UserSession } from '../api/client'
import { SessionContext, type SessionContextValue, type SessionStatus } from './session-context'

interface SessionProviderProps {
  children: ReactNode
}

/** 持有当前用户会话：启动时调 /me 恢复，登录 / 登出时更新，并同步 CSRF token。 */
export function SessionProvider({ children }: SessionProviderProps) {
  const [status, setStatus] = useState<SessionStatus>('loading')
  const [session, setSession] = useState<UserSession | null>(null)

  const apply = useCallback((next: UserSession | null) => {
    setCsrfToken(next?.csrfToken ?? '')
    setSession(next)
    setStatus(next === null ? 'anonymous' : 'authenticated')
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    const restore = async () => {
      try {
        const { data } = await api.GET('/api/user/auth/me', { signal: controller.signal })
        apply(data?.data ?? null)
      } catch (err) {
        if (controller.signal.aborted) {
          return
        }
        // 网络错误按未登录处理，登录页会再次报出具体错误
        apply(null)
        reportError(err)
      }
    }
    void restore()
    return () => controller.abort()
  }, [apply])

  const login = useCallback(
    async (username: string, credential: string, totpCode?: string): Promise<ApiError | null> => {
      const pw = credential
      try {
        const { data, error } = await api.POST('/api/user/auth/login', {
          body: { username, password: pw, ...(totpCode === undefined ? {} : { totpCode }) },
        })
        if (error !== undefined || data?.data === undefined) {
          return toApiError(error)
        }
        apply(data.data)
        return null
      } catch (err) {
        reportError(err)
        return toApiError(err)
      }
    },
    [apply],
  )

  const logout = useCallback(async () => {
    try {
      await api.POST('/api/user/auth/logout', { params: { header: { 'X-CSRF-Token': session?.csrfToken ?? '' } } })
    } catch (err) {
      reportError(err)
    } finally {
      // 无论服务端是否成功，前端都回到未登录态；服务端会话最迟按空闲期过期
      apply(null)
    }
  }, [apply, session])

  const refresh = useCallback(async () => {
    try {
      const { data } = await api.GET('/api/user/auth/me')
      apply(data?.data ?? null)
    } catch (err) {
      reportError(err)
    }
  }, [apply])

  const adopt = useCallback((next: UserSession) => apply(next), [apply])
  const clear = useCallback(() => apply(null), [apply])

  const value = useMemo<SessionContextValue>(
    () => ({ status, session, login, logout, refresh, adopt, clear }),
    [status, session, login, logout, refresh, adopt, clear],
  )

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>
}
