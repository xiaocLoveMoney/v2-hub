import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { api, registerTotpSetupHandler, setCsrfToken, toApiError, type ApiError, type Session } from '../api/client'
import { SessionContext, type SessionContextValue, type SessionStatus } from './session-context'

interface SessionProviderProps {
  children: ReactNode
}

/** 持有当前管理员会话：启动时调 /me 恢复，登录 / 登出时更新，并同步 CSRF token；接口要求超管先开启两步验证时刷新会话。 */
export function SessionProvider({ children }: SessionProviderProps) {
  const [status, setStatus] = useState<SessionStatus>('loading')
  const [session, setSession] = useState<Session | null>(null)

  const apply = useCallback((next: Session | null) => {
    setCsrfToken(next?.csrfToken ?? '')
    setSession(next)
    setStatus(next === null ? 'anonymous' : 'authenticated')
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    const restore = async () => {
      try {
        const { data } = await api.GET('/api/admin/auth/me', { signal: controller.signal })
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
        const { data, error } = await api.POST('/api/admin/auth/login', {
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
      await api.POST('/api/admin/auth/logout', { params: { header: { 'X-CSRF-Token': session?.csrfToken ?? '' } } })
    } catch (err) {
      reportError(err)
    } finally {
      // 无论服务端是否成功，前端都回到未登录态；服务端会话最迟按空闲期过期
      apply(null)
    }
  }, [apply, session])

  const refresh = useCallback(async () => {
    try {
      const { data } = await api.GET('/api/admin/auth/me')
      apply(data?.data ?? null)
    } catch (err) {
      reportError(err)
    }
  }, [apply])

  // 接口回 totp_setup_required 时刷新会话，布局据 totpSetupRequired 停到账号安全页（Ruling-98）；并发的多次只刷新一次
  const isRefreshingRef = useRef(false)
  useEffect(
    () =>
      registerTotpSetupHandler(() => {
        if (isRefreshingRef.current) {
          return
        }
        isRefreshingRef.current = true
        void refresh().finally(() => {
          isRefreshingRef.current = false
        })
      }),
    [refresh],
  )

  const value = useMemo<SessionContextValue>(() => {
    const granted = new Set(session?.permissions ?? [])
    return { status, session, login, logout, refresh, can: (permission: string) => granted.has(permission) }
  }, [status, session, login, logout, refresh])

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>
}
